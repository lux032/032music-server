package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"modernc.org/sqlite"
)

// Playback sessions (plan C): every play is a durable row in
// playback_sessions. The active state is derived from last_heartbeat_at and
// a per-state lease at read time, so a crashed client or a server restart
// never leaves a track stuck in "playing"; the background sweeper only
// finalizes what the derivation already considers expired. Counting and
// skip accounting live here, per session, and playback_progress is a pure
// aggregate (position, counters, timestamps) — never the source of the
// active state.
const (
	// PlaybackActiveLease bounds how long a playing/buffering session may
	// go without a heartbeat (clients report every 15 s) before it is
	// treated as interrupted.
	PlaybackActiveLease = 90 * time.Second
	// PlaybackPausedLease bounds paused sessions (clients report every
	// 60 s; background tabs may be throttled to about one timer/minute).
	PlaybackPausedLease = 10 * time.Minute
	// PlaybackActiveLeaseSQL / PlaybackPausedLeaseSQL are the same leases
	// expressed for strftime modifiers so the SQL derivation in the sweeper
	// and the write path uses exactly the read-side rule (H2).
	PlaybackActiveLeaseSQL = "-90 seconds"
	PlaybackPausedLeaseSQL = "-600 seconds"
	// PlaybackEndedRetention is how long ended sessions are kept for the
	// "most recent outcome" history label before the hourly purge.
	PlaybackEndedRetention = 30 * 24 * time.Hour

	// MaxPlaybackSeq keeps seq inside the range JSON clients can represent
	// exactly (2^53).
	MaxPlaybackSeq = int64(1) << 53
	// maxPlaybackEventDurationMillis bounds client-reported durations (M7).
	maxPlaybackEventDurationMillis = 24 * 60 * 60 * 1000 // 24 hours
	// playbackPositionOvershootMillis tolerates players reporting a
	// position slightly past the end; larger values are clamped (M7).
	playbackPositionOvershootMillis = 5000

	// playbackTimeLayout is the strftime('%Y-%m-%dT%H:%M:%fZ','now') shape
	// used for every session timestamp; text values compare lexically.
	playbackTimeLayout = "2006-01-02T15:04:05.000Z"
)

// Sentinel errors mapped to protocol codes by the HTTP layer.
var (
	// ErrInvalidPlaybackEvent wraps every client-input validation failure
	// (enums, bounds, formats) so the HTTP layer maps it to 400 without
	// guessing from message strings (L-1).
	ErrInvalidPlaybackEvent = errors.New("invalid playback event")
	// ErrTrackNotFound: the trackId of a start event does not exist.
	ErrTrackNotFound = errors.New("playback track not found")
	// ErrSessionNotFound: event for a session the server does not have
	// (never started, or removed by ClearPlaybackHistory). The client must
	// open a new session.
	ErrSessionNotFound = errors.New("playback session not found")
	// ErrSessionExpired: the session's lease ran out. The server has
	// finalized it as ended/expired; the client must open a new session
	// with resumedFromSessionId to inherit the counted flag.
	ErrSessionExpired = errors.New("playback session expired")
	// ErrSessionOwnerMismatch: the session belongs to another clientId.
	ErrSessionOwnerMismatch = errors.New("playback session belongs to a different client")
	// ErrSessionConflict: trackId/clientKind differ from the session;
	// both are immutable for the life of a session.
	ErrSessionConflict = errors.New("session track or client kind mismatch")
	// ErrResumeInvalid: resumedFromSessionId is unknown, belongs to a
	// different client or track, or ended with a non-resumable reason
	// (completed/skipped/replaced). See the protocol for the recovery rule.
	ErrResumeInvalid = errors.New("invalid resumedFromSessionId")
)

