package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
)

// Scrobble rules shared by every client (web player, API-token apps and
// media-token apps): a play counts once it has passed half of the track, and
// repeated reports of the same playback are counted only once.
const (
	// scrobbleThresholdSlackMillis tolerates the reporting jitter of clients
	// that fire the scrobble on a progress tick right around the 50% mark.
	scrobbleThresholdSlackMillis = 1000
	// scrobbleUnknownDurationWindow deduplicates plays of tracks whose
	// duration is unknown to both the library and the client.
	scrobbleUnknownDurationWindow = 30 * time.Second
	// lastFMMinimumDurationMillis follows the Last.fm rule that tracks of
	// 30 seconds or less are not scrobbled.
	lastFMMinimumDurationMillis = 30000
	// lastFMMaxScrobbleAge: Last.fm ignores scrobbles older than 14 days.
	LastFMMaxScrobbleAge = 14 * 24 * time.Hour
)

// Reasons returned when a scrobble report is accepted but not counted.
const (
	ScrobbleReasonThreshold = "threshold_not_reached"
	ScrobbleReasonDuplicate = "duplicate"
)

type ScrobbleInput struct {
	TrackID        int64
	PositionMillis int64
	DurationMillis int64
	// ReportedAt is when the client observed PositionMillis; zero means now.
	ReportedAt time.Time
}

type ScrobbleResult struct {
	Recorded bool
	Reason   string
	// QueuedForLastFM reports that a Last.fm submission was enqueued.
	QueuedForLastFM bool
}

// scrobbleMu serialises the read-check-write of RecordScrobble inside this
// process so two concurrent reports of the same play cannot both pass the
// duplicate check. The server is the only writer of its database.
var scrobbleMu sync.Mutex

// RecordScrobble counts one completed play when the reported position has
// passed half of the track, deduplicates reports of the same playback and,
// when Last.fm scrobbling is connected, snapshots the play into the Last.fm
// outbox in the same transaction.
//
// A PositionMillis of 0 means the client did not report a position (legacy
// clients); such reports are trusted to have applied the threshold already.
func (s *Store) RecordScrobble(ctx context.Context, input ScrobbleInput) (ScrobbleResult, error) {
	if input.TrackID <= 0 || input.PositionMillis < 0 || input.DurationMillis < 0 {
		return ScrobbleResult{}, errors.New("invalid scrobble payload")
	}
	now := time.Now()
	reportedAt := input.ReportedAt
	if reportedAt.IsZero() || reportedAt.After(now.Add(time.Minute)) {
		reportedAt = now
	}
	startedAt := reportedAt.Add(-time.Duration(input.PositionMillis) * time.Millisecond)

	scrobbleMu.Lock()
	defer scrobbleMu.Unlock()

	var result ScrobbleResult
	err := withBusyRetry(ctx, func() error {
		var txErr error
		result, txErr = s.recordScrobbleTx(ctx, input, startedAt)
		return txErr
	})
	return result, err
}

func (s *Store) recordScrobbleTx(ctx context.Context, input ScrobbleInput, startedAt time.Time) (ScrobbleResult, error) {
	tx, err := s.db.DB.BeginTx(ctx, nil)
	if err != nil {
		return ScrobbleResult{}, err
	}
	defer tx.Rollback()

	var libraryDuration int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(duration_ms,0) FROM tracks WHERE id=?`, input.TrackID).Scan(&libraryDuration); err != nil {
		return ScrobbleResult{}, err
	}
	duration := libraryDuration
	if duration <= 0 && plausibleClientDuration(input.DurationMillis) {
		duration = input.DurationMillis
	}

	if duration > 0 && input.PositionMillis > 0 {
		slack := min(int64(scrobbleThresholdSlackMillis), duration/10)
		if input.PositionMillis < duration/2-slack {
			return ScrobbleResult{Reason: ScrobbleReasonThreshold}, nil
		}
	}

	var lastStarted sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT last_scrobble_started_ms FROM playback_progress WHERE track_id=?`, input.TrackID).Scan(&lastStarted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ScrobbleResult{}, err
	}
	startedMillis := startedAt.UnixMilli()
	if lastStarted.Valid {
		// Two genuine plays of one track start at least half a track apart
		// (the first must run to 50% before the second can begin), so any
		// report whose estimated start lies closer belongs to a play that was
		// already counted: the 50% report and the "ended" report, a client
		// retry, or a second client reporting the same session.
		window := scrobbleUnknownDurationWindow
		if duration > 0 {
			window = time.Duration(duration/2-scrobbleThresholdSlackMillis) * time.Millisecond
		}
		delta := startedMillis - lastStarted.Int64
		if delta < 0 {
			delta = -delta
		}
		if time.Duration(delta)*time.Millisecond < window {
			return ScrobbleResult{Reason: ScrobbleReasonDuplicate}, nil
		}
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO playback_progress(track_id,state,position_ms,duration_ms,play_count,last_played_at,last_completed_at,last_scrobble_started_ms) VALUES(?,'stopped',?,?,1,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'),?) ON CONFLICT(track_id) DO UPDATE SET state='stopped',position_ms=excluded.position_ms,duration_ms=MAX(playback_progress.duration_ms,excluded.duration_ms),play_count=playback_progress.play_count+1,last_played_at=excluded.last_played_at,last_completed_at=excluded.last_completed_at,last_scrobble_started_ms=excluded.last_scrobble_started_ms,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, input.TrackID, input.PositionMillis, input.DurationMillis, startedMillis); err != nil {
		return ScrobbleResult{}, err
	}
	if libraryDuration <= 0 && plausibleClientDuration(input.DurationMillis) {
		if _, err = tx.ExecContext(ctx, `UPDATE tracks SET duration_ms=? WHERE id=? AND COALESCE(duration_ms,0)=0`, input.DurationMillis, input.TrackID); err != nil {
			return ScrobbleResult{}, err
		}
	}

	result := ScrobbleResult{Recorded: true}
	queued, err := enqueueLastFMScrobble(ctx, tx, input.TrackID, duration, startedAt)
	if err != nil {
		return ScrobbleResult{}, err
	}
	result.QueuedForLastFM = queued
	if err = tx.Commit(); err != nil {
		return ScrobbleResult{}, err
	}
	return result, nil
}

