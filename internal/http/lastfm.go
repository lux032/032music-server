package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/lastfm"
)

const (
	lastFMSettingsPath = "/admin/settings/metadata"
	lastFMCallbackPath = "/admin/settings/lastfm-scrobble/callback"
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
	// PendingAuthURL is the Last.fm approval page of an authorisation the
	// admin has started but not completed yet.
	PendingAuthURL string
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
	if a.lastfm != nil && settings.HasAPISecret() {
		if authURL, err := a.lastfm.PendingAuthorizationURL(r.Context()); err != nil {
			a.logger.Warn("load pending Last.fm authorisation", "error", err)
		} else {
			view.PendingAuthURL = authURL
		}
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

// handleConnectLastFM starts a Last.fm authorisation (desktop flow): the
// server requests a token and the settings page then offers a plain link to
// the Last.fm approval page. Nothing redirects a form submission to another
// origin (which the page's CSP form-action 'self' would block) and no
// callback has to reach this server.
func (a *App) handleConnectLastFM(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if a.lastfm == nil {
		redirectWithNotice(w, r, lastFMSettingsPath, "Last.fm 服务未启动")
		return
	}
	if _, err := a.lastfm.BeginAuthorization(r.Context()); err != nil {
		a.logger.Warn("start Last.fm authorisation", "error", err)
		redirectWithNotice(w, r, lastFMSettingsPath, "无法开始 Last.fm 授权："+err.Error())
		return
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "请点击“前往 Last.fm 授权”，在 Last.fm 页面允许访问后回到本页点“完成连接”")
}

// handleCompleteLastFM exchanges the approved pending token for a session.
func (a *App) handleCompleteLastFM(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	a.completeLastFM(w, r)
}

// handleLastFMCallback serves Last.fm applications whose API account has a
// callback URL pointing here. The token in the query is ignored: only the
// pending token this server requested is ever exchanged, so a forged
// callback cannot bind someone else's Last.fm account.
func (a *App) handleLastFMCallback(w http.ResponseWriter, r *http.Request) {
	a.completeLastFM(w, r)
}

func (a *App) completeLastFM(w http.ResponseWriter, r *http.Request) {
	if a.lastfm == nil {
		redirectWithNotice(w, r, lastFMSettingsPath, "Last.fm 服务未启动")
		return
	}
	username, err := a.lastfm.CompleteAuthorization(r.Context())
	if err != nil {
		if !errors.Is(err, lastfm.ErrAuthorizationNotApproved) && !errors.Is(err, lastfm.ErrNoPendingAuthorization) {
			a.logger.Warn("Last.fm authorisation failed", "error", err)
		}
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