// resumableEndReason lists the end reasons a session may be resumed from
// (M-3): interruptions, not deliberate completions. completed and skipped
// plays are finished business; replaced names a session already superseded
// by a resume, so allowing it would fork the chain.
func resumableEndReason(reason string) bool {
	switch reason {
	case "expired", "client_closed", "error", "stopped":
		return true
	}
	return false
}

// playbackIDPattern bounds clientId/sessionId to safe, small tokens (M7).
var playbackIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

type PlaybackEventInput struct {
	ClientID             string `json:"clientId"`
	ClientKind           string `json:"clientKind"`
	SessionID            string `json:"sessionId"`
	Seq                  int64  `json:"seq"`
	Type                 string `json:"type"`
	TrackID              int64  `json:"trackId"`
	State                string `json:"state,omitempty"`
	PositionMillis       int64  `json:"positionMillis"`
	DurationMillis       int64  `json:"durationMillis"`
	EndReason            string `json:"endReason,omitempty"`
	ResumedFromSessionID string `json:"resumedFromSessionId,omitempty"`
}

type PlaybackEventResult struct {
	Applied        bool   `json:"applied"`
	SessionID      string `json:"sessionId"`
	State          string `json:"state"`
	PositionMillis int64  `json:"positionMillis"`
	// Counted reports that this play has been counted (by this session or
	// inherited from the session it resumed from).
	Counted bool `json:"counted"`
	// QueuedForLastFM reports that this event enqueued a Last.fm scrobble.
	QueuedForLastFM bool `json:"-"`
}

func activePlaybackState(state string) bool {
	return state == "playing" || state == "buffering" || state == "paused"
}

func validEndReason(reason string) bool {
	switch reason {
	case "completed", "skipped", "stopped", "replaced", "error", "client_closed":
		return true
	}
	return false
}

// validatePlaybackEvent enforces the wire contract (M7). Positions past the
// end are not judged here (F-3): clampPlaybackPosition clamps them against
// the effective duration inside the transaction.
func validatePlaybackEvent(input *PlaybackEventInput) error {
	if len(input.ClientID) == 0 || len(input.ClientID) > 64 || !playbackIDPattern.MatchString(input.ClientID) {
		return errors.New("clientId must be 1-64 bytes of [A-Za-z0-9-]")
	}
	if input.ClientKind != "web" && input.ClientKind != "android" {
		return errors.New("clientKind must be web or android")
	}
	if len(input.SessionID) < 8 || len(input.SessionID) > 64 || !playbackIDPattern.MatchString(input.SessionID) {
		return errors.New("sessionId must be 8-64 bytes of [A-Za-z0-9-]")
	}
	if input.Seq < 1 || input.Seq > MaxPlaybackSeq {
		return fmt.Errorf("seq must be between 1 and %d", MaxPlaybackSeq)
	}
	if input.TrackID <= 0 {
		return errors.New("trackId must be positive")
	}
	if input.PositionMillis < 0 {
		return errors.New("positionMillis must not be negative")
	}
	if input.DurationMillis < 0 || input.DurationMillis > maxPlaybackEventDurationMillis {
		return errors.New("durationMillis must be between 0 and 24 hours")
	}
	switch input.Type {
	case "start", "heartbeat":
		if !activePlaybackState(input.State) {
			return errors.New("start and heartbeat must carry the real state (playing, buffering or paused)")
		}
		if input.EndReason != "" {
			return errors.New("endReason is only valid for end events")
		}
	case "pause", "buffering", "resume", "seek":
		if input.State != "" {
			return fmt.Errorf("%s events must not carry a state; the server derives it from the type", input.Type)
		}
		if input.EndReason != "" {
			return errors.New("endReason is only valid for end events")
		}
	case "end":
		if !validEndReason(input.EndReason) {
			return errors.New("end requires a valid endReason (completed, skipped, stopped, replaced, error, client_closed)")
		}
		if input.State != "" {
			return errors.New("end events must not carry a state")
		}
	default:
		return errors.New("invalid event type")
	}
	if input.Type != "start" && input.ResumedFromSessionID != "" {
		return errors.New("resumedFromSessionId is only valid for start events")
	}
	if input.ResumedFromSessionID != "" && (len(input.ResumedFromSessionID) < 8 || len(input.ResumedFromSessionID) > 64 || !playbackIDPattern.MatchString(input.ResumedFromSessionID)) {
		return errors.New("resumedFromSessionId must be 8-64 bytes of [A-Za-z0-9-]")
	}
	// No position clamping here (F-3): clampPlaybackPosition applies the
	// library-duration-first rule inside the event transaction, where the
	// probed duration is known.
	return nil
}

