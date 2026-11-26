package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

const (
	KeyStateHealthy        = "healthy"
	KeyStateCooldown       = "cooldown"
	KeyStateQuotaExhausted = "quota_exhausted"
	KeyStateInvalid        = "invalid"

	tavilyDefaultBaseURL      = "https://api.tavily.com"
	tavilyRateLimitCooldown   = 60 * time.Second
	tavilyServerErrorCooldown = 30 * time.Second
	tavilyQuotaCooldown       = 6 * time.Hour
	tavilyMaxRetryAfter       = 10 * time.Minute
)

// KeyStatus exposes masked, non-sensitive health information for one key in
// the Tavily round-robin pool.
type KeyStatus struct {
	Index         int    `json:"index"`
	Mask          string `json:"mask"`
	State         string `json:"state"`
	LastStatus    int    `json:"lastStatus,omitempty"`
	CooldownUntil string `json:"cooldownUntil,omitempty"`
}

type tavilyKeyEntry struct {
	key           string
	mask          string
	state         string
	lastStatus    int
	cooldownUntil time.Time
}

// TavilyKeyRing manages multiple Tavily API keys with process-wide
// round-robin selection and automatic per-key cooldown/failover.
type TavilyKeyRing struct {
	mu      sync.Mutex
	entries []tavilyKeyEntry
	cursor  int
	baseURL string
	http    *http.Client
	now     func() time.Time
}

func NewTavilyKeyRing(keys []string, httpClient *http.Client, baseURL string, now func() time.Time) (*TavilyKeyRing, error) {
	cleaned := storage.DedupeAPIKeys(keys)
	if len(cleaned) == 0 {
		return nil, errors.New("未配置 Tavily API Key")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = tavilyDefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	entries := make([]tavilyKeyEntry, len(cleaned))
	for i, k := range cleaned {
		entries[i] = tavilyKeyEntry{
			key:   k,
			mask:  storage.MaskAPIKey(k),
			state: KeyStateHealthy,
		}
	}
	return &TavilyKeyRing{
		entries: entries,
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		http:    httpClient,
		now:     now,
	}, nil
}

func (r *TavilyKeyRing) Statuses() []KeyStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := make([]KeyStatus, len(r.entries))
	for i := range r.entries {
		e := &r.entries[i]
		r.refreshEntryLocked(e, now)
		st := KeyStatus{
			Index:      i + 1,
			Mask:       e.mask,
			State:      e.state,
			LastStatus: e.lastStatus,
		}
		if !e.cooldownUntil.IsZero() && e.cooldownUntil.After(now) {
			st.CooldownUntil = e.cooldownUntil.UTC().Format(time.RFC3339)
		}
		out[i] = st
	}
	return out
}

func (r *TavilyKeyRing) refreshEntryLocked(e *tavilyKeyEntry, now time.Time) {
	if (e.state == KeyStateCooldown || e.state == KeyStateQuotaExhausted) && !e.cooldownUntil.IsZero() && !now.Before(e.cooldownUntil) {
		e.state = KeyStateHealthy
		e.cooldownUntil = time.Time{}
	}
}

// acquireCandidate picks the next healthy key in round-robin order that has
// not yet been tried in this call. It advances r.cursor immediately so
// concurrent and consecutive calls spread evenly across all healthy keys.
func (r *TavilyKeyRing) acquireCandidate(tried []bool) (idx int, key string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(r.entries)
	now := r.now()
	for offset := 0; offset < n; offset++ {
		i := (r.cursor + offset) % n
		if tried[i] {
			continue
		}
		e := &r.entries[i]
		r.refreshEntryLocked(e, now)
		if e.state == KeyStateHealthy {
			r.cursor = (i + 1) % n
			return i, e.key, true
		}
	}
	return -1, "", false
}

func (r *TavilyKeyRing) markKeyFailure(idx int, state string, status int, cooldown time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx < 0 || idx >= len(r.entries) {
		return
	}
	e := &r.entries[idx]
	e.state = state
	e.lastStatus = status
	if cooldown > 0 {
		e.cooldownUntil = r.now().Add(cooldown)
	} else {
		e.cooldownUntil = time.Time{}
	}
}

