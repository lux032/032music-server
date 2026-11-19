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
	link := func(index int) string {
		a := track.Artists[index]
		return fmt.Sprintf(`<a href="/admin/artists/%d">%s</a>`, a.ID, a.Name)
	}
	albumCredit := link(0) + " feat. " + link(1)
	trackCredit := albumCredit + " & " + link(2)
	body := getAdmin(t, app.Handler(), cookie, fmt.Sprintf("/admin/albums/%d", track.AlbumID))
	for _, want := range []string{
		`<h2 class="album-artist-links">` + albumCredit + "</h2>",
		`<small class="album-artist-links">` + trackCredit,
	} {
		if !strings.Contains(html.UnescapeString(body), want) {
			t.Errorf("missing linked credit %s", want)
		}
	}
}
