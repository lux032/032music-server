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
}

type PlaybackRecord struct {
	TrackID         int64  `json:"trackId"`
	State           string `json:"state"`
	PositionMillis  int64  `json:"positionMillis"`
	DurationMillis  int64  `json:"durationMillis"`
	PlayCount       int64  `json:"playCount"`
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

func updateFavorite(ctx context.Context, db *sql.DB, table string, id int64, favorite bool) error {
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
	result := make([]Album, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		album, err := s.AlbumByID(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, album)
	}
	return result, total, rows.Err()
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
	result := make([]Track, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		track, err := s.TrackByID(ctx, id)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, track)
	}
	return result, total, rows.Err()
}

func (s *Store) TrackByID(ctx context.Context, id int64) (Track, error) {
	var value Track
	var favorite int
	err := s.db.QueryRowContext(ctx, `SELECT
		t.id,a.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),
		COALESCE((SELECT GROUP_CONCAT(DISTINCT COALESCE(ar.user_display_name,ar.display_name)) FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.track_id=t.id),'Unknown Artist'),
		COALESCE(a.user_release_year,a.release_year,0),t.disc_number,t.track_number,COALESCE(t.user_composer,t.composer,''),
		COALESCE((SELECT GROUP_CONCAT(gx.name,',') FROM track_genre_overrides ox JOIN genres gx ON gx.id=ox.genre_id WHERE ox.track_id=t.id ORDER BY ox.position),(SELECT GROUP_CONCAT(gx.name,',') FROM track_genres rx JOIN genres gx ON gx.id=rx.genre_id WHERE rx.track_id=t.id ORDER BY rx.position),''),
		COALESCE((SELECT af.container FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
		COALESCE((SELECT af.mime_type FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
		COALESCE((SELECT af.relative_path FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
		COALESCE((SELECT af.file_size FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),0),
		COALESCE((SELECT '/api/v1/artwork/'||aw.id FROM artworks aw WHERE aw.album_id=a.id ORDER BY aw.is_primary DESC,aw.id LIMIT 1),''),
		COALESCE(t.duration_ms,0),'/api/v1/tracks/'||t.id||'/stream',t.added_at,t.updated_at,t.is_favorite,
		COALESCE(pp.last_played_at,''),COALESCE(pp.position_ms,0),COALESCE(pp.play_count,0)
		FROM tracks t JOIN albums a ON a.id=t.album_id LEFT JOIN playback_progress pp ON pp.track_id=t.id WHERE t.id=?`, id).Scan(
		&value.ID, &value.AlbumID, &value.Title, &value.Album, &value.Artist, &value.Year, &value.DiscNumber, &value.TrackNumber,
		&value.Composer, &value.Genres, &value.Container, &value.MIMEType, &value.RelativePath, &value.FileSize, &value.ArtworkURL,
		&value.DurationMillis, &value.StreamURL, &value.AddedAt, &value.UpdatedAt, &favorite, &value.LastPlayedAt, &value.PositionMillis, &value.PlayCount,
	)
	value.IsFavorite = favorite != 0
	return value, err
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
	if len(trackIDs) > 5000 {
		return errors.New("playlist cannot contain more than 5000 tracks")
	}
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
	seen := make(map[int64]struct{}, len(trackIDs))
	position := 0
	for _, trackID := range trackIDs {
		if trackID <= 0 {
			return fmt.Errorf("invalid track id %d", trackID)
		}
		if _, ok := seen[trackID]; ok {
			continue
		}
		seen[trackID] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT INTO playlist_items(playlist_id,track_id,position) VALUES(?,?,?)`, playlistID, trackID, position); err != nil {
			return fmt.Errorf("add track %d to playlist: %w", trackID, err)
		}
		position++
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
	tracks := make([]Track, 0)
	for rows.Next() {
		var trackID int64
		if err := rows.Scan(&trackID); err != nil {
			return PlaylistDetail{}, err
		}
		track, err := s.TrackByID(ctx, trackID)
		if err != nil {
			return PlaylistDetail{}, err
		}
		tracks = append(tracks, track)
	}
	return PlaylistDetail{Playlist: playlist, Tracks: tracks}, rows.Err()
}

func (s *Store) UpdatePlayback(ctx context.Context, update PlaybackUpdate) error {
	if update.TrackID <= 0 {
		return errors.New("trackId must be positive")
	}
	if update.PositionMillis < 0 || update.DurationMillis < 0 {
		return errors.New("playback times must not be negative")
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
	query := `INSERT INTO playback_progress(track_id,state,position_ms,duration_ms,last_played_at) VALUES(?,?,?,?,` + lastPlayed + `)
		ON CONFLICT(track_id) DO UPDATE SET state=excluded.state,position_ms=excluded.position_ms,duration_ms=MAX(playback_progress.duration_ms,excluded.duration_ms),last_played_at=COALESCE(excluded.last_played_at,playback_progress.last_played_at),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`
	if _, err := s.db.ExecContext(ctx, query, update.TrackID, update.State, update.PositionMillis, update.DurationMillis); err != nil {
		return err
	}
	if update.DurationMillis > 0 {
		_, _ = s.db.ExecContext(ctx, `UPDATE tracks SET duration_ms=? WHERE id=? AND COALESCE(duration_ms,0)=0`, update.DurationMillis, update.TrackID)
	}
	return nil
}

func (s *Store) Scrobble(ctx context.Context, trackID int64, positionMillis, durationMillis int64) error {
	if trackID <= 0 || positionMillis < 0 || durationMillis < 0 {
		return errors.New("invalid scrobble payload")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO playback_progress(track_id,state,position_ms,duration_ms,play_count,last_played_at,last_completed_at) VALUES(?,'stopped',?,?,1,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT(track_id) DO UPDATE SET state='stopped',position_ms=excluded.position_ms,duration_ms=MAX(playback_progress.duration_ms,excluded.duration_ms),play_count=playback_progress.play_count+1,last_played_at=excluded.last_played_at,last_completed_at=excluded.last_completed_at,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, trackID, positionMillis, durationMillis)
	if err == nil && durationMillis > 0 {
		_, _ = s.db.ExecContext(ctx, `UPDATE tracks SET duration_ms=? WHERE id=? AND COALESCE(duration_ms,0)=0`, durationMillis, trackID)
	}
	return err
}

func (s *Store) PlaybackHistory(ctx context.Context, limit, offset int) ([]PlaybackRecord, int64, error) {
	limit, offset = page(Filters{Limit: limit, Offset: offset})
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playback_progress WHERE last_played_at IS NOT NULL`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT track_id,state,position_ms,duration_ms,play_count,COALESCE(last_played_at,''),COALESCE(last_completed_at,''),updated_at FROM playback_progress WHERE last_played_at IS NOT NULL ORDER BY last_played_at DESC,track_id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]PlaybackRecord, 0)
	for rows.Next() {
		var value PlaybackRecord
		if err := rows.Scan(&value.TrackID, &value.State, &value.PositionMillis, &value.DurationMillis, &value.PlayCount, &value.LastPlayedAt, &value.LastCompletedAt, &value.UpdatedAt); err != nil {
			return nil, 0, err
		}
		track, err := s.TrackByID(ctx, value.TrackID)
		if err != nil {
			return nil, 0, err
		}
		value.Track = track
		result = append(result, value)
	}
	return result, total, rows.Err()
}

func (s *Store) ClearPlaybackHistory(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM playback_progress`)
	return err
}
