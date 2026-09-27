package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestMigration023PreservesArtistRunsAndAllowsCancelled(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := store.CreateArtistMatchRun(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateArtistMatchRun(ctx, id, 1, 1, 0, 0, "Artist"); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	runs, err := store.ListArtistMatchRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != id || runs[0].Processed != 1 || runs[0].Status != "running" {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	if err = store.FinishArtistMatchRun(ctx, id, "cancelled", "已手动停止"); err != nil {
		t.Fatal(err)
	}
	runs, err = store.ListArtistMatchRuns(ctx, 10)
	if err != nil || runs[0].Status != "cancelled" {
		t.Fatalf("cancelled runs=%v err=%v", runs, err)
	}
}

func TestAutoConfirmRejectsConcurrentManualConfirmation(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	work, err := store.CreateWork(ctx, WorkInput{Title: "Test", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	candidates := []WorkMatchCandidate{{Source: "bangumi", ExternalID: "1", Title: "Test", Type: "anime"}, {Source: "bangumi", ExternalID: "2", Title: "Test", Type: "anime"}}
	if err = store.ReplaceWorkMatchCandidates(ctx, work.ID, candidates); err != nil {
		t.Fatal(err)
	}
	saved, _ := store.WorkMatchCandidates(ctx, work.ID)
	if err = store.ConfirmWorkMatchCandidate(ctx, work.ID, saved[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	if err = store.AutoConfirmWorkMatchCandidate(ctx, work.ID, saved[1].ID, 0); !errors.Is(err, ErrAutoConfirmConflict) {
		t.Fatalf("err=%v", err)
	}
	saved, _ = store.WorkMatchCandidates(ctx, work.ID)
	if saved[0].Status != "confirmed" {
		t.Fatalf("confirmation changed: %+v", saved)
	}
}

func attachWorkForEnrichmentTest(t *testing.T, s *Store, ctx context.Context, id int64) {
	t.Helper()
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	var lib, album int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM libraries LIMIT 1`).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM albums LIMIT 1`).Scan(&album); errors.Is(err, sql.ErrNoRows) {
		r, e := s.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,'Test','Test','test')`, lib)
		if e != nil {
			t.Fatal(e)
		}
		album, _ = r.LastInsertId()
	} else if err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorkAlbum(ctx, id, album, "other"); err != nil {
		t.Fatal(err)
	}
}

func TestWorkTitleEditClearsPendingAndMiss(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	work, err := store.CreateWork(ctx, WorkInput{Title: "Old", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	attachWorkForEnrichmentTest(t, store, ctx, work.ID)
	if err = store.SetWorkEnrichmentMiss(ctx, work.ID, "bangumi"); err != nil {
		t.Fatal(err)
	}
	if err = store.ReplaceWorkMatchCandidates(ctx, work.ID, []WorkMatchCandidate{{Source: "bangumi", ExternalID: "1", Title: "Old"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpdateWork(ctx, work.ID, WorkInput{Title: "New", Type: "anime"}); err != nil {
		t.Fatal(err)
	}
	matches, _ := store.WorkMatchCandidates(ctx, work.ID)
	targets, err := store.WorksForEnrichment(ctx, "bangumi", false, 0, work.ID)
	if err != nil || len(matches) != 0 || len(targets) != 1 {
		t.Fatalf("matches=%v targets=%v err=%v", matches, targets, err)
	}
	var checked string
	err = store.db.QueryRowContext(ctx, `SELECT checked_at FROM work_enrichment_misses WHERE work_id=?`, work.ID).Scan(&checked)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("miss retained: %q %v", checked, err)
	}
}

func TestMigration022RetiresVGMdbSetting(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	setting, err := store.MetadataSourceSetting(ctx, "vgmdb")
	if err != nil || setting.Enabled || setting.AutoMatch {
		t.Fatalf("vgmdb setting=%+v err=%v", setting, err)
	}
}

func TestFailRunningArtistMatchRuns(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	running, err := store.CreateArtistMatchRun(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := store.CreateArtistMatchRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE artist_match_runs SET status='queued',current_artist='Previous' WHERE id=?`, queued); err != nil {
		t.Fatal(err)
	}
	completed, err := store.CreateArtistMatchRun(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.FinishArtistMatchRun(ctx, completed, "completed", ""); err != nil {
		t.Fatal(err)
	}
	count, err := store.FailRunningArtistMatchRuns(ctx, "restarted")
	if err != nil || count != 2 {
		t.Fatalf("recovered=%d err=%v", count, err)
	}
	rows, err := store.ListArtistMatchRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == completed {
			if row.Status != "completed" {
				t.Fatalf("completed modified: %+v", row)
			}
			continue
		}
		if row.ID != running && row.ID != queued || row.Status != "failed" || row.Current != "" || row.ErrorMessage != "restarted" {
			t.Fatalf("recovered run=%+v", row)
		}
	}
	count, err = store.FailRunningArtistMatchRuns(ctx, "again")
	if err != nil || count != 0 {
		t.Fatalf("second recovery=%d err=%v", count, err)
	}
}

func TestMigration024CreatesWorkEnrichmentRetriesOnExistingLibrary(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	work, err := store.CreateWork(ctx, WorkInput{Title: "Existing", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	// Model an existing v023 library and apply v024 through the migration runner.
	if _, err = store.db.ExecContext(ctx, `DROP TABLE work_enrichment_retries; DELETE FROM schema_migrations WHERE version=24`); err != nil {
		t.Fatal(err)
	}
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `INSERT INTO work_enrichment_retries(work_id,source,requested_at) VALUES(?,'bangumi','2024-01-01')`, work.ID); err != nil {
		t.Fatal(err)
	}
	var requested string
	if err = store.db.QueryRowContext(ctx, `SELECT requested_at FROM work_enrichment_retries WHERE work_id=?`, work.ID).Scan(&requested); err != nil || requested != "2024-01-01" {
		t.Fatalf("retry=%q err=%v", requested, err)
	}
}

func TestWorkTypeAndYearEditRetryRules(t *testing.T) {
	store, ctx := openEnrichmentTestStore(t)
	work, err := store.CreateWork(ctx, WorkInput{Title: "Title", Type: "anime", Year: 2023})
	if err != nil {
		t.Fatal(err)
	}
	attachWorkForEnrichmentTest(t, store, ctx, work.ID)
	if err = store.SetWorkEnrichmentMiss(ctx, work.ID, "bangumi"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpdateWork(ctx, work.ID, WorkInput{Title: "Title", Type: "anime", Year: 2024}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_enrichment_retries WHERE work_id=?`, work.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("year retry count=%d err=%v", count, err)
	}
	targets, err := store.WorksForEnrichment(ctx, "bangumi", false, 0, work.ID)
	if err != nil || len(targets) != 0 {
		t.Fatalf("year targets=%v err=%v", targets, err)
	}
	if _, err = store.UpdateWork(ctx, work.ID, WorkInput{Title: "Title", Type: "game", Year: 2024}); err != nil {
		t.Fatal(err)
	}
	targets, err = store.WorksForEnrichment(ctx, "bangumi", false, 0, work.ID)
	if err != nil || len(targets) != 1 || targets[0].Type != "game" {
		t.Fatalf("game targets=%v err=%v", targets, err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_enrichment_retries WHERE work_id=?`, work.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("type retry count=%d err=%v", count, err)
	}
}
