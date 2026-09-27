package storage

import "context"

const unreferencedWorkLimit = 200

// UnreferencedProtectedWorks lists at most 200 works and reports the full count.
func (s *Store) UnreferencedProtectedWorks(ctx context.Context) ([]Work, int64, error) {
	const filter = ` FROM works w WHERE ` + protectedWorkSQL + ` AND NOT EXISTS(SELECT 1 FROM album_works aw WHERE aw.work_id=w.id) AND NOT EXISTS(SELECT 1 FROM work_tracks wt WHERE wt.work_id=w.id)`
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+filter).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.title,COALESCE(w.reading_title,''),COALESCE(w.translated_title,''),w.type,COALESCE(w.year,0),COALESCE(w.poster_url,''),COALESCE(w.external_id,''),w.created_at,w.updated_at`+filter+` ORDER BY w.id LIMIT ?`, unreferencedWorkLimit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	values := []Work{}
	for rows.Next() {
		var v Work
		if err = rows.Scan(&v.ID, &v.Title, &v.ReadingTitle, &v.TranslatedTitle, &v.Type, &v.Year, &v.PosterURL, &v.ExternalID, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, 0, err
		}
		values = append(values, v)
	}
	return values, total, rows.Err()
}
