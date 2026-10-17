package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

const aiTaggingPath = "/admin/ai-tagging"

// SetAITagging attaches the AI tagging worker.
func (a *App) SetAITagging(service *tagging.Service) { a.aiTagging = service }

// aiTaggingView is the human-readable status shared by the page and its poller.
type aiTaggingView struct {
	StateLabel    string  `json:"stateLabel"`
	StateDetail   string  `json:"stateDetail"`
	Tone          string  `json:"tone"`
	Percent       float64 `json:"percent"`
	ProgressText  string  `json:"progressText"`
	CurrentText   string  `json:"currentText"`
	EstimateText  string  `json:"estimateText"`
	ReviewText    string  `json:"reviewText"`
	FailureText   string  `json:"failureText"`
	LastErrorText string  `json:"lastErrorText"`
	SampleText    string  `json:"sampleText"`
	SummaryText   string  `json:"summaryText"`
}

func aiStateLabel(s tagging.Snapshot) (string, string) {
	switch s.State {
	case tagging.StateNotConfigured:
		return "未配置模型服务", "off"
	case tagging.StateDisabled:
		return "未开启", "off"
	case tagging.StatePaused:
		return "已暂停", "warn"
	case tagging.StateIdle:
		if s.AudioActive && s.Stats != nil && s.Stats.WaitingAudio > 0 {
			return "等待声学分析", "busy"
		}
		return "已完成，新歌会自动打标", "ok"
	case tagging.StateRunning:
		return "打标中", "busy"
	case tagging.StateReviewing:
		return "复核中", "busy"
	case tagging.StateSampling:
		return "抽样对比中", "busy"
	case tagging.StateBackoff:
		return "暂停重试", "warn"
	case tagging.StateBlocked:
		return "需要修改设置", "warn"
	case tagging.StateError:
		return "出错，稍后自动重试", "warn"
	}
	return string(s.State), "off"
}

func formatTokens(n float64) string {
	return storage.FormatTokenCount(int64(math.Round(n)))
}