func enqueueLastFMScrobble(ctx context.Context, tx *sql.Tx, trackID, duration int64, startedAt time.Time) (bool, error) {
	var enabled int
	var sessionKey string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,session_key FROM lastfm_scrobble_settings WHERE id=1`).Scan(&enabled, &sessionKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if enabled == 0 || sessionKey == "" {
		return false, nil
	}
	if duration > 0 && duration <= lastFMMinimumDurationMillis {
		return false, nil
	}
	meta, err := lastFMTrackMetadata(ctx, tx, trackID)
	if err != nil {
		return false, err
	}
	if meta.Artist == "" || meta.Track == "" || strings.EqualFold(meta.Artist, "Unknown Artist") {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lastfm_scrobble_queue(track_id,artist,track,album,album_artist,track_number,duration_seconds,started_at) VALUES(?,?,?,?,?,?,?,?)`,
		trackID, meta.Artist, meta.Track, meta.Album, meta.AlbumArtist, meta.TrackNumber, duration/1000, startedAt.Unix())
	return err == nil, err
}

// LastFMTrack is the metadata submitted to Last.fm for one play.
type LastFMTrack struct {
	ID              int64
	TrackID         int64
	Artist          string
	Track           string
	Album           string
	AlbumArtist     string
	TrackNumber     int
	DurationSeconds int64
	StartedAt       int64
	Attempts        int
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func lastFMTrackMetadata(ctx context.Context, db queryRower, trackID int64) (LastFMTrack, error) {
	var value LastFMTrack
	err := db.QueryRowContext(ctx, `SELECT t.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),`+trackArtistSQL+`,
		COALESCE(a.user_performed_by,a.performed_by,(SELECT GROUP_CONCAT(COALESCE(ar.user_display_name,ar.display_name),', ') FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id),''),
		COALESCE(t.user_track_number,t.track_number,0),COALESCE(t.duration_ms,0)/1000
		FROM tracks t JOIN albums a ON a.id=t.album_id WHERE t.id=?`, trackID).Scan(&value.TrackID, &value.Track, &value.Album, &value.Artist, &value.AlbumArtist, &value.TrackNumber, &value.DurationSeconds)
	value.Artist = strings.TrimSpace(value.Artist)
	value.Track = strings.TrimSpace(value.Track)
	value.Album = strings.TrimSpace(value.Album)
	value.AlbumArtist = strings.TrimSpace(value.AlbumArtist)
	if value.AlbumArtist == value.Artist {
		value.AlbumArtist = ""
	}
	return value, err
}

// LastFMNowPlayingTrack loads the metadata for a track.updateNowPlaying call.
func (s *Store) LastFMNowPlayingTrack(ctx context.Context, trackID int64) (LastFMTrack, error) {
	return lastFMTrackMetadata(ctx, s.db, trackID)
}

type LastFMScrobbleSettings struct {
	Enabled, NowPlaying        bool
	APIKey, APISecret          string
	SessionKey, Username       string
	LastSuccessAt, LastError   string
	LastErrorAt                string
	PendingCount               int64
	OldestPendingStartedAtUnix int64
}

func (v LastFMScrobbleSettings) HasAPIKey() bool    { return v.APIKey != "" }
func (v LastFMScrobbleSettings) HasAPISecret() bool { return v.APISecret != "" }
func (v LastFMScrobbleSettings) Connected() bool    { return v.SessionKey != "" }

// Ready reports whether plays can be submitted right now.
func (v LastFMScrobbleSettings) Ready() bool {
	return v.Enabled && v.APIKey != "" && v.APISecret != "" && v.SessionKey != ""
}

func (s *Store) LastFMScrobbleSettings(ctx context.Context) (LastFMScrobbleSettings, error) {
	var value LastFMScrobbleSettings
	var enabled, nowPlaying int
	err := s.db.QueryRowContext(ctx, `SELECT l.enabled,l.now_playing,COALESCE((SELECT api_key FROM metadata_source_settings WHERE source='lastfm'),''),l.api_secret,l.session_key,l.username,COALESCE(l.last_success_at,''),l.last_error,COALESCE(l.last_error_at,''),
		(SELECT COUNT(*) FROM lastfm_scrobble_queue),COALESCE((SELECT MIN(started_at) FROM lastfm_scrobble_queue),0)
		FROM lastfm_scrobble_settings l WHERE l.id=1`).Scan(&enabled, &nowPlaying, &value.APIKey, &value.APISecret, &value.SessionKey, &value.Username, &value.LastSuccessAt, &value.LastError, &value.LastErrorAt, &value.PendingCount, &value.OldestPendingStartedAtUnix)
	value.Enabled = enabled != 0
	value.NowPlaying = nowPlaying != 0
	return value, err
}

// SaveLastFMScrobblePreferences stores the switches and, when non-empty, a
// new shared secret. An empty secret keeps the stored one.
func (s *Store) SaveLastFMScrobblePreferences(ctx context.Context, enabled, nowPlaying bool, apiSecret string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET enabled=?,now_playing=?,api_secret=CASE WHEN ?='' THEN api_secret ELSE ? END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, boolInt(enabled), boolInt(nowPlaying), apiSecret, apiSecret)
	return err
}

