package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/lyricstrans"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

const (
	lyricsTranslationPath    = "/admin/lyrics-translations"
	lyricsTranslationPerPage = 50
	lyricsTranslationTimeout = 3 * time.Minute
)

// translationChat creates the model client; tests replace it.
type translationChatFactory func(storage.AISettings) (tagging.Chat, error)

func defaultTranslationChat(st storage.AISettings) (tagging.Chat, error) {
	return tagging.NewChatClient(st.BaseURL, st.APIKey, st.ProxyURL, lyricsTranslationTimeout)
}

// flightGroup collapses concurrent calls with the same key (auto translate +
// a manual click, several clients playing the same song) into one.
type flightGroup[T any] struct {
	mu sync.Mutex
	m  map[string]*flight[T]
}

type flight[T any] struct {
	done chan struct{}
	v    T
	err  error
}

func (f *flightGroup[T]) do(key string, fn func() (T, error)) (T, error) {
	f.mu.Lock()
	if f.m == nil {
		f.m = map[string]*flight[T]{}
	}
	if fl, ok := f.m[key]; ok {
		f.mu.Unlock()
		<-fl.done
		return fl.v, fl.err
	}
	fl := &flight[T]{done: make(chan struct{})}
	f.m[key] = fl
	f.mu.Unlock()
	fl.v, fl.err = fn()
	f.mu.Lock()
	delete(f.m, key)
	f.mu.Unlock()
	close(fl.done)
	return fl.v, fl.err
}

func (a *App) translationChat() translationChatFactory {
	if a.newTranslationChat != nil {
		return a.newTranslationChat
	}
	return defaultTranslationChat
}

// lyricsTranslationEnabled is used by /api/v1/capabilities ("lyricsTranslation").
func (a *App) lyricsTranslationEnabled(ctx context.Context) bool {
	ai, err := a.store.AISettings(ctx)
	if err != nil {
		return false
	}
	tr, err := a.store.LyricsTranslationSettings(ctx)
	return err == nil && tr.Enabled && storage.TranslationConfigured(ai, tr)
}

// runLyricsTranslation calls the model, records usage and stores the result.
func (a *App) runLyricsTranslation(ctx context.Context, ai storage.AISettings, model string, v storage.LyricsTranslation) (storage.LyricsTranslation, error) {
	chat, err := a.translationChat()(ai)
	if err != nil {
		return v, err
	}
	tr, err := a.store.LyricsTranslationSettings(ctx)
	if err != nil {
		return v, err
	}
	fallback := strings.TrimSpace(tr.FallbackModel)
	call := func(name string, timeout time.Duration) (lyricstrans.Result, error) {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		res, callErr := lyricstrans.Translate(callCtx, chat, name, ai.ReasoningEffort, v.TargetLanguage, v.SourceLines)
		if res.Usage.Requests > 0 {
			if uerr := a.store.AddAIUsage(ctx, time.Now(), name, "translate", res.Usage); uerr != nil {
				a.logger.Error("record lyrics translation usage", "error", uerr)
			}
		}
		if errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() == nil {
			callErr = &tagging.LLMError{Kind: tagging.KindNetwork, Message: "翻译模型请求超时"}
		}
		return res, callErr
	}
	// Reserve half of the overall deadline for fallback after a primary timeout.
	primaryTimeout := lyricsTranslationTimeout
	if fallback != "" && fallback != model {
		if deadline, ok := ctx.Deadline(); ok {
			primaryTimeout = time.Until(deadline)
		}
		primaryTimeout /= 2
	}
	res, err := call(model, primaryTimeout)
	total := res.Usage
	if tagging.ShouldFallback(ctx, model, fallback, err) {
		a.logger.Warn("translation primary unavailable; trying fallback", "model", model, "fallback", fallback)
		model = fallback
		res, err = call(model, lyricsTranslationTimeout)
		total.Add(res.Usage)
	}
	if err != nil {
		return v, err
	}
	v.TranslatedLines, v.Model = res.Lines, model
	v.PromptTokens, v.CompletionTokens = total.PromptTokens, total.CompletionTokens
	if v.ID, err = a.store.SaveLyricsTranslation(ctx, v); err != nil {
		return v, err
	}
	return v, nil
}

