package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Playlist struct {
	Revision         int64  `json:"revision"`
	HasCustomArtwork bool   `json:"hasCustomArtwork"`
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	ItemCount        int64  `json:"itemCount"`
	ArtworkURL       string `json:"artworkUrl,omitempty"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

type PlaylistDetail struct {
	Playlist Playlist `json:"playlist"`
	Tracks   []Track  `json:"tracks"`
}

// PlaybackRecord is one row of the play-history aggregate. State is the
// effective state derived from the track's playback sessions at read time
// (playing > buffering > paused, otherwise the most recent end reason,
// otherwise stopped for pre-session history rows); it is never read from
// playback_progress.state (B4/M5).
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
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM albums a WHERE a.is_favorite=1 AND `+albumVisibleSQL("a.id")).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id FROM albums a WHERE a.is_favorite=1 AND `+albumVisibleSQL("a.id")+` ORDER BY a.updated_at DESC,a.id DESC LIMIT ? OFFSET ?`, limit, offset)
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
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks t WHERE t.is_favorite=1 AND `+trackVisibleSQL("t.id")).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id FROM tracks t WHERE t.is_favorite=1 AND `+trackVisibleSQL("t.id")+` ORDER BY t.updated_at DESC,t.id DESC LIMIT ? OFFSET ?`, limit, offset)
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
	` + albumArtworkURLSQL + `,
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
	rows, err := s.db.QueryContext(ctx, `SELECT p.id,p.name,p.description,COUNT(pi.track_id),COALESCE((SELECT '/api/v1/playlists/'||p.id||'/artwork?v='||substr(content_hash,1,16) FROM playlist_custom_images WHERE playlist_id=p.id),(SELECT '/api/v1/artwork/'||aw.id FROM playlist_items first JOIN tracks t ON t.id=first.track_id JOIN artworks aw ON aw.album_id=t.album_id WHERE first.playlist_id=p.id ORDER BY first.position,(aw.source_type='custom') DESC,aw.is_primary DESC,aw.id LIMIT 1),''),p.created_at,p.updated_at,p.revision,EXISTS(SELECT 1 FROM playlist_custom_images WHERE playlist_id=p.id) FROM playlists p LEFT JOIN playlist_items pi ON pi.playlist_id=p.id GROUP BY p.id ORDER BY p.updated_at DESC,p.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]Playlist, 0)
	for rows.Next() {
		var value Playlist
		if err := rows.Scan(&value.ID, &value.Name, &value.Description, &value.ItemCount, &value.ArtworkURL, &value.CreatedAt, &value.UpdatedAt, &value.Revision, &value.HasCustomArtwork); err != nil {
			return nil, 0, err
		}
		result = append(result, value)
	}
	return result, total, rows.Err()
}

