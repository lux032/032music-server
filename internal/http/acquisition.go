package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/acquisition"
	"github.com/lux032/032music-server/internal/acquisition/jackett"
	"github.com/lux032/032music-server/internal/acquisition/lanhttp"
	"github.com/lux032/032music-server/internal/acquisition/qbit"
	"github.com/lux032/032music-server/internal/acquisition/torrent"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
)

// acquisitionTaskDTO is an allowlist: never serialize storage tasks or diagnostics.
type acquisitionTaskDTO struct {
	PublicID            string `json:"publicId"`
	RGID                string `json:"rgid"`
	Title               string `json:"title"`
	Artist              string `json:"artist"`
	Kind                string `json:"kind"`
	TorrentTitle        string `json:"torrentTitle"`
	TorrentSize         int64  `json:"torrentSize"`
	State               string `json:"state"`
	AttentionReason     string `json:"attentionReason"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
	FinishedAt          string `json:"finishedAt"`
	DownloadCompletedAt string `json:"downloadCompletedAt"`
	AlbumID             int64  `json:"albumId"`
	AlbumTitle          string `json:"albumTitle"`
	DetectedCount       int    `json:"detectedCount"`
	VerifiedAt          string `json:"verifiedAt"`
}

func (a *App) acquisitionTaskView(ctx context.Context, t storage.AcquisitionTask) (acquisitionTaskDTO, error) {
	v := acquisitionTaskDTO{PublicID: t.PublicID, RGID: t.RGID, Title: t.Title, Artist: t.Artist, Kind: t.Kind, TorrentTitle: t.TorrentTitle, TorrentSize: t.TorrentSize, State: t.State, AttentionReason: t.AttentionReason, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, FinishedAt: t.FinishedAt, DownloadCompletedAt: t.DownloadCompletedAt}
	if t.State == "IN_LIBRARY" {
		e, err := a.store.AcquisitionEvidence(ctx, t.ID)
		if err != nil {
			return v, err
		}
		v.AlbumID, v.DetectedCount, v.VerifiedAt = e.AlbumID, e.DetectedCount, e.VerifiedAt
		if e.AlbumID != 0 {
			album, err := a.store.AlbumByID(ctx, e.AlbumID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return v, err
			}
			if err == nil {
				v.AlbumTitle = album.Title
			}
		}
	}
	return v, nil
}
func (a *App) acquisitionError(w http.ResponseWriter, err error) {
	code, message, status := "acquisition_failed", "获取请求失败，请稍后重试", 500
	var ae *acquisition.Error
	var replace *storage.ErrReplacesReleaseGroup
	var api *jackett.APIError
	var remote *jackett.HTTPError
	var network interface{ NetworkFailure() bool }
	switch {
	case errors.Is(err, storage.ErrIdentityNotReady):
		code, message, status = "identity_not_ready", "请先重建发行组身份", 409
	case errors.Is(err, storage.ErrAcquisitionRGActive):
		code, message, status = "acquisition_active", "该发行正在获取中，请先取消任务", 409
	case errors.Is(err, storage.ErrAcquisitionHashBound):
		code, message, status = "hash_bound", "该种子已绑定其他获取任务", 409
	case errors.Is(err, storage.ErrAcquisitionStale):
		code, message, status = "stale", "任务状态已变化，请刷新", 409
	case errors.As(err, &replace):
		writeJSON(w, 409, map[string]any{"error": map[string]string{"code": "replaces_rg", "message": "该专辑已关联其他发行组，请确认替换"}, "currentRg": replace.RGID})
		return
	case errors.As(err, &ae):
		messages := map[string]string{"disabled": "获取功能未启用", "invalid_settings": "请检查获取设置", "invalid_rgid": "无效发行组 ID", "invalid_idempotency_key": "缺少幂等键", "idempotency_conflict": "幂等键已用于其他发行", "already_owned": "该发行已在曲库中", "ignored": "该发行已忽略，请先恢复推荐", "invalid_result": "请选择有效的搜索结果", "not_retryable": "该任务不能重试，请手动关联专辑", "category_missing": "qBittorrent 分类不存在"}
		if m, ok := messages[ae.Code]; ok {
			code, message, status = ae.Code, m, 409
			if strings.HasPrefix(code, "invalid_") || code == "category_missing" {
				status = 400
			}
		}
	case errors.Is(err, qbit.ErrAuth):
		code, message, status = "qb_auth", "qBittorrent 认证失败，请修改获取设置", 502
	case errors.Is(err, sourcegate.ErrRateLimited):
		code, message, status = "jackett_cooldown", "Jackett 请求冷却中，请稍后重试", 429
	case errors.As(err, &api):
		code, message, status = "jackett_failed", "Jackett 请求失败，请检查设置", 502
		if api.Code == 100 || api.Code == 101 || api.Code == 102 {
			code, message = "jackett_auth", "Jackett 认证或权限错误，请检查设置"
		}
		if api.Code == 500 || api.Code == 501 {
			code, message, status = "jackett_cooldown", "Jackett 请求冷却中，请稍后重试", 429
		}
	case errors.As(err, &remote):
		code, message, status = "jackett_failed", "Jackett 请求失败，请稍后重试", 502
		if remote.Status == 401 || remote.Status == 403 {
			code, message = "jackett_auth", "Jackett 认证或权限错误，请检查设置"
		}
		if remote.Status == 429 {
			code, message, status = "jackett_cooldown", "Jackett 请求冷却中，请稍后重试", 429
		}
	case errors.Is(err, jackett.ErrMagnetOnly):
		code, message, status = "magnet_only", "不支持磁力链接，请选择种子文件", 400
	case errors.Is(err, torrent.ErrPureV2):
		code, message, status = "pure_v2", "不支持纯 v2 种子", 400
	case errors.Is(err, sql.ErrNoRows):
		code, message, status = "not_found", "任务、专辑或搜索结果不存在或已过期", 404
	case errors.As(err, &network) && network.NetworkFailure():
		// The adapter's operation prefix is fixed locally, not a remote description.
		if strings.HasPrefix(err.Error(), "qBittorrent ") {
			code, message, status = "qb_unreachable", "无法连接 qBittorrent，请稍后重试", 502
		} else {
			code, message, status = "jackett_unreachable", "无法连接 Jackett，请稍后重试", 502
		}
	}
	var cooldown *sourcegate.CooldownError
	if errors.As(err, &cooldown) {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(cooldown.RetryAfter.Seconds()))))
	}
	if status == 429 {
		if api != nil && api.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(api.RetryAfter.Seconds()))))
		}
		if remote != nil && remote.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, int(remote.RetryAfter.Seconds()))))
		}
	}
	// Never log err: unknown errors may embed credentials or download references.
	if status == 500 {
		a.logger.Error("acquisition request failed", "code", code)
	}
	writeAPIError(w, status, code, message)
}
func (a *App) handleAcquisitionJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && !a.validCSRFHeader(r) {
		writeAPIError(w, 403, "csrf_invalid", "CSRF 校验失败")
		return
	}
	if a.acquisition == nil {
		writeAPIError(w, 503, "unavailable", "获取服务暂不可用")
		return
	}
	ctx := r.Context()
	if r.Method == "GET" {
		tasks, err := a.store.ListAcquisitionTasks(ctx)
		if err != nil {
			a.acquisitionError(w, err)
			return
		}
		items := make([]acquisitionTaskDTO, 0, len(tasks))
		for _, t := range tasks {
			v, err := a.acquisitionTaskView(ctx, t)
			if err != nil {
				a.acquisitionError(w, err)
				return
			}
			items = append(items, v)
		}
		watch, _, err := a.effectiveLibraryWatch(ctx)
		if err != nil {
			a.acquisitionError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "watchDisabled": !watch.Enabled})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/test-jackett") || strings.HasSuffix(r.URL.Path, "/test-qb") {
		var err error
		if strings.HasSuffix(r.URL.Path, "/test-qb") {
			err = a.acquisition.TestQB(ctx)
		} else {
			err = a.acquisition.TestJackett(ctx)
		}
		if err != nil {
			a.acquisitionError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	var body struct {
		Q              string `json:"q"`
		Force          bool   `json:"force"`
		SearchID       string `json:"searchId"`
		ResultIdx      *int   `json:"resultIdx"`
		IdempotencyKey string `json:"idempotencyKey"`
		AlbumID        int64  `json:"albumId"`
		ConfirmReplace bool   `json:"confirmReplace"`
	}
	if !strings.HasSuffix(r.URL.Path, "/cancel") && !strings.HasSuffix(r.URL.Path, "/retry") {
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body) != nil {
			writeAPIError(w, 400, "invalid_body", "无效请求")
			return
		}
	}
	var task storage.AcquisitionTask
	var err error
	status := 200
	if raw := r.PathValue("rgid"); raw != "" {
		id, ok := metadata.NormalizeMusicBrainzID(raw)
		if !ok {
			writeAPIError(w, 400, "invalid_rgid", "无效发行组 ID")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/search") {
			result, e := a.acquisition.Search(ctx, id, body.Q, body.Force)
			if e != nil {
				a.acquisitionError(w, e)
				return
			}
			writeJSON(w, 200, result)
			return
		}
		if body.ResultIdx == nil {
			writeAPIError(w, 400, "invalid_result", "请选择搜索结果")
			return
		}
		task, err = a.acquisition.Confirm(ctx, id, body.SearchID, *body.ResultIdx, body.IdempotencyKey)
		status = 202
	} else {
		id := r.PathValue("publicId")
		switch {
		case strings.HasSuffix(r.URL.Path, "/cancel"):
			task, err = a.acquisition.Cancel(ctx, id)
		case strings.HasSuffix(r.URL.Path, "/retry"):
			task, err = a.acquisition.Retry(ctx, id)
		case strings.HasSuffix(r.URL.Path, "/link"):
			if body.AlbumID <= 0 {
				writeAPIError(w, 400, "invalid_body", "请选择本地专辑")
				return
			}
			task, err = a.acquisition.Link(ctx, id, body.AlbumID, body.ConfirmReplace)
		}
	}
	if err != nil {
		if errors.Is(err, storage.ErrAcquisitionHashBound) {
			writeJSON(w, 409, map[string]any{"error": map[string]string{"code": "hash_bound", "message": "该种子已绑定其他获取任务"}, "publicId": task.PublicID, "title": task.Title, "artist": task.Artist})
			return
		}
		a.acquisitionError(w, err)
		return
	}
	view, err := a.acquisitionTaskView(ctx, task)
	if err != nil {
		a.acquisitionError(w, err)
		return
	}
	writeJSON(w, status, map[string]any{"task": view})
}

type acquisitionSettingsPage struct {
	Chrome
	Settings storage.AcquisitionSettings
	Notice   string
}

func (a *App) handleAcquisitionSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "无效表单", 400)
			return
		}
		if !a.validCSRF(r) {
			http.Error(w, "invalid CSRF token", 403)
			return
		}
		// Explicit fields, not a secret-bearing storage type decoded as a DTO.
		v := storage.AcquisitionSettingsUpdate{Enabled: r.FormValue("enabled") != "", JackettURL: strings.TrimRight(strings.TrimSpace(r.FormValue("jackett_url")), "/"), JackettIndexer: strings.TrimSpace(r.FormValue("jackett_indexer")), QBURL: strings.TrimRight(strings.TrimSpace(r.FormValue("qb_url")), "/"), QBUsername: strings.TrimSpace(r.FormValue("qb_username")), QBCategory: strings.TrimSpace(r.FormValue("qb_category")), JackettAPIKey: r.FormValue("jackett_api_key"), QBPassword: r.FormValue("qb_password"), ClearKey: r.FormValue("clear_key") != "", ClearPassword: r.FormValue("clear_password") != ""}
		for _, base := range []string{v.JackettURL, v.QBURL} {
			if base != "" {
				if _, err := lanhttp.New(base, 0); err != nil {
					redirectWithNotice(w, r, "/admin/settings/acquisition", "连接地址无效：请使用不含凭据或查询参数的 HTTP(S) 地址")
					return
				}
			}
		}
		if v.Enabled && (v.JackettURL == "" || v.QBURL == "") {
			redirectWithNotice(w, r, "/admin/settings/acquisition", "启用前请填写 Jackett 和 qBittorrent 地址")
			return
		}
		if err := a.store.UpdateAcquisitionSettings(r.Context(), v); err != nil {
			a.acquisitionError(w, err)
			return
		}
		if a.acquisition != nil {
			a.acquisition.SettingsChanged()
		}
		redirectWithNotice(w, r, "/admin/settings/acquisition", "获取设置已保存")
		return
	}
	settings, err := a.store.AcquisitionSettings(r.Context())
	if err != nil {
		a.acquisitionError(w, err)
		return
	}
	session, _ := a.sessions.get(r)
	a.render(w, 200, "acquisition-settings.html", acquisitionSettingsPage{Chrome: a.chromeFor(r.Context(), session, "settings"), Settings: settings, Notice: r.URL.Query().Get("notice")})
}
