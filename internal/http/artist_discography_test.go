package httpapi

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestGroupArtistDiscographySeparatesParticipation(t *testing.T) {
	releases := []storage.ArtistRelease{
		{Album: storage.Album{ID: 1, ReleaseKind: "album"}, Relation: "appearance"},
		{Album: storage.Album{ID: 2, ReleaseKind: "album"}, Relation: "personal"},
		{Album: storage.Album{ID: 3, ReleaseKind: "single"}, Relation: "collaboration"},
		{Album: storage.Album{ID: 4, ReleaseKind: "compilation"}, Relation: "personal"},
		{Album: storage.Album{ID: 5, ReleaseKind: "single"}, Relation: "appearance"},
		{Album: storage.Album{ID: 6, ReleaseKind: "album"}, Relation: "personal"},
	}
	groups := groupArtistDiscography(releases)
	want := map[string][]int64{"专辑": {2, 6}, "合辑与现场": {4}, "合作发行": {3}, "参与作品": {1, 5}}
	if len(groups) != len(want) {
		t.Fatalf("groups = %+v", groups)
	}
	for _, group := range groups {
		var ids []int64
		for _, release := range group.Releases {
			ids = append(ids, release.ID)
		}
		if !reflect.DeepEqual(ids, want[group.Title]) {
			t.Fatalf("%s = %v, want %v", group.Title, ids, want[group.Title])
		}
	}
}

func TestArtistPageDiscography(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := t.Context()
	root := t.TempDir()
	if err := app.store.EnsureLibrary(ctx, "Discography", root); err != nil {
		t.Fatal(err)
	}
	library, err := app.store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		title, date           string
		albumArtists, artists []string
		composer              string
	}{
		{"ZZ newest personal", "2025-01-01", []string{"Singer"}, []string{"Singer"}, ""},
		{"AA older personal", "2021-01-01", []string{"Singer"}, []string{"Singer"}, ""},
		{"Joint release", "2024-01-01", []string{"Singer", "Other"}, []string{"Singer"}, ""},
		{"Guest release", "2023-01-01", []string{"Other"}, []string{"Singer"}, ""},
		{"Composer release", "2026-01-01", []string{"Other"}, []string{"Other"}, "Singer"},
	} {
		if err = app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: v.title + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: v.title, Artists: v.artists, AlbumArtists: v.albumArtists, Composer: v.composer, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"DATE": {v.date}, "RELEASETYPE": {"album"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := app.store.ListArtists(ctx, storage.Filters{Query: "Singer", Limit: 50})
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists = %+v, err = %v", artists, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/artists/"+strconv.FormatInt(artists[0].ID, 10), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	start := strings.Index(body, `<div class="artist-discography">`)
	end := strings.Index(body, `<section class="artist-tracks-section">`)
	if start < 0 || end < start {
		t.Fatal("missing discography markup")
	}
	directory := body[start:end]
	personal := strings.Index(directory, "<h2>专辑</h2>")
	joint := strings.Index(directory, "<h2>合作发行</h2>")
	guest := strings.Index(directory, "<h2>参与作品</h2>")
	newest := strings.Index(directory, "ZZ newest personal")
	older := strings.Index(directory, "AA older personal")
	if !(personal >= 0 && personal < newest && newest < older && older < joint && joint < strings.Index(directory, "Joint release") && strings.Index(directory, "Joint release") < guest && guest < strings.Index(directory, "Guest release")) {
		t.Fatalf("unexpected grouping/order: %s", directory)
	}
	if strings.Contains(directory, "Composer release") {
		t.Fatal("composer-only credit leaked into discography")
	}
}
