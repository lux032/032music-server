package scanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestScanIncrementalZeroChangeSkipsAudioFileWritesAndOrphanCleanup(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "zero-change.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	for a := 1; a <= 4; a++ {
		dir := filepath.Join(root, fmt.Sprintf("Album%02d", a))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for n := 1; n <= 5; n++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.flac", n)), []byte("fLaC"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	m := New(ctx, store, logger(), library, t.TempDir())
	m.workers = 4
	var reads atomic.Int32
	m.SetMetadataReader(func(path string) (metadata.AudioMetadata, error) {
		reads.Add(1)
		album := filepath.Base(filepath.Dir(path))
		title := strings.TrimSuffix(filepath.Base(path), ".flac")
		return metadata.AudioMetadata{
			Title:          album + "-" + title,
			Album:          album,
			Artists:        []string{"Artist " + album},
			AlbumArtists:   []string{"Artist " + album},
			DiscNumber:     1,
			TrackNumber:    1,
			DurationMillis: 180000,
			Container:      "flac",
		}, nil
	})
	m.probeAudio = func(string, string) metadata.AudioProps {
		return metadata.AudioProps{Codec: "flac", SampleRate: 44100, BitDepth: 16, Channels: 2}
	}

	jobID, err := m.Start(ctx, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	job := waitJob(t, store, jobID)
	if job.Status != "completed" || job.ProcessedFiles != 20 || job.SkippedFiles != 0 || job.FailedFiles != 0 {
		t.Fatalf("initial scan: %+v", job)
	}

	// Install triggers that abort if any write hits audio_files or orphan artist cleanup runs
	// on a completely unchanged incremental scan.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rawDB.Close() })
	if _, err := rawDB.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('SentinelOrphan','sentinelorphan','sentinelorphan')`); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TRIGGER forbid_audio_files_update BEFORE UPDATE ON audio_files BEGIN SELECT RAISE(ABORT, 'unexpected audio_files update on zero-change scan'); END`,
		`CREATE TRIGGER forbid_audio_files_insert BEFORE INSERT ON audio_files BEGIN SELECT RAISE(ABORT, 'unexpected audio_files insert on zero-change scan'); END`,
	} {
		if _, err := rawDB.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	reads.Store(0)
	jobID2, err := m.Start(ctx, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	job2 := waitJob(t, store, jobID2)
	if job2.Status != "completed" || job2.ProcessedFiles != 0 || job2.SkippedFiles != 20 || job2.FailedFiles != 0 || job2.MissingFiles != 0 {
		t.Fatalf("zero-change incremental scan: %+v", job2)
	}
	if reads.Load() != 0 {
		t.Fatalf("zero-change scan read metadata %d times", reads.Load())
	}
	// Sentinel orphan artist remains because CleanupOrphans was skipped on zero-change scan.
	var sentinelExists bool
	if err := rawDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE identity_key='sentinelorphan')`).Scan(&sentinelExists); err != nil || !sentinelExists {
		t.Fatalf("CleanupOrphans unexpectedly ran on zero-change scan: exists=%v err=%v", sentinelExists, err)
	}
}

func TestScanConcurrentPipelineArtworkDedupErrorIsolationAndCancel(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pipeline.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	// Create 120 tracks across 10 albums; half share folder cover.jpg, half share identical embedded artwork.
	// Include one file that fails metadata reading and one file that triggers a DB constraint failure.
	for a := 1; a <= 10; a++ {
		dir := filepath.Join(root, fmt.Sprintf("Alb%02d", a))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if a <= 5 {
			if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte(fmt.Sprintf("folder-cover-%d", a)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for n := 1; n <= 12; n++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.flac", n)), []byte("fLaC"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rawDB.Close() })
	if _, err := rawDB.ExecContext(ctx, `CREATE TRIGGER reject_db_poison BEFORE INSERT ON tracks WHEN NEW.title='DB_POISON' BEGIN SELECT RAISE(ABORT, 'injected db poison'); END`); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	m := New(ctx, store, logger(), library, dataDir)
	m.workers = 4
	sharedEmbedded := []byte("identical-embedded-artwork-payload")
	m.SetMetadataReader(func(path string) (metadata.AudioMetadata, error) {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "Alb01/02.flac" {
			return metadata.AudioMetadata{}, errors.New("corrupt tags")
		}
		album := filepath.Base(filepath.Dir(path))
		title := album + "-" + strings.TrimSuffix(filepath.Base(path), ".flac")
		if rel == "Alb01/03.flac" {
			title = "DB_POISON"
		}
		meta := metadata.AudioMetadata{
			Title:          title,
			Album:          album,
			Artists:        []string{"Shared Singer", "Artist " + album},
			AlbumArtists:   []string{"Artist " + album},
			DiscNumber:     1,
			TrackNumber:    1,
			DurationMillis: 200000,
			Container:      "flac",
		}
		if strings.Compare(album, "Alb05") > 0 {
			meta.Artwork = sharedEmbedded
			meta.ArtworkMIME = "image/jpeg"
		}
		return meta, nil
	})
	m.probeAudio = func(string, string) metadata.AudioProps {
		return metadata.AudioProps{Codec: "flac", SampleRate: 48000, BitDepth: 24, Channels: 2}
	}

	jobID, err := m.Start(ctx, "full")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	job := waitJob(t, store, jobID)
	if job.Status != "completed" || job.DiscoveredFiles != 120 || job.ProcessedFiles != 118 || job.FailedFiles != 2 || job.SkippedFiles != 0 {
		t.Fatalf("concurrent scan job = %+v, want discovered=120 processed=118 failed=2", job)
	}
	// Verify artwork cache deduplicated the 60 shared-embedded tracks into 1 file + 5 folder covers = 6 files.
	entries, err := os.ReadDir(filepath.Join(dataDir, "artwork"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 6 {
		t.Fatalf("artwork cache files = %d, want 6 (5 folder + 1 deduped embedded)", len(entries))
	}

	// Verify cancellation mid-scan does not leak goroutines and never marks existing tracks missing.
	cancelCtx, cancel := context.WithCancel(context.Background())
	m2 := New(cancelCtx, store, logger(), library, dataDir)
	m2.workers = 4
	var seen atomic.Int32
	m2.SetMetadataReader(func(path string) (metadata.AudioMetadata, error) {
		if seen.Add(1) == 3 {
			cancel()
		}
		time.Sleep(5 * time.Millisecond)
		return metadata.AudioMetadata{Title: filepath.Base(path), Album: "X", Artists: []string{"S"}, AlbumArtists: []string{"S"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 1000, Container: "flac"}, nil
	})
	m2.probeAudio = func(string, string) metadata.AudioProps { return metadata.AudioProps{} }
	cancelJobID, err := m2.Start(context.Background(), "full")
	if err != nil {
		t.Fatal(err)
	}
	m2.Wait()
	cancelJob := waitJob(t, store, cancelJobID)
	if cancelJob.Status != "failed" {
		t.Fatalf("cancelled scan status = %q, want failed", cancelJob.Status)
	}
	avail, err := store.CountAvailableAudioFiles(context.Background(), library.ID)
	if err != nil || avail != 118 {
		t.Fatalf("available after cancel = %d (%v), want 118", avail, err)
	}
}
