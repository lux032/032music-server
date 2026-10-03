package storage

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func durableArtistFixture(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, artist := seedEnrichmentEntities(t, s, ctx)
	input, err := s.ArtistForMatching(ctx, artist)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateDurableArtistRun(ctx, []ArtistRunItemInput{{Artist: input}, {Artist: ArtistMatchInput{ID: 999999, Name: "Deleted"}}})
	if err != nil {
		t.Fatal(err)
	}
	return s, run, artist
}
func TestDurableArtistItemsRecoveryAndIdempotentCounters(t *testing.T) {
	s, run, artist := durableArtistFixture(t)
	ctx := t.Context()
	item, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil || item.ObjectID != artist {
		t.Fatal(item, err)
	}
	c := ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if err = s.ReplaceArtistCandidates(ctx, artist, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "safe", DisplayName: "Artist", Score: 100}}); err != nil {
		t.Fatal(err)
	}
	if bound, err := s.AutoBindArtistRunCandidate(ctx, artist, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "safe", DisplayName: "Artist"}, c); err != nil || !bound {
		t.Fatal(bound, err)
	}
	// Crash after identity commit but before item completion: matched fact survives.
	n, err := s.RecoverDurableArtistRuns(ctx)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	legacyClosed, err := s.FailRunningArtistMatchRuns(ctx, "legacy interruption")
	if err != nil || legacyClosed != 0 {
		t.Fatal(legacyClosed, err)
	}
	r, err := s.DurableArtistRun(ctx, run)
	if err != nil || r.Status != "paused" || r.Processed != 0 {
		t.Fatal(r, err)
	}
	if _, err = s.ClaimArtistRunItem(ctx, run); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
	if err = s.TransitionArtistRun(ctx, run, "resume", ""); err != nil {
		t.Fatal(err)
	}
	item, err = s.ClaimArtistRunItem(ctx, run)
	if err != nil || !item.MatchedFact || item.ID != c.ItemID {
		t.Fatal(item, err)
	}
	c.ClaimToken = item.ClaimToken
	if err = s.CompleteArtistRunItem(ctx, c, "skipped"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteArtistRunItem(ctx, c, "failed"); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil || next.ObjectID != 999999 {
		t.Fatal(next, err)
	}
	if err = s.CompleteArtistRunItem(ctx, ArtistRunCheckpoint{RunID: run, ItemID: next.ID, ClaimToken: next.ClaimToken}, "skipped"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimArtistRunItem(ctx, run); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if err = s.TransitionArtistRun(ctx, run, "complete", ""); err != nil {
		t.Fatal(err)
	}
	r, err = s.DurableArtistRun(ctx, run)
	if err != nil || r.Status != "completed" || r.Processed != 2 || r.Matched != 1 || r.Skipped != 1 {
		t.Fatal(r, err)
	}
}
func TestDurableArtistWaitBudgetResumeAndCancel(t *testing.T) {
	s, run, _ := durableArtistFixture(t)
	ctx := t.Context()
	item, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	c := ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	deadline := time.Now().Add(time.Hour)
	for i := 0; i < 2; i++ {
		if err = s.RecordArtistRunWait(ctx, c, "lastfm", deadline, time.Minute, true); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecordArtistRunWait(ctx, c, "lastfm", deadline.Add(time.Minute), time.Minute, false); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordArtistRunWait(ctx, c, "lastfm", deadline, time.Minute, true); !errors.Is(err, ErrArtistWaitBudget) {
		t.Fatal(err)
	}
	r, err := s.DurableArtistRun(ctx, run)
	if err != nil || r.Status != "paused" || r.PauseReason != "rate_limit_count" || r.WaitTotalMS != int64((4*time.Minute)/time.Millisecond) {
		t.Fatal(r, err)
	}
	if err = s.TransitionArtistRun(ctx, run, "resume", ""); err != nil {
		t.Fatal(err)
	}
	r, err = s.DurableArtistRun(ctx, run)
	if err != nil || r.BudgetBaselineMS != r.WaitTotalMS || r.WaitingUntil != deadline.Add(time.Minute).UTC().Format(time.RFC3339Nano) {
		t.Fatal(r, err)
	}
	item, err = s.ClaimArtistRunItem(ctx, run)
	if err != nil || item.RateLimitCount != 0 {
		t.Fatal(item, err)
	}
	c.ClaimToken = item.ClaimToken
	if err = s.RecordArtistRunWait(ctx, c, "lastfm", deadline, 30*time.Minute, false); !errors.Is(err, ErrArtistWaitBudget) {
		t.Fatal(err)
	}
	r, _ = s.DurableArtistRun(ctx, run)
	if r.Status != "paused" || r.PauseReason != "rate_limit_wait_budget" || r.WaitTotalMS != int64((34*time.Minute)/time.Millisecond) {
		t.Fatal(r)
	}
	if err = s.TransitionArtistRun(ctx, run, "cancel", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionArtistRun(ctx, run, "resume", ""); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
	if err = s.RecordArtistRunWait(ctx, c, "lastfm", deadline, time.Minute, true); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
}
func TestDurableArtistSourceCheckpointAndBindingRollback(t *testing.T) {
	s, run, artist := durableArtistFixture(t)
	ctx := t.Context()
	item, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	c := ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	input, _ := s.ArtistForMatching(ctx, artist)
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	v := ArtistSourceSnapshot{Candidates: []ArtistCandidate{{Source: "musicbrainz", ExternalID: "safe", DisplayName: "Artist", Score: 100}}}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_item_source BEFORE INSERT ON artist_match_item_sources BEGIN SELECT RAISE(ABORT,'source checkpoint failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArtistRunSourceCheck(ctx, input, setting, v, c); err == nil {
		t.Fatal("missing checkpoint error")
	}
	candidates, _ := s.ArtistCandidates(ctx, artist)
	if len(candidates) != 0 {
		t.Fatal("candidate escaped source checkpoint rollback")
	}
	if _, err = s.db.ExecContext(ctx, `DROP TRIGGER fail_item_source`); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArtistRunSourceCheck(ctx, input, setting, v, c); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_item_sources WHERE item_id=?`, item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_matched_fact BEFORE UPDATE OF matched_fact ON artist_match_run_items BEGIN SELECT RAISE(ABORT,'matched fact failed'); END`); err != nil {
		t.Fatal(err)
	}
	if bound, err := s.AutoBindArtistRunCandidate(ctx, artist, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "safe", DisplayName: "Artist"}, c); err == nil || bound {
		t.Fatal(bound, err)
	}
	if _, err = s.ArtistExternalID(ctx, artist, "musicbrainz"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("profile escaped rollback", err)
	}
	if err = s.TransitionArtistRun(ctx, run, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArtistRunSourceCheck(ctx, input, setting, v, c); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
}
func TestDurableArtistRunAtomicCreateConflictAndLegacyRecovery(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, artist := seedEnrichmentEntities(t, s, ctx)
	input, _ := s.ArtistForMatching(ctx, artist)
	if _, err := s.CreateDurableArtistRun(ctx, []ArtistRunItemInput{{Artist: input}, {Artist: input}}); err == nil {
		t.Fatal("duplicate item accepted")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_runs`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	legacy, err := s.CreateArtistMatchRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateDurableArtistRun(ctx, []ArtistRunItemInput{{Artist: input}}); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
	if _, err = s.RecoverDurableArtistRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.FailRunningArtistMatchRuns(ctx, "legacy cannot resume precisely"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	r, err := s.DurableArtistRun(ctx, legacy)
	if err != nil || r.DurableVersion != 0 || r.Status != "failed" {
		t.Fatal(r, err)
	}
}

func TestDurableArtistStaleClaimCannotWriteAfterResume(t *testing.T) {
	s, run, artist := durableArtistFixture(t)
	ctx := t.Context()
	item, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	old := ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if err = s.TransitionArtistRun(ctx, run, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionArtistRun(ctx, run, "resume", ""); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil || next.ID != item.ID || next.ClaimToken == item.ClaimToken {
		t.Fatal(next, err)
	}
	input, _ := s.ArtistForMatching(ctx, artist)
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	if err = s.SaveArtistRunSourceCheck(ctx, input, setting, ArtistSourceSnapshot{}, old); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
	if err = s.CompleteArtistRunItem(ctx, old, "skipped"); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
	if err = s.RecordArtistRunWait(ctx, old, "musicbrainz", time.Now(), time.Second, true); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
}

func TestMigration037PreservesLegacyRowsAndForeignKeys(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if _, err := s.db.ExecContext(ctx, `DROP TABLE artist_match_item_sources; DROP TABLE artist_match_run_items; DROP TABLE artist_match_runs; CREATE TABLE artist_match_runs(id INTEGER PRIMARY KEY,status TEXT NOT NULL,total_artists INTEGER NOT NULL DEFAULT 0,processed_artists INTEGER NOT NULL DEFAULT 0,matched_artists INTEGER NOT NULL DEFAULT 0,review_artists INTEGER NOT NULL DEFAULT 0,failed_artists INTEGER NOT NULL DEFAULT 0,current_artist TEXT,error_message TEXT,created_at TEXT NOT NULL DEFAULT 'legacy',finished_at TEXT,skipped_artists INTEGER NOT NULL DEFAULT 0,no_result_artists INTEGER NOT NULL DEFAULT 0) STRICT; INSERT INTO artist_match_runs(status,total_artists,processed_artists,matched_artists,skipped_artists,no_result_artists) VALUES('completed',4,3,1,1,1)`); err != nil {
		t.Fatal(err)
	}
	script, err := migrationFiles.ReadFile("migrations/037_artist_durable_runs.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, string(script)); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListArtistMatchRuns(ctx, 1)
	if err != nil || len(runs) != 1 || runs[0].Processed != 3 || runs[0].Matched != 1 || runs[0].Skipped != 1 || runs[0].NoResult != 1 {
		t.Fatal(runs, err)
	}
	r, err := s.DurableArtistRun(ctx, runs[0].ID)
	if err != nil || r.DurableVersion != 0 {
		t.Fatal(r, err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO artist_match_run_items(run_id,object_id,input_json) VALUES(999999,1,'{}')`); err == nil {
		t.Fatal("run FK missing")
	}
}

func TestDurableArtistCompleteFailureRollsBackCounters(t *testing.T) {
	s, run, _ := durableArtistFixture(t)
	ctx := t.Context()
	item, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	c := ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_counter BEFORE UPDATE OF processed_artists ON artist_match_runs BEGIN SELECT RAISE(ABORT,'counter failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteArtistRunItem(ctx, c, "review"); err == nil {
		t.Fatal("missing counter error")
	}
	var status string
	if err = s.db.QueryRowContext(ctx, `SELECT status FROM artist_match_run_items WHERE id=?`, item.ID).Scan(&status); err != nil || status != "in_progress" {
		t.Fatal(status, err)
	}
	r, err := s.DurableArtistRun(ctx, run)
	if err != nil || r.Processed != 0 || r.Review != 0 {
		t.Fatal(r, err)
	}
}

func TestDurableArtistResumeConflictAndMissingQueryContext(t *testing.T) {
	s, run, artist := durableArtistFixture(t)
	ctx := t.Context()
	if err := s.TransitionArtistRun(ctx, run, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateArtistMatchRun(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionArtistRun(ctx, run, "resume", ""); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
	if err = s.FinishArtistMatchRun(ctx, other, "cancelled", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionArtistRun(ctx, run, "resume", ""); err != nil {
		t.Fatal(err)
	}
	item, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := s.ArtistForMatching(ctx, artist)
	setting, _ := s.MetadataSourceSetting(ctx, "lastfm")
	if err = s.SaveArtistRunSourceCheck(ctx, input, setting, ArtistSourceSnapshot{}, ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}); err == nil {
		t.Fatal("uncaptured Last.fm query accepted")
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_match_item_sources WHERE item_id=?`, item.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestArtistDurableRunnerIndexesTTLAndDeadlineSource(t *testing.T) {
	s, run, artist := durableArtistFixture(t)
	ctx := t.Context()
	for _, index := range []string{"idx_artist_match_runs_status_id", "idx_artist_run_items_pending"} {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatal(index, count, err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Fatal("FK violation")
	}
	rows.Close()
	item, err := s.ClaimArtistRunItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	c := ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
	input, _ := s.ArtistForMatching(ctx, artist)
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	if err = s.SaveArtistRunSourceCheck(ctx, input, setting, ArtistSourceSnapshot{}, c); err != nil {
		t.Fatal(err)
	}
	ik, ck := ArtistMatchKeys(input, setting)
	if _, err = s.ArtistRunItemSource(ctx, item.ID, "musicbrainz", ik, ck, 30); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artist_match_item_sources SET checked_at='2000-01-01T00:00:00Z' WHERE item_id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ArtistRunItemSource(ctx, item.ID, "musicbrainz", ik, ck, 30); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	far := time.Now().Add(time.Hour)
	if err = s.RecordArtistRunWait(ctx, c, "musicbrainz", far, 0, true); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordArtistRunWait(ctx, c, "lastfm", far.Add(-time.Minute), 0, true); err != nil {
		t.Fatal(err)
	}
	r, _ := s.DurableArtistRun(ctx, run)
	if r.WaitSource != "musicbrainz" || r.WaitingUntil != far.UTC().Format(time.RFC3339Nano) {
		t.Fatal(r)
	}
}

func TestArtistDurableConcurrentClaimsAndPause(t *testing.T) {
	s, run, _ := durableArtistFixture(t)
	ctx := t.Context()
	start := make(chan struct{})
	items := make(chan ArtistRunItem, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; item, err := s.ClaimArtistRunItem(ctx, run); items <- item; errs <- err }()
	}
	close(start)
	first, second := <-items, <-items
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if first.ID == second.ID {
		t.Fatal("duplicate concurrent claim", first, second)
	}
	if err := s.TransitionArtistRun(ctx, run, "pause", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimArtistRunItem(ctx, run); !errors.Is(err, ErrArtistRunState) {
		t.Fatal(err)
	}
}
