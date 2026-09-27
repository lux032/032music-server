package storage

import "context"

func (s *Store) PendingWorkReviewIDs(ctx context.Context) ([]int64, int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT work_id) FROM work_match_candidates WHERE status='candidate'`).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT work_id FROM work_match_candidates WHERE status='candidate' ORDER BY work_id LIMIT 200`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	return ids, count, rows.Err()
}

func (s *Store) PendingArtistReviewIDs(ctx context.Context) ([]int64, int, error) {
	var count int
	clause := ` FROM artist_relation_candidates c JOIN artists a ON a.id=c.artist_id WHERE c.status='candidate' AND a.merged_into_artist_id IS NULL`
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT c.artist_id)`+clause).Scan(&count); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT c.artist_id`+clause+` ORDER BY c.artist_id LIMIT 200`)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	return ids, count, rows.Err()
}