func buildAITaggingView(s tagging.Snapshot) aiTaggingView {
	var v aiTaggingView
	v.StateLabel, v.Tone = aiStateLabel(s)
	switch s.State {
	case tagging.StateBackoff:
		v.StateDetail = s.Detail
		if s.BackoffUntil != nil {
			v.StateDetail += "，" + s.BackoffUntil.In(homeZone).Format("15:04") + " 自动重试"
		}
	case tagging.StateBlocked:
		v.StateDetail = s.Detail + "。修改设置并保存后继续"
	case tagging.StateSampling:
		v.StateDetail = "抽样对比完成后继续打标"
	case tagging.StateIdle:
		if s.AudioActive && s.Stats != nil && s.Stats.WaitingAudio > 0 {
			v.StateDetail = fmt.Sprintf("还有 %d 首等待声学分析，分析完成后自动打标", s.Stats.WaitingAudio)
		}
	}
	if st := s.Stats; st != nil {
		if st.Eligible > 0 {
			v.Percent = math.Round(float64(st.Tagged)/float64(st.Eligible)*1000) / 10
		}
		v.ProgressText = fmt.Sprintf("已打标 %d / %d 首（%.1f%%）· 待打标 %d", st.Tagged, st.Eligible, v.Percent, s.Pending)
		if st.Stale > 0 {
			v.ProgressText += fmt.Sprintf("（含 %d 首信息有变化、需重新打标）", st.Stale)
		}
		if s.AudioActive && st.WaitingAudio > 0 {
			v.ProgressText += fmt.Sprintf(" · %d 首等待声学分析", st.WaitingAudio)
		}
		if st.AvgPrompt > 0 {
			per := st.AvgPrompt + st.AvgCompletion
			v.EstimateText = fmt.Sprintf("最近平均每首 %s 输入 + %s 输出 token", formatTokens(st.AvgPrompt), formatTokens(st.AvgCompletion))
			if remaining := s.Pending + st.WaitingAudio; remaining > 0 {
				v.EstimateText += fmt.Sprintf(" · 剩余 %d 首预计约 %s token", remaining, formatTokens(per*float64(remaining)))
			}
		}
		if st.NeedsReview > 0 {
			if s.ReviewActive {
				v.ReviewText = fmt.Sprintf("%d 首被判定需要复核，已由复核模型重新打标 %d 首", st.NeedsReview, st.Reviewed)
			} else {
				v.ReviewText = fmt.Sprintf("%d 首被判定需要复核（未开启复核，保留主模型结果）", st.NeedsReview)
			}
		}
		if st.UserCorrected > 0 {
			if v.ReviewText != "" {
				v.ReviewText += " · "
			}
			v.ReviewText += fmt.Sprintf("%d 首有人工修正", st.UserCorrected)
		}
		if st.RetryPending > 0 || st.Permanent > 0 {
			v.FailureText = fmt.Sprintf("%d 首多次失败、暂停重试 · %d 首稍后自动重试", st.Permanent, st.RetryPending)
		}
	}
	if len(s.Current) > 0 {
		names := make([]string, 0, len(s.Current))
		for _, c := range s.Current {
			names = append(names, c.Title)
		}
		if len(names) > 4 {
			names = append(names[:4], fmt.Sprintf("等 %d 首", len(s.Current)))
		}
		v.CurrentText = "正在处理：" + strings.Join(names, "、")
	}
	if s.LastError != "" && s.LastErrorAt != nil {
		v.LastErrorText = fmt.Sprintf("最近错误（%s）：%s", s.LastErrorAt.In(homeZone).Format("01-02 15:04"), s.LastError)
	}
	if r := s.Sample; r != nil {
		switch r.Status {
		case "pending", "running":
			v.SampleText = fmt.Sprintf("抽样对比进行中：%d / %d 首", r.Done, r.Size)
		case "done":
			v.SampleText = fmt.Sprintf("最近一次抽样对比已完成（%d 首）", r.Size)
		case "cancelled":
			v.SampleText = fmt.Sprintf("抽样对比已取消（完成 %d / %d 首）", r.Done, r.Size)
		case "failed":
			v.SampleText = "抽样对比失败：" + r.Error
		}
	}
	switch {
	case !s.Configured:
		v.SummaryText = "未配置 · 用大模型为曲库标注情绪与风格"
	case !s.Enabled:
		v.SummaryText = "未开启 · 用大模型为曲库标注情绪与风格"
	case s.Stats != nil:
		v.SummaryText = fmt.Sprintf("%s · 已打标 %d / %d", v.StateLabel, s.Stats.Tagged, s.Stats.Eligible)
	default:
		v.SummaryText = v.StateLabel
	}
	return v
}

func (a *App) aiTaggingSnapshot(ctx context.Context) (tagging.Snapshot, error) {
	if a.aiTagging == nil {
		return tagging.Snapshot{State: tagging.StateNotConfigured}, nil
	}
	return a.aiTagging.Snapshot(ctx)
}

// aiUsageRowView is one usage line on the page.
type aiUsageRowView struct {
	Model, Purpose                       string
	Requests                             int64
	Prompt, Cached, Completion, Thinking string
}

type aiTagChip struct {
	Kind, Tag, Label, Source string
	Weight, Confidence       float64
	WeightPct, ConfPct       int
	Negative                 bool
}

type aiRecentView struct {
	TrackID           int64
	Title, Artist     string
	Model, ReviewedBy string
	NeedsReview       bool
	TaggedAt          string
	Moods, Styles     []aiTagChip
}

type aiTaggingPageData struct {
	Chrome
	Notice        string
	Status        tagging.Snapshot
	View          aiTaggingView
	BaseURL       string
	Model         string
	ReviewModel   string
	ReviewEnabled bool
	KeySet        bool
	ProxySet      bool
	Reasoning     string
	IncludeLyrics bool
	RPM           int
	Concurrency   int
	FirstEnabled  string
	Usage         []aiUsageRowView
	UsageTotal    string
	Failures      []storage.AITaggingFailure
	Recent        []aiRecentView
	Comparison    *tagging.Comparison
	Sample        *storage.AISampleRun
}

var aiPurposeLabels = map[string]string{"tag": "打标", "review": "复核", "sample": "抽样对比", "test": "连接测试", "mix": "歌单主题与标题", "translate": "歌词翻译"}

