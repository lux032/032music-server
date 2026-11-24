package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestAlbumPeopleMergesPerformersAndCredits(t *testing.T) {
	tracks := []storage.Track{
		{ID: 1, DiscNumber: 1, DurationMillis: 60000,
			Artists: []storage.Artist{{ID: 10, Name: "Singer", ImageURL: "/img/10"}},
			Credits: []storage.TrackCredit{{Role: "composer", Artists: []storage.ArtistRef{{ID: 20, Name: "Writer"}, {ID: 10, Name: "Singer"}}}}},
		{ID: 2, DiscNumber: 1, DurationMillis: 30000,
			Artists: []storage.Artist{{ID: 10, Name: "Singer"}},
			Credits: []storage.TrackCredit{{Role: "lyricist", Artists: []storage.ArtistRef{{ID: 20, Name: "Writer"}}}}},
		{ID: 3, DiscNumber: 2, DurationMillis: 0,
			Credits: []storage.TrackCredit{{Role: "arranger", Artists: []storage.ArtistRef{{ID: 30, Name: "  "}}}}},
	}
	people := albumPeople(tracks)
	if len(people) != 2 {
		t.Fatalf("want 2 people (blank names skipped), got %+v", people)
	}
	singer, writer := people[0], people[1]
	if singer.ID != 10 || singer.URL != "/admin/artists/10" || singer.Roles != "演唱 · 作曲" || singer.Tracks != 2 || singer.ImageURL != "/img/10" {
		t.Fatalf("performer row wrong: %+v", singer)
	}
	if writer.ID != 20 || writer.URL != "/admin/credits/20?role=composer" || writer.Roles != "作曲 · 作词" || writer.Tracks != 2 || writer.Initial != "W" {
		t.Fatalf("credit row wrong: %+v", writer)
	}
	if got := tracksDuration(tracks); got != 90000 {
		t.Fatalf("tracksDuration = %d", got)
	}
	if got := discSummary(tracks, 1); got != "2 首 · 1:30" {
		t.Fatalf("discSummary(1) = %q", got)
	}
	if got := discSummary(tracks, 2); got != "1 首" {
		t.Fatalf("discSummary(2) = %q", got)
	}
}

func TestNameInitialSkipsPunctuation(t *testing.T) {
	cases := map[string]string{"(K)NoW_NAME": "K", "[ahi:]": "A", "8082Audio": "8", "のみこ": "の", "+": "+", "": ""}
	for in, want := range cases {
		if got := nameInitial(in); got != want {
			t.Errorf("nameInitial(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlbumPageRendersParticipantAvatars(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := app.store.EnsureLibrary(ctx, "main", root); err != nil {
		t.Fatal(err)
	}
	lib, err := app.store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	err = app.store.ImportTrack(ctx, storage.ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "Album/01.flac",
		FileSize:     100,
		ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title:        "Song",
			Album:        "Test Album",
			Artists:      []string{"Singer With Photo"},
			AlbumArtists: []string{"Singer With Photo"},
			Composer:     "Composer With Photo",
			Arranger:     "Arranger Without Photo",
			DiscNumber:   1,
			TrackNumber:  1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	albums, err := app.store.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums = %+v, err = %v", albums, err)
	}
	tracks, err := app.store.ListTracks(ctx, storage.Filters{AlbumID: albums[0].ID, Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks = %+v, err = %v", tracks, err)
	}
	people := albumPeople(tracks)
	var singerID, composerID int64
	for _, p := range people {
		switch p.Name {
		case "Singer With Photo":
			singerID = p.ID
		case "Composer With Photo":
			composerID = p.ID
		}
	}
	if singerID == 0 || composerID == 0 {
		t.Fatalf("missing expected participants: %+v", people)
	}
	for _, id := range []int64{singerID, composerID} {
		rec := uploadAlbumArtwork2(t, app, cookie, "/admin/artists/"+strconv.FormatInt(id, 10)+"/image", csrf, "avatar.webp", webpBytes(t))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("upload image for artist %d status = %d", id, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/albums/"+strconv.FormatInt(albums[0].ID, 10), nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET album page status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, id := range []int64{singerID, composerID} {
		want := `/api/v1/artists/` + strconv.FormatInt(id, 10) + `/image?v=`
		if !strings.Contains(body, want) {
			t.Fatalf("album page missing participant avatar %q", want)
		}
	}
	if !strings.Contains(body, `<span class="person-avatar" aria-hidden="true">A</span>`) {
		t.Fatalf("album page missing initial fallback for participant without photo")
	}
}
