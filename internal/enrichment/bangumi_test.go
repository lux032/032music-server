package enrichment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func TestBangumiTitleKeys(t *testing.T) {
	for _, tc := range []struct {
		local, remote string
		equal         bool
	}{
		{"けいおん!", "けいおん！", true}, {"けいおん!", "けいおん！！", false},
		{"ｹｲｵﾝ!", "けいおん！", false}, {"CLANNAD ～AFTER STORY～", "CLANNAD 〜AFTER STORY〜", true},
		{"ヴァイオレット・エヴァーガーデン", "ヴァイオレット・エヴァーガーデン", true},
	} {
		if got := strictTitleKey(tc.local) == strictTitleKey(tc.remote); got != tc.equal {
			t.Errorf("strict(%q,%q)=%v", tc.local, tc.remote, got)
		}
	}
	if strictTitleKey("ヴァイオレット・エヴァーガーデン") != "ヴァイオレット・エヴァーガーデン" || looseTitleKey("ヴァイオレット・エヴァーガーデン") != "ヴァイオレットエヴァーガーデン" {
		t.Fatal("kana punctuation changed incorrectly")
	}
}

func TestBangumiCandidateMatching(t *testing.T) {
	tests := []struct{ name, title, reply, want string }{
		{"unique original and translation", "葬送のフリーレン", `{"data":[{"id":123,"type":2,"name":"葬送のフリーレン","name_cn":"葬送的芙莉莲","date":null}]}`, "succeeded"},
		{"duplicate originals", "Test", `{"data":[{"id":1,"type":2,"name":"Test"},{"id":2,"type":2,"name":"Test"}]}`, "review"},
		{"exclamation distinguishes seasons", "けいおん!", `{"data":[{"id":1,"type":2,"name":"けいおん！"},{"id":2,"type":2,"name":"けいおん！！"}]}`, "succeeded"},
		{"kana not folded", "ｹｲｵﾝ!", `{"data":[{"id":1,"type":2,"name":"けいおん！"}]}`, "review"},
		{"wave dash", "CLANNAD ～AFTER STORY～", `{"data":[{"id":1,"type":2,"name":"CLANNAD 〜AFTER STORY〜"}]}`, "succeeded"},
		{"translated only", "葬送のフリーレン", `{"data":[{"id":1,"type":2,"name":"Frieren","name_cn":"葬送のフリーレン"}]}`, "review"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.reply) }))
			ctx := context.Background()
			original, _ := store.WorkByID(ctx, id)
			if _, err := store.UpdateWork(ctx, id, storage.WorkInput{Title: tc.title, Type: original.Type}); err != nil {
				t.Fatal(err)
			}
			outcome, err := manager.enrichBangumiWork(ctx, 0, storage.WorkEnrichmentTarget{ID: id, Title: tc.title, Type: "anime"}, false)
			if err != nil || outcome != tc.want {
				t.Fatalf("outcome=%s err=%v", outcome, err)
			}
			if tc.name == "unique original and translation" {
				work, _ := store.WorkByID(ctx, id)
				if work.TranslatedTitle != "葬送的芙莉莲" {
					t.Fatalf("translation=%q", work.TranslatedTitle)
				}
			}
		})
	}
}

func TestBangumiRejectedCandidateForceDoesNotConfirm(t *testing.T) {
	var count atomic.Int32
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		io.WriteString(w, `{"data":[{"id":1,"type":2,"name":"葬送のフリーレン","date":"2023-01-01"}]}`)
	}))
	ctx := context.Background()
	target := storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime", Year: 2024}
	outcome, err := manager.enrichBangumiWork(ctx, 0, target, false)
	if err != nil || outcome != "review" {
		t.Fatalf("%s %v", outcome, err)
	}
	candidates, _ := store.WorkMatchCandidates(ctx, id)
	if err = store.SetWorkMatchCandidateStatus(ctx, id, candidates[0].ID, "rejected"); err != nil {
		t.Fatal(err)
	}
	target.Year = 0 // The rejected strict match must remain rejected even without a year conflict.
	outcome, err = manager.enrichBangumiWork(ctx, 0, target, true)
	if err != nil || outcome != "skipped" {
		t.Fatalf("force: %s %v", outcome, err)
	}
	candidates, _ = store.WorkMatchCandidates(ctx, id)
	if candidates[0].Status != "rejected" || count.Load() != 2 {
		t.Fatalf("candidate=%+v requests=%d", candidates, count.Load())
	}
}

