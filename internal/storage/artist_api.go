package storage

import (
	"context"
	"database/sql"
)

// CanonicalArtistID follows merge links; the original ID remains available to callers.
func (s *Store) CanonicalArtistID(ctx context.Context, id int64) (int64, error) {
	return canonicalArtistID(ctx, s.db, id)
}

func (s *Store) SetArtistFavorite(ctx context.Context, id int64, favorite bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	canonical, err := canonicalArtistID(ctx, tx, id)
	if err != nil {
		return err
	}
	var result sql.Result
	if favorite {
		result, err = tx.ExecContext(ctx, `UPDATE artists SET is_favorite=1,favorited_at=CASE WHEN is_favorite=1 THEN favorited_at ELSE strftime('%Y-%m-%dT%H:%M:%fZ','now') END WHERE id=?`, canonical)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE artists SET is_favorite=0,favorited_at=NULL WHERE id=?`, canonical)
	}
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
	if _, err = tx.ExecContext(ctx, `UPDATE artist_merge_operations SET favorite_set_by_merge=0 WHERE target_artist_id=? AND status='merged'`, canonical); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FavoriteArtists(ctx context.Context, limit, offset int) ([]Artist, int64, error) {
	f := Filters{Favorite: true, Limit: limit, Offset: offset, favoriteOrder: true}
	total, err := s.CountArtists(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	items, err := s.ListArtists(ctx, f)
	return items, total, err
}

// ArtistAlbums returns albums where this artist has the album-artist role.
func (s *Store) ArtistAlbums(ctx context.Context, id int64) ([]Album, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT album_id FROM album_artists WHERE artist_id=? ORDER BY album_id`, id)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var albumID int64
		if err = rows.Scan(&albumID); err != nil {
			break
		}
		ids = append(ids, albumID)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	albums, err := s.albumsByIDs(ctx, ids)
	if albums == nil {
		albums = []Album{}
	}
	return albums, err
}

// Indexed role and album relations are unioned before loading the capped detail rows.
const artistTrackIDs = `SELECT track_id FROM track_artists WHERE role='primary' AND artist_id=? UNION SELECT t.id FROM album_artists aa JOIN tracks t ON t.album_id=aa.album_id WHERE aa.artist_id=?`

var artistTrackLimit = 5000

// Explicit projection for detail tracks, including the user-overridden credit fields.
const artistTrackSelect = `SELECT
 t.id,a.id,COALESCE(t.user_title,t.title),COALESCE(a.user_title,a.title),
 COALESCE((SELECT GROUP_CONCAT(name, ', ') FROM (SELECT COALESCE(ar.user_display_name,ar.display_name) name FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.track_id=t.id AND ta.role='primary' ORDER BY ta.position,ar.id)),
 (SELECT GROUP_CONCAT(name, ', ') FROM (SELECT COALESCE(ar.user_display_name,ar.display_name) name FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id ORDER BY aa.position,ar.id)),'Unknown Artist'),
 COALESCE(a.user_release_year,a.release_year,0),t.disc_number,t.track_number,COALESCE(t.user_composer,t.composer,''),COALESCE(t.lyricist,''),COALESCE(t.arranger,''),COALESCE(NULLIF(t.user_track_type,''),NULLIF(t.track_type,''),'regular'),
 COALESCE((SELECT GROUP_CONCAT(gx.name,',') FROM track_genre_overrides ox JOIN genres gx ON gx.id=ox.genre_id WHERE ox.track_id=t.id ORDER BY ox.position),(SELECT GROUP_CONCAT(gx.name,',') FROM track_genres rx JOIN genres gx ON gx.id=rx.genre_id WHERE rx.track_id=t.id ORDER BY rx.position),''),
 COALESCE((SELECT af.container FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
 COALESCE((SELECT af.mime_type FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
 COALESCE((SELECT af.relative_path FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),''),
 COALESCE((SELECT af.file_size FROM audio_files af WHERE af.track_id=t.id AND af.status='available' ORDER BY af.id LIMIT 1),0),
 COALESCE((SELECT '/api/v1/artwork/'||aw.id FROM artworks aw WHERE aw.album_id=a.id ORDER BY aw.is_primary DESC,aw.id LIMIT 1),''),
 COALESCE(t.duration_ms,0),'/api/v1/tracks/'||t.id||'/stream',t.added_at,t.updated_at,t.is_favorite,
 COALESCE(pp.last_played_at,''),COALESCE(pp.position_ms,0),COALESCE(pp.play_count,0)
 FROM (` + artistTrackIDs + `) matches JOIN tracks t ON t.id=matches.track_id JOIN albums a ON a.id=t.album_id LEFT JOIN playback_progress pp ON pp.track_id=t.id`

func (s *Store) ArtistTracks(ctx context.Context, id int64) ([]Track, int64, error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+artistTrackIDs+`)`, id, id).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, artistTrackSelect+` ORDER BY COALESCE(a.user_release_year,a.release_year,0),COALESCE(a.user_title,a.title) COLLATE NOCASE,a.id,t.disc_number,t.track_number,t.id LIMIT ?`, id, id, artistTrackLimit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	tracks := make([]Track, 0)
	for rows.Next() {
		track, err := scanArtistTrack(rows)
		if err != nil {
			return nil, 0, err
		}
		tracks = append(tracks, track)
	}
	return tracks, total, rows.Err()
}

func scanArtistTrack(row interface{ Scan(...any) error }) (Track, error) {
	var v Track
	var favorite int
	err := row.Scan(&v.ID, &v.AlbumID, &v.Title, &v.Album, &v.Artist, &v.Year, &v.DiscNumber, &v.TrackNumber, &v.Composer, &v.Lyricist, &v.Arranger, &v.TrackType, &v.Genres, &v.Container, &v.MIMEType, &v.RelativePath, &v.FileSize, &v.ArtworkURL, &v.DurationMillis, &v.StreamURL, &v.AddedAt, &v.UpdatedAt, &favorite, &v.LastPlayedAt, &v.PositionMillis, &v.PlayCount)
	v.IsFavorite = favorite != 0
	return v, err
}
