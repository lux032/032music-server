package httpapi

import (
	"context"
	"fmt"
	"html"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestAlbumPageKeepsArtistJoinPhrasesBetweenLinks(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := context.Background()
	if err := app.store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := app.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = app.store.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "single.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "DEMON'S SHOT", Album: "Single", DiscNumber: 1, TrackNumber: 1,
			Raw: map[string][]string{
				"ARTIST": {"Aiobahn +81 feat. DEMONDICE & C"}, "ARTISTS": {"Aiobahn +81", "DEMONDICE", "C"},
				"ALBUMARTIST": {"Aiobahn +81 feat. DEMONDICE"}, "ALBUMARTISTS": {"Aiobahn +81", "DEMONDICE"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	tracks, err := app.store.ListTracks(ctx, storage.Filters{})
	if err != nil || len(tracks) != 1 || len(tracks[0].Artists) != 3 {
		t.Fatalf("track fixture: %+v, %v", tracks, err)
	}
	track := tracks[0]
	body := html.UnescapeString(getAdmin(t, app.Handler(), cookie, fmt.Sprintf("/admin/albums/%d", track.AlbumID)))
	header := between(t, body, `<h2 class="album-artist-links">`, `</h2>`)
	for _, name := range []string{track.Artists[0].Name, track.Artists[1].Name} {
		if !strings.Contains(header, fmt.Sprintf(`<a class="artist-name" href="/admin/artists/%d">%s</a>`, artistID(t, track, name), name)) {
			t.Errorf("album header missing linked %s: %s", name, header)
		}
	}
	if !strings.Contains(header, `data-artist-links="detail"`) || !strings.Contains(header, `data-join=" feat. "`) || strings.Contains(header, "> | <") {
		t.Fatalf("album header should keep the feat. join phrase, got %s", header)
	}
	trackLine := between(t, body, `<small class="album-artist-links">`, `</small>`)
	if !strings.Contains(trackLine, `data-artist-links="detail"`) || !strings.Contains(trackLine, " feat. ") || !strings.Contains(trackLine, " & ") {
		t.Fatalf("track row should keep join phrases, got %s", trackLine)
	}
	for _, artist := range track.Artists {
		if !strings.Contains(trackLine, fmt.Sprintf(`href="/admin/artists/%d"`, artist.ID)) {
			t.Errorf("track row missing artist %d", artist.ID)
		}
	}

	library := html.UnescapeString(getAdmin(t, app.Handler(), cookie, "/admin/albums"))
	card := between(t, library, `class="album-card-artists">`, `</p>`)
	if !strings.Contains(card, `data-artist-links="compact"`) || !strings.Contains(card, "> | <") || !strings.Contains(card, `data-join=" feat. "`) {
		t.Fatalf("album grid should use pipe separators and keep the join phrase for +N, got %s", card)
	}
	home := html.UnescapeString(getAdmin(t, app.Handler(), cookie, "/admin/home"))
	if strings.Contains(home, `<a class="h-card-sub"`) {
		t.Fatal("home cards must not emit each artist as its own grid item")
	}
	homeCard := between(t, home, `class="h-card-sub">`, `</div>`)
	if !strings.Contains(homeCard, `data-artist-links="compact"`) || !strings.Contains(homeCard, "> | <") {
		t.Fatalf("home card should keep artists on one compact line, got %s", homeCard)
	}
}

func artistID(t *testing.T, track storage.Track, name string) int64 {
	t.Helper()
	for _, artist := range track.Artists {
		if artist.Name == name {
			return artist.ID
		}
	}
	t.Fatalf("missing artist %s", name)
	return 0
}

func between(t *testing.T, body, start, end string) string {
	t.Helper()
	from := strings.Index(body, start)
	if from < 0 {
		t.Fatalf("missing %s", start)
	}
	from += len(start)
	to := strings.Index(body[from:], end)
	if to < 0 {
		t.Fatalf("missing %s after %s", end, start)
	}
	return body[from : from+to]
}