func TestBangumiSearchTypesAndSkipped(t *testing.T) {
	for _, tc := range []struct {
		kind, types string
		calls       int
	}{{"game", "[4]", 1}, {"movie", "[2]", 1}, {"other", "[2,4]", 1}, {"drama", "", 0}, {"commercial", "", 0}} {
		t.Run(tc.kind, func(t *testing.T) {
			calls := 0
			manager, _, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("limit") != "10" {
					t.Errorf("limit=%s", r.URL.RawQuery)
				}
				var body struct {
					Keyword string `json:"keyword"`
					Filter  struct {
						Type []int `json:"type"`
					} `json:"filter"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				types, _ := json.Marshal(body.Filter.Type)
				if string(types) != tc.types || body.Keyword != "Original" {
					t.Errorf("body=%+v", body)
				}
				io.WriteString(w, `{"data":[]}`)
			}))
			outcome, err := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: id, Title: "Original", Type: tc.kind}, false)
			if err != nil || outcome != "skipped" || calls != tc.calls {
				t.Fatalf("outcome=%q err=%v calls=%d", outcome, err, calls)
			}
		})
	}
}

func TestBangumiInvalidJSONIsNotCached(t *testing.T) {
	count := 0
	manager, _, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count++; fmt.Fprint(w, "<html>broken</html>") }))
	target := storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime"}
	for i := 0; i < 2; i++ {
		if _, err := manager.enrichBangumiWork(context.Background(), 0, target, false); err == nil {
			t.Fatal("expected invalid JSON error")
		}
	}
	if count != 2 {
		t.Fatalf("requests=%d", count)
	}
}

func TestStartRunRejectsArtistScope(t *testing.T) {
	manager, _, _, _ := phase4TestManager(t, http.NotFoundHandler())
	if _, err := manager.StartRun(context.Background(), RunRequest{Scope: "artist", TargetID: 1}); err == nil {
		t.Fatal("artist scope accepted")
	}
}

func TestStartRunReviewIsNotRetried(t *testing.T) {
	var requests atomic.Int32
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, `{"data":[{"id":1,"type":2,"name":"Unrelated"}]}`)
	}))
	ctx := context.Background()
	first, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	if result := waitRun(t, store, first.ID); result.Review != 1 {
		t.Fatalf("first=%+v", result)
	}
	manager.Wait()
	second, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	if result := waitRun(t, store, second.ID); result.Total != 0 || requests.Load() != 1 {
		t.Fatalf("second=%+v requests=%d", result, requests.Load())
	}
}

func TestBangumiConfirmedWorkForceDoesNotChangeProfile(t *testing.T) {
	var requests atomic.Int32
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, `{"data":[{"id":999,"type":2,"name":"葬送のフリーレン"}]}`)
	}))
	ctx := context.Background()
	if err := store.ReplaceWorkMatchCandidates(ctx, id, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "123", Title: "葬送のフリーレン", Type: "anime"}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := store.WorkMatchCandidates(ctx, id)
	if err := store.ConfirmWorkMatchCandidate(ctx, id, candidates[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	outcome, err := manager.enrichBangumiWork(ctx, 0, storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime"}, true)
	if err != nil || outcome != "skipped" || requests.Load() != 0 {
		t.Fatalf("outcome=%q err=%v requests=%d", outcome, err, requests.Load())
	}
	candidates, _ = store.WorkMatchCandidates(ctx, id)
	if len(candidates) != 1 || candidates[0].Status != "confirmed" || candidates[0].ExternalID != "123" {
		t.Fatalf("candidates=%+v", candidates)
	}
}

func TestBangumiTypeAndYearPreventAutoConfirm(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{{"wrong type", `{"data":[{"id":1,"type":4,"name":"葬送のフリーレン"}]}`}, {"year conflict", `{"data":[{"id":1,"type":2,"name":"葬送のフリーレン","date":"2020-01-01"}]}`}} {
		t.Run(tc.name, func(t *testing.T) {
			manager, _, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, tc.reply) }))
			result, err := manager.enrichBangumiWork(context.Background(), 0, storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime", Year: 2023}, false)
			if err != nil || result != "review" {
				t.Fatalf("result=%s err=%v", result, err)
			}
		})
	}
}

func TestBangumiWaitRateLimitCancelled(t *testing.T) {
	manager, _, _, _ := phase4TestManager(t, http.NotFoundHandler())
	manager.bangumiInterval = time.Hour
	manager.bangumiLast = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := manager.waitBangumiRateLimit(ctx); err != context.Canceled || time.Since(started) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(started))
	}
}

func TestCancelRunLastItemAndRestart(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	run, err := manager.StartRun(context.Background(), RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	if err = manager.CancelRun(run.ID + 1); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("wrong run error=%v", err)
	}
	if err = manager.CancelRun(run.ID); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	finished, _ := store.EnrichmentRun(context.Background(), run.ID)
	if finished.Status != "cancelled" || finished.Failed != 0 {
		t.Fatalf("run=%+v", finished)
	}
	if err = manager.CancelRun(run.ID); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("finished run error=%v", err)
	}
	again, err := manager.StartRun(context.Background(), RunRequest{Scope: "work", TargetID: id, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("restart did not issue request")
	}
	if err = manager.CancelRun(again.ID); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
}

func TestBangumiSearchConcurrentManualConfirmPreserved(t *testing.T) {
	var store *storage.Store
	var id int64
	manager, st, _, workID := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		existing, err := store.WorkMatchCandidates(r.Context(), id)
		if err != nil || len(existing) != 1 {
			t.Errorf("existing=%v err=%v", existing, err)
		} else if err = store.ConfirmWorkMatchCandidate(r.Context(), id, existing[0].ID, 0); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"data":[{"id":2,"type":2,"name":"葬送のフリーレン"}]}`)
	}))
	store, id = st, workID
	ctx := context.Background()
	if err := store.ReplaceWorkMatchCandidates(ctx, id, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "1", Title: "Other", Type: "anime"}}); err != nil {
		t.Fatal(err)
	}
	outcome, err := manager.enrichBangumiWork(ctx, 0, storage.WorkEnrichmentTarget{ID: id, Title: "葬送のフリーレン", Type: "anime"}, true)
	if err != nil || outcome == "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	candidates, _ := store.WorkMatchCandidates(ctx, id)
	for _, candidate := range candidates {
		if candidate.Status == "confirmed" && candidate.ExternalID != "1" {
			t.Fatalf("confirmation overwritten: %+v", candidates)
		}
	}
}

