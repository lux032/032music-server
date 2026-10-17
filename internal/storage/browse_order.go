package storage

import (
	"fmt"
	"slices"
	"strings"
)

// Browse sort specs shared by ListAlbums, ListTracks and ListArtists. Each
// sort maps to its primary keys (%[1]s is the direction) and its natural
// direction; Filters.Order ("asc"/"desc") overrides the natural direction of
// the primary keys only, tie-breakers keep their fixed order so pagination
// stays stable. With Order empty the result is exactly the historical order.
// Keys that may be empty (no year, no date, never played) sort last in both
// directions.

type browseSortSpec struct {
	keys string
	desc bool
}

// albumDefaultTitleKey is the historical album title order; with an active
// letter index the reading title wins.
const (
	albumTitleKey        = "a.sort_title COLLATE NOCASE %[1]s"
	albumIndexedTitleKey = "COALESCE(NULLIF(a.reading_title,''),a.sort_title) COLLATE NOCASE %[1]s"
	albumLastPlayedSQL   = "COALESCE((SELECT MAX(pp.last_played_at) FROM tracks pt JOIN playback_progress pp ON pp.track_id=pt.id WHERE pt.album_id=a.id),'')"
)

var albumSorts = map[string]browseSortSpec{
	"title":          {albumTitleKey, false},
	"year":           {"COALESCE(a.user_release_year,a.release_year,0)=0 ASC,COALESCE(a.user_release_year,a.release_year,0) %[1]s", true},
	"date":           {albumReleaseDateSort + "='' ASC," + albumReleaseDateSort + " %[1]s", true},
	"added":          {"a.added_at %[1]s", true},
	"recentlyPlayed": {albumLastPlayedSQL + "='' ASC," + albumLastPlayedSQL + " %[1]s", true},
}

// AlbumBrowseSorts lists the sort values /browse/albums accepts.
var AlbumBrowseSorts = []string{"title", "year", "date", "added", "recentlyPlayed", "random"}

// trackAlbumOrder is the historical track order (album, disc, track).
const trackAlbumOrder = "COALESCE(a.user_title,a.title),COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),t.id"

var trackSorts = map[string]browseSortSpec{
	"album":          {"COALESCE(a.user_title,a.title) %[1]s", false},
	"title":          {"COALESCE(t.user_title,t.title) COLLATE NOCASE %[1]s", false},
	"year":           {"COALESCE(a.user_release_year,a.release_year,0)=0 ASC,COALESCE(a.user_release_year,a.release_year,0) %[1]s", true},
	"date":           {albumReleaseDateSort + "='' ASC," + albumReleaseDateSort + " %[1]s", true},
	"recentlyPlayed": {"COALESCE(pp.last_played_at,'')='' ASC,COALESCE(pp.last_played_at,'') %[1]s", true},
	"plays":          {"COALESCE(pp.play_count,0) %[1]s,COALESCE(pp.last_played_at,'') %[1]s", true},
	"added":          {"t.added_at %[1]s", true},
	"duration":       {"COALESCE(t.duration_ms,0) %[1]s", false},
}

// TrackBrowseSorts lists the sort values /browse/tracks accepts.
var TrackBrowseSorts = []string{"album", "title", "year", "date", "recentlyPlayed", "plays", "added", "duration", "random"}

var artistSorts = map[string]browseSortSpec{
	"name":   {"name COLLATE NOCASE %[1]s", false},
	"albums": {"album_count %[1]s,name COLLATE NOCASE", true},
	"tracks": {"track_count %[1]s,name COLLATE NOCASE", true},
}

// ArtistBrowseSorts lists the sort values /browse/artists accepts.
var ArtistBrowseSorts = []string{"name", "albums", "tracks", "random"}

// ValidBrowseOrder reports whether order is an accepted Filters.Order value.
func ValidBrowseOrder(order string) bool {
	return order == "" || order == "asc" || order == "desc"
}

