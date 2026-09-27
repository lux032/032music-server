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

func TestRecordScrobbleHalfThresholdAndDedupe(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	id := seedScrobbleTrack(t, s, ctx, "a.flac", 240000)
	base := time.Now().Add(-time.Hour)

	// Below half of the library duration: accepted, not counted. The client
	// duration is ignored when the library knows the length.
	result, err := s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, PositionMillis: 100000, DurationMillis: 150000, ReportedAt: base})
	if err != nil || result.Recorded || result.Reason != ScrobbleReasonThreshold {
		t.Fatalf("below threshold: %+v %v", result, err)
	}
	if playCount(t, s, ctx, id) != 0 {
		t.Fatal("below-threshold report was counted")
	}

	// Reaching half (1 s slack) counts once.
	result, err = s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, PositionMillis: 119500, ReportedAt: base.Add(119500 * time.Millisecond)})
	if err != nil || !result.Recorded {
		t.Fatalf("half play: %+v %v", result, err)
	}
	// The same playback reported again at the end (web player "ended"
	// event, client retry, second device) is a duplicate.
	result, err = s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, PositionMillis: 240000, ReportedAt: base.Add(240 * time.Second)})
	if err != nil || result.Recorded || result.Reason != ScrobbleReasonDuplicate {
		t.Fatalf("same playback: %+v %v", result, err)
	}
	if playCount(t, s, ctx, id) != 1 {
		t.Fatalf("play count = %d, want 1", playCount(t, s, ctx, id))
	}
	// Playing the track again right after it finished is a new play.
	result, err = s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, PositionMillis: 130000, ReportedAt: base.Add(370 * time.Second)})
	if err != nil || !result.Recorded {
		t.Fatalf("second play: %+v %v", result, err)
	}
	// Legacy clients that omit the position are trusted.
	result, err = s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, ReportedAt: base.Add(50 * time.Minute)})
	if err != nil || !result.Recorded {
		t.Fatalf("legacy report: %+v %v", result, err)
	}
	if playCount(t, s, ctx, id) != 3 {
		t.Fatalf("play count = %d, want 3", playCount(t, s, ctx, id))
	}
	if _, err = s.RecordScrobble(ctx, ScrobbleInput{TrackID: 9999, PositionMillis: 1}); err == nil {
		t.Fatal("unknown track accepted")
	}
}

func TestRecordScrobbleUsesClientDurationWhenLibraryHasNone(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	id := seedScrobbleTrack(t, s, ctx, "b.flac", 0)
	result, err := s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, PositionMillis: 60000, DurationMillis: 200000})
	if err != nil || result.Recorded {
		t.Fatalf("below client-duration threshold: %+v %v", result, err)
	}
	result, err = s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, PositionMillis: 100000, DurationMillis: 200000})
	if err != nil || !result.Recorded {
		t.Fatalf("client-duration half: %+v %v", result, err)
	}
}

func TestRecordScrobbleQueuesLastFMOnlyWhenConnected(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	id := seedScrobbleTrack(t, s, ctx, "c.flac", 200000)
	short := seedScrobbleTrack(t, s, ctx, "short.flac", 25000)
	started := time.Now().Add(-30 * time.Minute).Truncate(time.Second)

	result, err := s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, PositionMillis: 100000, ReportedAt: started.Add(100 * time.Second)})
	if err != nil || !result.Recorded || result.QueuedForLastFM || queueLength(t, s, ctx) != 0 {
		t.Fatalf("not connected: %+v %v", result, err)
	}

	if err := s.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastFMSession(ctx, "listener", "session-key"); err != nil {
		t.Fatal(err)
	}
	result, err = s.RecordScrobble(ctx, ScrobbleInput{TrackID: id, PositionMillis: 100000, ReportedAt: started.Add(20 * time.Minute)})
	if err != nil || !result.Recorded || !result.QueuedForLastFM {
		t.Fatalf("connected: %+v %v", result, err)
	}
	items, err := s.DueLastFMScrobbles(ctx, time.Now(), 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("queue = %+v %v", items, err)
	}
	item := items[0]
	wantStart := started.Add(20*time.Minute - 100*time.Second).Unix()
	if item.Artist != "Singer" || item.Track != "Song c.flac" || item.Album != "Album" || item.AlbumArtist != "Band" || item.TrackNumber != 3 || item.DurationSeconds != 200 || item.StartedAt != wantStart {
		t.Fatalf("queued metadata = %+v (want start %d)", item, wantStart)
	}

	// Tracks of 30 s or less count locally but are not sent to Last.fm.
	result, err = s.RecordScrobble(ctx, ScrobbleInput{TrackID: short, PositionMillis: 20000})
	if err != nil || !result.Recorded || result.QueuedForLastFM {
		t.Fatalf("short track: %+v %v", result, err)
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
