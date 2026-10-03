package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestEnrichmentRuntimeForceTracksSameRunAfterRestart(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	entered, release := make(chan struct{}), make(chan struct{})
	blocked := false
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		key := string(raw)
		mu.Lock()
		calls[key]++
		shouldBlock := strings.Contains(key, "Second") && !blocked
		if shouldBlock {
			blocked = true
		}
		mu.Unlock()
		if shouldBlock {
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	library := mustLibrary(t, s)
	for i, name := range []string{"First", "Second", "Third"} {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: "Anime", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, TrackType: "tv_size"}}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := m.StartRun(ctx, RunRequest{Scope: "tracks", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("second request not reached")
	}
	if err = m.PauseRun(run.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	close(release)
	before, _ := s.DurableEnrichmentRun(ctx, run.ID)
	if before.Processed < 1 {
		t.Fatal(before)
	}
	restarted := New(ctx, s, m.logger, t.TempDir())
	restarted.phaseEndpoints = m.phaseEndpoints
	if err = restarted.ResumeRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	after, _ := s.DurableEnrichmentRun(ctx, run.ID)
	if after.Status != "completed" || after.Processed != after.Total || !after.Force || after.ID != before.ID {
		t.Fatal(after)
	}
	mu.Lock()
	defer mu.Unlock()
	for key, n := range calls {
		if strings.Contains(key, "First") && n != 1 {
			t.Fatal("completed force target repeated", key, n)
		}
	}
}

func TestEnrichmentRuntimeRateLimitBudgetResume(t *testing.T) {
	calls := 0
	m, s, _, work := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 3 {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	artistFakeClock(m)
	run, err := m.StartRun(context.Background(), RunRequest{Scope: "work", TargetID: work, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	paused, _ := s.DurableEnrichmentRun(context.Background(), run.ID)
	if paused.Status != "paused" || paused.Processed != 0 || calls != 3 || paused.WaitTotalMS != 120000 {
		t.Fatal(paused, calls)
	}
	if err = m.ResumeRun(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	done, _ := s.DurableEnrichmentRun(context.Background(), run.ID)
	if done.Status != "completed" || done.Processed != 1 || done.BudgetBaselineMS != 120000 || done.WaitTotalMS < 180000 {
		t.Fatal(done)
	}
}

func TestEnrichmentRuntimeRequestCheckpointRestartNoNetwork(t *testing.T) {
	m, s, _, work := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("unexpected fixture request") }))
	ctx := context.Background()
	request := RunRequest{Scope: "work", TargetID: work, Force: true}
	run, err := s.CreateDurableEnrichmentRun(ctx, request.Scope, request.TargetID, true, phaseStages(request))
	if err != nil {
		t.Fatal(err)
	}
	items, err := m.collectDurablePhaseStage(ctx, request, "works")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, "works", items); err != nil {
		t.Fatal(err)
	}
	item, err := s.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, "works")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot phaseItemSnapshot
	if err = json.Unmarshal(item.Parameters, &snapshot); err != nil {
		t.Fatal(err)
	}
	c := storage.EnrichmentCheckpoint{RunID: run.ID, ItemID: item.ID, Epoch: run.Epoch, Token: item.Token}
	// Successful landing fact restores counters without rerunning side effects.
	if err = s.ApplyEnrichmentItem(ctx, c, "matched", nil); err != nil {
		t.Fatal(err)
	}
	restarted := New(ctx, s, m.logger, t.TempDir())
	if err = restarted.ResumeRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	done, _ := s.DurableEnrichmentRun(ctx, run.ID)
	if done.Status != "completed" || done.Succeeded != 1 {
		t.Fatal(done)
	}
}

// Pausing while a stage is still being counted must not leave a partial or
// duplicated preparation: after restart the stage is prepared exactly once.
func TestEnrichmentRuntimeStagePrepareOnceAfterPauseRestart(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls[string(raw)]++
		mu.Unlock()
		io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	library := mustLibrary(t, s)
	for i, name := range []string{"One", "Two", "Three"} {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: "Anime", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, TrackType: "tv_size"}}); err != nil {
			t.Fatal(err)
		}
	}
	paused := false
	m.testStageCollectHook = func(stage string) {
		if stage != "tracks" || paused {
			return
		}
		paused = true
		active, err := s.UnfinishedDurableEnrichmentRun(context.Background())
		if err != nil {
			t.Errorf("active run: %v", err)
			return
		}
		if err = m.PauseRun(active.ID); err != nil {
			t.Errorf("pause during collect: %v", err)
		}
	}
	run, err := m.StartRun(ctx, RunRequest{Scope: "tracks"})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	interrupted, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !paused || interrupted.Status != "paused" || interrupted.Total != 0 || interrupted.StageTracks != -1 {
		t.Fatalf("interrupted=%+v paused=%v", interrupted, paused)
	}
	restarted := New(ctx, s, m.logger, t.TempDir())
	restarted.phaseEndpoints = m.phaseEndpoints
	restarted.client = m.client
	restarted.bangumiInterval = 0
	if err = restarted.ResumeRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	done, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" || done.Total != 4 || done.Processed != 4 || done.StageTracks != 4 {
		t.Fatal(done)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("requests=%v", calls)
	}
	for key, n := range calls {
		if n != 1 {
			t.Fatalf("duplicated request %q x%d", key, n)
		}
	}
}

