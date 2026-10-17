package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/dailymix"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

const dailyMixPath = "/admin/daily-mixes"

// dailyMixWait is how long a first request of the day waits for generation
// (design §9: about 8 s including the model call) before answering 202.
const dailyMixWait = 8 * time.Second

// SetDailyMixes attaches the daily mix service.
func (a *App) SetDailyMixes(service *dailymix.Service) { a.dailyMixes = service }

func (a *App) dailyMixesEnabled(ctx context.Context) bool {
	if a.dailyMixes == nil {
		return false
	}
	set, err := a.store.DailyMixSettings(ctx)
	return err == nil && set.Enabled
}

// ---------------------------------------------------------------- JSON shapes

type dailyMixTagJSON struct {
	Kind  string `json:"kind"`
	Tag   string `json:"tag"`
	Label string `json:"label"`
}

type dailyMixJSON struct {
	ID              int64             `json:"id"`
	Day             string            `json:"day"`
	Slot            int               `json:"slot"`
	Status          string            `json:"status"`
	Title           string            `json:"title"`
	Description     string            `json:"description"`
	ThemeType       string            `json:"themeType"`
	ThemeTypeLabel  string            `json:"themeTypeLabel"`
	ThemeKey        string            `json:"themeKey"`
	Tags            []dailyMixTagJSON `json:"tags"`
	TrackCount      int               `json:"trackCount"`
	DurationMillis  int64             `json:"durationMillis"`
	CoverURLs       []string          `json:"coverUrls"`
	Rating          int               `json:"rating"`
	RatingReasons   []string          `json:"ratingReasons"`
	SavedPlaylistID int64             `json:"savedPlaylistId,omitempty"`
	CreatedAt       string            `json:"createdAt"`
}

type dailyMixTrackJSON struct {
	Position int           `json:"position"`
	Bucket   string        `json:"bucket"`
	Blocked  bool          `json:"blocked,omitempty"`
	Track    storage.Track `json:"track"`
}

func mixTags(m storage.DailyMix) []dailyMixTagJSON {
	var plan dailymix.Plan
	_ = json.Unmarshal([]byte(m.PlanJSON), &plan)
	out := []dailyMixTagJSON{}
	if plan.Mood != "" {
		out = append(out, dailyMixTagJSON{"mood", plan.Mood, tagging.TagLabel("mood", plan.Mood)})
	}
	if plan.Style != "" {
		out = append(out, dailyMixTagJSON{"style", plan.Style, tagging.TagLabel("style", plan.Style)})
	}
	return out
}

