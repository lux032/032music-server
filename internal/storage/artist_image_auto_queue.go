package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ArtistImageAutoQueueItem represents a newly MusicBrainz-confirmed artist
// queued for background AI-gated avatar acquisition or unaudited-avatar audit.
type ArtistImageAutoQueueItem struct {
	ArtistID      int64  `json:"artistId"`
	ArtistName    string `json:"artistName"`
	TriggerSource string `json:"triggerSource"`
	Status        string `json:"status"`
	Attempts      int    `json:"attempts"`
	NextRetryAt   string `json:"nextRetryAt,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

// ArtistImageAutoQueueStats summarizes persistent queue backlog for worker
// wake scheduling and admin status display.
type ArtistImageAutoQueueStats struct {
	Pending     int    `json:"pending"`
	Running     int    `json:"running"`
	Failed      int    `json:"failed"`
	NextRetryAt string `json:"nextRetryAt,omitempty"`
}

// ArtistHasConfirmedMusicBrainz reports whether artistID (or a merged-in
// source artist) currently holds a confirmed MusicBrainz external profile.
func (s *Store) ArtistHasConfirmedMusicBrainz(ctx context.Context, artistID int64) (bool, error) {
	canonicalID, err := s.CanonicalArtistID(ctx, artistID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var has bool
	err = s.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM artist_external_profiles p
		WHERE p.source='musicbrainz'
		  AND (p.artist_id=? OR p.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=?))
	)`, canonicalID, canonicalID).Scan(&has)
	return has, err
}