// Three items each waiting out a 10-minute 429 push the shared window past
// 30 minutes without three consecutive responses on one item: the run pauses
// on the window budget, keeps history, and a manual resume opens a new
// 30-minute window that lets the remaining item finish.
func TestEnrichmentRuntimeWaitWindowPauseAndManualResume(t *testing.T) {
	var mu sync.Mutex
	limited := map[string]bool{}
	calls := 0
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		key := string(raw)
		mu.Lock()
		calls++
		shouldLimit := !limited[key]
		limited[key] = true
		mu.Unlock()
		if shouldLimit {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(429)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	artistFakeClock(m)
	ctx := context.Background()
	library := mustLibrary(t, s)
	for i, name := range []string{"Alpha", "Beta", "Gamma"} {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: "Anime", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, TrackType: "tv_size"}}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := m.StartRun(ctx, RunRequest{Scope: "tracks", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	pausedRun, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pausedRun.Status != "paused" || pausedRun.PauseReason != "rate_limit_wait_budget" || pausedRun.Processed != 2 || pausedRun.WaitTotalMS != int64(30*time.Minute/time.Millisecond) {
		t.Fatalf("paused=%+v", pausedRun)
	}
	if err = m.ResumeRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	// The fixture track is a fourth limited item: its wait lands in the resumed
	// window (baseline 30min), adding one more 10-minute wait.
	done, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	baseline := int64(30 * time.Minute / time.Millisecond)
	if done.Status != "completed" || done.Processed != 4 || done.BudgetBaselineMS != baseline || done.WaitTotalMS != baseline+int64(10*time.Minute/time.Millisecond) {
		t.Fatalf("done=%+v", done)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 8 {
		t.Fatalf("calls=%d, want 8 (one 429 + one success per track)", calls)
	}
}

// A crash after the series member transaction committed but before the series
// item was completed must not re-apply members or re-issue graph requests on
// resume: the effect fact skips ApplyAutoSeries and the request checkpoints
// serve the graph.
func TestEnrichmentRuntimeSeriesCrashSkipsMemberReapply(t *testing.T) {
	server := syntheticSeriesGraphServer(t, chainEdges([][]int64{{1, 2}}), nil)
	defer server.Close()
	manager, store, albumID := seriesRunFixture(t, server)
	ctx := context.Background()
	work1 := bindRunWork(t, store, albumID, "Crash Season 1", 1, "TV", true)
	work2 := bindRunWork(t, store, albumID, "Crash Season 2", 2, "TV", true)
	request := RunRequest{Scope: "works", Force: true}
	run, err := store.CreateDurableEnrichmentRun(ctx, request.Scope, 0, true, phaseStages(request))
	if err != nil {
		t.Fatal(err)
	}
	items, err := manager.collectDurablePhaseStage(ctx, request, "works")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, "works", items); err != nil {
		t.Fatal(err)
	}
	for {
		item, claimErr := store.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, "works")
		if errors.Is(claimErr, sql.ErrNoRows) {
			break
		}
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		c := storage.EnrichmentCheckpoint{RunID: run.ID, ItemID: item.ID, Epoch: run.Epoch, Token: item.Token}
		if err = store.ApplyEnrichmentItem(ctx, c, "matched", nil); err != nil {
			t.Fatal(err)
		}
	}
	seriesItems, err := manager.collectDurablePhaseStage(ctx, request, "series")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, "series", seriesItems); err != nil {
		t.Fatal(err)
	}
	seriesItem, err := store.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, "series")
	if err != nil {
		t.Fatal(err)
	}
	c := storage.EnrichmentCheckpoint{RunID: run.ID, ItemID: seriesItem.ID, Epoch: run.Epoch, Token: seriesItem.Token}
	itemCtx := storage.WithEnrichmentCheckpoint(ctx, c)
	outcome, err := manager.enrichBangumiSeries(itemCtx, run.ID, true)
	if err != nil || outcome == "" {
		t.Fatalf("series outcome=%q err=%v", outcome, err)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series=%d", seriesCount(t, store))
	}
	if _, err = store.EnrichmentEffect(ctx, c, "series_members"); err != nil {
		t.Fatal("series member effect not recorded", err)
	}
	// Simulated crash: the item is still in_progress and the run is running.
	restarted := New(ctx, store, manager.logger, t.TempDir())
	restarted.phaseEndpoints = manager.phaseEndpoints
	restarted.bangumiInterval = 0
	restarted.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("resume re-requested %s", r.URL)
		return nil, fmt.Errorf("network forbidden on resume")
	})}
	if err = restarted.ResumeRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	done, err := store.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" || done.ID != run.ID || !done.Force {
		t.Fatalf("done=%+v", done)
	}
	if seriesCount(t, store) != 1 {
		t.Fatalf("series duplicated: %d", seriesCount(t, store))
	}
	if members := seriesMembersOf(t, store, work1); members[work2] != "auto" {
		t.Fatalf("members=%v", members)
	}
}

