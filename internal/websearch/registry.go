package websearch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// Registry maintains process-wide shared web search clients (most notably the
// Tavily multi-key round-robin pool) so avatar audit, biography synthesis,
// alias completion, and AI review share rotation cursors and cooldown state.
type Registry struct {
	mu                sync.Mutex
	tavilyRing        *TavilyKeyRing
	tavilyFingerprint string
	tavilyBaseURL     string
	deepseekBaseURL   string
	httpClient        *http.Client
	now               func() time.Time
}

func NewRegistry() *Registry {
	return &Registry{
		tavilyBaseURL:   tavilyDefaultBaseURL,
		deepseekBaseURL: "https://api.deepseek.com",
		now:             time.Now,
	}
}

// SetEndpointsForTest overrides upstream URLs and optional HTTP client/clock in tests.
func (reg *Registry) SetEndpointsForTest(tavilyBase, deepseekBase string, client *http.Client, now func() time.Time) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if tavilyBase != "" {
		reg.tavilyBaseURL = strings.TrimRight(tavilyBase, "/")
	}
	if deepseekBase != "" {
		reg.deepseekBaseURL = strings.TrimRight(deepseekBase, "/")
	}
	reg.httpClient = client
	if now != nil {
		reg.now = now
	}
	reg.tavilyRing = nil
	reg.tavilyFingerprint = ""
}

