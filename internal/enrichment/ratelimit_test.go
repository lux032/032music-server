package enrichment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"seconds", "120", 120 * time.Second},
		{"http date", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"missing", "", defaultRateLimitBackoff},
		{"invalid", "soon", defaultRateLimitBackoff},
		{"capped", "99999", maxRateLimitBackoff},
		{"huge within int64", "99999999999", maxRateLimitBackoff},
		{"int64 max", "9223372036854775807", maxRateLimitBackoff},
		{"beyond int64", "99999999999999999999999999999", maxRateLimitBackoff},
		{"negative seconds", "-5", 0},
		{"past date", now.Add(-time.Minute).Format(http.TimeFormat), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryAfter(tc.header, now)
			// HTTP dates only have second resolution; allow a small delta.
			if diff := got - tc.want; diff < -time.Second || diff > time.Second {
				t.Fatalf("parseRetryAfter(%q)=%s, want %s", tc.header, got, tc.want)
			}
		})
	}
}

func TestParseBangumiIntervalMS(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{"500", 500 * time.Millisecond, true},
		{"200", 200 * time.Millisecond, true},
		{"10000", 10 * time.Second, true},
		{"199", 0, false},
		{"10001", 0, false},
		{"abc", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseBangumiIntervalMS(tc.value)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("parseBangumiIntervalMS(%q)=(%s,%v), want (%s,%v)", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBangumiIntervalFromEnv(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Run("default 500ms", func(t *testing.T) {
		t.Setenv("MUSIC_SERVER_BANGUMI_INTERVAL_MS", "")
		if got := bangumiIntervalFromEnv(logger); got != 500*time.Millisecond {
			t.Fatalf("default interval=%s", got)
		}
	})
	t.Run("legal value", func(t *testing.T) {
		t.Setenv("MUSIC_SERVER_BANGUMI_INTERVAL_MS", "750")
		if got := bangumiIntervalFromEnv(logger); got != 750*time.Millisecond {
			t.Fatalf("interval=%s", got)
		}
	})
	t.Run("out of range falls back", func(t *testing.T) {
		t.Setenv("MUSIC_SERVER_BANGUMI_INTERVAL_MS", "100")
		if got := bangumiIntervalFromEnv(logger); got != 500*time.Millisecond {
			t.Fatalf("interval=%s", got)
		}
	})
	t.Run("invalid falls back", func(t *testing.T) {
		t.Setenv("MUSIC_SERVER_BANGUMI_INTERVAL_MS", "fast")
		if got := bangumiIntervalFromEnv(logger); got != 500*time.Millisecond {
			t.Fatalf("interval=%s", got)
		}
	})
}

func TestNewManagerBangumiIntervalFromEnv(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "env.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { delete(phase4DBPath, store); _ = store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MUSIC_SERVER_BANGUMI_INTERVAL_MS", "800")
	manager := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	if manager.bangumiInterval != 800*time.Millisecond {
		t.Fatalf("interval=%s", manager.bangumiInterval)
	}
}

func TestCachedJSONRateLimited(t *testing.T) {
	var requests atomic.Int32
	manager, store, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	ctx := context.Background()
	setting, err := store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	var target map[string]any
	_, err = manager.cachedJSON(ctx, "bangumi", "rl-key", manager.phaseEndpoints.Bangumi, setting, true, nil, &target)
	rateLimited := asRateLimited(err)
	if rateLimited == nil || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err=%v, want ErrRateLimited", err)
	}
	if rateLimited.Source != "bangumi" || rateLimited.RetryAfter != 120*time.Second || rateLimited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rateLimited=%+v", rateLimited)
	}
	// A 429 response must never be written to the HTTP cache.
	if _, cacheErr := store.GetHTTPResponseCache(ctx, "bangumi", "rl-key"); cacheErr == nil {
		t.Fatal("429 response was cached")
	}
	// During the backoff the next request is rejected immediately: the wait
	// must return ErrRateLimited without sleeping (holding the throttle lock
	// through a long backoff would be uncancellable).
	manager.sleep = func(ctx context.Context, d time.Duration) error {
		t.Errorf("sleep(%s) called during rate-limit backoff", d)
		return nil
	}
	start := time.Now()
	err = manager.waitBangumiRateLimit(ctx)
	backoff := asRateLimited(err)
	if backoff == nil || backoff.Source != "bangumi" {
		t.Fatalf("backoff wait err=%v, want bangumi ErrRateLimited", err)
	}
	if backoff.RetryAfter <= 115*time.Second || backoff.RetryAfter > 120*time.Second {
		t.Fatalf("backoff RetryAfter=%s, want the remaining ~120s", backoff.RetryAfter)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("backoff wait did not return immediately")
	}
	// A cancelled context aborts the ordinary interval wait immediately.
	manager.bangumiMu.Lock()
	manager.bangumiBlockedUntil = time.Time{}
	manager.bangumiLast = time.Now()
	manager.bangumiMu.Unlock()
	manager.bangumiInterval = 30 * time.Second
	manager.sleep = sleepContext
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	start = time.Now()
	if err = manager.waitBangumiRateLimit(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait err=%v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancelled interval wait did not return immediately")
	}
}

