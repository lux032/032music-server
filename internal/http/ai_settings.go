package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/aiclient"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
	"github.com/lux032/032music-server/internal/websearch"
)

const aiSettingsPath = "/admin/settings/ai"

var aiPurposeLabels = map[string]string{
	"tag":                        "AI 打标",
	"review":                     "AI 打标复核",
	"sample":                     "抽样对比",
	"test":                       "连接测试",
	"mix":                        "每日推荐标题",
	"translate":                  "歌词翻译",
	"playlist":                   "AI 灵感歌单",
	"artist_alias":               "中文别名补全",
	"artist_biography_translate": "歌手简介翻译",
	"artist_biography_web":       "歌手简介联网概括",
	"artist_image_web":           "艺术家头像联网选图",
	"artist_image_vision":        "艺术家头像视觉质检",
	"artist_image_audit":         "艺术家头像存量 AI 审查",
	"web_search_native":          "原生联网搜索",
	"album_review":               "AI 审核 · 专辑关联",
	"track_review":               "AI 审核 · 曲目关联",
	"work_review":                "AI 审核 · 作品对齐",
	"artist_review":              "AI 审核 · 艺术家匹配",
	"credit_split":               "AI 审核 · 复合署名拆分",
	"series_review":              "AI 审核 · 系列建议",
}

type aiSettingsPageData struct {
	Chrome
	Notice string

	// Global provider / connection
	Configured    bool
	BaseURL       string
	Model         string
	FallbackModel string
	ReviewModel   string
	ReviewEnabled bool
	KeySet        bool
	ProxySet      bool
	Reasoning     string
	RPM           int
	Concurrency   int

	// Unified web search settings
	WebSearchProvider string
	WebSearchReady    bool
	WebSearchSummary  string
	TavilyKeyCount    int
	TavilyKeyStatuses []websearch.KeyStatus
	DeepSeekKeySet    bool

	// AI Review module settings
	Review              storage.AIReviewSettings
	DeepSeekWebReady    bool
	ArtistAliasSettings storage.ArtistAliasSettings
	TaggingEnabled      bool
	TranslationEnabled  bool

	Usage      []aiUsageRowView
	UsageTotal string
}

func (a *App) webSearchStatusSummary(ctx context.Context) (ready bool, summary string, ws storage.WebSearchSettings, statuses []websearch.KeyStatus, deepSeekSet bool) {
	ws, _ = a.store.WebSearchSettings(ctx)
	aliasSt, _ := a.store.ArtistAliasSettings(ctx)
	ai, _ := a.store.AISettings(ctx)
	statuses = a.webSearchRegistry().TavilyStatuses(ws, ai)
	dsKey := ws.EffectiveDeepSeekKey(aliasSt, ai)
	deepSeekSet = dsKey != ""
	ready = ws.Configured(aliasSt, ai)
	tavCount := len(ws.TavilyKeys)
	switch storage.NormalizeWebSearchProvider(ws.Provider) {
	case storage.WebSearchProviderNativeThenSearch:
		switch {
		case ai.Configured() && tavCount > 0 && deepSeekSet:
			summary = fmt.Sprintf("模型原生优先（复用全局 AI）+ Tavily（%d 把 Key）/ DeepSeek 自动回退", tavCount)
		case ai.Configured() && tavCount > 0:
			summary = fmt.Sprintf("模型原生优先（复用全局 AI）+ Tavily（%d 把 Key）自动回退", tavCount)
		case ai.Configured() && deepSeekSet:
			summary = "模型原生优先（复用全局 AI）+ DeepSeek 自动回退"
		case ai.Configured():
			summary = "模型原生优先（复用全局 AI）"
		case tavCount > 0 || deepSeekSet:
			summary = "模型原生优先（全局 AI 未配置，当前仅回退引擎可用）"
		default:
			summary = "模型原生优先（请先配置全局 AI 模型服务）"
		}
	case storage.WebSearchProviderTavily:
		if tavCount > 0 {
			summary = fmt.Sprintf("Tavily（%d 把 Key 轮询）", tavCount)
		} else {
			summary = "Tavily（尚未配置 Key）"
		}
	case storage.WebSearchProviderDeepSeek:
		if deepSeekSet {
			summary = "DeepSeek 联网（已就绪）"
		} else {
			summary = "DeepSeek 联网（尚未配置 Key）"
		}
	default:
		switch {
		case tavCount > 0 && deepSeekSet:
			summary = fmt.Sprintf("Tavily（%d 把 Key 轮询）+ DeepSeek 自动兜底", tavCount)
		case tavCount > 0:
			summary = fmt.Sprintf("Tavily（%d 把 Key 轮询）", tavCount)
		case deepSeekSet:
			summary = "DeepSeek 联网（已就绪，未配 Tavily）"
		default:
			summary = "尚未配置联网搜索密钥"
		}
	}
	return ready, summary, ws, statuses, deepSeekSet
}

