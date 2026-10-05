package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// resumeStart builds a start event that resumes from a predecessor.
func resumeStart(sessionID, clientID string, trackID, seq, position, duration int64, from string) PlaybackEventInput {
	return PlaybackEventInput{
		ClientID: clientID, ClientKind: "android", SessionID: sessionID,
		Seq: seq, Type: "start", TrackID: trackID, State: "playing",
		PositionMillis: position, DurationMillis: duration, ResumedFromSessionID: from,
	}
}

func chainCountedRows(t *testing.T, ctx context.Context, s *Store, chainID string) int64 {
	t.Helper()
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playback_sessions WHERE chain_id=? AND counted_at IS NOT NULL`, chainID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestPlaybackChainForkSequentialUncounted covers the B-1 blocker: two
// sessions resuming sequentially from the same UNCOUNTED predecessor must
// count the chain exactly once — including the Last.fm outbox.
func TestPlaybackChainForkSequentialUncounted(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedScrobbleTrack(t, s, ctx, "chain-seq.flac", 240000)
	if err := s.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastFMSession(ctx, "listener", "session-key"); err != nil {
		t.Fatal(err)
	}

	// P starts at 0% and loses contact before ever being counted.
	root := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	root.SessionID = "chain-root-seq"
	mustEvent(t, ctx, s, root)
	ageSession(t, ctx, s, root.SessionID, "playing", 2*time.Minute)

	// A resumes from P at 55% and crosses the threshold: the chain counts.
	result := mustEvent(t, ctx, s, resumeStart("chain-fork-a", "device-1", trackID, 1, 130000, 240000, root.SessionID))
	if !result.Applied || !result.Counted {
		t.Fatalf("first resume: %+v", result)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("play count after A = %d, want 1", playCount)
	}
	// A goes away (client_closed is a resumable end reason).
	endA := sessionEvent(trackID, 2, "end", "", 150000, 240000)
	endA.SessionID = "chain-fork-a"
	endA.EndReason = "client_closed"
	mustEvent(t, ctx, s, endA)

	// B resumes from the SAME predecessor P (uncounted itself): the chain
	// already counted through A, so B must not count again (B-1).
	result = mustEvent(t, ctx, s, resumeStart("chain-fork-b", "device-1", trackID, 1, 130000, 240000, root.SessionID))
	if !result.Applied || !result.Counted {
		t.Fatalf("fork resume must report the chain as counted: %+v", result)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("fork double-counted: play count = %d, want 1", playCount)
	}
	if got := chainCountedRows(t, ctx, s, root.SessionID); got != 1 {
		t.Fatalf("counted rows in chain = %d, want 1", got)
	}
	if got := queueLength(t, s, ctx); got != 1 {
		t.Fatalf("lastfm queue = %d, want 1", got)
	}
	// B plays on past the threshold: still no recount by the sibling.
	hb := sessionEvent(trackID, 2, "heartbeat", "playing", 200000, 240000)
	hb.SessionID = "chain-fork-b"
	mustEvent(t, ctx, s, hb)
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("sibling recounted: play count = %d", playCount)
	}
}

// TestPlaybackChainForkConcurrentUncounted races N resumes from one
// uncounted predecessor; exactly one may count (B-1). Run with -race.
func TestPlaybackChainForkConcurrentUncounted(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	root := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	root.SessionID = "chain-root-conc"
	mustEvent(t, ctx, s, root)
	ageSession(t, ctx, s, root.SessionID, "playing", 2*time.Minute)

	const forks = 8
	var wg sync.WaitGroup
	errs := make([]error, forks)
	for i := 0; i < forks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.RecordPlaybackEvent(ctx, resumeStart(fmt.Sprintf("chain-conc-%d", i), "device-1", trackID, 1, 130000, 240000, root.SessionID))
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("fork %d: %v", i, err)
		}
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("concurrent fork play count = %d, want 1", playCount)
	}
	if got := chainCountedRows(t, ctx, s, root.SessionID); got != 1 {
		t.Fatalf("concurrent fork counted rows = %d, want 1", got)
	}
}

// TestPlaybackChainPurgeKeepsSoleCountedRow: the retention purge must not
// drop a chain's only counted row while any sibling remains (B-1), and must
// drop it once it is the last row of the chain.
func TestPlaybackChainPurgeKeepsSoleCountedRow(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	// A counted session A and its successor B (same chain via resume).
	playSessionOnce(t, ctx, s, "chain-purge-a", trackID, 130000, 240000)
	endA := sessionEvent(trackID, 3, "end", "", 150000, 240000)
	endA.SessionID = "chain-purge-a"
	endA.EndReason = "client_closed"
	mustEvent(t, ctx, s, endA)
	result := mustEvent(t, ctx, s, resumeStart("chain-purge-b", "device-1", trackID, 1, 150000, 240000, "chain-purge-a"))
	if !result.Counted {
		t.Fatalf("successor did not inherit chain count: %+v", result)
	}
	endB := sessionEvent(trackID, 2, "end", "", 240000, 240000)
	endB.SessionID = "chain-purge-b"
	endB.EndReason = "completed"
	mustEvent(t, ctx, s, endB)

	// Age only A past the retention window; B stays recent. The counted
	// row must survive because its chain still has a sibling (B-1).
	old := time.Now().UTC().Add(-(PlaybackEndedRetention + time.Hour)).Format(playbackTimeLayout)
	if _, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET ended_at=? WHERE session_id='chain-purge-a'`, old); err != nil {
		t.Fatal(err)
	}
	purged, err := s.PurgeEndedPlaybackSessions(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if purged != 0 {
		t.Fatalf("first purge removed %d rows, want 0 (counted row protected by sibling)", purged)
	}
	if got := chainCountedRows(t, ctx, s, "chain-purge-a"); got != 1 {
		t.Fatalf("chain count lost after purge: %d", got)
	}
	// Age B as well: the uncounted row goes first and the now-orphaned
	// counted row follows in the same run — the chain cleans up fully once
	// nothing remains to resume from.
	if _, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET ended_at=? WHERE session_id='chain-purge-b'`, old); err != nil {
		t.Fatal(err)
	}
	purged, err = s.PurgeEndedPlaybackSessions(ctx, time.Now())
	if err != nil || purged != 2 {
		t.Fatalf("second purge removed %d rows, want 2", purged)
	}
	var left int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playback_sessions`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("sessions left = %d, want 0", left)
	}
}