func TestWaitRateLimitReturnsImmediatelyDuringBackoff(t *testing.T) {
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusOK, http.Header{}, "{}"), nil
	}))
	manager.NoteRateLimited("musicbrainz", 5*time.Minute)
	manager.sleep = func(ctx context.Context, d time.Duration) error {
		t.Errorf("sleep(%s) called during rate-limit backoff", d)
		return nil
	}
	start := time.Now()
	err := manager.waitMBRateLimit(context.Background())
	rateLimited := asRateLimited(err)
	if rateLimited == nil || rateLimited.Source != "musicbrainz" {
		t.Fatalf("err=%v, want musicbrainz ErrRateLimited", err)
	}
	if rateLimited.RetryAfter <= 4*time.Minute || rateLimited.RetryAfter > 5*time.Minute {
		t.Fatalf("RetryAfter=%s, want the remaining ~5m", rateLimited.RetryAfter)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("backoff wait did not return immediately")
	}
}

func TestCachedJSONServiceUnavailableRetryAfter(t *testing.T) {
	t.Run("503 with Retry-After backs off", func(t *testing.T) {
		manager, store, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		ctx := context.Background()
		setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
		var target map[string]any
		_, err := manager.cachedJSON(ctx, "bangumi", "rl-503", manager.phaseEndpoints.Bangumi, setting, true, nil, &target)
		if rateLimited := asRateLimited(err); rateLimited == nil || rateLimited.RetryAfter != 30*time.Second {
			t.Fatalf("503+Retry-After err=%v", err)
		}
	})
	t.Run("503 without Retry-After is an ordinary failure", func(t *testing.T) {
		manager, store, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		ctx := context.Background()
		setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
		var target map[string]any
		_, err := manager.cachedJSON(ctx, "bangumi", "rl-503b", manager.phaseEndpoints.Bangumi, setting, true, nil, &target)
		if asRateLimited(err) != nil || err == nil {
			t.Fatalf("503 without Retry-After err=%v", err)
		}
	})
}

