package httpapi

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

type artistPartialTransport func(*http.Request) (*http.Response, error)

func (f artistPartialTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMatchArtistSecondaryDatabaseFailureSafeNotice(t *testing.T) {
	const mbid = "11111111-1111-4111-8111-111111111111"
	store, path := credentialTestStoreAt(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {mbid}}}}); err != nil {
		t.Fatal(err)
	}
	enableSource(t, store, "musicbrainz")
	enableSource(t, store, "lastfm")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER fail_lastfm BEFORE INSERT ON artist_external_profiles WHEN NEW.source='lastfm' BEGIN SELECT RAISE(ABORT,'private SQL trigger detail'); END`); err != nil {
		t.Fatal(err)
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = artistPartialTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"id":"` + mbid + `","name":"Artist","aliases":[],"tags":[],"relations":[]}`
		if r.URL.Host == "ws.audioscrobbler.com" {
			body = `{"artist":{"name":"Artist","mbid":"` + mbid + `","url":"https://last.fm/Artist"}}`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := enrichment.New(ctx, store, logger, t.TempDir())
	app, err := NewApp(credentialTestConfig(t), store, nil, manager, logger, "test")
	if err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("%v %v", artists, err)
	}
	id := artists[0].ID
	rec := postAdminForm(t, handler, cookie, csrfOf(t, app, cookie), "/admin/artists/"+strconv.FormatInt(id, 10)+"/match", nil)
	notice := notice303Of(t, rec)
	if !strings.Contains(notice, "身份已绑定") || !strings.Contains(notice, "Last.fm") || !strings.Contains(notice, "待人工确认") || strings.Contains(notice, "private") || strings.Contains(notice, "SQL") {
		t.Fatal(notice)
	}
	if got, err := store.ArtistExternalID(ctx, id, "musicbrainz"); err != nil || got != mbid {
		t.Fatalf("%s %v", got, err)
	}
	candidates, _ := store.ArtistCandidates(ctx, id)
	for _, candidate := range candidates {
		if candidate.Source == "lastfm" && candidate.Status != "candidate" {
			t.Fatal(candidate)
		}
	}
}
