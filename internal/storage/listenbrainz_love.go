package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ListenBrainzLoveItem struct {
	TrackID          int64
	Desired          bool
	Artist           string
	Track            string
	Album            string
	TagRecordingMBID string
	Attempts         int
}

type ListenBrainzLoveQueueStats struct {
	Pending   int64
	Failed    int64
	NextDue   int64
	LastErr   string
	LastErrAt string
}

type ListenBrainzLoveBackfill struct {
	State       string
	Total       int64
	Processed   int64
	Failed      int64
	SkippedNoID int64
	Cursor      int64
	LastError   string
	StartedAt   string
	FinishedAt  string
}

func (b ListenBrainzLoveBackfill) Percent() float64 {
	if b.Total <= 0 {
		return 0
	}
	percent := float64(b.Processed) / float64(b.Total) * 100
	if percent > 100 {
		percent = 100
	}
	return percent
}

func (b ListenBrainzLoveBackfill) Done() bool { return b.State == LoveBackfillDone }

func enqueueListenBrainzLove(ctx context.Context, tx *sql.Tx, trackID int64, desired bool) error {
	var syncLoves, feedbackUnsupported int
	var serverURL, userToken string
	err := tx.QueryRowContext(ctx, `SELECT sync_loves,feedback_unsupported,server_url,user_token FROM listenbrainz_settings WHERE id=1`).
		Scan(&syncLoves, &feedbackUnsupported, &serverURL, &userToken)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if syncLoves == 0 || feedbackUnsupported != 0 || strings.TrimSpace(serverURL) == "" || strings.TrimSpace(userToken) == "" {
		return nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO listenbrainz_love_queue(track_id,desired,attempts,next_attempt_at,last_error) VALUES(?,?,0,0,'')
		ON CONFLICT(track_id) DO UPDATE SET desired=excluded.desired,attempts=0,next_attempt_at=0,last_error=''`, trackID, boolInt(desired))
	return err
}

func (s *Store) SetListenBrainzLoveSync(ctx context.Context, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings SET sync_loves=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, boolInt(enabled))
	return err
}

func (s *Store) ListenBrainzLoveTrack(ctx context.Context, trackID int64) (ListenBrainzLoveItem, error) {
	var item ListenBrainzLoveItem
	err := s.db.QueryRowContext(ctx, `SELECT t.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),`+trackArtistSQL+`
		FROM tracks t JOIN albums a ON a.id=t.album_id WHERE t.id=?`, trackID).
		Scan(&item.TrackID, &item.Track, &item.Album, &item.Artist)
	if err != nil {
		return item, err
	}
	item.Track = strings.TrimSpace(item.Track)
	item.Album = strings.TrimSpace(item.Album)
	item.Artist = strings.TrimSpace(item.Artist)
	item.TagRecordingMBID = lookupTagRecordingMBID(ctx, s.db, trackID)
	return item, nil
}

func (s *Store) DueListenBrainzLoves(ctx context.Context, now time.Time, limit int) ([]ListenBrainzLoveItem, error) {
	if limit <= 0 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `SELECT q.track_id,q.desired,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),`+trackArtistSQL+`,q.attempts
		FROM listenbrainz_love_queue q JOIN tracks t ON t.id=q.track_id JOIN albums a ON a.id=t.album_id
		WHERE q.next_attempt_at<=? ORDER BY q.track_id LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ListenBrainzLoveItem, 0, limit)
	for rows.Next() {
		var item ListenBrainzLoveItem
		var desired int
		if err := rows.Scan(&item.TrackID, &desired, &item.Track, &item.Album, &item.Artist, &item.Attempts); err != nil {
			return nil, err
		}
		item.Desired = desired != 0
		item.Track = strings.TrimSpace(item.Track)
		item.Album = strings.TrimSpace(item.Album)
		item.Artist = strings.TrimSpace(item.Artist)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		items[i].TagRecordingMBID = lookupTagRecordingMBID(ctx, s.db, items[i].TrackID)
	}
	return items, nil
}

