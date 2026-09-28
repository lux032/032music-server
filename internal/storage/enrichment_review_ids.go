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

func (s *Store) PendingWorkReviewCounts(ctx context.Context) (albumCount, trackCount, workCount int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_subject_candidates WHERE status='candidate'`).Scan(&albumCount); err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_subject_candidates WHERE status='candidate'`).Scan(&trackCount); err != nil {
		return 0, 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT work_id) FROM work_match_candidates WHERE status='candidate'`).Scan(&workCount); err != nil {
		return 0, 0, 0, err
	}
	return albumCount, trackCount, workCount, nil
}

func (s *Store) PendingWorkReviewTotal(ctx context.Context) (int, error) {
	albumCount, trackCount, workCount, err := s.PendingWorkReviewCounts(ctx)
	return albumCount + trackCount + workCount, err
}

func (s *Store) PendingAlbumReviewCounts(ctx context.Context, albumID int64) (albumCandidates, trackCandidates int, err error) {
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_subject_candidates WHERE album_id=? AND status='candidate'`, albumID).Scan(&albumCandidates); err != nil {
		return 0, 0, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_subject_candidates c JOIN tracks t ON t.id=c.track_id WHERE t.album_id=? AND c.status='candidate'`, albumID).Scan(&trackCandidates); err != nil {
		return 0, 0, err
	}
	return albumCandidates, trackCandidates, nil
}
