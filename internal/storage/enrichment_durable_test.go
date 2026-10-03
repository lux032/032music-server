package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestDurableEnrichmentPreparationRecoveryAndForceItems(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	run, err := s.CreateDurableEnrichmentRun(ctx, "all", 0, true, []string{"albums", "tracks", "works", "series"})
	if err != nil {
		t.Fatal(err)
	}
	first := []EnrichmentItemInput{{ObjectID: 3, Parameters: json.RawMessage(`{"title":"third"}`)}, {ObjectID: 1, Parameters: json.RawMessage(`{"title":"first"}`)}}
	if err = s.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, "tracks", first); err != nil {
		t.Fatal(err)
	}
	// Re-preparation cannot replace the sequence with changed database sorting.
	if err = s.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, "tracks", []EnrichmentItemInput{{ObjectID: 99, Parameters: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	item, err := s.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, "tracks")
	if err != nil || item.ObjectID != 3 {
		t.Fatal(item, err)
	}
	c := EnrichmentCheckpoint{RunID: run.ID, ItemID: item.ID, Epoch: run.Epoch, Token: item.Token}
	if err = s.CompleteEnrichmentItem(ctx, c, "matched"); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteEnrichmentItem(ctx, c, "failed"); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, "tracks")
	if err != nil || next.ObjectID != 1 {
		t.Fatal(next, err)
	}
	old := EnrichmentCheckpoint{RunID: run.ID, ItemID: next.ID, Epoch: run.Epoch, Token: next.Token}
	if n, err := s.RecoverDurableEnrichmentRuns(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "resume", ""); err != nil {
		t.Fatal(err)
	}
	resumed, _ := s.DurableEnrichmentRun(ctx, run.ID)
	if !resumed.Force || resumed.ID != run.ID || resumed.Processed != 1 || resumed.Succeeded != 1 || resumed.Epoch == run.Epoch {
		t.Fatal(resumed)
	}
	if _, err = s.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, "tracks"); !errors.Is(err, ErrEnrichmentRunState) {
		t.Fatal("old worker epoch accepted", err)
	}
	next, err = s.ClaimEnrichmentItem(ctx, run.ID, resumed.Epoch, "tracks")
	if err != nil || next.ObjectID != 1 {
		t.Fatal(next, err)
	}
	if err = s.CompleteEnrichmentItem(ctx, old, "failed"); !errors.Is(err, ErrEnrichmentRunState) {
		t.Fatal(err)
	}
	if err = s.CompleteEnrichmentItem(ctx, EnrichmentCheckpoint{run.ID, next.ID, resumed.Epoch, next.Token}, "skipped"); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"albums", "works", "series"} {
		if err = s.PrepareEnrichmentStage(ctx, run.ID, resumed.Epoch, stage, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "complete", ""); err != nil {
		t.Fatal(err)
	}
	finished, _ := s.DurableEnrichmentRun(ctx, run.ID)
	if finished.Status != "completed" || finished.Processed != 2 || finished.Succeeded != 1 || finished.Skipped != 1 {
		t.Fatal(finished)
	}
}

