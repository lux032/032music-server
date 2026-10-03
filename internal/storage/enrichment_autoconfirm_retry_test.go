package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// AutoConfirmWorkMatchCandidate must clean the work's enrichment retry row in
// the same transaction as the confirm (and its durable effect fact): a crash
// after commit cannot strand a retry behind the resume effect skip, and a
// failure rolls the cleanup back together with the confirm.
func TestAutoConfirmWorkMatchCandidateCleansRetryAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "autoconfirm-retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	work, err := store.CreateWork(ctx, WorkInput{Title: "Frieren", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ReplaceWorkMatchCandidates(ctx, work.ID, []WorkMatchCandidate{{Source: "bangumi", ExternalID: "400602", Title: "Frieren", Type: "anime"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `INSERT INTO work_enrichment_retries(work_id,source,requested_at) VALUES(?,'bangumi','2024-01-01')`, work.ID); err != nil {
		t.Fatal(err)
	}
	candidates, err := store.WorkMatchCandidates(ctx, work.ID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%v err=%v", candidates, err)
	}
	retryCount := func() int {
		var n int
		if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM work_enrichment_retries WHERE work_id=?`, work.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Injected failure on the retry delete rolls back the whole confirm.
	if _, err = store.db.ExecContext(ctx, `CREATE TRIGGER fail_retry_cleanup BEFORE DELETE ON work_enrichment_retries BEGIN SELECT RAISE(ABORT,'injected retry failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = store.AutoConfirmWorkMatchCandidate(ctx, work.ID, candidates[0].ID, 0); err == nil {
		t.Fatal("missing injected error")
	}
	if retryCount() != 1 {
		t.Fatal("retry cleanup escaped failed confirm")
	}
	remaining, _ := store.WorkMatchCandidates(ctx, work.ID)
	if len(remaining) != 1 || remaining[0].Status != "candidate" {
		t.Fatalf("confirm leaked after rollback: %+v", remaining)
	}
	if _, err = store.db.ExecContext(ctx, `DROP TRIGGER fail_retry_cleanup`); err != nil {
		t.Fatal(err)
	}
	if err = store.AutoConfirmWorkMatchCandidate(ctx, work.ID, candidates[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	if retryCount() != 0 {
		t.Fatal("retry row survived successful confirm")
	}
	remaining, _ = store.WorkMatchCandidates(ctx, work.ID)
	if len(remaining) != 1 || remaining[0].Status != "confirmed" {
		t.Fatalf("candidates=%+v", remaining)
	}
}