func TestPhase4RunStopsOnRateLimit(t *testing.T) {
	var requests atomic.Int32
	manager, store, albumID, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	ctx := context.Background()
	// Extra works prove the run stops instead of continuing with other items.
	for i := 0; i < 3; i++ {
		created, err := store.CreateWork(ctx, storage.WorkInput{Title: fmt.Sprintf("Rate %d", i), Type: "anime"})
		if err != nil {
			t.Fatal(err)
		}
		if err = store.AddWorkAlbum(ctx, created.ID, albumID, "other"); err != nil {
			t.Fatal(err)
		}
	}
	artistFakeClock(manager)
	run, err := manager.StartRun(ctx, RunRequest{Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	finished, _ := store.DurableEnrichmentRun(ctx, run.ID)
	if finished.Status != "paused" || finished.PauseReason != "rate_limit_exhausted" || finished.AutoResumeCount != 3 {
		t.Fatalf("run=%+v", finished)
	}
	if finished.Failed != 0 {
		t.Fatalf("rate limiting must not count as ordinary failure: run=%+v", finished)
	}
	if got := int(requests.Load()); got != 12 {
		t.Fatalf("requests=%d, want 12 (three responses per round across the initial round and three auto resumes)", got)
	}
}

func TestArtistMatchStopsOnRateLimit(t *testing.T) {
	var requests atomic.Int32
	manager, store, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	ctx := context.Background()
	// 第二位艺术家在 MB 退避期内本来就会被立即拒绝（发不出请求），所以
	// requests==1 不是本测试的区分点；真正起区分作用的是下面 run 的
	// status=failed 与中文限流消息（吞错时会 failed++ 继续跑完整个队列，
	// 状态与消息都不同）。
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Other/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Other", Artists: []string{"Second Artist"}, AlbumArtists: []string{"Second Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	if artists, listErr := store.ArtistsForMatching(ctx); listErr != nil || len(artists) < 2 {
		t.Fatalf("artists=%v err=%v, want at least 2", artists, listErr)
	}
	setting, err := store.MetadataSourceSetting(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	artistFakeClock(manager)
	runID, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	// 同一对象连续限流后自动退避恢复；自动轮次用尽后停在 rate_limit_exhausted。
	finished, err := store.DurableArtistRun(ctx, runID)
	if err != nil || finished.Status != "paused" || finished.PauseReason != "rate_limit_exhausted" || finished.AutoResumeCount != 3 || finished.Failed != 0 {
		t.Fatalf("run=%+v err=%v", finished, err)
	}
	if got := int(requests.Load()); got != 12 {
		t.Fatalf("requests=%d, want 12 (three responses per round across the initial round and three auto resumes)", got)
	}
}

func TestSeriesGroupingStopsOnRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	manager, _ := newSeriesManager(t, server)
	ctx := context.Background()
	store := manager.store
	bindSeriesWork(t, store, "Season 1", "anime", 2023, 1, "2023-01-01")
	_, err := manager.enrichBangumiSeries(ctx, 1, true)
	rateLimited := asRateLimited(err)
	if rateLimited == nil || rateLimited.Source != "bangumi" {
		t.Fatalf("err=%v, want bangumi ErrRateLimited", err)
	}
	if strings.Contains(err.Error(), "consecutive failures") {
		t.Fatalf("rate limiting must not trip the circuit breaker: %v", err)
	}
}

func TestUserAgentFallback(t *testing.T) {
	setting := storage.MetadataSourceSetting{ApplicationName: "MyApp", ApplicationVersion: "1.2", Contact: "me@example.com"}
	if got := userAgent(setting); got != "MyApp/1.2 (me@example.com)" {
		t.Fatalf("ua=%q", got)
	}
	got := userAgent(storage.MetadataSourceSetting{})
	if !strings.Contains(got, defaultContactURL) || !strings.HasPrefix(got, "032-Music-Server/dev (") {
		t.Fatalf("fallback ua=%q", got)
	}
	// The seeded default name contains spaces; they become dashes.
	if got = userAgent(storage.MetadataSourceSetting{ApplicationName: "032 Music Server", ApplicationVersion: "dev"}); !strings.HasPrefix(got, "032-Music-Server/dev (") {
		t.Fatalf("default-name ua=%q", got)
	}
	if got = userAgent(storage.MetadataSourceSetting{ApplicationName: "My  App", ApplicationVersion: "1 2", Contact: "c"}); !strings.HasPrefix(got, "My-App/1-2 (") {
		t.Fatalf("whitespace ua=%q", got)
	}
	// Control characters in the contact can never reach the header.
	got = userAgent(storage.MetadataSourceSetting{Contact: "me@example.com\r\nX-Injected: yes"})
	if strings.ContainsAny(got, "\r\n") || !strings.Contains(got, "me@example.com") {
		t.Fatalf("control-char ua=%q", got)
	}
	if got = userAgent(storage.MetadataSourceSetting{Contact: " \t\n "}); !strings.Contains(got, defaultContactURL) {
		t.Fatalf("blank-contact ua=%q", got)
	}
}

func TestCachedJSONUserAgent(t *testing.T) {
	uas := make(chan string, 4)
	manager, store, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas <- r.Header.Get("User-Agent")
		io.WriteString(w, `{"data":[]}`)
	}))
	ctx := context.Background()
	setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
	setting.ApplicationName = "MyApp"
	setting.ApplicationVersion = "1.2"
	setting.Contact = "me@example.com"
	var target map[string]any
	if _, err := manager.cachedJSON(ctx, "bangumi", "ua-1", manager.phaseEndpoints.Bangumi, setting, true, nil, &target); err != nil {
		t.Fatal(err)
	}
	if got := <-uas; got != "MyApp/1.2 (me@example.com)" {
		t.Fatalf("ua=%q", got)
	}
	// Empty contact falls back to the project repository, never a placeholder.
	setting.Contact = ""
	if _, err := manager.cachedJSON(ctx, "bangumi", "ua-2", manager.phaseEndpoints.Bangumi, setting, true, nil, &target); err != nil {
		t.Fatal(err)
	}
	if got := <-uas; !strings.Contains(got, defaultContactURL) {
		t.Fatalf("fallback ua=%q", got)
	}
}

