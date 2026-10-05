package storage

import (
	"context"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func seedScrobbleTrack(t *testing.T, s *Store, ctx context.Context, path string, durationMillis int64) int64 {
	t.Helper()
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: lib.ID, RelativePath: path, FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song " + path, Album: "Album", AlbumArtists: []string{"Band"}, Artists: []string{"Singer"}, DurationMillis: durationMillis, DiscNumber: 1, TrackNumber: 3}}
	if err := s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT t.id FROM tracks t JOIN audio_files af ON af.track_id=t.id WHERE af.relative_path=?`, path).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func playCount(t *testing.T, s *Store, ctx context.Context, trackID int64) int64 {
	t.Helper()
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT play_count FROM playback_progress WHERE track_id=?),0)`, trackID).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func queueLength(t *testing.T, s *Store, ctx context.Context) int64 {
	t.Helper()
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lastfm_scrobble_queue`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// sessionPlay drives a session that counts one play: start, then one
// heartbeat at positionMs. Returns the heartbeat result.
func sessionPlay(t *testing.T, s *Store, ctx context.Context, sessionID string, trackID, positionMs, durationMs int64) PlaybackEventResult {
	t.Helper()
	start := PlaybackEventInput{ClientID: "device-1", ClientKind: "android", SessionID: sessionID, Seq: 1, Type: "start", TrackID: trackID, State: "playing", PositionMillis: 0, DurationMillis: durationMs}
	if _, err := s.RecordPlaybackEvent(ctx, start); err != nil {
		t.Fatalf("start: %v", err)
	}
	heartbeat := start
	heartbeat.Seq = 2
	heartbeat.Type = "heartbeat"
	heartbeat.PositionMillis = positionMs
	result, err := s.RecordPlaybackEvent(ctx, heartbeat)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	return result
}

func TestSessionCountHalfThresholdAndDedupe(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	id := seedScrobbleTrack(t, s, ctx, "a.flac", 240000)

	// Below half of the library duration: accepted, not counted. The client
	// duration is ignored when the library knows the length.
	start := PlaybackEventInput{ClientID: "device-1", ClientKind: "android", SessionID: "session-threshold", Seq: 1, Type: "start", TrackID: id, State: "playing", DurationMillis: 150000}
	if _, err := s.RecordPlaybackEvent(ctx, start); err != nil {
		t.Fatal(err)
	}
	below := start
	below.Seq = 2
	below.Type = "heartbeat"
	below.PositionMillis = 100000
	result, err := s.RecordPlaybackEvent(ctx, below)
	if err != nil || result.Counted {
		t.Fatalf("below threshold: %+v %v", result, err)
	}
	if playCount(t, s, ctx, id) != 0 {
		t.Fatal("below-threshold report was counted")
	}

	// Reaching half (1 s slack) counts once.
	half := below
	half.Seq = 3
	half.PositionMillis = 119500
	if result, err = s.RecordPlaybackEvent(ctx, half); err != nil || !result.Counted {
		t.Fatalf("half play: %+v %v", result, err)
	}
	// Reporting the same playback again (progress ticks, client retry, the
	// end event) is a duplicate within the session.
	done := half
	done.Seq = 4
	done.PositionMillis = 240000
	if result, err = s.RecordPlaybackEvent(ctx, done); err != nil || !result.Counted {
		t.Fatalf("same playback: %+v %v", result, err)
	}
	if playCount(t, s, ctx, id) != 1 {
		t.Fatalf("play count = %d, want 1", playCount(t, s, ctx, id))
	}
	// Playing the track again in a new session is a new play.
	if result = sessionPlay(t, s, ctx, "session-second", id, 130000, 240000); !result.Counted {
		t.Fatalf("second play: %+v", result)
	}
	if playCount(t, s, ctx, id) != 2 {
		t.Fatalf("play count = %d, want 2", playCount(t, s, ctx, id))
	}
	// An unknown track is rejected.
	start.SessionID = "session-unknown"
	start.TrackID = 9999
	if _, err = s.RecordPlaybackEvent(ctx, start); err == nil {
		t.Fatal("unknown track accepted")
	}
}

func TestSessionCountUsesClientDurationWhenLibraryHasNone(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	id := seedScrobbleTrack(t, s, ctx, "b.flac", 0)
	start := PlaybackEventInput{ClientID: "device-1", ClientKind: "android", SessionID: "session-client-duration", Seq: 1, Type: "start", TrackID: id, State: "playing", DurationMillis: 200000}
	if _, err := s.RecordPlaybackEvent(ctx, start); err != nil {
		t.Fatal(err)
	}
	below := start
	below.Seq = 2
	below.Type = "heartbeat"
	below.PositionMillis = 60000
	if result, err := s.RecordPlaybackEvent(ctx, below); err != nil || result.Counted {
		t.Fatalf("below client-duration threshold: %+v %v", result, err)
	}
	half := below
	half.Seq = 3
	half.PositionMillis = 100000
	if result, err := s.RecordPlaybackEvent(ctx, half); err != nil || !result.Counted {
		t.Fatalf("client-duration half: %+v %v", result, err)
	}
}

func TestSessionCountQueuesLastFMOnlyWhenConnected(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	id := seedScrobbleTrack(t, s, ctx, "c.flac", 200000)
	short := seedScrobbleTrack(t, s, ctx, "short.flac", 25000)

	result := sessionPlay(t, s, ctx, "session-queue-1", id, 100000, 200000)
	if !result.Counted || result.QueuedForLastFM || queueLength(t, s, ctx) != 0 {
		t.Fatalf("not connected: %+v", result)
	}

	if err := s.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastFMSession(ctx, "listener", "session-key"); err != nil {
		t.Fatal(err)
	}
	connected := sessionPlay(t, s, ctx, "session-queue-2", id, 100000, 200000)
	if !connected.Counted || !connected.QueuedForLastFM {
		t.Fatalf("connected: %+v", connected)
	}
	items, err := s.DueLastFMScrobbles(ctx, time.Now(), 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("queue = %+v %v", items, err)
	}
	item := items[0]
	// startedAt = session start − initial position: this session started at
	// position 0, so the queued timestamp must be within seconds of now.
	if item.Artist != "Singer" || item.Track != "Song c.flac" || item.Album != "Album" || item.AlbumArtist != "Band" || item.TrackNumber != 3 || item.DurationSeconds != 200 {
		t.Fatalf("queued metadata = %+v", item)
	}
	if now := time.Now().Unix(); item.StartedAt < now-30 || item.StartedAt > now {
		t.Fatalf("queued startedAt = %d, want ≈ %d", item.StartedAt, now)
	}

	// Tracks of 30 s or less count locally but are not sent to Last.fm.
	if result := sessionPlay(t, s, ctx, "session-queue-short", short, 20000, 25000); !result.Counted || result.QueuedForLastFM {
		t.Fatalf("short track: %+v", result)
	}

	// Deferral hides the item until its retry time; retry-now restores it.
	if err := s.DeferLastFMScrobbles(ctx, []int64{item.ID}, time.Now().Add(time.Hour), "offline"); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueLastFMScrobbles(ctx, time.Now(), 50); len(due) != 0 {
		t.Fatalf("deferred item is due: %+v", due)
	}
	if err := s.RetryLastFMScrobblesNow(ctx); err != nil {
		t.Fatal(err)
	}
	if due, _ := s.DueLastFMScrobbles(ctx, time.Now(), 50); len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("retry now: %+v", due)
	}

	// Disconnecting stops new submissions but keeps queued plays.
	if err := s.ClearLastFMSession(ctx, ""); err != nil {
		t.Fatal(err)
	}
	settings, err := s.LastFMScrobbleSettings(ctx)
	if err != nil || settings.Connected() || settings.Username != "" || settings.PendingCount != 1 || !settings.HasAPISecret() {
		t.Fatalf("after disconnect: %+v %v", settings, err)
	}
	if _, err := s.DropExpiredLastFMScrobbles(ctx, time.Now().Add(LastFMMaxScrobbleAge+time.Hour)); err != nil || queueLength(t, s, ctx) != 0 {
		t.Fatalf("expired items kept: %v", err)
	}
}

func TestLastFMPendingAuthorization(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	now := time.Now()
	if token, err := s.PendingLastFMAuthorization(ctx, time.Hour, now); err != nil || token != "" {
		t.Fatalf("fresh store: %q %v", token, err)
	}
	if err := s.BeginLastFMAuthorization(ctx, "token-1", now); err != nil {
		t.Fatal(err)
	}
	if token, err := s.PendingLastFMAuthorization(ctx, time.Hour, now); err != nil || token != "token-1" {
		t.Fatalf("pending: %q %v", token, err)
	}
	// A newer attempt replaces the token; finishing the old one keeps it.
	if err := s.BeginLastFMAuthorization(ctx, "token-2", now); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLastFMAuthorization(ctx, "token-1"); err != nil {
		t.Fatal(err)
	}
	if token, _ := s.PendingLastFMAuthorization(ctx, time.Hour, now); token != "token-2" {
		t.Fatalf("stale clear removed the newer token: %q", token)
	}
	if err := s.ClearLastFMAuthorization(ctx, "token-2"); err != nil {
		t.Fatal(err)
	}
	if token, _ := s.PendingLastFMAuthorization(ctx, time.Hour, now); token != "" {
		t.Fatalf("cleared token still pending: %q", token)
	}
	if err := s.BeginLastFMAuthorization(ctx, "token-3", now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if token, _ := s.PendingLastFMAuthorization(ctx, time.Hour, now); token != "" {
		t.Fatalf("expired token offered: %q", token)
	}
}
