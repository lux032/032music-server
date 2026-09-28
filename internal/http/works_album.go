package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

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
	role := r.FormValue("role")
	if role == "" {
		role = "other"
	}
	// D1：专辑级关系类型只接受 ost / other。
	if role != "ost" && role != "other" {
		http.Error(w, "专辑级关系类型只支持 ost / other", http.StatusBadRequest)
		return
	}
	err := a.store.AddWorkAlbum(r.Context(), id, parseInt64(r.FormValue("albumId")), role)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "作品或专辑不存在", http.StatusNotFound)
			return
		}
		http.Error(w, "无法添加专辑关联", http.StatusBadRequest)
		return
	}
	target := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/works/"+strconv.FormatInt(id, 10))
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
	workID := parseInt64(r.FormValue("workId"))
	role := r.FormValue("role")
	if role == "" {
		role = "other"
	}
	// D1：专辑级关系类型只接受 ost / other。
	if role != "ost" && role != "other" {
		http.Error(w, "专辑级关系类型只支持 ost / other", http.StatusBadRequest)
		return
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
	redirectWithNotice(w, r, target, "作品关联已添加")
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
