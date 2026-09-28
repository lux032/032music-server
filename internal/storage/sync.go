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
	TrackCount   int64  `json:"trackCount"`
	AlbumType    string `json:"albumType"`
	Compilation  bool   `json:"compilation"`
	Live         bool   `json:"live"`
	Formats      string `json:"formats"`
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
	DiscNumber     int    `json:"discNumber,omitempty"`
	TrackNumber    int    `json:"trackNumber,omitempty"`
	TrackExtras
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
		`+albumArtworkURLSQL+`,
		a.added_at,
		a.updated_at,
		COALESCE((SELECT MAX(pp.last_played_at) FROM tracks t JOIN playback_progress pp ON pp.track_id=t.id WHERE t.album_id=a.id), ''),
		a.is_favorite,
 (SELECT COUNT(*) FROM tracks t WHERE t.album_id=a.id),COALESCE(a.user_album_type,a.album_type,'album'),COALESCE(a.user_is_compilation,a.is_compilation,0),COALESCE(a.user_is_live,a.is_live,0),COALESCE((SELECT GROUP_CONCAT(DISTINCT UPPER(af.container)) FROM tracks t JOIN audio_files af ON af.track_id=t.id AND af.status='available' WHERE t.album_id=a.id),'')
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
		var favorite, compilation, live int
		if err := rows.Scan(&item.ID, &item.Title, &item.Artist, &item.Year, &item.ArtworkURL, &item.AddedAt, &item.UpdatedAt, &item.LastPlayedAt, &favorite, &item.TrackCount, &item.AlbumType, &compilation, &live, &item.Formats); err != nil {
			return SyncAlbumsResult{}, err
		}
		item.IsFavorite = favorite != 0
		item.Compilation = compilation != 0
		item.Live = live != 0
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
        ` + trackArtistSQL + `,
		COALESCE(t.duration_ms, 0),
		` + albumArtworkURLSQL + `,
		'/api/v1/tracks/' || t.id || '/stream',
		t.is_favorite
	FROM tracks t
	JOIN albums a ON a.id = t.album_id
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

	rows.Close()
	if err := hydrateTrackExtras(ctx, s, func() []*SyncTrack {
		ptrs := make([]*SyncTrack, len(items))
		for i := range items {
			ptrs[i] = &items[i]
		}
		return ptrs
	}()); err != nil {
		return SyncTracksResult{}, err
	}

	return SyncTracksResult{
		Items:      items,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Total:      total,
	}, nil
}
