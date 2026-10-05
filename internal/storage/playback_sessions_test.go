package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var sessionTestLibrarySeq int64

// seedSessionTrack inserts a library/album/track chain with the given
// library duration (0 = unknown) and returns the track ID.
func seedSessionTrack(t *testing.T, ctx context.Context, s *Store, durationMillis int64) int64 {
	t.Helper()
	root := fmt.Sprintf("/music/session-%d", atomic.AddInt64(&sessionTestLibrarySeq, 1))
	result, err := s.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Test',?)`, root)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	result, err = s.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,?,?,?)`, libraryID, "Album", "album", fmt.Sprintf("album-%d", libraryID))
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := result.LastInsertId()
	result, err = s.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,duration_ms) VALUES(?,?,?,NULLIF(?,0))`, albumID, "Track", "track", durationMillis)
	if err != nil {
		t.Fatal(err)
	}
	trackID, _ := result.LastInsertId()
	return trackID
}

func sessionEvent(trackID int64, seq int64, eventType, state string, position, duration int64) PlaybackEventInput {
	return PlaybackEventInput{
		ClientID: "device-1", ClientKind: "android", SessionID: "session-" + eventType + "-test",
		Seq: seq, Type: eventType, TrackID: trackID, State: state,
		PositionMillis: position, DurationMillis: duration,
	}
}

func mustEvent(t *testing.T, ctx context.Context, s *Store, input PlaybackEventInput) PlaybackEventResult {
	t.Helper()
	result, err := s.RecordPlaybackEvent(ctx, input)
	if err != nil {
		t.Fatalf("event %+v: %v", input, err)
	}
	return result
}

// playSessionOnce plays one full session that crosses the 50% threshold so
// the play is counted exactly once. Shared by feature tests.
func playSessionOnce(t *testing.T, ctx context.Context, s *Store, sessionID string, trackID, positionMs, durationMs int64) PlaybackEventResult {
	t.Helper()
	start := sessionEvent(trackID, 1, "start", "playing", 0, durationMs)
	start.SessionID = sessionID
	mustEvent(t, ctx, s, start)
	heartbeat := sessionEvent(trackID, 2, "heartbeat", "playing", positionMs, durationMs)
	heartbeat.SessionID = sessionID
	return mustEvent(t, ctx, s, heartbeat)
}

type sessionState struct {
	state, endedAt, endReason, countedAt, startedAt, heartbeat, chainID string
	seq, positionMs                                                     int64
}

func readSession(t *testing.T, ctx context.Context, s *Store, sessionID string) sessionState {
	t.Helper()
	var v sessionState
	err := s.db.QueryRowContext(ctx, `SELECT state,seq,position_ms,COALESCE(ended_at,''),COALESCE(end_reason,''),COALESCE(counted_at,''),started_at,last_heartbeat_at,chain_id FROM playback_sessions WHERE session_id=?`, sessionID).
		Scan(&v.state, &v.seq, &v.positionMs, &v.endedAt, &v.endReason, &v.countedAt, &v.startedAt, &v.heartbeat, &v.chainID)
	if err != nil {
		t.Fatalf("read session %s: %v", sessionID, err)
	}
	return v
}

// ageSession rewrites the stored heartbeat (and state) to simulate the
// passage of time without sleeping.
func ageSession(t *testing.T, ctx context.Context, s *Store, sessionID, state string, age time.Duration) {
	t.Helper()
	when := time.Now().UTC().Add(-age).Format(playbackTimeLayout)
	if _, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET state=?,last_heartbeat_at=? WHERE session_id=?`, state, when, sessionID); err != nil {
		t.Fatal(err)
	}
}

func progressOf(t *testing.T, ctx context.Context, s *Store, trackID int64) (position, playCount, skipCount int64, state string) {
	t.Helper()
	err := s.db.QueryRowContext(ctx, `SELECT position_ms,play_count,skip_count,state FROM playback_progress WHERE track_id=?`, trackID).Scan(&position, &playCount, &skipCount, &state)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	return position, playCount, skipCount, state
}

