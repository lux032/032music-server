package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/listenbrainz"
	"github.com/lux032/032music-server/internal/storage"
)

type listenBrainzView struct {
	Enabled, NowPlaying    bool
	HasToken, Connected    bool
	ServerURL, Username    string
	LookupUnsupported      bool
	FeedbackUnsupported    bool
	LastSuccessAt          string
	LastError, LastErrorAt string
	PendingCount           int64
	OldestPending          string
	ServiceRunning         bool

	LoveSync            bool
	LovePending         int64
	LoveFailed          int64
	LoveLastError       string
	LoveLastErrorAt     string
	LoveCanSync         bool
	BackfillState       string
	BackfillTotal       int64
	BackfillProcessed   int64
	BackfillFailed      int64
	BackfillSkippedNoID int64
	BackfillPercent     float64
	BackfillStartedAt   string
	BackfillFinishedAt  string
	BackfillError       string
	BackfillRunning     bool
	BackfillDone        bool
}

func (a *App) listenBrainzView(r *http.Request) listenBrainzView {
	settings, err := a.store.ListenBrainzSettings(r.Context())
	if err != nil {
		a.logger.Warn("load ListenBrainz settings", "error", err)
		return listenBrainzView{ServerURL: listenbrainz.DefaultServerURL, ServiceRunning: a.listenbrainz != nil}
	}
	view := listenBrainzView{
		Enabled:             settings.Enabled,
		NowPlaying:          settings.NowPlaying,
		HasToken:            settings.HasToken(),
		Connected:           settings.Connected(),
		ServerURL:           settings.ServerURL,
		Username:            settings.Username,
		LookupUnsupported:   settings.LookupUnsupported,
		FeedbackUnsupported: settings.FeedbackUnsupported,
		LastSuccessAt:       settings.LastSuccessAt,
		LastError:           settings.LastError,
		LastErrorAt:         settings.LastErrorAt,
		PendingCount:        settings.PendingCount,
		ServiceRunning:      a.listenbrainz != nil,
		LoveSync:            settings.SyncLoves,
		LoveLastError:       settings.LastLoveError,
		LoveLastErrorAt:     settings.LastLoveErrorAt,
		LoveCanSync:         settings.LoveSyncReady(),
	}
	if settings.OldestPendingStartedAtUnix > 0 {
		view.OldestPending = time.Unix(settings.OldestPendingStartedAtUnix, 0).Local().Format("2006-01-02 15:04")
	}
	a.fillListenBrainzLoveView(r, &view)
	return view
}

func (a *App) fillListenBrainzLoveView(r *http.Request, view *listenBrainzView) {
	stats, err := a.store.ListenBrainzLoveQueueStats(r.Context())
	if err != nil {
		a.logger.Warn("load ListenBrainz love queue stats", "error", err)
	} else {
		view.LovePending, view.LoveFailed = stats.Pending, stats.Failed
	}
	run, err := a.store.ListenBrainzLoveBackfill(r.Context())
	if err != nil {
		a.logger.Warn("load ListenBrainz love backfill", "error", err)
		return
	}
	view.BackfillState = run.State
	view.BackfillTotal = run.Total
	view.BackfillProcessed = run.Processed
	view.BackfillFailed = run.Failed
	view.BackfillSkippedNoID = run.SkippedNoID
	view.BackfillPercent = run.Percent()
	view.BackfillStartedAt = formatAdminTime(run.StartedAt)
	view.BackfillFinishedAt = formatAdminTime(run.FinishedAt)
	view.BackfillError = run.LastError
	view.BackfillRunning = run.State == storage.LoveBackfillRunning
	view.BackfillDone = run.Done()
}

func (a *App) handleSaveListenBrainz(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	enabled := r.FormValue("listenbrainz_enabled") != ""
	nowPlaying := r.FormValue("listenbrainz_now_playing") != ""
	rawURL := r.FormValue("listenbrainz_server_url")
	token := strings.TrimSpace(r.FormValue("listenbrainz_user_token"))

	normalizedURL, err := listenbrainz.NormalizeServerURL(rawURL)
	if err != nil {
		redirectWithNotice(w, r, lastFMSettingsPath, "ListenBrainz 设置保存失败："+err.Error())
		return
	}
	existing, err := a.store.ListenBrainzSettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tokenToValidate := token
	if tokenToValidate == "" && normalizedURL != existing.ServerURL && existing.UserToken != "" {
		tokenToValidate = existing.UserToken
	}

	notice := "ListenBrainz 播放记录设置已保存"
	if tokenToValidate != "" {
		var username string
		if a.listenbrainz != nil {
			normalizedURL, username, err = a.listenbrainz.ValidateToken(ctx, normalizedURL, tokenToValidate)
		} else {
			username, err = listenbrainz.NewClient(normalizedURL).ValidateToken(ctx, tokenToValidate)
		}
		if err != nil {
			redirectWithNotice(w, r, lastFMSettingsPath, "ListenBrainz Token 校验失败："+err.Error())
			return
		}
		if err := a.store.SetListenBrainzConnection(ctx, normalizedURL, tokenToValidate, username); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		notice = "已验证并连接 ListenBrainz 账号 " + username
	}
	if err := a.store.SaveListenBrainzPreferences(ctx, enabled, nowPlaying, normalizedURL); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if a.listenbrainz != nil {
		a.listenbrainz.Wake()
	}
	redirectWithNotice(w, r, lastFMSettingsPath, notice)
}

