package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/storage"
)

type matchReviewItem struct {
	Artist     storage.Artist
	Candidates []storage.ArtistCandidate
}
type artistReleaseGroup struct {
	Title       string
	Description string
	Releases    []storage.Album
}
type identityPageData struct {
	ActiveArtistRun  *artistRunView
	LastArtistRun    *artistRunView
	ArtistRunHistory []artistRunView

	Chrome
	Section, Notice, ReturnTo              string
	Settings                               []storage.MetadataSourceSetting
	BiographySettings                      storage.BiographySettings
	LastFMScrobble                         lastFMScrobbleView
	IdentityConflict                       *storage.ArtistIdentityConflict
	Artist                                 *storage.ArtistDetail
	Artists                                []storage.Artist
	Albums                                 []storage.Album
	ReleaseGroups                          []artistReleaseGroup
	Tracks                                 []storage.Track
	Review                                 []matchReviewItem
	CreditCorrectionReview                 []matchReviewItem
	ReviewTotal, ReviewLimit, ReviewOffset int
	ReviewPage                             int
	ReviewQuery, ReviewSource              string
	// 历史任务分页（GET server 控制，1 起页码）。
	ArtistRunsPage  int
	ArtistRunsTotal int
	ArtistRunsLimit int
	Merges          []storage.MergeOperation
	MatchRuns       []storage.ArtistMatchRun
	ImageBackfill   artistImageBackfillView
	BioBackfill     artistBiographyBackfillView
}

func (a *App) identityBase(r *http.Request, section string) identityPageData {
	session, _ := a.sessions.get(r)
	return identityPageData{Chrome: a.chromeFor(r.Context(), session, identityNavKey(section)), Section: section, Notice: r.URL.Query().Get("notice"), ReturnTo: r.URL.RequestURI()}
}

// identityNavKey maps the identity section to the sidebar navigation key.
func identityNavKey(section string) string {
	switch section {
	case "settings", "matches", "merges":
		return section
	default:
		return "artists"
	}
}

