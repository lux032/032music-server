package storage

import (
	"context"
	"database/sql"
)

// TrackWork is one work a track belongs to: the inverse of TracksForWork, so
// a track is listed under a work exactly when the work's track list contains
// it (a direct work_tracks link or an album-level album_works link).
type TrackWork struct {
	ID              int64  `json:"id"`
	Title           string `json:"title"`
	TranslatedTitle string `json:"translatedTitle"`
	Type            string `json:"type"`
	// Role is the best-ranked usage, same rule as TracksForWork: a direct
	// link (manual, then bangumi, then auto) wins over the album link.
	Role   string `json:"role"`
	Season int    `json:"season"`
	// SeriesID is the series the work belongs to; 0 (omitted) when none.
	SeriesID int64 `json:"seriesId,omitempty"`
}

// WorksForTrack lists the works of one track, ordered by work title. A
// missing track is sql.ErrNoRows; a track without works is an empty list.
func (s *Store) WorksForTrack(ctx context.Context, trackID int64) ([]TrackWork, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracks WHERE id=?)`, trackID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, sql.ErrNoRows
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.title,COALESCE(w.translated_title,''),w.type,l.role,l.season,COALESCE((SELECT m.series_id FROM work_series_members m WHERE m.work_id=w.id),0)
FROM (
  SELECT work_id,role,season,ROW_NUMBER() OVER (PARTITION BY work_id ORDER BY rank,role,season) rn FROM (
    SELECT work_id,role,season,CASE source WHEN 'manual' THEN 0 WHEN 'bangumi' THEN 1 ELSE 2 END rank FROM work_tracks WHERE track_id=?
    UNION ALL
    SELECT aw.work_id,aw.role,aw.season,3 FROM album_works aw JOIN tracks t ON t.album_id=aw.album_id WHERE t.id=?
  )
) l JOIN works w ON w.id=l.work_id
WHERE l.rn=1
ORDER BY w.title COLLATE NOCASE,w.id`, trackID, trackID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]TrackWork, 0)
	for rows.Next() {
		var v TrackWork
		if err = rows.Scan(&v.ID, &v.Title, &v.TranslatedTitle, &v.Type, &v.Role, &v.Season, &v.SeriesID); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}
