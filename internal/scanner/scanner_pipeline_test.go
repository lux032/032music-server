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

func TestScanRuleSkippedAccountingAndSkipsOrphanCleanup(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rule-skip.db")
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

	writeAlbum := func(folder string, tracks int) {
		dir := filepath.Join(root, folder)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= tracks; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.flac", i)), []byte("fLaC"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeAlbum("Keep", 2)
	writeAlbum("Blocked", 3)

	m := New(ctx, store, logger(), library, t.TempDir())
	m.workers = 2
	m.SetMetadataReader(func(path string) (metadata.AudioMetadata, error) {
		album := filepath.Base(filepath.Dir(path))
		return metadata.AudioMetadata{
			Title:          album + "-" + strings.TrimSuffix(filepath.Base(path), ".flac"),
			Album:          album,
			Year:           2026,
			Artists:        []string{"Singer"},
			AlbumArtists:   []string{"Singer"},
			DiscNumber:     1,
			TrackNumber:    1,
			DurationMillis: 180000,
			Container:      "flac",
		}, nil
	})
	m.probeAudio = func(string, string) metadata.AudioProps {
		return metadata.AudioProps{Codec: "flac", SampleRate: 44100, BitDepth: 16, Channels: 2}
	}

	scan := func(scanType string) storage.ScanJob {
		t.Helper()
		id, e := m.Start(ctx, scanType)
		if e != nil {
			t.Fatal(e)
		}
		m.Wait()
		return waitJob(t, store, id)
	}

	if job := scan("incremental"); job.Status != "completed" || job.ProcessedFiles != 5 || job.RuleSkippedFiles != 0 {
		t.Fatalf("initial scan = %+v", job)
	}

	// Delete Blocked in library while leaving its 3 files on disk.
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var blockedID int64
	for _, a := range albums {
		if a.Title == "Blocked" {
			blockedID = a.ID
		}
	}
	if blockedID == 0 {
		t.Fatal("Blocked album not found")
	}
	if _, err := store.DeleteAlbums(ctx, []int64{blockedID}); err != nil {
		t.Fatal(err)
	}

	// Plant a sentinel orphan artist to prove CleanupOrphans is skipped on zero real changes.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rawDB.Close() })
	if _, err := rawDB.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('SentinelRuleSkip','sentinelruleskip','sentinelruleskip')`); err != nil {
		t.Fatal(err)
	}

	job := scan("incremental")
	if job.Status != "completed" || job.DiscoveredFiles != 5 || job.ProcessedFiles != 0 || job.SkippedFiles != 5 || job.RuleSkippedFiles != 3 || job.FailedFiles != 0 {
		t.Fatalf("rule-skipped incremental scan = %+v, want discovered=5 processed=0 skipped=5 ruleSkipped=3 failed=0", job)
	}
	var sentinelExists bool
	if err := rawDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE identity_key='sentinelruleskip')`).Scan(&sentinelExists); err != nil || !sentinelExists {
		t.Fatalf("CleanupOrphans unexpectedly ran when only rule-skipped files were seen: exists=%v err=%v", sentinelExists, err)
	}

	// Full scan re-imports Keep (2) and counts Blocked (3) in skipped & ruleSkipped.
	fullJob := scan("full")
	if fullJob.Status != "completed" || fullJob.DiscoveredFiles != 5 || fullJob.ProcessedFiles != 2 || fullJob.SkippedFiles != 3 || fullJob.RuleSkippedFiles != 3 || fullJob.FailedFiles != 0 {
		t.Fatalf("full scan with delete rule = %+v, want processed=2 skipped=3 ruleSkipped=3", fullJob)
	}
}