func chipsFor(tags []storage.TrackTag, kind string) []aiTagChip {
	var out []aiTagChip
	for _, t := range tags {
		if t.Kind != kind {
			continue
		}
		c := aiTagChip{Kind: t.Kind, Tag: t.Tag, Label: tagging.TagLabel(t.Kind, t.Tag), Source: t.Source, Weight: t.Weight, Confidence: t.Confidence,
			WeightPct: int(math.Round(t.Weight * 100)), ConfPct: int(math.Round(t.Confidence * 100))}
		if m, ok := tagging.MoodByID(t.Tag); ok && kind == "mood" {
			c.Negative = m.Negative
		}
		out = append(out, c)
	}
	return out
}

func (a *App) handleAITaggingPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	ctx := r.Context()
	snap, err := a.aiTaggingSnapshot(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	st, err := a.store.AISettings(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	data := aiTaggingPageData{Chrome: a.chromeFor(ctx, session, "console"), Notice: r.URL.Query().Get("notice"), Status: snap, View: buildAITaggingView(snap),
		BaseURL: st.BaseURL, Model: st.Model, ReviewModel: st.ReviewModel, ReviewEnabled: st.ReviewEnabled, KeySet: st.APIKey != "", ProxySet: st.ProxyURL != "",
		Reasoning: st.ReasoningEffort, IncludeLyrics: st.IncludeLyrics, RPM: st.RequestsPerMinute, Concurrency: st.Concurrency, FirstEnabled: st.FirstEnabledAt}
	usage, err := a.store.AIUsageTotals(ctx, "")
	if err != nil {
		a.logger.Error("AI usage", "error", err)
	}
	var total storage.AIUsage
	for _, u := range usage {
		total.Add(u.Usage)
		data.Usage = append(data.Usage, aiUsageRowView{Model: u.Model, Purpose: aiPurposeLabels[u.Purpose], Requests: u.Usage.Requests,
			Prompt: storage.FormatTokenCount(u.Usage.PromptTokens), Cached: storage.FormatTokenCount(u.Usage.CachedTokens),
			Completion: storage.FormatTokenCount(u.Usage.CompletionTokens), Thinking: storage.FormatTokenCount(u.Usage.ReasoningTokens)})
	}
	if total.Requests > 0 {
		data.UsageTotal = fmt.Sprintf("累计 %d 次请求 · 输入 %s（其中缓存命中 %s）· 输出 %s", total.Requests,
			storage.FormatTokenCount(total.PromptTokens), storage.FormatTokenCount(total.CachedTokens), storage.FormatTokenCount(total.CompletionTokens))
		if total.ReasoningTokens > 0 {
			data.UsageTotal += fmt.Sprintf("（含思考 %s，按输出计费）", storage.FormatTokenCount(total.ReasoningTokens))
		}
	}
	if data.Failures, err = a.store.TaggingFailures(ctx, 50); err != nil {
		a.logger.Error("AI tagging failures", "error", err)
	}
	recent, err := a.store.RecentTagResults(ctx, 20)
	if err != nil {
		a.logger.Error("recent AI tags", "error", err)
	}
	for _, r := range recent {
		data.Recent = append(data.Recent, aiRecentView{TrackID: r.TrackID, Title: r.Title, Artist: r.Artist, Model: r.Model, ReviewedBy: r.ReviewedBy,
			NeedsReview: r.NeedsReview, TaggedAt: r.TaggedAt, Moods: chipsFor(r.Tags, "mood"), Styles: chipsFor(r.Tags, "style")})
	}
	if snap.Sample != nil {
		data.Sample = snap.Sample
		if snap.Sample.Done > 0 {
			items, err := a.store.AISampleItems(ctx, snap.Sample.ID)
			if err != nil {
				a.logger.Error("AI sample items", "error", err)
			} else {
				cmp := tagging.Compare(items)
				data.Comparison = &cmp
			}
		}
	}
	a.render(w, http.StatusOK, "ai-tagging.html", data)
}

func (a *App) handleAITaggingStatus(w http.ResponseWriter, r *http.Request) {
	snap, err := a.aiTaggingSnapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": snap, "view": buildAITaggingView(snap)})
}

func validProxyURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "socks5" || u.Scheme == "socks5h")
}

