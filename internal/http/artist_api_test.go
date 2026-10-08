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
		TopTracks   []storage.Track `json:"topTracks"`
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
	if got, want := keys(raw), []string{"albums", "artist", "releases", "topTracks", "tracksTotal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level keys=%v", got)
	}
	var artistJSON map[string]json.RawMessage
	if err := json.Unmarshal(raw["artist"], &artistJSON); err != nil {
		t.Fatal(err)
	}
	if got, want := keys(artistJSON), []string{"albumCount", "aliases", "artistType", "biography", "biographySource", "country", "id", "imageUrl", "isFavorite", "mergedFrom", "name", "trackCount"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("artist keys=%v", got)
	}
	if len(body.Albums) != 1 || body.TopTracks == nil || len(body.TopTracks) != 0 || body.TracksTotal != 1 {
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
	// The track list follows the merge to the surviving artist as well.
	mergedTracks := request("GET", "/api/v1/artists/"+jsonNumber(albumArtist)+"/tracks")
	var mergedPage struct {
		Items []storage.Track `json:"items"`
		Total int64           `json:"total"`
	}
	if err := json.Unmarshal(mergedTracks.Body.Bytes(), &mergedPage); err != nil || mergedTracks.Code != 200 || mergedPage.Total != 1 || len(mergedPage.Items) != 1 || mergedPage.Items[0].Title != "Song" {
		t.Fatalf("merged tracks=%d %s err=%v", mergedTracks.Code, mergedTracks.Body.String(), err)
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

func TestArtistTracksAPISortsPagesAndTopTracks(t *testing.T) {
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"Unplayed", "Played"} {
		input := storage.ImportInput{LibraryID: library.ID, RelativePath: title + ".flac", FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title, Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1}}
		if err := store.ImportTrack(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Query: "Played", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var played int64
	for _, track := range tracks {
		if track.Title == "Played" {
			played = track.ID
		}
	}
	for seq, event := range []storage.PlaybackEventInput{
		{Type: "start", State: "playing", PositionMillis: 0},
		{Type: "end", EndReason: "completed", PositionMillis: 200000},
	} {
		event.ClientID, event.ClientKind, event.SessionID, event.Seq, event.TrackID, event.DurationMillis = "device-1", "android", "artist-top-session", int64(seq+1), played, 200000
		if _, err := store.RecordPlaybackEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := store.ListArtists(ctx, storage.Filters{Limit: 100})
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists=%+v err=%v", artists, err)
	}
	singer := jsonNumber(artists[0].ID)
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	titles := func(items []storage.Track) string {
		names := make([]string, len(items))
		for i, track := range items {
			names[i] = track.Title
		}
		return strings.Join(names, ",")
	}

	var detail struct {
		TopTracks   []storage.Track `json:"topTracks"`
		TracksTotal int64           `json:"tracksTotal"`
	}
	rec := request("/api/v1/artists/" + singer)
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || rec.Code != 200 || titles(detail.TopTracks) != "Played" || detail.TopTracks[0].PlayCount != 1 || detail.TracksTotal != 2 {
		t.Fatalf("detail=%d %s err=%v", rec.Code, rec.Body.String(), err)
	}

	for _, tc := range []struct{ query, want string }{
		{"", "Unplayed,Played"},
		{"?sort=plays", "Played,Unplayed"},
		{"?sort=plays&order=asc", "Unplayed,Played"},
		{"?sort=title&limit=1&offset=1", "Unplayed"},
	} {
		var page struct {
			Items  []storage.Track `json:"items"`
			Total  int64           `json:"total"`
			Limit  int             `json:"limit"`
			Offset int             `json:"offset"`
		}
		rec := request("/api/v1/artists/" + singer + "/tracks" + tc.query)
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 || page.Total != 2 || titles(page.Items) != tc.want {
			t.Fatalf("%s=%d %s err=%v want %s", tc.query, rec.Code, rec.Body.String(), err, tc.want)
		}
	}
	for _, query := range []string{"?sort=popular", "?order=sideways"} {
		if rec := request("/api/v1/artists/" + singer + "/tracks" + query); rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_request") {
			t.Fatalf("%s=%d %s", query, rec.Code, rec.Body.String())
		}
	}
	if rec := request("/api/v1/artists/999999/tracks"); rec.Code != 404 {
		t.Fatalf("missing=%d %s", rec.Code, rec.Body.String())
	}
}

func TestArtistPageHotTracksAndTracksPlaySort(t *testing.T) {
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"Quiet Song", "Loud Song"} {
		input := storage.ImportInput{LibraryID: library.ID, RelativePath: title + ".flac", FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title, Album: "Album", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1}}
		if err := store.ImportTrack(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := store.ListArtists(ctx, storage.Filters{Limit: 100})
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists=%+v err=%v", artists, err)
	}
	singer := jsonNumber(artists[0].ID)
	cookie := adminCookie(t, app)
	get := func(path string, admin bool) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if admin {
			req.AddCookie(cookie)
		} else {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s=%d %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	section := func() string {
		body := get("/admin/artists/"+singer, true)
		start := strings.Index(body, `<section class="artist-tracks-section">`)
		if start < 0 {
			t.Fatal("missing tracks section")
		}
		end := strings.Index(body[start:], "</section>")
		return body[start : start+end]
	}

	// Without plays the section falls back to discography order.
	unplayed := section()
	if !strings.Contains(unplayed, "<h2>歌曲</h2>") || strings.Contains(unplayed, " 次</small>") || strings.Index(unplayed, "Quiet Song") > strings.Index(unplayed, "Loud Song") {
		t.Fatalf("fallback section=%s", unplayed)
	}

	tracks, err := store.ListTracks(ctx, storage.Filters{Query: "Loud", Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks=%+v err=%v", tracks, err)
	}
	for seq, event := range []storage.PlaybackEventInput{
		{Type: "start", State: "playing", PositionMillis: 0},
		{Type: "end", EndReason: "completed", PositionMillis: 200000},
	} {
		event.ClientID, event.ClientKind, event.SessionID, event.Seq, event.TrackID, event.DurationMillis = "device-1", "web", "artist-page-session", int64(seq+1), tracks[0].ID, 200000
		if _, err := store.RecordPlaybackEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	popular := section()
	if !strings.Contains(popular, "<h2>热门歌曲</h2>") || !strings.Contains(popular, "播放 1 次") || !strings.Contains(popular, "Loud Song") || strings.Contains(popular, "Quiet Song") {
		t.Fatalf("popular section=%s", popular)
	}

	// The tracks page and API expose the play-count sort.
	page := get("/admin/tracks?artist="+singer+"&sort=plays", true)
	if loud, quiet := strings.Index(page, `data-track-title="Loud Song"`), strings.Index(page, `data-track-title="Quiet Song"`); loud < 0 || quiet < 0 || loud > quiet || !strings.Contains(page, "播放次数") {
		t.Fatalf("tracks page plays sort loud=%d quiet=%d", loud, quiet)
	}
	var list struct {
		Items []storage.Track `json:"items"`
	}
	if err := json.Unmarshal([]byte(get("/api/v1/tracks?sort=plays", false)), &list); err != nil || len(list.Items) != 2 || list.Items[0].Title != "Loud Song" {
		t.Fatalf("api plays sort=%+v err=%v", list.Items, err)
	}
}