// HasPendingArtistImageCandidate reports whether artistID currently has any
// unresolved review card in artist_image_candidates.
func (s *Store) HasPendingArtistImageCandidate(ctx context.Context, artistID int64) (bool, error) {
	var has bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_image_candidates WHERE artist_id=? AND status='pending')`, artistID).Scan(&has)
	return has, err
}

// EnqueueArtistImageAutoQueue idempotently inserts or re-arms a canonical
// unmerged artist in artist_image_auto_queue only when the artist has no
// custom avatar and holds a confirmed MusicBrainz identity.
func (s *Store) EnqueueArtistImageAutoQueue(ctx context.Context, artistID int64, triggerSource string) (bool, error) {
	if artistID <= 0 {
		return false, nil
	}
	triggerSource = strings.TrimSpace(triggerSource)
	if triggerSource == "" {
		triggerSource = "auto_match"
	}
	canonicalID, err := s.CanonicalArtistID(ctx, artistID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_auto_queue SET artist_id=artist_id WHERE 0`); err != nil {
		return false, err
	}
	var eligible bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM artists a
		WHERE a.id=? AND a.merged_into_artist_id IS NULL
		  AND EXISTS(
		      SELECT 1 FROM artist_external_profiles p
		      WHERE p.source='musicbrainz'
		        AND (p.artist_id=a.id OR p.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=a.id))
		  )
		  AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id)
		  AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id)
	)`, canonicalID).Scan(&eligible); err != nil {
		return false, err
	}
	if !eligible {
		return false, nil
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO artist_image_auto_queue(artist_id,trigger_source,status,attempts,next_retry_at,last_error)
		VALUES(?,?,'pending',0,NULL,'')
		ON CONFLICT(artist_id) DO UPDATE SET
			trigger_source=excluded.trigger_source,
			status='pending',
			attempts=0,
			next_retry_at=NULL,
			last_error='',
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE artist_image_auto_queue.status<>'running'`, canonicalID, triggerSource)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ClaimNextArtistImageAutoQueueItem claims the oldest ready pending item in
// artist_image_auto_queue and marks it running. Returns sql.ErrNoRows when no
// ready item is currently due.
func (s *Store) ClaimNextArtistImageAutoQueueItem(ctx context.Context) (ArtistImageAutoQueueItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ArtistImageAutoQueueItem{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_auto_queue SET artist_id=artist_id WHERE 0`); err != nil {
		return ArtistImageAutoQueueItem{}, err
	}
	var item ArtistImageAutoQueueItem
	err = tx.QueryRowContext(ctx, `SELECT q.artist_id,COALESCE(a.user_display_name,a.display_name),q.trigger_source,q.status,q.attempts,COALESCE(q.next_retry_at,''),q.last_error,q.created_at,q.updated_at
		FROM artist_image_auto_queue q
		JOIN artists a ON a.id=q.artist_id
		WHERE q.status='pending'
		  AND (q.next_retry_at IS NULL OR q.next_retry_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ORDER BY COALESCE(q.next_retry_at,q.created_at) ASC, q.created_at ASC
		LIMIT 1`).Scan(
		&item.ArtistID, &item.ArtistName, &item.TriggerSource, &item.Status,
		&item.Attempts, &item.NextRetryAt, &item.LastError, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return ArtistImageAutoQueueItem{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_auto_queue SET status='running',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE artist_id=?`, item.ArtistID); err != nil {
		return ArtistImageAutoQueueItem{}, err
	}
	if err = tx.Commit(); err != nil {
		return ArtistImageAutoQueueItem{}, err
	}
	item.Status = "running"
	return item, nil
}

// CompleteArtistImageAutoQueueItem removes a finished item from the persistent
// auto-ingest queue.
func (s *Store) CompleteArtistImageAutoQueueItem(ctx context.Context, artistID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM artist_image_auto_queue WHERE artist_id=?`, artistID)
	return err
}

// RetryArtistImageAutoQueueItem increments attempts for a transiently failed
// item and schedules a future retry until maxAttempts is reached, after which
// the row transitions to failed.
func (s *Store) RetryArtistImageAutoQueueItem(ctx context.Context, artistID int64, backoff time.Duration, maxAttempts int, errText string) (bool, error) {
	if maxAttempts < 1 {
		maxAttempts = 3
	}
	errText = strings.TrimSpace(errText)
	if runes := []rune(errText); len(runes) > 400 {
		errText = strings.TrimSpace(string(runes[:400]))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_auto_queue SET artist_id=artist_id WHERE 0`); err != nil {
		return false, err
	}
	var attempts int
	if err = tx.QueryRowContext(ctx, `SELECT attempts FROM artist_image_auto_queue WHERE artist_id=?`, artistID).Scan(&attempts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	attempts++
	if attempts >= maxAttempts {
		if _, err = tx.ExecContext(ctx, `UPDATE artist_image_auto_queue SET status='failed',attempts=?,next_retry_at=NULL,last_error=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE artist_id=?`, attempts, errText, artistID); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	secs := int(backoff / time.Second)
	if secs < 1 {
		secs = 1
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_auto_queue SET status='pending',attempts=?,next_retry_at=strftime('%Y-%m-%dT%H:%M:%fZ','now',?),last_error=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE artist_id=?`,
		attempts, fmt.Sprintf("+%d seconds", secs), errText, artistID); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// RecoverArtistImageAutoQueue resets any rows left in running state by a prior
// server restart back to pending so the worker resumes cleanly.
func (s *Store) RecoverArtistImageAutoQueue(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE artist_image_auto_queue SET status='pending',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status='running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ArtistImageAutoQueueStats reports current pending/running/failed counts and
// the earliest scheduled retry timestamp among future pending rows.
func (s *Store) ArtistImageAutoQueueStats(ctx context.Context) (ArtistImageAutoQueueStats, error) {
	var stats ArtistImageAutoQueueStats
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status='running' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END), 0),
		COALESCE(MIN(CASE WHEN status='pending' AND next_retry_at IS NOT NULL AND next_retry_at>strftime('%Y-%m-%dT%H:%M:%fZ','now') THEN next_retry_at END), '')
	FROM artist_image_auto_queue`).Scan(&stats.Pending, &stats.Running, &stats.Failed, &stats.NextRetryAt)
	return stats, err
}