func (r *TavilyKeyRing) poolExhaustedError(op string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	var soonest time.Duration
	hasCooldown := false
	hasQuota := false
	hasInvalid := false
	for i := range r.entries {
		e := &r.entries[i]
		r.refreshEntryLocked(e, now)
		switch e.state {
		case KeyStateCooldown:
			hasCooldown = true
			if e.cooldownUntil.After(now) {
				rem := e.cooldownUntil.Sub(now)
				if soonest == 0 || rem < soonest {
					soonest = rem
				}
			}
		case KeyStateQuotaExhausted:
			hasQuota = true
		case KeyStateInvalid:
			hasInvalid = true
		}
	}
	if hasCooldown {
		if soonest <= 0 {
			soonest = tavilyRateLimitCooldown
		}
		return &WebError{Provider: "Tavily", Status: http.StatusTooManyRequests, Operation: op, RetryAfter: soonest}
	}
	if hasQuota {
		return &WebError{Provider: "Tavily", Status: 432, Operation: op}
	}
	if hasInvalid {
		return &WebError{Provider: "Tavily", Status: http.StatusUnauthorized, Operation: op}
	}
	return &WebError{Provider: "Tavily", Status: http.StatusServiceUnavailable, Operation: op}
}

func (r *TavilyKeyRing) Search(ctx context.Context, query string) ([]SearchResult, error) {
	var resp struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
	}
	body := map[string]any{
		"query":               query,
		"max_results":         5,
		"search_depth":        "basic",
		"include_raw_content": false,
	}
	if err := r.doWithFailover(ctx, "/search", "search", body, &resp); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Results))
	for _, item := range resp.Results {
		if !SafePageURL(item.URL) {
			continue
		}
		out = append(out, SearchResult{
			URL:     strings.TrimSpace(item.URL),
			Title:   strings.TrimSpace(item.Title),
			Content: strings.TrimSpace(item.Content),
		})
	}
	return out, nil
}

func (r *TavilyKeyRing) Fetch(ctx context.Context, pageURL string) (string, error) {
	if !SafePageURL(pageURL) {
		return "", errors.New("目标网页地址不安全或无效")
	}
	var resp struct {
		Results []struct {
			URL        string `json:"url"`
			RawContent string `json:"raw_content"`
			Content    string `json:"content"`
		} `json:"results"`
	}
	body := map[string]any{
		"urls": []string{strings.TrimSpace(pageURL)},
	}
	if err := r.doWithFailover(ctx, "/extract", "fetch", body, &resp); err != nil {
		return "", err
	}
	for _, item := range resp.Results {
		if text := strings.TrimSpace(item.RawContent); text != "" {
			return text, nil
		}
		if text := strings.TrimSpace(item.Content); text != "" {
			return text, nil
		}
	}
	// Target page yielded no extractable text; treat as single-page fetch miss
	// without penalizing the healthy Tavily key.
	return "", &WebError{Provider: "Tavily", Status: http.StatusNotFound, Operation: "fetch"}
}

func (r *TavilyKeyRing) doWithFailover(ctx context.Context, path, op string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	tried := make([]bool, len(r.entries))
	for {
		idx, key, ok := r.acquireCandidate(tried)
		if !ok {
			return r.poolExhaustedError(op)
		}
		tried[idx] = true

		status, retryAfter, callErr := r.doSingle(ctx, path, op, key, raw, out)
		if callErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch {
		case status == http.StatusTooManyRequests:
			d := retryAfter
			if d <= 0 {
				d = tavilyRateLimitCooldown
			}
			r.markKeyFailure(idx, KeyStateCooldown, status, d)
			continue
		case status == http.StatusPaymentRequired || status == 432 || status == 433:
			r.markKeyFailure(idx, KeyStateQuotaExhausted, status, tavilyQuotaCooldown)
			continue
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			r.markKeyFailure(idx, KeyStateInvalid, status, 0)
			continue
		case status >= 500 || status == 0:
			// Transient server or network error: short cooldown and try next key.
			r.markKeyFailure(idx, KeyStateCooldown, status, tavilyServerErrorCooldown)
			continue
		default:
			// Non-key client error (e.g. 400/404/422 on extract target URL): do not
			// penalize key and do not burn remaining keys.
			return callErr
		}
	}
}

func (r *TavilyKeyRing) doSingle(ctx context.Context, path, op, key string, rawBody []byte, out any) (status int, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+path, bytes.NewReader(rawBody))
	if err != nil {
		return 400, 0, errors.New("Tavily 请求地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	client := *r.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		return 0, 0, errors.New("Tavily 联网连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		ra := parseRetryAfterHeader(resp.Header.Get("Retry-After"), r.now())
		return resp.StatusCode, ra, &WebError{Provider: "Tavily", Status: resp.StatusCode, Operation: op, RetryAfter: ra}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return 0, 0, errors.New("Tavily 响应读取失败或过大")
	}
	if err = json.Unmarshal(data, out); err != nil {
		return 0, 0, errors.New("Tavily 联网响应格式无效")
	}
	return http.StatusOK, 0, nil
}

func parseRetryAfterHeader(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		d := time.Duration(secs) * time.Second
		if d > tavilyMaxRetryAfter {
			return tavilyMaxRetryAfter
		}
		return d
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			if d > tavilyMaxRetryAfter {
				return tavilyMaxRetryAfter
			}
			return d
		}
	}
	return 0
}