func (s *Store) NextListenBrainzLoveAttempt(ctx context.Context) (time.Time, error) {
	var next sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(next_attempt_at) FROM listenbrainz_love_queue`).Scan(&next); err != nil || !next.Valid {
		return time.Time{}, err
	}
	return time.Unix(next.Int64, 0), nil
}

func (s *Store) CompleteListenBrainzLove(ctx context.Context, trackID int64, desired bool) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM listenbrainz_love_queue WHERE track_id=? AND desired=?`, trackID, boolInt(desired))
	return err
}

func (s *Store) DeferListenBrainzLove(ctx context.Context, trackID int64, next time.Time, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_love_queue SET attempts=attempts+1,next_attempt_at=?,last_error=? WHERE track_id=?`, next.Unix(), message, trackID)
	return err
}

func (s *Store) DropListenBrainzLove(ctx context.Context, trackID int64, message string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM listenbrainz_love_queue WHERE track_id=?`, trackID); err != nil {
		return err
	}
	return s.SetListenBrainzLoveError(ctx, message)
}

func (s *Store) RetryListenBrainzLovesNow(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_love_queue SET next_attempt_at=0`)
	return err
}

func (s *Store) ListenBrainzLoveQueueStats(ctx context.Context) (ListenBrainzLoveQueueStats, error) {
	var value ListenBrainzLoveQueueStats
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN attempts>=? THEN 1 ELSE 0 END),0),MIN(next_attempt_at),
		COALESCE((SELECT last_error FROM listenbrainz_love_queue ORDER BY track_id LIMIT 1),'')
		FROM listenbrainz_love_queue`, loveFailedAfterAttempts).Scan(&value.Pending, &value.Failed, &next, &value.LastErr)
	if err != nil {
		return value, err
	}
	if next.Valid {
		value.NextDue = next.Int64
	}
	return value, nil
}

func (s *Store) SetListenBrainzLoveError(ctx context.Context, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_settings SET last_love_error=?,last_love_error_at=CASE WHEN ?='' THEN NULL ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END WHERE id=1`, message, message)
	return err
}

func (s *Store) BeginListenBrainzLoveBackfill(ctx context.Context, total int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_love_backfill SET state='running',total=?,processed=0,failed=0,skipped_no_id=0,cursor=0,last_error='',started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),finished_at=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, total)
	return err
}

func (s *Store) ResumeListenBrainzLoveBackfill(ctx context.Context, total int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_love_backfill SET state='running',total=?,last_error='',finished_at=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND state='cancelled'`, total)
	return err
}

func (s *Store) CountLoveBackfillTracksAfter(ctx context.Context, cursor int64) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks t WHERE t.id>? AND t.is_favorite=1 AND `+trackVisibleSQL("t.id"), cursor).Scan(&total)
	return total, err
}

func (s *Store) CancelListenBrainzLoveBackfill(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_love_backfill SET state='cancelled',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND state='running'`)
	return err
}

func (s *Store) FinishListenBrainzLoveBackfill(ctx context.Context, state, lastError string) error {
	if state != LoveBackfillDone && state != LoveBackfillCancelled {
		return fmt.Errorf("invalid backfill state %q", state)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_love_backfill SET state=?,last_error=?,finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, state, lastError)
	return err
}

func (s *Store) AdvanceListenBrainzLoveBackfill(ctx context.Context, cursor int64, failed, skippedNoID int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE listenbrainz_love_backfill SET cursor=?,processed=processed+1,failed=failed+?,skipped_no_id=skipped_no_id+?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND state='running'`, cursor, failed, skippedNoID)
	return err
}

func (s *Store) ListenBrainzLoveBackfill(ctx context.Context) (ListenBrainzLoveBackfill, error) {
	var value ListenBrainzLoveBackfill
	var started, finished sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT state,total,processed,failed,skipped_no_id,cursor,last_error,started_at,finished_at FROM listenbrainz_love_backfill WHERE id=1`).
		Scan(&value.State, &value.Total, &value.Processed, &value.Failed, &value.SkippedNoID, &value.Cursor, &value.LastError, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return ListenBrainzLoveBackfill{State: LoveBackfillIdle}, nil
	}
	value.StartedAt = started.String
	value.FinishedAt = finished.String
	return value, err
}

func (s *Store) NextListenBrainzLoveBackfillTrack(ctx context.Context, cursor int64) (int64, error) {
	return s.NextLoveBackfillTrack(ctx, cursor)
}