func (reg *Registry) getOrCreateTavilyRing(ws storage.WebSearchSettings, ai storage.AISettings) (*TavilyKeyRing, error) {
	keys := storage.DedupeAPIKeys(ws.TavilyKeys)
	if len(keys) == 0 {
		return nil, errors.New("未配置 Tavily API Key")
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	fp := tavilyConfigFingerprint(reg.tavilyBaseURL, ai.ProxyURL, keys)
	if reg.tavilyRing != nil && reg.tavilyFingerprint == fp {
		return reg.tavilyRing, nil
	}
	httpClient := reg.httpClient
	if httpClient == nil {
		c, err := tagging.NewChatClient(reg.tavilyBaseURL, keys[0], ai.ProxyURL, 30*time.Second)
		if err != nil {
			return nil, err
		}
		httpClient = c.HTTP
	}
	ring, err := NewTavilyKeyRing(keys, httpClient, reg.tavilyBaseURL, reg.now)
	if err != nil {
		return nil, err
	}
	reg.tavilyRing = ring
	reg.tavilyFingerprint = fp
	return ring, nil
}

func (reg *Registry) getDeepSeekClient(ws storage.WebSearchSettings, alias storage.ArtistAliasSettings, ai storage.AISettings) (*DeepSeekClient, error) {
	reg.mu.Lock()
	baseURL := reg.deepseekBaseURL
	httpClient := reg.httpClient
	reg.mu.Unlock()
	if httpClient != nil {
		key := ws.EffectiveDeepSeekKey(alias, ai)
		if key == "" {
			return nil, errors.New("未配置 DeepSeek 联网 API Key")
		}
		return &DeepSeekClient{HTTP: httpClient, Key: key, BaseURL: strings.TrimRight(baseURL, "/")}, nil
	}
	return newDeepSeekClientWithBase(ws, alias, ai, baseURL)
}

// Client returns a configured web search router honoring ws.Provider.
func (reg *Registry) Client(ws storage.WebSearchSettings, alias storage.ArtistAliasSettings, ai storage.AISettings) (Client, error) {
	provider := storage.NormalizeWebSearchProvider(ws.Provider)
	switch provider {
	case storage.WebSearchProviderTavily:
		ring, err := reg.getOrCreateTavilyRing(ws, ai)
		if err != nil {
			return nil, err
		}
		return &RouterClient{primary: ring, primaryName: "Tavily"}, nil
	case storage.WebSearchProviderDeepSeek:
		ds, err := reg.getDeepSeekClient(ws, alias, ai)
		if err != nil {
			return nil, err
		}
		return &RouterClient{primary: ds, primaryName: "DeepSeek"}, nil
	default:
		ring, tavErr := reg.getOrCreateTavilyRing(ws, ai)
		ds, dsErr := reg.getDeepSeekClient(ws, alias, ai)
		if tavErr == nil && dsErr == nil {
			return &RouterClient{primary: ring, primaryName: "Tavily", fallback: ds, fallbackName: "DeepSeek"}, nil
		}
		if tavErr == nil {
			return &RouterClient{primary: ring, primaryName: "Tavily"}, nil
		}
		if dsErr == nil {
			return &RouterClient{primary: ds, primaryName: "DeepSeek"}, nil
		}
		return nil, errors.New("未配置可用的联网搜索服务（请配置 Tavily 或 DeepSeek 联网密钥）")
	}
}

// ClientFromStore loads current settings from store and constructs a shared router.
func (reg *Registry) ClientFromStore(ctx context.Context, store *storage.Store) (Client, error) {
	ws, err := store.WebSearchSettings(ctx)
	if err != nil {
		return nil, err
	}
	alias, err := store.ArtistAliasSettings(ctx)
	if err != nil {
		return nil, err
	}
	ai, err := store.AISettings(ctx)
	if err != nil {
		return nil, err
	}
	return reg.Client(ws, alias, ai)
}

// TavilyStatuses returns masked status rows for all configured Tavily keys.
func (reg *Registry) TavilyStatuses(ws storage.WebSearchSettings, ai storage.AISettings) []KeyStatus {
	keys := storage.DedupeAPIKeys(ws.TavilyKeys)
	if len(keys) == 0 {
		return nil
	}
	ring, err := reg.getOrCreateTavilyRing(ws, ai)
	if err != nil {
		out := make([]KeyStatus, len(keys))
		for i, k := range keys {
			out[i] = KeyStatus{Index: i + 1, Mask: storage.MaskAPIKey(k), State: KeyStateHealthy}
		}
		return out
	}
	return ring.Statuses()
}

// TestConnection performs a controlled search probe and returns a redacted summary.
func (reg *Registry) TestConnection(ctx context.Context, ws storage.WebSearchSettings, alias storage.ArtistAliasSettings, ai storage.AISettings) (string, error) {
	client, err := reg.Client(ws, alias, ai)
	if err != nil {
		return "", err
	}
	results, err := client.Search(ctx, "musicbrainz")
	if err != nil {
		return "", err
	}
	router, _ := client.(*RouterClient)
	label := "联网搜索"
	if router != nil && router.lastUsed != "" {
		label = router.lastUsed
	}
	if label == "Tavily" {
		statuses := reg.TavilyStatuses(ws, ai)
		healthy := 0
		for _, st := range statuses {
			if st.State == KeyStateHealthy {
				healthy++
			}
		}
		return fmt.Sprintf("Tavily 正常（共 %d 把 Key，当前可用 %d 把，返回 %d 条结果）", len(statuses), healthy, len(results)), nil
	}
	return fmt.Sprintf("%s 正常（返回 %d 条结果）", label, len(results)), nil
}

// RouterClient dispatches Search/Fetch to the primary provider and optionally
// falls back to a secondary provider on provider-level failures.
type RouterClient struct {
	primary      Client
	primaryName  string
	fallback     Client
	fallbackName string
	lastUsed     string
}

func (c *RouterClient) Search(ctx context.Context, query string) ([]SearchResult, error) {
	res, err := c.primary.Search(ctx, query)
	if err == nil {
		c.lastUsed = c.primaryName
		return res, nil
	}
	if ctx.Err() != nil || c.fallback == nil {
		return nil, err
	}
	fbRes, fbErr := c.fallback.Search(ctx, query)
	if fbErr == nil {
		c.lastUsed = c.fallbackName
		return fbRes, nil
	}
	return nil, fbErr
}

func (c *RouterClient) Fetch(ctx context.Context, pageURL string) (string, error) {
	if !SafePageURL(pageURL) {
		return "", errors.New("目标网页地址不安全或无效")
	}
	body, err := c.primary.Fetch(ctx, pageURL)
	if err == nil {
		c.lastUsed = c.primaryName
		return body, nil
	}
	if ctx.Err() != nil || c.fallback == nil {
		return "", err
	}
	// Do not mask single-page 404 fetch misses when Tavily key itself is healthy,
	// unless fallback can actually extract it or primary pool failed.
	fbBody, fbErr := c.fallback.Fetch(ctx, pageURL)
	if fbErr == nil {
		c.lastUsed = c.fallbackName
		return fbBody, nil
	}
	return "", err
}

func tavilyConfigFingerprint(baseURL, proxyURL string, keys []string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(strings.TrimSpace(baseURL)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strings.TrimSpace(proxyURL)))
	for _, k := range keys {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(k))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
