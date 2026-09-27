package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Confirming a Bangumi candidate already bound to another work must return a
// typed conflict instead of a raw UNIQUE constraint failure.
func TestConfirmWorkCandidateExternalIDConflict(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "work-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateWork(ctx, WorkInput{Title: "Frieren", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateWork(ctx, WorkInput{Title: "Frieren", Type: "other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first.ID, second.ID} {
		if err = store.ReplaceWorkMatchCandidates(ctx, id, []WorkMatchCandidate{{Source: "bangumi", ExternalID: "400602", Title: "Frieren", Type: "anime"}}); err != nil {
			t.Fatal(err)
		}
	}
	c1, _ := store.WorkMatchCandidates(ctx, first.ID)
	if err = store.ConfirmWorkMatchCandidate(ctx, first.ID, c1[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	c2, _ := store.WorkMatchCandidates(ctx, second.ID)
	err = store.ConfirmWorkMatchCandidate(ctx, second.ID, c2[0].ID, 0)
	var conflict *WorkExternalIDConflictError
	if !errors.As(err, &conflict) || conflict.OwnerWorkID != first.ID {
		t.Fatalf("err=%v", err)
	}
	if err = store.AutoConfirmWorkMatchCandidate(ctx, second.ID, c2[0].ID, 0); !errors.Is(err, ErrAutoConfirmConflict) {
		t.Fatalf("auto err=%v", err)
	}
	// Re-confirming on the owning work stays idempotent.
	if err = store.ConfirmWorkMatchCandidate(ctx, first.ID, c1[0].ID, 0); err != nil {
		t.Fatal(err)
	}
}
