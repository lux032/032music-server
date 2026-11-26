package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/artistaliases"
	"github.com/lux032/032music-server/internal/storage"
)

const artistAliasPath = "/admin/artist-aliases"

func (a *App) SetArtistAliases(s *artistaliases.Service) { a.artistAliases = s }

type artistAliasPageData struct {
	Chrome
	Notice               string
	Settings             storage.ArtistAliasSettings
	KeySet, AIConfigured bool
	WebSearchSummary     string
	Detail               string
	Eligible             int
	Stats                storage.ArtistAliasStats
	Jobs                 []storage.ArtistAliasJob
	Candidates           []storage.ArtistAliasCandidate
	Usage                []storage.AIUsageRow
	Filter               string
}

func (a *App) handleArtistAliasPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, e := a.store.ArtistAliasSettings(ctx)
	if e != nil {
		a.renderAdminFeatureError(w, "artist aliases", e)
		return
	}
	session, _ := a.sessions.get(r)
	wsReady, wsSummary, _, _, _ := a.webSearchStatusSummary(ctx)
	data := artistAliasPageData{Chrome: a.chromeFor(ctx, session, "artist-aliases"), Notice: r.URL.Query().Get("notice"), Settings: st, KeySet: wsReady, WebSearchSummary: wsSummary, Eligible: 0}
	ai, e := a.store.AISettings(ctx)
	if e != nil {
		a.renderAdminFeatureError(w, "artist aliases", e)
		return
	}
	data.AIConfigured = ai.Configured()
	if a.artistAliases != nil {
		data.Detail = a.artistAliases.Detail()
		list, e := a.artistAliases.Eligible(ctx, false)
		if e != nil {
			a.renderAdminFeatureError(w, "artist aliases", e)
			return
		}
		data.Eligible = len(list)
	}
	data.Stats, e = a.store.ArtistAliasStats(ctx)
	if e != nil {
		a.renderAdminFeatureError(w, "artist aliases", e)
		return
	}
	data.Jobs, e = a.store.ArtistAliasJobs(ctx, 50)
	if e != nil {
		a.renderAdminFeatureError(w, "artist aliases", e)
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "accepted", "rejected", "pending":
	default:
		status = "pending"
	}
	data.Filter = status
	artistID := parseInt64(r.URL.Query().Get("artistId"))
	data.Candidates, e = a.store.ArtistAliasCandidates(ctx, status, artistID, 100)
	if e != nil {
		a.renderAdminFeatureError(w, "artist aliases", e)
		return
	}
	usage, e := a.store.AIUsageTotals(ctx, "")
	if e != nil {
		a.renderAdminFeatureError(w, "artist aliases", e)
		return
	}
	for _, u := range usage {
		if u.Purpose == "artist_alias" {
			data.Usage = append(data.Usage, u)
		}
	}
	// Never hand secrets to the renderer, including future accidental fields.
	data.Settings.DeepSeekKey = ""
	a.render(w, 200, "artist-aliases.html", data)
}
func (a *App) handleArtistAliasStatus(w http.ResponseWriter, r *http.Request) {
	st, e := a.store.ArtistAliasSettings(r.Context())
	if e != nil {
		a.writeFeatureError(w, r, e, "query_failed")
		return
	}
	stats, e := a.store.ArtistAliasStats(r.Context())
	if e != nil {
		a.writeFeatureError(w, r, e, "query_failed")
		return
	}
	detail := ""
	if a.artistAliases != nil {
		detail = a.artistAliases.Detail()
	}
	writeJSON(w, 200, map[string]any{"settings": st, "stats": stats, "detail": detail})
}
func (a *App) handleArtistAliasAction(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if a.artistAliases == nil {
		redirectWithNotice(w, r, artistAliasPath, "中文别名补全服务不可用")
		return
	}
	st, e := a.store.ArtistAliasSettings(r.Context())
	if e != nil {
		a.renderAdminFeatureError(w, "artist aliases", e)
		return
	}
	notice := "设置已保存"
	switch r.PathValue("action") {
	case "settings":
		st.Enabled = r.FormValue("enabled") == "on"
		dsChanged := false
		if key := strings.TrimSpace(r.FormValue("deepseek_key")); key != "" {
			st.DeepSeekKey = key
			dsChanged = true
		}
		if r.FormValue("clear_key") == "on" {
			st.DeepSeekKey = ""
			dsChanged = true
		}
		st.SearchLimit, _ = strconv.Atoi(r.FormValue("search_limit"))
		st.FetchLimit, _ = strconv.Atoi(r.FormValue("fetch_limit"))
		st.ModelLimit, _ = strconv.Atoi(r.FormValue("model_limit"))
		st.RPM, _ = strconv.Atoi(r.FormValue("rpm"))
		if dsChanged {
			if ws, wsErr := a.store.WebSearchSettings(r.Context()); wsErr == nil {
				ws.DeepSeekKey = st.DeepSeekKey
				_ = a.store.SaveWebSearchSettings(r.Context(), ws)
			}
		}
	case "pause":
		st.Paused = true
		notice = "已暂停，调用次数和队列已保留"
	case "resume":
		st.Paused = false
		notice = "已继续后台补全"
	case "backfill":
		n, err := a.artistAliases.Backfill(r.Context())
		if err != nil {
			redirectWithNotice(w, r, artistAliasPath, err.Error())
			return
		}
		redirectWithNotice(w, r, artistAliasPath, fmt.Sprintf("已加入 %d 位存量歌手；已排队的歌手不会重复加入", n))
		return
	default:
		http.NotFound(w, r)
		return
	}
	if e = a.store.SaveArtistAliasSettings(r.Context(), st); e != nil {
		redirectWithNotice(w, r, artistAliasPath, e.Error())
		return
	}
	a.artistAliases.Interrupt()
	redirectWithNotice(w, r, artistAliasPath, notice)
}
func (a *App) handleArtistAliasReview(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	action := r.PathValue("action")
	if action != "accept" && action != "reject" {
		http.NotFound(w, r)
		return
	}
	e := a.store.ReviewArtistAlias(r.Context(), parseInt64(r.PathValue("candidateId")), action == "accept", r.FormValue("value"))
	if e != nil {
		notice := "审核未保存，歌手身份或候选状态可能已变化，请刷新重试"
		if errors.Is(e, storage.ErrInvalidSearchAlias) {
			notice = "别名须为 1 至 200 个字符"
		}
		redirectWithNotice(w, r, artistAliasPath, notice)
		return
	}
	notice := "已接受，搜索立即生效"
	if action == "reject" {
		notice = "已拒绝；后续自动任务不会再次添加该名称"
	}
	redirectWithNotice(w, r, artistAliasPath, notice)
}
