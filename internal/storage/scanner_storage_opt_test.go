package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func openScannerOptTestStore(t *testing.T) (*Store, int64) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "scanner-opt.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLibrary(ctx, "Lib", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	return s, lib.ID
}

func TestScanSnapshotBatchLRCAndMarkMissingByIDs(t *testing.T) {
	ctx := context.Background()
	s, libID := openScannerOptTestStore(t)

	rg1 := "11111111-1111-4111-8111-111111111111"
	rg2 := "22222222-2222-4222-8222-222222222222"
	rg3 := "33333333-3333-4333-8333-333333333333"

	inputs := []ImportInput{
		{
			LibraryID: libID, RelativePath: "A/01.flac", FileSize: 100, ModifiedAtNS: 1000, HasExternalLRC: false,
			Metadata: metadata.AudioMetadata{Title: "A1", Album: "Album A", Artists: []string{"S"}, AlbumArtists: []string{"S"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 200000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rg1}}},
		},
		{
			LibraryID: libID, RelativePath: "A/02.flac", FileSize: 200, ModifiedAtNS: 2000, HasExternalLRC: true,
			Metadata: metadata.AudioMetadata{Title: "A2", Album: "Album A", Artists: []string{"S"}, AlbumArtists: []string{"S"}, DiscNumber: 1, TrackNumber: 2, DurationMillis: 210000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rg2}}},
		},
		{
			LibraryID: libID, RelativePath: "B/01.flac", FileSize: 300, ModifiedAtNS: 3000, HasExternalLRC: false,
			Metadata: metadata.AudioMetadata{Title: "B1", Album: "Album B", Artists: []string{"S"}, AlbumArtists: []string{"S"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 180000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rg3}}},
		},
	}
	errs, err := s.ImportTrackBatch(ctx, inputs)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range errs {
		if e != nil {
			t.Fatalf("input %d failed: %v", i, e)
		}
	}
	// Mark B/01.flac as legacy probe version and clear duration on A/02.flac to check snapshot flags.
	if _, err := s.db.ExecContext(ctx, `UPDATE audio_files SET audio_probe_version=0 WHERE relative_path='B/01.flac'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE tracks SET duration_ms=NULL WHERE title='A2'`); err != nil {
		t.Fatal(err)
	}

	snap, err := s.LoadScanFileSnapshot(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap) != 3 {
		t.Fatalf("snapshot size = %d, want 3", len(snap))
	}
	if a1 := snap["A/01.flac"]; a1.FileSize != 100 || a1.ModifiedAtNS != 1000 || a1.AudioProbeVersion != AudioProbeVersion || a1.HasExternalLRC || a1.Status != "available" || !a1.HasDuration {
		t.Fatalf("snap A/01 = %+v", a1)
	}
	if a2 := snap["A/02.flac"]; !a2.HasExternalLRC || a2.HasDuration {
		t.Fatalf("snap A/02 = %+v, want HasExternalLRC=true HasDuration=false", a2)
	}
	if b1 := snap["B/01.flac"]; b1.AudioProbeVersion != 0 || !b1.HasDuration {
		t.Fatalf("snap B/01 = %+v, want AudioProbeVersion=0 HasDuration=true", b1)
	}

	// BatchUpdateExternalLRC toggles both enable and disable sets accurately.
	if err := s.BatchUpdateExternalLRC(ctx, []ExternalLRCUpdate{
		{AudioFileID: snap["A/01.flac"].AudioFileID, HasExternalLRC: true},
		{AudioFileID: snap["A/02.flac"].AudioFileID, HasExternalLRC: false},
	}); err != nil {
		t.Fatal(err)
	}
	snap2, err := s.LoadScanFileSnapshot(ctx, libID)
	if err != nil {
		t.Fatal(err)
	}
	if !snap2["A/01.flac"].HasExternalLRC || snap2["A/02.flac"].HasExternalLRC {
		t.Fatalf("after BatchUpdateExternalLRC: A1=%v A2=%v", snap2["A/01.flac"].HasExternalLRC, snap2["A/02.flac"].HasExternalLRC)
	}

	// Empty MarkMissingByIDs must not acquire the write lock.
	holdTx, err := s.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	n, err := s.MarkMissingByIDs(shortCtx, libID, nil)
	cancel()
	holdTx.Rollback()
	if err != nil || n != 0 {
		t.Fatalf("empty MarkMissingByIDs while write lock held = (%d, %v), want (0, nil)", n, err)
	}

	// Install trigger guarding Album B's release groups: marking A/02 missing must only touch Album A.
	var albumAID, albumBID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title='Album A'`).Scan(&albumAID); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title='Album B'`).Scan(&albumBID); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		fmt.Sprintf(`CREATE TRIGGER guard_rg_b_del BEFORE DELETE ON album_release_groups WHEN OLD.album_id=%d BEGIN SELECT RAISE(ABORT, 'untouched album B deleted'); END`, albumBID),
		fmt.Sprintf(`CREATE TRIGGER guard_rg_b_upd BEFORE UPDATE ON album_release_groups WHEN OLD.album_id=%d BEGIN SELECT RAISE(ABORT, 'untouched album B updated'); END`, albumBID),
	} {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE release_group_rebuild SET status='ready' WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	marked, err := s.MarkMissingByIDs(ctx, libID, []int64{snap["A/02.flac"].AudioFileID})
	if err != nil || marked != 1 {
		t.Fatalf("MarkMissingByIDs = (%d, %v), want (1, nil)", marked, err)
	}
	gotRG, err := s.AlbumReleaseGroup(ctx, albumAID)
	if err != nil || gotRG != rg1 {
		t.Fatalf("Album A release group after A/02 missing = %q (%v), want %q", gotRG, err, rg1)
	}
}

