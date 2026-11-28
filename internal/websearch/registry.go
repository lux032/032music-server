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

	"github.com/lux032/032music-server/internal/netfetch"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

const (
	nativeUnsupportedCooldown   = 10 * time.Minute
	nativeFallbackReserveBudget = 15 * time.Second
)

type backendKind string

const (
	backendNative   backendKind = "native"
	backendTavily   backendKind = "tavily"
	backendDeepSeek backendKind = "deepseek"
)

type namedBackend struct {
	name     string
	kind     backendKind
	client   Client
	nativeFP string
}

// Registry maintains process-wide shared web search clients (most notably the
// Tavily multi-key round-robin pool and native endpoint unsupported cooldown)
// so avatar audit, biography synthesis, alias completion, and AI review share
// rotation cursors, rate limits, and cooldown state.
type Registry struct {
	mu                     sync.Mutex
	tavilyRing             *TavilyKeyRing
	tavilyFingerprint      string
	tavilyBaseURL          string
	deepseekBaseURL        string
	nativeBaseURLOverride  string
	httpClient             *http.Client
	directFetchClient      *http.Client
	now                    func() time.Time
	rateWait               func(ctx context.Context, rpm int) error
	usageRecorder          func(ctx context.Context, model, purpose string, usage storage.AIUsage)
	nativeCooldownGen      uint64
	nativeUnsupportedKey   string
	nativeUnsupportedUntil time.Time
}

func NewRegistry() *Registry {
	return &Registry{
		tavilyBaseURL:   tavilyDefaultBaseURL,
		deepseekBaseURL: "https://api.deepseek.com",
		now:             time.Now,
	}
}

// SetRateWait wires a shared RPM limiter callback for native LLM web search.
func (reg *Registry) SetRateWait(fn func(ctx context.Context, rpm int) error) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.rateWait = fn
}

// SetUsageRecorder wires global AI usage accounting for native LLM web search.
func (reg *Registry) SetUsageRecorder(fn func(ctx context.Context, model, purpose string, usage storage.AIUsage)) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.usageRecorder = fn
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

// SetNativeEndpointsForTest overrides the native /v1/responses endpoint and
// optional HTTP/direct-fetch clients in unit tests.
func (reg *Registry) SetNativeEndpointsForTest(nativeBase string, client, directFetchClient *http.Client) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.nativeBaseURLOverride = strings.TrimSpace(nativeBase)
	if client != nil {
		reg.httpClient = client
	}
	reg.directFetchClient = directFetchClient
	reg.nativeCooldownGen++
	reg.nativeUnsupportedKey = ""
	reg.nativeUnsupportedUntil = time.Time{}
}

