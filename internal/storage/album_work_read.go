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

func (s *Store) WorksForAlbum(ctx context.Context, albumID int64) ([]AlbumWorkView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.title,w.type,aw.role,aw.source,'',0 FROM album_works aw JOIN works w ON w.id=aw.work_id WHERE aw.album_id=? UNION ALL SELECT w.id,w.title,w.type,wt.role,wt.source,COALESCE(t.user_title,t.title),t.id FROM tracks t JOIN work_tracks wt ON wt.track_id=t.id JOIN works w ON w.id=wt.work_id WHERE t.album_id=? AND NOT EXISTS(SELECT 1 FROM album_works aw WHERE aw.album_id=t.album_id AND aw.work_id=wt.work_id) ORDER BY 7,1`, albumID, albumID)
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