func TestMusicBrainzRequestUserAgent(t *testing.T) {
	uas := make(chan string, 1)
	manager, store, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uas <- r.Header.Get("User-Agent")
		io.WriteString(w, `{"artists":[]}`)
	}))
	ctx := context.Background()
	setting, _ := store.MetadataSourceSetting(ctx, "musicbrainz")
	setting.Contact = "mb@example.com"
	if _, _, err := manager.musicBrainzSearch(ctx, storage.ArtistMatchInput{ID: 1, Name: "Artist"}, setting); err != nil {
		t.Fatal(err)
	}
	if got := <-uas; !strings.Contains(got, "(mb@example.com)") {
		t.Fatalf("ua=%q", got)
	}
}

// transportManager builds a manager whose HTTP client is intercepted by
// transport, so requests to hardcoded external hosts stay inside the test.
func transportManager(t *testing.T, transport http.RoundTripper) *Manager {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "ua.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	phase4DBPath[store] = dbPath
	t.Cleanup(func() { delete(phase4DBPath, store); _ = store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	manager := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.client = &http.Client{Transport: transport}
	return manager
}

func cannedResponse(status int, header http.Header, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d status", status), Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

func TestWikidataUserAgentAndRateLimit(t *testing.T) {
	var capturedUA string
	var status atomic.Int32
	status.Store(http.StatusTooManyRequests)
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		capturedUA = r.Header.Get("User-Agent")
		header := http.Header{"Retry-After": {"30"}}
		return cannedResponse(int(status.Load()), header, "{}"), nil
	}))
	ctx := context.Background()
	_, err := manager.wikidataSitelinks(ctx, "https://www.wikidata.org/wiki/Q42")
	if rateLimited := asRateLimited(err); rateLimited == nil || rateLimited.Source != "wikidata" || rateLimited.RetryAfter != 30*time.Second {
		t.Fatalf("err=%v, want wikidata ErrRateLimited", err)
	}
	if !strings.Contains(capturedUA, defaultContactURL) {
		t.Fatalf("wikidata ua=%q", capturedUA)
	}
	// Generic cooldown gates also cover Wikidata; expire it explicitly before
	// checking a successful response carries the same contactable UA.
	manager.cooldownMu.Lock()
	delete(manager.blockedUntil, "wikidata")
	manager.cooldownMu.Unlock()
	status.Store(http.StatusOK)
	if _, err = manager.wikidataSitelinks(ctx, "https://www.wikidata.org/wiki/Q42"); err == nil || asRateLimited(err) != nil {
		t.Fatalf("ok-response err=%v", err) // entity missing, but not rate limited
	}
}

func TestSpotifyImageRateLimit(t *testing.T) {
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusTooManyRequests, http.Header{}, "{}"), nil
	}))
	// Missing Retry-After falls back to the default backoff.
	_, err := manager.spotifyImage(context.Background(), "https://open.spotify.com/artist/abc")
	if rateLimited := asRateLimited(err); rateLimited == nil || rateLimited.Source != "spotify" || rateLimited.RetryAfter != defaultRateLimitBackoff {
		t.Fatalf("err=%v, want spotify ErrRateLimited", err)
	}
}

