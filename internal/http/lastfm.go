package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/lastfm"
)

const (
	lastFMSettingsPath    = "/admin/settings/metadata"
	lastFMCallbackPath    = "/admin/settings/lastfm-scrobble/callback"
	lastFMAuthStateMaxAge = 30 * time.Minute
)

// lastFMScrobbleView is the Last.fm scrobbling card on the data sources page.
// It never carries the shared secret or the session key.
type lastFMScrobbleView struct {
	Enabled, NowPlaying    bool
	HasAPIKey, HasSecret   bool
	Connected              bool
	Username               string
	LastSuccessAt          string
	LastError, LastErrorAt string
	PendingCount           int64
	OldestPending          string
	ServiceRunning         bool
}

func (a *App) lastFMScrobbleView(r *http.Request) lastFMScrobbleView {
	settings, err := a.store.LastFMScrobbleSettings(r.Context())
	if err != nil {
		a.logger.Warn("load Last.fm scrobble settings", "error", err)
		return lastFMScrobbleView{ServiceRunning: a.lastfm != nil}
	}
	view := lastFMScrobbleView{
		Enabled: settings.Enabled, NowPlaying: settings.NowPlaying,
		HasAPIKey: settings.HasAPIKey(), HasSecret: settings.HasAPISecret(),
		Connected: settings.Connected(), Username: settings.Username,
		LastSuccessAt: settings.LastSuccessAt, LastError: settings.LastError, LastErrorAt: settings.LastErrorAt,
		PendingCount: settings.PendingCount, ServiceRunning: a.lastfm != nil,
	}
	if settings.OldestPendingStartedAtUnix > 0 {
		view.OldestPending = time.Unix(settings.OldestPendingStartedAtUnix, 0).Local().Format("2006-01-02 15:04")
	}
	return view
}

func (a *App) handleSaveLastFMScrobble(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	secret := strings.TrimSpace(r.FormValue("lastfm_api_secret"))
	if err := a.store.SaveLastFMScrobblePreferences(r.Context(), r.FormValue("scrobble_enabled") != "", r.FormValue("now_playing_enabled") != "", secret); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if a.lastfm != nil {
		a.lastfm.Wake()
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "Last.fm 播放记录设置已保存")
}

// handleConnectLastFM starts the Last.fm web authorisation. The callback
// carries a single-use state so a forged callback cannot bind the server to
// someone else's Last.fm account.
func (a *App) handleConnectLastFM(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	settings, err := a.store.LastFMScrobbleSettings(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if !settings.HasAPIKey() || !settings.HasAPISecret() {
		redirectWithNotice(w, r, lastFMSettingsPath, "请先在 Last.fm 卡片填写 API Key，并在播放记录卡片填写 Shared Secret")
		return
	}
	state, err := randomToken()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := a.store.BeginLastFMAuthorization(r.Context(), state, time.Now()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	callback := requestBaseURL(r) + lastFMCallbackPath + "?state=" + url.QueryEscape(state)
	http.Redirect(w, r, lastfm.AuthURL(settings.APIKey, callback), http.StatusSeeOther)
}

func (a *App) handleLastFMCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	ok, err := a.store.ConsumeLastFMAuthorization(r.Context(), query.Get("state"), lastFMAuthStateMaxAge, time.Now())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if !ok {
		redirectWithNotice(w, r, lastFMSettingsPath, "授权请求无效或已过期，请重新点击“连接 Last.fm 账号”")
		return
	}
	token := query.Get("token")
	if token == "" {
		redirectWithNotice(w, r, lastFMSettingsPath, "Last.fm 没有返回授权 Token，请重新连接")
		return
	}
	if a.lastfm == nil {
		redirectWithNotice(w, r, lastFMSettingsPath, "Last.fm 服务未启动")
		return
	}
	username, err := a.lastfm.Connect(r.Context(), token)
	if err != nil {
		a.logger.Warn("Last.fm authorisation failed", "error", err)
		redirectWithNotice(w, r, lastFMSettingsPath, "连接 Last.fm 失败："+err.Error())
		return
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已连接 Last.fm 账号 "+username+"，播放过半的歌曲将自动记录")
}

func (a *App) handleDisconnectLastFM(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if err := a.store.ClearLastFMSession(r.Context(), ""); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已断开 Last.fm 账号")
}

func (a *App) handleRetryLastFM(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if err := a.store.RetryLastFMScrobblesNow(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if a.lastfm != nil {
		a.lastfm.Wake()
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已安排立即重新提交待发送的播放记录")
}

// requestBaseURL reconstructs the origin the admin's browser used. Forwarded
// headers are honoured because the result is only used as the redirect target
// for that same browser; it grants nothing.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); proto == "https" || proto == "http" {
		scheme = proto
	}
	host := r.Host
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwarded != "" {
		host = forwarded
	}
	return scheme + "://" + host
}
