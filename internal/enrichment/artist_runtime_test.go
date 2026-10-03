package enrichment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func artistFakeClock(m *Manager) {
	var mu sync.Mutex
	now := time.Now()
	m.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	m.sleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		return nil
	}
}
func TestArtistRuntimeRateLimitWaitResumeBudget(t *testing.T) {
	m, artist := independentArtistManager(t, false)
	artistFakeClock(m)
	calls := 0
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls <= 3 {
			return cannedResponse(429, http.Header{"Retry-After": {"60"}}, `{}`), nil
		}
		return cannedResponse(200, http.Header{}, `{"id":"`+safetyMBID+`","name":"ACE+","relations":[]}`), nil
	})
	run, err := m.StartAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	state, err := m.store.DurableArtistRun(context.Background(), run)
	if err != nil || state.Status != "paused" || state.Processed != 0 || calls != 3 || state.WaitTotalMS != 120000 {
		t.Fatal(state, calls, err)
	}
	if err = m.ResumeArtistMatching(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	state, err = m.store.DurableArtistRun(context.Background(), run)
	if err != nil || state.Status != "completed" || state.Matched != 1 || state.WaitTotalMS < 180000 || state.BudgetBaselineMS != 120000 {
		t.Fatal(state, err)
	}
	if _, err = m.store.ArtistExternalID(context.Background(), artist, "musicbrainz"); err != nil {
		t.Fatal(err)
	}
}
func TestArtistRuntimeRestartMatchedFactAndDeletedSkip(t *testing.T) {
	m, artist := independentArtistManager(t, false)
	ctx := context.Background()
	input, _ := m.store.ArtistForMatching(ctx, artist)
	run, err := m.store.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: input}, {Artist: storage.ArtistMatchInput{ID: 999999}}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := m.store.ClaimArtistRunItem(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.store.ReplaceArtistCandidates(ctx, artist, []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "ACE+", Score: 100}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.store.AutoBindArtistRunCandidate(ctx, artist, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "ACE+"}, storage.ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}); err != nil || !ok {
		t.Fatal(ok, err)
	}
	restarted := New(ctx, m.store, m.logger, t.TempDir())
	requests := 0
	restarted.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { requests++; return nil, errors.New("network forbidden") })}
	state, _ := m.store.DurableArtistRun(ctx, run)
	if state.Status != "paused" || requests != 0 {
		t.Fatal(state)
	}
	if _, err = restarted.StartAll(ctx); err == nil {
		t.Fatal("paused bypassed")
	}
	if err = restarted.ResumeArtistMatching(ctx, run); err != nil {
		t.Fatal(err)
	}
	restarted.Wait()
	state, _ = m.store.DurableArtistRun(ctx, run)
	if state.Status != "completed" || state.Matched != 1 || state.Skipped != 1 || requests != 0 {
		t.Fatal(state, requests)
	}
}
func TestArtistRuntimePauseCancelInterruptWait(t *testing.T) {
	for _, action := range []string{"pause", "cancel"} {
		t.Run(action, func(t *testing.T) {
			m, _ := independentArtistManager(t, false)
			entered := make(chan struct{})
			m.sleep = func(ctx context.Context, d time.Duration) error { close(entered); <-ctx.Done(); return ctx.Err() }
			m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return cannedResponse(429, http.Header{"Retry-After": {"60"}}, `{}`), nil
			})
			run, err := m.StartAll(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			if action == "pause" {
				err = m.PauseArtistMatching(run)
			} else {
				err = m.CancelArtistMatching(run)
			}
			if err != nil {
				t.Fatal(err)
			}
			m.Wait()
			state, _ := m.store.DurableArtistRun(context.Background(), run)
			expected := "paused"
			if action == "cancel" {
				expected = "cancelled"
			}
			if state.Status != expected || state.Processed != 0 {
				t.Fatal(state)
			}
			if action == "cancel" {
				if err = m.ResumeArtistMatching(context.Background(), run); err == nil {
					t.Fatal("cancel resumed")
				}
			}
		})
	}
}

func TestArtistRuntimeSharedExtensionAndWaitBudget(t *testing.T) {
	m, _ := independentArtistManager(t, false)
	artistFakeClock(m)
	baseSleep := m.sleep
	extended := false
	m.sleep = func(ctx context.Context, d time.Duration) error {
		if !extended {
			extended = true
			m.blockSourceUntil("musicbrainz", m.clockNow().Add(40*time.Minute))
		}
		return baseSleep(ctx, d)
	}
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(429, http.Header{"Retry-After": {"60"}}, `{}`), nil
	})
	run, err := m.StartAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	state, _ := m.store.DurableArtistRun(context.Background(), run)
	if state.Status != "paused" || state.PauseReason != "rate_limit_wait_budget" || state.WaitTotalMS != 1800000 {
		t.Fatal(state)
	}
	var count int
	db := writableSafetyDB(t, m)
	if err := db.QueryRow(`SELECT rate_limit_count FROM artist_match_run_items WHERE run_id=?`, run).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