// sessionExpiredAt applies the read-side lease rule: an open session is
// expired once its heartbeat is older than the lease for its state.
func sessionExpiredAt(state, lastHeartbeatAt string, now time.Time) bool {
	if state == "ended" {
		return false
	}
	heartbeat, err := time.Parse(playbackTimeLayout, lastHeartbeatAt)
	if err != nil {
		return false
	}
	lease := PlaybackActiveLease
	if state == "paused" {
		lease = PlaybackPausedLease
	}
	return now.Sub(heartbeat) > lease
}

// sessionExpiredSQL is the same rule as sessionExpiredAt, expressed against
// SQLite's own clock for the sweeper and retention queries.
const sessionExpiredSQL = `(state IN ('playing','buffering') AND last_heartbeat_at < strftime('%Y-%m-%dT%H:%M:%fZ','now','` + PlaybackActiveLeaseSQL + `')) OR (state='paused' AND last_heartbeat_at < strftime('%Y-%m-%dT%H:%M:%fZ','now','` + PlaybackPausedLeaseSQL + `'))`

type playbackSessionRow struct {
	sessionID       string
	clientID        string
	clientKind      string
	trackID         int64
	state           string
	seq             int64
	positionMs      int64
	durationMs      int64
	initialPosMs    int64
	startedAt       string
	lastHeartbeatAt string
	endedAt         string
	endReason       string
	countedAt       string
	chainID         string
}

func scanPlaybackSession(row *sql.Row) (playbackSessionRow, error) {
	var s playbackSessionRow
	err := row.Scan(&s.clientID, &s.clientKind, &s.trackID, &s.state, &s.seq, &s.positionMs, &s.durationMs, &s.initialPosMs, &s.startedAt, &s.lastHeartbeatAt, &s.endedAt, &s.endReason, &s.countedAt, &s.chainID)
	return s, err
}

const playbackSessionSelect = `SELECT client_id,client_kind,track_id,state,seq,position_ms,duration_ms,initial_position_ms,started_at,last_heartbeat_at,COALESCE(ended_at,''),COALESCE(end_reason,''),COALESCE(counted_at,''),chain_id FROM playback_sessions WHERE session_id=?`

