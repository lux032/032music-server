package lastfm

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// loveTestService builds a service with one favouritable track and a fake
// Last.fm endpoint. The fake's responder decides each call's outcome.
func loveTestService(t *testing.T, respond func(values url.Values) (int, string)) (*Service, *storage.Store, *fakeLastFM, int64) {
	t.Helper()
	fake := &fakeLastFM{respond: respond}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "love.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "a.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "夜に駆ける", Album: "THE BOOK", Artists: []string{"YOASOBI"}, DurationMillis: 261000, DiscNumber: 1, TrackNumber: 2}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks: %v %v", tracks, err)
	}
	if err = store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{Source: "lastfm", APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetLastFMSession(ctx, "listener", "SK"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetLastFMLoveSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.SetClientFactory(func(apiKey, apiSecret string) *Client {
		client := NewClient(apiKey, apiSecret)
		client.Endpoint = server.URL
		return client
	})
	// Keep the rate-limit pause out of the tests' wall-clock budget.
	service.now = time.Now
	return service, store, fake, tracks[0].ID
}

// A favourite queues a love call that the worker submits with the shared
// session, then removes from the queue.
func TestLoveSyncSubmitsTrackLove(t *testing.T) {
	service, store, fake, trackID := loveTestService(t, func(values url.Values) (int, string) {
		if values.Get("method") == "track.love" {
			return 200, `{"lfm":{"status":"ok"}}`
		}
		return 400, `{"error":3,"message":"Invalid Method"}`
	})
	ctx := context.Background()
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	calls := fake.calls("track.love")
	if len(calls) != 1 {
		t.Fatalf("love calls = %d", len(calls))
	}
	verifySignature(t, calls[0])
	if calls[0].Get("sk") != "SK" || calls[0].Get("artist") != "YOASOBI" || calls[0].Get("track") != "夜に駆ける" {
		t.Fatalf("love params: %v", calls[0])
	}
	if stats, _ := store.LastFMLoveQueueStats(ctx); stats.Pending != 0 {
		t.Fatalf("queue not drained: %+v", stats)
	}

	// Unfavouriting submits track.unlove instead.
	if err := store.SetTrackFavorite(ctx, trackID, false); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	if calls = fake.calls("track.unlove"); len(calls) != 1 || calls[0].Get("sk") != "SK" {
		t.Fatalf("unlove calls: %v", calls)
	}
}

// A permanent rejection (unknown track, error 6) drops the entry instead of
// retrying forever, and records the reason on the settings card.
func TestLoveSyncDropsPermanentlyRejectedTrack(t *testing.T) {
	service, store, fake, trackID := loveTestService(t, func(values url.Values) (int, string) {
		return 400, `{"error":6,"message":"Invalid track name"}`
	})
	ctx := context.Background()
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	if calls := fake.calls("track.love"); len(calls) != 1 {
		t.Fatalf("love calls = %d", len(calls))
	}
	if stats, _ := store.LastFMLoveQueueStats(ctx); stats.Pending != 0 {
		t.Fatalf("rejected entry kept: %+v", stats)
	}
	settings, _ := store.LastFMScrobbleSettings(ctx)
	if settings.LastLoveError == "" {
		t.Fatal("rejection reason not recorded")
	}
}

// A transient failure keeps the entry, schedules a retry and does not resend
// before it is due.
func TestLoveSyncRetriesTransientFailure(t *testing.T) {
	var mu sync.Mutex
	failing := true
	service, store, fake, trackID := loveTestService(t, func(values url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if failing {
			return 503, `{"error":11,"message":"Service Offline"}`
		}
		return 200, `{"lfm":{"status":"ok"}}`
	})
	ctx := context.Background()
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	delay := service.Flush(ctx)
	if delay < time.Minute {
		t.Fatalf("retry delay = %s", delay)
	}
	stats, _ := store.LastFMLoveQueueStats(ctx)
	if stats.Pending != 1 || stats.LastErr == "" {
		t.Fatalf("after failure: %+v", stats)
	}
	service.Flush(ctx)
	if calls := fake.calls("track.love"); len(calls) != 1 {
		t.Fatalf("resent while backing off: %d", len(calls))
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	if err := store.RetryLastFMLovesNow(ctx); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	if stats, _ = store.LastFMLoveQueueStats(ctx); stats.Pending != 0 {
		t.Fatalf("not drained after retry: %+v", stats)
	}
}

// An invalid session disconnects the account and keeps the queued change.
func TestLoveSyncInvalidSessionDisconnects(t *testing.T) {
	service, store, _, trackID := loveTestService(t, func(values url.Values) (int, string) {
		return 403, `{"error":9,"message":"Invalid session key"}`
	})
	ctx := context.Background()
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	settings, _ := store.LastFMScrobbleSettings(ctx)
	if settings.Connected() || settings.LastLoveError != "" && settings.LastError == "" {
		t.Fatalf("after invalid session: %+v", settings)
	}
	if stats, _ := store.LastFMLoveQueueStats(ctx); stats.Pending != 1 {
		t.Fatalf("queued change lost: %+v", stats)
	}
}

// The backfill walks the existing favourites one at a time, records progress
// and finishes.
func TestLoveBackfillPushesExistingFavourites(t *testing.T) {
	service, store, fake, trackID := loveTestService(t, func(values url.Values) (int, string) {
		return 200, `{"lfm":{"status":"ok"}}`
	})
	ctx := context.Background()
	// Favourite while sync is off so nothing is queued, like pre-existing data.
	if err := store.SetLastFMLoveSync(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLastFMLoveSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	total, err := store.CountLoveBackfillTracks(ctx)
	if err != nil || total != 1 {
		t.Fatalf("total = %d %v", total, err)
	}
	if err = store.BeginLastFMLoveBackfill(ctx, total); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	if calls := fake.calls("track.love"); len(calls) != 1 || calls[0].Get("track") != "夜に駆ける" {
		t.Fatalf("backfill calls: %v", calls)
	}
	run, _ := store.LastFMLoveBackfill(ctx)
	if run.State != storage.LoveBackfillDone || run.Processed != 1 || run.Percent() != 100 {
		t.Fatalf("run: %+v", run)
	}
}

// A cancelled run stops submitting and keeps its cursor.
func TestLoveBackfillCancelledStops(t *testing.T) {
	service, store, fake, trackID := loveTestService(t, func(values url.Values) (int, string) {
		return 200, `{"lfm":{"status":"ok"}}`
	})
	ctx := context.Background()
	if err := store.SetLastFMLoveSync(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLastFMLoveSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginLastFMLoveBackfill(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelLastFMLoveBackfill(ctx); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	if calls := fake.calls("track.love"); len(calls) != 0 {
		t.Fatalf("cancelled run submitted: %d", len(calls))
	}
	run, _ := store.LastFMLoveBackfill(ctx)
	if run.State != storage.LoveBackfillCancelled {
		t.Fatalf("state: %+v", run)
	}
}

// Love sync works with the scrobble switch off: they are separate features.
func TestLoveSyncWorksWithoutScrobbleEnabled(t *testing.T) {
	service, store, fake, trackID := loveTestService(t, func(values url.Values) (int, string) {
		if values.Get("method") == "track.love" {
			return 200, `{"lfm":{"status":"ok"}}`
		}
		return 400, `{"error":3,"message":"Invalid Method"}`
	})
	ctx := context.Background()
	if err := store.SaveLastFMScrobblePreferences(ctx, false, false, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	if calls := fake.calls("track.love"); len(calls) != 1 {
		t.Fatalf("love calls with scrobble off = %d", len(calls))
	}
}

// GetLovedTracks decodes both the array and single-object response shapes.
func TestGetLovedTracksDecoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"lovedtracks":{"@attr":{"totalPages":"3","page":"1"},"track":[
			{"name":"A","artist":{"name":"X"},"date":{"uts":"1216371514"}},
			{"name":"B","artist":{"name":"Y"},"date":{"uts":1216371515}}]}}`)
	}))
	defer server.Close()
	client := NewClient("key", "secret")
	client.Endpoint = server.URL
	items, err := client.GetLovedTracks(context.Background(), "listener", 1, 2)
	if err != nil || len(items) != 2 {
		t.Fatalf("loved tracks: %v %v", items, err)
	}
	if items[0].Track != "A" || items[0].Artist != "X" || items[0].LovedAt != 1216371514 || items[1].LovedAt != 1216371515 {
		t.Fatalf("decoded: %+v", items)
	}

	single := NewClient("key", "secret")
	single.Endpoint = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"lovedtracks":{"track":{"name":"Solo","artist":{"name":"One"},"date":{"uts":"5"}}}}`)
	})).URL
	items, err = single.GetLovedTracks(context.Background(), "listener", 1, 1)
	if err != nil || len(items) != 1 || items[0].Track != "Solo" || items[0].LovedAt != 5 {
		t.Fatalf("single: %+v %v", items, err)
	}
}
