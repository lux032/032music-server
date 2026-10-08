package httpapi

import (
	"net/http"

	"github.com/lux032/032music-server/internal/storage"
)

type artistDetailResponse struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	ImageURL        string   `json:"imageUrl"`
	IsFavorite      bool     `json:"isFavorite"`
	AlbumCount      int64    `json:"albumCount"`
	TrackCount      int64    `json:"trackCount"`
	Biography       string   `json:"biography"`
	BiographySource string   `json:"biographySource"`
	Country         string   `json:"country"`
	ArtistType      string   `json:"artistType"`
	Aliases         []string `json:"aliases"`
	MergedFrom      int64    `json:"mergedFrom"`
}

func (a *App) handleAPIArtist(w http.ResponseWriter, r *http.Request) {
	requested := parseInt64(r.PathValue("id"))
	id, err := a.store.CanonicalArtistID(r.Context(), requested)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	detail, err := a.store.ArtistDetail(r.Context(), id)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	albums, err := a.store.ArtistAlbums(r.Context(), id)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	if err = a.store.AttachAlbumArtistRefs(r.Context(), albums); err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	// Releases mirror the web artist page: own and collaboration albums plus
	// albums where the artist only sings some tracks (relation=appearance).
	releases, err := a.store.ArtistDiscography(r.Context(), id)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	releaseAlbums := make([]storage.Album, len(releases))
	for i := range releases {
		releaseAlbums[i] = releases[i].Album
	}
	if err = a.store.AttachAlbumArtistRefs(r.Context(), releaseAlbums); err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	for i := range releases {
		releases[i].Album = releaseAlbums[i]
	}
	topTracks, err := a.store.ArtistTopTracks(r.Context(), id, storage.ArtistTopTrackLimit)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	total, err := a.store.ArtistTrackCount(r.Context(), id)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	mergedFrom := int64(0)
	if requested != id {
		mergedFrom = requested
	}
	aliases := detail.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	writeJSON(w, http.StatusOK, struct {
		Artist      artistDetailResponse    `json:"artist"`
		Albums      []storage.Album         `json:"albums"`
		Releases    []storage.ArtistRelease `json:"releases"`
		TopTracks   []storage.Track         `json:"topTracks"`
		TracksTotal int64                   `json:"tracksTotal"`
	}{artistDetailResponse{detail.ID, detail.Name, detail.ImageURL, detail.IsFavorite, detail.AlbumCount, detail.TrackCount, detail.Biography, detail.BiographySource, detail.Country, detail.ArtistType, aliases, mergedFrom}, albums, releases, topTracks, total})
}

// handleAPIArtistTracks pages the tracks shown on the artist detail page
// with a selectable sort; see storage.ArtistTrackSorts.
func (a *App) handleAPIArtistTracks(w http.ResponseWriter, r *http.Request) {
	id, err := a.store.CanonicalArtistID(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	limit, offset := pageValues(r)
	q := r.URL.Query()
	tracks, total, err := a.store.ArtistTracks(r.Context(), id, storage.ArtistTrackQuery{Sort: q.Get("sort"), Order: q.Get("order"), Limit: limit, Offset: offset})
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, tracks, total, limit, offset)
}

func (a *App) handleSetArtistFavorite(w http.ResponseWriter, r *http.Request) {
	a.favoriteResult(w, r, a.store.SetArtistFavorite(r.Context(), parseInt64(r.PathValue("id")), true))
}
func (a *App) handleUnsetArtistFavorite(w http.ResponseWriter, r *http.Request) {
	a.favoriteResult(w, r, a.store.SetArtistFavorite(r.Context(), parseInt64(r.PathValue("id")), false))
}
func (a *App) handleFavoriteArtists(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageValues(r)
	items, total, err := a.store.FavoriteArtists(r.Context(), limit, offset)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, items, total, limit, offset)
}
