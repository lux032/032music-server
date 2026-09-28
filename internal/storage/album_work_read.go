package storage

import (
	"context"
	"sort"
	"strings"
)

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

type AlbumLevelWorkView struct {
	Work   Work   `json:"work"`
	Role   string `json:"role"`
	Source string `json:"source"`
	Season int    `json:"season"`
}

type AlbumTrackWorkView struct {
	TrackID     int64  `json:"trackId"`
	TrackTitle  string `json:"trackTitle"`
	DiscNumber  int    `json:"discNumber"`
	TrackNumber int    `json:"trackNumber"`
	Work        Work   `json:"work"`
	Role        string `json:"role"`
	Source      string `json:"source"`
	Season      int    `json:"season"`
	Sequence    int    `json:"sequence"`
	// Roles 是这首曲目对这部作品的全部用途（D49），按 WorkRoleRank 排序去重；
	// Role/Source/Season/Sequence 仍来自 manual > bangumi > auto 的代表行。
	Roles []string `json:"roles"`
}

func (s *Store) AlbumLevelWorks(ctx context.Context, albumID int64) ([]AlbumLevelWorkView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.id,w.title,COALESCE(w.reading_title,''),COALESCE(w.translated_title,''),w.type,COALESCE(w.year,0),COALESCE(w.poster_url,''),COALESCE(w.external_id,''),w.updated_at,aw.role,aw.source,aw.season FROM album_works aw JOIN works w ON w.id=aw.work_id WHERE aw.album_id=? ORDER BY CASE aw.source WHEN 'manual' THEN 0 WHEN 'bangumi' THEN 1 ELSE 2 END,w.id`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlbumLevelWorkView{}
	for rows.Next() {
		var v AlbumLevelWorkView
		if err = rows.Scan(&v.Work.ID, &v.Work.Title, &v.Work.ReadingTitle, &v.Work.TranslatedTitle, &v.Work.Type, &v.Work.Year, &v.Work.PosterURL, &v.Work.ExternalID, &v.Work.UpdatedAt, &v.Role, &v.Source, &v.Season); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// TrackWorksForAlbum fetches all track-level work associations for an album in a single query.
// D49：同一（曲目，作品）的全部用途经 agg 子查询一并带出；展示来源仍取代表行。
func (s *Store) TrackWorksForAlbum(ctx context.Context, albumID int64) ([]AlbumTrackWorkView, error) {
	rows, err := s.db.QueryContext(ctx, `WITH ranked AS (SELECT wt.track_id,COALESCE(t.user_title,t.title) AS track_title,COALESCE(t.user_disc_number,t.disc_number,1) AS disc_num,COALESCE(t.user_track_number,t.track_number,0) AS track_num,w.id AS work_id,w.title,COALESCE(w.reading_title,'') AS reading_title,COALESCE(w.translated_title,'') AS translated_title,w.type,COALESCE(w.year,0) AS year,COALESCE(w.poster_url,'') AS poster_url,COALESCE(w.external_id,'') AS external_id,w.updated_at,wt.role,wt.source,wt.season,wt.sequence,ROW_NUMBER() OVER (PARTITION BY wt.track_id,wt.work_id ORDER BY CASE wt.source WHEN 'manual' THEN 0 WHEN 'bangumi' THEN 1 ELSE 2 END,CASE wt.role WHEN 'op' THEN 0 WHEN 'ed' THEN 1 WHEN 'insert' THEN 2 WHEN 'theme' THEN 3 WHEN 'character' THEN 4 WHEN 'image_song' THEN 5 WHEN 'ost' THEN 6 ELSE 7 END,wt.role) AS rn FROM tracks t JOIN work_tracks wt ON wt.track_id=t.id JOIN works w ON w.id=wt.work_id WHERE t.album_id=?), agg AS (SELECT wt.track_id,wt.work_id,GROUP_CONCAT(DISTINCT wt.role) AS roles FROM tracks t JOIN work_tracks wt ON wt.track_id=t.id WHERE t.album_id=? GROUP BY wt.track_id,wt.work_id) SELECT ranked.track_id,ranked.track_title,ranked.disc_num,ranked.track_num,ranked.work_id,ranked.title,ranked.reading_title,ranked.translated_title,ranked.type,ranked.year,ranked.poster_url,ranked.external_id,ranked.updated_at,ranked.role,ranked.source,ranked.season,ranked.sequence,COALESCE(agg.roles,'') FROM ranked JOIN agg ON agg.track_id=ranked.track_id AND agg.work_id=ranked.work_id WHERE ranked.rn=1 ORDER BY ranked.disc_num,ranked.track_num,ranked.track_id,ranked.work_id`, albumID, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlbumTrackWorkView{}
	for rows.Next() {
		var v AlbumTrackWorkView
		var rolesStr string
		if err = rows.Scan(&v.TrackID, &v.TrackTitle, &v.DiscNumber, &v.TrackNumber, &v.Work.ID, &v.Work.Title, &v.Work.ReadingTitle, &v.Work.TranslatedTitle, &v.Work.Type, &v.Work.Year, &v.Work.PosterURL, &v.Work.ExternalID, &v.Work.UpdatedAt, &v.Role, &v.Source, &v.Season, &v.Sequence, &rolesStr); err != nil {
			return nil, err
		}
		// GROUP_CONCAT 不保证顺序：按 WorkRoleRank 排序（与 AlbumsForWork 一致）。
		for _, r := range strings.Split(rolesStr, ",") {
			if r = strings.TrimSpace(r); r != "" {
				v.Roles = append(v.Roles, r)
			}
		}
		sort.SliceStable(v.Roles, func(i, j int) bool { return WorkRoleRank(v.Roles[i]) < WorkRoleRank(v.Roles[j]) })
		out = append(out, v)
	}
	return out, rows.Err()
}
