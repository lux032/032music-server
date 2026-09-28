package storage

import "context"

// AlbumWorkView includes one album-level work or an otherwise unlisted
// track-level work; a track-level row includes its track title.
type AlbumWorkView struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Type       string `json:"type"`
	Role       string `json:"role"`
	Source     string `json:"source"`
	TrackTitle string `json:"trackTitle,omitempty"`
	TrackID    int64  `json:"trackId,omitempty"`
}

func (s *Store) AlbumEligibleForBangumiSearch(ctx context.Context, albumID int64) (bool, error) {
	var eligible bool
	err := s.db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM album_works WHERE album_id=a.id AND source IN ('manual','bangumi')) AND COALESCE(a.user_is_compilation,a.is_compilation,0)=0 AND NOT EXISTS(SELECT 1 FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id AND LOWER(COALESCE(ar.user_display_name,ar.display_name))='various artists') AND NOT EXISTS(SELECT 1 FROM tracks t JOIN audio_files af ON af.track_id=t.id JOIN audio_file_tags tag ON tag.audio_file_id=af.id WHERE t.album_id=a.id AND UPPER(tag.field_name) IN ('COMPILATION','TCMP') AND tag.value='1') AND NOT EXISTS(SELECT 1 FROM album_work_suppressions WHERE album_id=a.id AND inferred_key LIKE 'bangumi:%') AND NOT EXISTS(SELECT 1 FROM album_subject_candidates WHERE album_id=a.id AND status IN ('confirmed','rejected')) FROM albums a WHERE a.id=?`, albumID).Scan(&eligible)
	return eligible, err
}

// AlbumBangumiBlockReason distinguishes a user unlink from a rejected lookup.
// Existing authoritative links are never reported as blocked.
func (s *Store) AlbumBangumiBlockReason(ctx context.Context, albumID int64) (string, error) {
	var suppressed, rejected bool
	err := s.db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM album_works WHERE album_id=? AND source IN ('manual','bangumi')) AND EXISTS(SELECT 1 FROM album_work_suppressions s WHERE s.album_id=? AND s.inferred_key LIKE 'bangumi:%' AND NOT EXISTS(SELECT 1 FROM album_subject_candidates c WHERE c.album_id=s.album_id AND c.status='rejected' AND s.inferred_key='bangumi:'||c.external_id)),NOT EXISTS(SELECT 1 FROM album_works WHERE album_id=? AND source IN ('manual','bangumi')) AND EXISTS(SELECT 1 FROM album_subject_candidates WHERE album_id=? AND status='rejected')`, albumID, albumID, albumID, albumID).Scan(&suppressed, &rejected)
	if err != nil {
		return "", err
	}
	if suppressed {
		return "unlinked", nil
	}
	if rejected {
		return "rejected", nil
	}
	return "", nil
}

func (s *Store) WorksForAlbum(ctx context.Context, albumID int64) ([]AlbumWorkView, error) {
	rows, err := s.db.QueryContext(ctx, `WITH ranked_tracks AS (SELECT w.id,w.title,w.type,wt.role,wt.source,COALESCE(t.user_title,t.title) track_title,t.id track_id,ROW_NUMBER() OVER (PARTITION BY wt.work_id,wt.track_id ORDER BY CASE wt.source WHEN 'manual' THEN 0 WHEN 'bangumi' THEN 1 ELSE 2 END,wt.role) rank FROM tracks t JOIN work_tracks wt ON wt.track_id=t.id JOIN works w ON w.id=wt.work_id WHERE t.album_id=?), preferred_roles AS (SELECT work_id,role FROM (SELECT wt.work_id,wt.role,ROW_NUMBER() OVER (PARTITION BY wt.work_id ORDER BY CASE wt.source WHEN 'manual' THEN 0 WHEN 'bangumi' THEN 1 ELSE 2 END,wt.track_id,wt.role) rank FROM tracks t JOIN work_tracks wt ON wt.track_id=t.id WHERE t.album_id=?) WHERE rank=1) SELECT w.id,w.title,w.type,COALESCE(pr.role,aw.role),aw.source,'',0 FROM album_works aw JOIN works w ON w.id=aw.work_id LEFT JOIN preferred_roles pr ON pr.work_id=aw.work_id WHERE aw.album_id=? UNION ALL SELECT id,title,type,role,source,track_title,track_id FROM ranked_tracks rt WHERE rank=1 AND NOT EXISTS(SELECT 1 FROM album_works aw WHERE aw.album_id=? AND aw.work_id=rt.id) ORDER BY 7,1`, albumID, albumID, albumID, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlbumWorkView{}
	seen := map[int64]bool{}
	for rows.Next() {
		var v AlbumWorkView
		if err = rows.Scan(&v.ID, &v.Title, &v.Type, &v.Role, &v.Source, &v.TrackTitle, &v.TrackID); err != nil {
			return nil, err
		}
		if v.TrackID != 0 && seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		out = append(out, v)
	}
	return out, rows.Err()
}