func TestDurableEnrichmentAtomicPreparationAndCompletion(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	run, err := s.CreateDurableEnrichmentRun(ctx, "tracks", 0, true, []string{"tracks"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate := []EnrichmentItemInput{{ObjectID: 1, Parameters: json.RawMessage(`{}`)}, {ObjectID: 1, Parameters: json.RawMessage(`{}`)}}
	if err = s.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, "tracks", duplicate); err == nil {
		t.Fatal("duplicate snapshot accepted")
	}
	prepared, err := s.EnrichmentStagePrepared(ctx, run.ID, "tracks")
	if err != nil || prepared {
		t.Fatal(prepared, err)
	}
	current, _ := s.DurableEnrichmentRun(ctx, run.ID)
	if current.Total != 0 {
		t.Fatal("partial prepare", current)
	}
	if err = s.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, "tracks", duplicate[:1]); err != nil {
		t.Fatal(err)
	}
	item, err := s.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, "tracks")
	if err != nil {
		t.Fatal(err)
	}
	c := EnrichmentCheckpoint{run.ID, item.ID, run.Epoch, item.Token}
	if _, err = s.db.ExecContext(ctx, `CREATE TABLE durable_side_effect(value INTEGER NOT NULL) STRICT; CREATE TRIGGER fail_enrichment_count BEFORE UPDATE OF processed ON enrichment_runs BEGIN SELECT RAISE(ABORT,'checkpoint failed'); END`); err != nil {
		t.Fatal(err)
	}
	writes := 0
	apply := func(tx *sql.Tx) error {
		writes++
		_, err := tx.ExecContext(ctx, `INSERT INTO durable_side_effect(value) VALUES(1)`)
		return err
	}
	if err = s.ApplyEnrichmentItem(ctx, c, "matched", apply); err == nil {
		t.Fatal("missing injected error")
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM durable_side_effect`).Scan(&count); err != nil || count != 0 {
		t.Fatal("side effect escaped failed completion", count, err)
	}
	if _, err = s.db.ExecContext(ctx, `DROP TRIGGER fail_enrichment_count`); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyEnrichmentItem(ctx, c, "matched", apply); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyEnrichmentItem(ctx, c, "matched", apply); err != nil {
		t.Fatal(err)
	}
	if writes != 2 {
		t.Fatal("completed item reexecuted callback", writes)
	}
}

func TestDurableEnrichmentWaitBudgetAndRequestCheckpoint(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	run, err := s.CreateDurableEnrichmentRun(ctx, "works", 0, true, []string{"works"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, "works", []EnrichmentItemInput{{ObjectID: 1, Parameters: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	item, err := s.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, "works")
	if err != nil {
		t.Fatal(err)
	}
	c := EnrichmentCheckpoint{run.ID, item.ID, run.Epoch, item.Token}
	deadline := time.Now().Add(time.Hour)
	if err = s.RecordEnrichmentWait(ctx, c, "bangumi", deadline, time.Minute, true); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveEnrichmentRequest(ctx, c, "bangumi", "subject:1", "https://example.test/1", 200, []byte(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	var responses int
	if err = s.db.QueryRowContext(ctx, `SELECT rate_limit_count FROM enrichment_run_items WHERE id=?`, item.ID).Scan(&responses); err != nil || responses != 0 {
		t.Fatal(responses, err)
	}
	status, body, err := s.EnrichmentRequest(ctx, run.ID, "bangumi", "subject:1", "https://example.test/1")
	if err != nil || status != 200 || string(body) != `{"id":1}` {
		t.Fatal(status, string(body), err)
	}
	for i := 0; i < 3; i++ {
		err = s.RecordEnrichmentWait(ctx, c, "bangumi", deadline, time.Minute, true)
		if i < 2 && err != nil || i == 2 && !errors.Is(err, ErrEnrichmentWaitBudget) {
			t.Fatal(i, err)
		}
	}
	paused, _ := s.DurableEnrichmentRun(ctx, run.ID)
	if paused.Status != "paused" || paused.WaitTotalMS != 240000 {
		t.Fatal(paused)
	}
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "resume", ""); err != nil {
		t.Fatal(err)
	}
	resumed, _ := s.DurableEnrichmentRun(ctx, run.ID)
	if resumed.BudgetBaselineMS != 240000 || resumed.WaitingUntil != deadline.UTC().Format(time.RFC3339Nano) {
		t.Fatal(resumed)
	}
	item, err = s.ClaimEnrichmentItem(ctx, run.ID, resumed.Epoch, "works")
	if err != nil {
		t.Fatal(err)
	}
	c = EnrichmentCheckpoint{run.ID, item.ID, resumed.Epoch, item.Token}
	if err = s.RecordEnrichmentWait(ctx, c, "bangumi", deadline, 30*time.Minute, false); !errors.Is(err, ErrEnrichmentWaitBudget) {
		t.Fatal(err)
	}
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "cancel", "manual"); err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "resume", ""); !errors.Is(err, ErrEnrichmentRunState) {
		t.Fatal(err)
	}
}

func TestMigration038RunnerForeignKeysAndLegacyRows(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	legacy, err := s.CreateEnrichmentRun(ctx, "tracks", 0, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishEnrichmentRun(ctx, legacy.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	row, err := s.DurableEnrichmentRun(ctx, legacy.ID)
	if err != nil || row.Version != 0 || !row.Force || row.Status != "completed" {
		t.Fatal(row, err)
	}
	for _, index := range []string{"idx_enrichment_runs_status", "idx_enrichment_items_pending"} {
		var count int
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatal(index, count, err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO enrichment_run_items(run_id,stage,object_id,parameters_json) VALUES(999999,'tracks',1,'{}')`); err == nil {
		t.Fatal("run FK not enforced")
	}
}