func TestBangumiMissRetryAfterTitleChange(t *testing.T) {
	calls := 0
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, `{"data":[]}`) }))
	ctx := context.Background()
	start := func(force bool) storage.EnrichmentRun {
		t.Helper()
		run, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id, Force: force})
		if err != nil {
			t.Fatal(err)
		}
		return waitRun(t, store, run.ID)
	}
	if result := start(false); result.Skipped != 1 {
		t.Fatalf("initial run=%+v", result)
	}
	// Wait for phaseRunning to be released after the persisted terminal status.
	manager.Wait()
	if result := start(false); result.Total != 0 || calls != 1 {
		t.Fatalf("miss retry=%+v calls=%d", result, calls)
	}
	manager.Wait()
	if result := start(true); result.Total != 1 || calls != 2 {
		t.Fatalf("forced retry=%+v calls=%d", result, calls)
	}
	manager.Wait()
	if _, err := store.UpdateWork(ctx, id, storage.WorkInput{Title: "New title", Type: "anime", Year: 2023}); err != nil {
		t.Fatal(err)
	}
	if result := start(false); result.Total != 1 || calls != 3 {
		t.Fatalf("renamed retry=%+v calls=%d", result, calls)
	}
	manager.Wait()
}

func TestCancelArtistMatchingStopsRun(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer close(release)
	manager, store, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mb/artist/" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	ctx := context.Background()
	setting, err := store.MetadataSourceSetting(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	runID, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("artist request did not start")
	}
	if err = manager.CancelArtistMatching(runID); err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	runs, err := store.ListArtistMatchRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].Status != "cancelled" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if err = manager.CancelArtistMatching(runID); !errors.Is(err, ErrRunNotActive) {
		t.Fatalf("completed run err=%v", err)
	}
}

func TestBangumiRejectedCandidateRenamedWorkRetry(t *testing.T) {
	ctx := context.Background()
	calls := 0
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, `{"data":[{"id":1,"type":2,"name":"New title"},{"id":2,"type":2,"name":"New title"}]}`)
	}))
	if err := store.ReplaceWorkMatchCandidates(ctx, id, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "1", Title: "Old title", Type: "anime"}}); err != nil {
		t.Fatal(err)
	}
	previous, _ := store.WorkMatchCandidates(ctx, id)
	if err := store.SetWorkMatchCandidateStatus(ctx, id, previous[0].ID, "rejected"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateWork(ctx, id, storage.WorkInput{Title: "New title", Type: "anime"}); err != nil {
		t.Fatal(err)
	}
	run, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	result := waitRun(t, store, run.ID)
	manager.Wait()
	if result.Succeeded != 1 || calls != 1 {
		t.Fatalf("run=%+v calls=%d", result, calls)
	}
	candidates, _ := store.WorkMatchCandidates(ctx, id)
	for _, candidate := range candidates {
		if candidate.ExternalID == "1" && candidate.Status != "rejected" || candidate.ExternalID == "2" && candidate.Status != "confirmed" {
			t.Fatalf("candidates=%+v", candidates)
		}
	}
}

