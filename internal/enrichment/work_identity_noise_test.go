package enrichment

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestWorkIdentityNoiseClearsPendingButPreservesDecisions(t *testing.T) {
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":99,"type":2,"name":"Completely unrelated"}]}`)
	}))
	ctx := context.Background()
	rejected, err := store.AddWorkMatchCandidate(ctx, id, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "1", Title: "Rejected", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetWorkMatchCandidateStatus(ctx, id, rejected, "rejected"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.AddWorkMatchCandidate(ctx, id, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: "2", Title: "Stale", Type: "anime"}); err != nil {
		t.Fatal(err)
	}
	outcome, err := manager.enrichBangumiWork(ctx, 0, storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime"}, true)
	if err != nil || outcome != "skipped" {
		t.Fatalf("%s %v", outcome, err)
	}
	candidates, err := store.WorkMatchCandidates(ctx, id)
	if err != nil || len(candidates) != 1 || candidates[0].ID != rejected || candidates[0].Status != "rejected" {
		t.Fatalf("%+v %v", candidates, err)
	}
}
