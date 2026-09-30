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
	Title    string
	Releases []storage.Album
}
type identityPageData struct {
	Chrome
	Section, Notice   string
	Settings          []storage.MetadataSourceSetting
	BiographySettings storage.BiographySettings
	LastFMScrobble    lastFMScrobbleView
	IdentityConflict  *storage.ArtistIdentityConflict
	Artist            *storage.ArtistDetail
	Artists           []storage.Artist
	Albums            []storage.Album
	ReleaseGroups     []artistReleaseGroup
	Tracks            []storage.Track
	Review            []matchReviewItem
	Merges            []storage.MergeOperation
	MatchRuns         []storage.ArtistMatchRun
}

func (a *App) identityBase(r *http.Request, section string) identityPageData {
	session, _ := a.sessions.get(r)
	return identityPageData{Chrome: a.chromeFor(r.Context(), session, identityNavKey(section)), Section: section, Notice: r.URL.Query().Get("notice")}
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
	} else if err := a.enrichment.CancelArtistMatching(parseInt64(r.PathValue("id"))); errors.Is(err, enrichment.ErrRunNotActive) {
		message = "任务已结束或不存在"
	} else if err != nil {
		message = "停止任务失败：" + err.Error()
	}
	redirectWithNotice(w, r, "/admin/matches", message)
}

func (a *App) handleRunArtistMatching(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if _, err := a.enrichment.StartAll(r.Context()); err != nil {
		redirectWithNotice(w, r, "/admin/matches", err.Error())
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
		if notice, ok := enrichment.RateLimitNotice(err); ok {
			if result.AutoMatched {
				// The match was confirmed before the rate limit hit; do not let
				// the notice read like the match failed.
				source, minutes, _ := enrichment.RateLimitNoticeParts(err)
				notice = fmt.Sprintf("已自动确认匹配，但图片/简介因 %s 限流暂未获取，约 %d 分钟后可重试", source, minutes)
			}
			redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), notice)
			return
		}
		redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), err.Error())
		return
	}
	message := "已生成候选，需要人工确认"
	if result.AutoMatched {
		message = "两个来源身份一致，已自动匹配"
	}
	redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), message)
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
			http.Redirect(w, r, fmt.Sprintf("/admin/artists/%d?identityConflict=%d", id, candidateID), http.StatusSeeOther)
		} else if errors.Is(err, sql.ErrNoRows) {
			redirectWithNotice(w, r, fmt.Sprintf("/admin/artists/%d", id), "候选不存在或已变化，请重新匹配")
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
		a.logger.Warn("cache confirmed artist image", "artistId", id, "error", err)
	}
	if err := a.enrichment.RefreshArtistBiographies(r.Context(), id, true); err != nil && !errors.Is(err, sql.ErrNoRows) {
		if source, ok := enrichment.RateLimitedSourceName(err); ok && rateLimitedSource == "" {
			rateLimitedSource = source
		}
		a.logger.Warn("cache confirmed artist biographies", "artistId", id, "error", err)
	}
	message := "外部身份已经人工确认"
	if rateLimitedSource != "" {
		message = fmt.Sprintf("已确认匹配；%s 限流中，图片/简介可稍后手动刷新，或在下次自动匹配时补全", rateLimitedSource)
	}
	redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), message)
}

func (a *App) handleRefreshArtistBiographies(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	id := parseInt64(r.PathValue("id"))
	if err := a.enrichment.RefreshArtistBiographies(r.Context(), id, true); err != nil {
		if notice, ok := enrichment.RateLimitNotice(err); ok {
			redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), notice)
			return
		}
		redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), "简介刷新失败："+err.Error())
		return
	}
	redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), "简介缓存已刷新")
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
	redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), message)
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
	redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(id, 10), "候选已拒绝")
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
	if detail.MergedIntoID != 0 {
		http.Redirect(w, r, "/admin/artists/"+strconv.FormatInt(detail.MergedIntoID, 10)+"?notice="+url.QueryEscape("该歌手已合并，正在显示目标歌手"), 303)
		return
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
	data.Albums, _ = a.store.ListAlbums(r.Context(), storage.Filters{ArtistID: id, Limit: 500})
	data.ReleaseGroups = groupArtistReleases(data.Albums)
	data.Tracks, _ = a.store.ListTracks(r.Context(), storage.Filters{ArtistID: id, Limit: 20})
	data.Artists, _ = a.store.ListArtists(r.Context(), storage.Filters{Limit: 500})
	a.render(w, 200, "artist.html", data)
}

func groupArtistReleases(albums []storage.Album) []artistReleaseGroup {
	groups := []artistReleaseGroup{
		{Title: "专辑"},
		{Title: "单曲与 EP"},
		{Title: "合辑"},
		{Title: "其他发行"},
	}
	for _, album := range albums {
		switch strings.ToLower(strings.TrimSpace(album.AlbumType)) {
		case "single", "ep":
			groups[1].Releases = append(groups[1].Releases, album)
		case "compilation":
			groups[2].Releases = append(groups[2].Releases, album)
		case "soundtrack", "other":
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
		redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(sourceID, 10), err.Error())
		return
	}
	redirectWithNotice(w, r, "/admin/artists/"+strconv.FormatInt(targetID, 10), "歌手合并完成，可在合并历史中回退；操作 #"+strconv.FormatInt(operation, 10))
}

func (a *App) handleMergeIdentityConflict(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	sourceID := parseInt64(r.PathValue("id"))
	candidateID := parseInt64(r.PathValue("candidate"))
	returnTo := fmt.Sprintf("/admin/artists/%d?identityConflict=%d", sourceID, candidateID)
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
	redirectWithNotice(w, r, fmt.Sprintf("/admin/artists/%d", targetID), fmt.Sprintf("歌手合并完成（操作 #%d），可在合并历史回退；未新增或迁移候选身份绑定", operation))
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
	artists, err := a.store.ListArtists(r.Context(), storage.Filters{Limit: 500})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	pendingByArtist, err := a.store.PendingArtistCandidates(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for _, artist := range artists {
		if pending := pendingByArtist[artist.ID]; len(pending) > 0 {
			data.Review = append(data.Review, matchReviewItem{Artist: artist, Candidates: pending})
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
