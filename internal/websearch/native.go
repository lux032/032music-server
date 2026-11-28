package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/storage"
)

const (
	nativeStageTimeout          = 60 * time.Second
	nativeSearchMaxOutputTokens = 2000
	nativeFetchMaxOutputTokens  = 3000
	nativeSearchContextSize     = "medium"
	nativeMaxResults            = 5
	nativeUsagePurpose          = "web_search_native"
)

const nativeSearchSystemPrompt = `You are a web search adapter. Use the web_search_preview tool to search the web for the user's query and return up to 5 real, relevant web results as strict JSON matching {"results":[{"url":"...","title":"...","content":"..."}]}.

Rules:
1. You MUST invoke web_search_preview; never answer from memory alone.
2. Every url MUST be copied verbatim from the actual search results returned by the tool. Never fabricate, guess, or alter URLs.
3. For content, preserve concise factual excerpts directly supported by the retrieved page or snippet (prefer exact key phrases/quotes from the page when available).
4. Keep results ordered by relevance to the user's query. If no relevant web page is found, return {"results":[]}.`

const nativeFetchSystemPrompt = `You are a web page text extractor. Use the web_search_preview tool to open and read the exact URL provided by the user, then output the page's main factual text in clean plain text.

Rules:
1. You MUST open or inspect the exact URL via web_search_preview; never fabricate page contents from memory.
2. Preserve factual details, names, dates, and exact phrasing faithfully without commentary or Markdown wrappers.
3. If the URL cannot be opened or contains no readable content, output an empty string.`

var nativeSearchResultSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["results"],"properties":{"results":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["url","title","content"],"properties":{"url":{"type":"string"},"title":{"type":"string"},"content":{"type":"string"}}}}}}`)

var (
	reNativeThinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)
	trackingQueryKeys  = map[string]bool{
		"fbclid":  true,
		"gclid":   true,
		"dclid":   true,
		"msclkid": true,
		"mc_cid":  true,
		"mc_eid":  true,
		"igshid":  true,
		"yclid":   true,
		"spm":     true,
		"ref":     true,
		"ref_src": true,
		"uo":      true,
		"si":      true,
	}
)

// NativeClient calls OpenAI's /v1/responses endpoint with web_search_preview,
// reusing global AISettings (base URL, API key, model, proxy, RPM limiter) and
// verifying returned URLs against server-side action.sources / url_citation annotations.
type NativeClient struct {
	HTTP          *http.Client
	ResponsesURL  string
	APIKey        string
	Model         string
	RPM           int
	Now           func() time.Time
	RateWait      func(ctx context.Context, rpm int) error
	UsageRecorder func(ctx context.Context, model, purpose string, usage storage.AIUsage)
}

type nativeUnsupportedError struct {
	err error
}

func (e *nativeUnsupportedError) Error() string { return e.err.Error() }
func (e *nativeUnsupportedError) Unwrap() error { return e.err }

func isNativeUnsupportedError(err error) bool {
	var ue *nativeUnsupportedError
	return errors.As(err, &ue)
}

type nativeBadRequestError struct {
	code    string
	param   string
	message string
}

func (e *nativeBadRequestError) Error() string {
	return "模型原生联网 网页请求失败（HTTP 400）"
}

func (e *nativeBadRequestError) mentions(substr string) bool {
	s := strings.ToLower(substr)
	return strings.Contains(e.param, s) || strings.Contains(e.message, s) || strings.Contains(e.code, s)
}

func (e *nativeBadRequestError) isNonExtensionClientError() bool {
	// Structured non-extension error codes/params always win before any
	// substring heuristic so credential/model/input/quota failures never retry.
	for _, kw := range []string{"model_not_found", "invalid_model", "context_length_exceeded", "invalid_api_key", "insufficient_quota", "billing_hard_limit"} {
		if strings.Contains(e.code, kw) || strings.Contains(e.message, kw) {
			return true
		}
	}
	return e.param == "model" || e.param == "input" || e.param == "instructions"
}

func (e *nativeBadRequestError) isUnsupportedTool() bool {
	if e.isNonExtensionClientError() || e.mentions("search_context_size") || e.mentions("include") {
		return false
	}
	if !(e.mentions("web_search_preview") || e.mentions("web_search")) {
		return false
	}
	for _, kw := range []string{"unsupported", "not supported", "invalid tool", "unknown tool", "unrecognized tool", "not allowed", "not available", "invalid value"} {
		if strings.Contains(e.message, kw) || strings.Contains(e.code, kw) {
			return true
		}
	}
	return false
}

// normalizeResponsesURL parses a global OpenAI-compatible base URL (such as
// https://host/v1, https://host/v1/chat/completions, or https://host/v1/responses),
// rejects fragments or embedded credentials, normalizes only u.Path to end in
// /responses, and preserves any gateway query parameters.
func normalizeResponsesURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	lower := strings.ToLower(p)
	switch {
	case strings.HasSuffix(lower, "/responses"):
		u.Path = p
	case strings.HasSuffix(lower, "/chat/completions"):
		u.Path = p[:len(p)-len("/chat/completions")] + "/responses"
	default:
		u.Path = p + "/responses"
	}
	u.RawPath = ""
	return u.String()
}

func (c *NativeClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *NativeClient) waitRateLimit(ctx context.Context) error {
	if c.RateWait == nil {
		return nil
	}
	return c.RateWait(ctx, c.RPM)
}

func (c *NativeClient) recordUsage(usage storage.AIUsage) {
	if c.UsageRecorder == nil || usage.Requests <= 0 {
		return
	}
	uCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.UsageRecorder(uCtx, strings.TrimSpace(c.Model), nativeUsagePurpose, usage)
}

// withStageBudget enforces a single bounded deadline across limiter waits and
// all HTTP compatibility retries inside one native Search/Fetch stage.
func withStageBudget(ctx context.Context, budget time.Duration) (context.Context, context.CancelFunc) {
	if budget <= 0 {
		return ctx, func() {}
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) <= budget {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, budget)
}

// Search executes native OpenAI /v1/responses web search and ALWAYS verifies
// returned URLs against server-side action.sources / open_page / url_citation
// annotations. Unverified or fabricated URLs are never accepted.
func (c *NativeClient) Search(ctx context.Context, query string) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	stageCtx, cancel := withStageBudget(ctx, nativeStageTimeout)
	defer cancel()

	rawJSON, err := c.sendSearchWithFallback(stageCtx, query)
	if err != nil {
		return nil, err
	}
	parsed, parseErr := parseNativeResponsesPayload(rawJSON)
	if parsed.hasUsage {
		c.recordUsage(parsed.usage)
	}
	if parseErr != nil {
		return nil, parseErr
	}

	if !parsed.sawCompletedSearch {
		return nil, errors.New("模型原生搜索未触发真实联网检索")
	}
	// If the server returned no verifiable source URLs at all (e.g. a gateway
	// rejected `include` and also emitted no url_citation annotations), return
	// zero results so RouterClient automatically falls back to Tavily/DeepSeek.
	if len(parsed.sourcePool) == 0 {
		return nil, nil
	}

	results, jsonOK := decodeNativeSearchResults(parsed.text)
	if jsonOK {
		return filterNativeSearchResults(results, parsed.sourcePool), nil
	}

	// Model output was not valid JSON, but server returned real retrieved URLs:
	// surface up to nativeMaxResults verified tool URLs (useful for avatar discovery).
	out := make([]SearchResult, 0, min(len(parsed.orderedSources), nativeMaxResults))
	for _, src := range parsed.orderedSources {
		if len(out) >= nativeMaxResults {
			break
		}
		out = append(out, SearchResult{
			URL:   src.cleanURL,
			Title: src.title,
		})
	}
	return out, nil
}

// Fetch uses OpenAI /v1/responses web_search_preview to open and extract text
// from a single public page URL (intended as a fallback after direct HTTP and
// external extractors). Requires an exact canonical target URL match in the
// server-side opened/retrieved tool actions.
func (c *NativeClient) Fetch(ctx context.Context, pageURL string) (string, error) {
	pageURL = strings.TrimSpace(pageURL)
	if !SafePageURL(pageURL) {
		return "", errors.New("目标网页地址不安全或无效")
	}
	stageCtx, cancel := withStageBudget(ctx, nativeStageTimeout)
	defer cancel()

	rawJSON, err := c.sendFetchWithFallback(stageCtx, pageURL)
	if err != nil {
		return "", err
	}
	parsed, parseErr := parseNativeResponsesPayload(rawJSON)
	if parsed.hasUsage {
		c.recordUsage(parsed.usage)
	}
	if parseErr != nil {
		return "", parseErr
	}

	if !parsed.sawCompletedSearch {
		return "", errors.New("模型原生网页读取未触发真实访问")
	}
	targetCanon := canonicalizeFetchURL(pageURL)
	if targetCanon == "" {
		return "", errors.New("目标网页地址不安全或无效")
	}
	// Require exact normalized target URL match in server-side tool actions/sources
	// (never accept an unrelated open_page URL or loose same-host path).
	grounded := parsed.openedFetchURLs[targetCanon]
	if !grounded {
		if _, ok := parsed.fetchSourceURLs[targetCanon]; ok {
			grounded = true
		}
	}
	if !grounded {
		return "", &WebError{Provider: "Native", Status: http.StatusNotFound, Operation: "fetch"}
	}
	text := strings.TrimSpace(reNativeThinkBlock.ReplaceAllString(parsed.text, ""))
	if utf8.RuneCountInString(text) < 40 {
		return "", &WebError{Provider: "Native", Status: http.StatusNotFound, Operation: "fetch"}
	}
	return text, nil
}

func (c *NativeClient) buildSearchBody(query string, includeCtx, includeSources, includeTextFormat bool) map[string]any {
	tool := map[string]any{"type": "web_search_preview"}
	if includeCtx {
		tool["search_context_size"] = nativeSearchContextSize
	}
	body := map[string]any{
		"model":             strings.TrimSpace(c.Model),
		"instructions":      nativeSearchSystemPrompt,
		"input":             query,
		"tools":             []any{tool},
		"max_output_tokens": nativeSearchMaxOutputTokens,
	}
	if includeSources {
		body["include"] = []string{"web_search_call.action.sources"}
	}
	if includeTextFormat {
		body["text"] = map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   "web_search_results",
				"strict": true,
				"schema": nativeSearchResultSchema,
			},
		}
	}
	return body
}

func (c *NativeClient) buildFetchBody(pageURL string, includeCtx, includeSources bool) map[string]any {
	tool := map[string]any{"type": "web_search_preview"}
	if includeCtx {
		tool["search_context_size"] = nativeSearchContextSize
	}
	body := map[string]any{
		"model":             strings.TrimSpace(c.Model),
		"instructions":      nativeFetchSystemPrompt,
		"input":             "Open and extract the main text of this exact URL: " + pageURL,
		"tools":             []any{tool},
		"max_output_tokens": nativeFetchMaxOutputTokens,
	}
	if includeSources {
		body["include"] = []string{"web_search_call.action.sources"}
	}
	return body
}

func (c *NativeClient) sendSearchWithFallback(ctx context.Context, query string) ([]byte, error) {
	includeCtx, includeSources, includeFormat := true, true, true
	// Up to 4 attempts: initial request + up to 3 single-extension removals
	// (include, search_context_size, text.format).
	for attempt := 0; attempt < 4; attempt++ {
		data, badReq, err := c.doResponsesRequest(ctx, "search", c.buildSearchBody(query, includeCtx, includeSources, includeFormat))
		if err == nil {
			return data, nil
		}
		if badReq == nil {
			return nil, err
		}
		if badReq.isNonExtensionClientError() {
			return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "search"}
		}
		if badReq.isUnsupportedTool() {
			return nil, &nativeUnsupportedError{err: &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "search"}}
		}
		if !includeCtx && !includeSources && !includeFormat {
			return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "search"}
		}

		nextCtx, nextSources, nextFormat := includeCtx, includeSources, includeFormat
		matchedSpecific := false
		if includeSources && badReq.mentions("include") {
			nextSources = false
			matchedSpecific = true
		}
		if includeCtx && badReq.mentions("search_context_size") {
			nextCtx = false
			matchedSpecific = true
		}
		if includeFormat && (badReq.mentions("json_schema") || badReq.mentions("text.format") || badReq.mentions("response_format") || badReq.param == "text") {
			nextFormat = false
			matchedSpecific = true
		}
		if !matchedSpecific {
			// Generic unrecognized 400: only perform a single compatibility downgrade
			// on the initial full-extension request.
			if attempt == 0 {
				nextCtx, nextSources, nextFormat = false, false, false
			} else {
				return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "search"}
			}
		}
		if nextCtx == includeCtx && nextSources == includeSources && nextFormat == includeFormat {
			return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "search"}
		}
		includeCtx, includeSources, includeFormat = nextCtx, nextSources, nextFormat
	}
	return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "search"}
}

func (c *NativeClient) sendFetchWithFallback(ctx context.Context, pageURL string) ([]byte, error) {
	includeCtx, includeSources := true, true
	for attempt := 0; attempt < 3; attempt++ {
		data, badReq, err := c.doResponsesRequest(ctx, "fetch", c.buildFetchBody(pageURL, includeCtx, includeSources))
		if err == nil {
			return data, nil
		}
		if badReq == nil {
			return nil, err
		}
		if badReq.isNonExtensionClientError() {
			return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "fetch"}
		}
		if badReq.isUnsupportedTool() {
			return nil, &nativeUnsupportedError{err: &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "fetch"}}
		}
		if !includeCtx && !includeSources {
			return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "fetch"}
		}
		nextCtx, nextSources := includeCtx, includeSources
		matchedSpecific := false
		if includeSources && badReq.mentions("include") {
			nextSources = false
			matchedSpecific = true
		}
		if includeCtx && badReq.mentions("search_context_size") {
			nextCtx = false
			matchedSpecific = true
		}
		if !matchedSpecific {
			if attempt == 0 {
				nextCtx, nextSources = false, false
			} else {
				return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "fetch"}
			}
		}
		if nextCtx == includeCtx && nextSources == includeSources {
			return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "fetch"}
		}
		includeCtx, includeSources = nextCtx, nextSources
	}
	return nil, &WebError{Provider: "Native", Status: http.StatusBadRequest, Operation: "fetch"}
}

func (c *NativeClient) doResponsesRequest(ctx context.Context, op string, body map[string]any) ([]byte, *nativeBadRequestError, error) {
	if !SafePageURL(c.ResponsesURL) {
		return nil, nil, errors.New("模型原生搜索接口地址无效")
	}
	if err := c.waitRateLimit(ctx); err != nil {
		return nil, nil, err
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ResponsesURL, bytes.NewReader(rawBody))
	if err != nil {
		return nil, nil, errors.New("模型原生搜索请求无效")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: nativeStageTimeout}
	}
	client := *httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("模型原生搜索连接失败")
	}
	defer resp.Body.Close()

	// Classify non-200/non-400 status codes directly from headers BEFORE reading
	// the response body so an oversized or slow error body never masks 404/429
	// cooldown or Retry-After headers.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, &nativeUnsupportedError{err: &WebError{Provider: "Native", Status: http.StatusNotFound, Operation: op}}
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		ra := parseRetryAfterHeader(resp.Header.Get("Retry-After"), c.now())
		return nil, nil, &WebError{Provider: "Native", Status: resp.StatusCode, Operation: op, RetryAfter: ra}
	}

	if resp.StatusCode == http.StatusBadRequest {
		errData, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		badReq := parseNativeBadRequest(errData)
		return nil, badReq, badReq
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil || len(data) > 4<<20 {
		return nil, nil, errors.New("模型原生搜索响应读取失败或过大")
	}
	return data, nil, nil
}

func parseNativeBadRequest(data []byte) *nativeBadRequestError {
	out := &nativeBadRequestError{}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Param   string `json:"param"`
			Message string `json:"message"`
		} `json:"error"`
		Code    string `json:"code"`
		Param   string `json:"param"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &envelope) == nil {
		if envelope.Error.Code != "" || envelope.Error.Param != "" || envelope.Error.Message != "" {
			out.code = strings.ToLower(strings.TrimSpace(envelope.Error.Code))
			out.param = strings.ToLower(strings.TrimSpace(envelope.Error.Param))
			out.message = strings.ToLower(strings.TrimSpace(envelope.Error.Message))
		} else {
			out.code = strings.ToLower(strings.TrimSpace(envelope.Code))
			out.param = strings.ToLower(strings.TrimSpace(envelope.Param))
			out.message = strings.ToLower(strings.TrimSpace(envelope.Message))
		}
	}
	return out
}