func TestImportTrackBatchSavepointCacheAndConcurrency(t *testing.T) {
	ctx := context.Background()
	s, libID := openScannerOptTestStore(t)

	// Reject any track titled POISON so we verify savepoint isolation and artist cache rollback safety.
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_poison_track BEFORE INSERT ON tracks WHEN NEW.title='POISON' BEGIN SELECT RAISE(ABORT, 'poison track'); END`); err != nil {
		t.Fatal(err)
	}

	rgA := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	rgB := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	batch := []ImportInput{
		{
			LibraryID: libID, RelativePath: "Alb/01.flac", FileSize: 10, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: "Good 1", Album: "Alb", Artists: []string{"SharedArtist"}, AlbumArtists: []string{"SharedArtist"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 120000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rgA}}},
		},
		{
			// Uses a brand-new artist name ("RollbackOnlyArtist") that will be created inside the
			// savepoint before tracks INSERT aborts. The batch artist cache must NOT retain its rolled-back ID.
			LibraryID: libID, RelativePath: "Alb/02.flac", FileSize: 10, ModifiedAtNS: 2,
			Metadata: metadata.AudioMetadata{Title: "POISON", Album: "Alb", Artists: []string{"RollbackOnlyArtist"}, AlbumArtists: []string{"RollbackOnlyArtist"}, DiscNumber: 1, TrackNumber: 2, DurationMillis: 120000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rgB}}},
		},
		{
			// Uses both SharedArtist (cache hit) and RollbackOnlyArtist (must be freshly inserted after rollback).
			LibraryID: libID, RelativePath: "Alb/03.flac", FileSize: 10, ModifiedAtNS: 3,
			Metadata: metadata.AudioMetadata{Title: "Good 3", Album: "Alb", Artists: []string{"SharedArtist", "RollbackOnlyArtist"}, AlbumArtists: []string{"SharedArtist"}, DiscNumber: 1, TrackNumber: 3, DurationMillis: 120000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rgA}}},
		},
	}

	errs, err := s.ImportTrackBatch(ctx, batch)
	if err != nil {
		t.Fatalf("ImportTrackBatch top-level error: %v", err)
	}
	if errs[0] != nil || errs[1] == nil || errs[2] != nil {
		t.Fatalf("unexpected per-item errors: %v", errs)
	}

	tracks, err := s.ListTracks(ctx, Filters{Limit: 10})
	if err != nil || len(tracks) != 2 {
		t.Fatalf("tracks = %d (%v), want 2", len(tracks), err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE release_group_rebuild SET status='ready' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var albID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title='Alb'`).Scan(&albID); err != nil {
		t.Fatal(err)
	}
	// Poison track's rgB must have been rolled back; Album should resolve cleanly to rgA.
	if gotRG, err := s.AlbumReleaseGroup(ctx, albID); err != nil || gotRG != rgA {
		t.Fatalf("album RG = %q (%v), want %q", gotRG, err, rgA)
	}

	// Moving Alb/03.flac to a new album inside a batch refreshes both the old and new albums' release groups.
	moveBatch := []ImportInput{
		{
			LibraryID: libID, RelativePath: "Alb/03.flac", FileSize: 11, ModifiedAtNS: 4,
			Metadata: metadata.AudioMetadata{Title: "Good 3", Album: "MovedAlb", Artists: []string{"SharedArtist"}, AlbumArtists: []string{"SharedArtist"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 120000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rgB}}},
		},
	}
	if errs, err = s.ImportTrackBatch(ctx, moveBatch); err != nil || errs[0] != nil {
		t.Fatalf("move batch: %v %v", errs, err)
	}
	var movedID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title='MovedAlb'`).Scan(&movedID); err != nil {
		t.Fatal(err)
	}
	if gotRG, err := s.AlbumReleaseGroup(ctx, movedID); err != nil || gotRG != rgB {
		t.Fatalf("moved album RG = %q (%v), want %q", gotRG, err, rgB)
	}

	// Concurrent ImportTrackBatch + playback events must serialize cleanly without SQLITE_BUSY_SNAPSHOT.
	var wg sync.WaitGroup
	errCh := make(chan error, 4)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for b := 0; b < 5; b++ {
			var chunk []ImportInput
			for j := 0; j < 10; j++ {
				n := b*10 + j
				chunk = append(chunk, ImportInput{
					LibraryID: libID, RelativePath: fmt.Sprintf("Conc/%03d.flac", n), FileSize: 100, ModifiedAtNS: int64(n + 1),
					Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Conc %d", n), Album: fmt.Sprintf("ConcAlb %d", b), Artists: []string{"ConcArtist"}, AlbumArtists: []string{"ConcArtist"}, DiscNumber: 1, TrackNumber: j + 1, DurationMillis: 150000},
				})
			}
			resErrs, e := s.ImportTrackBatch(ctx, chunk)
			if e != nil {
				errCh <- fmt.Errorf("batch %d: %w", b, e)
				return
			}
			for _, re := range resErrs {
				if re != nil {
					errCh <- fmt.Errorf("batch %d item: %w", b, re)
					return
				}
			}
		}
	}()
	go func() {
		defer wg.Done()
		for seq := int64(1); seq <= 15; seq++ {
			evType := "heartbeat"
			if seq == 1 {
				evType = "start"
			}
			_, e := s.RecordPlaybackEvent(ctx, PlaybackEventInput{
				ClientID: "c1", ClientKind: "web", SessionID: "concurrent-batch-sess",
				Seq: seq, Type: evType, State: "playing", TrackID: tracks[0].ID,
				PositionMillis: seq * 1000, DurationMillis: 120000,
			})
			if e != nil {
				errCh <- fmt.Errorf("playback seq %d: %w", seq, e)
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
}

func TestMarkMissingAndPurgeMissingScopedReleaseGroups(t *testing.T) {
	ctx := context.Background()
	s, libID := openScannerOptTestStore(t)
	rg1 := "11111111-1111-4111-8111-111111111111"
	rg2 := "22222222-2222-4222-8222-222222222222"
	rg3 := "33333333-3333-4333-8333-333333333333"
	if errs, err := s.ImportTrackBatch(ctx, []ImportInput{
		{LibraryID: libID, RelativePath: "A/1.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "A1", Album: "Album A", Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 1000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rg1}}}},
		{LibraryID: libID, RelativePath: "A/2.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "A2", Album: "Album A", Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 2, DurationMillis: 1000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rg2}}}},
		{LibraryID: libID, RelativePath: "B/1.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "B1", Album: "Album B", Artists: []string{"Y"}, AlbumArtists: []string{"Y"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 1000, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": {rg3}}}},
	}); err != nil || errs[0] != nil || errs[1] != nil || errs[2] != nil {
		t.Fatalf("seed: %v %v", errs, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE release_group_rebuild SET status='ready' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var albumA, albumB int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title='Album A'`).Scan(&albumA); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title='Album B'`).Scan(&albumB); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		fmt.Sprintf(`CREATE TRIGGER guard_b_del BEFORE DELETE ON album_release_groups WHEN OLD.album_id=%d BEGIN SELECT RAISE(ABORT, 'album B RG deleted'); END`, albumB),
		fmt.Sprintf(`CREATE TRIGGER guard_b_upd BEFORE UPDATE ON album_release_groups WHEN OLD.album_id=%d BEGIN SELECT RAISE(ABORT, 'album B RG updated'); END`, albumB),
	} {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	// Zero-missing MarkMissing must skip write lock and never touch Album B.
	holdTx, err := s.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	n, err := s.MarkMissing(shortCtx, libID, "1970-01-01T00:00:00.000Z")
	cancel()
	holdTx.Rollback()
	if err != nil || n != 0 {
		t.Fatalf("idle MarkMissing = (%d, %v), want (0, nil)", n, err)
	}

	// Mark only A/2.flac stale; MarkMissing refreshes Album A without touching Album B.
	if _, err := s.db.ExecContext(ctx, `UPDATE audio_files SET last_scanned_at='2020-01-01T00:00:00.000Z' WHERE relative_path='A/2.flac'`); err != nil {
		t.Fatal(err)
	}
	n, err = s.MarkMissing(ctx, libID, "2025-01-01T00:00:00.000Z")
	if err != nil || n != 1 {
		t.Fatalf("MarkMissing A/2 = (%d, %v), want (1, nil)", n, err)
	}
	if got, err := s.AlbumReleaseGroup(ctx, albumA); err != nil || got != rg1 {
		t.Fatalf("Album A RG = %q (%v), want %q", got, err, rg1)
	}

	// PurgeMissing deletes A/2.flac and refreshes only Album A without touching Album B.
	res, err := s.PurgeMissing(ctx, libID, nil)
	if err != nil || res.Files != 1 || res.Tracks != 1 {
		t.Fatalf("PurgeMissing = (%+v, %v)", res, err)
	}
	if got, err := s.AlbumReleaseGroup(ctx, albumA); err != nil || got != rg1 {
		t.Fatalf("Album A RG after purge = %q (%v), want %q", got, err, rg1)
	}
}

func TestRefreshAlbumWorksAndCleanupOrphansPlans(t *testing.T) {
	ctx := context.Background()
	s, libID := openScannerOptTestStore(t)

	// 1. Verify RefreshAlbumWorks list query uses materialized CTEs with zero correlated scalar subqueries.
	rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN `+refreshAlbumWorksListSQL)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	rows.Close()
	plan := strings.Join(details, "\n")
	if strings.Contains(plan, "CORRELATED") {
		t.Fatalf("refreshAlbumWorksListSQL still contains correlated subquery:\n%s", plan)
	}
	for _, cte := range []string{"MATERIALIZE first_audio", "MATERIALIZE va_albums", "MATERIALIZE comp_albums"} {
		if !strings.Contains(plan, cte) {
			t.Fatalf("refreshAlbumWorksListSQL missing %q:\n%s", cte, plan)
		}
	}

	// 2. Verify CleanupOrphans preserves artists referenced in primary-artist correction JSON while removing unreferenced orphans.
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: libID, RelativePath: "Keep/01.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "Keep Song", Album: "Keep Album", Artists: []string{"Active"}, AlbumArtists: []string{"Active"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 1000},
	}); err != nil {
		t.Fatal(err)
	}
	var trackID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM tracks LIMIT 1`).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	var protectedID, orphanID int64
	res, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('ProtectedJSON','protectedjson','protectedjson')`)
	if err != nil {
		t.Fatal(err)
	}
	protectedID, _ = res.LastInsertId()
	res, err = s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('TrueOrphan','trueorphan','trueorphan')`)
	if err != nil {
		t.Fatal(err)
	}
	orphanID, _ = res.LastInsertId()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_primary_artist_state(track_id,artists_json,active) VALUES(?,?,1)`, trackID, fmt.Sprintf("[%d]", protectedID)); err != nil {
		t.Fatal(err)
	}

	if err := s.CleanupOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	var protectedExists, orphanExists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=?)`, protectedID).Scan(&protectedExists); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=?)`, orphanID).Scan(&orphanExists); err != nil {
		t.Fatal(err)
	}
	if !protectedExists {
		t.Fatal("CleanupOrphans deleted artist referenced in track_primary_artist_state.artists_json")
	}
	if orphanExists {
		t.Fatal("CleanupOrphans failed to delete unreferenced orphan artist")
	}
}