func tavilyKeyStateLabel(state string) string {
	switch state {
	case websearch.KeyStateHealthy:
		return "正常"
	case websearch.KeyStateCooldown:
		return "冷却中"
	case websearch.KeyStateQuotaExhausted:
		return "额度耗尽"
	case websearch.KeyStateInvalid:
		return "鉴权失效"
	default:
		return state
	}
}

func (a *App) buildAIUsageView(ctx context.Context) ([]aiUsageRowView, string) {
	usage, err := a.store.AIUsageTotals(ctx, "")
	if err != nil {
		a.logger.Error("AI usage", "error", err)
		return nil, ""
	}
	var rows []aiUsageRowView
	var total storage.AIUsage
	for _, u := range usage {
		total.Add(u.Usage)
		label := aiPurposeLabels[u.Purpose]
		if label == "" {
			label = u.Purpose
		}
		rows = append(rows, aiUsageRowView{
			Model:      u.Model,
			Purpose:    label,
			Requests:   u.Usage.Requests,
			Prompt:     storage.FormatTokenCount(u.Usage.PromptTokens),
			Cached:     storage.FormatTokenCount(u.Usage.CachedTokens),
			Completion: storage.FormatTokenCount(u.Usage.CompletionTokens),
			Thinking:   storage.FormatTokenCount(u.Usage.ReasoningTokens),
		})
	}
	summary := ""
	if total.Requests > 0 {
		summary = fmt.Sprintf("累计 %d 次请求 · 输入 %s（其中缓存命中 %s）· 输出 %s", total.Requests,
			storage.FormatTokenCount(total.PromptTokens), storage.FormatTokenCount(total.CachedTokens), storage.FormatTokenCount(total.CompletionTokens))
		if total.ReasoningTokens > 0 {
			summary += fmt.Sprintf("（含思考 %s，按输出计费）", storage.FormatTokenCount(total.ReasoningTokens))
		}
	}
	return rows, summary
}

func (a *App) handleAISettingsPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	session, _ := a.sessions.get(r)
	ai, err := a.store.AISettings(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "ai-settings", err)
		return
	}
	rev, err := a.store.AIReviewSettings(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "ai-settings", err)
		return
	}
	aliasSt, _ := a.store.ArtistAliasSettings(ctx)
	trSt, _ := a.store.LyricsTranslationSettings(ctx)
	usageRows, usageTotal := a.buildAIUsageView(ctx)
	wsReady, wsSummary, ws, tavStatuses, dsSet := a.webSearchStatusSummary(ctx)

	data := aiSettingsPageData{
		Chrome:              a.chromeFor(ctx, session, "ai-settings"),
		Notice:              r.URL.Query().Get("notice"),
		Configured:          ai.Configured(),
		BaseURL:             ai.BaseURL,
		Model:               ai.Model,
		FallbackModel:       ai.FallbackModel,
		ReviewModel:         ai.ReviewModel,
		ReviewEnabled:       ai.ReviewEnabled,
		KeySet:              ai.APIKey != "",
		ProxySet:            ai.ProxyURL != "",
		Reasoning:           ai.ReasoningEffort,
		RPM:                 ai.RequestsPerMinute,
		Concurrency:         ai.Concurrency,
		WebSearchProvider:   ws.Provider,
		WebSearchReady:      wsReady,
		WebSearchSummary:    wsSummary,
		TavilyKeyCount:      len(ws.TavilyKeys),
		TavilyKeyStatuses:   tavStatuses,
		DeepSeekKeySet:      dsSet,
		Review:              rev,
		DeepSeekWebReady:    wsReady,
		ArtistAliasSettings: aliasSt,
		TaggingEnabled:      ai.TaggingEnabled,
		TranslationEnabled:  trSt.Enabled,
		Usage:               usageRows,
		UsageTotal:          usageTotal,
	}
	a.render(w, http.StatusOK, "ai-settings.html", data)
}