func (a *App) handleDisconnectListenBrainz(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.ClearListenBrainzConnection(r.Context(), ""); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已断开 ListenBrainz 账号")
}

func (a *App) handleRetryListenBrainz(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.RetryListenBrainzScrobblesNow(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if a.listenbrainz != nil {
		a.listenbrainz.Wake()
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已安排立即重新提交待发送的 ListenBrainz 播放记录")
}

func (a *App) handleSaveListenBrainzLoveSync(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	rawSync := strings.TrimSpace(r.FormValue("sync_loves"))
	enabled := rawSync == "1" || strings.EqualFold(rawSync, "true") || strings.EqualFold(rawSync, "on")
	if err := a.store.SetListenBrainzLoveSync(r.Context(), enabled); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if a.listenbrainz != nil {
		a.listenbrainz.Wake()
	}
	notice := "已开启 ListenBrainz 红心同步：之后收藏或取消收藏单曲时会同步到 ListenBrainz"
	if !enabled {
		notice = "已关闭 ListenBrainz 红心同步（本地收藏不受影响）"
	}
	redirectWithNotice(w, r, lastFMSettingsPath, notice)
}

func (a *App) handleStartListenBrainzLoveBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	ctx := r.Context()
	settings, err := a.store.ListenBrainzSettings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if settings.FeedbackUnsupported {
		redirectWithNotice(w, r, lastFMSettingsPath, "当前 ListenBrainz 服务器不支持红心反馈接口")
		return
	}
	if !settings.LoveSyncReady() {
		redirectWithNotice(w, r, lastFMSettingsPath, "请先连接 ListenBrainz 账号并开启红心同步")
		return
	}
	run, err := a.store.ListenBrainzLoveBackfill(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if run.State == storage.LoveBackfillRunning {
		redirectWithNotice(w, r, lastFMSettingsPath, "已有 ListenBrainz 回填任务在进行中")
		return
	}
	if run.State == storage.LoveBackfillCancelled && run.Cursor > 0 {
		remaining, err := a.store.CountLoveBackfillTracksAfter(ctx, run.Cursor)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if remaining > 0 {
			if err := a.store.ResumeListenBrainzLoveBackfill(ctx, run.Processed+remaining); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if a.listenbrainz != nil {
				a.listenbrainz.Wake()
			}
			redirectWithNotice(w, r, lastFMSettingsPath, "已继续把剩余收藏同步到 ListenBrainz，可在下方查看进度")
			return
		}
	}
	total, err := a.store.CountLoveBackfillTracks(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if total == 0 {
		redirectWithNotice(w, r, lastFMSettingsPath, "当前没有已收藏的单曲可回填")
		return
	}
	if err := a.store.BeginListenBrainzLoveBackfill(ctx, total); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if a.listenbrainz != nil {
		a.listenbrainz.Wake()
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已开始把现有收藏同步到 ListenBrainz，可在下方查看进度")
}

func (a *App) handleCancelListenBrainzLoveBackfill(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.CancelListenBrainzLoveBackfill(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已停止 ListenBrainz 回填；已经同步的歌曲保持收藏")
}

func (a *App) handleRetryListenBrainzLoves(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := a.store.RetryListenBrainzLovesNow(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if a.listenbrainz != nil {
		a.listenbrainz.Wake()
	}
	redirectWithNotice(w, r, lastFMSettingsPath, "已安排立即重试待同步的 ListenBrainz 红心")
}

func (a *App) handleListenBrainzLoveStatus(w http.ResponseWriter, r *http.Request) {
	view := a.listenBrainzView(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":             view.LoveSync,
		"pending":             view.LovePending,
		"failed":              view.LoveFailed,
		"lastError":           view.LoveLastError,
		"lastErrorAt":         view.LoveLastErrorAt,
		"feedbackUnsupported": view.FeedbackUnsupported,
		"backfill": map[string]any{
			"state":       view.BackfillState,
			"total":       view.BackfillTotal,
			"processed":   view.BackfillProcessed,
			"failed":      view.BackfillFailed,
			"skippedNoId": view.BackfillSkippedNoID,
			"percent":     view.BackfillPercent,
			"running":     view.BackfillRunning,
			"done":        view.BackfillDone,
			"startedAt":   view.BackfillStartedAt,
			"finishedAt":  view.BackfillFinishedAt,
			"lastError":   view.BackfillError,
		},
	})
}
