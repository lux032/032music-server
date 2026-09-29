package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestAdminReturnPathAcceptsOnlyLocalAdminPaths(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected string
	}{
		{name: "admin path", value: "/admin/albums?page=2", expected: "/admin/albums?page=2"},
		{name: "old notice removed", value: "/admin/albums?page=2&notice=old", expected: "/admin/albums?page=2"},
		{name: "absolute URL", value: "https://example.com/admin", expected: "/admin/favorites"},
		{name: "protocol relative URL", value: "//example.com/admin", expected: "/admin/favorites"},
		{name: "non admin path", value: "/api/v1/albums", expected: "/admin/favorites"},
		{name: "admin prefix confusion", value: "/administrator", expected: "/admin/favorites"},
		{name: "empty", value: "", expected: "/admin/favorites"},
		// M1：百分编码不是走私通道。
		{name: "encoded backslash after dotdot", value: "/admin/../%5Cevil.com", expected: "/admin/favorites"},
		{name: "encoded traversal and backslash", value: "/admin/..%2F..%2F%5Cx", expected: "/admin/favorites"},
		{name: "encoded crlf", value: "/admin/%0d%0a", expected: "/admin/favorites"},
		{name: "encoded question mark stays encoded", value: "/admin/x%3Fy", expected: "/admin/x%3Fy"},
		{name: "encoded traversal out of admin", value: "/admin/..%2Fapi", expected: "/admin/favorites"},
		{name: "raw backslash", value: "/\\evil.com", expected: "/admin/favorites"},
		{name: "fragment preserved", value: "/admin/works/9#edit", expected: "/admin/works/9#edit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if actual := safeAdminReturnTo(test.value, "/admin/favorites"); actual != test.expected {
				t.Fatalf("safeAdminReturnTo() = %q, want %q", actual, test.expected)
			}
		})
	}
}

func TestPlaylistTrackIDsPreservesOrder(t *testing.T) {
	tracks := []storage.Track{{ID: 9}, {ID: 3}, {ID: 12}}
	want := []int64{9, 3, 12}
	if got := playlistTrackIDs(tracks); !reflect.DeepEqual(got, want) {
		t.Fatalf("playlistTrackIDs() = %v, want %v", got, want)
	}
}

func TestIndexOfTrackID(t *testing.T) {
	values := []int64{9, 3, 12}
	if got := indexOfTrackID(values, 3); got != 1 {
		t.Fatalf("indexOfTrackID() = %d, want 1", got)
	}
	if got := indexOfTrackID(values, 99); got != -1 {
		t.Fatalf("indexOfTrackID() = %d, want -1", got)
	}
}

func TestAdminPlaybackRendersTrackContainer(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	importTrack := func(path, title, container string) {
		t.Helper()
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: path, FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: title, Album: "Album " + title, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Container: container}}); err != nil {
			t.Fatal(err)
		}
	}
	importTrack("mp3-song.mp3", "MP3 Song", "mp3")
	importTrack("unknown-song", "Unknown Song", "")
	tracks, err := store.ListTracks(ctx, storage.Filters{})
	if err != nil || len(tracks) != 2 {
		t.Fatalf("tracks = %v (%v)", tracks, err)
	}
	for _, track := range tracks {
		if err := store.UpdatePlayback(ctx, storage.PlaybackUpdate{TrackID: track.ID, State: "playing", PositionMillis: 1000, DurationMillis: 200000}); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/playback", nil)
	req.AddCookie(adminCookie(t, app))
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `data-track-container="FLAC"`) {
		t.Fatalf("playback page still hardcodes FLAC container: %s", body)
	}
	articles := map[string]string{}
	for _, chunk := range strings.Split(body, "<article ")[1:] {
		tag := chunk[:strings.Index(chunk, ">")]
		for _, title := range []string{"MP3 Song", "Unknown Song"} {
			if strings.Contains(tag, `data-track-title="`+title+`"`) {
				articles[title] = tag
			}
		}
	}
	if !strings.Contains(articles["MP3 Song"], `data-track-container="mp3"`) {
		t.Fatalf("mp3 row tag = %q", articles["MP3 Song"])
	}
	if tag, ok := articles["Unknown Song"]; !ok || strings.Contains(tag, "data-track-container") {
		t.Fatalf("empty-container row should omit data-track-container, tag = %q (found=%v)", tag, ok)
	}
}
