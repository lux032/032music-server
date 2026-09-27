package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type Playlist struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ItemCount   int64  `json:"itemCount"`
	ArtworkURL  string `json:"artworkUrl,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type PlaylistDetail struct {
	Playlist Playlist `json:"playlist"`
	Tracks   []Track  `json:"tracks"`
}

type PlaybackUpdate struct {
	TrackID        int64  `json:"trackId"`
	State          string `json:"state"`
	PositionMillis int64  `json:"positionMillis"`
	DurationMillis int64  `json:"durationMillis"`
	Continuing     bool   `json:"continuing"`
	ClientID       string `json:"clientId,omitempty"`
	Skipped        *bool  `json:"skipped,omitempty"`
}

type PlaybackRecord struct {
	TrackID         int64  `json:"trackId"`
	State           string `json:"state"`
	PositionMillis  int64  `json:"positionMillis"`
	DurationMillis  int64  `json:"durationMillis"`
	PlayCount       int64  `json:"playCount"`
	SkipCount       int64  `json:"skipCount"`
	LastSkippedAt   string `json:"lastSkippedAt,omitempty"`
	LastPlayedAt    string `json:"lastPlayedAt,omitempty"`
	LastCompletedAt string `json:"lastCompletedAt,omitempty"`
	UpdatedAt       string `json:"updatedAt"`
	Track           Track  `json:"track"`
}

func (s *Store) SetAlbumFavorite(ctx context.Context, id int64, favorite bool) error {
	return updateFavorite(ctx, s.db, "albums", id, favorite)
}

func (s *Store) SetTrackFavorite(ctx context.Context, id int64, favorite bool) error {
	return updateFavorite(ctx, s.db, "tracks", id, favorite)
}

func updateFavorite(ctx context.Context, db retryDB, table string, id int64, favorite bool) error {
	value := 0
	if favorite {
		value = 1
	}
	result, err := db.ExecContext(ctx, "UPDATE "+table+" SET is_favorite=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", value, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) FavoriteAlbums(ctx context.Context, limit, offset int) ([]Album, int64, error) {
	limit, offset = page(Filters{Limit: limit, Offset: offset})
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM albums WHERE is_favorite=1`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM albums WHERE is_favorite=1 ORDER BY updated_at DESC,id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	result, err := s.albumsByIDs(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	return result, total, nil
}

