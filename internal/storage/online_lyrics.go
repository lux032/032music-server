package storage

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Online lyrics cache states.
const (
	OnlineLyricsFound      = "found"
	OnlineLyricsNotFound   = "not_found"
	OnlineLyricsSuppressed = "suppressed"
)

// OnlineLyrics is the cached online lyrics state of one track.
type OnlineLyrics struct {
	TrackID           int64
	Title, Artist     string // local track (list view only)
	Status            string
	Manual            bool
	Source            string
	RemoteID          string
	RemoteTitle       string
	RemoteArtist      string
	Lyrics            string
	TranslationLyrics string
	RetryAfter        string
	UpdatedAt         string
}

// LyricsTrackInfo is what online lyric matching needs about a track.
type LyricsTrackInfo struct {
	TrackID    int64
	Title      string
	Artist     string
	Album      string
	DurationMs int64
	TrackType  string
}

// Instrumental reports track types that never have lyrics.
func (v LyricsTrackInfo) Instrumental() bool {
	return v.TrackType == "instrumental" || v.TrackType == "off_vocal"
}

// LyricsTrackInfo returns the effective (user-edited) metadata; sql.ErrNoRows when missing.
func (s *Store) LyricsTrackInfo(ctx context.Context, trackID int64) (LyricsTrackInfo, error) {
	var v LyricsTrackInfo
	err := s.db.QueryRowContext(ctx, `SELECT t.id, COALESCE(t.user_title, t.title), `+trackArtistSQL+`, COALESCE(a.user_title, a.title),
		COALESCE(t.duration_ms, 0), `+effectiveTrackTypeSQL+`
		FROM tracks t JOIN albums a ON a.id = t.album_id WHERE t.id = ?`, trackID).
		Scan(&v.TrackID, &v.Title, &v.Artist, &v.Album, &v.DurationMs, &v.TrackType)
	return v, err
}

// FindLyricsTracks finds tracks by (effective) title or artist for the lyrics diagnosis page.
func (s *Store) FindLyricsTracks(ctx context.Context, query string, limit int) ([]LyricsTrackInfo, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, COALESCE(t.user_title, t.title), `+trackArtistSQL+`, COALESCE(a.user_title, a.title),
		COALESCE(t.duration_ms, 0), `+effectiveTrackTypeSQL+`
		FROM tracks t JOIN albums a ON a.id = t.album_id
		WHERE COALESCE(t.user_title, t.title) LIKE '%'||?||'%' OR `+trackArtistSQL+` LIKE '%'||?||'%'
		ORDER BY COALESCE(t.user_title, t.title), t.id LIMIT ?`, query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LyricsTrackInfo
	for rows.Next() {
		var v LyricsTrackInfo
		if err := rows.Scan(&v.TrackID, &v.Title, &v.Artist, &v.Album, &v.DurationMs, &v.TrackType); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

const onlineLyricsColumns = `o.track_id, COALESCE(t.user_title, t.title), ` + trackArtistSQL + `, o.status, o.manual, o.source, o.remote_id,
	o.remote_title, o.remote_artist, o.lyrics, o.translation_lyrics, COALESCE(o.retry_after, ''), o.updated_at
	FROM online_lyrics o JOIN tracks t ON t.id = o.track_id JOIN albums a ON a.id = t.album_id`

func scanOnlineLyrics(row interface{ Scan(...any) error }) (OnlineLyrics, error) {
	var v OnlineLyrics
	var manual int
	err := row.Scan(&v.TrackID, &v.Title, &v.Artist, &v.Status, &manual, &v.Source, &v.RemoteID, &v.RemoteTitle, &v.RemoteArtist,
		&v.Lyrics, &v.TranslationLyrics, &v.RetryAfter, &v.UpdatedAt)
	v.Manual = manual != 0
	return v, err
}

// OnlineLyrics returns the cache row of a track; sql.ErrNoRows when there is none.
func (s *Store) OnlineLyrics(ctx context.Context, trackID int64) (OnlineLyrics, error) {
	return scanOnlineLyrics(s.db.QueryRowContext(ctx, `SELECT `+onlineLyricsColumns+` WHERE o.track_id = ?`, trackID))
}

// SaveOnlineLyricsFound stores found lyrics (manual = chosen by a user).
// sql.ErrNoRows when the track does not exist.
func (s *Store) SaveOnlineLyricsFound(ctx context.Context, v OnlineLyrics) error {
	if strings.TrimSpace(v.Lyrics) == "" {
		return errors.New("empty lyrics")
	}
	return s.saveOnlineLyrics(ctx, OnlineLyrics{TrackID: v.TrackID, Status: OnlineLyricsFound, Manual: v.Manual, Source: v.Source,
		RemoteID: v.RemoteID, RemoteTitle: v.RemoteTitle, RemoteArtist: v.RemoteArtist, Lyrics: v.Lyrics, TranslationLyrics: v.TranslationLyrics})
}

// SaveOnlineLyricsNotFound remembers a miss until retryAfter.
func (s *Store) SaveOnlineLyricsNotFound(ctx context.Context, trackID int64, retryAfter time.Time) error {
	return s.saveOnlineLyrics(ctx, OnlineLyrics{TrackID: trackID, Status: OnlineLyricsNotFound, RetryAfter: retryAfter.UTC().Format(time.RFC3339Nano)})
}

// SuppressOnlineLyrics stops automatic online lyrics for a track.
func (s *Store) SuppressOnlineLyrics(ctx context.Context, trackID int64) error {
	return s.saveOnlineLyrics(ctx, OnlineLyrics{TrackID: trackID, Status: OnlineLyricsSuppressed, Manual: true})
}

func (s *Store) saveOnlineLyrics(ctx context.Context, v OnlineLyrics) error {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id = ?`, v.TrackID).Scan(&exists); err != nil {
		return err
	}
	var retry any
	if v.RetryAfter != "" {
		retry = v.RetryAfter
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO online_lyrics(track_id, status, manual, source, remote_id, remote_title, remote_artist, lyrics, translation_lyrics, retry_after)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(track_id) DO UPDATE SET status = excluded.status, manual = excluded.manual, source = excluded.source,
			remote_id = excluded.remote_id, remote_title = excluded.remote_title, remote_artist = excluded.remote_artist,
			lyrics = excluded.lyrics, translation_lyrics = excluded.translation_lyrics, retry_after = excluded.retry_after,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')`,
		v.TrackID, v.Status, boolInt(v.Manual), v.Source, v.RemoteID, v.RemoteTitle, v.RemoteArtist, v.Lyrics, v.TranslationLyrics, retry)
	return err
}

