package storage

import (
	"context"
	"fmt"
	"strings"
)

// WorksByIDs and ArtistsByIDs fetch only the pending review entities, including
// those beyond the ordinary paginated library views.
func (s *Store) WorksByIDs(ctx context.Context, ids []int64) ([]Work, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		marks[i] = "?"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id,title,COALESCE(reading_title,''),COALESCE(translated_title,''),type,type_locked,origin,COALESCE(year,0),COALESCE(poster_url,''),COALESCE(external_id,''),(SELECT COUNT(*) FROM (SELECT track_id FROM work_tracks WHERE work_id=works.id UNION SELECT t.id FROM tracks t JOIN album_works aw ON aw.album_id=t.album_id WHERE aw.work_id=works.id)),created_at,updated_at FROM works WHERE id IN (%s) ORDER BY id`, strings.Join(marks, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Work
	for rows.Next() {
		var v Work
		var typeLocked int
		if err = rows.Scan(&v.ID, &v.Title, &v.ReadingTitle, &v.TranslatedTitle, &v.Type, &typeLocked, &v.Origin, &v.Year, &v.PosterURL, &v.ExternalID, &v.TrackCount, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		v.TypeLocked = typeLocked != 0
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) ArtistsByIDs(ctx context.Context, ids []int64) ([]Artist, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	marks := make([]string, len(ids))
	for i, id := range ids {
		args[i] = id
		marks[i] = "?"
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id,COALESCE(user_display_name,display_name) FROM artists WHERE id IN (%s) AND merged_into_artist_id IS NULL ORDER BY id`, strings.Join(marks, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Artist
	for rows.Next() {
		var v Artist
		if err = rows.Scan(&v.ID, &v.Name); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}