// handleSaveAITagging saves the model service settings and the tagging switch.
// API key and proxy are write-only: an empty field keeps the stored value.
func (a *App) handleSaveAITagging(w http.ResponseWriter, r *http.Request) {
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
	next.ReviewModel = strings.TrimSpace(r.FormValue("review_model"))
	next.ReviewEnabled = r.FormValue("review_enabled") != ""
	next.IncludeLyrics = r.FormValue("include_lyrics") != ""
	next.TaggingEnabled = r.FormValue("enabled") != ""
	next.ReasoningEffort = r.FormValue("reasoning_effort")
	if key := strings.TrimSpace(r.FormValue("api_key")); key != "" {
		next.APIKey = key
	} else if r.FormValue("clear_api_key") != "" {
		next.APIKey = ""
	}
	if proxy := strings.TrimSpace(r.FormValue("proxy_url")); proxy != "" {
		if !validProxyURL(proxy) {
			redirectWithNotice(w, r, aiTaggingPath, "代理地址无效：支持 http://、https://、socks5:// 开头的地址")
			return
		}
		next.ProxyURL = proxy
	} else if r.FormValue("clear_proxy") != "" {
		next.ProxyURL = ""
	}
	if v, err := strconv.Atoi(r.FormValue("rpm")); err == nil && v >= 1 && v <= 10000 {
		next.RequestsPerMinute = v
	}
	if v, err := strconv.Atoi(r.FormValue("concurrency")); err == nil && v >= 1 && v <= 16 {
		next.Concurrency = v
	}
	if next.BaseURL != "" {
		if err := tagging.ValidateBaseURL(next.BaseURL); err != nil {
			redirectWithNotice(w, r, aiTaggingPath, err.Error())
			return
		}
	}
	if next.ReviewEnabled && next.ReviewModel == "" {
		redirectWithNotice(w, r, aiTaggingPath, "开启复核需要填写复核模型")
		return
	}
	if next.TaggingEnabled && !next.Configured() {
		redirectWithNotice(w, r, aiTaggingPath, "开启打标前需要填写接口地址、模型和 API Key")
		return
	}
	if !next.TaggingEnabled || !current.TaggingEnabled {
		next.TaggingPaused = false
	}
	if err := a.store.SaveAISettings(ctx, next); err != nil {
		redirectWithNotice(w, r, aiTaggingPath, "保存失败："+err.Error())
		return
	}
	if a.aiTagging != nil {
		a.aiTagging.Wake()
	}
	switch {
	case next.TaggingEnabled && !current.TaggingEnabled && current.FirstEnabledAt == "":
		msg := "AI 打标已开启，开始首次全量打标；之后新入库的歌曲会自动打标"
		if a.audioAnalysisEnabled(ctx) {
			msg += "（等声学分析完成后再打标）"
		}
		redirectWithNotice(w, r, aiTaggingPath, msg)
	case next.TaggingEnabled && !current.TaggingEnabled:
		redirectWithNotice(w, r, aiTaggingPath, "AI 打标已开启，继续处理尚未打标的曲目")
	case !next.TaggingEnabled && current.TaggingEnabled:
		redirectWithNotice(w, r, aiTaggingPath, "AI 打标已关闭，已有标签会保留")
	default:
		redirectWithNotice(w, r, aiTaggingPath, "设置已保存")
	}
}