func (a *App) handleMetadataSettings(w http.ResponseWriter, r *http.Request) {
	data := a.identityBase(r, "settings")
	settings, err := a.store.MetadataSourceSettings(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for i := range settings {
		settings[i].APIKey = ""
	}
	data.Settings = settings
	data.BiographySettings, _ = a.store.BiographySettings(r.Context())
	data.LastFMScrobble = a.lastFMScrobbleView(r)
	a.render(w, 200, "metadata-settings.html", data)
}

// metadataScopeLabel renders the source key with its brand name in success
// notices, so users see "MusicBrainz 设置已保存" instead of a raw scope key.
func metadataScopeLabel(scope string) string {
	switch scope {
	case "musicbrainz":
		return "MusicBrainz"
	case "lastfm":
		return "Last.fm"
	case "bangumi":
		return "Bangumi"
	default:
		return scope
	}
}

func (a *App) handleSaveMetadataSettings(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	scope := r.FormValue("scope")
	existingLastFM, _ := a.store.MetadataSourceSetting(r.Context(), "lastfm")
	if scope == "biography" {
		biographySettings := storage.BiographySettings{
			PreferredLanguages: strings.TrimSpace(r.FormValue("biography_languages")),
			SourcePriority:     strings.TrimSpace(r.FormValue("biography_source_priority")),
			WikipediaEnabled:   r.FormValue("wikipedia_enabled") != "",
			EnglishFallback:    r.FormValue("biography_english_fallback") != "",
			CacheDays:          int(parseInt64(r.FormValue("biography_cache_days"))),
		}
		if err := a.store.SaveBiographySettings(r.Context(), biographySettings); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		redirectWithNotice(w, r, "/admin/settings/metadata", "歌手简介策略设置已保存")
		return
	}

	// 单卡独立保存逻辑
	if scope != "" {
		validScope := false
		for _, s := range []string{"musicbrainz", "lastfm", "bangumi"} {
			if scope == s {
				validScope = true
				break
			}
		}
		if validScope {
			setting := storage.MetadataSourceSetting{
				Source:             scope,
				Enabled:            r.FormValue(scope+"_enabled") != "",
				AutoMatch:          r.FormValue(scope+"_auto") != "",
				Priority:           int(parseInt64(r.FormValue(scope + "_priority"))),
				CacheDays:          int(parseInt64(r.FormValue(scope + "_cache_days"))),
				Language:           r.FormValue(scope + "_language"),
				APIKey:             strings.TrimSpace(r.FormValue(scope + "_api_key")),
				ApplicationName:    r.FormValue(scope + "_application_name"),
				ApplicationVersion: r.FormValue(scope + "_application_version"),
				Contact:            r.FormValue(scope + "_contact"),
			}
			if scope == "musicbrainz" && setting.Enabled && strings.TrimSpace(setting.Contact) == "" {
				redirectWithNotice(w, r, "/admin/settings/metadata", "启用 MusicBrainz 时必须填写联系邮箱或项目地址")
				return
			}
			if strings.IndexFunc(setting.Contact, unicode.IsControl) >= 0 {
				redirectWithNotice(w, r, "/admin/settings/metadata", "联系方式不能包含换行等控制字符")
				return
			}
			if scope == "lastfm" && setting.Enabled && setting.APIKey == "" && !existingLastFM.HasAPIKey {
				redirectWithNotice(w, r, "/admin/settings/metadata", "首次启用 Last.fm 时必须填写 API Key")
				return
			}
			if err := a.store.SaveMetadataSourceSetting(r.Context(), setting); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			redirectWithNotice(w, r, "/admin/settings/metadata", fmt.Sprintf("%s 设置已保存", metadataScopeLabel(scope)))
			return
		}
		http.Error(w, "invalid scope", 400)
		return
	}
}

func (a *App) handleCancelArtistMatching(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	message := "任务已停止"
	if a.enrichment == nil {
		message = "任务已结束或不存在"
	} else if err := a.enrichment.CancelArtistMatching(parseInt64(r.PathValue("id"))); errors.Is(err, enrichment.ErrRunNotActive) || errors.Is(err, sql.ErrNoRows) {
		message = "任务已结束或不存在"
	} else if err != nil {
		a.logger.Error("cancel artist run", "error", err)
		message = "停止任务失败，请稍后重试"
	}
	redirectWithNotice(w, r, "/admin/matches", message)
}

func (a *App) handleRunArtistMatching(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if a.enrichment == nil {
		redirectWithNotice(w, r, "/admin/matches", "任务服务不可用")
		return
	}
	if _, err := a.enrichment.StartAll(r.Context()); err != nil {
		message := "启动任务失败，请稍后重试"
		if errors.Is(err, enrichment.ErrNoEligibleArtists) {
			message = "没有需要检查的艺术家"
		} else if errors.Is(err, storage.ErrArtistRunState) || errors.Is(err, enrichment.ErrRunNotActive) {
			message = "已有活动或暂停任务，请查看任务并手动继续"
			if run, e := a.store.UnfinishedDurableArtistRun(r.Context()); e == nil && storage.ArtistRunAutoResumeEligible(run) {
				message = autoResumeNotice(run.WaitingUntil)
			}
		} else {
			a.logger.Error("start artist matching", "error", err)
		}
		redirectWithNotice(w, r, "/admin/matches", message)
		return
	}

	redirectWithNotice(w, r, "/admin/matches", "自动匹配任务已启动")
}

func (a *App) handleMatchArtist(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	id := parseInt64(r.PathValue("id"))
	result, err := a.enrichment.MatchArtist(r.Context(), id)
	if err != nil {
		var partial *enrichment.ArtistMatchPartialError
		if result.AutoMatched && errors.As(err, &partial) {
			a.logger.Warn("artist matched with attachment failure", "artistId", id, "error", err)
			redirectWithNotice(w, r, artistProfilePath(id), partial.Notice())
			return
		}
		if notice, ok := enrichment.RateLimitNotice(err); ok {
			if result.AutoMatched {
				// The match was confirmed before the rate limit hit; do not let
				// the notice read like the match failed.
				source, minutes, _ := enrichment.RateLimitNoticeParts(err)
				notice = fmt.Sprintf("已自动确认匹配，但图片/简介因 %s 限流暂未获取，约 %d 分钟后可重试", source, minutes)
			}
			redirectWithNotice(w, r, artistProfilePath(id), notice)
			return
		}
		redirectWithNotice(w, r, artistProfilePath(id), err.Error())
		return
	}
	message := "已生成候选，需要人工确认"
	if result.AutoMatched {
		message = "两个来源身份一致，已自动匹配"
	}
	redirectWithNotice(w, r, artistProfilePath(id), message)
}

func (a *App) handleConfirmArtistMatch(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	id := parseInt64(r.PathValue("id"))
	candidateID := parseInt64(r.PathValue("candidate"))
	if err := a.store.ConfirmArtistCandidate(r.Context(), id, candidateID); err != nil {
		var conflict *storage.ArtistExternalIDConflictError
		if errors.As(err, &conflict) {
			http.Redirect(w, r, artistProfilePath(id)+"&identityConflict="+strconv.FormatInt(candidateID, 10), http.StatusSeeOther)
		} else if errors.Is(err, storage.ErrCompositeArtistIdentity) {
			redirectWithNotice(w, r, artistProfilePath(id), err.Error())
		} else if errors.Is(err, sql.ErrNoRows) {
			redirectWithNotice(w, r, artistProfilePath(id), "候选不存在或已变化，请重新匹配")
		} else {
			a.logger.Error("confirm artist identity", "artistId", id, "error", err)
			http.Error(w, "确认身份失败，请稍后重试", http.StatusInternalServerError)
		}
		return
	}
	rateLimitedSource := ""
	if err := a.enrichment.RefreshConfirmedArtistImage(r.Context(), id); err != nil && !errors.Is(err, sql.ErrNoRows) {
		if source, ok := enrichment.RateLimitedSourceName(err); ok {
			rateLimitedSource = source
		}
		a.logger.Warn("cache confirmed artist image", "artistId", id, "error", enrichment.RedactSourceError(err))
	}
	if err := a.enrichment.RefreshArtistBiographies(r.Context(), id, true); err != nil && !errors.Is(err, sql.ErrNoRows) {
		if source, ok := enrichment.RateLimitedSourceName(err); ok && rateLimitedSource == "" {
			rateLimitedSource = source
		}
		a.logger.Warn("cache confirmed artist biographies", "artistId", id, "error", enrichment.RedactSourceError(err))
	}
	message := "外部身份已经人工确认"
	if rateLimitedSource != "" {
		message = fmt.Sprintf("已确认匹配；%s 限流中，头像可稍后在“艺术家匹配与审核”页用“已匹配艺术家头像补全”补齐，简介可稍后手动刷新", rateLimitedSource)
	}
	redirectWithNotice(w, r, artistProfilePath(id), message)
}

func (a *App) handleRefreshArtistBiographies(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	id := parseInt64(r.PathValue("id"))
	if err := a.enrichment.RefreshArtistBiographies(r.Context(), id, true); err != nil {
		if errors.Is(err, enrichment.ErrNoBiographyLanguages) {
			// P2-6：语言配置为空与“未确认身份”明确区分。
			redirectWithNotice(w, r, artistProfilePath(id), "未配置简介语言，请在数据来源设置中配置简介语言")
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			// M3：无已确认 MusicBrainz 身份（含合并继承）时给出安全文案，
			// 不回显原始 sql.ErrNoRows。
			redirectWithNotice(w, r, artistProfilePath(id), "未确认 MusicBrainz 身份，无法刷新简介")
			return
		}
		if errors.Is(err, storage.ErrArtistBiographyBackfillState) {
			redirectWithNotice(w, r, artistProfilePath(id), "身份或任务状态已变化，简介未写入，请重试")
			return
		}
		if notice, ok := enrichment.RateLimitNotice(err); ok {
			redirectWithNotice(w, r, artistProfilePath(id), notice)
			return
		}
		redirectWithNotice(w, r, artistProfilePath(id), "简介刷新失败："+enrichment.RedactSourceError(err).Error())
		return
	}
	redirectWithNotice(w, r, artistProfilePath(id), "简介缓存已刷新")
}

func (a *App) handleSelectArtistBiography(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	id := parseInt64(r.PathValue("id"))
	source := r.FormValue("source")
	language := r.FormValue("language")
	if r.FormValue("reset") != "" {
		source, language = "", ""
	}
	if err := a.store.SetArtistBiographyPreference(r.Context(), id, source, language); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	message := "已为该歌手固定简介版本"
	if source == "" {
		message = "已恢复全局简介优先级"
	}
	redirectWithNotice(w, r, artistProfilePath(id), message)
}

func (a *App) handleRejectArtistMatch(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	id := parseInt64(r.PathValue("id"))
	if err := a.store.RejectArtistCandidate(r.Context(), id, parseInt64(r.PathValue("candidate"))); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	redirectWithNotice(w, r, artistProfilePath(id), "候选已拒绝")
}

func (a *App) handleArtistPage(w http.ResponseWriter, r *http.Request) {
	data := a.identityBase(r, "artists")
	id := parseInt64(r.PathValue("id"))
	detail, err := a.store.ArtistDetail(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	q := r.URL.Query()
	if detail.MergedIntoID != 0 {
		target, e := a.store.CanonicalArtistID(r.Context(), id)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		if q.Has("credit") {
			if q.Get("notice") == "" {
				q.Set("notice", "该歌手已合并，正在显示目标歌手")
			}
			http.Redirect(w, r, creditDetailPath(target, q, true), 303)
		} else {
			path := "/admin/artists/" + strconv.FormatInt(target, 10)
			if q.Get("view") == "profile" {
				path = artistProfilePath(target)
			}
			redirectWithNotice(w, r, path, "该歌手已合并，正在显示目标歌手")
		}
		return
	}
	if !q.Has("identityConflict") {
		if q.Has("credit") || (q.Get("view") != "profile" && detail.AlbumCount == 0 && detail.PerformedTrackCount == 0 && detail.CreditTrackCount > 0) {
			http.Redirect(w, r, creditDetailPath(id, q, true), 303)
			return
		}
	}
	data.Artist = &detail

	if candidateID := parseInt64(r.URL.Query().Get("identityConflict")); candidateID > 0 {
		conflict, conflictErr := a.store.ArtistIdentityConflict(r.Context(), id, candidateID)
		if conflictErr == nil {
			data.IdentityConflict = &conflict
		} else {
			data.Notice = "身份冲突已变化，请重新核对候选"
		}
	}
	releases, err := a.store.ArtistDiscography(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.ReleaseGroups = groupArtistDiscography(releases)
	data.Tracks, _ = a.store.ListTracks(r.Context(), storage.Filters{ArtistID: id, Limit: 20, PerformerOnly: true})
	data.Artists, _ = a.store.ListArtists(r.Context(), storage.Filters{Limit: 500})
	a.render(w, 200, "artist.html", data)
}

// groupArtistDiscography preserves the date order supplied by storage within
// every group. Ownership is classified before release type, so a guest track
// on someone else's album never becomes a personal album or single.
func groupArtistDiscography(releases []storage.ArtistRelease) []artistReleaseGroup {
	personal := []storage.Album{}
	collaborations := artistReleaseGroup{Title: "合作发行", Description: "与其他专辑艺人共同署名的发行"}
	appearances := artistReleaseGroup{Title: "参与作品", Description: "曲目演唱参与及多人合辑，不计入个人发行"}
	for _, release := range releases {
		switch release.Relation {
		case "personal":
			personal = append(personal, release.Album)
		case "collaboration":
			collaborations.Releases = append(collaborations.Releases, release.Album)
		case "appearance":
			appearances.Releases = append(appearances.Releases, release.Album)
		}
	}
	groups := groupArtistReleases(personal)
	for _, group := range []artistReleaseGroup{collaborations, appearances} {
		if len(group.Releases) > 0 {
			groups = append(groups, group)
		}
	}
	return groups
}

func groupArtistReleases(albums []storage.Album) []artistReleaseGroup {
	groups := []artistReleaseGroup{
		{Title: "专辑"},
		{Title: "单曲与 EP"},
		{Title: "合辑与现场"},
		{Title: "其他发行"},
	}
	for _, album := range albums {
		// ReleaseKind 已合并手动纠正、标签与本地推断；为空时回退 AlbumType。
		kind := album.ReleaseKind
		if kind == "" {
			kind = album.AlbumType
		}
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "single", "ep":
			groups[1].Releases = append(groups[1].Releases, album)
		case "compilation", "live":
			groups[2].Releases = append(groups[2].Releases, album)
		case "soundtrack", "bootleg", "other":
			groups[3].Releases = append(groups[3].Releases, album)
		default:
			groups[0].Releases = append(groups[0].Releases, album)
		}
	}
	result := make([]artistReleaseGroup, 0, len(groups))
	for _, group := range groups {
		if len(group.Releases) > 0 {
			result = append(result, group)
		}
	}
	return result
}

func (a *App) handleMergeArtist(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	sourceID := parseInt64(r.PathValue("id"))
	targetID := parseInt64(r.FormValue("targetArtist"))
	operation, err := a.store.MergeArtists(r.Context(), sourceID, targetID)
	if err != nil {
		redirectWithNotice(w, r, artistProfilePath(sourceID), err.Error())
		return
	}
	redirectWithNotice(w, r, artistProfilePath(targetID), "歌手合并完成，可在合并历史中回退；操作 #"+strconv.FormatInt(operation, 10))
}

func (a *App) handleResetArtistIdentity(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	returnTo := artistProfilePath(id)
	if r.FormValue("confirm") != "1" {
		redirectWithNotice(w, r, returnTo, "请二次确认解除身份，未修改数据")
		return
	}
	err := a.store.ResetArtistIdentity(r.Context(), id, r.FormValue("source"), r.FormValue("expectedID"))
	if err != nil {
		redirectWithNotice(w, r, returnTo, "身份已变化或解除失败，请刷新后核对")
		return
	}
	redirectWithNotice(w, r, returnTo, "已解除指定来源的身份并清除相关自动资料；歌曲关联和自定义资料保持不变，请重新匹配。其他来源如有错误需分别解除。")
}

func (a *App) handleMergeIdentityConflict(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	sourceID := parseInt64(r.PathValue("id"))
	candidateID := parseInt64(r.PathValue("candidate"))
	returnTo := artistProfilePath(sourceID) + "&identityConflict=" + strconv.FormatInt(candidateID, 10)
	if r.FormValue("confirm") != "1" {
		redirectWithNotice(w, r, returnTo, "请核对歌手并二次确认，尚未执行合并")
		return
	}
	targetID := parseInt64(r.FormValue("expectedTarget"))
	operation, err := a.store.MergeArtistsForIdentityConflict(r.Context(), sourceID, candidateID, targetID)
	if err != nil {
		message := "合并失败，未执行合并，请刷新后重新核对"
		if errors.Is(err, storage.ErrIdentityConflictStale) {
			message = err.Error()
		} else {
			a.logger.Error("merge identity conflict", "artistId", sourceID, "error", err)
		}
		redirectWithNotice(w, r, returnTo, message)
		return
	}
	redirectWithNotice(w, r, artistProfilePath(targetID), fmt.Sprintf("歌手合并完成（操作 #%d），可在合并历史回退；未新增或迁移候选身份绑定", operation))
}

func (a *App) handleMergeHistory(w http.ResponseWriter, r *http.Request) {
	data := a.identityBase(r, "merges")
	operations, err := a.store.MergeOperations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	data.Merges = operations
	a.render(w, 200, "merge-history.html", data)
}
func (a *App) handleRollbackMerge(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if err := a.store.RollbackArtistMerge(r.Context(), parseInt64(r.PathValue("id"))); err != nil {
		redirectWithNotice(w, r, "/admin/merges", err.Error())
		return
	}
	redirectWithNotice(w, r, "/admin/merges", "合并已经回退")
}

func (a *App) handleMatchReview(w http.ResponseWriter, r *http.Request) {
	data := a.identityBase(r, "matches")
	data.MatchRuns, _ = a.store.ListArtistMatchRuns(r.Context(), 30)
	// 活动任务置顶：直接查未完成的 durable run，不受历史分页影响。
	if active, err := a.store.UnfinishedDurableArtistRun(r.Context()); err == nil {
		view := artistRunDTO(active)
		data.ActiveArtistRun = &view
	} else if !errors.Is(err, sql.ErrNoRows) {
		a.logger.Error("matches active run", "error", err)
	}
	data.ArtistRunsPage, data.ArtistRunsLimit = runsPageNumber(r), 10
	runsOffset := (data.ArtistRunsPage - 1) * data.ArtistRunsLimit
	if runs, count, err := a.store.ListDurableArtistRuns(r.Context(), data.ArtistRunsLimit, runsOffset); err == nil {
		data.ArtistRunsTotal = count
		for _, run := range runs {
			if data.ActiveArtistRun != nil && run.ID == data.ActiveArtistRun.ID {
				continue
			}
			view := artistRunDTO(run)
			// “最近任务”只在第一页从页内取；其余页走独立最新查询（见下）。
			if data.ArtistRunsPage == 1 && data.LastArtistRun == nil && run.Status != "running" && run.Status != "paused" && run.Status != "queued" {
				copy := view
				data.LastArtistRun = &copy
			}
			data.ArtistRunHistory = append(data.ArtistRunHistory, view)
		}
	}
	if data.LastArtistRun == nil {
		if latest, _, err := a.store.ListDurableArtistRuns(r.Context(), 5, 0); err == nil {
			for _, run := range latest {
				if run.Status == "running" || run.Status == "paused" || run.Status == "queued" {
					continue
				}
				view := artistRunDTO(run)
				data.LastArtistRun = &view
				break
			}
		}
	}

	pageNumber, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if pageNumber < 1 {
		pageNumber = 1
	}
	if pageNumber > 1000000 {
		pageNumber = 1000000
	}
	source := r.URL.Query().Get("source")
	if source != "" && source != "musicbrainz" && source != "lastfm" {
		http.Error(w, "invalid source", 400)
		return
	}
	page, err := a.store.PendingArtistReviewPage(r.Context(), storage.ArtistReviewFilter{Query: r.URL.Query().Get("q"), Source: source, Limit: 50, Offset: (pageNumber - 1) * 50})
	if err != nil {
		http.Error(w, "加载艺术家审核失败", 500)
		return
	}
	data.ReviewTotal, data.ReviewLimit, data.ReviewOffset = page.Total, page.Limit, page.Offset
	data.ReviewPage = pageNumber
	data.ReviewQuery, data.ReviewSource = r.URL.Query().Get("q"), source
	for _, item := range page.Items {
		review := matchReviewItem{Artist: item.Artist, Candidates: item.Candidates}
		if item.NeedsCreditCorrection {
			data.CreditCorrectionReview = append(data.CreditCorrectionReview, review)
		} else {
			data.Review = append(data.Review, review)
		}
	}
	// 头像补全区块：统计只读 DB+磁盘；失败只记日志，页面其余部分照常渲染。
	if a.enrichment != nil {
		if cached, total, statsErr := a.enrichment.ArtistImageBackfillStats(r.Context()); statsErr == nil {
			data.ImageBackfill.Cached, data.ImageBackfill.Total = cached, total
			data.ImageBackfill.Missing = total - cached
		} else {
			a.logger.Error("artist image backfill stats", "error", statsErr)
		}
	}
	if active, activeErr := a.store.UnfinishedArtistImageBackfillRun(r.Context()); activeErr == nil {
		view := artistImageRunDTO(active)
		data.ImageBackfill.Active = &view
	} else if !errors.Is(activeErr, sql.ErrNoRows) {
		a.logger.Error("matches active image backfill run", "error", activeErr)
	}
	if data.ImageBackfill.Active == nil {
		if latest, latestErr := a.store.LatestArtistImageBackfillRun(r.Context()); latestErr == nil {
			view := artistImageRunDTO(latest)
			data.ImageBackfill.Last = &view
		} else if !errors.Is(latestErr, sql.ErrNoRows) {
			a.logger.Error("matches latest image backfill run", "error", latestErr)
		}
	}
	// 歌手简介补全区块：统计只读 DB；失败只记日志，页面其余部分照常渲染。
	if a.enrichment != nil {
		if missing, total, statsErr := a.enrichment.ArtistBiographyBackfillStats(r.Context()); statsErr == nil {
			data.BioBackfill.Missing, data.BioBackfill.Total = missing, total
		} else {
			a.logger.Error("artist biography backfill stats", "error", statsErr)
		}
	}
	if active, activeErr := a.store.UnfinishedArtistBiographyBackfillRun(r.Context()); activeErr == nil {
		view := artistBiographyRunDTO(active)
		data.BioBackfill.Active = &view
	} else if !errors.Is(activeErr, sql.ErrNoRows) {
		a.logger.Error("matches active biography backfill run", "error", activeErr)
	}
	if data.BioBackfill.Active == nil {
		if latest, latestErr := a.store.LatestArtistBiographyBackfillRun(r.Context()); latestErr == nil {
			view := artistBiographyRunDTO(latest)
			data.BioBackfill.Last = &view
		} else if !errors.Is(latestErr, sql.ErrNoRows) {
			a.logger.Error("matches latest biography backfill run", "error", latestErr)
		}
	}
	a.render(w, 200, "match-review.html", data)
}

func redirectWithNotice(w http.ResponseWriter, r *http.Request, path, message string) {
	// L1：fragment 必须在 query 之后，否则 notice 会被拼进 fragment 里丢失。
	fragment := ""
	if idx := strings.IndexByte(path, '#'); idx >= 0 {
		fragment = path[idx:]
		path = path[:idx]
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	http.Redirect(w, r, path+separator+"notice="+url.QueryEscape(message)+fragment, 303)
}
