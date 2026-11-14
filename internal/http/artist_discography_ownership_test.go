package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestArtistAPIDiscographyUsesAlbumOwnershipNotDuets(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := t.Context()
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Ownership", root); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		album, title, kind    string
		albumArtists, artists []string
	}{
		{"Personal album", "Solo", "album", []string{"Owner"}, []string{"Owner"}},
		{"Personal album", "Guest duet", "album", []string{"Owner"}, []string{"Owner", "Guest"}},
		{"Joint album", "Joint song", "album", []string{"Owner", "Guest"}, []string{"Owner"}},
		{"VA compilation", "VA duet", "compilation", []string{"Various Artists"}, []string{"Owner", "Guest"}},
		{"Personal single", "Single duet", "single", []string{"Owner"}, []string{"Owner", "Guest"}},
	} {
		if err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID: library.ID, RelativePath: v.album + "/" + v.title + ".flac", FileSize: 1, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: v.title, Album: v.album, Artists: v.artists, AlbumArtists: v.albumArtists,
				DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"RELEASETYPE": {v.kind}}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	artists, err := store.ListArtists(ctx, storage.Filters{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, artist := range artists {
		ids[artist.Name] = artist.ID
	}
	request := func(t *testing.T, path string, out any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Owner", "Guest"} {
		t.Run(name, func(t *testing.T) {
			if ids[name] == 0 {
				t.Fatalf("missing artist %s", name)
			}
			path := "/api/v1/artists/" + jsonNumber(ids[name])
			var detail struct {
				Releases []storage.ArtistRelease `json:"releases"`
			}
			request(t, path, &detail)
			got := map[string]string{}
			for _, release := range detail.Releases {
				got[release.Title] = release.Relation
			}
			want := map[string]string{"Personal album": "personal", "Personal single": "personal", "Joint album": "collaboration", "VA compilation": "appearance"}
			if name == "Guest" {
				want["Personal album"] = "appearance"
				want["Personal single"] = "appearance"
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("releases = %v, want %v", got, want)
			}
			// Album classification must not remove either performer's track access
			// or flatten the duet's structured artist references.
			var tracks struct {
				Items []storage.Track `json:"items"`
			}
			request(t, path+"/tracks?limit=100", &tracks)
			found := false
			for _, track := range tracks.Items {
				if track.Title != "Guest duet" {
					continue
				}
				found = true
				if len(track.Artists) != 2 || track.Artists[0].ID != ids["Owner"] || track.Artists[1].ID != ids["Guest"] {
					t.Fatalf("duet artists = %+v", track.Artists)
				}
			}
			if !found {
				t.Fatal("guest duet missing from artist tracks")
			}
		})
	}
}