func splitReasons(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// dailyMixesJSON builds the list items (cover mosaic: the first four distinct albums).
func (a *App) dailyMixesJSON(ctx context.Context, mixes []storage.DailyMix) ([]dailyMixJSON, error) {
	out := make([]dailyMixJSON, 0, len(mixes))
	itemsBy := map[int64][]storage.DailyMixItem{}
	var trackIDs []int64
	for _, m := range mixes {
		items, err := a.store.DailyMixItems(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		itemsBy[m.ID] = items
		for i, it := range items {
			if i < 12 {
				trackIDs = append(trackIDs, it.TrackID)
			}
		}
	}
	albumOf := map[int64]int64{}
	if len(trackIDs) > 0 {
		tracks, err := a.store.TracksByIDs(ctx, trackIDs)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		for _, t := range tracks {
			albumOf[t.ID] = t.AlbumID
		}
	}
	var albumIDs []int64
	for _, id := range albumOf {
		albumIDs = append(albumIDs, id)
	}
	art, err := a.store.AlbumArtworkURLs(ctx, albumIDs)
	if err != nil {
		return nil, err
	}
	for _, m := range mixes {
		j := dailyMixJSON{ID: m.ID, Day: m.Day, Slot: m.Slot, Status: m.Status, Title: m.Title, Description: m.Description, ThemeType: m.ThemeType,
			ThemeTypeLabel: dailymix.TypeLabel(m.ThemeType), ThemeKey: m.ThemeKey, Tags: mixTags(m), TrackCount: m.TrackCount, DurationMillis: m.DurationMs,
			CoverURLs: []string{}, Rating: m.Rating, RatingReasons: splitReasons(m.RatingReasons), SavedPlaylistID: m.SavedPlaylistID, CreatedAt: m.CreatedAt}
		seen := map[int64]bool{}
		for _, it := range itemsBy[m.ID] {
			album := albumOf[it.TrackID]
			if album == 0 || seen[album] || art[album] == "" {
				continue
			}
			seen[album] = true
			j.CoverURLs = append(j.CoverURLs, art[album])
			if len(j.CoverURLs) == 4 {
				break
			}
		}
		out = append(out, j)
	}
	return out, nil
}

// ---------------------------------------------------------------- API

func (a *App) requireDailyMixes(w http.ResponseWriter) bool {
	if a.dailyMixes == nil {
		writeAPIError(w, http.StatusNotFound, "not_available", "Daily mixes are not available.")
		return false
	}
	return true
}

func (a *App) handleAPIDailyMixes(w http.ResponseWriter, r *http.Request) {
	if !a.requireDailyMixes(w) {
		return
	}
	ctx := r.Context()
	status, day, mixes, err := a.dailyMixes.Today(ctx, dailyMixWait)
	if err != nil && ctx.Err() == nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	if ctx.Err() != nil {
		return
	}
	items, err := a.dailyMixesJSON(ctx, mixes)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	_, set, _ := a.dailyMixes.CurrentDay(ctx)
	left, _ := a.dailyMixes.ReplacementsLeft(ctx, day, set)
	body := map[string]any{"status": status, "day": day, "items": items, "replacementsLeft": left}
	code := http.StatusOK
	switch status {
	case dailymix.StatusGenerating:
		code = http.StatusAccepted
		w.Header().Set("Retry-After", "5")
	case dailymix.StatusNotReady:
		body["message"] = dailymix.NotReadyText
	}
	writeJSON(w, code, body)
}

func (a *App) dailyMixForRequest(w http.ResponseWriter, r *http.Request) (storage.DailyMix, bool) {
	if !a.requireDailyMixes(w) {
		return storage.DailyMix{}, false
	}
	id, ok := positiveID(r.PathValue("id"))
	if !ok {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid mix id.")
		return storage.DailyMix{}, false
	}
	m, err := a.store.DailyMixByID(r.Context(), id)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return storage.DailyMix{}, false
	}
	return m, true
}

func (a *App) handleAPIDailyMix(w http.ResponseWriter, r *http.Request) {
	m, ok := a.dailyMixForRequest(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	list, err := a.dailyMixesJSON(ctx, []storage.DailyMix{m})
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	items, err := a.store.DailyMixItems(ctx, m.ID)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.TrackID
	}
	tracks, err := a.store.TracksByIDs(ctx, ids)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	blocked, err := a.store.RecommendationBlocks(ctx, ids)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	out := make([]dailyMixTrackJSON, len(items))
	for i, it := range items {
		out[i] = dailyMixTrackJSON{Position: it.Position, Bucket: it.Bucket, Blocked: blocked[it.TrackID], Track: tracks[i]}
	}
	writeJSON(w, http.StatusOK, map[string]any{"mix": list[0], "tracks": out})
}

func (a *App) handleAPIRateDailyMix(w http.ResponseWriter, r *http.Request) {
	m, ok := a.dailyMixForRequest(w, r)
	if !ok {
		return
	}
	var in struct {
		Value   *int     `json:"value"`
		Reasons []string `json:"reasons"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Value == nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", "value is required (-1, 0 or 1).")
		return
	}
	updated, err := a.store.RateDailyMix(r.Context(), m.ID, *in.Value, in.Reasons, time.Now())
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	list, err := a.dailyMixesJSON(r.Context(), []storage.DailyMix{updated})
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (a *App) handleAPIRegenerateDailyMix(w http.ResponseWriter, r *http.Request) {
	m, ok := a.dailyMixForRequest(w, r)
	if !ok {
		return
	}
	next, _, err := a.dailyMixes.Replace(r.Context(), m.ID, false)
	switch {
	case errors.Is(err, dailymix.ErrReplaceLimit):
		writeAPIError(w, http.StatusTooManyRequests, "replacement_limit", "今天换一个的次数已用完。")
		return
	case errors.Is(err, dailymix.ErrNotActive):
		writeAPIError(w, http.StatusConflict, "not_active", "这个歌单已被替换或不是今天的歌单。")
		return
	case errors.Is(err, dailymix.ErrNotReady):
		writeAPIError(w, http.StatusConflict, "no_theme", "暂时没有可替换的主题。")
		return
	case err != nil:
		a.writeFeatureError(w, r, err, "regenerate_failed")
		return
	}
	list, err := a.dailyMixesJSON(r.Context(), []storage.DailyMix{next})
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

func (a *App) handleAPISaveDailyMix(w http.ResponseWriter, r *http.Request) {
	m, ok := a.dailyMixForRequest(w, r)
	if !ok {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		in.Name = m.Title + " · " + m.Day
	}
	if in.Description == "" {
		in.Description = m.Description
	}
	p, err := a.store.SaveDailyMixAsPlaylist(r.Context(), m.ID, in.Name, in.Description)
	if err != nil {
		a.writeFeatureError(w, r, err, "create_failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"playlist": p})
}

func (a *App) handleAPIRecommendationBlock(blocked bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := positiveID(r.PathValue("id"))
		if !ok {
			writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid track id.")
			return
		}
		if err := a.store.SetRecommendationBlock(r.Context(), id, blocked); err != nil {
			a.writeFeatureError(w, r, err, "update_failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *App) handleAPIDailyMixSettings(w http.ResponseWriter, r *http.Request) {
	set, err := a.store.DailyMixSettings(r.Context())
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (a *App) handleAPIPatchDailyMixSettings(w http.ResponseWriter, r *http.Request) {
	set, err := a.store.DailyMixSettings(r.Context())
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	var in struct {
		Enabled         *bool `json:"enabled"`
		MixCount        *int  `json:"mixCount"`
		TrackCount      *int  `json:"trackCount"`
		ExplorePercent  *int  `json:"explorePercent"`
		PregenerateHour *int  `json:"pregenerateHour"`
		RegenerateLimit *int  `json:"regenerateLimit"`
		AllowSad        *bool `json:"allowSad"`
		LLMTitles       *bool `json:"llmTitles"`
	}
	if !decode(w, r, &in) {
		return
	}
	setBool := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	setInt := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	setBool(&set.Enabled, in.Enabled)
	setInt(&set.MixCount, in.MixCount)
	setInt(&set.TrackCount, in.TrackCount)
	setInt(&set.ExplorePercent, in.ExplorePercent)
	setInt(&set.PregenerateHour, in.PregenerateHour)
	setInt(&set.RegenerateLimit, in.RegenerateLimit)
	setBool(&set.AllowSad, in.AllowSad)
	setBool(&set.LLMTitles, in.LLMTitles)
	if err := a.store.SaveDailyMixSettings(r.Context(), set); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if a.dailyMixes != nil {
		a.dailyMixes.Wake()
	}
	set, _ = a.store.DailyMixSettings(r.Context())
	writeJSON(w, http.StatusOK, set)
}

// ---------------------------------------------------------------- admin page

type dailyMixTrackView struct {
	Position   int
	TrackID    int64
	Title      string
	Artist     string
	Bucket     string
	Blocked    bool
	Reason     dailymix.Reason
	ViaLabel   string
	Moods      []aiTagChip
	Styles     []aiTagChip
	TagSource  string
	DurationMs int64
}

type dailyMixView struct {
	Mix       storage.DailyMix
	JSON      dailyMixJSON
	Plan      dailymix.Plan
	TypeLabel string
	Duration  string
	Tracks    []dailyMixTrackView
	Familiar  int
	Explore   int
	ByVia     string
}

type dailyMixHistoryView struct {
	Day    string
	Titles []string
}

type dailyMixPageData struct {
	Chrome
	Notice           string
	Settings         storage.DailyMixSettings
	Snapshot         dailymix.Snapshot
	Status           string
	Day              string
	Mixes            []dailyMixView
	ReplacementsLeft int
	Blocked          int
	AIConfigured     bool
	History          []dailyMixHistoryView
	RunLines         []string
	RunError         string
	RunShortlist     []string
	RunSkipped       string
	MixCountOptions  []int
	HourOptions      []int
}

var viaLabels = map[string]string{"tag": "标签命中", "adjacent": "相邻标签", "acoustic": "听感相近", "work": "作品", "composer": "创作者", "artist": "歌手本人", "similar": "听感相近", "bridge": "熟悉开场"}

func formatMixDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h > 0 {
		return fmt.Sprintf("%d 小时 %d 分", h, m)
	}
	return fmt.Sprintf("%d 分钟", m)
}

func (a *App) dailyMixViews(ctx context.Context, mixes []storage.DailyMix) ([]dailyMixView, error) {
	list, err := a.dailyMixesJSON(ctx, mixes)
	if err != nil {
		return nil, err
	}
	var out []dailyMixView
	for i, m := range mixes {
		v := dailyMixView{Mix: m, JSON: list[i], TypeLabel: dailymix.TypeLabel(m.ThemeType), Duration: formatMixDuration(m.DurationMs)}
		_ = json.Unmarshal([]byte(m.PlanJSON), &v.Plan)
		items, err := a.store.DailyMixItems(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, len(items))
		for k, it := range items {
			ids[k] = it.TrackID
		}
		tracks, err := a.store.TracksByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		tags, err := a.store.TrackTagsByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		blocked, err := a.store.RecommendationBlocks(ctx, ids)
		if err != nil {
			return nil, err
		}
		var vias []string
		for k, it := range items {
			tv := dailyMixTrackView{Position: it.Position + 1, TrackID: it.TrackID, Title: tracks[k].Title, Artist: tracks[k].Artist, Bucket: it.Bucket,
				Blocked: blocked[it.TrackID], DurationMs: tracks[k].DurationMillis}
			_ = json.Unmarshal([]byte(it.ReasonJSON), &tv.Reason)
			tv.ViaLabel = viaLabels[tv.Reason.Via]
			eff := storage.EffectiveTags(tags[it.TrackID])
			tv.Moods, tv.Styles = chipsFor(eff, "mood"), chipsFor(eff, "style")
			if len(eff) > 0 {
				tv.TagSource = eff[0].Source
			}
			if it.Bucket == "familiar" {
				v.Familiar++
			} else {
				v.Explore++
			}
			v.Tracks = append(v.Tracks, tv)
		}
		for _, via := range []string{"tag", "work", "composer", "artist", "similar", "adjacent", "acoustic", "bridge"} {
			if n := v.Plan.ByVia[via]; n > 0 {
				vias = append(vias, fmt.Sprintf("%s %d", viaLabels[via], n))
			}
		}
		v.ByVia = strings.Join(vias, " · ")
		out = append(out, v)
	}
	return out, nil
}

func (a *App) handleDailyMixPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	ctx := r.Context()
	if a.dailyMixes == nil {
		a.renderAdminFeatureError(w, "console", errors.New("每日推荐不可用"))
		return
	}
	day, set, err := a.dailyMixes.CurrentDay(ctx)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	data := dailyMixPageData{Chrome: a.chromeFor(ctx, session, "daily-mixes"), Notice: r.URL.Query().Get("notice"), Settings: set, Day: day,
		Snapshot: a.dailyMixes.Snapshot(ctx), MixCountOptions: []int{1, 2, 3, 4, 5, 6, 7, 8}}
	for h := 0; h < 24; h++ {
		data.HourOptions = append(data.HourOptions, h)
	}
	mixes, err := a.store.DailyMixes(ctx, day)
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	if data.Mixes, err = a.dailyMixViews(ctx, mixes); err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	switch {
	case !set.Enabled:
		data.Status = "已关闭"
	case len(mixes) > 0:
		data.Status = fmt.Sprintf("今天（%s）的 %d 个歌单已生成", day, len(mixes))
	case data.Snapshot.Generating:
		data.Status = "正在生成…"
	case data.Snapshot.LastRun != nil && data.Snapshot.LastRun.Day == day && data.Snapshot.LastRun.Error != "":
		data.Status = "未能生成"
	default:
		data.Status = fmt.Sprintf("今天的歌单还没有生成（每天 %02d:00 预生成，首次打开时也会生成）", set.PregenerateHour)
	}
	if run := data.Snapshot.LastRun; run != nil {
		kind := "生成当天歌单"
		if run.Kind == "replace" {
			kind = "换一个"
		}
		data.RunLines = append(data.RunLines,
			fmt.Sprintf("最近一次：%s %s（推荐日 %s），用时 %.1f 秒", run.StartedAt.In(homeZone).Format("01-02 15:04"), kind, run.Day, float64(run.Millis)/1000),
			fmt.Sprintf("可播放曲目 %d 首，其中有标签 %d 首、有声学特征 %d 首；可用主题 %d 个，冷却后可选 %d 个，生成 %d 个歌单", run.Tracks, run.Tagged, run.Acoustic, run.Candidates, run.Eligible, run.Mixes),
			"标题："+run.LLM)
		if len(run.ProfileMood) > 0 || len(run.ProfileStyle) > 0 {
			data.RunLines = append(data.RunLines, "近期偏好：情绪 "+orDash(strings.Join(run.ProfileMood, "、"))+"；风格 "+orDash(strings.Join(run.ProfileStyle, "、")))
		}
		data.RunError = run.Error
		data.RunShortlist = run.Shortlist
		data.RunSkipped = strings.Join(run.Skipped, "、")
	}
	data.ReplacementsLeft, _ = a.dailyMixes.ReplacementsLeft(ctx, day, set)
	data.Blocked, _ = a.store.CountRecommendationBlocks(ctx)
	if ai, err := a.store.AISettings(ctx); err == nil {
		data.AIConfigured = ai.Configured()
	}
	history, err := a.store.DailyMixHistory(ctx, storage.DayAfter(day, -7), storage.DayAfter(day, -1))
	if err == nil {
		byDay := map[string]*dailyMixHistoryView{}
		for _, m := range history {
			if m.Status != "active" {
				continue
			}
			h := byDay[m.Day]
			if h == nil {
				data.History = append(data.History, dailyMixHistoryView{Day: m.Day})
				h = &data.History[len(data.History)-1]
				byDay[m.Day] = h
			}
			h.Titles = append(h.Titles, m.Title)
		}
	}
	a.render(w, http.StatusOK, "daily-mixes.html", data)
}

func (a *App) handleSaveDailyMixSettings(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	set, err := a.store.DailyMixSettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	atoi := func(name string, def int) int {
		v, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name)))
		if err != nil {
			return def
		}
		return v
	}
	set.Enabled = r.FormValue("enabled") != ""
	set.AllowSad = r.FormValue("allow_sad") != ""
	set.LLMTitles = r.FormValue("llm_titles") != ""
	set.MixCount = atoi("mix_count", set.MixCount)
	set.TrackCount = atoi("track_count", set.TrackCount)
	set.ExplorePercent = atoi("explore_percent", set.ExplorePercent)
	set.PregenerateHour = atoi("pregenerate_hour", set.PregenerateHour)
	set.RegenerateLimit = atoi("regenerate_limit", set.RegenerateLimit)
	if err := a.store.SaveDailyMixSettings(ctx, set); err != nil {
		redirectWithNotice(w, r, dailyMixPath, err.Error())
		return
	}
	if a.dailyMixes != nil {
		a.dailyMixes.Wake()
	}
	redirectWithNotice(w, r, dailyMixPath, "已保存。歌单数量、曲数等变化从下一次生成开始生效；可点“重新生成今天的歌单”立即应用。")
}

func (a *App) handleRegenerateDailyMixesToday(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.dailyMixes == nil {
		redirectWithNotice(w, r, dailyMixPath, "每日推荐不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := a.dailyMixes.RegenerateToday(ctx); err != nil {
		redirectWithNotice(w, r, dailyMixPath, "未能生成："+err.Error())
		return
	}
	redirectWithNotice(w, r, dailyMixPath, "已重新生成今天的歌单")
}

func (a *App) handleAdminReplaceDailyMix(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, ok := positiveID(r.PathValue("id"))
	if !ok || a.dailyMixes == nil {
		http.NotFound(w, r)
		return
	}
	next, _, err := a.dailyMixes.Replace(r.Context(), id, true)
	if err != nil {
		msg := err.Error()
		switch {
		case errors.Is(err, dailymix.ErrNotActive):
			msg = "这个歌单已被替换或不是今天的歌单"
		case errors.Is(err, dailymix.ErrNotReady):
			msg = "暂时没有可替换的主题"
		}
		redirectWithNotice(w, r, dailyMixPath, "未能换一个："+msg)
		return
	}
	redirectWithNotice(w, r, dailyMixPath+"#mix-"+strconv.FormatInt(next.ID, 10), "已换成「"+next.Title+"」（管理页换一个不计入每日次数）")
}

func (a *App) handleAdminBlockTrack(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id, ok := positiveID(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	blocked := r.FormValue("blocked") == "1"
	if err := a.store.SetRecommendationBlock(r.Context(), id, blocked); err != nil {
		redirectWithNotice(w, r, dailyMixPath, err.Error())
		return
	}
	back := r.FormValue("return")
	if !strings.HasPrefix(back, "/admin/") || safeAdminReturnTo(strings.SplitN(back, "#", 2)[0], "") == "" {
		back = dailyMixPath
	}
	if blocked {
		redirectWithNotice(w, r, back, "已设为不再推荐（从下一次生成开始生效）")
	} else {
		redirectWithNotice(w, r, back, "已恢复推荐")
	}
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// dashboardDailyMixSummary is the dashboard card line.
func (a *App) dashboardDailyMixSummary(ctx context.Context) string {
	if a.dailyMixes == nil {
		return "不可用"
	}
	day, set, err := a.dailyMixes.CurrentDay(ctx)
	if err != nil {
		return ""
	}
	if !set.Enabled {
		return "已关闭"
	}
	mixes, err := a.store.DailyMixes(ctx, day)
	if err != nil {
		return ""
	}
	if len(mixes) == 0 {
		if snap := a.dailyMixes.Snapshot(ctx); snap.LastRun != nil && snap.LastRun.Day == day && snap.LastRun.Error != "" {
			return "今天未能生成"
		}
		return "今天的歌单尚未生成"
	}
	titles := make([]string, 0, len(mixes))
	for _, m := range mixes {
		titles = append(titles, m.Title)
	}
	return fmt.Sprintf("今天 %d 个：%s", len(mixes), strings.Join(titles, "、"))
}
