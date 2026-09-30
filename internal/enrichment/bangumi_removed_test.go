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

// D-4 window 1: the work is deleted while the Bangumi search request is in
// flight and the search then returns a definitive 404 (no results). Writing
// the miss row fails its foreign key; the outcome must be "skipped" so the
// run neither counts a failure nor feeds the consecutive-failure breaker.
func TestBangumiSearchMissAfterWorkDeletedIsSkipped(t *testing.T) {
	var store *storage.Store
	var id int64
	manager, s, _, workID := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store != nil {
			if e := store.DeleteWork(context.Background(), id); e != nil {
				t.Errorf("delete work: %v", e)
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	store = s
	id = workID
	outcome, e := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime"}, true)
	if e != nil || outcome != "skipped" {
		t.Fatalf("deleted-during-search-miss outcome=%q err=%v", outcome, e)
	}
}

// D-4 window 2 (zero-candidate half): the work is deleted during the search
// request and the search returns an empty result page. The existence recheck
// after candidate building turns this into a skip before any write happens.
func TestBangumiEmptyResultAfterWorkDeletedIsSkipped(t *testing.T) {
	var store *storage.Store
	var id int64
	manager, s, _, workID := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store != nil {
			if e := store.DeleteWork(context.Background(), id); e != nil {
				t.Errorf("delete work: %v", e)
			}
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	store = s
	id = workID
	outcome, e := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime"}, true)
	if e != nil || outcome != "skipped" {
		t.Fatalf("deleted-during-empty-result outcome=%q err=%v", outcome, e)
	}
}

// D-4 window 2（零候选分支）：作品在“存在性复查之后、miss 写入之前”被删除，
// miss 写入外键失败，结果必须是 skipped（testWorkWriteHook 确定性命中该窗口）。
func TestBangumiZeroCandidateMissWriteAfterWorkDeletedIsSkipped(t *testing.T) {
	manager, s, _, workID := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[]}`)
	}))
	manager.testWorkWriteHook = func() {
		if e := s.DeleteWork(context.Background(), workID); e != nil {
			t.Errorf("delete work: %v", e)
		}
	}
	outcome, e := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: workID, Title: "葬送のフリーレン", Type: "anime"}, true)
	if e != nil || outcome != "skipped" {
		t.Fatalf("zero-candidate window outcome=%q err=%v", outcome, e)
	}
}

// D-4 window 2（review 返回前复查）：候选已落库且 pending>0，作品在返回
// review 之前被清理，级联删除后没有东西可审，结果必须是 skipped 而非 review。
func TestBangumiReviewReturnAfterWorkDeletedIsSkipped(t *testing.T) {
	manager, s, _, workID := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 标题部分匹配的候选：pending=1、matches=0，走 review 分支。
		fmt.Fprint(w, `{"data":[{"id":456,"name":"葬送のフリーレン 第2期","type":2}]}`)
	}))
	manager.testWorkWriteHook = func() {
		if e := s.DeleteWork(context.Background(), workID); e != nil {
			t.Errorf("delete work: %v", e)
		}
	}
	outcome, e := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: workID, Title: "葬送のフリーレン", Type: "anime"}, true)
	if e != nil || outcome != "skipped" {
		t.Fatalf("review-window outcome=%q err=%v", outcome, e)
	}
}

// 对照：钩子不删除作品时，pending>0 依旧返回 review（复查不改变正常路径）。
func TestBangumiReviewReturnNormallyReview(t *testing.T) {
	manager, _, _, workID := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":456,"name":"葬送のフリーレン 第2期","type":2}]}`)
	}))
	manager.testWorkWriteHook = func() {}
	outcome, e := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: workID, Title: "葬送のフリーレン", Type: "anime"}, true)
	if e != nil || outcome != "review" {
		t.Fatalf("normal review path outcome=%q err=%v", outcome, e)
	}
}