// BeginLastFMAuthorization records the anti-forgery state of a pending
// browser authorisation.
func (s *Store) BeginLastFMAuthorization(ctx context.Context, state string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET pending_state=?,pending_state_at=? WHERE id=1`, state, at.Unix())
	return err
}

// ConsumeLastFMAuthorization clears the pending state and reports whether it
// matched and was issued within maxAge. A state can be used only once.
func (s *Store) ConsumeLastFMAuthorization(ctx context.Context, state string, maxAge time.Duration, now time.Time) (bool, error) {
	if state == "" {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET pending_state='',pending_state_at=0 WHERE id=1 AND pending_state<>'' AND pending_state=? AND pending_state_at>=?`, state, now.Add(-maxAge).Unix())
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *Store) SetLastFMSession(ctx context.Context, username, sessionKey string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET username=?,session_key=?,enabled=1,last_error='',last_error_at=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, username, sessionKey)
	return err
}

// ClearLastFMSession disconnects the account. Queued plays are kept so they
// can still be submitted after reconnecting (within Last.fm's 14-day limit).
func (s *Store) ClearLastFMSession(ctx context.Context, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET session_key='',username=CASE WHEN ?='' THEN '' ELSE username END,last_error=?,last_error_at=CASE WHEN ?='' THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, reason, reason, reason)
	return err
}

func (s *Store) SetLastFMStatus(ctx context.Context, success bool, message string) error {
	if success {
		_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET last_success_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),last_error='',last_error_at=NULL WHERE id=1`)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET last_error=?,last_error_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, message)
	return err
}

// DueLastFMScrobbles returns up to limit queued plays whose retry time has
// come, oldest play first.
func (s *Store) DueLastFMScrobbles(ctx context.Context, now time.Time, limit int) ([]LastFMTrack, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,COALESCE(track_id,0),artist,track,album,album_artist,track_number,duration_seconds,started_at,attempts FROM lastfm_scrobble_queue WHERE next_attempt_at<=? ORDER BY started_at,id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []LastFMTrack
	for rows.Next() {
		var v LastFMTrack
		if err := rows.Scan(&v.ID, &v.TrackID, &v.Artist, &v.Track, &v.Album, &v.AlbumArtist, &v.TrackNumber, &v.DurationSeconds, &v.StartedAt, &v.Attempts); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// NextLastFMScrobbleAttempt returns the earliest retry time of the queue, or
// zero when the queue is empty.
func (s *Store) NextLastFMScrobbleAttempt(ctx context.Context) (time.Time, error) {
	var next sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(next_attempt_at) FROM lastfm_scrobble_queue`).Scan(&next); err != nil || !next.Valid {
		return time.Time{}, err
	}
	return time.Unix(next.Int64, 0), nil
}

func (s *Store) DeleteLastFMScrobbles(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders, args := inClause(ids)
	_, err := s.db.ExecContext(ctx, `DELETE FROM lastfm_scrobble_queue WHERE id IN (`+placeholders+`)`, args...)
	return err
}

func (s *Store) DeferLastFMScrobbles(ctx context.Context, ids []int64, next time.Time, message string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders, args := inClause(ids)
	args = append([]any{next.Unix(), message}, args...)
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_queue SET attempts=attempts+1,next_attempt_at=?,last_error=? WHERE id IN (`+placeholders+`)`, args...)
	return err
}

// RetryLastFMScrobblesNow makes every queued play due immediately.
func (s *Store) RetryLastFMScrobblesNow(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_queue SET next_attempt_at=0`)
	return err
}

// DropExpiredLastFMScrobbles removes plays Last.fm would reject as too old.
func (s *Store) DropExpiredLastFMScrobbles(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM lastfm_scrobble_queue WHERE started_at<?`, now.Add(-LastFMMaxScrobbleAge).Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