// P1-1: resuming a running run must fail fast with a state conflict instead
// of blocking on the worker's done channel; the paused join path still works.
func TestEnrichmentRuntimeResumeRunningRejectedImmediately(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m, s, _, work := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-r.Context().Done():
			return
		case <-release:
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	run, err := m.StartRun(ctx, RunRequest{Scope: "work", TargetID: work, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request not reached")
	}
	started := time.Now()
	if err = m.ResumeRun(ctx, run.ID); !errors.Is(err, storage.ErrEnrichmentRunState) {
		t.Fatalf("running resume err=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("resume blocked on running worker for %s", elapsed)
	}
	// Paused join path: pause while the request is in flight, then resume.
	if err = m.PauseRun(run.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err = m.ResumeRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	done, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" {
		t.Fatalf("done=%+v", done)
	}
	// Terminal and missing runs keep their distinct errors.
	if err = m.ResumeRun(ctx, run.ID); !errors.Is(err, storage.ErrEnrichmentRunState) {
		t.Fatalf("completed resume err=%v", err)
	}
	if err = m.ResumeRun(ctx, run.ID+999); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing resume err=%v", err)
	}
}

// P2-1: shutdown during a rate-limit wait pauses with reason "shutdown" (not
// a storage/runtime error) and keeps the elapsed wait history.
func TestEnrichmentRuntimeShutdownDuringWaitPausesWithShutdownReason(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	t.Cleanup(cancelBase)
	m, s, _, work := phase4TestManagerWithCtx(t, base, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(429)
	}))
	var mu sync.Mutex
	now := time.Now()
	m.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	entered := make(chan struct{})
	var once sync.Once
	m.sleep = func(ctx context.Context, d time.Duration) error {
		once.Do(func() { close(entered) })
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		<-ctx.Done()
		return ctx.Err()
	}
	run, err := m.StartRun(base, RunRequest{Scope: "work", TargetID: work, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("wait not reached")
	}
	cancelBase()
	m.Wait()
	paused, err := s.DurableEnrichmentRun(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != "paused" || paused.PauseReason != "shutdown" || paused.WaitTotalMS != int64(10*time.Minute/time.Millisecond) {
		t.Fatalf("paused=%+v", paused)
	}
}

// P2-1/P2-7: a manual cancel during a wait keeps the cancelled state and the
// interrupted elapsed is still charged to history.
func TestEnrichmentRuntimeCancelDuringWaitKeepsCancelledAndRecordsElapsed(t *testing.T) {
	m, s, _, work := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(429)
	}))
	var mu sync.Mutex
	now := time.Now()
	m.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	entered := make(chan struct{})
	var once sync.Once
	m.sleep = func(ctx context.Context, d time.Duration) error {
		once.Do(func() { close(entered) })
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		<-ctx.Done()
		return ctx.Err()
	}
	ctx := context.Background()
	run, err := m.StartRun(ctx, RunRequest{Scope: "work", TargetID: work, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("wait not reached")
	}
	if err = m.CancelRun(run.ID); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	cancelled, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" || cancelled.WaitTotalMS != int64(10*time.Minute/time.Millisecond) {
		t.Fatalf("cancelled=%+v", cancelled)
	}
	if err = m.ResumeRun(ctx, run.ID); !errors.Is(err, storage.ErrEnrichmentRunState) {
		t.Fatalf("cancelled resume err=%v", err)
	}
}

