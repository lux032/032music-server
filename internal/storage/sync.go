package storage

import (
	"context"
	"strconv"
)

type SyncAlbum struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Artist       string `json:"artist"`
	Year         int    `json:"year"`
	ArtworkURL   string `json:"artworkUrl"`
	AddedAt      string `json:"addedAt"`
	UpdatedAt    string `json:"updatedAt"`
	LastPlayedAt string `json:"lastPlayedAt,omitempty"`
	IsFavorite   bool   `json:"isFavorite"`
}

type SyncAlbumsParams struct {
	Cursor int64
	Limit  int
}

type SyncAlbumsResult struct {
	Items      []SyncAlbum `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
	HasMore    bool        `json:"hasMore"`
	Total      int64       `json:"total"`
}

type SyncTrack struct {
	ID             int64  `json:"id"`
	AlbumID        int64  `json:"albumId"`
	Title          string `json:"title"`
	Album          string `json:"album"`
	Artist         string `json:"artist"`
	DurationMillis int64  `json:"durationMillis"`
	ArtworkURL     string `json:"artworkUrl"`
	StreamURL      string `json:"streamUrl"`
	IsFavorite     bool   `json:"isFavorite"`
}

type SyncTracksParams struct {
	Cursor  int64
	Limit   int
	AlbumID int64
}

type SyncTracksResult struct {
	Items      []SyncTrack `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
	HasMore    bool        `json:"hasMore"`
	Total      int64       `json:"total"`
}

func (s *Store) SyncAlbums(ctx context.Context, params SyncAlbumsParams) (SyncAlbumsResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM albums`).Scan(&total); err != nil {
		return SyncAlbumsResult{}, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT
		a.id,
		COALESCE(a.user_title, a.title),
		COALESCE(a.user_performed_by, a.performed_by,
			(SELECT GROUP_CONCAT(COALESCE(ar.user_display_name, ar.display_name), ', ')
			 FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id),
			'Unknown Artist'),
		COALESCE(a.user_release_year, a.release_year, 0),
		COALESCE((SELECT '/api/v1/artwork/' || aw.id FROM artworks aw WHERE aw.album_id=a.id ORDER BY aw.is_primary DESC, aw.id LIMIT 1), ''),
		a.added_at,
		a.updated_at,
		COALESCE((SELECT MAX(pp.last_played_at) FROM tracks t JOIN playback_progress pp ON pp.track_id=t.id WHERE t.album_id=a.id), ''),
		a.is_favorite
	FROM albums a
	WHERE a.id > ?
	ORDER BY a.id ASC
	LIMIT ?`, params.Cursor, limit+1)
	if err != nil {
		return SyncAlbumsResult{}, err
	}
	defer rows.Close()

	items := make([]SyncAlbum, 0, limit)
	for rows.Next() {
		var item SyncAlbum
		var favorite int
		if err := rows.Scan(&item.ID, &item.Title, &item.Artist, &item.Year, &item.ArtworkURL, &item.AddedAt, &item.UpdatedAt, &item.LastPlayedAt, &favorite); err != nil {
			return SyncAlbumsResult{}, err
		}
		item.IsFavorite = favorite != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SyncAlbumsResult{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor string
	if hasMore && len(items) > 0 {
		nextCursor = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	return SyncAlbumsResult{Items: items, NextCursor: nextCursor, HasMore: hasMore, Total: total}, nil
}

func (s *Store) SyncTracks(ctx context.Context, params SyncTracksParams) (SyncTracksResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracks WHERE (? = 0 OR album_id = ?)`, params.AlbumID, params.AlbumID).Scan(&total); err != nil {
		return SyncTracksResult{}, err
	}

	fetchLimit := limit + 1
	query := `SELECT
		t.id,
		t.album_id,
		COALESCE(t.user_title, t.title),
		COALESCE(a.user_title, a.title),
		COALESCE(
			(SELECT GROUP_CONCAT(COALESCE(ar.user_display_name, ar.display_name), ', ')
			 FROM track_artists ta
			 JOIN artists ar ON ar.id = ta.artist_id
			 WHERE ta.track_id = t.id),
			a.user_performed_by,
			a.performed_by,
			(SELECT GROUP_CONCAT(COALESCE(ar.user_display_name, ar.display_name), ', ')
			 FROM album_artists aa
			 JOIN artists ar ON ar.id = aa.artist_id
			 WHERE aa.album_id = a.id),
			'Unknown Artist'
		),
		COALESCE(t.duration_ms, 0),
		CASE WHEN aw.id IS NULL THEN '' ELSE '/api/v1/artwork/' || aw.id END,
		'/api/v1/tracks/' || t.id || '/stream',
		t.is_favorite
	FROM tracks t
	JOIN albums a ON a.id = t.album_id
	LEFT JOIN artworks aw ON aw.album_id = a.id AND aw.is_primary = 1
	WHERE (? = 0 OR t.album_id = ?) AND t.id > ?
	ORDER BY t.id ASC
	LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, params.AlbumID, params.AlbumID, params.Cursor, fetchLimit)
	if err != nil {
		return SyncTracksResult{}, err
	}
	defer rows.Close()

	items := make([]SyncTrack, 0, limit)
	for rows.Next() {
		var item SyncTrack
		var isFavorite int
		if err := rows.Scan(
			&item.ID,
			&item.AlbumID,
			&item.Title,
			&item.Album,
			&item.Artist,
			&item.DurationMillis,
			&item.ArtworkURL,
			&item.StreamURL,
			&isFavorite,
		); err != nil {
			return SyncTracksResult{}, err
		}
		item.IsFavorite = isFavorite != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SyncTracksResult{}, err
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var nextCursor string
	if hasMore && len(items) > 0 {
		nextCursor = strconv.FormatInt(items[len(items)-1].ID, 10)
	}

	return SyncTracksResult{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Total:      total,
	}, nil
}
