package listenbrainz

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func newTestStore(t *testing.T) (*storage.Store, context.Context, int64) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "lb-svc.db"))
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
	lib, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "a.flac", FileSize: 10, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Night Dancer", Album: "Singles", Artists: []string{"imase"},
			DurationMillis: 210000, TrackNumber: 1, DiscNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks = %+v err=%v", tracks, err)
	}
	return store, ctx, tracks[0].ID
}

func newFakeService(t *testing.T, store *storage.Store, handler http.HandlerFunc) (*Service, *time.Time) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	clock := time.Unix(1_760_000_000, 0)
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return clock }
	svc.SetClientFactory(func(_ string) *Client {
		c := NewClient(server.URL)
		return c
	})
	return svc, &clock
}

func TestValidateTokenAndSubmitListens(t *testing.T) {
	store, ctx, trackID := newTestStore(t)
	var mu sync.Mutex
	var listenTypes []string
	var authHeader string

	svc, clock := newFakeService(t, store, func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/1/validate-token":
			_, _ = io.WriteString(w, `{"code":200,"valid":true,"user_name":"alice"}`)
		case "/1/submit-listens":
			var body struct {
				ListenType string `json:"listen_type"`
				Payload    []struct {
					ListenedAt int64 `json:"listened_at"`
				} `json:"payload"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			listenTypes = append(listenTypes, body.ListenType)
			mu.Unlock()
			if body.ListenType == "playing_now" {
				_, _ = io.WriteString(w, `{"status":"ok","recording_msid":"55555555-5555-4555-8555-555555555555"}`)
				return
			}
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	})

	url, user, err := svc.ValidateToken(ctx, "https://lb.local/", "tok-abc")
	if err != nil || url != "https://lb.local" || user != "alice" || authHeader != "Token tok-abc" {
		t.Fatalf("ValidateToken = %q %q err=%v auth=%q", url, user, err, authHeader)
	}
	if err = store.SaveListenBrainzPreferences(ctx, true, true, url); err != nil {
		t.Fatal(err)
	}
	if err = store.SetListenBrainzConnection(ctx, url, "tok-abc", user); err != nil {
		t.Fatal(err)
	}

	// NowPlaying caches returned recording_msid.
	svc.sendNowPlaying(ctx, trackID, *clock)
	cache, err := store.ListenBrainzTrackIDCache(ctx, trackID, "imase", "Night Dancer", "Singles")
	if err != nil || cache.RecordingMSID != "55555555-5555-4555-8555-555555555555" {
		t.Fatalf("cached msid = %+v err=%v", cache, err)
	}

	// Queue two plays -> submitted as one "import" batch, even if older than 14 days.
	for _, sid := range []string{"session-1111", "session-2222"} {
		if _, err := store.RecordPlaybackEvent(ctx, storage.PlaybackEventInput{
			ClientID: "web-1111", ClientKind: "web", SessionID: sid, Seq: 1, Type: "start",
			TrackID: trackID, State: "playing", PositionMillis: 0, DurationMillis: 210000,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RecordPlaybackEvent(ctx, storage.PlaybackEventInput{
			ClientID: "web-1111", ClientKind: "web", SessionID: sid, Seq: 2, Type: "heartbeat",
			TrackID: trackID, State: "playing", PositionMillis: 120000, DurationMillis: 210000,
		}); err != nil {
			t.Fatal(err)
		}
	}
	*clock = clock.Add(30 * 24 * time.Hour) // >14 days: still submitted to ListenBrainz
	svc.Flush(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(listenTypes) != 2 || listenTypes[0] != "playing_now" || listenTypes[1] != "import" {
		settings, _ := store.ListenBrainzSettings(ctx)
		t.Fatalf("listenTypes = %v settings=%+v", listenTypes, settings)
	}
	settings, _ := store.ListenBrainzSettings(ctx)
	if settings.PendingCount != 0 || settings.LastSuccessAt == "" {
		t.Fatalf("settings after flush = %+v", settings)
	}
}

func TestLoveLookupFallbackMSIDAndSkip(t *testing.T) {
	store, ctx, track1 := newTestStore(t)
	lib, _ := store.LibraryByRoot(ctx, "/music")
	// Track 2 has no MBID and lookup returns empty -> skipped_no_id.
	if err := store.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "b.flac", FileSize: 10, ModifiedAtNS: 2,
		Metadata: metadata.AudioMetadata{
			Title: "Indigo Track", Album: "Doujin", Artists: []string{"Unknown Circle"},
			DurationMillis: 180000, TrackNumber: 2, DiscNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	tracks, _ := store.ListTracks(ctx, storage.Filters{Limit: 10})
	var track2 int64
	for _, tr := range tracks {
		if tr.ID != track1 {
			track2 = tr.ID
		}
	}
	_ = store.SetTrackFavorite(ctx, track1, true)
	_ = store.SetTrackFavorite(ctx, track2, true)
	_ = store.SetListenBrainzConnection(ctx, DefaultServerURL, "tok", "alice")
	_ = store.SetListenBrainzLoveSync(ctx, true)

	const resolvedMBID = "66666666-6666-4666-8666-666666666666"
	var feedbacks []map[string]any
	svc, clock := newFakeService(t, store, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1/metadata/lookup/":
			if r.URL.Query().Get("recording_name") == "Night Dancer" {
				_, _ = io.WriteString(w, `{"recording_mbid":"`+resolvedMBID+`"}`)
				return
			}
			_, _ = io.WriteString(w, `{}`)
		case "/1/feedback/recording-feedback":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			feedbacks = append(feedbacks, body)
			_, _ = io.WriteString(w, `{"status":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	})

	total, _ := store.CountLoveBackfillTracks(ctx)
	if err := store.BeginListenBrainzLoveBackfill(ctx, total); err != nil {
		t.Fatal(err)
	}
	// Pass 1: track1 resolved via lookup -> feedback score 1.
	svc.Flush(ctx)
	*clock = clock.Add(5 * time.Second)
	// Pass 2: track2 lookup returns empty -> skipped_no_id, backfill completes.
	svc.Flush(ctx)

	run, err := store.ListenBrainzLoveBackfill(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if run.State != storage.LoveBackfillDone || run.Processed != 2 || run.SkippedNoID != 1 {
		t.Fatalf("backfill = %+v", run)
	}
	if len(feedbacks) != 1 || feedbacks[0]["recording_mbid"] != resolvedMBID {
		t.Fatalf("feedbacks = %+v", feedbacks)
	}
}

func TestCustomServerUnsupportedDegradation(t *testing.T) {
	store, ctx, trackID := newTestStore(t)
	_ = store.SaveListenBrainzPreferences(ctx, true, true, "http://koito.local")
	_ = store.SetListenBrainzConnection(ctx, "http://koito.local", "tok", "alice")
	_ = store.SetListenBrainzLoveSync(ctx, true)
	// Seed MSID via playing_now cache so lookup isn't needed; feedback returns 404.
	_ = store.UpsertListenBrainzRecordingMSID(ctx, trackID, "imase", "Night Dancer", "Singles", "77777777-7777-4777-8777-777777777777")
	_ = store.SetTrackFavorite(ctx, trackID, true)

	// Queue a play alongside the unsupported love change to confirm scrobbles are not stalled.
	if _, err := store.RecordPlaybackEvent(ctx, storage.PlaybackEventInput{
		ClientID: "web-2222", ClientKind: "web", SessionID: "session-koito", Seq: 1, Type: "start",
		TrackID: trackID, State: "playing", PositionMillis: 0, DurationMillis: 210000,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordPlaybackEvent(ctx, storage.PlaybackEventInput{
		ClientID: "web-2222", ClientKind: "web", SessionID: "session-koito", Seq: 2, Type: "heartbeat",
		TrackID: trackID, State: "playing", PositionMillis: 120000, DurationMillis: 210000,
	}); err != nil {
		t.Fatal(err)
	}

	submitted := 0
	svc, clock := newFakeService(t, store, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/1/submit-listens" {
			submitted++
			_, _ = io.WriteString(w, `{"status":"ok"}`)
			return
		}
		http.NotFound(w, r)
	})
	delay := svc.Flush(ctx)
	if delay > 2*time.Second {
		t.Fatalf("feedback 404 should not stall pending scrobbles: delay=%v", delay)
	}
	*clock = clock.Add(2 * time.Second)
	svc.Flush(ctx)

	settings, _ := store.ListenBrainzSettings(ctx)
	if !settings.FeedbackUnsupported || settings.LastLoveError == "" {
		t.Fatalf("expected feedback_unsupported degradation, got %+v", settings)
	}
	if submitted != 1 || settings.PendingCount != 0 {
		t.Fatalf("expected scrobble to succeed despite feedback 404: submitted=%d pending=%d", submitted, settings.PendingCount)
	}
}
