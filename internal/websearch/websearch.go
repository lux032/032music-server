// Package websearch provides a provider-agnostic web search and page-fetch
// contract shared by artist alias completion and AI biography synthesis.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// SearchResult is a single web search hit returned by a search provider.
type SearchResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Client abstracts web search and single-page content extraction so callers
// remain provider-independent and deterministic in tests.
type Client interface {
	Search(ctx context.Context, query string) ([]SearchResult, error)
	Fetch(ctx context.Context, pageURL string) (string, error)
}

// WebError omits response bodies, URLs, and credentials from error strings.
type WebError struct {
	Status    int
	Operation string
}

func (e *WebError) Error() string {
	label := "联网请求"
	if e.Operation == "search" {
		label = "网页搜索"
	} else if e.Operation == "fetch" {
		label = "网页读取"
	}
	return fmt.Sprintf("DeepSeek %s失败（HTTP %d）", label, e.Status)
}

// DeepSeekClient calls DeepSeek's /v1/tools/search and /v1/tools/fetch endpoints.
type DeepSeekClient struct {
	HTTP    *http.Client
	Key     string
	BaseURL string
}

// NewDeepSeekClient constructs a DeepSeek web client reusing the configured
// alias DeepSeek key or falling back to the global AI key when BaseURL points
// to the official api.deepseek.com endpoint.
func NewDeepSeekClient(aliasSettings storage.ArtistAliasSettings, aiSettings storage.AISettings) (Client, error) {
	key := strings.TrimSpace(aliasSettings.DeepSeekKey)
	u, _ := url.Parse(strings.TrimSpace(aiSettings.BaseURL))
	if key == "" && u != nil && u.Hostname() == "api.deepseek.com" && u.Scheme == "https" {
		key = strings.TrimSpace(aiSettings.APIKey)
	}
	if key == "" {
		return nil, errors.New("未配置 DeepSeek 联网 API Key")
	}
	c, err := tagging.NewChatClient("https://api.deepseek.com", key, aiSettings.ProxyURL, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &DeepSeekClient{HTTP: c.HTTP, Key: key, BaseURL: "https://api.deepseek.com"}, nil
}

func (c *DeepSeekClient) call(ctx context.Context, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("DeepSeek 请求地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("DeepSeek 联网连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		operation := "search"
		if strings.HasSuffix(path, "/fetch") {
			operation = "fetch"
		}
		return &WebError{Status: resp.StatusCode, Operation: operation}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return fmt.Errorf("DeepSeek 网页内容读取失败或过大")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("DeepSeek 联网响应格式无效")
	}
	return nil
}

func (c *DeepSeekClient) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var v struct {
		Results []SearchResult `json:"results"`
	}
	err := c.call(ctx, "/v1/tools/search", map[string]any{"query": query, "count": 5, "need_content": true}, &v)
	return v.Results, err
}

func (c *DeepSeekClient) Fetch(ctx context.Context, pageURL string) (string, error) {
	var v struct {
		Content string `json:"content"`
	}
	err := c.call(ctx, "/v1/tools/fetch", map[string]any{"url": pageURL}, &v)
	return v.Content, err
}

// SafePageURL validates that a fetched/referenced URL is a clean http(s) URL
// without embedded credentials.
func SafePageURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}