func historyState(t *testing.T, ctx context.Context, s *Store, trackID int64) string {
	t.Helper()
	history, total, err := s.PlaybackHistory(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range history {
		if record.TrackID == trackID {
			return record.State
		}
	}
	t.Fatalf("track %d not in history (total %d)", trackID, total)
	return ""
}

func TestPlaybackSessionLifecycleAndIdempotentStart(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	// A heartbeat must never create a session (H1).
	orphan := sessionEvent(trackID, 1, "heartbeat", "playing", 1000, 240000)
	if _, err := s.RecordPlaybackEvent(ctx, orphan); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("heartbeat before start: %v", err)
	}

	start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	result := mustEvent(t, ctx, s, start)
	if !result.Applied || result.State != "playing" || result.Counted {
		t.Fatalf("start result: %+v", result)
	}
	if got := historyState(t, ctx, s, trackID); got != "playing" {
		t.Fatalf("history state = %q, want playing", got)
	}

	// Idempotent start retry: same payload, must not reset state/seq/lease.
	heartbeat := sessionEvent(trackID, 2, "heartbeat", "playing", 15000, 240000)
	heartbeat.SessionID = start.SessionID
	mustEvent(t, ctx, s, heartbeat)
	time.Sleep(5 * time.Millisecond)
	replay := mustEvent(t, ctx, s, start)
	if replay.Applied {
		t.Fatalf("start replay applied: %+v", replay)
	}
	row := readSession(t, ctx, s, start.SessionID)
	if row.seq != 2 || row.positionMs != 15000 {
		t.Fatalf("start replay overwrote session: %+v", row)
	}
}

func TestPlaybackSessionOutOfOrderAndTerminal(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)
	id := "session-order-test"

	start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	start.SessionID = id
	mustEvent(t, ctx, s, start)

	hb3 := sessionEvent(trackID, 3, "heartbeat", "playing", 30000, 240000)
	hb3.SessionID = id
	if result := mustEvent(t, ctx, s, hb3); !result.Applied {
		t.Fatalf("seq 3 not applied: %+v", result)
	}
	heartbeatAt := readSession(t, ctx, s, id).heartbeat

	// A late seq-2 event neither renews the lease nor moves the breakpoint (H1).
	time.Sleep(5 * time.Millisecond)
	hb2 := sessionEvent(trackID, 2, "heartbeat", "playing", 20000, 240000)
	hb2.SessionID = id
	if result := mustEvent(t, ctx, s, hb2); result.Applied {
		t.Fatalf("stale seq applied: %+v", result)
	}
	row := readSession(t, ctx, s, id)
	if row.positionMs != 30000 || row.heartbeat != heartbeatAt {
		t.Fatalf("stale event mutated session: %+v", row)
	}
	if position, _, _, _ := progressOf(t, ctx, s, trackID); position != 30000 {
		t.Fatalf("stale event moved breakpoint: %d", position)
	}

	// Terminal end, then a late heartbeat must not revive the session (H1).
	end := sessionEvent(trackID, 4, "end", "", 239000, 240000)
	end.SessionID = id
	end.EndReason = "completed"
	if result := mustEvent(t, ctx, s, end); !result.Applied || result.State != "ended" {
		t.Fatalf("end: %+v", result)
	}
	if position, playCount, _, _ := progressOf(t, ctx, s, trackID); position != 0 || playCount != 1 {
		t.Fatalf("completed: position=%d playCount=%d", position, playCount)
	}
	hb5 := sessionEvent(trackID, 5, "heartbeat", "playing", 240000, 240000)
	hb5.SessionID = id
	if result := mustEvent(t, ctx, s, hb5); result.Applied || result.State != "ended" {
		t.Fatalf("post-end heartbeat: %+v", result)
	}
	// End retry with the same seq is an idempotent no-op (no double skip/count).
	if result := mustEvent(t, ctx, s, end); result.Applied {
		t.Fatalf("end retry applied: %+v", result)
	}
	// A start replay on an ended session cannot revive it either.
	if result := mustEvent(t, ctx, s, start); result.Applied || result.State != "ended" {
		t.Fatalf("start on ended session: %+v", result)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("play count changed after terminal events")
	}
}

func TestPlaybackSessionLeaseBoundaries(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	cases := []struct {
		name  string
		state string
		age   time.Duration
		want  bool // event applies
	}{
		{"playing inside lease", "playing", 89 * time.Second, true},
		{"playing past lease", "playing", 91 * time.Second, false},
		{"buffering inside lease", "buffering", 89 * time.Second, true},
		{"buffering past lease", "buffering", 91 * time.Second, false},
		{"paused inside lease", "paused", 599 * time.Second, true},
		{"paused past lease", "paused", 601 * time.Second, false},
		{"paused past active lease survives", "paused", 120 * time.Second, true},
	}
	for i, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			id := fmt.Sprintf("session-lease-%d", i)
			start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
			start.SessionID = id
			mustEvent(t, ctx, s, start)
			ageSession(t, ctx, s, id, test.state, test.age)

			heartbeat := sessionEvent(trackID, 2, "heartbeat", test.state, 5000, 240000)
			heartbeat.SessionID = id
			result, err := s.RecordPlaybackEvent(ctx, heartbeat)
			if test.want {
				if err != nil || !result.Applied {
					t.Fatalf("inside lease: %+v %v", result, err)
				}
				return
			}
			if !errors.Is(err, ErrSessionExpired) {
				t.Fatalf("past lease: %v", err)
			}
			// The 409 finalized the session in the same transaction (H2).
			row := readSession(t, ctx, s, id)
			if row.endedAt == "" || row.endReason != "expired" {
				t.Fatalf("expired session not persisted: %+v", row)
			}
			// The position is kept for a later resume.
			if position, _, _, _ := progressOf(t, ctx, s, trackID); position != 0 {
				t.Fatalf("expired event moved breakpoint: %d", position)
			}
			// A retry now hits the finalized row and still gets 409.
			if _, err := s.RecordPlaybackEvent(ctx, heartbeat); !errors.Is(err, ErrSessionExpired) {
				t.Fatalf("retry after expiry: %v", err)
			}
		})
	}
}

