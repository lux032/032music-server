package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Last.fm love ("红心") sync.
//
// The local favourite is authoritative: SetTrackFavorite only records the
// desired remote state in lastfm_love_queue, and the Last.fm worker submits it
// later. Queueing happens in the same transaction as the favourite update, so
// a favourite that succeeded locally can never lose its pending sync task.
//
// A separate, explicitly started backfill pushes the favourites that already
// existed before the feature was enabled. It walks tracks by id and records its
// cursor, so it is resumable and its progress is a plain processed/total ratio.

// LoveBackfill states.
const (
	LoveBackfillIdle      = "idle"
	LoveBackfillRunning   = "running"
	LoveBackfillDone      = "done"
	LoveBackfillCancelled = "cancelled"
)

// loveFailedAfterAttempts is the attempt count after which the settings card
// reports a queued love change as failing (it is still retried, more slowly).
const loveFailedAfterAttempts = 5

// LastFMLoveItem is one pending love/unlove submission.
type LastFMLoveItem struct {
	TrackID  int64
	Desired  bool
	Artist   string
	Track    string
	Attempts int
}

// LastFMLoveQueueStats summarises the pending love work.
type LastFMLoveQueueStats struct {
	Pending   int64
	Failed    int64
	NextDue   int64 // unix seconds, 0 when nothing is queued
	LastErr   string
	LastErrAt string
}

// LastFMLoveBackfill is the single backfill run's state.
type LastFMLoveBackfill struct {
	State      string
	Total      int64
	Processed  int64
	Failed     int64
	Cursor     int64
	LastError  string
	StartedAt  string
	FinishedAt string
}

// Percent is the backfill progress in tenths of a percent (0–100).
func (b LastFMLoveBackfill) Percent() float64 {
	if b.Total <= 0 {
		return 0
	}
	percent := float64(b.Processed) / float64(b.Total) * 100
	if percent > 100 {
		percent = 100
	}
	return percent
}

// Done reports whether the backfill finished all the way through.
func (b LastFMLoveBackfill) Done() bool { return b.State == LoveBackfillDone }

// enqueueLastFMLove records the desired love state of one track inside an
// existing transaction. The queue collapses repeated toggling into the final
// desired state and resets the retry schedule, because the user just acted.
func enqueueLastFMLove(ctx context.Context, tx *sql.Tx, trackID int64, desired bool) error {
	var enabled int
	var sessionKey string
	err := tx.QueryRowContext(ctx, `SELECT enabled,sync_loves,session_key FROM lastfm_scrobble_settings WHERE id=1`).Scan(&enabled, &enabled, &sessionKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	// Without a connected account there is nothing to submit to; the local
	// favourite is still saved. The next enable/connect does not replay it.
	if enabled == 0 || sessionKey == "" {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO lastfm_love_queue(track_id,desired,attempts,next_attempt_at,last_error) VALUES(?,?,0,0,'')
		ON CONFLICT(track_id) DO UPDATE SET desired=excluded.desired,attempts=0,next_attempt_at=0,last_error=''`, trackID, boolInt(desired))
	return err
}

// SetTrackFavorite syncs the local favourite and, when Last.fm love sync is
// enabled and connected, queues the matching track.love / track.unlove call in
// the same transaction.
func (s *Store) SetTrackFavorite(ctx context.Context, id int64, favorite bool) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = updateFavorite(ctx, tx, "tracks", id, favorite); err != nil {
		return err
	}
	if err = enqueueLastFMLove(ctx, tx, id, favorite); err != nil {
		return err
	}
	return tx.Commit()
}

// SetLastFMLoveSync stores the love-sync switch. Turning it off stops new
// submissions; already queued items stay until they are sent or dropped.
func (s *Store) SetLastFMLoveSync(ctx context.Context, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET sync_loves=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, boolInt(enabled))
	return err
}

// DueLastFMLoves returns the queued love changes that are due, oldest first.
func (s *Store) DueLastFMLoves(ctx context.Context, now time.Time, limit int) ([]LastFMLoveItem, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT q.track_id,q.desired,COALESCE(t.user_title,t.title),`+trackArtistSQL+`,q.attempts
		FROM lastfm_love_queue q JOIN tracks t ON t.id=q.track_id JOIN albums a ON a.id=t.album_id
		WHERE q.next_attempt_at<=? ORDER BY q.track_id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]LastFMLoveItem, 0, limit)
	for rows.Next() {
		var item LastFMLoveItem
		var desired int
		if err := rows.Scan(&item.TrackID, &desired, &item.Track, &item.Artist, &item.Attempts); err != nil {
			return nil, err
		}
		item.Desired = desired != 0
		// A row with no usable names is still returned: the worker drops it
		// instead of skipping it here, because skipping would leave a due row
		// that never leaves the queue and would wake the worker every second.
		items = append(items, item)
	}
	return items, rows.Err()
}

// NextLastFMLoveAttempt returns the earliest scheduled retry, or zero.
func (s *Store) NextLastFMLoveAttempt(ctx context.Context) (time.Time, error) {
	var next sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(next_attempt_at) FROM lastfm_love_queue`).Scan(&next); err != nil || !next.Valid {
		return time.Time{}, err
	}
	return time.Unix(next.Int64, 0), nil
}

// CompleteLastFMLove drops a submitted item. The desired state is re-read first
// so a toggle that happened while the request was in flight is not lost.
func (s *Store) CompleteLastFMLove(ctx context.Context, trackID int64, desired bool) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lastfm_love_queue WHERE track_id=? AND desired=?`, trackID, boolInt(desired))
	return err
}

// DeferLastFMLove reschedules a failed item with exponential backoff.
func (s *Store) DeferLastFMLove(ctx context.Context, trackID int64, next time.Time, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_love_queue SET attempts=attempts+1,next_attempt_at=?,last_error=? WHERE track_id=?`, next.Unix(), message, trackID)
	return err
}