// OnlineLyricsFilter filters the admin list. Status "manual" selects manual rows.
type OnlineLyricsFilter struct {
	Query  string
	Status string
	Limit  int
	Offset int
}

// ListOnlineLyrics lists cache rows (newest first) and the matching total.
func (s *Store) ListOnlineLyrics(ctx context.Context, f OnlineLyricsFilter) ([]OnlineLyrics, int64, error) {
	where := []string{"1 = 1"}
	var args []any
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, `(COALESCE(t.user_title, t.title) LIKE '%'||?||'%' OR `+trackArtistSQL+` LIKE '%'||?||'%' OR o.remote_title LIKE '%'||?||'%')`)
		args = append(args, q, q, q)
	}
	switch f.Status {
	case OnlineLyricsFound, OnlineLyricsNotFound, OnlineLyricsSuppressed:
		where = append(where, "o.status = ?")
		args = append(args, f.Status)
	case "manual":
		where = append(where, "o.manual = 1 AND o.status = 'found'")
	}
	cond := strings.Join(where, " AND ")
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM online_lyrics o JOIN tracks t ON t.id = o.track_id JOIN albums a ON a.id = t.album_id WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+onlineLyricsColumns+` WHERE `+cond+` ORDER BY o.updated_at DESC, o.track_id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []OnlineLyrics
	for rows.Next() {
		v, err := scanOnlineLyrics(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, v)
	}
	return out, total, rows.Err()
}

// OnlineLyricsCounts returns the number of rows per status.
func (s *Store) OnlineLyricsCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(*) FROM online_lyrics GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// DeleteOnlineLyrics removes cache rows; those tracks are matched again on next request.
func (s *Store) DeleteOnlineLyrics(ctx context.Context, trackIDs []int64) (int64, error) {
	if len(trackIDs) == 0 {
		return 0, nil
	}
	args := make([]any, len(trackIDs))
	for i, id := range trackIDs {
		args[i] = id
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM online_lyrics WHERE track_id IN (`+sqlPlaceholders(len(trackIDs))+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearOnlineLyricsNotFound removes every not-found row so they are retried now.
func (s *Store) ClearOnlineLyricsNotFound(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM online_lyrics WHERE status = 'not_found'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