func (a *App) handleSaveAISettings(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	current, err := a.store.AISettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	next := current
	next.BaseURL = strings.TrimRight(strings.TrimSpace(r.FormValue("base_url")), "/")
	next.Model = strings.TrimSpace(r.FormValue("model"))
	next.FallbackModel = strings.TrimSpace(r.FormValue("fallback_model"))
	next.ReviewModel = strings.TrimSpace(r.FormValue("review_model"))
	next.ReviewEnabled = r.FormValue("review_enabled") != ""
	next.ReasoningEffort = r.FormValue("reasoning_effort")
	if key := strings.TrimSpace(r.FormValue("api_key")); key != "" {
		next.APIKey = key
	} else if r.FormValue("clear_api_key") != "" {
		next.APIKey = ""
	}
	if proxy := strings.TrimSpace(r.FormValue("proxy_url")); proxy != "" {
		if !validProxyURL(proxy) {
			redirectWithNotice(w, r, aiSettingsPath, "代理地址无效：支持 http://、https://、socks5:// 开头的地址")
			return
		}
		next.ProxyURL = proxy
	} else if r.FormValue("clear_proxy") != "" {
		next.ProxyURL = ""
	}
	if v, convErr := strconv.Atoi(r.FormValue("rpm")); convErr == nil && v >= 1 && v <= 10000 {
		next.RequestsPerMinute = v
	}
	if v, convErr := strconv.Atoi(r.FormValue("concurrency")); convErr == nil && v >= 1 && v <= 16 {
		next.Concurrency = v
	}
	if next.BaseURL != "" {
		if err := tagging.ValidateBaseURL(next.BaseURL); err != nil {
			redirectWithNotice(w, r, aiSettingsPath, err.Error())
			return
		}
	}
	if next.ReviewEnabled && next.ReviewModel == "" {
		redirectWithNotice(w, r, aiSettingsPath, "开启 AI 打标复核需要填写复核模型")
		return
	}

	rev, err := a.store.AIReviewSettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rev.Enabled = r.FormValue("ai_review_enabled") != ""
	rev.InlineEnabled = r.FormValue("ai_review_inline") != ""
	rev.WebSearchEnabled = r.FormValue("ai_review_web") != ""
	rev.Model = strings.TrimSpace(r.FormValue("ai_review_model"))
	if v := strings.TrimSpace(r.FormValue("album_mode")); v != "" {
		rev.AlbumMode = v
	}
	if v := strings.TrimSpace(r.FormValue("track_mode")); v != "" {
		rev.TrackMode = v
	}
	if v := strings.TrimSpace(r.FormValue("work_mode")); v != "" {
		rev.WorkMode = v
	}
	if v := strings.TrimSpace(r.FormValue("artist_mode")); v != "" {
		rev.ArtistMode = v
	}
	if v := strings.TrimSpace(r.FormValue("artist_merge_mode")); v != "" {
		rev.ArtistMergeMode = v
	}
	if v := strings.TrimSpace(r.FormValue("credit_mode")); v != "" {
		rev.CreditMode = v
	}
	if v := strings.TrimSpace(r.FormValue("series_mode")); v != "" {
		rev.SeriesMode = v
	}
	if rev.Enabled && !next.Configured() {
		redirectWithNotice(w, r, aiSettingsPath, "开启 AI 初审前需要填写接口地址、主模型和 API Key")
		return
	}

	ws, wsErr := a.store.WebSearchSettings(ctx)
	if wsErr != nil {
		http.Error(w, wsErr.Error(), http.StatusInternalServerError)
		return
	}
	wsChanged := false
	if prov := strings.TrimSpace(r.FormValue("web_search_provider")); prov != "" {
		ws.Provider = storage.NormalizeWebSearchProvider(prov)
		wsChanged = true
	}
	if r.FormValue("clear_tavily_keys") != "" {
		ws.TavilyKeys = nil
		wsChanged = true
	} else if rawTavily := strings.TrimSpace(r.FormValue("tavily_keys")); rawTavily != "" {
		parsed := storage.ParseAPIKeyList(rawTavily)
		if strings.ToLower(strings.TrimSpace(r.FormValue("tavily_keys_mode"))) == "replace" {
			ws.TavilyKeys = parsed
		} else {
			ws.TavilyKeys = storage.DedupeAPIKeys(append(ws.TavilyKeys, parsed...))
		}
		wsChanged = true
	}
	if r.FormValue("clear_web_deepseek_key") != "" {
		ws.DeepSeekKey = ""
		wsChanged = true
	} else if dsKey := strings.TrimSpace(r.FormValue("web_deepseek_key")); dsKey != "" {
		ws.DeepSeekKey = dsKey
		wsChanged = true
	}

	if err := a.store.SaveAISettings(ctx, next); err != nil {
		redirectWithNotice(w, r, aiSettingsPath, "保存全局模型设置失败："+err.Error())
		return
	}
	if err := a.store.SaveAIReviewSettings(ctx, rev); err != nil {
		redirectWithNotice(w, r, aiSettingsPath, "保存 AI 初审设置失败："+err.Error())
		return
	}
	if wsChanged {
		if err := a.store.SaveWebSearchSettings(ctx, ws); err != nil {
			redirectWithNotice(w, r, aiSettingsPath, "保存联网搜索设置失败："+err.Error())
			return
		}
		if a.artistAliases != nil {
			a.artistAliases.Interrupt()
		}
	}
	a.webSearchRegistry().ResetNativeCooldown()
	if a.sharedAILimiter != nil {
		a.sharedAILimiter.SetRate(next.RequestsPerMinute)
	}
	if a.aiTagging != nil {
		a.aiTagging.Wake()
	}
	redirectWithNotice(w, r, aiSettingsPath, "AI 设置已保存")
}