// chainCountedTx reports, inside the event transaction, whether any session
// of the chain has already been counted. This is the single source of truth
// for every Counted response and for the recount check (B-1): a resume
// chain counts at most once no matter how many sessions fork from it. WAL
// write serialisation plus the idx_playback_sessions_chain_counted unique
// partial index back this up.
func chainCountedTx(ctx context.Context, tx *sql.Tx, chainID string) (bool, error) {
	var counted bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM playback_sessions WHERE chain_id=? AND counted_at IS NOT NULL)`, chainID).Scan(&counted)
	return counted, err
}

// clampPlaybackPosition bounds a reported position by the effective
// duration + 5 s overshoot (M7/L-2): the library duration wins, then the
// session duration, then the reported one; with no known duration there is
// no clamp.
func clampPlaybackPosition(position, libraryDuration, sessionDuration, reportedDuration int64) int64 {
	effective := libraryDuration
	if effective <= 0 {
		effective = max(sessionDuration, reportedDuration)
	}
	if effective > 0 && position > effective+playbackPositionOvershootMillis {
		return effective + playbackPositionOvershootMillis
	}
	return position
}

// RecordPlaybackEvent applies one client event to its session. The whole
// read-check-write runs in a single transaction wrapped in withBusyRetry
// (M8): session row, progress aggregate, counters and the Last.fm outbox
// are committed atomically, and a BUSY loser retries the whole transaction
// so concurrent events stay idempotent.
func (s *Store) RecordPlaybackEvent(ctx context.Context, input PlaybackEventInput) (PlaybackEventResult, error) {
	if err := validatePlaybackEvent(&input); err != nil {
		return PlaybackEventResult{}, fmt.Errorf("%w: %v", ErrInvalidPlaybackEvent, err)
	}
	var result PlaybackEventResult
	err := withBusyRetry(ctx, func() error {
		var txErr error
		result, txErr = s.recordPlaybackEventTx(ctx, input)
		return txErr
	})
	return result, err
}

func (s *Store) recordPlaybackEventTx(ctx context.Context, input PlaybackEventInput) (PlaybackEventResult, error) {
	tx, err := s.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return PlaybackEventResult{}, err
	}
	defer tx.Rollback()

	sess, err := scanPlaybackSession(tx.QueryRowContext(ctx, playbackSessionSelect, input.SessionID))
	if errors.Is(err, sql.ErrNoRows) {
		result, err := s.startPlaybackSessionTx(ctx, tx, input)
		if errors.Is(err, errSessionCreatedConcurrently) {
			// A concurrent start won the race and committed between our read
			// and our insert (a narrow fallback; the primary concurrency
			// mechanism is the whole-transaction busy retry, L-3). Treat this
			// start as an idempotent replay.
			sess, err = scanPlaybackSession(tx.QueryRowContext(ctx, playbackSessionSelect, input.SessionID))
			if err != nil {
				return PlaybackEventResult{}, err
			}
			if sess.clientID != input.ClientID {
				return PlaybackEventResult{}, ErrSessionOwnerMismatch
			}
			if sess.clientKind != input.ClientKind || sess.trackID != input.TrackID {
				return PlaybackEventResult{}, ErrSessionConflict
			}
			counted, err := chainCountedTx(ctx, tx, sess.chainID)
			if err != nil {
				return PlaybackEventResult{}, err
			}
			return PlaybackEventResult{Applied: false, SessionID: input.SessionID, State: sess.state, PositionMillis: sess.positionMs, Counted: counted}, tx.Commit()
		}
		if err != nil {
			return PlaybackEventResult{}, err
		}
		return result, tx.Commit()
	}
	if err != nil {
		return PlaybackEventResult{}, err
	}

	if sess.clientID != input.ClientID {
		return PlaybackEventResult{}, ErrSessionOwnerMismatch
	}
	if sess.clientKind != input.ClientKind || sess.trackID != input.TrackID {
		return PlaybackEventResult{}, ErrSessionConflict
	}

	counted, err := chainCountedTx(ctx, tx, sess.chainID)
	if err != nil {
		return PlaybackEventResult{}, err
	}

	if sess.endedAt != "" {
		if sess.endReason == "expired" && input.Type != "start" {
			// Already finalized: tell the client to open a new session.
			return PlaybackEventResult{}, ErrSessionExpired
		}
		// Terminal sessions cannot be revived: late events, end retries and
		// start replays are idempotent no-ops (H1).
		return PlaybackEventResult{Applied: false, SessionID: input.SessionID, State: "ended", PositionMillis: sess.positionMs, Counted: counted}, tx.Commit()
	}

	if sessionExpiredAt(sess.state, sess.lastHeartbeatAt, time.Now()) {
		// The lease ran out: finalize in the same transaction so the 409 is
		// durable even if the sweeper never runs (B1/H2). ended_at uses the
		// last heartbeat — the last sign of life — so the finalized row
		// derives exactly like the read path did before the fix (M-1). The
		// position is kept: an interrupted play resumes from its breakpoint.
		if _, err := tx.ExecContext(ctx, `UPDATE playback_sessions SET state='ended',ended_at=last_heartbeat_at,end_reason='expired' WHERE session_id=? AND ended_at IS NULL`, input.SessionID); err != nil {
			return PlaybackEventResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return PlaybackEventResult{}, err
		}
		return PlaybackEventResult{}, ErrSessionExpired
	}

	if input.Type == "start" {
		// Idempotent start replay (H1): the session already exists and a
		// start must never reset its state, seq or lease.
		return PlaybackEventResult{Applied: false, SessionID: input.SessionID, State: sess.state, PositionMillis: sess.positionMs, Counted: counted}, tx.Commit()
	}
	if input.Seq <= sess.seq {
		// Stale or duplicated event: applied=false, and it neither renews
		// the lease nor moves the breakpoint (H1).
		return PlaybackEventResult{Applied: false, SessionID: input.SessionID, State: sess.state, PositionMillis: sess.positionMs, Counted: counted}, tx.Commit()
	}

	var libraryDuration int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(duration_ms,0) FROM tracks WHERE id=?`, sess.trackID).Scan(&libraryDuration); err != nil {
		return PlaybackEventResult{}, err
	}
	input.PositionMillis = clampPlaybackPosition(input.PositionMillis, libraryDuration, sess.durationMs, input.DurationMillis)

	newState := sess.state
	switch input.Type {
	case "heartbeat":
		newState = input.State
	case "pause":
		newState = "paused"
	case "buffering":
		newState = "buffering"
	case "resume":
		newState = "playing"
	case "seek":
		// seek keeps the current state and only moves the position.
	case "end":
		newState = "ended"
	}
	if input.Type == "end" {
		if _, err := tx.ExecContext(ctx, `UPDATE playback_sessions SET state='ended',seq=?,position_ms=?,duration_ms=MAX(duration_ms,?),last_heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),ended_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),end_reason=? WHERE session_id=?`, input.Seq, input.PositionMillis, input.DurationMillis, input.EndReason, input.SessionID); err != nil {
			return PlaybackEventResult{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE playback_sessions SET state=?,seq=?,position_ms=?,duration_ms=MAX(duration_ms,?),last_heartbeat_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE session_id=?`, newState, input.Seq, input.PositionMillis, input.DurationMillis, input.SessionID); err != nil {
			return PlaybackEventResult{}, err
		}
	}

	counted, queued, err := applyPlaybackSideEffects(ctx, tx, sess.trackID, sess.chainID, sess.startedAt, sess.initialPosMs, libraryDuration, max(sess.durationMs, input.DurationMillis), input, sess.state, newState)
	if err != nil {
		return PlaybackEventResult{}, err
	}
	position := input.PositionMillis
	if input.Type == "end" && input.EndReason == "completed" {
		position = 0
	}
	result := PlaybackEventResult{Applied: true, SessionID: input.SessionID, State: newState, PositionMillis: position, Counted: counted, QueuedForLastFM: queued}
	return result, tx.Commit()
}

// errSessionCreatedConcurrently marks a start that lost an insert race
// against a concurrent start of the same session id.
var errSessionCreatedConcurrently = errors.New("playback session created concurrently")

// isPrimaryKeyConstraint matches exactly SQLITE_CONSTRAINT_PRIMARYKEY
// (extended code 1555) so the concurrent-start fallback can never swallow
// other constraint failures such as the chain-counted unique index (B-1).
func isPrimaryKeyConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == 1555
}

// startPlaybackSessionTx handles events for a session id the server has not
// seen. Only start may create a session: a heartbeat that arrives first is
// rejected with ErrSessionNotFound instead of creating a row that a later
// start would have to overwrite (H1).
func (s *Store) startPlaybackSessionTx(ctx context.Context, tx *sql.Tx, input PlaybackEventInput) (PlaybackEventResult, error) {
	if input.Type != "start" {
		return PlaybackEventResult{}, ErrSessionNotFound
	}
	var libraryDuration int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(duration_ms,0) FROM tracks WHERE id=?`, input.TrackID).Scan(&libraryDuration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PlaybackEventResult{}, ErrTrackNotFound
		}
		return PlaybackEventResult{}, err
	}
	input.PositionMillis = clampPlaybackPosition(input.PositionMillis, libraryDuration, 0, input.DurationMillis)

	chainID := input.SessionID
	if input.ResumedFromSessionID != "" {
		prev, err := scanPlaybackSession(tx.QueryRowContext(ctx, playbackSessionSelect, input.ResumedFromSessionID))
		if errors.Is(err, sql.ErrNoRows) {
			return PlaybackEventResult{}, ErrResumeInvalid
		}
		if err != nil {
			return PlaybackEventResult{}, err
		}
		// The resume chain must stay on the same client and track (H2/H3):
		// anything else is a fork that would double-count.
		if prev.clientID != input.ClientID || prev.trackID != input.TrackID {
			return PlaybackEventResult{}, ErrResumeInvalid
		}
		if prev.endedAt == "" {
			if sessionExpiredAt(prev.state, prev.lastHeartbeatAt, time.Now()) {
				// A predecessor whose lease silently ran out (server down,
				// sweeper not yet run) is a valid resume source: finalize it
				// first, using its last heartbeat as the end time (M-1).
				if _, err := tx.ExecContext(ctx, `UPDATE playback_sessions SET state='ended',ended_at=last_heartbeat_at,end_reason='expired' WHERE session_id=? AND ended_at IS NULL`, input.ResumedFromSessionID); err != nil {
					return PlaybackEventResult{}, err
				}
			} else {
				// A still-active predecessor on the same client and track is
				// superseded by this resume: end it as replaced in the same
				// transaction and link the chain (M-3). The replacement is
				// safe against forks because chain membership, not the
				// start-time snapshot, decides counting (B-1).
				if _, err := tx.ExecContext(ctx, `UPDATE playback_sessions SET state='ended',ended_at=last_heartbeat_at,end_reason='replaced' WHERE session_id=? AND ended_at IS NULL`, input.ResumedFromSessionID); err != nil {
					return PlaybackEventResult{}, err
				}
			}
		} else if !resumableEndReason(prev.endReason) {
			// completed/skipped plays are finished; replaced already named
			// a successor. Resuming from either would fork or undercount
			// the chain (M-3).
			return PlaybackEventResult{}, ErrResumeInvalid
		}
		chainID = prev.chainID
	}

	var resumedFrom any
	if input.ResumedFromSessionID != "" {
		resumedFrom = input.ResumedFromSessionID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO playback_sessions(session_id,client_id,client_kind,track_id,state,seq,position_ms,duration_ms,initial_position_ms,started_at,last_heartbeat_at,chain_id,resumed_from) VALUES(?,?,?,?,?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'),?,?)`,
		input.SessionID, input.ClientID, input.ClientKind, input.TrackID, input.State, input.Seq, input.PositionMillis, input.DurationMillis, input.PositionMillis, chainID, resumedFrom); err != nil {
		if isPrimaryKeyConstraint(err) {
			return PlaybackEventResult{}, errSessionCreatedConcurrently
		}
		return PlaybackEventResult{}, err
	}

	counted, queued, err := applyPlaybackSideEffects(ctx, tx, input.TrackID, chainID, "", input.PositionMillis, libraryDuration, input.DurationMillis, input, "", input.State)
	if err != nil {
		return PlaybackEventResult{}, err
	}
	result := PlaybackEventResult{Applied: true, SessionID: input.SessionID, State: input.State, PositionMillis: input.PositionMillis, Counted: counted, QueuedForLastFM: queued}
	return result, nil
}

// applyPlaybackSideEffects runs inside the session transaction after an
// event was accepted. It maintains the playback_progress aggregate (the
// last accepted event wins the breakpoint; completed resets it to 0),
// derives the per-chain play count at the existing ~50% threshold with
// slack (at most one counted session per chain, B-1), counts explicit skips
// at the existing MIN(30s, duration/2) threshold, and snapshots counted
// plays into the Last.fm outbox — all in the same commit (B4/H3/H4/M8).
//
// Counting and the last_played_at refresh require evidence that the track
// actually played: the state before (priorState) or after (newState) the
// event must be playing (N-1). A session that only ever reported
// paused/buffering — a cold restore the user never resumed — can therefore
// never count, scrobble or top the history list, no matter which endReason
// it ends with; a session that played past the threshold counts on the very
// pause/buffering/end event that carries the reached position. start
// passes priorState="".
func applyPlaybackSideEffects(ctx context.Context, tx *sql.Tx, trackID int64, chainID, sessionStartedAt string, initialPositionMs, libraryDuration, sessionDurationMs int64, input PlaybackEventInput, priorState, newState string) (bool, bool, error) {
	progressPosition := input.PositionMillis
	if input.Type == "end" && input.EndReason == "completed" {
		progressPosition = 0
	}
	evidenceOfPlay := newState == "playing" || priorState == "playing"
	refreshPlayed := evidenceOfPlay
	if _, err := tx.ExecContext(ctx, `INSERT INTO playback_progress(track_id,position_ms,duration_ms,last_played_at) VALUES(?,?,?,CASE WHEN ? THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE NULL END)
		ON CONFLICT(track_id) DO UPDATE SET position_ms=excluded.position_ms,duration_ms=MAX(playback_progress.duration_ms,excluded.duration_ms),last_played_at=CASE WHEN ? THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE playback_progress.last_played_at END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, trackID, progressPosition, input.DurationMillis, refreshPlayed, refreshPlayed); err != nil {
		return false, false, err
	}

	duration := libraryDuration
	if duration <= 0 {
		duration = max(sessionDurationMs, input.DurationMillis)
	}

	alreadyCounted, err := chainCountedTx(ctx, tx, chainID)
	if err != nil {
		return false, false, err
	}
	counted := alreadyCounted
	queued := false
	// F-1/N-1: only events with evidence of actual playback count — the
	// state before or after the event is playing. A paused/buffering start
	// at a high position (cold restore without a persisted session) never
	// counts or reaches Last.fm, and neither does its end, until playback
	// actually resumes.
	if !alreadyCounted && duration > 0 && evidenceOfPlay {
		slack := min(int64(scrobbleThresholdSlackMillis), duration/10)
		if input.PositionMillis >= duration/2-slack {
			// Counting also sets last_played_at so a counted play is always
			// visible in the history list, even if every earlier event of the
			// session was paused/buffering (F-1/L-4 interaction).
			if _, err := tx.ExecContext(ctx, `UPDATE playback_progress SET play_count=play_count+1,last_completed_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),last_played_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE track_id=?`, trackID); err != nil {
				return false, false, err
			}
			// The chain-counted unique partial index is the last line of
			// defence here: a sibling that somehow slipped past the EXISTS
			// check would fail this statement and roll back the event (B-1).
			if _, err := tx.ExecContext(ctx, `UPDATE playback_sessions SET counted_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE session_id=?`, input.SessionID); err != nil {
				return false, false, err
			}
			counted = true
			// Last.fm startedAt is derived from the session start minus its
			// initial position (H3). For a fresh start the stored started_at
			// is "now" inside this transaction.
			startedAt := time.Now()
			if sessionStartedAt != "" {
				if parsed, err := time.Parse(playbackTimeLayout, sessionStartedAt); err == nil {
					startedAt = parsed
				}
			}
			startedAt = startedAt.Add(-time.Duration(initialPositionMs) * time.Millisecond)
			var err error
			queued, err = enqueueLastFMScrobble(ctx, tx, trackID, duration, startedAt)
			if err != nil {
				return false, false, err
			}
		}
	}

	if input.Type == "end" && input.EndReason == "skipped" {
		// A skip counts exactly once, only on an explicit skipped end below
		// the existing MIN(30s, duration/2) threshold (H4). expired,
		// stopped, replaced, error, client_closed and completed never count.
		threshold := int64(30000)
		if duration > 0 {
			threshold = min(int64(30000), duration/2)
		}
		if input.PositionMillis < threshold {
			if _, err := tx.ExecContext(ctx, `UPDATE playback_progress SET skip_count=skip_count+1,last_skipped_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE track_id=?`, trackID); err != nil {
				return false, false, err
			}
		}
	}

	if libraryDuration <= 0 && plausibleClientDuration(input.DurationMillis) {
		if _, err := tx.ExecContext(ctx, `UPDATE tracks SET duration_ms=? WHERE id=? AND COALESCE(duration_ms,0)=0`, input.DurationMillis, trackID); err != nil {
			return false, false, err
		}
	}
	return counted, queued, nil
}

// SetPlaybackSessionHeartbeatForTest backdates a session's state and
// heartbeat so tests (including handler tests in other packages) can
// simulate lease expiry without sleeping. Not used by production code.
func (s *Store) SetPlaybackSessionHeartbeatForTest(ctx context.Context, sessionID, state, heartbeatAt string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET state=?,last_heartbeat_at=? WHERE session_id=?`, state, heartbeatAt, sessionID)
	return err
}

