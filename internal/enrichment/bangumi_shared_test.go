package enrichment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/bangumi"
	"github.com/lux032/032music-server/internal/sourcegate"
)

func TestBangumiSharedFetchCacheAndErrors(t *testing.T) {
	for _, status := range []int{200, 404, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var requests atomic.Int32
			m, _, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(status)
				if r.URL.Path == "/v0/subjects/7/subjects" {
					fmt.Fprint(w, `[{"id":8,"type":3,"name":"song","name_cn":"歌","relation":"片头曲"}]`)
				} else {
					fmt.Fprint(w, `{"id":7,"name":"song","name_cn":"歌","date":"2020-01-02","infobox":[{"key":"艺术家","value":[{"v":"LiSA"}]},{"key":"演唱","value":"singer"}]}`)
				}
			}))
			// Both exported APIs use the same keys as enrichment.
			for _, relations := range []bool{false, true} {
				// A fresh gate isolates the two endpoint error cases, not their cache.
				m.SetSourceGate(sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"bangumi": 0, "musicbrainz": 0}}))
				before := requests.Load()
				for i := 0; i < 2; i++ {
					var err error
					if relations {
						var result []bangumi.Relation
						result, err = m.BangumiRelations(context.Background(), 7)
						if status == 200 && (len(result) != 1 || result[0].Relation != "片头曲") {
							t.Fatal(result)
						}
					} else {
						var result bangumi.MusicSubject
						result, err = m.BangumiMusicSubject(context.Background(), 7)
						if status == 200 && (result.ID != 7 || len(result.Artists) != 2 || result.Date != "2020-01-02") {
							t.Fatal(result)
						}
					}
					switch status {
					case 200:
						if err != nil {
							t.Fatal(err)
						}
					case 404:
						if !errors.Is(err, bangumi.ErrGone) {
							t.Fatal(err)
						}
					case 429:
						var cd *sourcegate.CooldownError
						if !errors.As(err, &cd) || cd.Source != "bangumi" || cd.RetryAfter <= 0 {
							t.Fatal(err)
						}
					}
				}
				if requests.Load()-before != 1 {
					t.Fatalf("requests=%d; cache/cooldown missed", requests.Load()-before)
				}
			}
		})
	}
}

func TestBangumiSharedGateSpacingCooldownAndRejectedGate(t *testing.T) {
	m, _, _, _ := phase4TestManager(t, http.NotFoundHandler())
	now := time.Now()
	var waits []time.Duration
	g := sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"bangumi": time.Second, "musicbrainz": 0}, Now: func() time.Time { return now }, Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); now = now.Add(d); return nil }})
	m.SetSourceGate(g)
	m.now = func() time.Time { return now }
	m.SetSourceGate(sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0}}))
	if m.gate != g {
		t.Fatal("accepted gate without bangumi")
	}
	if err := m.waitBangumiRateLimit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := g.Wait(context.Background(), "bangumi"); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != time.Second {
		t.Fatal(waits)
	}
	m.NoteRateLimited("bangumi", time.Minute)
	var cd *sourcegate.CooldownError
	if err := g.Wait(context.Background(), "bangumi"); !errors.As(err, &cd) {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	g.Block("bangumi", now.Add(time.Second))
	if err := m.waitBangumiRateLimit(context.Background()); asRateLimited(err) == nil {
		t.Fatal(err)
	}
}

func TestBangumiBaseURLEnv(t *testing.T) {
	t.Setenv("MUSIC_SERVER_BANGUMI_BASE_URL", "http://127.0.0.1:12345/mirror/")
	m, _, _, _ := phase4TestManager(t, http.NotFoundHandler())
	// phase4TestManager overrides endpoints after New; create another manager on
	// the same store to exercise environment construction itself.
	fresh := newManagedTestManager(t, context.Background(), m.store, m.logger, t.TempDir())
	if fresh.phaseEndpoints.BangumiAPI != "http://127.0.0.1:12345/mirror" || fresh.phaseEndpoints.Bangumi != "http://127.0.0.1:12345/mirror/v0/search/subjects" {
		t.Fatal(fresh.phaseEndpoints)
	}
	fresh.SetBangumiBaseURL("")
	if fresh.phaseEndpoints.BangumiAPI != "https://api.bgm.tv" || fresh.phaseEndpoints.Bangumi != "https://api.bgm.tv/v0/search/subjects" {
		t.Fatal(fresh.phaseEndpoints)
	}
	fresh.SetBangumiBaseURL("http://localhost:4567/")
	if fresh.phaseEndpoints != bangumiEndpoints("http://localhost:4567") {
		t.Fatal(fresh.phaseEndpoints)
	}
}

func TestBangumiSharedFetchConcurrentRunMemo(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	// Hold the first subject responses until every shared worker is fetching,
	// so removing the shared-fetch guard exposes concurrent memo writes.
	var firstSubjects sync.WaitGroup
	firstSubjects.Add(8)
	var subjectRequests atomic.Int32
	var once sync.Once
	m, _, _, id := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bangumi" {
			once.Do(func() { close(entered) })
			<-release
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		if r.URL.Path == "/v0/subjects/7/subjects" {
			fmt.Fprint(w, `[{"id":8,"type":3,"relation":"片头曲"}]`)
			return
		}
		if subjectRequests.Add(1) <= 8 {
			firstSubjects.Done()
			firstSubjects.Wait()
		}
		fmt.Fprint(w, `{"id":7,"name":"song"}`)
	}))
	m.setBangumiIntervalForTest(0)
	run, err := m.store.CreateEnrichmentRun(context.Background(), "work", id, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.executePhase4Run(context.Background(), run.ID, RunRequest{Scope: "work", TargetID: id, Force: true})
	}()
	defer func() { close(release); <-done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not reach fetch")
	}
	if m.runMemo == nil {
		t.Fatal("run memo was not initialized")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := m.BangumiMusicSubject(context.Background(), 7); err != nil {
					t.Error(err)
				}
				if _, err := m.BangumiRelations(context.Background(), 7); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	// The run is still blocked in its search request, so inspecting its memo
	// here cannot overlap with enrichment writes or the deferred reset.
	for _, key := range []string{"bangumi\x00subject:7", "bangumi\x00subject-rel:7"} {
		if m.runMemo[key] {
			t.Errorf("shared fetch wrote run memo key %q", key)
		}
	}
}