func TestPlaybackSessionSweeperAndDerivedExpiry(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	start := sessionEvent(trackID, 1, "start", "playing", 123000, 240000)
	mustEvent(t, ctx, s, start)
	ageSession(t, ctx, s, start.SessionID, "playing", 2*time.Minute)

	// Reads derive expiry without the sweeper: an open session past its
	// lease already shows interrupted, never playing (B1/H2/M5).
	if got := historyState(t, ctx, s, trackID); got != "interrupted" {
		t.Fatalf("derived history state = %q, want interrupted", got)
	}
	fixed, err := s.FixExpiredPlaybackSessions(ctx)
	if err != nil || fixed != 1 {
		t.Fatalf("fixed = %d, err = %v", fixed, err)
	}
	if got := historyState(t, ctx, s, trackID); got != "interrupted" {
		t.Fatalf("post-sweep history = %q, want interrupted", got)
	}
	if fixed, _ = s.FixExpiredPlaybackSessions(ctx); fixed != 0 {
		t.Fatalf("second sweep fixed %d", fixed)
	}
	// Expiry keeps the breakpoint; it is not a skip and not a completion.
	if position, _, skipCount, _ := progressOf(t, ctx, s, trackID); position != 123000 || skipCount != 0 {
		t.Fatalf("expired: position=%d skipCount=%d", position, skipCount)
	}
}

func TestPlaybackSessionSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	trackID := seedSessionTrack(t, ctx, s, 240000)
	start := sessionEvent(trackID, 1, "start", "playing", 5000, 240000)
	if _, err := s.RecordPlaybackEvent(ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen: the session is still valid (B1) and heartbeats apply.
	s, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	heartbeat := sessionEvent(trackID, 2, "heartbeat", "playing", 10000, 240000)
	heartbeat.SessionID = start.SessionID
	if result, err := s.RecordPlaybackEvent(ctx, heartbeat); err != nil || !result.Applied {
		t.Fatalf("post-restart heartbeat: %+v %v", result, err)
	}
	if got := historyState(t, ctx, s, trackID); got != "playing" {
		t.Fatalf("post-restart history = %q", got)
	}

	// And a session whose lease ran out during downtime expires on read.
	ageSession(t, ctx, s, start.SessionID, "playing", 3*time.Minute)
	if got := historyState(t, ctx, s, trackID); got != "interrupted" {
		t.Fatalf("downtime expiry not derived: %q", got)
	}
}

