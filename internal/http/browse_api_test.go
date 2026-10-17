package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// seedBrowseLibrary imports 12 single-track albums: "Album 01".."Album 12" by
// "Singer NN", released 2001..2011 except Album 12 (undated), with track
// durations growing from 61 s to 72 s.
func seedBrowseLibrary(t *testing.T, store *storage.Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		year := 2000 + i
		if i == 12 {
			year = 0
		}
		album := fmt.Sprintf("Album %02d", i)
		artist := fmt.Sprintf("Singer %02d", i)
		input := storage.ImportInput{
			LibraryID: library.ID, RelativePath: artist + "/" + album + "/01.flac",
			FileSize: 1024, ModifiedAtNS: int64(i),
			Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song %02d", i), Album: album, Artists: []string{artist}, AlbumArtists: []string{artist}, DiscNumber: 1, TrackNumber: 1, Year: year, DurationMillis: int64(60000 + i*1000)},
		}
		if err := store.ImportTrack(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
}

type browseItem struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Name         string `json:"name"`
	Year         int    `json:"year"`
	Duration     int64  `json:"durationMillis"`
	AlbumID      int64  `json:"albumId"`
	Artists      []storage.ArtistRef
	AlbumArtists []storage.ArtistRef `json:"albumArtists"`
}

func browsePage(t *testing.T, app *App, path string) ([]browseItem, int64) {
	t.Helper()
	rec := apiGet(t, app, path, testAppAPIToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	var body struct {
		Items []browseItem `json:"items"`
		Total int64        `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Items, body.Total
}

func titles(items []browseItem) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Title + item.Name
	}
	return out
}

func TestBrowseAPIOrderAndYearRange(t *testing.T) {
	app, store, _ := setupTestApp(t)
	seedBrowseLibrary(t, store)
	for _, path := range []string{"/api/v1/browse/albums", "/api/v1/browse/tracks", "/api/v1/browse/artists"} {
		assertAPIAuth(t, app, path)
	}

	// Natural direction of year is newest first; asc flips it, undated stays last.
	desc, _ := browsePage(t, app, "/api/v1/browse/albums?sort=year&limit=3")
	if got := titles(desc); !slices.Equal(got, []string{"Album 11", "Album 10", "Album 09"}) {
		t.Fatalf("year desc = %v", got)
	}
	asc, total := browsePage(t, app, "/api/v1/browse/albums?sort=year&order=asc&limit=100")
	if total != 12 || asc[0].Title != "Album 01" || asc[10].Title != "Album 11" || asc[11].Title != "Album 12" {
		t.Fatalf("year asc = %v (total %d)", titles(asc), total)
	}
	if desc, _ := browsePage(t, app, "/api/v1/browse/albums?sort=year&limit=100"); desc[11].Title != "Album 12" {
		t.Fatalf("undated album not last under desc: %v", titles(desc))
	}
	byName, _ := browsePage(t, app, "/api/v1/browse/albums?order=desc&limit=2")
	if got := titles(byName); !slices.Equal(got, []string{"Album 12", "Album 11"}) {
		t.Fatalf("title desc = %v", got)
	}

	// Album year range (C8); bounds are inclusive and undated albums never match.
	ranged, total := browsePage(t, app, "/api/v1/browse/albums?yearFrom=2003&yearTo=2005&sort=year&order=asc")
	if got := titles(ranged); total != 3 || !slices.Equal(got, []string{"Album 03", "Album 04", "Album 05"}) {
		t.Fatalf("year range = %v (total %d)", got, total)
	}
	if from, total := browsePage(t, app, "/api/v1/browse/albums?yearFrom=2010"); total != 2 || len(from) != 2 {
		t.Fatalf("yearFrom only = %v (total %d)", titles(from), total)
	}

	// Album artist refs (C7).
	if albums, _ := browsePage(t, app, "/api/v1/browse/albums?q=Album%2003"); len(albums) != 1 || len(albums[0].Artists) != 1 || albums[0].Artists[0].Name != "Singer 03" || albums[0].Artists[0].ID == 0 {
		t.Fatalf("album artists = %+v", albums)
	}
	tracks, _ := browsePage(t, app, "/api/v1/browse/tracks?sort=duration&order=desc&limit=2")
	if len(tracks) != 2 || tracks[0].Title != "Song 12" || tracks[1].Title != "Song 11" {
		t.Fatalf("duration desc = %v", titles(tracks))
	}
	if len(tracks[0].AlbumArtists) != 1 || tracks[0].AlbumArtists[0].Name != "Singer 12" {
		t.Fatalf("track album artists = %+v", tracks[0].AlbumArtists)
	}
	if tracks, _ := browsePage(t, app, "/api/v1/browse/tracks?yearFrom=2002&yearTo=2003&sort=year&order=asc"); !slices.Equal(titles(tracks), []string{"Song 02", "Song 03"}) {
		t.Fatalf("track year range = %v", titles(tracks))
	}
	if artists, _ := browsePage(t, app, "/api/v1/browse/artists?sort=name&order=desc&limit=1"); len(artists) != 1 || artists[0].Name != "Singer 12" {
		t.Fatalf("artists name desc = %v", titles(artists))
	}

	for _, path := range []string{
		"/api/v1/browse/albums?sort=plays",
		"/api/v1/browse/albums?order=up",
		"/api/v1/browse/albums?yearFrom=2005&yearTo=2003",
		"/api/v1/browse/albums?yearFrom=abc",
		"/api/v1/browse/tracks?sort=random&seed=x",
		"/api/v1/browse/artists?sort=added",
	} {
		if rec := apiGet(t, app, path, testAppAPIToken, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", path, rec.Code)
		}
	}
}

func TestBrowseAPISeededRandom(t *testing.T) {
	app, store, _ := setupTestApp(t)
	seedBrowseLibrary(t, store)
	for _, resource := range []string{"albums", "tracks", "artists"} {
		collect := func(seed int) []int64 {
			var ids []int64
			for offset := 0; offset < 12; offset += 5 {
				items, total := browsePage(t, app, fmt.Sprintf("/api/v1/browse/%s?sort=random&seed=%d&limit=5&offset=%d", resource, seed, offset))
				if total != 12 {
					t.Fatalf("%s total = %d", resource, total)
				}
				for _, item := range items {
					ids = append(ids, item.ID)
				}
			}
			return ids
		}
		first := collect(42)
		if again := collect(42); !slices.Equal(first, again) {
			t.Fatalf("%s: same seed, different order: %v vs %v", resource, first, again)
		}
		sorted := slices.Clone(first)
		slices.Sort(sorted)
		if len(sorted) != 12 || len(slices.Compact(sorted)) != 12 {
			t.Fatalf("%s: pages overlap or skip rows: %v", resource, first)
		}
		if slices.IsSorted(first) {
			t.Fatalf("%s: seed 42 kept id order: %v", resource, first)
		}
		differs := false
		for _, seed := range []int{7, 1000003, 987654321} {
			differs = differs || !slices.Equal(first, collect(seed))
		}
		if !differs {
			t.Fatalf("%s: every seed gave the same order", resource)
		}
	}
}

// The legacy lists keep their behaviour: sort values they never understood
// still mean the default order, and order/seed/yearFrom are ignored.
func TestLegacyListsIgnoreBrowseParameters(t *testing.T) {
	app, store, _ := setupTestApp(t)
	seedBrowseLibrary(t, store)
	for _, pair := range [][2]string{
		{"/api/v1/albums?limit=100", "/api/v1/albums?limit=100&sort=random&seed=3&order=desc&yearFrom=2005"},
		{"/api/v1/tracks?limit=100", "/api/v1/tracks?limit=100&sort=duration&order=desc"},
		{"/api/v1/artists?limit=100", "/api/v1/artists?limit=100&sort=random&order=desc"},
		{"/api/v1/albums?sort=year&limit=100", "/api/v1/albums?sort=year&order=asc&limit=100"},
	} {
		base, baseTotal := browsePage(t, app, pair[0])
		other, otherTotal := browsePage(t, app, pair[1])
		if baseTotal != otherTotal || !slices.Equal(titles(base), titles(other)) {
			t.Fatalf("%s changed the legacy list:\n%v\n%v", pair[1], titles(base), titles(other))
		}
	}
}
