package enrichment

import (
	"context"
	"fmt"
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
	"testing"
)

func TestBangumiDeletedDuringSearchIsSkipped(t *testing.T) {
	var store *storage.Store
	var id int64
	manager, s, _, workID := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store != nil {
			if e := store.DeleteWork(context.Background(), id); e != nil {
				t.Errorf("delete work: %v", e)
			}
		}
		fmt.Fprint(w, `{"data":[{"id":123,"name":"葬送のフリーレン","type":2}]}`)
	}))
	store = s
	id = workID
	outcome, e := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime"}, true)
	if e != nil || outcome != "skipped" {
		t.Fatalf("deleted during search outcome=%q err=%v", outcome, e)
	}
}
func TestBangumiRemovedWorkIsSkipped(t *testing.T) {
	manager, s, _, id := phase4TestManager(t, http.NotFoundHandler())
	if e := s.DeleteWork(context.Background(), id); e != nil {
		t.Fatal(e)
	}
	outcome, e := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: id, Title: "Deleted", Type: "anime"}, false)
	if e != nil || outcome != "skipped" {
		t.Fatalf("removed outcome %q err %v", outcome, e)
	}
}