func TestPlaybackSessionResumeChain(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)
	otherTrack := seedSessionTrack(t, ctx, s, 240000)

	counted := sessionEvent(trackID, 1, "start", "playing", 130000, 240000) // past 50%: counted at start
	counted.SessionID = "session-resume-src"
	if result := mustEvent(t, ctx, s, counted); !result.Counted {
		t.Fatalf("source session not counted: %+v", result)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatal("source not counted once")
	}
	ageSession(t, ctx, s, counted.SessionID, "playing", 2*time.Minute)

	resume := func(sessionID, clientID string, track int64, from string) (PlaybackEventResult, error) {
		input := sessionEvent(track, 1, "start", "playing", 130000, 240000)
		input.SessionID = sessionID
		input.ClientID = clientID
		input.ResumedFromSessionID = from
		return s.RecordPlaybackEvent(ctx, input)
	}

	// A valid resume inherits the counted flag: no double count (H2/H3).
	result, err := resume("session-resume-a", "device-1", trackID, counted.SessionID)
	if err != nil || !result.Applied || !result.Counted {
		t.Fatalf("resume: %+v %v", result, err)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("resume double-counted: %d", playCount)
	}
	// The open-but-expired source was finalized as part of the resume.
	if row := readSession(t, ctx, s, counted.SessionID); row.endReason != "expired" {
		t.Fatalf("source not finalized: %+v", row)
	}
	// The resume keeps counting suppressed for the rest of the session.
	hb := sessionEvent(trackID, 2, "heartbeat", "playing", 200000, 240000)
	hb.SessionID = "session-resume-a"
	if result := mustEvent(t, ctx, s, hb); !result.Counted {
		t.Fatalf("resume session lost counted flag: %+v", result)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("resume recount: %d", playCount)
	}

	// Forks of the chain inherit too: they can never double-count.
	result, err = resume("session-resume-fork", "device-1", trackID, counted.SessionID)
	if err != nil || !result.Counted {
		t.Fatalf("fork: %+v %v", result, err)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("fork double-counted: %d", playCount)
	}

	// Wrong client, wrong track, unknown predecessor: all rejected.
	if _, err := resume("session-resume-b", "device-2", trackID, counted.SessionID); !errors.Is(err, ErrResumeInvalid) {
		t.Fatalf("wrong client: %v", err)
	}
	if _, err := resume("session-resume-c", "device-1", otherTrack, counted.SessionID); !errors.Is(err, ErrResumeInvalid) {
		t.Fatalf("wrong track: %v", err)
	}
	if _, err := resume("session-resume-d", "device-1", trackID, "session-missing-1"); !errors.Is(err, ErrResumeInvalid) {
		t.Fatalf("unknown predecessor: %v", err)
	}
	// Resuming from a session that is still active supersedes it: the
	// predecessor is ended as replaced in the same transaction and the new
	// session joins its chain (M-3).
	active := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	active.SessionID = "session-resume-live"
	mustEvent(t, ctx, s, active)
	result, err = resume("session-resume-e", "device-1", trackID, active.SessionID)
	if err != nil || !result.Applied {
		t.Fatalf("resume replacing active session: %+v %v", result, err)
	}
	if row := readSession(t, ctx, s, active.SessionID); row.endReason != "replaced" {
		t.Fatalf("active predecessor not replaced: %+v", row)
	}
	if row := readSession(t, ctx, s, "session-resume-e"); row.chainID != active.SessionID {
		t.Fatalf("replacement not chained: %+v", row)
	}
	// A replaced session already named its successor: resuming from it
	// again would fork the chain and is rejected (M-3).
	if _, err := resume("session-resume-f", "device-1", trackID, active.SessionID); !errors.Is(err, ErrResumeInvalid) {
		t.Fatalf("resume from replaced session: %v", err)
	}
}

