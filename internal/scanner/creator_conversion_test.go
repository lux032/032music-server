package scanner

import (
	"context"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyCreatorConversionRequiresExplicitScan(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "song.flac"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(ctx, store, logger(), library, t.TempDir())
	composer := "A, B"
	manager.readMetadata = func(string) (metadata.AudioMetadata, error) {
		return metadata.AudioMetadata{Title: "Song", Album: "Album", DiscNumber: 1, Artists: []string{"Singer"}, AlbumArtists: []string{"VA"}, Composer: composer}, nil
	}
	manager.probeAudio = func(string, string) metadata.AudioProps { return metadata.AudioProps{} }
	scan := func(convert bool, want int) {
		t.Helper()
		var job int64
		var e error
		if convert {
			job, e = manager.StartLegacyCreatorConversion(ctx)
		} else {
			job, e = manager.Start(ctx, "full")
		}
		if e != nil {
			t.Fatal(e)
		}
		manager.Wait()
		if s := waitJob(t, store, job); s.Status != "completed" {
			t.Fatal(s)
		}
		tracks, e := store.ListTracks(ctx, storage.Filters{Limit: 10})
		if e != nil || len(tracks) != 1 {
			t.Fatalf("%v %v", tracks, e)
		}
		for _, c := range tracks[0].Credits {
			if c.Role == "composer" {
				if len(c.Artists) != want {
					t.Fatalf("credits: %+v", c)
				}
				return
			}
		}
		t.Fatal("missing composer")
	}
	scan(false, 1)
	scan(true, 2)
	scan(false, 2)
	composer = "Different, Person"
	scan(false, 1)
}
