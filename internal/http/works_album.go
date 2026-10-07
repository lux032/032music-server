package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

func (a *App) handleAPIWorkAlbums(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	if _, err := a.store.WorkByID(r.Context(), id); err != nil {
		writeWorkResult(w, http.StatusOK, storage.Work{}, err)
		return
	}
	values, err := a.store.AlbumsForWork(r.Context(), id)
	apiResult(w, values, err)
}
func (a *App) handleAPIAddWorkAlbum(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AlbumID int64  `json:"albumId"`
		Role    string `json:"role"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := a.store.AddWorkAlbum(r.Context(), parseInt64(r.PathValue("id")), input.AlbumID, input.Role); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		} else if !errors.Is(err, storage.ErrInvalidWork) {
			status = http.StatusInternalServerError
		}
		writeAPIError(w, status, "association_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) handleAPIRemoveWorkAlbum(w http.ResponseWriter, r *http.Request) {
	err := a.store.RemoveWorkAlbum(r.Context(), parseInt64(r.PathValue("id")), parseInt64(r.PathValue("albumId")))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		writeAPIError(w, status, "association_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) handleAddWorkAlbum(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	target := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/works/"+strconv.FormatInt(id, 10))
	albumID := parseInt64(r.FormValue("albumId"))
	if albumID <= 0 {
		redirectWithNotice(w, r, target, "请从搜索结果中选择专辑")
		return
	}
	role := r.FormValue("role")
	if role == "" {
		role = "other"
	}
	// D1：专辑级关系类型只接受 ost / other。
	if role != "ost" && role != "other" {
		http.Error(w, "专辑级关系类型只支持 ost / other", http.StatusBadRequest)
		return
	}
	err := a.store.AddWorkAlbum(r.Context(), id, albumID, role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "作品或专辑不存在", http.StatusNotFound)
			return
		}
		http.Error(w, "无法添加专辑关联", http.StatusBadRequest)
		return
	}
	redirectWithNotice(w, r, target, "专辑关联已添加")
}
func (a *App) handleRemoveWorkAlbum(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	id := parseInt64(r.PathValue("id"))
	err := a.store.RemoveWorkAlbum(r.Context(), id, parseInt64(r.PathValue("albumId")))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	target := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/works/"+strconv.FormatInt(id, 10))
	redirectWithNotice(w, r, target, "专辑关联已解除")
}

func (a *App) handleAddAlbumWork(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	albumID := parseInt64(r.PathValue("id"))
	rawWorkID := strings.TrimSpace(r.FormValue("workId"))
	bangumiSubject := strings.TrimSpace(r.FormValue("bangumiSubject"))
	role := r.FormValue("role")
	if role == "" {
		role = "other"
	}
	// D1：专辑级关系类型只接受 ost / other。
	if role != "ost" && role != "other" {
		http.Error(w, "专辑级关系类型只支持 ost / other", http.StatusBadRequest)
		return
	}
	if rawWorkID != "" && bangumiSubject != "" {
		http.Error(w, "请只填写本地作品 ID 或 Bangumi 条目其中一项", http.StatusBadRequest)
		return
	}
	if rawWorkID == "" && bangumiSubject == "" {
		http.Error(w, "请从搜索结果选择本地作品，或填写 Bangumi 条目链接 / ID", http.StatusBadRequest)
		return
	}
	if _, err := a.store.AlbumByID(r.Context(), albumID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "专辑不存在", http.StatusNotFound)
			return
		}
		http.Error(w, "无法添加作品关联", http.StatusInternalServerError)
		return
	}
	var workID int64
	notice := "作品关联已添加"
	if bangumiSubject != "" {
		id, created, failure := a.ensureBangumiWork(r.Context(), bangumiSubject)
		if failure != nil {
			http.Error(w, failure.message, failure.status)
			return
		}
		workID = id
		if created {
			notice = "已从 Bangumi 创建作品并关联"
		}
	} else {
		workID = parseInt64(rawWorkID)
		if _, err := a.store.WorkByID(r.Context(), workID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, fmt.Sprintf("本地作品 #%s 不存在。作品 ID 是本项目内的编号，不是 Bangumi ID；若要按 Bangumi 条目关联，请填写在 Bangumi 条目一栏。", rawWorkID), http.StatusNotFound)
				return
			}
			http.Error(w, "无法添加作品关联", http.StatusInternalServerError)
			return
		}
	}
	err := a.store.AddWorkAlbum(r.Context(), workID, albumID, role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "作品或专辑不存在", http.StatusNotFound)
			return
		}
		http.Error(w, "无法添加作品关联", http.StatusBadRequest)
		return
	}
	target := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/albums/"+strconv.FormatInt(albumID, 10)+"#edit")
	redirectWithNotice(w, r, target, notice)
}

func (a *App) handleRemoveAlbumWork(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	albumID := parseInt64(r.PathValue("id"))
	workID := parseInt64(r.PathValue("workId"))
	err := a.store.RemoveWorkAlbum(r.Context(), workID, albumID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	target := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/albums/"+strconv.FormatInt(albumID, 10)+"#edit")
	redirectWithNotice(w, r, target, "作品关联已解除")
}
func (a *App) handleRefreshWorks(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.scanner == nil {
		http.Error(w, "scanner unavailable", http.StatusServiceUnavailable)
		return
	}
	// Keep the synchronous refresh alive even if the browser disconnects.
	stats, err := a.scanner.RefreshAlbumWorks(context.WithoutCancel(r.Context()))
	if err != nil {
		if errors.Is(err, scanner.ErrScanRunning) {
			http.Redirect(w, r, "/admin/works?notice="+url.QueryEscape("扫描进行中，请稍后"), http.StatusSeeOther)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	notice := fmt.Sprintf("已重算 %d 张专辑，新建 %d 个作品，清理 %d 个作品，待处理无引用作品 %d 个，失败专辑 %d 张", stats.AlbumsRefreshed, stats.WorksCreated, stats.WorksDeleted, stats.ProtectedUnreferenced, stats.AlbumsFailed)
	http.Redirect(w, r, "/admin/works?notice="+url.QueryEscape(notice), http.StatusSeeOther)
}