// handleAITaggingTest sends one fictional track to the primary (and review) model.
func (a *App) handleAITaggingTest(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.aiTagging == nil {
		redirectWithNotice(w, r, aiTaggingPath, "AI 打标不可用")
		return
	}
	st, err := a.store.AISettings(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !st.Configured() {
		redirectWithNotice(w, r, aiTaggingPath, "请先填写并保存接口地址、模型和 API Key")
		return
	}
	models := []string{st.Model}
	if st.ReviewModel != "" && st.ReviewModel != st.Model {
		models = append(models, st.ReviewModel)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	var parts []string
	for _, m := range models {
		res, err := a.aiTagging.TestConnection(ctx, st, m)
		if err != nil {
			parts = append(parts, fmt.Sprintf("%s 失败：%s", m, err.Error()))
			continue
		}
		format := "结构化输出"
		if res.Format == "json_object" {
			format = "JSON 模式（不支持结构化输出，已自动降级）"
		}
		labels := make([]string, 0, len(res.Result.Moods))
		for _, mood := range res.Result.Moods {
			labels = append(labels, tagging.TagLabel("mood", mood.Tag))
		}
		line := fmt.Sprintf("%s 正常：%.1f 秒，%s，输入 %d / 输出 %d token", m, res.Latency.Seconds(), format, res.Usage.PromptTokens, res.Usage.CompletionTokens)
		if res.Usage.CachedTokens > 0 {
			line += fmt.Sprintf("（缓存命中 %d）", res.Usage.CachedTokens)
		}
		if res.Usage.ReasoningTokens > 0 {
			line += fmt.Sprintf("，含思考 %d token（按输出计费，可把“思考强度”设为 none 关闭）", res.Usage.ReasoningTokens)
		}
		line += "，示例结果：" + strings.Join(labels, "、")
		parts = append(parts, line)
	}
	redirectWithNotice(w, r, aiTaggingPath, strings.Join(parts, "；"))
}

func (a *App) handleAITaggingPause(paused bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.validCSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		st, err := a.store.AISettings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !st.TaggingEnabled {
			redirectWithNotice(w, r, aiTaggingPath, "AI 打标未开启")
			return
		}
		st.TaggingPaused = paused
		if err := a.store.SaveAISettings(r.Context(), st); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if a.aiTagging != nil {
			a.aiTagging.Wake()
		}
		if paused {
			redirectWithNotice(w, r, aiTaggingPath, "已暂停；正在处理的几首完成后停止")
		} else {
			redirectWithNotice(w, r, aiTaggingPath, "已继续打标")
		}
	}
}

func (a *App) handleAITaggingRetry(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	n, err := a.store.ClearTaggingFailures(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if a.aiTagging != nil {
		a.aiTagging.Wake()
	}
	redirectWithNotice(w, r, aiTaggingPath, "已清除 "+strconv.FormatInt(n, 10)+" 条失败记录，稍后重新打标")
}

func (a *App) handleAITaggingSample(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.aiTagging == nil {
		redirectWithNotice(w, r, aiTaggingPath, "AI 打标不可用")
		return
	}
	size, _ := strconv.Atoi(r.FormValue("size"))
	if err := a.aiTagging.RequestSample(r.Context(), size); err != nil {
		redirectWithNotice(w, r, aiTaggingPath, "无法开始抽样对比："+err.Error())
		return
	}
	redirectWithNotice(w, r, aiTaggingPath, fmt.Sprintf("已开始抽样对比：%d 首分别用主模型和复核模型打标（不写入标签），完成后在本页查看结果", size))
}

func (a *App) handleAITaggingSampleCancel(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.aiTagging != nil {
		if err := a.aiTagging.CancelSample(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		a.aiTagging.Wake()
	}
	redirectWithNotice(w, r, aiTaggingPath, "已取消抽样对比")
}

// aiTaggingEnabled is used by /api/v1/capabilities ("trackTags").
func (a *App) aiTaggingEnabled(ctx context.Context) bool {
	if a.aiTagging == nil {
		return false
	}
	st, err := a.store.AISettings(ctx)
	return err == nil && st.Configured() && st.TaggingEnabled
}

func (a *App) dashboardAITaggingView(ctx context.Context) aiTaggingView {
	snap, err := a.aiTaggingSnapshot(ctx)
	if err != nil {
		a.logger.Error("AI tagging status", "error", err)
		return aiTaggingView{SummaryText: "状态暂不可用"}
	}
	return buildAITaggingView(snap)
}

// ---------------------------------------------------------------- per-track tags

type trackTagsJSON struct {
	Kind       string  `json:"kind"`
	Tag        string  `json:"tag"`
	Label      string  `json:"label"`
	Weight     float64 `json:"weight"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"`
	Internal   bool    `json:"internal,omitempty"` // 内部负面标记，不用于展示
}

func tagJSON(tags []storage.TrackTag) []trackTagsJSON {
	out := make([]trackTagsJSON, 0, len(tags))
	for _, t := range tags {
		v := trackTagsJSON{Kind: t.Kind, Tag: t.Tag, Label: tagging.TagLabel(t.Kind, t.Tag), Weight: t.Weight, Confidence: t.Confidence, Source: t.Source}
		if m, ok := tagging.MoodByID(t.Tag); ok && t.Kind == "mood" {
			v.Internal = m.Negative
		}
		out = append(out, v)
	}
	return out
}

func (a *App) trackTagsPayload(ctx context.Context, trackID int64) (map[string]any, error) {
	tags, err := a.store.TrackTags(ctx, trackID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"trackId": trackID, "effective": tagJSON(storage.EffectiveTags(tags)), "all": tagJSON(tags)}
	if meta, ok, err := a.store.TrackTagMeta(ctx, trackID); err != nil {
		return nil, err
	} else if ok {
		m := map[string]any{"model": meta.Model, "taggedAt": meta.TaggedAt, "usedAudio": meta.UsedAudio, "usedLyrics": meta.UsedLyrics, "needsReview": meta.NeedsReview}
		if meta.Energy > 0 {
			m["energy"] = meta.Energy
		}
		if meta.Vocal != "" {
			m["vocal"] = meta.Vocal
		}
		if meta.Idol != "" {
			m["idol"] = meta.Idol
		}
		if meta.ReviewedBy != "" {
			m["reviewedBy"] = meta.ReviewedBy
		}
		out["meta"] = m
	}
	return out, nil
}

func (a *App) handleAPITrackTags(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if _, err := a.store.TaggingTrackInfo(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusNotFound, "not_found", "Track not found.")
		return
	} else if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	payload, err := a.trackTagsPayload(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// userTagsFrom validates a list of tag ids for one kind; weights are split equally.
func userTagsFrom(kind string, ids []string) ([]storage.TrackTag, error) {
	seen := map[string]bool{}
	var out []storage.TrackTag
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		var ok bool
		if kind == "mood" {
			_, ok = tagging.MoodByID(id)
		} else {
			_, ok = tagging.StyleByID(id)
		}
		if !ok {
			return nil, fmt.Errorf("unknown %s tag %q", kind, id)
		}
		seen[id] = true
		out = append(out, storage.TrackTag{Kind: kind, Tag: id})
	}
	if len(out) > 3 {
		return nil, fmt.Errorf("at most 3 %s tags", kind)
	}
	for i := range out {
		out[i].Weight = math.Round(1/float64(len(out))*100) / 100
	}
	return out, nil
}

// handleAPIPutTrackTags sets user corrections. Body: {"moods":["cheerful"],"styles":["pop"]};
// an omitted kind is unchanged, an empty list clears the correction for that kind.
func (a *App) handleAPIPutTrackTags(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	var body struct {
		Moods  *[]string `json:"moods"`
		Styles *[]string `json:"styles"`
	}
	if !decode(w, r, &body) {
		return
	}
	for kind, list := range map[string]*[]string{"mood": body.Moods, "style": body.Styles} {
		if list == nil {
			continue
		}
		tags, err := userTagsFrom(kind, *list)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_tags", err.Error())
			return
		}
		if err := a.store.SetUserTrackTags(r.Context(), id, kind, tags); errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "Track not found.")
			return
		} else if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "update_failed", err.Error())
			return
		}
	}
	payload, err := a.trackTagsPayload(r.Context(), id)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