func TestScanPrunesStaleDeleteRules(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "prune-rules.db")
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

	writeTrack := func(rel string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("fLaC"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeTrack("Keep/01.flac")
	writeTrack("Keep/02.flac")
	writeTrack("Keep/03.flac")
	writeTrack("MergedSrc/01.flac")
	writeTrack("ナナヲアカリ/ぴ/01.flac")
	writeTrack("StillHere/CD1/01.flac")
	writeTrack("Locked/01.flac")

	m := New(ctx, store, logger(), library, t.TempDir())
	m.workers = 2
	m.SetMetadataReader(func(path string) (metadata.AudioMetadata, error) {
		rel, _ := filepath.Rel(root, path)
		folder := storage.AlbumFolder(filepath.ToSlash(rel))
		album := filepath.Base(folder)
		return metadata.AudioMetadata{
			Title:          album + "-" + strings.TrimSuffix(filepath.Base(path), ".flac"),
			Album:          album,
			Year:           2026,
			Artists:        []string{"Singer"},
			AlbumArtists:   []string{"Singer"},
			DiscNumber:     1,
			TrackNumber:    1,
			DurationMillis: 180000,
			Container:      "flac",
		}, nil
	})
	m.probeAudio = func(string, string) metadata.AudioProps {
		return metadata.AudioProps{Codec: "flac"}
	}

	scan := func(mgr *Manager, scanType string) storage.ScanJob {
		t.Helper()
		id, e := mgr.Start(ctx, scanType)
		if e != nil {
			t.Fatal(e)
		}
		mgr.Wait()
		return waitJob(t, store, id)
	}

	if job := scan(m, "incremental"); job.Status != "completed" || job.ProcessedFiles != 7 {
		t.Fatalf("seed scan = %+v", job)
	}

	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]int64{}
	for _, a := range albums {
		byTitle[a.Title] = a.ID
	}
	if _, err := store.MergeAlbums(ctx, byTitle["Keep"], []int64{byTitle["MergedSrc"]}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteAlbums(ctx, []int64{byTitle["ぴ"], byTitle["StillHere"], byTitle["Locked"]}); err != nil {
		t.Fatal(err)
	}

	// Remove ナナヲアカリ/ぴ from disk (simulating moving it away) and make Locked unreadable.
	if err := os.RemoveAll(filepath.Join(root, "ナナヲアカリ", "ぴ")); err != nil {
		t.Fatal(err)
	}
	lockedDir := filepath.Join(root, "Locked")
	if os.Geteuid() != 0 {
		if err := os.Chmod(lockedDir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o750) })
	}

	// 1. Mid-flight cancelled scan must never prune stale delete rules.
	cancelCtx, cancel := context.WithCancel(context.Background())
	mCancelled := New(cancelCtx, store, logger(), library, t.TempDir())
	mCancelled.workers = 1
	mCancelled.SetMetadataReader(func(path string) (metadata.AudioMetadata, error) {
		cancel()
		return m.readMetadata(path)
	})
	mCancelled.probeAudio = m.probeAudio
	if cJob := scan(mCancelled, "full"); cJob.Status != "failed" {
		t.Fatalf("cancelled scan = %+v, want failed", cJob)
	}
	if delCount, mergeCount, err := store.CountAlbumKeyRules(ctx); err != nil || delCount != 3 || mergeCount != 1 {
		t.Fatalf("rules after cancelled scan = (%d, %d, %v), want (3, 1, nil)", delCount, mergeCount, err)
	}

	// 2. Healthy incremental scan prunes only the emptied ナナヲアカリ/ぴ delete rule;
	// StillHere/CD1 (still on disk), Locked (unreadable), and MergedSrc (merge rule) survive.
	if job := scan(m, "incremental"); job.Status != "completed" || job.ProcessedFiles != 0 || job.RuleSkippedFiles != 1 {
		t.Fatalf("prune scan = %+v, want completed processed=0 ruleSkipped=1", job)
	}
	wantDel := int64(2)
	if os.Geteuid() == 0 {
		// Root ignores chmod 0000; Locked was readable and still had 01.flac, so still 2.
		wantDel = 2
	}
	if delCount, mergeCount, err := store.CountAlbumKeyRules(ctx); err != nil || delCount != wantDel || mergeCount != 1 {
		t.Fatalf("rules after healthy prune = (%d, %d, %v), want (%d, 1, nil)", delCount, mergeCount, err, wantDel)
	}

	// 3. Placing a brand-new album into ナナヲアカリ/ぴ now imports cleanly!
	writeTrack("ナナヲアカリ/ぴ/01.flac")
	writeTrack("ナナヲアカリ/ぴ/02.flac")
	if job := scan(m, "incremental"); job.Status != "completed" || job.ProcessedFiles != 2 || job.RuleSkippedFiles != 1 {
		t.Fatalf("re-import ぴ scan = %+v, want processed=2 ruleSkipped=1", job)
	}
	albumsAfter, err := store.ListAlbums(ctx, storage.Filters{Query: "ぴ", Limit: 10})
	if err != nil || len(albumsAfter) != 1 || albumsAfter[0].TrackCount != 2 {
		t.Fatalf("albums matching ぴ = %+v (err=%v), want 1 album with 2 tracks", albumsAfter, err)
	}

	// 4. S1 (>50% missing guard) refusal must never prune stale delete rules.
	if err := os.RemoveAll(filepath.Join(root, "StillHere")); err != nil {
		t.Fatal(err)
	}
	// Also temporarily remove Keep + ぴ (>50% of available tracks) to trip S1 guard.
	if err := os.RemoveAll(filepath.Join(root, "Keep")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "ナナヲアカリ")); err != nil {
		t.Fatal(err)
	}
	if s1Job := scan(m, "incremental"); s1Job.Status != "failed" {
		t.Fatalf("S1 guarded scan = %+v, want failed", s1Job)
	}
	// StillHere delete rule must still be intact because S1 guard refused reconciliation.
	if delCount, _, err := store.CountAlbumKeyRules(ctx); err != nil || delCount != wantDel {
		t.Fatalf("delete rules after S1 refusal = %d (err=%v), want %d", delCount, err, wantDel)
	}
}