// FixExpiredPlaybackSessions finalizes open sessions whose lease has run
// out. Reads derive expiry on their own, so this only persists the outcome
// (and lets late heartbeats get a precise 409); it is safe to run at any
// cadence, including once at startup after a server restart (B1).
func (s *Store) FixExpiredPlaybackSessions(ctx context.Context) (int64, error) {
	// ended_at = last_heartbeat_at: the last sign of life, identical to the
	// time the read path already derived the expiry from (M-1).
	result, err := s.db.ExecContext(ctx, `UPDATE playback_sessions SET state='ended',ended_at=last_heartbeat_at,end_reason='expired' WHERE ended_at IS NULL AND (`+sessionExpiredSQL+`)`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// PurgeEndedPlaybackSessions deletes ended sessions older than the
// retention window (D7). It never touches playback_progress, so history
// counters survive the purge (M6). A chain's counted row is kept while any
// sibling session (open or ended) still exists: it is the only durable
// record that the chain already counted, and dropping it early would let a
// late resume count the chain again (B-1). Once the counted row is the
// last row of its chain it is purged like any other — by then no session
// of the chain remains to resume from.
func (s *Store) PurgeEndedPlaybackSessions(ctx context.Context, now time.Time) (int64, error) {
	cutoff := now.Add(-PlaybackEndedRetention).UTC().Format(playbackTimeLayout)
	var total int64
	err := withBusyRetry(ctx, func() error {
		tx, err := s.db.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// Pass 1: uncounted rows carry no chain-counting fact.
		result, err := tx.ExecContext(ctx, `DELETE FROM playback_sessions WHERE ended_at IS NOT NULL AND ended_at<? AND counted_at IS NULL`, cutoff)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		// Pass 2: counted rows only once they are the last of their chain.
		result, err = tx.ExecContext(ctx, `DELETE FROM playback_sessions WHERE ended_at IS NOT NULL AND ended_at<? AND counted_at IS NOT NULL AND NOT EXISTS(SELECT 1 FROM playback_sessions s2 WHERE s2.chain_id=playback_sessions.chain_id AND s2.session_id<>playback_sessions.session_id)`, cutoff)
		if err != nil {
			return err
		}
		countedAffected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		total = affected + countedAffected
		return tx.Commit()
	})
	return total, err
}