// P2-3: each claimed item publishes its title as current (epoch guarded); the
// series stage shows an explicit title during its single long item.
func TestEnrichmentRuntimePerItemCurrentAndStageTitles(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "Second") {
			once.Do(func() { close(entered) })
			select {
			case <-r.Context().Done():
				return
			case <-release:
			}
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	library := mustLibrary(t, s)
	for i, name := range []string{"First", "Second"} {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: "Anime", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, TrackType: "tv_size"}}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := m.StartRun(ctx, RunRequest{Scope: "tracks", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("second request not reached")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, _ := s.DurableEnrichmentRun(ctx, run.ID)
		if current.Stage == "tracks" && strings.Contains(current.Current, "Second") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("current=%+v", current)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	m.Wait()
	done, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" || done.Current != "" {
		t.Fatalf("done=%+v", done)
	}
	// Epoch/state guard: a terminal run rejects late current writes.
	if err = s.SetEnrichmentCurrent(ctx, run.ID, done.Epoch, "tracks", "late"); !errors.Is(err, storage.ErrEnrichmentRunState) {
		t.Fatalf("late current err=%v", err)
	}
}

// P2-3 (series): the single series item shows an explicit stage title.
func TestEnrichmentRuntimeSeriesStageCurrentTitle(t *testing.T) {
	server := syntheticSeriesGraphServer(t, chainEdges([][]int64{{1, 2}}), nil)
	defer server.Close()
	manager, store, albumID := seriesRunFixture(t, server)
	bindRunWork(t, store, albumID, "Current Season 1", 1, "TV", true)
	bindRunWork(t, store, albumID, "Current Season 2", 2, "TV", true)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	manager.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/subjects") {
			once.Do(func() { close(entered) })
			select {
			case <-r.Context().Done():
				return nil, r.Context().Err()
			case <-release:
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	ctx := context.Background()
	run, err := manager.StartRun(ctx, RunRequest{Scope: "works"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("series graph request not reached")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, _ := store.DurableEnrichmentRun(ctx, run.ID)
		if current.Stage == "series" && current.Current == "作品系列归组" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("current=%+v", current)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	manager.Wait()
	done, _ := store.DurableEnrichmentRun(ctx, run.ID)
	if done.Status != "completed" {
		t.Fatalf("done=%+v", done)
	}
}

// P2-5: in a durable run a poster rate limit after auto-confirm is a warning
// for the backfill pass, never a wait that drags the confirmed item into the
// rate-limit budget. The poster URL is a public IP literal so the SSRF guard
// passes and the shared cooldown gate fires before any network access.
func TestEnrichmentRuntimePosterFailureDoesNotPauseConfirmedWork(t *testing.T) {
	m, s, _, work := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// title 严格相等触发唯一匹配自动确认。
		io.WriteString(w, `{"data":[{"id":123,"type":2,"name":"葬送のフリーレン","name_cn":"","date":"2023-10-01","platform":"TV","images":{"large":"http://93.184.216.34/poster.jpg","common":""}}]}`)
	}))
	ctx := context.Background()
	// The poster source is throttled for ten minutes: downloadPublicImage hits
	// the shared cooldown before any request, so this never touches the network.
	m.blockSourceUntil("work poster", m.clockNow().Add(10*time.Minute))
	run, err := m.StartRun(ctx, RunRequest{Scope: "work", TargetID: work, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	done, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" || done.Succeeded != 1 || done.Failed != 0 || done.WaitTotalMS != 0 {
		t.Fatalf("done=%+v", done)
	}
	updated, err := s.WorkByID(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PosterURL == "" {
		t.Fatal("confirm did not store the poster URL")
	}
	// 海报未缓存：留给后续 backfill，不影响本 item 完成。
	if _, _, err = m.CachedWorkPoster(updated.PosterURL); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("poster cached unexpectedly: %v", err)
	}
}

// P2-4: a completed run keeps the last ordinary failure visible.
func TestEnrichmentRuntimeCompletedRunKeepsLastFailure(t *testing.T) {
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "Beta") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	library := mustLibrary(t, s)
	for i, name := range []string{"Alpha", "Beta", "Gamma"} {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: "Anime", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, TrackType: "tv_size"}}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := m.StartRun(ctx, RunRequest{Scope: "tracks", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	done, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "completed" || done.Failed != 1 || done.Processed != 4 || done.ErrorMessage == "" {
		t.Fatalf("done=%+v", done)
	}
}

// P2-4/P2-7: five consecutive ordinary failures end the run as failed with a
// visible Chinese error message.
func TestEnrichmentRuntimeFiveConsecutiveFailuresFailRun(t *testing.T) {
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	ctx := context.Background()
	library := mustLibrary(t, s)
	for i, name := range []string{"F1", "F2", "F3", "F4", "F5", "F6"} {
		if err := s.ImportTrack(ctx, storage.ImportInput{LibraryID: library, RelativePath: name + "/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: "Anime", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, TrackType: "tv_size"}}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := m.StartRun(ctx, RunRequest{Scope: "tracks", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	done, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != "failed" || done.Failed != 5 || !strings.Contains(done.ErrorMessage, "连续五次") {
		t.Fatalf("done=%+v", done)
	}
}

// P2-6: StartAuto with a paused durable run skips without side effects; the
// paused state is untouched and discoverable in logs.
func TestEnrichmentRuntimeStartAutoSkipsPausedRun(t *testing.T) {
	m, s, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	run, err := s.CreateDurableEnrichmentRun(ctx, "all", 0, false, phaseStages(RunRequest{Scope: "all"}))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.TransitionEnrichmentRun(ctx, run.ID, "pause", "server_restart"); err != nil {
		t.Fatal(err)
	}
	m.StartAuto(ctx)
	m.Wait()
	runs, err := s.ListEnrichmentRuns(ctx, 10, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	still, err := s.DurableEnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != "paused" {
		t.Fatalf("still=%+v", still)
	}
}