func TestLastFMError29RateLimited(t *testing.T) {
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return cannedResponse(http.StatusOK, http.Header{}, `{"error":29,"message":"Rate limit exceeded"}`), nil
	}))
	setting := storage.MetadataSourceSetting{Source: "lastfm", APIKey: "key"}
	_, _, err := manager.lastFMInfoLanguage(context.Background(), storage.ArtistMatchInput{ID: 1, Name: "Artist"}, setting, "en")
	if rateLimited := asRateLimited(err); rateLimited == nil || rateLimited.Source != "lastfm" {
		t.Fatalf("err=%v, want lastfm ErrRateLimited", err)
	}
}

func TestDownloadPublicImageUserAgentAndRateLimit(t *testing.T) {
	var capturedUA string
	var status atomic.Int32
	status.Store(http.StatusOK)
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		capturedUA = r.Header.Get("User-Agent")
		if int(status.Load()) == http.StatusOK {
			// Minimal JPEG magic for http.DetectContentType.
			return cannedResponse(http.StatusOK, http.Header{}, "\xff\xd8\xff\xe0\x00\x10JFIF"), nil
		}
		return cannedResponse(int(status.Load()), http.Header{"Retry-After": {"10"}}, ""), nil
	}))
	// An IP-literal public URL skips DNS resolution, so the test never touches
	// the network: the transport above answers the request.
	data, _, _, err := manager.downloadPublicImage(context.Background(), "http://93.184.216.34/poster.jpg", "work poster")
	if err != nil || len(data) == 0 {
		t.Fatalf("download err=%v", err)
	}
	if !strings.Contains(capturedUA, defaultContactURL) {
		t.Fatalf("image ua=%q", capturedUA)
	}
	status.Store(http.StatusTooManyRequests)
	_, _, _, err = manager.downloadPublicImage(context.Background(), "http://93.184.216.34/poster2.jpg", "work poster")
	if asRateLimited(err) == nil {
		t.Fatalf("err=%v, want ErrRateLimited", err)
	}
}

// waitArtistMatchRun polls until the artist match run leaves "running".
func waitArtistMatchRun(t *testing.T, store *storage.Store, runID int64) storage.ArtistMatchRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListArtistMatchRuns(context.Background(), 1)
		if err == nil && len(runs) == 1 && runs[0].ID == runID && runs[0].Status != "running" {
			return runs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("artist match run did not finish")
	return storage.ArtistMatchRun{}
}

// H1：已确认艺术家的刷新路径不得吞掉限流。已确认 MBID 的艺术家会先走
// RefreshConfirmedArtistImage；那里遇到 429 必须停止整轮。注意：第一位艺术家
// 的 429 已把 MusicBrainz 推入退避期，第二位本来就会被立即拒绝，所以请求数
// 只是辅助证据，真正起区分作用的是 run 的 status=failed 与中文限流消息
// （吞错时会记录警告后继续，整轮状态与消息都不同）。
func TestArtistMatchRefreshStopsOnRateLimit(t *testing.T) {
	var requests atomic.Int32
	manager, store, _, _ := phase4TestManager(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	ctx := context.Background()
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	profile := storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-refresh-1", DisplayName: artists[0].Name}
	if err = store.UpsertExternalArtistProfile(ctx, artists[0].ID, profile); err != nil {
		t.Fatal(err)
	}
	setting, err := store.MetadataSourceSetting(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	_, err = manager.StartAll(ctx)
	if !errors.Is(err, ErrNoEligibleArtists) {
		t.Fatalf("expected confirmed-source skip, got %v", err)
	}
	manager.Wait()
	if got := requests.Load(); got != 0 {
		t.Fatalf("automatic identity scan must not fetch attachments: %d", got)
	}
}

// H1: a confirmed artist with a fresh profile hits the biography refresh
// before matching; Last.fm error code 29 (rate limit) there must also stop
// the run after exactly one request.
func TestArtistMatchBiographyRateLimitStopsRun(t *testing.T) {
	var requests atomic.Int32
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		return cannedResponse(http.StatusOK, http.Header{}, `{"error":29,"message":"Rate limit exceeded"}`), nil
	}))
	store := manager.store
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Bio/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Bio", Artists: []string{"Bio Artist"}, AlbumArtists: []string{"Bio Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	artists, err := store.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	// Confirmed MBID and a fresh MusicBrainz profile (the upsert stamps
	// fetched_at and image_checked_at), so matching itself would skip the
	// sources; the biography refresh is what hits Last.fm.
	profile := storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-bio-1", DisplayName: artists[0].Name}
	if err = store.UpsertExternalArtistProfile(ctx, artists[0].ID, profile); err != nil {
		t.Fatal(err)
	}
	setting, err := store.MetadataSourceSetting(ctx, "lastfm")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	setting.APIKey = "key"
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveBiographySettings(ctx, storage.BiographySettings{PreferredLanguages: "en", SourcePriority: "lastfm", WikipediaEnabled: false, CacheDays: 30}); err != nil {
		t.Fatal(err)
	}
	artistFakeClock(manager)
	runID, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	finished, err := store.DurableArtistRun(ctx, runID)
	if err != nil || finished.Status != "paused" || finished.PauseReason != "rate_limit_exhausted" || finished.AutoResumeCount != 3 {
		t.Fatalf("run=%+v err=%v", finished, err)
	}
	if got := int(requests.Load()); got != 12 {
		t.Fatalf("requests=%d, want 12 (biography-refresh limit retries three times per round across four rounds)", got)
	}
}