func TestArtistRuntimeCheckpointFailureStopsMoreRequests(t *testing.T) {
	m, _ := independentArtistManager(t, false)
	db := writableSafetyDB(t, m)
	if _, err := db.Exec(`CREATE TRIGGER fail_checkpoint BEFORE INSERT ON artist_match_item_sources BEGIN SELECT RAISE(ABORT,'checkpoint fail'); END`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return cannedResponse(200, http.Header{}, `{"id":"`+safetyMBID+`","name":"ACE+"}`), nil
	})
	run, err := m.StartAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	state, _ := m.store.DurableArtistRun(context.Background(), run)
	if state.Status != "paused" || state.Processed != 0 || calls != 1 {
		t.Fatal(state, calls)
	}
}

func TestArtistRuntimeResumeJoinsOldLoop(t *testing.T) {
	m, _ := independentArtistManager(t, false)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	m.sleep = func(ctx context.Context, d time.Duration) error {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		<-release
		return ctx.Err()
	}
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(429, http.Header{"Retry-After": {"60"}}, `{}`), nil
	})
	run, err := m.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err = m.PauseArtistMatching(run); err != nil {
		t.Fatal(err)
	}
	resumed := make(chan error, 1)
	cancelCtx, cancel := context.WithCancel(ctx)
	go func() { resumed <- m.ResumeArtistMatching(cancelCtx, run) }()
	cancel()
	if err = <-resumed; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, _ := m.store.DurableArtistRun(ctx, run)
	if state.Status != "paused" {
		t.Fatal("resume transitioned before old loop joined", state)
	}
	close(release)
	m.Wait()
}

func TestArtistRuntimeWaitPersistenceFailurePauses(t *testing.T) {
	m, _ := independentArtistManager(t, false)
	db := writableSafetyDB(t, m)
	if _, err := db.Exec(`CREATE TRIGGER fail_wait BEFORE UPDATE OF wait_total_ms ON artist_match_runs BEGIN SELECT RAISE(ABORT,'wait persistence failed'); END`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return cannedResponse(429, http.Header{}, `{}`), nil
	})
	run, err := m.StartAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	state, _ := m.store.DurableArtistRun(context.Background(), run)
	if state.Status != "paused" || calls != 1 || state.Processed != 0 {
		t.Fatal(state, calls)
	}
}

func TestArtistRuntimeSavedSourcesResumeOutcomes(t *testing.T) {
	for _, outcome := range []string{"matched", "review", "no_result"} {
		t.Run(outcome, func(t *testing.T) {
			m, artist := independentArtistManager(t, true)
			ctx := context.Background()
			input, _ := m.store.ArtistForMatching(ctx, artist)
			input, err := m.store.ArtistMatchQueryContext(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			run, err := m.store.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: input}})
			if err != nil {
				t.Fatal(err)
			}
			item, err := m.store.ClaimArtistRunItem(ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			c := storage.ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}
			for _, source := range []string{"musicbrainz", "lastfm"} {
				setting, _ := m.store.MetadataSourceSetting(ctx, source)
				v := storage.ArtistSourceSnapshot{}
				if outcome != "no_result" {
					name := "ACE+"
					id := safetyMBID
					if outcome == "review" && source == "lastfm" {
						id = "different"
					}
					v.Candidates = []storage.ArtistCandidate{{Source: source, ExternalID: id, MBID: id, DisplayName: name, Score: 85}}
					v.Profiles = map[string]storage.ExternalArtistProfile{id: {Source: source, ExternalID: id, DisplayName: name}}
				}
				if source == "lastfm" {
					independent := true
					v.Independent = &independent
				}
				if err = m.store.SaveArtistRunSourceCheck(ctx, input, setting, v, c); err != nil {
					t.Fatal(err)
				}
			}
			restarted := New(ctx, m.store, m.logger, t.TempDir())
			requests := 0
			restarted.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				return nil, errors.New("unexpected recovery network")
			})}
			if err = restarted.ResumeArtistMatching(ctx, run); err != nil {
				t.Fatal(err)
			}
			restarted.Wait()
			r, _ := m.store.DurableArtistRun(ctx, run)
			if r.Status != "completed" || r.Processed != 1 || requests != 0 || outcome == "matched" && r.Matched != 1 || outcome == "review" && r.Review != 1 || outcome == "no_result" && r.NoResult != 1 {
				t.Fatalf("%s: %+v requests=%d", outcome, r, requests)
			}
		})
	}
}

func TestArtistRuntimeDetailsRateLimitAfterSourcesStillMatches(t *testing.T) {
	m, artist := independentArtistManager(t, true)
	ctx := context.Background()
	db := writableSafetyDB(t, m)
	if _, err := db.Exec(`DELETE FROM audio_file_tags`); err != nil {
		t.Fatal(err)
	}
	artistFakeClock(m)
	searches, lastfm, details := 0, 0, 0
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "ws.audioscrobbler.com" {
			lastfm++
			return cannedResponse(200, http.Header{}, `{"artist":{"name":"ACE+","mbid":"`+safetyMBID+`"}}`), nil
		}
		if r.URL.Query().Get("query") != "" {
			searches++
			return cannedResponse(200, http.Header{}, `{"artists":[{"id":"`+safetyMBID+`","name":"ACE+","score":85}]}`), nil
		}
		details++
		return cannedResponse(429, http.Header{"Retry-After": {"60"}}, `{}`), nil
	})
	run, err := m.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	r, _ := m.store.DurableArtistRun(ctx, run)
	if r.Status != "completed" || r.Matched != 1 || searches != 1 || lastfm != 1 || details != 1 {
		t.Fatal(r, searches, lastfm, details)
	}
	if id, err := m.store.ArtistExternalID(ctx, artist, "musicbrainz"); err != nil || id != safetyMBID {
		t.Fatal(id, err)
	}
}

