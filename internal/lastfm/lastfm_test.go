package lastfm

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestSignFollowsLastFMRules(t *testing.T) {
	params := url.Values{"method": {"auth.getSession"}, "api_key": {"KEY"}, "token": {"TOK"}, "format": {"json"}}
	sum := md5.Sum([]byte("api_keyKEYmethodauth.getSessiontokenTOKSECRET"))
	if got, want := Sign(params, "SECRET"), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
	withFormat := Sign(params, "SECRET")
	params.Del("format")
	if Sign(params, "SECRET") != withFormat {
		t.Fatal("format must not be signed")
	}
}

type fakeLastFM struct {
	mu       sync.Mutex
	requests []url.Values
	respond  func(values url.Values) (int, string)
}

func (f *fakeLastFM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	values, _ := url.ParseQuery(string(body))
	f.mu.Lock()
	f.requests = append(f.requests, values)
	f.mu.Unlock()
	status, payload := f.respond(values)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, payload)
}

func (f *fakeLastFM) calls(method string) []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result []url.Values
	for _, values := range f.requests {
		if values.Get("method") == method {
			result = append(result, values)
		}
	}
	return result
}

func verifySignature(t *testing.T, values url.Values) {
	t.Helper()
	signature := values.Get("api_sig")
	unsigned := url.Values{}
	for key, value := range values {
		if key != "api_sig" {
			unsigned[key] = value
		}
	}
	if Sign(unsigned, "secret") != signature {
		t.Fatalf("bad signature for %v", values)
	}
}

func newTestService(t *testing.T, fake *fakeLastFM) (*Service, *storage.Store, int64) {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "lastfm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "a.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "夜に駆ける", Album: "THE BOOK", Artists: []string{"YOASOBI"}, DurationMillis: 261000, DiscNumber: 1, TrackNumber: 2}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks: %v %v", tracks, err)
	}
	if err := store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{Source: "lastfm", APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	service := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.SetClientFactory(func(apiKey, apiSecret string) *Client {
		client := NewClient(apiKey, apiSecret)
		client.Endpoint = server.URL
		return client
	})
	return service, store, tracks[0].ID
}

func TestConnectScrobbleAndNowPlaying(t *testing.T) {
	fake := &fakeLastFM{respond: func(values url.Values) (int, string) {
		switch values.Get("method") {
		case "auth.getSession":
			return 200, `{"session":{"name":"listener","key":"SK","subscriber":0}}`
		case "track.scrobble":
			return 200, `{"scrobbles":{"@attr":{"accepted":1,"ignored":0},"scrobble":{"ignoredMessage":{"code":"0","#text":""}}}}`
		case "track.updateNowPlaying":
			return 200, `{"nowplaying":{}}`
		}
		return 400, `{"error":3,"message":"Invalid Method"}`
	}}
	service, store, trackID := newTestService(t, fake)
	ctx := context.Background()

	username, err := service.Connect(ctx, "TOKEN")
	if err != nil || username != "listener" {
		t.Fatalf("connect: %q %v", username, err)
	}
	if calls := fake.calls("auth.getSession"); len(calls) != 1 || calls[0].Get("token") != "TOKEN" {
		t.Fatalf("getSession calls: %v", calls)
	} else {
		verifySignature(t, calls[0])
	}

	reportedAt := time.Now().Add(-time.Minute)
	result, err := store.RecordScrobble(ctx, storage.ScrobbleInput{TrackID: trackID, PositionMillis: 140000, ReportedAt: reportedAt})
	if err != nil || !result.QueuedForLastFM {
		t.Fatalf("record: %+v %v", result, err)
	}
	service.Flush(ctx)
	calls := fake.calls("track.scrobble")
	if len(calls) != 1 {
		t.Fatalf("scrobble calls = %d", len(calls))
	}
	verifySignature(t, calls[0])
	call := calls[0]
	wantTimestamp := reportedAt.Add(-140 * time.Second).Unix()
	if call.Get("sk") != "SK" || call.Get("artist[0]") != "YOASOBI" || call.Get("track[0]") != "夜に駆ける" || call.Get("album[0]") != "THE BOOK" || call.Get("duration[0]") != "261" || call.Get("trackNumber[0]") != "2" || call.Get("timestamp[0]") != itoa(wantTimestamp) {
		t.Fatalf("scrobble params: %v (want timestamp %d)", call, wantTimestamp)
	}
	settings, _ := store.LastFMScrobbleSettings(ctx)
	if settings.PendingCount != 0 || settings.LastSuccessAt == "" {
		t.Fatalf("after submit: %+v", settings)
	}

	service.NowPlaying(trackID)
	service.NowPlaying(trackID) // repeated timeline report of the same play
	service.Wait()
	if calls := fake.calls("track.updateNowPlaying"); len(calls) != 1 || calls[0].Get("track") != "夜に駆ける" {
		t.Fatalf("now playing calls: %v", calls)
	}
}

func TestTemporaryFailureIsRetriedAndInvalidSessionDisconnects(t *testing.T) {
	var mu sync.Mutex
	mode := "offline"
	fake := &fakeLastFM{respond: func(values url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		switch mode {
		case "offline":
			return 503, `{"error":11,"message":"Service Offline"}`
		case "invalid-session":
			return 403, `{"error":9,"message":"Invalid session key"}`
		}
		return 200, `{"scrobbles":{"@attr":{"accepted":1,"ignored":0},"scrobble":[{"ignoredMessage":{"code":"0","#text":""}}]}}`
	}}
	service, store, trackID := newTestService(t, fake)
	ctx := context.Background()
	if err := store.SetLastFMSession(ctx, "listener", "SK"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordScrobble(ctx, storage.ScrobbleInput{TrackID: trackID, PositionMillis: 140000}); err != nil {
		t.Fatal(err)
	}

	delay := service.Flush(ctx)
	if delay < time.Minute {
		t.Fatalf("retry delay = %s", delay)
	}
	settings, _ := store.LastFMScrobbleSettings(ctx)
	if settings.PendingCount != 1 || !strings.Contains(settings.LastError, "Service Offline") {
		t.Fatalf("offline: %+v", settings)
	}
	// Not due yet: nothing is sent.
	service.Flush(ctx)
	if n := len(fake.calls("track.scrobble")); n != 1 {
		t.Fatalf("scrobble calls while backing off = %d", n)
	}

	mu.Lock()
	mode = "invalid-session"
	mu.Unlock()
	_ = store.RetryLastFMScrobblesNow(ctx)
	service.Flush(ctx)
	settings, _ = store.LastFMScrobbleSettings(ctx)
	if settings.Connected() || settings.PendingCount != 1 || settings.LastError == "" {
		t.Fatalf("invalid session must disconnect and keep the play: %+v", settings)
	}

	mu.Lock()
	mode = "ok"
	mu.Unlock()
	if err := store.SetLastFMSession(ctx, "listener", "SK2"); err != nil {
		t.Fatal(err)
	}
	service.Flush(ctx)
	settings, _ = store.LastFMScrobbleSettings(ctx)
	if settings.PendingCount != 0 || settings.LastError != "" {
		t.Fatalf("after reconnect: %+v", settings)
	}
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }
