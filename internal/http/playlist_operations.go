package httpapi

import (
	"context"
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
)

func (a *App) deletePlaylist(ctx context.Context, id int64) error {
	a.customImageMu.Lock()
	defer a.customImageMu.Unlock()
	names, err := a.store.DeletePlaylistWithImages(ctx, id)
	if err != nil {
		return err
	}
	a.removeUnreferenced(ctx, names)
	return nil
}
func (a *App) playlistItemResponse(w http.ResponseWriter, r *http.Request, result storage.PlaylistItemResult, err error) {
	if err != nil {
		a.writeFeatureError(w, r, err, "playlist_items_failed")
		return
	}
	writeJSON(w, 200, result)
}
func (a *App) handleAppendPlaylistItems(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TrackIDs *[]int64 `json:"trackIds"`
		AlbumID  *int64   `json:"albumId"`
	}
	if !decode(w, r, &input) {
		return
	}
	if (input.TrackIDs == nil) == (input.AlbumID == nil) || input.AlbumID != nil && *input.AlbumID <= 0 {
		writeAPIError(w, 400, "invalid_request", "Specify exactly one of trackIds or a positive albumId.")
		return
	}
	var ids []int64
	if input.TrackIDs != nil {
		ids = *input.TrackIDs
	}
	result, err := a.store.AppendPlaylistItems(r.Context(), parseInt64(r.PathValue("id")), ids, input.AlbumID)
	a.playlistItemResponse(w, r, result, err)
}
func (a *App) handleRemovePlaylistItems(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TrackIDs         []int64 `json:"trackIds"`
		ExpectedRevision *int64  `json:"expectedRevision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedRevision == nil || *input.ExpectedRevision < 0 || input.TrackIDs == nil {
		writeAPIError(w, 400, "invalid_request", "trackIds and nonnegative expectedRevision are required.")
		return
	}
	result, err := a.store.RemovePlaylistItems(r.Context(), parseInt64(r.PathValue("id")), input.TrackIDs, input.ExpectedRevision)
	a.playlistItemResponse(w, r, result, err)
}
func (a *App) handleInsertPlaylistItem(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TrackID int64 `json:"trackId"`
		Index   *int  `json:"index"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Index == nil || *input.Index < 0 {
		writeAPIError(w, 400, "invalid_request", "A nonnegative index is required.")
		return
	}
	result, err := a.store.InsertPlaylistItemAt(r.Context(), parseInt64(r.PathValue("id")), input.TrackID, *input.Index)
	a.playlistItemResponse(w, r, result, err)
}
func (a *App) handlePlaylistOrder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TrackIDs         []int64 `json:"trackIds"`
		ExpectedRevision *int64  `json:"expectedRevision"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.ExpectedRevision == nil || *input.ExpectedRevision < 0 || input.TrackIDs == nil {
		writeAPIError(w, 400, "invalid_request", "trackIds and nonnegative expectedRevision are required.")
		return
	}
	result, err := a.store.ReorderPlaylistItems(r.Context(), parseInt64(r.PathValue("id")), input.TrackIDs, input.ExpectedRevision)
	a.playlistItemResponse(w, r, result, err)
}
