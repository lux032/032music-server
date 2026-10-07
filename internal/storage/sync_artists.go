package storage

import (
	"context"
	"strconv"
)

// SyncArtist is one performer row of the client sync model. Only canonical
// (not merged) artists that perform something are listed: album artists and
// primary track artists. Pure credit people (composer/lyricist/...) stay on the
// web "幕后人员" view and are not synced.
//
// AlbumCount follows ArtistDiscography: albums credited to the artist plus
// albums where the artist sings at least one track. TrackCount follows the
// performer track set used by the artist detail endpoint (primary track
// credits united with tracks of the artist's own albums).
type SyncArtist struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	SortName   string `json:"sortName,omitempty"`
	ImageURL   string `json:"imageUrl"`
	IsFavorite bool   `json:"isFavorite"`
	AlbumCount int64  `json:"albumCount"`
	TrackCount int64  `json:"trackCount"`
}

type SyncArtistsParams struct {
	Cursor int64
	Limit  int
}

type SyncArtistsResult struct {
	Items      []SyncArtist `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
	HasMore    bool         `json:"hasMore"`
	Total      int64        `json:"total"`
}

// syncArtistWhere 只下发仍有可见专辑或可见主唱曲目的演唱歌手（见 visibility.go）。
var syncArtistWhere = `ar.merged_into_artist_id IS NULL AND (
	EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=ar.id AND ` + albumVisibleSQL("aa.album_id") + `)
	OR EXISTS(SELECT 1 FROM track_artists ta WHERE ta.artist_id=ar.id AND ta.role='primary' AND ` + trackVisibleSQL("ta.track_id") + `))`

func (s *Store) SyncArtists(ctx context.Context, params SyncArtistsParams) (SyncArtistsResult, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists ar WHERE `+syncArtistWhere).Scan(&total); err != nil {
		return SyncArtistsResult{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		ar.id,
		COALESCE(ar.user_display_name,ar.display_name),
		COALESCE(NULLIF(ar.reading_name,''),NULLIF(ar.sort_name,''),''),
		`+artistImageURLSQL("ar")+`,
		ar.is_favorite,
		(SELECT COUNT(*) FROM (SELECT aa.album_id FROM album_artists aa WHERE aa.artist_id=ar.id UNION SELECT t.album_id FROM track_artists ta JOIN tracks t ON t.id=ta.track_id WHERE ta.artist_id=ar.id AND ta.role='primary') sa WHERE `+albumVisibleSQL("sa.album_id")+`),
		(SELECT COUNT(*) FROM (SELECT ta.track_id FROM track_artists ta WHERE ta.role='primary' AND ta.artist_id=ar.id UNION SELECT t.id FROM album_artists aa JOIN tracks t ON t.album_id=aa.album_id WHERE aa.artist_id=ar.id) st WHERE `+trackVisibleSQL("st.track_id")+`)
	FROM artists ar
	WHERE `+syncArtistWhere+` AND ar.id > ?
	ORDER BY ar.id ASC
	LIMIT ?`, params.Cursor, limit+1)
	if err != nil {
		return SyncArtistsResult{}, err
	}
	defer rows.Close()
	items := make([]SyncArtist, 0, limit)
	for rows.Next() {
		var item SyncArtist
		var favorite int
		if err := rows.Scan(&item.ID, &item.Name, &item.SortName, &item.ImageURL, &favorite, &item.AlbumCount, &item.TrackCount); err != nil {
			return SyncArtistsResult{}, err
		}
		item.IsFavorite = favorite != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return SyncArtistsResult{}, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor string
	if hasMore && len(items) > 0 {
		nextCursor = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	return SyncArtistsResult{Items: items, NextCursor: nextCursor, HasMore: hasMore, Total: total}, nil
}

// albumArtistRefs loads the credited album artists (credit order) for a batch
// of albums. Merges move relations onto the target, so rows are canonical;
// merged rows are still skipped defensively. CROSS JOIN pins the join order:
// otherwise SQLite drives the plan from idx_artists_merged_into and scans every
// artist (~0.6s per 1000 albums on a real library instead of a few ms).
func (s *Store) albumArtistRefs(ctx context.Context, albumIDs []int64) (map[int64][]ArtistRef, error) {
	result := make(map[int64][]ArtistRef, len(albumIDs))
	if len(albumIDs) == 0 {
		return result, nil
	}
	placeholders, args := inClause(albumIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT aa.album_id,ar.id,COALESCE(ar.user_display_name,ar.display_name) FROM album_artists aa CROSS JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id IN (`+placeholders+`) AND ar.merged_into_artist_id IS NULL ORDER BY aa.album_id,aa.position,ar.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var albumID int64
		var ref ArtistRef
		if err := rows.Scan(&albumID, &ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		result[albumID] = appendArtistRef(result[albumID], ref)
	}
	return result, rows.Err()
}

// trackArtistRefs loads the primary (performing) track artists for a batch of
// tracks in credit order. Tracks without primary credits are absent; clients
// fall back to the album artists, matching the server's own track artist text.
func (s *Store) trackArtistRefs(ctx context.Context, trackIDs []int64) (map[int64][]ArtistRef, error) {
	result := make(map[int64][]ArtistRef, len(trackIDs))
	if len(trackIDs) == 0 {
		return result, nil
	}
	placeholders, args := inClause(trackIDs)
	rows, err := s.db.QueryContext(ctx, `SELECT ta.track_id,ar.id,COALESCE(ar.user_display_name,ar.display_name) FROM track_artists ta CROSS JOIN artists ar ON ar.id=ta.artist_id WHERE ta.role='primary' AND ta.track_id IN (`+placeholders+`) AND ar.merged_into_artist_id IS NULL ORDER BY ta.track_id,ta.position,ar.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var trackID int64
		var ref ArtistRef
		if err := rows.Scan(&trackID, &ref.ID, &ref.Name); err != nil {
			return nil, err
		}
		result[trackID] = appendArtistRef(result[trackID], ref)
	}
	return result, rows.Err()
}

func appendArtistRef(list []ArtistRef, ref ArtistRef) []ArtistRef {
	for _, existing := range list {
		if existing.ID == ref.ID {
			return list
		}
	}
	return append(list, ref)
}

// AttachAlbumArtistRefs fills Album.Artists for API responses.
func (s *Store) AttachAlbumArtistRefs(ctx context.Context, albums []Album) error {
	ids := make([]int64, len(albums))
	for i := range albums {
		ids[i] = albums[i].ID
	}
	refs, err := s.albumArtistRefs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range albums {
		albums[i].Artists = nonNilRefs(refs[albums[i].ID])
	}
	return nil
}

func nonNilRefs(refs []ArtistRef) []ArtistRef {
	if refs == nil {
		return []ArtistRef{}
	}
	return refs
}