// ValidateBrowseSort checks sort against one of the *BrowseSorts lists.
func ValidateBrowseSort(sortKey string, allowed []string) error {
	if sortKey == "" || slices.Contains(allowed, sortKey) {
		return nil
	}
	return fmt.Errorf("invalid sort %q: must be one of %s", sortKey, strings.Join(allowed, ", "))
}

func browseDirection(order string, desc bool) string {
	switch order {
	case "asc":
		desc = false
	case "desc":
		desc = true
	}
	if desc {
		return "DESC"
	}
	return "ASC"
}

func (spec browseSortSpec) sql(order string) string {
	return fmt.Sprintf(spec.keys, browseDirection(order, spec.desc))
}

// randomOrderModulus is the prime 2^31-1; every intermediate value stays
// below 2^62, so SQLite integer arithmetic never overflows into REAL.
const randomOrderModulus = 2147483647

// randomOrderSQL is a seeded pseudo-random sort key for idColumn: the same
// seed yields the same permutation, so pages of one shuffle never overlap or
// skip rows. Two squaring rounds modulo a prime scramble consecutive ids;
// the id breaks the rare key collisions.
func randomOrderSQL(idColumn string, seed int64) string {
	if seed < 0 {
		seed = -(seed + 1)
	}
	s1 := seed % randomOrderModulus
	s2 := (seed / randomOrderModulus) % randomOrderModulus
	h1 := fmt.Sprintf("(((%s %% 4294967296)+%d)*1103515245 %% %d)", idColumn, s1, randomOrderModulus)
	h2 := fmt.Sprintf("((%[1]s*%[1]s+%[2]d) %% %[3]d)", h1, s2, randomOrderModulus)
	return fmt.Sprintf("(%[1]s*%[1]s %% %[2]d),%[3]s", h2, randomOrderModulus, idColumn)
}

// albumOrder returns a stable ORDER BY for album browsing. An explicit sort
// wins; with an active letter index and no explicit sort the reading-title
// order applies. a.id always terminates the key so pagination is stable.
func albumOrder(f Filters) string {
	if f.Sort == "random" {
		return randomOrderSQL("a.id", f.Seed)
	}
	if spec, ok := albumSorts[f.Sort]; ok && f.Sort != "title" {
		if f.Sort == "added" {
			return spec.sql(f.Order) + ",a.id"
		}
		return spec.sql(f.Order) + ",a.sort_title,a.id"
	}
	title := browseSortSpec{albumTitleKey, false}
	if f.Index != "" {
		title.keys = albumIndexedTitleKey
	}
	return title.sql(f.Order) + ",a.id"
}

// trackOrder returns a stable ORDER BY for track browsing; ties fall back to
// album order.
func trackOrder(f Filters) string {
	if f.Sort == "random" {
		return randomOrderSQL("t.id", f.Seed)
	}
	spec, ok := trackSorts[f.Sort]
	if !ok || f.Sort == "album" {
		if f.Order == "" {
			return trackAlbumOrder
		}
		return trackSorts["album"].sql(f.Order) + "," + trackAlbumOrder
	}
	if f.Sort == "title" || f.Sort == "added" {
		return spec.sql(f.Order) + ",t.id"
	}
	return spec.sql(f.Order) + "," + trackAlbumOrder
}

// artistOrder returns the ORDER BY for ListArtists (columns name,
// album_count, track_count are select aliases).
func artistOrder(f Filters) string {
	if f.Sort == "random" {
		return randomOrderSQL("ar.id", f.Seed)
	}
	if spec, ok := artistSorts[f.Sort]; ok && f.Sort != "name" {
		return spec.sql(f.Order) + ",ar.id"
	}
	if f.favoriteOrder && f.Order == "" {
		return "ar.favorited_at DESC, ar.id DESC"
	}
	return artistSorts["name"].sql(f.Order) + ",ar.id"
}