// TestPlaybackSessionResumeReasons locks the resumable end reasons (M-3).
func TestPlaybackSessionResumeReasons(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	endWith := func(id, reason string) {
		start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
		start.SessionID = id
		mustEvent(t, ctx, s, start)
		end := sessionEvent(trackID, 2, "end", "", 1000, 240000)
		end.SessionID = id
		end.EndReason = reason
		mustEvent(t, ctx, s, end)
	}

	// Resumable: stopped, error, client_closed, expired.
	for i, reason := range []string{"stopped", "error", "client_closed"} {
		id := fmt.Sprintf("chain-reason-ok-%d", i)
		endWith(id, reason)
		if _, err := s.RecordPlaybackEvent(ctx, resumeStart(id+"-next", "device-1", trackID, 1, 5000, 240000, id)); err != nil {
			t.Fatalf("resume from %s: %v", reason, err)
		}
	}
	// Not resumable: completed, skipped, replaced.
	for i, reason := range []string{"completed", "skipped", "replaced"} {
		id := fmt.Sprintf("chain-reason-no-%d", i)
		if reason == "replaced" {
			// replaced is only written by the server; inject it directly.
			endWith(id, "stopped")
			if _, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET end_reason='replaced' WHERE session_id=?`, id); err != nil {
				t.Fatal(err)
			}
		} else {
			endWith(id, reason)
		}
		if _, err := s.RecordPlaybackEvent(ctx, resumeStart(id+"-next", "device-1", trackID, 1, 5000, 240000, id)); !errors.Is(err, ErrResumeInvalid) {
			t.Fatalf("resume from %s accepted: %v", reason, err)
		}
	}
}

// TestPlaybackExpiryLabelStableAcrossSweep (M-1): expiry finalization uses
// the last heartbeat as ended_at, so the derived label cannot flip when the
// sweeper runs after another session ended more recently.
func TestPlaybackExpiryLabelStableAcrossSweep(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	// Device A loses contact at T-3min; device B completes at T-1min
	// (simulated by backdating its end after the fact).
	lost := sessionEvent(trackID, 1, "start", "playing", 1000, 240000)
	lost.SessionID = "chain-label-lost"
	lost.ClientID = "device-2"
	mustEvent(t, ctx, s, lost)
	ageSession(t, ctx, s, lost.SessionID, "playing", 3*time.Minute)

	other := sessionEvent(trackID, 1, "start", "playing", 5000, 240000)
	other.SessionID = "chain-label-done"
	mustEvent(t, ctx, s, other)
	end := sessionEvent(trackID, 2, "end", "", 240000, 240000)
	end.SessionID = other.SessionID
	end.EndReason = "completed"
	mustEvent(t, ctx, s, end)
	oneMinuteAgo := time.Now().UTC().Add(-time.Minute).Format(playbackTimeLayout)
	if _, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET ended_at=? WHERE session_id=?`, oneMinuteAgo, other.SessionID); err != nil {
		t.Fatal(err)
	}

	// Before the sweep, A (heartbeat T-3min) is interrupted and B
	// (ended T-1min) is the most recent end: completed wins.
	if got := historyState(t, ctx, s, trackID); got != "completed" {
		t.Fatalf("pre-sweep label = %q, want completed", got)
	}
	if _, err := s.FixExpiredPlaybackSessions(ctx); err != nil {
		t.Fatal(err)
	}
	// After the sweep the derived label must be identical (no flip to
	// interrupted): A's ended_at is its T-3min heartbeat, still older.
	if got := historyState(t, ctx, s, trackID); got != "completed" {
		t.Fatalf("post-sweep label = %q, want completed", got)
	}
	row := readSession(t, ctx, s, lost.SessionID)
	if row.endedAt != row.heartbeat {
		t.Fatalf("expired ended_at = %q, want heartbeat %q", row.endedAt, row.heartbeat)
	}
}

// TestPlaybackSkipThresholdEdges backfills the legacy skip-inference edge
// cases onto the end-event rule (M-4): short-track half-length threshold,
// library duration priority, client-duration fallback.
func TestPlaybackSkipThresholdEdges(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)

	skipVia := func(id string, trackID, position, duration int64) {
		start := sessionEvent(trackID, 1, "start", "playing", 0, duration)
		start.SessionID = id
		mustEvent(t, ctx, s, start)
		end := sessionEvent(trackID, 2, "end", "", position, duration)
		end.SessionID = id
		end.EndReason = "skipped"
		mustEvent(t, ctx, s, end)
	}
	skipCountOfTrack := func(trackID int64) int64 {
		var count int64
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT skip_count FROM playback_progress WHERE track_id=?),0)`, trackID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	// Short track (20 s): threshold is half the duration (10 s), not 30 s.
	short := seedSessionTrack(t, ctx, s, 20000)
	skipVia("skip-edge-short-yes", short, 8000, 20000)
	if got := skipCountOfTrack(short); got != 1 {
		t.Fatalf("8s skip of 20s track = %d, want 1", got)
	}
	skipVia("skip-edge-short-no", short, 12000, 20000)
	if got := skipCountOfTrack(short); got != 1 {
		t.Fatalf("12s skip of 20s track = %d, want still 1", got)
	}

	// Library duration wins over the reported one: library 40 s (threshold
	// 20 s) beats the reported 240 s, so a 25 s skip does not count.
	libWins := seedSessionTrack(t, ctx, s, 40000)
	skipVia("skip-edge-libwins", libWins, 25000, 240000)
	if got := skipCountOfTrack(libWins); got != 0 {
		t.Fatalf("library duration ignored: skip = %d, want 0", got)
	}

	// Unknown library duration falls back to the reported duration:
	// reported 240 s → threshold 30 s; a 25 s skip counts.
	fallback := seedSessionTrack(t, ctx, s, 0)
	skipVia("skip-edge-fallback", fallback, 25000, 240000)
	if got := skipCountOfTrack(fallback); got != 1 {
		t.Fatalf("reported-duration fallback skip = %d, want 1", got)
	}

	// TrackByID counter exposure (M-4): a track with no playback history
	// omits the nullable counters; a skipped track exposes them.
	untouched := seedSessionTrack(t, ctx, s, 40000)
	unplayed, err := s.TrackByID(ctx, untouched)
	if err != nil {
		t.Fatal(err)
	}
	if unplayed.ViewCount != nil || unplayed.SkipCount != nil {
		t.Fatalf("unplayed track exposes counters: %+v", unplayed)
	}
	played, err := s.TrackByID(ctx, short)
	if err != nil {
		t.Fatal(err)
	}
	if played.SkipCount == nil || *played.SkipCount != 1 {
		t.Fatalf("played track SkipCount = %v", played.SkipCount)
	}
}

// TestPlaybackPositionClampedByLibraryDuration (L-2): with no reported
// duration the library duration still bounds the position.
func TestPlaybackPositionClampedByLibraryDuration(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 60000) // 60 s library duration

	start := sessionEvent(trackID, 1, "start", "playing", 0, 0)
	start.SessionID = "chain-clamp-lib"
	mustEvent(t, ctx, s, start)
	hb := sessionEvent(trackID, 2, "heartbeat", "playing", 70000, 0)
	hb.SessionID = start.SessionID
	result := mustEvent(t, ctx, s, hb)
	if result.PositionMillis != 65000 {
		t.Fatalf("position = %d, want clamped 65000", result.PositionMillis)
	}
}

// TestPlaybackPausedHeartbeatKeepsHistoryOrder (L-4): paused heartbeats
// must not refresh last_played_at and bump a paused track to the top.
func TestPlaybackPausedHeartbeatKeepsHistoryOrder(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	first := seedSessionTrack(t, ctx, s, 240000)
	second := seedSessionTrack(t, ctx, s, 240000)

	startA := sessionEvent(first, 1, "start", "playing", 1000, 240000)
	startA.SessionID = "chain-order-a"
	mustEvent(t, ctx, s, startA)
	time.Sleep(5 * time.Millisecond)
	startB := sessionEvent(second, 1, "start", "playing", 1000, 240000)
	startB.SessionID = "chain-order-b"
	mustEvent(t, ctx, s, startB)

	lastPlayed := func(trackID int64) string {
		var value string
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(last_played_at,'') FROM playback_progress WHERE track_id=?`, trackID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}

	// A pauses: the pause event itself is play evidence (priorState was
	// playing) and may refresh; from here on, paused heartbeats must not.
	pause := sessionEvent(first, 2, "pause", "", 2000, 240000)
	pause.SessionID = startA.SessionID
	mustEvent(t, ctx, s, pause)
	time.Sleep(5 * time.Millisecond)
	before := lastPlayed(first)
	hb := sessionEvent(first, 3, "heartbeat", "paused", 2000, 240000)
	hb.SessionID = startA.SessionID
	mustEvent(t, ctx, s, hb)
	if got := lastPlayed(first); got != before {
		t.Fatalf("paused heartbeat refreshed last_played_at: %q -> %q", before, got)
	}

	// Ordering: after B's next playing heartbeat, the still-playing track
	// stays on top (A's paused heartbeats never bump it back).
	time.Sleep(5 * time.Millisecond)
	hbB := sessionEvent(second, 2, "heartbeat", "playing", 2000, 240000)
	hbB.SessionID = startB.SessionID
	mustEvent(t, ctx, s, hbB)
	history, _, err := s.PlaybackHistory(ctx, 100, 0)
	if err != nil || len(history) != 2 {
		t.Fatalf("history = %v %v", history, err)
	}
	if history[0].TrackID != second {
		t.Fatalf("history order = [%d %d], want playing track first", history[0].TrackID, history[1].TrackID)
	}

	// A resume (real playback) refreshes again.
	time.Sleep(5 * time.Millisecond)
	resume := sessionEvent(first, 4, "resume", "", 3000, 240000)
	resume.SessionID = startA.SessionID
	mustEvent(t, ctx, s, resume)
	if got := lastPlayed(first); got <= before {
		t.Fatalf("resume did not refresh last_played_at: %q vs %q", got, before)
	}
}

