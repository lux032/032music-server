package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/lux032/032music-server/internal/storage"
)

// selectedAlbumIDs returns the posted album ids in selection order; the
// browser appends them in the order the user ticked the cards.
func selectedAlbumIDs(r *http.Request) []int64 {
	_ = r.ParseForm()
	values := r.PostForm["album"]
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		ids = append(ids, parseInt64(value))
	}
	return ids
}

// handleMergeAlbums merges the second and later selected albums into the
// first selected (main) album.
func (a *App) handleMergeAlbums(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	back := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/albums")
	ids := selectedAlbumIDs(r)
	if len(ids) < 2 {
		redirectWithNotice(w, r, back, "合并至少需要选择两张专辑")
		return
	}
	title, err := a.store.AlbumTitleByID(r.Context(), ids[0])
	if err != nil {
		redirectWithNotice(w, r, back, "主专辑不存在，请刷新后重试")
		return
	}
	merged, err := a.store.MergeAlbums(r.Context(), ids[0], ids[1:])
	if err != nil {
		a.albumBulkFailed(w, r, back, "merge albums", err)
		return
	}
	a.logger.Info("albums merged", "target", ids[0], "sources", ids[1:])
	redirectWithNotice(w, r, back, "已将 "+strconv.Itoa(merged)+" 张专辑合并到《"+title+"》")
}

// handleDeleteAlbums removes the selected albums from the library. Audio
// files stay on disk and are skipped by later scans.
func (a *App) handleDeleteAlbums(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	back := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/albums")
	ids := selectedAlbumIDs(r)
	if len(ids) == 0 {
		redirectWithNotice(w, r, back, "请先选择要删除的专辑")
		return
	}
	deleted, err := a.store.DeleteAlbums(r.Context(), ids)
	if err != nil {
		a.albumBulkFailed(w, r, back, "delete albums", err)
		return
	}
	a.logger.Info("albums deleted", "albums", ids)
	redirectWithNotice(w, r, back, "已从曲库删除 "+strconv.Itoa(deleted)+" 张专辑")
}

func (a *App) albumBulkFailed(w http.ResponseWriter, r *http.Request, back, action string, err error) {
	if errors.Is(err, storage.ErrAlbumSelection) {
		redirectWithNotice(w, r, back, "所选专辑已变化，请刷新后重试")
		return
	}
	a.logger.Error(action, "error", err)
	http.Error(w, "操作失败", http.StatusInternalServerError)
}