func TestBangumiTypeChangeAfterMissUsesGameFilter(t *testing.T) {
	ctx := context.Background()
	calls := 0
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Filter struct {
				Type []int `json:"type"`
			} `json:"filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if calls == 2 && (len(body.Filter.Type) != 1 || body.Filter.Type[0] != 4) {
			t.Errorf("game filter=%v", body.Filter.Type)
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	run, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, store, run.ID)
	manager.Wait()
	if _, err = store.UpdateWork(ctx, id, storage.WorkInput{Title: "葬送のフリーレン", Type: "game", Year: 2023}); err != nil {
		t.Fatal(err)
	}
	run, err = manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	result := waitRun(t, store, run.ID)
	manager.Wait()
	if result.Total != 1 || calls != 2 {
		t.Fatalf("run=%+v calls=%d", result, calls)
	}
}

func TestBangumiYearEditDoesNotRetryMiss(t *testing.T) {
	ctx := context.Background()
	calls := 0
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, `{"data":[]}`) }))
	first, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, store, first.ID)
	manager.Wait()
	if _, err = store.UpdateWork(ctx, id, storage.WorkInput{Title: "葬送のフリーレン", Type: "anime", Year: 2024}); err != nil {
		t.Fatal(err)
	}
	second, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	result := waitRun(t, store, second.ID)
	manager.Wait()
	if result.Total != 0 || calls != 1 {
		t.Fatalf("run=%+v calls=%d", result, calls)
	}
}

func TestBangumiConfirmedWorkRenameDoesNotRetry(t *testing.T) {
	ctx := context.Background()
	calls := 0
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, `{"data":[]}`) }))
	if err := store.ReplaceWorkMatchCandidates(ctx, id, []storage.WorkMatchCandidate{{Source: "bangumi", ExternalID: "100", Title: "Original", Type: "anime"}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := store.WorkMatchCandidates(ctx, id)
	if err := store.ConfirmWorkMatchCandidate(ctx, id, candidates[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateWork(ctx, id, storage.WorkInput{Title: "Changed", Type: "anime", Year: 2023}); err != nil {
		t.Fatal(err)
	}
	run, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
	if err != nil {
		t.Fatal(err)
	}
	result := waitRun(t, store, run.ID)
	manager.Wait()
	candidates, _ = store.WorkMatchCandidates(ctx, id)
	if result.Total != 0 || calls != 0 || candidates[0].Status != "confirmed" || candidates[0].ExternalID != "100" {
		t.Fatalf("run=%+v candidates=%+v calls=%d", result, candidates, calls)
	}
}

func TestBangumiFailedRetryKeepsMarker(t *testing.T) {
	ctx := context.Background()
	calls := 0
	manager, store, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	if _, err := store.UpdateWork(ctx, id, storage.WorkInput{Title: "New", Type: "anime"}); err != nil {
		t.Fatal(err)
	}
	start := func() storage.EnrichmentRun {
		t.Helper()
		run, err := manager.StartRun(ctx, RunRequest{Scope: "work", TargetID: id})
		if err != nil {
			t.Fatal(err)
		}
		result := waitRun(t, store, run.ID)
		manager.Wait()
		return result
	}
	if result := start(); result.Failed != 1 {
		t.Fatalf("failed run=%+v", result)
	}
	pending, err := store.WorksForEnrichment(ctx, "bangumi", false, 0, id)
	if err != nil || len(pending) != 1 {
		t.Fatalf("marker lost: pending=%+v err=%v", pending, err)
	}
	if result := start(); result.Skipped != 1 || calls != 2 {
		t.Fatalf("retry=%+v calls=%d", result, calls)
	}
	// A successful empty response creates a miss and clears the retry marker.
	pending, err = store.WorksForEnrichment(ctx, "bangumi", false, 0, id)
	if err != nil || len(pending) != 0 {
		t.Fatalf("successful retry still selected: %+v err=%v", pending, err)
	}
}