// TestPlaybackPausedOrBufferingStartDoesNotCount (F-1): a fresh session that
// starts paused or buffering at a high position (cold restore without a
// persisted session id) must not count or reach Last.fm until playback
// actually resumes or ends with the true final position.
func TestPlaybackPausedOrBufferingStartDoesNotCount(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedScrobbleTrack(t, s, ctx, "paused-start.flac", 240000)
	if err := s.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLastFMSession(ctx, "listener", "session-key"); err != nil {
		t.Fatal(err)
	}

	// Paused start at 60%: no count, no outbox, no history row yet.
	pausedStart := sessionEvent(trackID, 1, "start", "paused", 145000, 240000)
	pausedStart.SessionID = "chain-paused-start"
	if result := mustEvent(t, ctx, s, pausedStart); result.Counted {
		t.Fatalf("paused start counted: %+v", result)
	}
	// Paused heartbeats at the same position still do not count.
	hbPaused := sessionEvent(trackID, 2, "heartbeat", "paused", 145000, 240000)
	hbPaused.SessionID = pausedStart.SessionID
	if result := mustEvent(t, ctx, s, hbPaused); result.Counted {
		t.Fatalf("paused heartbeat counted: %+v", result)
	}
	if _, total, err := s.PlaybackHistory(ctx, 100, 0); err != nil || total != 0 {
		t.Fatalf("paused-only session visible in history: total=%d", total)
	}

	// Buffering start at 60% on a second track: also no count.
	bufferingTrack := seedScrobbleTrack(t, s, ctx, "buffering-start.flac", 240000)
	bufferingStart := sessionEvent(bufferingTrack, 1, "start", "buffering", 145000, 240000)
	bufferingStart.SessionID = "chain-buffering-start"
	if result := mustEvent(t, ctx, s, bufferingStart); result.Counted {
		t.Fatalf("buffering start counted: %+v", result)
	}

	// Buffering → playing heartbeat counts (playback actually started).
	hbPlaying := sessionEvent(bufferingTrack, 2, "heartbeat", "playing", 145000, 240000)
	hbPlaying.SessionID = bufferingStart.SessionID
	if result := mustEvent(t, ctx, s, hbPlaying); !result.Counted {
		t.Fatalf("playing after buffering not counted: %+v", result)
	}

	// Resume on the paused session counts and becomes visible in history.
	resume := sessionEvent(trackID, 3, "resume", "", 145000, 240000)
	resume.SessionID = pausedStart.SessionID
	if result := mustEvent(t, ctx, s, resume); !result.Counted {
		t.Fatalf("resume not counted: %+v", result)
	}
	if got := historyState(t, ctx, s, trackID); got != "playing" {
		t.Fatalf("history after resume = %q, want playing", got)
	}
	if got := queueLength(t, s, ctx); got != 2 {
		t.Fatalf("lastfm queue = %d, want 2", got)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, trackID); playCount != 1 {
		t.Fatalf("play count = %d, want 1", playCount)
	}

	// A session that only ever reported paused must NOT count when it ends,
	// whatever endReason it ends with (N-1): there is no evidence of actual
	// playback. It must not reach Last.fm or the history list either.
	third := seedScrobbleTrack(t, s, ctx, "paused-end.flac", 240000)
	pausedOnly := sessionEvent(third, 1, "start", "paused", 130000, 240000)
	pausedOnly.SessionID = "chain-paused-end"
	mustEvent(t, ctx, s, pausedOnly)
	queueBefore := queueLength(t, s, ctx)
	end := sessionEvent(third, 2, "end", "", 240000, 240000)
	end.SessionID = pausedOnly.SessionID
	end.EndReason = "stopped"
	if result := mustEvent(t, ctx, s, end); result.Counted {
		t.Fatalf("never-played session counted on end: %+v", result)
	}
	if _, playCount, _, _ := progressOf(t, ctx, s, third); playCount != 0 {
		t.Fatalf("never-played session play count = %d, want 0", playCount)
	}
	if got := queueLength(t, s, ctx); got != queueBefore {
		t.Fatalf("never-played session enqueued lastfm: queue %d -> %d", queueBefore, got)
	}
	var nullPlayed string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(last_played_at,'') FROM playback_progress WHERE track_id=?`, third).Scan(&nullPlayed); err != nil || nullPlayed != "" {
		t.Fatalf("never-played session refreshed last_played_at: %q", nullPlayed)
	}
}

// TestPlaybackCountRequiresPlayEvidence (N-1) locks the counting matrix:
// the state before OR after the event must be playing.
func TestPlaybackCountRequiresPlayEvidence(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)

	playCountOf := func(trackID int64) int64 {
		_, playCount, _, _ := progressOf(t, ctx, s, trackID)
		return playCount
	}
	sessionOn := func(id string, trackID int64) func(eventType, state string, seq, position int64) PlaybackEventResult {
		return func(eventType, state string, seq, position int64) PlaybackEventResult {
			event := sessionEvent(trackID, seq, eventType, state, position, 240000)
			event.SessionID = id
			if eventType == "end" {
				event.EndReason = "stopped"
			}
			return mustEvent(t, ctx, s, event)
		}
	}

	// playing to 40%, then pause at 51%: the pause event carries evidence
	// of playback (priorState playing) and counts.
	t1 := seedSessionTrack(t, ctx, s, 240000)
	s1 := sessionOn("chain-evidence-1", t1)
	s1("start", "playing", 1, 96000)
	if result := s1("pause", "", 2, 123000); !result.Counted {
		t.Fatalf("pause after playing past threshold not counted: %+v", result)
	}

	// playing, then buffering at 51%: counts.
	t2 := seedSessionTrack(t, ctx, s, 240000)
	s2 := sessionOn("chain-evidence-2", t2)
	s2("start", "playing", 1, 96000)
	if result := s2("buffering", "", 2, 123000); !result.Counted {
		t.Fatalf("buffering after playing not counted: %+v", result)
	}

	// playing, then end(stopped) at 51%: counts.
	t3 := seedSessionTrack(t, ctx, s, 240000)
	s3 := sessionOn("chain-evidence-3", t3)
	s3("start", "playing", 1, 96000)
	if result := s3("end", "", 2, 123000); !result.Counted {
		t.Fatalf("end after playing not counted: %+v", result)
	}

	// paused start, seek to 60% while paused, then end: no evidence of
	// playback at any point — must not count.
	t4 := seedSessionTrack(t, ctx, s, 240000)
	s4 := sessionOn("chain-evidence-4", t4)
	s4("start", "paused", 1, 0)
	s4("seek", "", 2, 145000)
	if result := s4("end", "", 3, 145000); result.Counted {
		t.Fatalf("paused seek + end counted: %+v", result)
	}

	if playCountOf(t1) != 1 || playCountOf(t2) != 1 || playCountOf(t3) != 1 || playCountOf(t4) != 0 {
		t.Fatalf("matrix play counts = %d/%d/%d/%d, want 1/1/1/0",
			playCountOf(t1), playCountOf(t2), playCountOf(t3), playCountOf(t4))
	}
}

// TestPlaybackLibraryDurationWinsOverReported (F-3): the reported duration
// must never pre-clamp the position below the library-derived thresholds —
// library 240 s, misreported 60 s, position 130 s must count (and not skip).
func TestPlaybackLibraryDurationWinsOverReported(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	start := sessionEvent(trackID, 1, "start", "playing", 0, 60000)
	start.SessionID = "chain-clamp-priority"
	mustEvent(t, ctx, s, start)
	// With F-3's validate-time pre-clamp this position would have been cut
	// to 65 s (60 s + 5 s), below the 119 s counting threshold.
	hb := sessionEvent(trackID, 2, "heartbeat", "playing", 130000, 60000)
	hb.SessionID = start.SessionID
	if result := mustEvent(t, ctx, s, hb); !result.Counted {
		t.Fatalf("library-duration count suppressed by reported-duration clamp: %+v", result)
	}
	if _, playCount, skipCount, _ := progressOf(t, ctx, s, trackID); playCount != 1 || skipCount != 0 {
		t.Fatalf("play=%d skip=%d, want 1/0", playCount, skipCount)
	}
	// The stored position is the true 130 s, not the clamped 65 s.
	if position, _, _, _ := progressOf(t, ctx, s, trackID); position != 130000 {
		t.Fatalf("position = %d, want 130000", position)
	}
	// And a skip at 25 s (below the library-derived 30 s threshold) counts.
	second := seedSessionTrack(t, ctx, s, 240000)
	start2 := sessionEvent(second, 1, "start", "playing", 0, 60000)
	start2.SessionID = "chain-clamp-priority-2"
	mustEvent(t, ctx, s, start2)
	end := sessionEvent(second, 2, "end", "", 25000, 60000)
	end.SessionID = start2.SessionID
	end.EndReason = "skipped"
	mustEvent(t, ctx, s, end)
	var skips int64
	if err := s.db.QueryRowContext(ctx, `SELECT skip_count FROM playback_progress WHERE track_id=?`, second).Scan(&skips); err != nil || skips != 1 {
		t.Fatalf("library-threshold skip = %d, want 1 (%v)", skips, err)
	}
}

// TestPlaybackLatestEndedTiebreakDeterministic (F-4): identical ended_at
// values resolve to exactly one row, deterministically (highest rowid wins).
func TestPlaybackLatestEndedTiebreakDeterministic(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 240000)

	// Two ended sessions with deliberately identical ended_at but different
	// reasons; the later-inserted row (higher rowid) must win every time.
	endSession := func(id, reason string) {
		start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
		start.SessionID = id
		mustEvent(t, ctx, s, start)
		end := sessionEvent(trackID, 2, "end", "", 1000, 240000)
		end.SessionID = id
		end.EndReason = reason
		mustEvent(t, ctx, s, end)
	}
	endSession("chain-tie-1", "stopped")
	endSession("chain-tie-2", "error")
	sameEnd := time.Now().UTC().Format(playbackTimeLayout)
	if _, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET ended_at=? WHERE session_id IN ('chain-tie-1','chain-tie-2')`, sameEnd); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if got := historyState(t, ctx, s, trackID); got != "error" {
			t.Fatalf("tie run %d: state = %q, want deterministic error (highest rowid)", i, got)
		}
	}
}