func (s *Store) FavoriteTracks(ctx context.Context, limit, offset int) ([]Track, int64, error) {
	limit, offset = page(Filters{Limit: limit, Offset: offset})
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE is_favorite=1`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tracks WHERE is_favorite=1 ORDER BY updated_at DESC,id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	result, err := s.tracksByIDs(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	return result, total, nil
}

const trackByIDSelect = `SELECT
	t.id,a.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),
	` + trackArtistSQL + `,
	COALESCE(a.user_release_year,a.release_year,0),COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),COALESCE(t.user_composer,t.composer,''),COALESCE(t.lyricist,''),COALESCE(t.arranger,''),COALESCE(NULLIF(t.user_track_type,''),NULLIF(t.track_type,''),'regular'),
	COALESCE((SELECT GROUP_CONCAT(gx.name,',' ORDER BY ox.position, gx.id) FROM track_genre_overrides ox JOIN genres gx ON gx.id=ox.genre_id WHERE ox.track_id=t.id),(SELECT GROUP_CONCAT(gx.name,',' ORDER BY rx.position, gx.id) FROM track_genres rx JOIN genres gx ON gx.id=rx.genre_id WHERE rx.track_id=t.id),''),
	COALESCE((SELECT af.container FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
	COALESCE((SELECT af.mime_type FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
	COALESCE((SELECT af.relative_path FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
	COALESCE((SELECT af.file_size FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),0),
	COALESCE((SELECT '/api/v1/artwork/'||aw.id FROM artworks aw WHERE aw.album_id=a.id ORDER BY aw.is_primary DESC,aw.id LIMIT 1),''),
	COALESCE(t.duration_ms,0),'/api/v1/tracks/'||t.id||'/stream',t.added_at,t.updated_at,t.is_favorite,
	COALESCE(pp.last_played_at,''),COALESCE(pp.position_ms,0),COALESCE(pp.play_count,0)
	FROM tracks t JOIN albums a ON a.id=t.album_id LEFT JOIN playback_progress pp ON pp.track_id=t.id`

func scanTrack(row interface{ Scan(...any) error }) (Track, error) {
	var value Track
	var favorite int
	err := row.Scan(
		&value.ID, &value.AlbumID, &value.Title, &value.Album, &value.Artist, &value.Year, &value.DiscNumber, &value.TrackNumber,
		&value.Composer, &value.Lyricist, &value.Arranger, &value.TrackType, &value.Genres, &value.Container, &value.MIMEType, &value.RelativePath, &value.FileSize, &value.ArtworkURL,
		&value.DurationMillis, &value.StreamURL, &value.AddedAt, &value.UpdatedAt, &favorite, &value.LastPlayedAt, &value.PositionMillis, &value.PlayCount,
	)
	value.IsFavorite = favorite != 0
	return value, err
}

func (s *Store) TrackByID(ctx context.Context, id int64) (Track, error) {
	track, err := scanTrack(s.db.QueryRowContext(ctx, trackByIDSelect+` WHERE t.id=?`, id))
	if err != nil {
		return track, err
	}
	items := []Track{track}
	err = s.hydrateTracks(ctx, items)
	return items[0], err
}

// tracksByIDs loads many tracks in a single query (N+1 fix) and returns them
// in the order of the given ids. A missing id yields sql.ErrNoRows.
func (s *Store) tracksByIDs(ctx context.Context, ids []int64) ([]Track, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders, args := inClause(ids)
	rows, err := s.db.QueryContext(ctx, trackByIDSelect+` WHERE t.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]Track{}
	for rows.Next() {
		track, err := scanTrack(rows)
		if err != nil {
			return nil, err
		}
		byID[track.ID] = track
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Track, 0, len(ids))
	for _, id := range ids {
		track, ok := byID[id]
		if !ok {
			return nil, sql.ErrNoRows
		}
		result = append(result, track)
	}
	rows.Close()
	return result, s.hydrateTracks(ctx, result)
}

func (s *Store) ListPlaylists(ctx context.Context, limit, offset int) ([]Playlist, int64, error) {
	limit, offset = page(Filters{Limit: limit, Offset: offset})
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playlists`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.name,p.description,COUNT(pi.track_id),COALESCE((SELECT '/api/v1/artwork/'||aw.id FROM playlist_items first JOIN tracks t ON t.id=first.track_id JOIN artworks aw ON aw.album_id=t.album_id WHERE first.playlist_id=p.id ORDER BY first.position,aw.is_primary DESC,aw.id LIMIT 1),''),p.created_at,p.updated_at FROM playlists p LEFT JOIN playlist_items pi ON pi.playlist_id=p.id GROUP BY p.id ORDER BY p.updated_at DESC,p.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]Playlist, 0)
	for rows.Next() {
		var value Playlist
		if err := rows.Scan(&value.ID, &value.Name, &value.Description, &value.ItemCount, &value.ArtworkURL, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, value)
	}
	return result, total, rows.Err()
}

func (s *Store) PlaylistByID(ctx context.Context, id int64) (Playlist, error) {
	var value Playlist
	err := s.db.QueryRowContext(ctx, `SELECT p.id,p.name,p.description,COUNT(pi.track_id),COALESCE((SELECT '/api/v1/artwork/'||aw.id FROM playlist_items first JOIN tracks t ON t.id=first.track_id JOIN artworks aw ON aw.album_id=t.album_id WHERE first.playlist_id=p.id ORDER BY first.position,aw.is_primary DESC,aw.id LIMIT 1),''),p.created_at,p.updated_at FROM playlists p LEFT JOIN playlist_items pi ON pi.playlist_id=p.id WHERE p.id=? GROUP BY p.id`, id).Scan(&value.ID, &value.Name, &value.Description, &value.ItemCount, &value.ArtworkURL, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}

func (s *Store) CreatePlaylist(ctx context.Context, name, description string) (Playlist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Playlist{}, errors.New("playlist name must not be empty")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO playlists(name,description) VALUES(?,?)`, name, strings.TrimSpace(description))
	if err != nil {
		return Playlist{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Playlist{}, err
	}
	return s.PlaylistByID(ctx, id)
}

func (s *Store) UpdatePlaylist(ctx context.Context, id int64, name, description string) (Playlist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Playlist{}, errors.New("playlist name must not be empty")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE playlists SET name=?,description=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, name, strings.TrimSpace(description), id)
	if err != nil {
		return Playlist{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Playlist{}, err
	}
	if count == 0 {
		return Playlist{}, sql.ErrNoRows
	}
	return s.PlaylistByID(ctx, id)
}

func (s *Store) DeletePlaylist(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM playlists WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ReplacePlaylistItems(ctx context.Context, playlistID int64, trackIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM playlists WHERE id=?)`, playlistID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM playlist_items WHERE playlist_id=?`, playlistID); err != nil {
		return err
	}
	if err := insertPlaylistItems(ctx, tx, playlistID, trackIDs); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE playlists SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, playlistID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PlaylistDetail(ctx context.Context, id int64) (PlaylistDetail, error) {
	playlist, err := s.PlaylistByID(ctx, id)
	if err != nil {
		return PlaylistDetail{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT track_id FROM playlist_items WHERE playlist_id=? ORDER BY position`, id)
	if err != nil {
		return PlaylistDetail{}, err
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var trackID int64
		if err := rows.Scan(&trackID); err != nil {
			return PlaylistDetail{}, err
		}
		ids = append(ids, trackID)
	}
	if err := rows.Err(); err != nil {
		return PlaylistDetail{}, err
	}
	tracks, err := s.tracksByIDs(ctx, ids)
	if err != nil {
		return PlaylistDetail{}, err
	}
	return PlaylistDetail{Playlist: playlist, Tracks: tracks}, nil
}

// maxClientDurationMillis bounds client-reported track durations. The probed
// scanner value is the source of truth; a client value is only ever used to
// backfill a missing probe, and only when it is physically plausible (M10).
const maxClientDurationMillis = 6 * 60 * 60 * 1000 // 6 hours

func plausibleClientDuration(durationMillis int64) bool {
	return durationMillis > 0 && durationMillis <= maxClientDurationMillis
}

func (s *Store) UpdatePlayback(ctx context.Context, update PlaybackUpdate) error {
	if update.TrackID <= 0 {
		return errors.New("trackId must be positive")
	}
	if update.PositionMillis < 0 || update.DurationMillis < 0 {
		return errors.New("playback times must not be negative")
	}
	if len(update.ClientID) > 128 {
		return errors.New("clientId must not exceed 128 bytes")
	}
	if update.Skipped != nil && *update.Skipped && update.State != "stopped" {
		return errors.New("skipped=true must have stopped state")
	}
	switch update.State {
	case "playing", "paused", "buffering", "stopped":
	default:
		return errors.New("invalid playback state")
	}
	lastPlayed := "NULL"
	if update.State == "playing" {
		lastPlayed = "strftime('%Y-%m-%dT%H:%M:%fZ','now')"
	}
	mode := 0 // omitted: infer; explicit false: suppress; explicit true: count
	if update.Skipped != nil {
		if *update.Skipped {
			mode = 1
		} else {
			mode = -1
		}
	}
	// The threshold subquery reads the probed track duration in the same atomic
	// statement; fallback uses the old progress duration and incoming duration.
	threshold := `MIN(30000, CASE WHEN COALESCE((SELECT duration_ms FROM tracks WHERE id=excluded.track_id),0)>0 THEN (SELECT duration_ms FROM tracks WHERE id=excluded.track_id)/2 WHEN MAX(playback_progress.duration_ms,excluded.duration_ms)>0 THEN MAX(playback_progress.duration_ms,excluded.duration_ms)/2 ELSE 30000 END)`
	inferred := `excluded.state='stopped' AND ?=1 AND playback_progress.state IN ('playing','paused','buffering') AND MAX(playback_progress.position_ms,excluded.position_ms)<` + threshold + ` AND playback_progress.last_played_at IS NOT NULL AND playback_progress.last_played_at>=strftime('%Y-%m-%dT%H:%M:%fZ','now','-30 minutes') AND (playback_progress.last_completed_at IS NULL OR playback_progress.last_completed_at<playback_progress.last_played_at)`
	skip := `(?=1 OR (?=0 AND ` + inferred + `))`
	// The INSERT branch counts only explicit skipped=true; inference requires a
	// pre-existing row (previous state), so a first-ever stopped report is not
	// counted as a skip.
	query := `INSERT INTO playback_progress(track_id,state,position_ms,duration_ms,last_played_at,skip_count,last_skipped_at) VALUES(?,?,?,?,` + lastPlayed + `,CASE WHEN ?=1 THEN 1 ELSE 0 END,CASE WHEN ?=1 THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE NULL END)
		ON CONFLICT(track_id) DO UPDATE SET state=excluded.state,position_ms=excluded.position_ms,duration_ms=MAX(playback_progress.duration_ms,excluded.duration_ms),last_played_at=COALESCE(excluded.last_played_at,playback_progress.last_played_at),skip_count=playback_progress.skip_count+CASE WHEN ` + skip + ` THEN 1 ELSE 0 END,last_skipped_at=CASE WHEN ` + skip + ` THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE playback_progress.last_skipped_at END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	if _, err := s.db.ExecContext(ctx, query, update.TrackID, update.State, update.PositionMillis, update.DurationMillis, mode, mode, mode, mode, boolInt(update.Continuing), mode, mode, boolInt(update.Continuing)); err != nil {
		return err
	}
	if plausibleClientDuration(update.DurationMillis) {
		_, _ = s.db.ExecContext(ctx, `UPDATE tracks SET duration_ms=? WHERE id=? AND COALESCE(duration_ms,0)=0`, update.DurationMillis, update.TrackID)
	}
	return nil
}

// Scrobble records a play reported now. See RecordScrobble for the 50%
// threshold and duplicate rules.
func (s *Store) Scrobble(ctx context.Context, trackID int64, positionMillis, durationMillis int64) error {
	_, err := s.RecordScrobble(ctx, ScrobbleInput{TrackID: trackID, PositionMillis: positionMillis, DurationMillis: durationMillis})
	return err
}

func (s *Store) PlaybackHistory(ctx context.Context, limit, offset int) ([]PlaybackRecord, int64, error) {
	limit, offset = page(Filters{Limit: limit, Offset: offset})
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playback_progress WHERE last_played_at IS NOT NULL`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT track_id,state,position_ms,duration_ms,play_count,skip_count,COALESCE(last_skipped_at,''),COALESCE(last_played_at,''),COALESCE(last_completed_at,''),updated_at FROM playback_progress WHERE last_played_at IS NOT NULL ORDER BY last_played_at DESC,track_id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]PlaybackRecord, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var value PlaybackRecord
		if err := rows.Scan(&value.TrackID, &value.State, &value.PositionMillis, &value.DurationMillis, &value.PlayCount, &value.SkipCount, &value.LastSkippedAt, &value.LastPlayedAt, &value.LastCompletedAt, &value.UpdatedAt); err != nil {
			return nil, 0, err
		}
		ids = append(ids, value.TrackID)
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	tracks, err := s.tracksByIDs(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range result {
		result[i].Track = tracks[i]
	}
	return result, total, nil
}

func (s *Store) ClearPlaybackHistory(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM playback_progress`)
	return err
}

func insertPlaylistItems(ctx context.Context, tx *sql.Tx, id int64, trackIDs []int64) error {
	if len(trackIDs) > 5000 {
		return errors.New("playlist cannot contain more than 5000 tracks")
	}
	seen := make(map[int64]bool, len(trackIDs))
	position := 0
	for _, trackID := range trackIDs {
		if trackID <= 0 {
			return fmt.Errorf("invalid track id %d", trackID)
		}
		if seen[trackID] {
			continue
		}
		seen[trackID] = true
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracks WHERE id=?)`, trackID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("invalid track id %d", trackID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO playlist_items(playlist_id,track_id,position) VALUES(?,?,?)`, id, trackID, position); err != nil {
			return err
		}
		position++
	}
	return nil
}
func (s *Store) CreatePlaylistWithItems(ctx context.Context, name, description string, trackIDs []int64) (Playlist, error) {
	if trackIDs == nil {
		return s.CreatePlaylist(ctx, name, description)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Playlist{}, errors.New("playlist name must not be empty")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Playlist{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO playlists(name,description) VALUES(?,?)`, name, strings.TrimSpace(description))
	if err != nil {
		return Playlist{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Playlist{}, err
	}
	if err = insertPlaylistItems(ctx, tx, id, trackIDs); err != nil {
		return Playlist{}, err
	}
	if err = tx.Commit(); err != nil {
		return Playlist{}, err
	}
	return s.PlaylistByID(ctx, id)
}
