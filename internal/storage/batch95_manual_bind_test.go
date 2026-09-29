package storage

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func batch95Candidate(external string) WorkMatchCandidate {
	return WorkMatchCandidate{Source: "bangumi", ExternalID: external, Title: "Subject " + external, TranslatedTitle: "条目", Type: "anime", Year: 2024, PageURL: "https://bgm.tv/subject/" + external, Score: 100, Evidence: []string{"手动"}, Payload: json.RawMessage(`{"id":1,"type":2}`)}
}

func candidateStatus(t *testing.T, s *Store, workID int64, external string) string {
	t.Helper()
	var status string
	if err := s.db.QueryRow(`SELECT status FROM work_match_candidates WHERE work_id=? AND source='bangumi' AND external_id=?`, workID, external).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestBatch95ManualBindWorkBangumiAtomicSemantics(t *testing.T) {
	f := newAlbumMergeFixture(t)
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		work, _ := f.store.CreateWork(ctx, WorkInput{Title: "Success", Type: "anime"})
		if err := f.store.ManualBindWorkBangumi(ctx, work.ID, batch95Candidate("9501")); err != nil {
			t.Fatal(err)
		}
		if external, err := f.store.WorkBangumiExternalID(ctx, work.ID); err != nil || external != "9501" || candidateStatus(t, f.store, work.ID, "9501") != "confirmed" {
			t.Fatalf("external=%q err=%v", external, err)
		}
	})

	t.Run("successful manual choice overrides rejection", func(t *testing.T) {
		work, _ := f.store.CreateWork(ctx, WorkInput{Title: "Rejected then selected", Type: "anime"})
		id, err := f.store.AddWorkMatchCandidate(ctx, work.ID, batch95Candidate("9502"))
		if err != nil || f.store.SetWorkMatchCandidateStatus(ctx, work.ID, id, "rejected") != nil {
			t.Fatal(err)
		}
		if err = f.store.ManualBindWorkBangumi(ctx, work.ID, batch95Candidate("9502")); err != nil {
			t.Fatal(err)
		}
		if status := candidateStatus(t, f.store, work.ID, "9502"); status != "confirmed" {
			t.Fatalf("status=%s", status)
		}
	})

	t.Run("failure preserves rejection", func(t *testing.T) {
		owner, _ := f.store.CreateWork(ctx, WorkInput{Title: "Owner", Type: "anime"})
		if err := f.store.ManualBindWorkBangumi(ctx, owner.ID, batch95Candidate("9503")); err != nil {
			t.Fatal(err)
		}
		target, _ := f.store.CreateWork(ctx, WorkInput{Title: "Target", Type: "anime"})
		id, err := f.store.AddWorkMatchCandidate(ctx, target.ID, batch95Candidate("9503"))
		if err != nil || f.store.SetWorkMatchCandidateStatus(ctx, target.ID, id, "rejected") != nil {
			t.Fatal(err)
		}
		err = f.store.ManualBindWorkBangumi(ctx, target.ID, batch95Candidate("9503"))
		var conflict *WorkExternalIDConflictError
		if !errors.As(err, &conflict) || conflict.OwnerWorkID != owner.ID {
			t.Fatalf("err=%v conflict=%+v", err, conflict)
		}
		if status := candidateStatus(t, f.store, target.ID, "9503"); status != "rejected" {
			t.Fatalf("status=%s", status)
		}
	})

	t.Run("already aligned and sequential second call rejected", func(t *testing.T) {
		work, _ := f.store.CreateWork(ctx, WorkInput{Title: "Already", Type: "anime"})
		if err := f.store.ManualBindWorkBangumi(ctx, work.ID, batch95Candidate("9504")); err != nil {
			t.Fatal(err)
		}
		if err := f.store.ManualBindWorkBangumi(ctx, work.ID, batch95Candidate("9505")); !errors.Is(err, ErrWorkAlreadyBangumiBound) {
			t.Fatalf("second call err=%v", err)
		}
		external, _ := f.store.WorkBangumiExternalID(ctx, work.ID)
		if external != "9504" {
			t.Fatalf("external=%q", external)
		}
	})
}