type lyricsTranslationRequest struct {
	TargetLanguage string `json:"targetLanguage"`
}

type lyricsTranslationResponse struct {
	ID             int64    `json:"id"`
	TargetLanguage string   `json:"targetLanguage"`
	Lines          []string `json:"lines"`
	Model          string   `json:"model"`
	Cached         bool     `json:"cached"`
	UpdatedAt      string   `json:"updatedAt,omitempty"`
	// Source is "ai" for model translations, or the lyrics source whose own translation was used (netease).
	Source string `json:"source"`
}

// handleAPILyricsTranslation translates the track's lyrics (as returned by
// GET /tracks/{id}/lyrics) line by line. NetEase's own Simplified Chinese
// translation is used when the lyrics come from NetEase; otherwise the AI
// translation is returned from cache or created (and cached).
func (a *App) handleAPILyricsTranslation(w http.ResponseWriter, r *http.Request) {
	trackID := parseInt64(r.PathValue("id"))
	var body lyricsTranslationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Request body must be JSON {targetLanguage}.")
		return
	}
	if !lyricstrans.ValidLanguage(body.TargetLanguage) {
		writeAPIError(w, http.StatusBadRequest, "invalid_language", "targetLanguage must be a language code such as en or zh-Hans.")
		return
	}
	ctx := r.Context()
	resolved, err := a.resolveLyrics(ctx, trackID)
	if a.writeLyricsError(w, trackID, err) {
		return
	}
	if resolved.TranslationLRC != "" && lyricsonline.IsSimplifiedChinese(body.TargetLanguage) {
		if aligned, ok := lyricsonline.AlignTranslation(resolved.Result.Lines, resolved.TranslationLRC); ok {
			writeJSON(w, http.StatusOK, lyricsTranslationResponse{TargetLanguage: body.TargetLanguage, Lines: aligned,
				Model: lyricsonline.SourceName(resolved.Source), Cached: true, Source: resolved.Source})
			return
		}
	}
	original := make([]string, len(resolved.Result.Lines))
	for i, l := range resolved.Result.Lines {
		original[i] = l.Text
	}
	lines, err := lyricstrans.NormalizeLines(original)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "lyrics_too_long", err.Error())
		return
	}
	ai, err := a.store.AISettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	tr, err := a.store.LyricsTranslationSettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if !tr.Enabled || !storage.TranslationConfigured(ai, tr) {
		writeAPIError(w, http.StatusConflict, "translation_disabled", "Lyrics translation is not enabled on this server.")
		return
	}
	hash := storage.LyricsSourceHash(lines)
	if cached, err := a.store.CachedLyricsTranslation(ctx, trackID, body.TargetLanguage, hash); err == nil {
		writeJSON(w, http.StatusOK, translationResponse(cached, true))
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	key := fmt.Sprintf("%d|%s|%s", trackID, body.TargetLanguage, hash)
	v, err := a.translationFlights.do(key, func() (storage.LyricsTranslation, error) {
		// Finish (and cache) even if this client gives up waiting.
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lyricsTranslationTimeout)
		defer cancel()
		return a.runLyricsTranslation(runCtx, ai, storage.EffectiveTranslationModel(ai, tr),
			storage.LyricsTranslation{TrackID: trackID, TargetLanguage: body.TargetLanguage, SourceHash: hash, SourceLines: lines})
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, http.StatusNotFound, "not_found", "Track not found.")
			return
		}
		a.logger.Warn("lyrics translation failed", "trackId", trackID, "language", body.TargetLanguage, "error", err)
		writeAPIError(w, http.StatusBadGateway, "translation_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, translationResponse(v, false))
}

func translationResponse(v storage.LyricsTranslation, cached bool) lyricsTranslationResponse {
	return lyricsTranslationResponse{ID: v.ID, TargetLanguage: v.TargetLanguage, Lines: v.TranslatedLines, Model: v.Model, Cached: cached,
		UpdatedAt: v.UpdatedAt, Source: "ai"}
}

// ---------------------------------------------------------------- admin