type trackTagOption struct {
	ID, Label, Hint string
	Checked         bool
}

type trackTagsPageData struct {
	Chrome
	Notice       string
	Info         storage.TaggingTrackInfo
	Effective    []aiTagChip
	LLM          []aiTagChip
	User         []aiTagChip
	Meta         *storage.TrackTagMeta
	ReviewText   string
	Evidence     *tagging.AudioEvidence
	MoodOptions  []trackTagOption
	NegOptions   []trackTagOption
	StyleOptions []trackTagOption
	HasUser      bool
	ReturnTo     string
}

func (a *App) handleTrackTagsPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	ctx := r.Context()
	id := parseInt64(r.PathValue("id"))
	info, err := a.store.TaggingTrackInfo(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.renderAdminFeatureError(w, "library", err)
		return
	}
	tags, err := a.store.TrackTags(ctx, id)
	if err != nil {
		a.renderAdminFeatureError(w, "library", err)
		return
	}
	data := trackTagsPageData{Chrome: a.chromeFor(ctx, session, "library"), Notice: r.URL.Query().Get("notice"), Info: info,
		ReturnTo: safeAdminReturnTo(r.URL.Query().Get("returnTo"), "")}
	eff := storage.EffectiveTags(tags)
	data.Effective = append(chipsFor(eff, "mood"), chipsFor(eff, "style")...)
	var llm, user []storage.TrackTag
	for _, t := range tags {
		switch t.Source {
		case "llm":
			llm = append(llm, t)
		case "user":
			user = append(user, t)
		}
	}
	data.LLM = append(chipsFor(llm, "mood"), chipsFor(llm, "style")...)
	data.User = append(chipsFor(user, "mood"), chipsFor(user, "style")...)
	data.HasUser = len(user) > 0
	if meta, ok, err := a.store.TrackTagMeta(ctx, id); err == nil && ok {
		data.Meta = &meta
		if meta.ReviewReasons != "" {
			var labels []string
			for _, code := range strings.Split(meta.ReviewReasons, ",") {
				labels = append(labels, tagging.ReviewReasonLabel(code))
			}
			data.ReviewText = strings.Join(labels, "、")
		}
	}
	if a.aiTagging != nil {
		if ev, err := a.aiTagging.TrackEvidence(ctx, id); err != nil {
			a.logger.Warn("track audio evidence", "trackId", id, "error", err)
		} else {
			data.Evidence = ev
		}
	}
	checked := map[string]bool{}
	for _, t := range eff {
		checked[t.Kind+":"+t.Tag] = true
	}
	for _, t := range tagging.Moods {
		opt := trackTagOption{ID: t.ID, Label: t.Label, Hint: t.Hint, Checked: checked["mood:"+t.ID]}
		if t.Negative {
			data.NegOptions = append(data.NegOptions, opt)
		} else {
			data.MoodOptions = append(data.MoodOptions, opt)
		}
	}
	for _, t := range tagging.Styles {
		data.StyleOptions = append(data.StyleOptions, trackTagOption{ID: t.ID, Label: t.Label, Hint: t.Hint, Checked: checked["style:"+t.ID]})
	}
	a.render(w, http.StatusOK, "track-tags.html", data)
}