func (a *App) handleAISettingsTest(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	st, err := a.store.AISettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !st.Configured() {
		redirectWithNotice(w, r, aiSettingsPath, "请先填写并保存接口地址、模型和 API Key")
		return
	}
	rev, _ := a.store.AIReviewSettings(ctx)
	models := []string{st.Model}
	for _, extra := range []string{st.ReviewModel, aiclient.ResolveReviewModel(st, rev)} {
		extra = strings.TrimSpace(extra)
		if extra == "" {
			continue
		}
		dup := false
		for _, m := range models {
			if m == extra {
				dup = true
				break
			}
		}
		if !dup {
			models = append(models, extra)
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	var parts []string
	if a.aiTagging != nil {
		for _, m := range models {
			res, testErr := a.aiTagging.TestConnection(callCtx, st, m)
			if testErr != nil {
				parts = append(parts, fmt.Sprintf("%s 失败：%s", m, testErr.Error()))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s 正常（%.1fs，输入 %d / 输出 %d token）", m, res.Latency.Seconds(), res.Usage.PromptTokens, res.Usage.CompletionTokens))
		}
	} else {
		chat, chatErr := aiclient.DefaultChatFactory(st)
		if chatErr != nil {
			redirectWithNotice(w, r, aiSettingsPath, "连接测试失败："+chatErr.Error())
			return
		}
		for _, m := range models {
			res, used, testErr := aiclient.CompleteWithFallback(callCtx, a.store, chat, a.sharedAILimiter, st, tagging.ChatRequest{
				Model:  m,
				System: `Reply with JSON: {"ok":true}`,
				User:   `{"ping":1}`,
				Format: "json_object",
			}, "test")
			if testErr != nil {
				parts = append(parts, fmt.Sprintf("%s 失败：%s", m, testErr.Error()))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s 正常（%.1fs，输入 %d / 输出 %d token）", used, res.Latency.Seconds(), res.Usage.PromptTokens, res.Usage.CompletionTokens))
		}
	}
	redirectWithNotice(w, r, aiSettingsPath, strings.Join(parts, "；"))
}

func (a *App) handleAISettingsWebSearchTest(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	ws, err := a.store.WebSearchSettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	aliasSt, _ := a.store.ArtistAliasSettings(ctx)
	aiSt, _ := a.store.AISettings(ctx)
	callCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	summary, testErr := a.webSearchRegistry().TestConnection(callCtx, ws, aliasSt, aiSt)
	if testErr != nil {
		redirectWithNotice(w, r, aiSettingsPath, "联网搜索测试失败："+testErr.Error())
		return
	}
	redirectWithNotice(w, r, aiSettingsPath, "联网搜索测试成功："+summary)
}