// H2: when the Wikipedia REST summary endpoint is rate limited, the action
// API fallback must not be requested.
func TestWikipediaSummaryRateLimitStopsFallback(t *testing.T) {
	var requests atomic.Int32
	var lastPath atomic.Value
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests.Add(1)
		lastPath.Store(r.URL.Path)
		return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, "{}"), nil
	}))
	_, _, err := manager.wikipediaSummary(context.Background(), "en", "Some Artist")
	if rateLimited := asRateLimited(err); rateLimited == nil || rateLimited.Source != "wikipedia" {
		t.Fatalf("err=%v, want wikipedia ErrRateLimited", err)
	}
	if got := int(requests.Load()); got != 1 {
		t.Fatalf("requests=%d, want 1 (action API fallback must not run)", got)
	}
	if path, _ := lastPath.Load().(string); !strings.Contains(path, "rest_v1") {
		t.Fatalf("only the REST endpoint may be requested, got %q", path)
	}
}

// H-1: a 429 from the Wikidata image endpoint inside musicBrainzLookup must
// abort the run instead of being swallowed as "no image". The second artist
// must never reach Wikidata.
func TestWikidataImageRateLimitStopsStartAll(t *testing.T) {
	var mbRequests, wikidataRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mbRequests.Add(1)
		io.WriteString(w, `{"id":"mbid-wd","name":"Artist","sort-name":"Artist","aliases":[],"tags":[],"relations":[{"type":"wikidata","url":{"resource":"https://www.wikidata.org/wiki/Q42"}}]}`)
	}))
	defer server.Close()
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "www.wikidata.org" {
			wikidataRequests.Add(1)
			return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, "{}"), nil
		}
		return http.DefaultTransport.RoundTrip(r)
	}))
	manager.musicBrainzBase = server.URL + "/mb"
	store := manager.store
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for i, mbid := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		name := fmt.Sprintf("WD Artist %d", i)
		if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: fmt.Sprintf("WD%d/01.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "WD", Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {mbid}}}}); err != nil {
			t.Fatal(err)
		}
	}
	setting, err := store.MetadataSourceSetting(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	artistFakeClock(manager)
	runID, err := manager.StartAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	finished, err := store.DurableArtistRun(ctx, runID)
	if err != nil || finished.Status != "paused" || finished.PauseReason != "rate_limit_exhausted" || finished.AutoResumeCount != 3 {
		t.Fatalf("run=%+v err=%v", finished, err)
	}
	if got := int(wikidataRequests.Load()); got != 12 {
		t.Fatalf("wikidata requests=%d, want 12 (three per round across four rounds)", got)
	}
	if got := int(mbRequests.Load()); got != 12 {
		t.Fatalf("mb requests=%d, want 12", got)
	}
}

