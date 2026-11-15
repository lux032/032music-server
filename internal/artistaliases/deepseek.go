package artistaliases

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Errors deliberately omit response bodies, URLs and provider text that could
// reflect a submitted credential or private proxy information.
type WebError struct{ Status int }

func (e *WebError) Error() string {
	return fmt.Sprintf("DeepSeek 联网请求失败（HTTP %d）", e.Status)
}
func (c *DeepSeekWeb) call(ctx context.Context, path string, body any, out any) error {
	raw, e := json.Marshal(body)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if e != nil {
		return fmt.Errorf("DeepSeek 请求地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	client := *c.HTTP
	// Do not forward credentials through redirects, even on the same host.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("DeepSeek 联网连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &WebError{resp.StatusCode}
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if e != nil || len(data) > 2<<20 {
		return fmt.Errorf("DeepSeek 网页内容读取失败或过大")
	}
	if e = json.Unmarshal(data, out); e != nil {
		return fmt.Errorf("DeepSeek 联网响应格式无效")
	}
	return nil
}
func (c *DeepSeekWeb) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var v struct {
		Results []SearchResult `json:"results"`
	}
	e := c.call(ctx, "/v1/tools/search", map[string]any{"query": query, "count": 5}, &v)
	return v.Results, e
}
func (c *DeepSeekWeb) Fetch(ctx context.Context, url string) (string, error) {
	var v struct {
		Content string `json:"content"`
	}
	e := c.call(ctx, "/v1/tools/fetch", map[string]any{"url": url}, &v)
	return v.Content, e
}
