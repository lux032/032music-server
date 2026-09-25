package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestAdminArtistPageIgnoresFavoriteQuery(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "admin.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Unfavorited Admin Singer"}, AlbumArtists: []string{"Unfavorited Admin Singer"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/admin/artists/album?favorite=true", "/admin/artists/track?favorite=true"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(login.Result().Cookies()[0])
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Unfavorited Admin Singer") {
			t.Fatalf("admin %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestArtistDetailFavoritesAndMergeAPI(t *testing.T) {
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := storage.ImportInput{LibraryID: library.ID, RelativePath: "song.flac", FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Album Artist"}, Composer: "Composer", DiscNumber: 1, TrackNumber: 1}}
	if err := store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	artists, err := store.ListArtists(ctx, storage.Filters{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var singer, albumArtist int64
	for _, artist := range artists {
		if artist.Name == "Singer" {
			singer = artist.ID
		}
		if artist.Name == "Album Artist" {
			albumArtist = artist.ID
		}
	}
	if singer == 0 || albumArtist == 0 {
		t.Fatalf("artists=%+v", artists)
	}
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	detail := request("GET", "/api/v1/artists/"+jsonNumber(albumArtist))
	if detail.Code != 200 {
		t.Fatalf("detail=%d %s", detail.Code, detail.Body.String())
	}
	var body struct {
		Artist struct {
			ID         int64 `json:"id"`
			MergedFrom int64 `json:"mergedFrom"`
			IsFavorite bool  `json:"isFavorite"`
		} `json:"artist"`
		Albums      []storage.Album `json:"albums"`
		Tracks      []storage.Track `json:"tracks"`
		TracksTotal int64           `json:"tracksTotal"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(detail.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	keys := func(m map[string]json.RawMessage) []string {
		result := make([]string, 0, len(m))
		for key := range m {
			result = append(result, key)
		}
		sort.Strings(result)
		return result
	}
	if got, want := keys(raw), []string{"albums", "artist", "tracks", "tracksTotal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level keys=%v", got)
	}
	var artistJSON map[string]json.RawMessage
	if err := json.Unmarshal(raw["artist"], &artistJSON); err != nil {
		t.Fatal(err)
	}
	if got, want := keys(artistJSON), []string{"albumCount", "aliases", "artistType", "biography", "biographySource", "country", "id", "imageUrl", "isFavorite", "mergedFrom", "name", "trackCount"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("artist keys=%v", got)
	}
	if len(body.Albums) != 1 || len(body.Tracks) != 1 || body.TracksTotal != 1 || body.Tracks[0].Artist != "Singer" {
		t.Fatalf("detail=%+v", body)
	}
	if rec := request("GET", "/api/v1/artists/999999"); rec.Code != 404 {
		t.Fatalf("missing=%d %s", rec.Code, rec.Body.String())
	}
	if rec := request("PUT", "/api/v1/artists/999999/favorite"); rec.Code != 404 {
		t.Fatalf("missing favorite=%d", rec.Code)
	}
	if rec := request("PUT", "/api/v1/artists/"+jsonNumber(albumArtist)+"/favorite"); rec.Code != 204 {
		t.Fatalf("favorite=%d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/v1/artists?favorite=true", "/api/v1/artists?favorite=true&role=album", "/api/v1/favorites/artists?limit=1"} {
		rec := request("GET", path)
		var page struct {
			Items []storage.Artist `json:"items"`
			Total int64            `json:"total"`
			Limit int              `json:"limit"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 || page.Total != 1 || len(page.Items) != 1 || !page.Items[0].IsFavorite || page.Items[0].ID != albumArtist {
			t.Fatalf("page %s=%d %s err=%v", path, rec.Code, rec.Body.String(), err)
		}
	}
	operation, err := store.MergeArtists(ctx, albumArtist, singer)
	if err != nil {
		t.Fatal(err)
	}
	merged := request("GET", "/api/v1/artists/"+jsonNumber(albumArtist))
	if err := json.Unmarshal(merged.Body.Bytes(), &body); err != nil || merged.Code != 200 || body.Artist.ID != singer || body.Artist.MergedFrom != albumArtist || !body.Artist.IsFavorite {
		t.Fatalf("merged=%d %s err=%v", merged.Code, merged.Body.String(), err)
	}
	if rec := request("DELETE", "/api/v1/artists/"+jsonNumber(albumArtist)+"/favorite"); rec.Code != 204 {
		t.Fatalf("unset via merged=%d", rec.Code)
	}
	if rec := request("PUT", "/api/v1/artists/"+jsonNumber(albumArtist)+"/favorite"); rec.Code != 204 {
		t.Fatalf("set via merged=%d", rec.Code)
	}
	if err := store.RollbackArtistMerge(ctx, operation); err != nil {
		t.Fatal(err)
	}
	// Explicit PUT after merging must survive rollback.
	rec := request("GET", "/api/v1/artists/"+jsonNumber(singer))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Artist.IsFavorite {
		t.Fatalf("rollback target=%s err=%v", rec.Body.String(), err)
	}
	rec = request("DELETE", "/api/v1/artists/"+jsonNumber(albumArtist)+"/favorite")
	if rec.Code != 204 {
		t.Fatalf("unset=%d", rec.Code)
	}
	rec = request("GET", "/api/v1/favorites/artists")
	var page struct {
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || page.Total != 1 {
		t.Fatalf("empty=%s err=%v", rec.Body.String(), err)
	}
}
