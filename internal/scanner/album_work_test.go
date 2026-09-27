package scanner

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshAlbumWorksUsesScanMutex(t *testing.T) {
	ctx := context.Background()
	s, e := storage.Open(filepath.Join(t.TempDir(), "scan.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	m := New(ctx, s, slog.New(slog.NewTextHandler(io.Discard, nil)), storage.Library{}, t.TempDir())
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	if _, e = m.RefreshAlbumWorks(ctx); !errors.Is(e, ErrScanRunning) {
		t.Fatalf("refresh during scan: %v", e)
	}
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
	if _, e = m.RefreshAlbumWorks(ctx); e != nil {
		t.Fatalf("refresh after scan: %v", e)
	}
}
func TestScanRefreshesAlbumWorks(t *testing.T) {
	ctx := context.Background()
	s, e := storage.Open(filepath.Join(t.TempDir(), "scan.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e = s.EnsureLibrary(ctx, "Test", root); e != nil {
		t.Fatal(e)
	}
	lib, e := s.LibraryByRoot(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	audio := filepath.Join(root, "track.flac")
	data := make([]byte, 42)
	copy(data, "fLaC")
	data[4] = 0x80
	data[7] = 34
	binary.BigEndian.PutUint64(data[18:26], uint64(44100)<<44|uint64(1)<<41|uint64(15)<<36|44100)
	if e = os.WriteFile(audio, data, 0600); e != nil {
		t.Fatal(e)
	}
	m := New(ctx, s, slog.New(slog.NewTextHandler(io.Discard, nil)), lib, t.TempDir())
	m.readMetadata = func(string) (metadata.AudioMetadata, error) {
		return metadata.AudioMetadata{Title: "Track", Album: "NieR:Automata Original Soundtrack", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}, nil
	}
	job, e := m.Start(ctx, "incremental")
	if e != nil {
		t.Fatal(e)
	}
	m.Wait()
	finished := waitJob(t, s, job)
	if finished.Status != "completed" {
		t.Fatalf("scan %+v", finished)
	}
	works, e := s.ListWorks(ctx, storage.WorkFilters{})
	if e != nil || len(works) != 1 || works[0].Title != "NieR:Automata" {
		t.Fatalf("post-scan works %+v %v", works, e)
	}
}
