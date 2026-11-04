package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/lastfm"
	"github.com/lux032/032music-server/internal/storage"
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

	// Love sync: the switch, the pending queue and the optional backfill.
	LoveSync           bool
	LovePending        int64
	LoveFailed         int64
	LoveLastError      string
	LoveLastErrorAt    string
	LoveCanSync        bool
	BackfillState      string
	BackfillTotal      int64
	BackfillProcessed  int64
	BackfillFailed     int64
	BackfillPercent    float64
	BackfillStartedAt  string
	BackfillFinishedAt string
	BackfillError      string
	BackfillRunning    bool
	BackfillDone       bool
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
	view.LoveSync = settings.SyncLoves
	view.LoveLastError = settings.LastLoveError
	view.LoveLastErrorAt = settings.LastLoveErrorAt
	// The backfill needs the same prerequisites as the live sync, so the button
	// is only offered when it would actually run.
	view.LoveCanSync = settings.LoveSyncReady()
	a.fillLastFMLoveView(r, &view)
	if a.lastfm != nil && settings.HasAPISecret() {
		if authURL, err := a.lastfm.PendingAuthorizationURL(r.Context()); err != nil {
			a.logger.Warn("load pending Last.fm authorisation", "error", err)
		} else {
			view.PendingAuthURL = authURL
		}
	}
	return view
}

// fillLastFMLoveView adds the love queue and backfill progress to the card.
func (a *App) fillLastFMLoveView(r *http.Request, view *lastFMScrobbleView) {
	stats, err := a.store.LastFMLoveQueueStats(r.Context())
	if err != nil {
		a.logger.Warn("load Last.fm love queue stats", "error", err)
	} else {
		view.LovePending, view.LoveFailed = stats.Pending, stats.Failed
	}
	run, err := a.store.LastFMLoveBackfill(r.Context())
	if err != nil {
		a.logger.Warn("load Last.fm love backfill", "error", err)
		return
	}
	view.BackfillState = run.State
	view.BackfillTotal, view.BackfillProcessed, view.BackfillFailed = run.Total, run.Processed, run.Failed
	view.BackfillPercent = run.Percent()
	view.BackfillStartedAt, view.BackfillFinishedAt = formatAdminTime(run.StartedAt), formatAdminTime(run.FinishedAt)
	view.BackfillError = run.LastError
	view.BackfillRunning = run.State == storage.LoveBackfillRunning
	view.BackfillDone = run.Done()
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
// handleSaveLastFMLoveSync stores the love-sync switch. Turning it off keeps
// the queue: already-requested changes are still submitted, only new
// favourite changes stop being mirrored.
func (a *App) handleSaveLastFMLoveSync(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	enabled := r.FormValue("sync_loves") != ""
	if err := a.store.SetLastFMLoveSync(r.Context(), enabled); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if a.lastfm != nil {
		a.lastfm.Wake()
	}
	notice := "已开启 Last.fm 红心同步：之后收藏或取消收藏单曲时会同步到 Last.fm"
	if !enabled {
		notice = "已关闭 Last.fm 红心同步（本地收藏不受影响）"
	}
	redirectWithNotice(w, r, lastFMSettingsPath, notice)
}

// handleStartLastFMLoveBackfill starts the explicit, resumable push of the
// favourites that already existed. It is never automatic: an admin decides
// when to send a possibly large batch to Last.fm.
func (a *App) handleStartLastFMLoveBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	ctx := r.Context()
	settings, err := a.store.LastFMScrobbleSettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if !settings.LoveSyncReady() {
		redirectWithNotice(w, r, lastFMSettingsPath, "请先连接 Last.fm 账号并开启红心同步")
		return
	}
	run, err := a.store.LastFMLoveBackfill(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if run.State == storage.LoveBackfillRunning {
		redirectWithNotice(w, r, lastFMSettingsPath, "已有回填任务在进行中")
		return
	}
	total, err := a.store.CountLoveBackfillTracks(ctx)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if total == 0 {
		redirectWithNotice(w, r, lastFMSettingsPath, "当前没有已收藏的单曲可回填")
		return
	}
	if err := a.store.BeginLastFMLoveBackfill(ctx, total); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if a.lastfm != nil {
		a.lastfm.Wake()
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已开始把现有收藏同步到 Last.fm，可在下方查看进度")
}

// handleCancelLastFMLoveBackfill stops a running backfill; finished tracks stay
// loved on Last.fm, and restarting later resumes from the stored cursor.
func (a *App) handleCancelLastFMLoveBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if err := a.store.CancelLastFMLoveBackfill(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已停止回填；已经同步的歌曲保持收藏")
}

// handleRetryLastFMLoves makes the queued love changes due immediately after a
// failure window, so an admin does not have to wait for the backoff.
func (a *App) handleRetryLastFMLoves(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return
	}
	if err := a.store.RetryLastFMLovesNow(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if a.lastfm != nil {
		a.lastfm.Wake()
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已安排立即重试待同步的红心")
}

// handleLastFMLoveStatus returns the love-sync card state for the poller.
func (a *App) handleLastFMLoveStatus(w http.ResponseWriter, r *http.Request) {
	view := a.lastFMScrobbleView(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"pending":     view.LovePending,
		"failed":      view.LoveFailed,
		"lastError":   view.LoveLastError,
		"lastErrorAt": view.LoveLastErrorAt,
		"backfill": map[string]any{
			"state":      view.BackfillState,
			"total":      view.BackfillTotal,
			"processed":  view.BackfillProcessed,
			"failed":     view.BackfillFailed,
			"percent":    view.BackfillPercent,
			"running":    view.BackfillRunning,
			"done":       view.BackfillDone,
			"startedAt":  view.BackfillStartedAt,
			"finishedAt": view.BackfillFinishedAt,
			"lastError":  view.BackfillError,
		},
	})
}

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