func TestScanJobRuleSkippedProgressAndMigrationIdempotency(t *testing.T) {
	ctx := context.Background()
	s, libID := openScannerOptTestStore(t)
	// Re-running Migrate must remain idempotent with Migration 089 applied.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate failed: %v", err)
	}
	jobID, err := s.CreateScanJob(ctx, libID, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartScanJob(ctx, jobID, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateScanJobProgress(ctx, jobID, 10, 88, 12, 2, "Artist/Album/01.flac"); err != nil {
		t.Fatal(err)
	}
	job, err := s.LatestScanJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job.ProcessedFiles != 10 || job.SkippedFiles != 88 || job.RuleSkippedFiles != 12 || job.FailedFiles != 2 || job.CurrentRelativePath != "Artist/Album/01.flac" {
		t.Fatalf("LatestScanJob after UpdateScanJobProgress = %+v", job)
	}
	if err := s.UpdateScanJob(ctx, jobID, 20, 80, 0, ""); err != nil {
		t.Fatal(err)
	}
	job, err = s.LatestScanJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job.ProcessedFiles != 20 || job.SkippedFiles != 80 || job.RuleSkippedFiles != 0 || job.FailedFiles != 0 {
		t.Fatalf("LatestScanJob after backward-compatible UpdateScanJob = %+v", job)
	}
}

func TestImportTrackBatchDetailedRuleSkippedAndSavepoint(t *testing.T) {
	ctx := context.Background()
	s, libID := openScannerOptTestStore(t)

	// Seed and delete one album so its grouping_key receives an active delete rule.
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: libID, RelativePath: "ナナヲアカリ/ぴ/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "ぴ", Album: "ぴ", Year: 2026, Artists: []string{"ナナヲアカリ"}, AlbumArtists: []string{"ナナヲアカリ"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 200000},
	}); err != nil {
		t.Fatal(err)
	}
	var deletedID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE title='ぴ'`).Scan(&deletedID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteAlbums(ctx, []int64{deletedID}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_bad_track BEFORE INSERT ON tracks WHEN NEW.title='BAD_TRACK' BEGIN SELECT RAISE(ABORT, 'bad track'); END`); err != nil {
		t.Fatal(err)
	}

	results, err := s.ImportTrackBatchDetailed(ctx, []ImportInput{
		{
			LibraryID: libID, RelativePath: "ナナヲアカリ/ぴ/02.flac", FileSize: 200, ModifiedAtNS: 2,
			Metadata: metadata.AudioMetadata{Title: "だらだらり", Album: "ぴ", Year: 2026, Artists: []string{"ナナヲアカリ"}, AlbumArtists: []string{"ナナヲアカリ"}, DiscNumber: 1, TrackNumber: 2, DurationMillis: 180000},
		},
		{
			LibraryID: libID, RelativePath: "Good/01.flac", FileSize: 150, ModifiedAtNS: 2,
			Metadata: metadata.AudioMetadata{Title: "BAD_TRACK", Album: "Good", Year: 2026, Artists: []string{"GoodArtist"}, AlbumArtists: []string{"GoodArtist"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 180000},
		},
		{
			LibraryID: libID, RelativePath: "Good/02.flac", FileSize: 160, ModifiedAtNS: 2,
			Metadata: metadata.AudioMetadata{Title: "OK Song", Album: "Good", Year: 2026, Artists: []string{"GoodArtist"}, AlbumArtists: []string{"GoodArtist"}, DiscNumber: 1, TrackNumber: 2, DurationMillis: 190000},
		},
	})
	if err != nil {
		t.Fatalf("ImportTrackBatchDetailed error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want 3", len(results))
	}
	if !results[0].RuleSkipped || results[0].Err != nil {
		t.Fatalf("results[0] = %+v, want RuleSkipped=true Err=nil", results[0])
	}
	if results[1].RuleSkipped || results[1].Err == nil {
		t.Fatalf("results[1] = %+v, want RuleSkipped=false Err!=nil", results[1])
	}
	if results[2].RuleSkipped || results[2].Err != nil {
		t.Fatalf("results[2] = %+v, want RuleSkipped=false Err=nil", results[2])
	}

	var trackCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks`).Scan(&trackCount); err != nil || trackCount != 1 {
		t.Fatalf("tracks count = %d (err=%v), want 1 (only OK Song)", trackCount, err)
	}
}