func (s *Store) PlaylistByID(ctx context.Context, id int64) (Playlist, error) {
	var value Playlist
	err := s.db.QueryRowContext(ctx, `SELECT p.id,p.name,p.description,COUNT(pi.track_id),COALESCE((SELECT '/api/v1/playlists/'||p.id||'/artwork?v='||substr(content_hash,1,16) FROM playlist_custom_images WHERE playlist_id=p.id),(SELECT '/api/v1/artwork/'||aw.id FROM playlist_items first JOIN tracks t ON t.id=first.track_id JOIN artworks aw ON aw.album_id=t.album_id WHERE first.playlist_id=p.id ORDER BY first.position,(aw.source_type='custom') DESC,aw.is_primary DESC,aw.id LIMIT 1),''),p.created_at,p.updated_at,p.revision,EXISTS(SELECT 1 FROM playlist_custom_images WHERE playlist_id=p.id) FROM playlists p LEFT JOIN playlist_items pi ON pi.playlist_id=p.id WHERE p.id=? GROUP BY p.id`, id).Scan(&value.ID, &value.Name, &value.Description, &value.ItemCount, &value.ArtworkURL, &value.CreatedAt, &value.UpdatedAt, &value.Revision, &value.HasCustomArtwork)
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
	result, err := s.db.ExecContext(ctx, `UPDATE playlists SET name=?,description=?,revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=? AND (name<>? OR description<>?)`, name, strings.TrimSpace(description), id, name, strings.TrimSpace(description))
	if err != nil {
		return Playlist{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Playlist{}, err
	}
	if count == 0 {
		return s.PlaylistByID(ctx, id)
	}
	return s.PlaylistByID(ctx, id)
}

func (s *Store) DeletePlaylist(ctx context.Context, id int64) error {
	_, err := s.DeletePlaylistWithImages(ctx, id)
	return err
}

func (s *Store) ReplacePlaylistItems(ctx context.Context, id int64, ids []int64) error {
	return s.ReplacePlaylistItemsAtRevision(ctx, id, ids, nil)
}

func (s *Store) PlaylistDetail(ctx context.Context, id int64) (PlaylistDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PlaylistDetail{}, err
	}
	defer tx.Rollback()
	playlist, err := playlistByIDTx(ctx, tx, id)
	if err != nil {
		return PlaylistDetail{}, err
	}
	rows, err := tx.QueryContext(ctx, trackByIDSelect+` JOIN playlist_items pi ON pi.track_id=t.id WHERE pi.playlist_id=? ORDER BY pi.position`, id)
	if err != nil {
		return PlaylistDetail{}, err
	}
	tracks := make([]Track, 0)
	for rows.Next() {
		track, err := scanTrack(rows)
		if err != nil {
			rows.Close()
			return PlaylistDetail{}, err
		}
		tracks = append(tracks, track)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return PlaylistDetail{}, err
	}
	missingRows, err := tx.QueryContext(ctx, `SELECT pi.track_id,NOT EXISTS(SELECT 1 FROM audio_files af WHERE af.track_id=pi.track_id AND af.status='available') FROM playlist_items pi WHERE pi.playlist_id=?`, id)
	if err != nil {
		return PlaylistDetail{}, err
	}
	missing := map[int64]bool{}
	for missingRows.Next() {
		var trackID int64
		var value bool
		if err = missingRows.Scan(&trackID, &value); err != nil {
			missingRows.Close()
			return PlaylistDetail{}, err
		}
		missing[trackID] = value
	}
	err = missingRows.Err()
	missingRows.Close()
	if err != nil {
		return PlaylistDetail{}, err
	}
	for i := range tracks {
		tracks[i].Missing = missing[tracks[i].ID]
	}

	if err = tx.Commit(); err != nil {
		return PlaylistDetail{}, err
	}
	// Core metadata and tracks share a snapshot; extras tolerate deleted IDs.
	if err = s.hydrateTracks(ctx, tracks); err != nil {
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

func (s *Store) PlaybackHistory(ctx context.Context, limit, offset int) ([]PlaybackRecord, int64, error) {
	limit, offset = page(Filters{Limit: limit, Offset: offset})
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM playback_progress WHERE last_played_at IS NOT NULL`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT track_id,position_ms,duration_ms,play_count,skip_count,COALESCE(last_skipped_at,''),COALESCE(last_played_at,''),COALESCE(last_completed_at,''),updated_at FROM playback_progress WHERE last_played_at IS NOT NULL ORDER BY last_played_at DESC,track_id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]PlaybackRecord, 0)
	ids := make([]int64, 0)
	for rows.Next() {
		var value PlaybackRecord
		if err := rows.Scan(&value.TrackID, &value.PositionMillis, &value.DurationMillis, &value.PlayCount, &value.SkipCount, &value.LastSkippedAt, &value.LastPlayedAt, &value.LastCompletedAt, &value.UpdatedAt); err != nil {
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
	states, err := s.effectivePlaybackStates(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	for i := range result {
		result[i].Track = tracks[i]
		result[i].State = states[result[i].TrackID]
	}
	return result, total, nil
}

// effectivePlaybackStates derives the per-track display state for one page
// of history rows from two bounded, indexed queries (M-2/M5): the open
// sessions of the page's tracks (bounded by the number of active devices,
// via idx_playback_sessions_open_track) and each track's single most
// recently ended session (an index seek per track via
// idx_playback_sessions_ended_track) — never the whole 30-day session
// history. Multi-device priority is playing > buffering > paused among
// sessions whose lease is still valid; otherwise the most recent end
// (finalized rows use their last heartbeat as ended_at, M-1) contributes
// its reason; tracks without any session keep the legacy "stopped" label
// so pre-migration rows never resurrect a stale playing.
func (s *Store) effectivePlaybackStates(ctx context.Context, trackIDs []int64) (map[int64]string, error) {
	states := make(map[int64]string, len(trackIDs))
	if len(trackIDs) == 0 {
		return states, nil
	}
	placeholders, args := inClause(trackIDs)
	byTrack := map[int64][]sessionSummary{}

	openRows, err := s.db.QueryContext(ctx, `SELECT track_id,state,last_heartbeat_at FROM playback_sessions WHERE track_id IN (`+placeholders+`) AND ended_at IS NULL`, args...)
	if err != nil {
		return nil, err
	}
	defer openRows.Close()
	for openRows.Next() {
		var trackID int64
		var summary sessionSummary
		if err := openRows.Scan(&trackID, &summary.state, &summary.heartbeat); err != nil {
			return nil, err
		}
		byTrack[trackID] = append(byTrack[trackID], summary)
	}
	if err := openRows.Err(); err != nil {
		return nil, err
	}

	endedRows, err := s.db.QueryContext(ctx, `SELECT track_id,ended_at,COALESCE(end_reason,'') FROM playback_sessions latest WHERE latest.track_id IN (`+placeholders+`) AND latest.ended_at IS NOT NULL AND latest.rowid=(SELECT rowid FROM playback_sessions WHERE track_id=latest.track_id AND ended_at IS NOT NULL ORDER BY ended_at DESC, rowid DESC LIMIT 1)`, args...)
	if err != nil {
		return nil, err
	}
	defer endedRows.Close()
	for endedRows.Next() {
		var trackID int64
		var summary sessionSummary
		if err := endedRows.Scan(&trackID, &summary.endedAt, &summary.endReason); err != nil {
			return nil, err
		}
		byTrack[trackID] = append(byTrack[trackID], summary)
	}
	if err := endedRows.Err(); err != nil {
		return nil, err
	}

	now := time.Now()
	for _, trackID := range trackIDs {
		states[trackID] = deriveEffectiveState(byTrack[trackID], now)
	}
	return states, nil
}

type sessionSummary struct {
	state, heartbeat, endedAt, endReason string
}

func deriveEffectiveState(sessions []sessionSummary, now time.Time) string {
	best := ""
	bestRank := 0
	bestHeartbeat := ""
	latestEnded := ""
	reason := ""
	for _, session := range sessions {
		if session.endedAt == "" {
			if sessionExpiredAt(session.state, session.heartbeat, now) {
				// An open session past its lease derives as interrupted even
				// before the sweeper finalizes it (B1/H2): it competes with
				// ended sessions by its last heartbeat time.
				if session.heartbeat > latestEnded {
					latestEnded, reason = session.heartbeat, "expired"
				}
				continue
			}
			rank := 0
			switch session.state {
			case "playing":
				rank = 3
			case "buffering":
				rank = 2
			case "paused":
				rank = 1
			}
			if rank > bestRank || rank == bestRank && session.heartbeat > bestHeartbeat {
				best, bestRank, bestHeartbeat = session.state, rank, session.heartbeat
			}
			continue
		}
		if session.endedAt > latestEnded {
			latestEnded, reason = session.endedAt, session.endReason
		}
	}
	if best != "" {
		return best
	}
	switch reason {
	case "expired":
		return "interrupted"
	case "completed":
		return "completed"
	case "skipped":
		return "skipped"
	case "error":
		return "error"
	case "stopped", "replaced", "client_closed":
		return "stopped"
	}
	// No sessions at all (pre-migration history row) or no ended session.
	return "stopped"
}

// ClearPlaybackHistory wipes the aggregate and every session in one
// transaction (M6/D6). Clients that are still playing get a 404
// session_not_found on their next event and open a fresh session; a late
// start replayed with an old session id simply creates a new, unlinked
// session (no resume chain, no inherited counted flag).
func (s *Store) ClearPlaybackHistory(ctx context.Context) error {
	return withBusyRetry(ctx, func() error {
		tx, err := s.db.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `DELETE FROM playback_progress`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM playback_sessions`); err != nil {
			return err
		}
		return tx.Commit()
	})
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
	if len(trackIDs) > 0 {
		if err = bumpPlaylist(ctx, tx, id); err != nil {
			return Playlist{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Playlist{}, err
	}
	return s.PlaylistByID(ctx, id)
}