// H-1: a 429 from the Spotify oEmbed endpoint stops the image URL loop
// instead of continuing with the next URL.
func TestSpotifyImageRateLimitStopsLoop(t *testing.T) {
	var spotifyRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"mbid-sp","name":"Artist","sort-name":"Artist","aliases":[],"tags":[],"relations":[{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/one"}},{"type":"streaming","url":{"resource":"https://open.spotify.com/artist/two"}}]}`)
	}))
	defer server.Close()
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "open.spotify.com" {
			spotifyRequests.Add(1)
			return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, "{}"), nil
		}
		return http.DefaultTransport.RoundTrip(r)
	}))
	manager.musicBrainzBase = server.URL + "/mb"
	setting, err := manager.store.MetadataSourceSetting(context.Background(), "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = manager.musicBrainzLookup(context.Background(), "mbid-sp", setting)
	if rateLimited := asRateLimited(err); rateLimited == nil || rateLimited.Source != "spotify" {
		t.Fatalf("err=%v, want spotify ErrRateLimited", err)
	}
	if got := int(spotifyRequests.Load()); got != 1 {
		t.Fatalf("spotify requests=%d, want 1 (loop stopped at the first 429)", got)
	}
}

// M-1: a 429 while downloading the poster right after an automatic work
// confirmation stops the phase4 run. The confirmation itself stays persisted.
func TestPhase4WorkPosterRateLimitStopsRun(t *testing.T) {
	var posterRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":[{"id":123,"type":2,"name":"葬送のフリーレン","name_cn":"葬送的芙莉莲","date":"2023-09-29","images":{"large":"http://93.184.216.34/poster.jpg"}}]}`)
	}))
	defer server.Close()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "poster.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { delete(phase4DBPath, store); _ = store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	manager := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.phaseEndpoints = phase4Endpoints{Bangumi: server.URL + "/bangumi", BangumiAPI: server.URL}
	// The poster URL uses a public IP literal so validatePublicImageURL passes
	// without DNS; the transport answers it with a 429 instead of the network.
	manager.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "93.184.216.34" {
			posterRequests.Add(1)
			return cannedResponse(http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, ""), nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	manager.bangumiInterval = 0
	setting, err := store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 1})
	if err != nil || len(albums) == 0 {
		t.Fatalf("albums=%v err=%v", albums, err)
	}
	work, err := store.CreateWork(ctx, storage.WorkInput{Title: "葬送のフリーレン", Type: "anime", Year: 2023})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AddWorkAlbum(ctx, work.ID, albums[0].ID, "other"); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateEnrichmentRun(ctx, "work", work.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "work", TargetID: work.ID, Force: true})
	finished, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "failed" || !strings.Contains(finished.ErrorMessage, "作品海报 限流（429）") {
		t.Fatalf("run=%+v", finished)
	}
	if got := int(posterRequests.Load()); got != 1 {
		t.Fatalf("poster requests=%d, want 1", got)
	}
	// The automatic confirmation was persisted before the poster download.
	candidates, err := store.WorkMatchCandidates(ctx, work.ID)
	if err != nil {
		t.Fatal(err)
	}
	confirmed := false
	for _, candidate := range candidates {
		confirmed = confirmed || candidate.Status == "confirmed"
	}
	if !confirmed {
		t.Fatalf("confirmation must survive the poster 429: %+v", candidates)
	}
}

// Low-2: when an artist is auto-confirmed and the rate limit only hits
// afterwards (biography refresh), the run still stops — but the confirmed
// artist is counted as matched and processed before the run ends.
func TestArtistMatchConfirmedThenRateLimitCountsMatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "aliases") {
			io.WriteString(w, `{"id":"33333333-3333-4333-8333-333333333333","name":"Count Artist","sort-name":"Count Artist","aliases":[],"tags":[],"relations":[]}`)
			return
		}
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	manager := transportManager(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return http.DefaultTransport.RoundTrip(r)
	}))
	manager.musicBrainzBase = server.URL + "/mb"
	store := manager.store
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Count/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Count", Artists: []string{"Count Artist"}, AlbumArtists: []string{"Count Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"MUSICBRAINZ_ARTISTID": {"33333333-3333-4333-8333-333333333333"}}}}); err != nil {
		t.Fatal(err)
	}
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
	finished := waitArtistMatchRun(t, store, runID)
	manager.Wait()
	// Automatic scanning no longer performs post-confirmation attachment HTTP.
	if finished.Status != "completed" {
		t.Fatalf("run=%+v", finished)
	}
	if finished.Matched != 1 || finished.Processed != 1 {
		t.Fatalf("confirmed artist must be counted before the run stops: run=%+v", finished)
	}
}
