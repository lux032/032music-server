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

	// AI Review module settings
	Review              storage.AIReviewSettings
	DeepSeekWebReady    bool
	ArtistAliasSettings storage.ArtistAliasSettings
	TaggingEnabled      bool
	TranslationEnabled  bool

	Usage      []aiUsageRowView
	UsageTotal string
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
		Review:              rev,
		DeepSeekWebReady:    aliasSt.DeepSeekKey != "" || strings.Contains(strings.ToLower(ai.BaseURL), "api.deepseek.com"),
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

	if err := a.store.SaveAISettings(ctx, next); err != nil {
		redirectWithNotice(w, r, aiSettingsPath, "保存全局模型设置失败："+err.Error())
		return
	}
	if err := a.store.SaveAIReviewSettings(ctx, rev); err != nil {
		redirectWithNotice(w, r, aiSettingsPath, "保存 AI 初审设置失败："+err.Error())
		return
	}
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