func TestPlaybackSessionMultiDeviceAndRepeatCounting(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	// Two devices playing the same track independently count separately (H3).
	playSessionOnce(t, ctx, s, "session-device-a", trackID, 130000, 240000)
	other := sessionEvent(trackID, 1, "start", "playing", 130000, 240000)
	other.SessionID = "session-device-b"
	other.ClientID = "device-2"
	other.ClientKind = "web"
	if result := mustEvent(t, ctx, s, other); !result.Counted {
		t.Fatalf("second device not counted: %+v", result)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 2 {
		t.Fatalf("multi-device play count = %d, want 2", playCount)
	}

	// A new session after a completed end counts again (loop/new play).
	end := sessionEvent(trackID, 3, "end", "", 240000, 240000)
	end.SessionID = "session-device-a"
	end.EndReason = "completed"
	mustEvent(t, ctx, s, end)
	playSessionOnce(t, ctx, s, "session-device-c", trackID, 125000, 240000)
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 3 {
		t.Fatalf("replay play count = %d, want 3", playCount)
	}
}

func TestPlaybackSessionCountThreshold(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	// Library duration 240s: slack = min(1000, 24000) = 1000 → threshold 119000.
	trackID := seedSessionTrack(t, ctx, s, 240000)

	start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	mustEvent(t, ctx, s, start)
	below := sessionEvent(trackID, 2, "heartbeat", "playing", 118999, 240000)
	below.SessionID = start.SessionID
	if result := mustEvent(t, ctx, s, below); result.Counted {
		t.Fatalf("below threshold counted: %+v", result)
	}
	at := sessionEvent(trackID, 3, "seek", "", 119000, 240000)
	at.SessionID = start.SessionID
	if result := mustEvent(t, ctx, s, at); !result.Counted {
		t.Fatalf("threshold seek not counted: %+v", result)
	}
	// Consecutive seeks past the threshold never recount.
	for seq := int64(4); seq <= 6; seq++ {
		seek := sessionEvent(trackID, seq, "seek", "", 130000+seq*1000, 240000)
		seek.SessionID = start.SessionID
		mustEvent(t, ctx, s, seek)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("play count = %d, want 1", playCount)
	}

	// Client duration fallback when the library duration is unknown.
	noDuration := seedSessionTrack(t, ctx, s, 0)
	start2 := sessionEvent(noDuration, 1, "start", "playing", 0, 200000)
	start2.SessionID = "session-client-duration"
	mustEvent(t, ctx, s, start2)
	below2 := sessionEvent(noDuration, 2, "heartbeat", "playing", 98999, 200000)
	below2.SessionID = start2.SessionID
	if result := mustEvent(t, ctx, s, below2); result.Counted {
		t.Fatalf("client-duration below threshold counted: %+v", result)
	}
	half2 := sessionEvent(noDuration, 3, "heartbeat", "playing", 99000, 200000)
	half2.SessionID = start2.SessionID
	if result := mustEvent(t, ctx, s, half2); !result.Counted {
		t.Fatalf("client-duration half not counted: %+v", result)
	}

	// Completely unknown duration never counts.
	never := seedSessionTrack(t, ctx, s, 0)
	start3 := sessionEvent(never, 1, "start", "playing", 999999, 0)
	start3.SessionID = "session-noduration"
	if result := mustEvent(t, ctx, s, start3); result.Counted {
		t.Fatalf("unknown duration counted: %+v", result)
	}
}

func TestPlaybackSessionSkipCountsOnce(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000) // threshold min(30s, 120s) = 30000

	endSession := func(id string, seq, position int64, reason string) PlaybackEventResult {
		start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
		start.SessionID = id
		mustEvent(t, ctx, s, start)
		end := sessionEvent(trackID, seq, "end", "", position, 240000)
		end.SessionID = id
		end.EndReason = reason
		return mustEvent(t, ctx, s, end)
	}

	// Explicit skipped below MIN(30s, duration/2) counts exactly once (H4).
	endSession("session-skip-1", 2, 0, "skipped")
	if _, _, skipCount, _ := progressOf(t, ctx, s, trackID); skipCount != 1 {
		t.Fatalf("skip count = %d, want 1", skipCount)
	}
	// A retried end must not double the skip.
	retry := sessionEvent(trackID, 2, "end", "", 0, 240000)
	retry.SessionID = "session-skip-1"
	retry.EndReason = "skipped"
	if result := mustEvent(t, ctx, s, retry); result.Applied {
		t.Fatalf("skip end retry applied: %+v", result)
	}
	if _, _, skipCount, _ := progressOf(t, ctx, s, trackID); skipCount != 1 {
		t.Fatalf("skip retry double-counted: %d", skipCount)
	}

	// Skipped past the threshold does not count as a skip.
	endSession("session-skip-2", 2, 31000, "skipped")
	// Other end reasons below the threshold never count as skips.
	for i, reason := range []string{"stopped", "replaced", "error", "client_closed", "completed"} {
		endSession(fmt.Sprintf("session-skip-r%d", i), 2, 1000, reason)
	}
	// An expired session below the threshold is not a skip either.
	start := sessionEvent(trackID, 1, "start", "playing", 5000, 240000)
	start.SessionID = "session-skip-exp"
	mustEvent(t, ctx, s, start)
	ageSession(t, ctx, s, start.SessionID, "playing", 3*time.Minute)
	if _, err := s.FixExpiredPlaybackSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, skipCount, _ := progressOf(t, ctx, s, trackID); skipCount != 1 {
		t.Fatalf("skip count = %d, want still 1", skipCount)
	}

	// Unknown duration falls back to the 30 s threshold.
	unknown := seedSessionTrack(t, ctx, s, 0)
	start2 := sessionEvent(unknown, 1, "start", "playing", 0, 0)
	start2.SessionID = "session-skip-unk"
	mustEvent(t, ctx, s, start2)
	end2 := sessionEvent(unknown, 2, "end", "", 29999, 0)
	end2.SessionID = start2.SessionID
	end2.EndReason = "skipped"
	mustEvent(t, ctx, s, end2)
	var skipCount int64
	if err := s.db.QueryRowContext(ctx, `SELECT skip_count FROM playback_progress WHERE track_id=?`, unknown).Scan(&skipCount); err != nil || skipCount != 1 {
		t.Fatalf("unknown-duration skip = %d, %v", skipCount, err)
	}
}