// DropLastFMLove removes an item Last.fm will never accept.
func (s *Store) DropLastFMLove(ctx context.Context, trackID int64, message string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lastfm_love_queue WHERE track_id=?`, trackID)
	if err != nil {
		return err
	}
	return s.SetLastFMLoveError(ctx, message)
}

// RetryLastFMLovesNow makes every queued love change due immediately.
func (s *Store) RetryLastFMLovesNow(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_love_queue SET next_attempt_at=0`)
	return err
}

// LastFMLoveQueueStats reports the pending work for the settings card.
func (s *Store) LastFMLoveQueueStats(ctx context.Context) (LastFMLoveQueueStats, error) {
	var value LastFMLoveQueueStats
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN attempts>=? THEN 1 ELSE 0 END),0),MIN(next_attempt_at),
		COALESCE((SELECT last_error FROM lastfm_love_queue ORDER BY track_id LIMIT 1),'')
		FROM lastfm_love_queue`, loveFailedAfterAttempts).Scan(&value.Pending, &value.Failed, &next, &value.LastErr)
	if err != nil {
		return value, err
	}
	if next.Valid {
		value.NextDue = next.Int64
	}
	return value, nil
}

// SetLastFMLoveError records the most recent love-sync error for the card.
func (s *Store) SetLastFMLoveError(ctx context.Context, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_scrobble_settings SET last_love_error=?,last_love_error_at=CASE WHEN ?='' THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END WHERE id=1`, message, message)
	return err
}

// BeginLastFMLoveBackfill starts (or restarts) the explicit backfill run. The
// caller has already counted the eligible favourites.
func (s *Store) BeginLastFMLoveBackfill(ctx context.Context, total int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_love_backfill SET state='running',total=?,processed=0,failed=0,cursor=0,last_error='',started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),finished_at=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, total)
	return err
}

// CancelLastFMLoveBackfill marks a running backfill cancelled.
func (s *Store) CancelLastFMLoveBackfill(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_love_backfill SET state='cancelled',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND state='running'`)
	return err
}

// FinishLastFMLoveBackfill marks the run done or cancelled and records errors.
func (s *Store) FinishLastFMLoveBackfill(ctx context.Context, state string, lastError string) error {
	if state != LoveBackfillDone && state != LoveBackfillCancelled {
		return fmt.Errorf("invalid backfill state %q", state)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_love_backfill SET state=?,last_error=?,finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, state, lastError)
	return err
}

// AdvanceLastFMLoveBackfill records one processed track.
func (s *Store) AdvanceLastFMLoveBackfill(ctx context.Context, cursor int64, failed int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE lastfm_love_backfill SET cursor=?,processed=processed+1,failed=failed+?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND state='running'`, cursor, failed)
	return err
}

// LastFMLoveBackfill returns the backfill state.
func (s *Store) LastFMLoveBackfill(ctx context.Context) (LastFMLoveBackfill, error) {
	var value LastFMLoveBackfill
	var started, finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT state,total,processed,failed,cursor,last_error,started_at,finished_at FROM lastfm_love_backfill WHERE id=1`).Scan(&value.State, &value.Total, &value.Processed, &value.Failed, &value.Cursor, &value.LastError, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return LastFMLoveBackfill{State: LoveBackfillIdle}, nil
	}
	value.StartedAt = started.String
	value.FinishedAt = finished.String
	return value, err
}

// CountLoveBackfillTracks counts the visible favourites the backfill will push.
func (s *Store) CountLoveBackfillTracks(ctx context.Context) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks t WHERE t.is_favorite=1 AND `+trackVisibleSQL("t.id")).Scan(&total)
	return total, err
}

// NextLoveBackfillTrack returns the next favourite after cursor, or 0 when the
// backfill is complete. The local favourite state is re-read: unfavouriting
// during the run removes the track from the walk.
func (s *Store) NextLoveBackfillTrack(ctx context.Context, cursor int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT t.id FROM tracks t WHERE t.id>? AND t.is_favorite=1 AND `+trackVisibleSQL("t.id")+` ORDER BY t.id LIMIT 1`, cursor).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