func TestArtistRuntimePausedDetailsRestoresWithoutHTTP(t *testing.T) {
	m, _ := independentArtistManager(t, true)
	ctx := context.Background()
	db := writableSafetyDB(t, m)
	if _, err := db.Exec(`DELETE FROM audio_file_tags`); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "ws.audioscrobbler.com" {
			return cannedResponse(200, http.Header{}, `{"artist":{"name":"ACE+","mbid":"`+safetyMBID+`"}}`), nil
		}
		if r.URL.Query().Get("query") != "" {
			return cannedResponse(200, http.Header{}, `{"artists":[{"id":"`+safetyMBID+`","name":"ACE+","score":85}]}`), nil
		}
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	run, err := m.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err = m.PauseArtistMatching(run); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	requests := 0
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("resume request forbidden")
	})
	if err = m.ResumeArtistMatching(ctx, run); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	r, _ := m.store.DurableArtistRun(ctx, run)
	if r.Status != "completed" || r.Matched != 1 || requests != 0 {
		t.Fatal(r, requests)
	}
}

func TestArtistRuntimeStableGlobalReviewNotRequeried(t *testing.T) {
	m, artist := independentArtistManager(t, true)
	ctx := context.Background()
	input, _ := m.store.ArtistForMatching(ctx, artist)
	setting, _ := m.store.MetadataSourceSetting(ctx, "musicbrainz")
	snapshot := storage.ArtistSourceSnapshot{Candidates: []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: "review", MBID: "review", DisplayName: "Review", Score: 85}}}
	if err := m.store.SaveArtistSourceCheck(ctx, input, setting, snapshot); err != nil {
		t.Fatal(err)
	}
	db := writableSafetyDB(t, m)
	if _, err := db.Exec(`UPDATE artist_source_match_state SET checked_at='2000-01-01T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	old, _ := m.store.ArtistCandidates(ctx, artist)
	mbRequests := 0
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "ws.audioscrobbler.com" {
			mbRequests++
			return nil, errors.New("stable review must not requery")
		}
		return cannedResponse(200, http.Header{}, `{"error":6,"message":"not found"}`), nil
	})
	run, err := m.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	state, _ := m.store.DurableArtistRun(ctx, run)
	current, _ := m.store.ArtistCandidates(ctx, artist)
	if state.Status != "completed" || mbRequests != 0 || len(current) != 1 || current[0].ID != old[0].ID {
		t.Fatal(state, mbRequests, current)
	}
}

func TestArtistRuntimeRecoveredSourceSurvivesOtherFailure(t *testing.T) {
	for _, score := range []int{100, 85} {
		t.Run(fmt.Sprint(score), func(t *testing.T) {
			m, artist := independentArtistManager(t, true)
			ctx := context.Background()
			input, _ := m.store.ArtistForMatching(ctx, artist)
			run, err := m.store.CreateDurableArtistRun(ctx, []storage.ArtistRunItemInput{{Artist: input}})
			if err != nil {
				t.Fatal(err)
			}
			item, err := m.store.ClaimArtistRunItem(ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			setting, _ := m.store.MetadataSourceSetting(ctx, "musicbrainz")
			snapshot := storage.ArtistSourceSnapshot{Candidates: []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: safetyMBID, MBID: safetyMBID, DisplayName: "ACE+", Score: score}}, Profiles: map[string]storage.ExternalArtistProfile{safetyMBID: {Source: "musicbrainz", ExternalID: safetyMBID, DisplayName: "ACE+"}}}
			if err = m.store.SaveArtistRunSourceCheck(ctx, input, setting, snapshot, storage.ArtistRunCheckpoint{RunID: run, ItemID: item.ID, ClaimToken: item.ClaimToken}); err != nil {
				t.Fatal(err)
			}
			resumed := New(ctx, m.store, m.logger, t.TempDir())
			mbRequests := 0
			resumed.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "ws.audioscrobbler.com" {
					mbRequests++
				}
				return cannedResponse(500, http.Header{}, `{}`), nil
			})}
			if err = resumed.ResumeArtistMatching(ctx, run); err != nil {
				t.Fatal(err)
			}
			resumed.Wait()
			state, _ := m.store.DurableArtistRun(ctx, run)
			if state.Status != "completed" || state.Failed != 0 || mbRequests != 0 || score == 100 && state.Matched != 1 || score == 85 && state.Review != 1 {
				t.Fatal(state, mbRequests)
			}
		})
	}
}