func TestPlaybackSessionLastFMSeparation(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedScrobbleTrack(t, s, ctx, "session-lastfm.flac", 200000)

	// Counting no longer writes playback_progress.state: it stays the
	// default 'stopped' while the session is actively playing (B4).
	start := sessionEvent(trackID, 1, "start", "playing", 110000, 200000)
	if result := mustEvent(t, ctx, s, start); !result.Counted || result.QueuedForLastFM {
		t.Fatalf("disconnected lastfm: %+v", result)
	}
	_, playCount, _, progressState := progressOf(t, ctx, s, trackID)
	if playCount != 1 || progressState != "stopped" {
		t.Fatalf("progress state = %q playCount = %d", progressState, playCount)
	}
	if got := historyState(t, ctx, s, trackID); got != "playing" {
		t.Fatalf("history state = %q, want playing from session", got)
	}

	// Connected: the same play in a new session enqueues Last.fm with
	// startedAt = session start − initial position (H3).
	if err := s.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastFMSession(ctx, "listener", "session-key"); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	second := sessionEvent(trackID, 1, "start", "playing", 110000, 200000)
	second.SessionID = "session-lastfm-2"
	if result := mustEvent(t, ctx, s, second); !result.QueuedForLastFM {
		t.Fatalf("connected not queued: %+v", result)
	}
	var startedAt int64
	if err := s.db.QueryRowContext(ctx, `SELECT started_at FROM lastfm_scrobble_queue ORDER BY id DESC LIMIT 1`).Scan(&startedAt); err != nil {
		t.Fatal(err)
	}
	want := before.Add(-110 * time.Second).Unix()
	if startedAt < want-10 || startedAt > time.Now().Add(-100*time.Second).Unix() {
		t.Fatalf("lastfm startedAt = %d, want ≈ %d", startedAt, want)
	}

	// Tracks of 30 s or less count locally but are not scrobbled.
	short := seedScrobbleTrack(t, s, ctx, "session-short.flac", 25000)
	shortStart := sessionEvent(short, 1, "start", "playing", 20000, 25000)
	shortStart.SessionID = "session-lastfm-short"
	if result := mustEvent(t, ctx, s, shortStart); !result.Counted || result.QueuedForLastFM {
		t.Fatalf("short track: %+v", result)
	}
}

