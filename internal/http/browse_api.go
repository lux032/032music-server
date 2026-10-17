package httpapi

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

// Browse API (C2/C3/C7/C8): /api/v1/browse/{albums,tracks,artists} accept
// every filter of the legacy /albums, /tracks and /artists lists plus
// order=asc|desc, sort=random&seed=<n> and (albums) yearFrom/yearTo. Items
// carry clickable artist refs: albums "artists", tracks "albumArtists". The
// legacy endpoints keep their exact behaviour (see legacyAPISort).

// browseFilters parses the shared browse parameters; invalid values are
// reported by name so the handler can answer 400.
func browseFilters(r *http.Request, sorts []string) (storage.Filters, []string) {
	q := r.URL.Query()
	f := filters(r)
	var invalid []string
	if err := storage.ValidateBrowseSort(f.Sort, sorts); err != nil {
		invalid = append(invalid, "sort="+f.Sort)
	}
	f.Order = q.Get("order")
	if !storage.ValidBrowseOrder(f.Order) {
		invalid = append(invalid, "order="+f.Order)
	}
	if raw := q.Get("seed"); raw != "" {
		seed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			invalid = append(invalid, "seed="+raw)
		}
		f.Seed = seed
	} else if f.Sort == "random" {
		// No seed: a fresh shuffle per request (pages are not stable).
		f.Seed = rand.Int64()
	}
	limit, offset := pageValues(r)
	f.Limit, f.Offset = limit, offset
	return f, invalid
}

func writeInvalidFilter(w http.ResponseWriter, invalid []string) bool {
	if len(invalid) == 0 {
		return false
	}
	writeAPIError(w, http.StatusBadRequest, "invalid_filter", strings.Join(invalid, ", "))
	return true
}

// parseYearRange reads yearFrom/yearTo (album browse); a reversed range is invalid.
func parseYearRange(r *http.Request, f *storage.Filters) []string {
	q := r.URL.Query()
	var invalid []string
	for _, name := range []string{"yearFrom", "yearTo"} {
		raw := q.Get(name)
		if raw == "" {
			continue
		}
		year, err := strconv.Atoi(raw)
		if err != nil || year < 0 || year > 9999 {
			invalid = append(invalid, name+"="+raw)
			continue
		}
		if name == "yearFrom" {
			f.YearFrom = year
		} else {
			f.YearTo = year
		}
	}
	if f.YearFrom > 0 && f.YearTo > 0 && f.YearFrom > f.YearTo {
		invalid = append(invalid, "yearFrom/yearTo")
	}
	return invalid
}

func (a *App) handleBrowseAlbums(w http.ResponseWriter, r *http.Request) {
	f, invalid := browseFilters(r, storage.AlbumBrowseSorts)
	invalid = append(invalid, parseYearRange(r, &f)...)
	if writeInvalidFilter(w, invalid) {
		return
	}
	values, err := a.store.ListAlbums(r.Context(), f)
	if err == nil {
		err = a.store.AttachAlbumArtistRefs(r.Context(), values)
	}
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	total, err := a.store.CountAlbums(r.Context(), f)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, values, total, f.Limit, f.Offset)
}

func (a *App) handleBrowseTracks(w http.ResponseWriter, r *http.Request) {
	f, invalid := browseFilters(r, storage.TrackBrowseSorts)
	var focusInvalid []string
	f.Focus, focusInvalid = parseTrackFocus(r.URL.Query())
	if writeInvalidFilter(w, append(invalid, focusInvalid...)) {
		return
	}
	values, err := a.store.ListTracks(r.Context(), f)
	if err == nil {
		err = a.store.AttachTrackAlbumArtistRefs(r.Context(), values)
	}
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	total, err := a.store.CountTracks(r.Context(), f)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, values, total, f.Limit, f.Offset)
}

func (a *App) handleBrowseArtists(w http.ResponseWriter, r *http.Request) {
	f, invalid := browseFilters(r, storage.ArtistBrowseSorts)
	if writeInvalidFilter(w, invalid) {
		return
	}
	values, err := a.store.ListArtists(r.Context(), f)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	total, err := a.store.CountArtists(r.Context(), f)
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	writePage(w, values, total, f.Limit, f.Offset)
}

// legacyAPISort keeps /albums, /tracks and /artists exactly as before the
// browse API: sort values they never understood (random, duration, ...)
// still fall back to the default order instead of taking the new meaning.
func legacyAPISort(f *storage.Filters, understood ...string) {
	for _, value := range understood {
		if f.Sort == value {
			return
		}
	}
	f.Sort = ""
}