type lyricsTranslationPageData struct {
	Chrome
	Notice        string
	Enabled       bool
	Model         string
	TaggingModel  string
	FallbackModel string
	Configured    bool // base URL + key set (models aside)
	Effective     string
	Query         string
	Language      string
	Languages     []string
	Items         []storage.LyricsTranslation
	Total         int64
	Page          int
	Pages         int
	PrevURL       string
	NextURL       string
	ReturnURL     string
}

func (a *App) handleLyricsTranslationsPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	ctx := r.Context()
	ai, err := a.store.AISettings(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	tr, err := a.store.LyricsTranslationSettings(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	q := r.URL.Query()
	data := lyricsTranslationPageData{Chrome: a.chromeFor(ctx, session, "console"), Notice: q.Get("notice"), Enabled: tr.Enabled, Model: tr.Model, FallbackModel: tr.FallbackModel,
		TaggingModel: ai.Model, Configured: strings.TrimSpace(ai.BaseURL) != "" && ai.APIKey != "", Effective: storage.EffectiveTranslationModel(ai, tr),
		Query: strings.TrimSpace(q.Get("q")), Language: q.Get("lang")}
	data.Page, _ = strconv.Atoi(q.Get("page"))
	if data.Page < 1 {
		data.Page = 1
	}
	data.Items, data.Total, err = a.store.ListLyricsTranslations(ctx, storage.LyricsTranslationFilter{Query: data.Query, Language: data.Language,
		Limit: lyricsTranslationPerPage, Offset: (data.Page - 1) * lyricsTranslationPerPage})
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	if data.Languages, err = a.store.LyricsTranslationLanguages(ctx); err != nil {
		a.logger.Error("lyrics translation languages", "error", err)
	}
	data.Pages = int((data.Total + lyricsTranslationPerPage - 1) / lyricsTranslationPerPage)
	pageURL := func(p int) string {
		v := url.Values{}
		if data.Query != "" {
			v.Set("q", data.Query)
		}
		if data.Language != "" {
			v.Set("lang", data.Language)
		}
		v.Set("page", strconv.Itoa(p))
		return lyricsTranslationPath + "?" + v.Encode()
	}
	data.ReturnURL = pageURL(data.Page)
	if data.Page > 1 {
		data.PrevURL = pageURL(data.Page - 1)
	}
	if data.Page < data.Pages {
		data.NextURL = pageURL(data.Page + 1)
	}
	a.render(w, http.StatusOK, "lyrics-translations.html", data)
}

func (a *App) handleSaveLyricsTranslationSettings(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	next := storage.LyricsTranslationSettings{Enabled: r.FormValue("enabled") != "", Model: strings.TrimSpace(r.FormValue("model")), FallbackModel: strings.TrimSpace(r.FormValue("fallback_model"))}
	ai, err := a.store.AISettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if next.Enabled && !storage.TranslationConfigured(ai, next) {
		redirectWithNotice(w, r, lyricsTranslationPath, "开启翻译前需要在“AI 打标”页填写接口地址和 API Key，并填写翻译模型（或打标主模型）")
		return
	}
	if err := a.store.SaveLyricsTranslationSettings(ctx, next); err != nil {
		redirectWithNotice(w, r, lyricsTranslationPath, "保存失败："+err.Error())
		return
	}
	if next.Enabled {
		redirectWithNotice(w, r, lyricsTranslationPath, "歌词翻译已开启，客户端可以使用翻译功能（模型："+storage.EffectiveTranslationModel(ai, next)+"）")
		return
	}
	redirectWithNotice(w, r, lyricsTranslationPath, "设置已保存，歌词翻译未开启；已缓存的翻译会保留")
}

func formIDs(r *http.Request) []int64 {
	_ = r.ParseForm()
	var ids []int64
	for _, raw := range r.Form["id"] {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// safeReturn keeps redirects on this page (list filters / detail).
func safeReturn(raw string) string {
	if strings.HasPrefix(raw, lyricsTranslationPath) && !strings.Contains(raw, "//") {
		return raw
	}
	return lyricsTranslationPath
}

func (a *App) handleDeleteLyricsTranslations(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ids := formIDs(r)
	back := safeReturn(r.FormValue("return"))
	if len(ids) == 0 {
		redirectWithNotice(w, r, back, "没有选择要删除的翻译")
		return
	}
	n, err := a.store.DeleteLyricsTranslations(r.Context(), ids)
	if err != nil {
		redirectWithNotice(w, r, back, "删除失败："+err.Error())
		return
	}
	redirectWithNotice(w, r, back, fmt.Sprintf("已删除 %d 条翻译；客户端下次请求时会重新翻译", n))
}

type lyricsTranslationRow struct {
	Source, Translated string
}

type lyricsTranslationDetailData struct {
	Chrome
	Notice       string
	Item         storage.LyricsTranslation
	Rows         []lyricsTranslationRow
	LanguageName string
	Configured   bool
	Models       []string
	DefaultModel string
}

func (a *App) handleLyricsTranslationDetail(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	ctx := r.Context()
	item, err := a.store.LyricsTranslationByID(ctx, parseInt64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		redirectWithNotice(w, r, lyricsTranslationPath, "这条翻译不存在或已被删除")
		return
	}
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	ai, err := a.store.AISettings(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	tr, err := a.store.LyricsTranslationSettings(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	data := lyricsTranslationDetailData{Chrome: a.chromeFor(ctx, session, "console"), Notice: r.URL.Query().Get("notice"), Item: item,
		LanguageName: lyricstrans.LanguageName(item.TargetLanguage), Configured: strings.TrimSpace(ai.BaseURL) != "" && ai.APIKey != "",
		DefaultModel: storage.EffectiveTranslationModel(ai, tr)}
	seen := map[string]bool{}
	for _, m := range []string{data.DefaultModel, tr.Model, ai.Model, ai.ReviewModel, item.Model} {
		if m = strings.TrimSpace(m); m != "" && !seen[m] {
			seen[m] = true
			data.Models = append(data.Models, m)
		}
	}
	for i, src := range item.SourceLines {
		data.Rows = append(data.Rows, lyricsTranslationRow{Source: src, Translated: item.TranslatedLines[i]})
	}
	a.render(w, http.StatusOK, "lyrics-translation.html", data)
}

// handleRetranslateLyrics translates the stored source lines again with the
// chosen model and overwrites the cached result. It works while the client
// switch is off, so translations can be fixed before turning it on.
func (a *App) handleRetranslateLyrics(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := parseInt64(r.PathValue("id"))
	detail := lyricsTranslationPath + "/" + strconv.FormatInt(id, 10)
	item, err := a.store.LyricsTranslationByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		redirectWithNotice(w, r, lyricsTranslationPath, "这条翻译不存在或已被删除")
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ai, err := a.store.AISettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	model := strings.TrimSpace(r.FormValue("model"))
	if model == "" {
		tr, err := a.store.LyricsTranslationSettings(ctx)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		model = storage.EffectiveTranslationModel(ai, tr)
	}
	if strings.TrimSpace(ai.BaseURL) == "" || ai.APIKey == "" || model == "" {
		redirectWithNotice(w, r, detail, "需要先在“AI 打标”页填写接口地址和 API Key，并指定模型")
		return
	}
	start := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, lyricsTranslationTimeout)
	defer cancel()
	if _, err := a.runLyricsTranslation(runCtx, ai, model, item); err != nil {
		redirectWithNotice(w, r, detail, "重新翻译失败（"+model+"）："+err.Error())
		return
	}
	redirectWithNotice(w, r, detail, fmt.Sprintf("已用 %s 重新翻译（%.1f 秒）", model, time.Since(start).Seconds()))
}

func (a *App) dashboardLyricsTranslationSummary(ctx context.Context) string {
	tr, err := a.store.LyricsTranslationSettings(ctx)
	if err != nil {
		return "状态暂不可用"
	}
	_, total, err := a.store.ListLyricsTranslations(ctx, storage.LyricsTranslationFilter{Limit: 1})
	if err != nil {
		return "状态暂不可用"
	}
	state := "未开启"
	if tr.Enabled && a.lyricsTranslationEnabled(ctx) {
		state = "已开启"
	}
	return fmt.Sprintf("%s · 已缓存 %d 条翻译", state, total)
}
