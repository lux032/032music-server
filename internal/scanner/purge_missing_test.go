package scanner

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// TestScanPurgeMissingPolicy covers the Navidrome-style Scanner.PurgeMissing
// behaviour: missing tracks are only hidden unless the policy asks for a
// purge after this kind of scan.
func TestScanPurgeMissingPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   string // "" = no policy source wired (defaults to never)
		scanType string
		purged   bool
	}{
		{"unset", "", "incremental", false},
		{"never", config.PurgeMissingNever, "full", false},
		{"always-incremental", config.PurgeMissingAlways, "incremental", true},
		{"full-incremental", config.PurgeMissingFull, "incremental", false},
		{"full-full", config.PurgeMissingFull, "full", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, _ := testStore(t)
			root := t.TempDir()
			if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
				t.Fatal(err)
			}
			library, err := store.LibraryByRoot(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			for _, rel := range []string{"Keep/01.flac", "Keep/02.flac", "Gone/01.flac"} {
				path := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("fLaC"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			m := New(ctx, store, logger(), library, t.TempDir())
			m.SetMetadataReader(func(path string) (metadata.AudioMetadata, error) {
				album := filepath.Base(filepath.Dir(path))
				artist := "Keeper"
				if album == "Gone" {
					artist = "Leaver"
				}
				return metadata.AudioMetadata{Title: strings.TrimSuffix(filepath.Base(path), ".flac"), Album: album, Artists: []string{artist}, AlbumArtists: []string{artist}, DiscNumber: 1, TrackNumber: 1, Container: "flac"}, nil
			})
			m.probeAudio = func(string, string) metadata.AudioProps { return metadata.AudioProps{} }
			if tc.policy != "" {
				m.SetPurgeMissingPolicy(func(context.Context) string { return tc.policy })
			}
			scan := func(scanType string) storage.ScanJob {
				t.Helper()
				jobID, err := m.Start(ctx, scanType)
				if err != nil {
					t.Fatal(err)
				}
				m.Wait()
				return waitJob(t, store, jobID)
			}
			if job := scan("incremental"); job.Status != "completed" {
				t.Fatalf("initial scan: %+v", job)
			}
			if err := os.Remove(filepath.Join(root, "Gone", "01.flac")); err != nil {
				t.Fatal(err)
			}
			missing, _, err := store.ListMissingFiles(ctx, 10, 0)
			if err != nil || len(missing) != 0 {
				t.Fatalf("missing before rescan = %+v err=%v", missing, err)
			}
			if job := scan(tc.scanType); job.Status != "completed" || job.MissingFiles != 1 {
				t.Fatalf("rescan: %+v", job)
			}

			// Either way the missing album is gone from every browse surface.
			albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 10})
			if err != nil || len(albums) != 1 || albums[0].Title != "Keep" || albums[0].TrackCount != 2 {
				t.Fatalf("visible albums = %+v err=%v", albums, err)
			}
			artists, err := store.ListArtists(ctx, storage.Filters{Limit: 10})
			if err != nil || len(artists) != 1 || artists[0].Name != "Keeper" {
				t.Fatalf("visible artists = %+v err=%v", artists, err)
			}

			missing, total, err := store.ListMissingFiles(ctx, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if tc.purged {
				if total != 0 || len(missing) != 0 {
					t.Fatalf("purge left missing files: %+v", missing)
				}
				var goneAlbum int64
				for _, id := range []int64{1, 2, 3} {
					if album, e := store.AlbumByID(ctx, id); e == nil && album.Title == "Gone" {
						goneAlbum = id
					}
				}
				if goneAlbum != 0 {
					t.Fatalf("purged album %d still exists", goneAlbum)
				}
				return
			}
			if total != 1 || len(missing) != 1 || missing[0].RelativePath != "Gone/01.flac" || !missing[0].TrackHidden {
				t.Fatalf("missing files = %+v total=%d", missing, total)
			}
			track, err := store.TrackByID(ctx, missing[0].TrackID)
			if err != nil {
				t.Fatalf("hidden track must be kept: %v", err)
			}
			if _, err := store.AlbumByID(ctx, track.AlbumID); errors.Is(err, sql.ErrNoRows) {
				t.Fatal("hidden album must be kept")
			}
		})
	}
}