type nativeSourceEntry struct {
	cleanURL string
	title    string
	opened   bool
}

type parsedNativeResponse struct {
	text               string
	sawCompletedSearch bool
	sourcePool         map[string]nativeSourceEntry
	orderedSources     []nativeSourceEntry
	openedFetchURLs    map[string]bool
	fetchSourceURLs    map[string]bool
	usage              storage.AIUsage
	hasUsage           bool
}

func parseNativeResponsesPayload(data []byte) (parsedNativeResponse, error) {
	out := parsedNativeResponse{
		sourcePool:      map[string]nativeSourceEntry{},
		openedFetchURLs: map[string]bool{},
		fetchSourceURLs: map[string]bool{},
	}

	// Decode usage independently of output so valid token usage is still
	// accounted even if output has unexpected structure.
	var envelope struct {
		Output json.RawMessage `json:"output"`
		Usage  *struct {
			InputTokens        int64 `json:"input_tokens"`
			OutputTokens       int64 `json:"output_tokens"`
			PromptTokens       int64 `json:"prompt_tokens"`
			CompletionTokens   int64 `json:"completion_tokens"`
			InputTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			PromptTokensDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			OutputTokensDetails struct {
				ReasoningTokens int64 `json:"reasoning_tokens"`
			} `json:"output_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens int64 `json:"completion_tokens_details"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return out, errors.New("模型原生搜索响应格式无效")
	}
	if envelope.Usage != nil {
		prompt := envelope.Usage.InputTokens
		if prompt == 0 {
			prompt = envelope.Usage.PromptTokens
		}
		completion := envelope.Usage.OutputTokens
		if completion == 0 {
			completion = envelope.Usage.CompletionTokens
		}
		cached := envelope.Usage.InputTokensDetails.CachedTokens
		if cached == 0 {
			cached = envelope.Usage.PromptTokensDetails.CachedTokens
		}
		reasoning := envelope.Usage.OutputTokensDetails.ReasoningTokens
		if reasoning == 0 {
			reasoning = envelope.Usage.CompletionTokensDetails.ReasoningTokens
		}
		out.usage = storage.AIUsage{
			Requests:         1,
			PromptTokens:     max(0, prompt),
			CompletionTokens: max(0, completion),
			CachedTokens:     max(0, cached),
			ReasoningTokens:  max(0, reasoning),
		}
		out.hasUsage = true
	} else {
		out.usage = storage.AIUsage{Requests: 1}
		out.hasUsage = true
	}

	var outputBlocks []struct {
		Type   string `json:"type"`
		Role   string `json:"role"`
		Status string `json:"status"`
		Action *struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Sources []json.RawMessage `json:"sources"`
		} `json:"action"`
		Content []struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Annotations []struct {
				Type        string `json:"type"`
				URL         string `json:"url"`
				Title       string `json:"title"`
				URLCitation *struct {
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"url_citation"`
			} `json:"annotations"`
		} `json:"content"`
	}
	if len(envelope.Output) > 0 && string(bytes.TrimSpace(envelope.Output)) != "null" {
		if err := json.Unmarshal(envelope.Output, &outputBlocks); err != nil {
			return out, errors.New("模型原生搜索响应格式无效")
		}
	}

	addSource := func(rawURL, title string, opened bool) {
		clean := stripTrackingParams(rawURL)
		if !SafePageURL(clean) {
			return
		}
		canon := canonicalizeURL(clean)
		if canon == "" {
			return
		}
		if fetchCanon := canonicalizeFetchURL(clean); fetchCanon != "" {
			out.fetchSourceURLs[fetchCanon] = true
			if opened {
				out.openedFetchURLs[fetchCanon] = true
			}
		}
		title = strings.TrimSpace(title)
		if existing, ok := out.sourcePool[canon]; ok {
			if existing.title == "" && title != "" {
				existing.title = title
			}
			if opened {
				existing.opened = true
			}
			out.sourcePool[canon] = existing
			return
		}
		entry := nativeSourceEntry{cleanURL: clean, title: title, opened: opened}
		out.sourcePool[canon] = entry
		out.orderedSources = append(out.orderedSources, entry)
	}

	var textBuf strings.Builder
	for _, blk := range outputBlocks {
		bType := strings.ToLower(strings.TrimSpace(blk.Type))
		if bType == "web_search_call" || bType == "web_search" {
			st := strings.ToLower(strings.TrimSpace(blk.Status))
			if st != "" && st != "completed" {
				continue
			}
			out.sawCompletedSearch = true
			if blk.Action != nil {
				actType := strings.ToLower(strings.TrimSpace(blk.Action.Type))
				opened := actType == "open_page" || actType == "find_in_page"
				if strings.TrimSpace(blk.Action.URL) != "" {
					addSource(blk.Action.URL, "", opened)
				}
				for _, srcRaw := range blk.Action.Sources {
					var s string
					if json.Unmarshal(srcRaw, &s) == nil && strings.TrimSpace(s) != "" {
						addSource(s, "", false)
						continue
					}
					var obj struct {
						URL   string `json:"url"`
						Title string `json:"title"`
					}
					if json.Unmarshal(srcRaw, &obj) == nil && strings.TrimSpace(obj.URL) != "" {
						addSource(obj.URL, obj.Title, false)
					}
				}
			}
			continue
		}

		// Only read answer text and URL citations from assistant message blocks.
		role := strings.ToLower(strings.TrimSpace(blk.Role))
		if bType != "message" || (role != "" && role != "assistant") {
			continue
		}
		for _, c := range blk.Content {
			cType := strings.ToLower(strings.TrimSpace(c.Type))
			if cType != "" && cType != "output_text" && cType != "text" {
				continue
			}
			if c.Text != "" {
				textBuf.WriteString(c.Text)
			}
			for _, ann := range c.Annotations {
				annType := strings.ToLower(strings.TrimSpace(ann.Type))
				if ann.URLCitation != nil && strings.TrimSpace(ann.URLCitation.URL) != "" {
					addSource(ann.URLCitation.URL, ann.URLCitation.Title, false)
				} else if (annType == "" || annType == "url_citation") && strings.TrimSpace(ann.URL) != "" {
					addSource(ann.URL, ann.Title, false)
				}
			}
		}
	}
	out.text = strings.TrimSpace(textBuf.String())
	return out, nil
}

func decodeNativeSearchResults(text string) ([]SearchResult, bool) {
	cleaned := strings.TrimSpace(reNativeThinkBlock.ReplaceAllString(text, ""))
	start, end := strings.Index(cleaned, "{"), strings.LastIndex(cleaned, "}")
	if start < 0 || end <= start {
		return nil, false
	}
	var payload struct {
		Results []SearchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(cleaned[start:end+1]), &payload); err != nil {
		return nil, false
	}
	return payload.Results, true
}

func filterNativeSearchResults(items []SearchResult, pool map[string]nativeSourceEntry) []SearchResult {
	out := make([]SearchResult, 0, min(len(items), nativeMaxResults))
	seen := map[string]bool{}
	for _, item := range items {
		if len(out) >= nativeMaxResults {
			break
		}
		clean := stripTrackingParams(item.URL)
		if !SafePageURL(clean) {
			continue
		}
		canon := canonicalizeURL(clean)
		if canon == "" || seen[canon] {
			continue
		}
		src, ok := pool[canon]
		if !ok {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = src.title
		}
		seen[canon] = true
		// Always return the matched server-side tool URL (with tracking params
		// stripped) rather than the model's potentially rewritten URL.
		out = append(out, SearchResult{
			URL:     src.cleanURL,
			Title:   title,
			Content: strings.TrimSpace(item.Content),
		})
	}
	return out
}

func isTrackingParam(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(lower, "utm_") || trackingQueryKeys[lower]
}

// stripTrackingParams removes known tracking query parameters and URL fragments
// while preserving the exact ordering and raw encoding of all surviving query
// parameters (so signed or order-sensitive URLs remain intact).
func stripTrackingParams(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	u.Fragment = ""
	u.RawFragment = ""
	if u.RawQuery != "" {
		rawPairs := strings.Split(u.RawQuery, "&")
		kept := make([]string, 0, len(rawPairs))
		for _, pair := range rawPairs {
			if pair == "" {
				continue
			}
			keyRaw := pair
			if eq := strings.IndexByte(pair, '='); eq >= 0 {
				keyRaw = pair[:eq]
			}
			keyUnescaped, unescErr := url.QueryUnescape(keyRaw)
			if unescErr != nil {
				keyUnescaped = keyRaw
			}
			if isTrackingParam(keyUnescaped) {
				continue
			}
			kept = append(kept, pair)
		}
		u.RawQuery = strings.Join(kept, "&")
	}
	return u.String()
}

// canonicalizeURL normalizes a URL for server-side search source-pool matching:
// lowercases host, strips www. and default ports, trims trailing slashes, drops
// tracking query params and fragments, and canonically orders distinct query
// keys while preserving repeated-key value order.
func canonicalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	host := canonicalHost(raw)
	if host == "" {
		return ""
	}
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	return host + path + canonicalQuery(u.RawQuery)
}

// canonicalizeFetchURL produces a stricter resource key for Fetch grounding:
// preserves scheme, exact host (without collapsing www.), and trailing slash
// distinctions while still stripping tracking params and fragments.
func canonicalizeFetchURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host += ":" + port
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	return scheme + "://" + host + path + canonicalQuery(u.RawQuery)
}

func canonicalQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil || len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		if isTrackingParam(k) {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return "?" + strings.Join(parts, "&")
}

func canonicalHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return strings.TrimPrefix(host, "www.")
}