func trackTagsPath(id int64, returnTo string) string {
	p := "/admin/tracks/" + strconv.FormatInt(id, 10) + "/tags"
	if returnTo != "" {
		p += "?returnTo=" + url.QueryEscape(returnTo)
	}
	return p
}

// handleSaveTrackTags saves user corrections from the track tags page.
func (a *App) handleSaveTrackTags(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	back := trackTagsPath(id, safeAdminReturnTo(r.FormValue("returnTo"), ""))
	ctx := r.Context()
	if r.FormValue("action") == "reset" {
		for _, kind := range []string{"mood", "style"} {
			if err := a.store.SetUserTrackTags(ctx, id, kind, nil); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		redirectWithNotice(w, r, back, "已清除人工修正，恢复使用自动结果")
		return
	}
	moods, err := userTagsFrom("mood", r.Form["mood"])
	if err != nil {
		redirectWithNotice(w, r, back, "情绪最多选 3 个")
		return
	}
	styles, err := userTagsFrom("style", r.Form["style"])
	if err != nil {
		redirectWithNotice(w, r, back, "风格最多选 3 个")
		return
	}
	if len(moods) == 0 || len(styles) == 0 {
		redirectWithNotice(w, r, back, "情绪和风格都至少选择 1 个")
		return
	}
	for kind, tags := range map[string][]storage.TrackTag{"mood": moods, "style": styles} {
		if err := a.store.SetUserTrackTags(ctx, id, kind, tags); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	redirectWithNotice(w, r, back, "已保存人工修正；重新打标不会覆盖")
}

func (a *App) handleRetagTrack(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	back := trackTagsPath(id, safeAdminReturnTo(r.FormValue("returnTo"), ""))
	if err := a.store.ResetTrackTagging(r.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if a.aiTagging != nil {
		a.aiTagging.Wake()
	}
	st, _ := a.store.AISettings(r.Context())
	if !st.TaggingEnabled {
		redirectWithNotice(w, r, back, "已重新排队；AI 打标开启后处理")
		return
	}
	redirectWithNotice(w, r, back, "已重新排队，稍后刷新查看新结果")
}