func TestPlaybackHistoryDerivation(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	legacy := seedSessionTrack(t, ctx, s, 240000)
	multi := seedSessionTrack(t, ctx, s, 240000)

	// A pre-migration row (no sessions) never shows the old stored playing.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO playback_progress(track_id,state,position_ms,duration_ms,last_played_at) VALUES(?,'playing',1000,240000,strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, legacy); err != nil {
		t.Fatal(err)
	}
	if got := historyState(t, ctx, s, legacy); got != "stopped" {
		t.Fatalf("legacy row state = %q, want stopped", got)
	}

	// Multi-device priority: playing > buffering > paused. The paused
	// device starts playing first (a paused-only session never creates a
	// history row, L-4) and then pauses.
	paused := sessionEvent(multi, 1, "start", "playing", 1000, 240000)
	paused.SessionID = "session-prio-paused"
	paused.ClientID = "device-2"
	mustEvent(t, ctx, s, paused)
	pauseEvent := sessionEvent(multi, 2, "pause", "", 1000, 240000)
	pauseEvent.SessionID = paused.SessionID
	pauseEvent.ClientID = "device-2"
	mustEvent(t, ctx, s, pauseEvent)
	if got := historyState(t, ctx, s, multi); got != "paused" {
		t.Fatalf("priority paused = %q", got)
	}
	buffering := sessionEvent(multi, 1, "start", "buffering", 2000, 240000)
	buffering.SessionID = "session-prio-buf"
	mustEvent(t, ctx, s, buffering)
	if got := historyState(t, ctx, s, multi); got != "buffering" {
		t.Fatalf("priority buffering = %q", got)
	}
	playing := sessionEvent(multi, 1, "start", "playing", 3000, 240000)
	playing.SessionID = "session-prio-play"
	playing.ClientID = "device-3"
	mustEvent(t, ctx, s, playing)
	if got := historyState(t, ctx, s, multi); got != "playing" {
		t.Fatalf("priority playing = %q", got)
	}

	// Once the playing session ends, the most recent end reason wins.
	end := sessionEvent(multi, 2, "end", "", 5000, 240000)
	end.SessionID = playing.SessionID
	end.ClientID = "device-3"
	end.EndReason = "error"
	mustEvent(t, ctx, s, end)
	if got := historyState(t, ctx, s, multi); got != "buffering" {
		t.Fatalf("after end state = %q, want buffering", got)
	}
	// Expire the remaining open sessions: latest end reason (error) shows.
	ageSession(t, ctx, s, paused.SessionID, "paused", 11*time.Minute)
	ageSession(t, ctx, s, buffering.SessionID, "buffering", 2*time.Minute)
	if got := historyState(t, ctx, s, multi); got != "error" {
		t.Fatalf("ended reason state = %q, want error", got)
	}
	// Finalize the expired sessions, age them past the retention window and
	// purge: nothing may resurrect a stale playing afterwards (M5/M6).
	if _, err := s.FixExpiredPlaybackSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeEndedPlaybackSessions(ctx, time.Now().Add(PlaybackEndedRetention+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := historyState(t, ctx, s, multi); got != "stopped" {
		t.Fatalf("post-purge state = %q, want stopped", got)
	}
}

func TestClearPlaybackHistoryClearsSessions(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)
	playSessionOnce(t, ctx, s, "session-clear-1", trackID, 130000, 240000)

	if err := s.ClearPlaybackHistory(ctx); err != nil {
		t.Fatal(err)
	}
	var progressRows, sessionRows int64
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM playback_progress),(SELECT COUNT(*) FROM playback_sessions)`).Scan(&progressRows, &sessionRows); err != nil {
		t.Fatal(err)
	}
	if progressRows != 0 || sessionRows != 0 {
		t.Fatalf("clear left rows: progress=%d sessions=%d", progressRows, sessionRows)
	}
	if _, total, err := s.PlaybackHistory(ctx, 100, 0); err != nil || total != 0 {
		t.Fatalf("history after clear: total=%d err=%v", total, err)
	}

	// The active client gets session_not_found on its next heartbeat (M6)…
	heartbeat := sessionEvent(trackID, 3, "heartbeat", "playing", 140000, 240000)
	heartbeat.SessionID = "session-clear-1"
	if _, err := s.RecordPlaybackEvent(ctx, heartbeat); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("post-clear heartbeat: %v", err)
	}
	// …and a fresh start rebuilds with no inherited state (new play semantics).
	restart := sessionEvent(trackID, 1, "start", "playing", 140000, 240000)
	restart.SessionID = "session-clear-2"
	if result := mustEvent(t, ctx, s, restart); !result.Applied || !result.Counted {
		t.Fatalf("post-clear restart: %+v", result)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("post-clear play count = %d, want 1", playCount)
	}
}

func TestPlaybackEventValidation(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	valid := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	cases := []struct {
		name   string
		mutate func(*PlaybackEventInput)
	}{
		{"empty clientId", func(in *PlaybackEventInput) { in.ClientID = "" }},
		{"clientId too long", func(in *PlaybackEventInput) { in.ClientID = strings.Repeat("c", 65) }},
		{"clientId bad chars", func(in *PlaybackEventInput) { in.ClientID = "bad client!" }},
		{"bad clientKind", func(in *PlaybackEventInput) { in.ClientKind = "sonos" }},
		{"sessionId too short", func(in *PlaybackEventInput) { in.SessionID = "short" }},
		{"sessionId bad chars", func(in *PlaybackEventInput) { in.SessionID = "has space here" }},
		{"seq zero", func(in *PlaybackEventInput) { in.Seq = 0 }},
		{"seq over 2^53", func(in *PlaybackEventInput) { in.Seq = MaxPlaybackSeq + 1 }},
		{"negative track", func(in *PlaybackEventInput) { in.TrackID = -1 }},
		{"negative position", func(in *PlaybackEventInput) { in.PositionMillis = -1 }},
		{"duration over 24h", func(in *PlaybackEventInput) { in.DurationMillis = 24*60*60*1000 + 1 }},
		{"bad type", func(in *PlaybackEventInput) { in.Type = "explode" }},
		{"start without state", func(in *PlaybackEventInput) { in.State = "" }},
		{"heartbeat without state", func(in *PlaybackEventInput) { in.Type = "heartbeat"; in.State = "" }},
		{"pause carrying state", func(in *PlaybackEventInput) { in.Type = "pause" }},
		{"endReason on start", func(in *PlaybackEventInput) { in.EndReason = "skipped" }},
		{"end without reason", func(in *PlaybackEventInput) { in.Type = "end" }},
		{"end bad reason", func(in *PlaybackEventInput) { in.Type = "end"; in.EndReason = "lost" }},
		{"resume flag on heartbeat", func(in *PlaybackEventInput) { in.Type = "heartbeat"; in.ResumedFromSessionID = "session-other-1" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if _, err := s.RecordPlaybackEvent(ctx, input); err == nil {
				t.Fatalf("invalid input accepted: %+v", input)
			}
		})
	}

	// Position beyond duration+5s is clamped, not rejected (M7).
	clamped := sessionEvent(trackID, 1, "start", "playing", 300000, 240000)
	clamped.SessionID = "session-clamp"
	result := mustEvent(t, ctx, s, clamped)
	if result.PositionMillis != 245000 {
		t.Fatalf("clamped position = %d, want 245000", result.PositionMillis)
	}

	// Ownership and immutability (M7/H1).
	mine := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	mine.SessionID = "session-owner"
	mustEvent(t, ctx, s, mine)
	stranger := sessionEvent(trackID, 2, "heartbeat", "playing", 1000, 240000)
	stranger.SessionID = mine.SessionID
	stranger.ClientID = "device-2"
	if _, err := s.RecordPlaybackEvent(ctx, stranger); !errors.Is(err, ErrSessionOwnerMismatch) {
		t.Fatalf("owner mismatch: %v", err)
	}
	otherTrack := seedSessionTrack(t, ctx, s, 240000)
	wrongTrack := sessionEvent(otherTrack, 2, "heartbeat", "playing", 1000, 240000)
	wrongTrack.SessionID = mine.SessionID
	if _, err := s.RecordPlaybackEvent(ctx, wrongTrack); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("track conflict: %v", err)
	}
	wrongKind := sessionEvent(trackID, 2, "heartbeat", "playing", 1000, 240000)
	wrongKind.SessionID = mine.SessionID
	wrongKind.ClientKind = "web"
	if _, err := s.RecordPlaybackEvent(ctx, wrongKind); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("kind conflict: %v", err)
	}
	// Rejected events must not renew the lease or move the breakpoint.
	if position, _, _, _ := progressOf(t, ctx, s, trackID); position != 0 {
		t.Fatalf("rejected event moved breakpoint: %d", position)
	}
	if row := readSession(t, ctx, s, mine.SessionID); row.positionMs != 0 || row.seq != 1 {
		t.Fatalf("rejected event mutated session: %+v", row)
	}

	// Unknown track id.
	unknown := sessionEvent(999999, 1, "start", "playing", 0, 240000)
	unknown.SessionID = "session-unknown-track"
	if _, err := s.RecordPlaybackEvent(ctx, unknown); err == nil {
		t.Fatal("unknown track accepted")
	}
}

func TestPlaybackSessionConcurrentWrites(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	// Distinct sessions counting concurrently must all land exactly once (M8).
	const sessions = 8
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start := sessionEvent(trackID, 1, "start", "playing", 130000, 240000)
			start.SessionID = fmt.Sprintf("session-conc-%d", i)
			start.ClientID = fmt.Sprintf("device-%d", i)
			if _, err := s.RecordPlaybackEvent(ctx, start); err != nil {
				t.Errorf("concurrent start %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != sessions {
		t.Fatalf("concurrent play count = %d, want %d", playCount, sessions)
	}

	// Concurrent identical retries of one session count at most once.
	other := seedSessionTrack(t, ctx, s, 240000)
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := sessionEvent(other, 1, "start", "playing", 130000, 240000)
			start.SessionID = "session-conc-same"
			if _, err := s.RecordPlaybackEvent(ctx, start); err != nil {
				t.Errorf("duplicate start: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, playCount, _, _ := progressOf(t, ctx, s, other); playCount != 1 {
		t.Fatalf("duplicate start play count = %d, want 1", playCount)
	}
}

func TestPlaybackSessionPurge(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	old := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	old.SessionID = "session-purge-old"
	mustEvent(t, ctx, s, old)
	oldEnd := sessionEvent(trackID, 2, "end", "", 1000, 240000)
	oldEnd.SessionID = old.SessionID
	oldEnd.EndReason = "stopped"
	mustEvent(t, ctx, s, oldEnd)
	oldWhen := time.Now().UTC().Add(-(PlaybackEndedRetention + time.Hour)).Format(playbackTimeLayout)
	if _, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET ended_at=? WHERE session_id=?`, oldWhen, old.SessionID); err != nil {
		t.Fatal(err)
	}

	recent := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	recent.SessionID = "session-purge-recent"
	mustEvent(t, ctx, s, recent)
	recentEnd := sessionEvent(trackID, 2, "end", "", 1000, 240000)
	recentEnd.SessionID = recent.SessionID
	recentEnd.EndReason = "stopped"
	mustEvent(t, ctx, s, recentEnd)

	open := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	open.SessionID = "session-purge-open"
	mustEvent(t, ctx, s, open)

	purged, err := s.PurgeEndedPlaybackSessions(ctx, time.Now())
	if err != nil || purged != 1 {
		t.Fatalf("purged = %d, err = %v", purged, err)
	}
	var remaining int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playback_sessions`).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("remaining sessions = %d, want 2", remaining)
	}
}

func TestPlaybackSweeperLifecycle(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)
	start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	mustEvent(t, ctx, s, start)
	ageSession(t, ctx, s, start.SessionID, "playing", 2*time.Minute)

	sweeper := NewPlaybackSweeper(s, nil)
	sweeper.ExpireInterval = 20 * time.Millisecond
	sweeper.PurgeInterval = time.Hour
	sweepCtx, stop := context.WithCancel(ctx)
	sweeper.Start(sweepCtx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if row := readSession(t, ctx, s, start.SessionID); row.endReason == "expired" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sweeper did not finalize the expired session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Stop must terminate the goroutine promptly.
	stop()
	time.Sleep(50 * time.Millisecond)
}