func (reg *Registry) isNativeUnsupported(fp string) bool {
	if fp == "" {
		return false
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	nowFn := reg.now
	if nowFn == nil {
		nowFn = time.Now
	}
	if reg.nativeUnsupportedKey != fp {
		return false
	}
	if !reg.nativeUnsupportedUntil.After(nowFn()) {
		reg.nativeUnsupportedKey = ""
		reg.nativeUnsupportedUntil = time.Time{}
		return false
	}
	return true
}

func (reg *Registry) markNativeUnsupported(fp string, expectedGen uint64) {
	if fp == "" {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	// Ignore stale writes from requests that began before a settings save or
	// manual ResetNativeCooldown.
	if reg.nativeCooldownGen != expectedGen {
		return
	}
	nowFn := reg.now
	if nowFn == nil {
		nowFn = time.Now
	}
	reg.nativeUnsupportedKey = fp
	reg.nativeUnsupportedUntil = nowFn().Add(nativeUnsupportedCooldown)
}

// ResetNativeCooldown clears any cached unsupported-endpoint cooldown and
// advances the generation counter so in-flight requests from prior settings
// cannot reapply stale cooldown state.
func (reg *Registry) ResetNativeCooldown() {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	reg.nativeCooldownGen++
	reg.nativeUnsupportedKey = ""
	reg.nativeUnsupportedUntil = time.Time{}
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

func (reg *Registry) getNativeClient(ai storage.AISettings) (*NativeClient, string, uint64, error) {
	reg.mu.Lock()
	overrideBase := reg.nativeBaseURLOverride
	httpClient := reg.httpClient
	nowFn := reg.now
	rateWait := reg.rateWait
	usageRec := reg.usageRecorder
	gen := reg.nativeCooldownGen
	reg.mu.Unlock()

	baseURL := strings.TrimSpace(ai.BaseURL)
	if overrideBase != "" {
		baseURL = overrideBase
	}
	model := strings.TrimSpace(ai.Model)
	key := strings.TrimSpace(ai.APIKey)
	if baseURL == "" || model == "" || key == "" {
		return nil, "", gen, errors.New("未配置全局 AI 模型服务")
	}
	responsesURL := normalizeResponsesURL(baseURL)
	if !SafePageURL(responsesURL) {
		return nil, "", gen, errors.New("全局 AI 接口地址无效")
	}
	if httpClient == nil {
		c, err := tagging.NewChatClient(baseURL, key, ai.ProxyURL, nativeStageTimeout)
		if err != nil {
			return nil, "", gen, err
		}
		httpClient = c.HTTP
	}
	fp := nativeConfigFingerprint(responsesURL, model, key, ai.ProxyURL)
	return &NativeClient{
		HTTP:          httpClient,
		ResponsesURL:  responsesURL,
		APIKey:        key,
		Model:         model,
		RPM:           ai.RequestsPerMinute,
		Now:           nowFn,
		RateWait:      rateWait,
		UsageRecorder: usageRec,
	}, fp, gen, nil
}

// Client returns a configured web search router honoring ws.Provider.
func (reg *Registry) Client(ws storage.WebSearchSettings, alias storage.ArtistAliasSettings, ai storage.AISettings) (Client, error) {
	return reg.buildRouter(ws, alias, ai, false)
}

func (reg *Registry) buildRouter(ws storage.WebSearchSettings, alias storage.ArtistAliasSettings, ai storage.AISettings, isolateCooldown bool) (*RouterClient, error) {
	reg.mu.Lock()
	directClient := reg.directFetchClient
	gen := reg.nativeCooldownGen
	reg.mu.Unlock()

	provider := storage.NormalizeWebSearchProvider(ws.Provider)
	switch provider {
	case storage.WebSearchProviderNativeThenSearch:
		var backends []namedBackend
		if native, fp, nGen, err := reg.getNativeClient(ai); err == nil {
			gen = nGen
			backends = append(backends, namedBackend{
				name:     "模型原生搜索",
				kind:     backendNative,
				client:   native,
				nativeFP: fp,
			})
		}
		if ring, err := reg.getOrCreateTavilyRing(ws, ai); err == nil {
			backends = append(backends, namedBackend{
				name:   "Tavily",
				kind:   backendTavily,
				client: ring,
			})
		}
		if ds, err := reg.getDeepSeekClient(ws, alias, ai); err == nil {
			backends = append(backends, namedBackend{
				name:   "DeepSeek",
				kind:   backendDeepSeek,
				client: ds,
			})
		}
		if len(backends) == 0 {
			return nil, errors.New("未配置可用的联网搜索服务（请先配置全局 AI 模型或 Tavily / DeepSeek 联网密钥）")
		}
		return &RouterClient{
			reg:               reg,
			backends:          backends,
			useLayeredFetch:   true,
			directFetchClient: directClient,
			proxyURL:          strings.TrimSpace(ai.ProxyURL),
			cooldownGen:       gen,
			isolateCooldown:   isolateCooldown,
		}, nil
	case storage.WebSearchProviderTavily:
		ring, err := reg.getOrCreateTavilyRing(ws, ai)
		if err != nil {
			return nil, err
		}
		return &RouterClient{
			reg:      reg,
			backends: []namedBackend{{name: "Tavily", kind: backendTavily, client: ring}},
		}, nil
	case storage.WebSearchProviderDeepSeek:
		ds, err := reg.getDeepSeekClient(ws, alias, ai)
		if err != nil {
			return nil, err
		}
		return &RouterClient{
			reg:      reg,
			backends: []namedBackend{{name: "DeepSeek", kind: backendDeepSeek, client: ds}},
		}, nil
	default:
		var backends []namedBackend
		if ring, err := reg.getOrCreateTavilyRing(ws, ai); err == nil {
			backends = append(backends, namedBackend{name: "Tavily", kind: backendTavily, client: ring})
		}
		if ds, err := reg.getDeepSeekClient(ws, alias, ai); err == nil {
			backends = append(backends, namedBackend{name: "DeepSeek", kind: backendDeepSeek, client: ds})
		}
		if len(backends) == 0 {
			return nil, errors.New("未配置可用的联网搜索服务（请配置 Tavily 或 DeepSeek 联网密钥）")
		}
		return &RouterClient{reg: reg, backends: backends}, nil
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
// It resets any cached native unsupported cooldown and runs the probe in
// cooldown-isolated mode so draft settings never pollute production cooldown state.
func (reg *Registry) TestConnection(ctx context.Context, ws storage.WebSearchSettings, alias storage.ArtistAliasSettings, ai storage.AISettings) (string, error) {
	reg.ResetNativeCooldown()
	router, err := reg.buildRouter(ws, alias, ai, true)
	if err != nil {
		return "", err
	}
	results, err := router.Search(ctx, "musicbrainz")
	if err != nil {
		return "", err
	}
	provider := storage.NormalizeWebSearchProvider(ws.Provider)
	if provider == storage.WebSearchProviderNativeThenSearch && len(results) == 0 {
		return "", errors.New("联网搜索未返回任何有效来源结果")
	}
	label := router.LastUsed()
	if label == "" {
		label = "联网搜索"
	}
	if label == "模型原生搜索" {
		model := strings.TrimSpace(ai.Model)
		if model != "" {
			return fmt.Sprintf("模型原生搜索正常（%s，返回 %d 条结果）", model, len(results)), nil
		}
		return fmt.Sprintf("模型原生搜索正常（返回 %d 条结果）", len(results)), nil
	}
	prefix := ""
	if provider == storage.WebSearchProviderNativeThenSearch {
		prefix = "原生搜索不可用，已自动回退 "
	}
	if label == "Tavily" {
		statuses := reg.TavilyStatuses(ws, ai)
		healthy := 0
		for _, st := range statuses {
			if st.State == KeyStateHealthy {
				healthy++
			}
		}
		return fmt.Sprintf("%sTavily 正常（共 %d 把 Key，当前可用 %d 把，返回 %d 条结果）", prefix, len(statuses), healthy, len(results)), nil
	}
	return fmt.Sprintf("%s%s 正常（返回 %d 条结果）", prefix, label, len(results)), nil
}

// RouterClient dispatches Search/Fetch across an ordered chain of backends with
// automatic failover on errors or zero verified results.
type RouterClient struct {
	reg               *Registry
	backends          []namedBackend
	useLayeredFetch   bool
	directFetchClient *http.Client
	proxyURL          string
	cooldownGen       uint64
	isolateCooldown   bool

	mu       sync.Mutex
	lastUsed string
}

func (c *RouterClient) setLastUsed(name string) {
	c.mu.Lock()
	c.lastUsed = name
	c.mu.Unlock()
}

func (c *RouterClient) LastUsed() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastUsed
}

// nativeCallContext reserves fallback time when the parent context has a
// deadline and fallback backends remain after Native.
func nativeCallContext(ctx context.Context, hasFallback bool) (context.Context, context.CancelFunc) {
	if !hasFallback {
		return ctx, func() {}
	}
	dl, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	rem := time.Until(dl)
	if rem <= nativeFallbackReserveBudget+5*time.Second {
		return ctx, func() {}
	}
	budget := rem - nativeFallbackReserveBudget
	if budget > nativeStageTimeout {
		budget = nativeStageTimeout
	}
	return context.WithTimeout(ctx, budget)
}

func errorPriority(err error) int {
	if err == nil {
		return 0
	}
	var we *WebError
	if errors.As(err, &we) {
		switch we.Status {
		case http.StatusTooManyRequests:
			return 5
		case http.StatusUnauthorized, http.StatusPaymentRequired, 432, 433:
			return 4
		case http.StatusForbidden, http.StatusNotFound:
			return 3
		default:
			return 2
		}
	}
	return 1
}

func pickActionableError(current, candidate error) error {
	if candidate == nil {
		return current
	}
	if current == nil {
		return candidate
	}
	if errorPriority(candidate) > errorPriority(current) {
		return candidate
	}
	return current
}

func (c *RouterClient) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var bestErr error
	var sawEmpty bool
	var lastEmptyName string

	for i, b := range c.backends {
		hasRemaining := i < len(c.backends)-1
		if b.kind == backendNative && hasRemaining && !c.isolateCooldown && c.reg != nil && c.reg.isNativeUnsupported(b.nativeFP) {
			continue
		}
		callCtx := ctx
		var cancel context.CancelFunc
		if b.kind == backendNative {
			callCtx, cancel = nativeCallContext(ctx, hasRemaining)
		}
		res, err := b.client.Search(callCtx, query)
		if cancel != nil {
			cancel()
		}
		if err == nil {
			if len(res) > 0 {
				c.setLastUsed(b.name)
				return res, nil
			}
			sawEmpty = true
			lastEmptyName = b.name
			if hasRemaining {
				continue
			}
			c.setLastUsed(b.name)
			return res, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if b.kind == backendNative && !c.isolateCooldown && isNativeUnsupportedError(err) && c.reg != nil {
			c.reg.markNativeUnsupported(b.nativeFP, c.cooldownGen)
		}
		bestErr = pickActionableError(bestErr, err)
	}
	if sawEmpty {
		c.setLastUsed(lastEmptyName)
		return nil, nil
	}
	if bestErr != nil {
		return nil, bestErr
	}
	return nil, errors.New("未配置可用的联网搜索服务")
}

func (c *RouterClient) Fetch(ctx context.Context, pageURL string) (string, error) {
	if !SafePageURL(pageURL) {
		return "", errors.New("目标网页地址不安全或无效")
	}
	// Reject literal private/loopback/link-local/localhost targets immediately
	// before calling any direct fetcher or external extractor.
	if err := netfetch.ValidateURLNotLiteralPrivate(pageURL); err != nil {
		return "", err
	}

	if !c.useLayeredFetch {
		var bestErr error
		for _, b := range c.backends {
			body, err := b.client.Fetch(ctx, pageURL)
			if err == nil && strings.TrimSpace(body) != "" {
				c.setLastUsed(b.name)
				return body, nil
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if err == nil {
				err = &WebError{Provider: b.name, Status: http.StatusNotFound, Operation: "fetch"}
			}
			bestErr = pickActionableError(bestErr, err)
		}
		return "", bestErr
	}

	// Native-first Fetch layering:
	// 1. Direct SSRF-safe public HTTP GET + HTML text cleaning (preserves raw
	//    verbatim webpage text for alias/review quote grounding).
	directClient := c.directFetchClient
	if directClient == nil && c.proxyURL != "" {
		pc, err := netfetch.NewPublicClientWithProxy(c.proxyURL, 15*time.Second)
		if err != nil {
			return "", fmt.Errorf("代理地址无效：%w", err)
		}
		directClient = pc
	}
	var bestErr error
	if body, err := netfetch.FetchPageText(ctx, directClient, pageURL, ""); err == nil && strings.TrimSpace(body) != "" {
		c.setLastUsed("网页直连")
		return body, nil
	} else if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// Never forward a destination blocked by SSRF / redirect policy to
		// external extractors or native open_page.
		if errors.Is(err, netfetch.ErrBlockedDestination) {
			return "", err
		}
		bestErr = pickActionableError(bestErr, err)
	}

	// 2. Fallback to configured non-native extractors (Tavily /extract, DeepSeek /fetch).
	hasExternal := false
	for _, b := range c.backends {
		if b.kind == backendNative {
			continue
		}
		hasExternal = true
		body, err := b.client.Fetch(ctx, pageURL)
		if err == nil && strings.TrimSpace(body) != "" {
			c.setLastUsed(b.name)
			return body, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err == nil {
			err = &WebError{Provider: b.name, Status: http.StatusNotFound, Operation: "fetch"}
		}
		bestErr = pickActionableError(bestErr, err)
	}

	// 3. Final fallback: native /v1/responses open_page extraction.
	for _, b := range c.backends {
		if b.kind != backendNative {
			continue
		}
		if hasExternal && !c.isolateCooldown && c.reg != nil && c.reg.isNativeUnsupported(b.nativeFP) {
			continue
		}
		body, err := b.client.Fetch(ctx, pageURL)
		if err == nil && strings.TrimSpace(body) != "" {
			c.setLastUsed(b.name)
			return body, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil && !c.isolateCooldown && isNativeUnsupportedError(err) && c.reg != nil {
			c.reg.markNativeUnsupported(b.nativeFP, c.cooldownGen)
		}
		bestErr = pickActionableError(bestErr, err)
	}
	if bestErr != nil {
		return "", bestErr
	}
	return "", &WebError{Provider: "Native", Status: http.StatusNotFound, Operation: "fetch"}
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

func nativeConfigFingerprint(responsesURL, model, apiKey, proxyURL string) string {
	h := sha256.New()
	for _, part := range []string{
		strings.TrimSpace(responsesURL),
		strings.TrimSpace(model),
		strings.TrimSpace(apiKey),
		strings.TrimSpace(proxyURL),
	} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}
