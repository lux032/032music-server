package storage

import (
	"context"
	"database/sql"
	"strings"
)

type ArtistRef struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}
type TrackCredit struct {
	Role    string      `json:"role"`
	Artists []ArtistRef `json:"artists"`
}
type CreditFilter struct {
	Role     string
	ArtistID int64
}
type TrackFocus struct {
	Decades                                                  []int
	YearFrom, YearTo                                         int
	TrackTypes, ExcludeTypes, TieupRoles, WorkTypes, Formats []string
	Quality                                                  string
	CreditArtistID                                           int64
	Credits                                                  []CreditFilter
}

var CreditRoles = []string{"lyricist", "composer", "arranger", "producer"}

func IsCreditRole(role string) bool {
	for _, r := range CreditRoles {
		if r == role {
			return true
		}
	}
	return false
}

const losslessCodecSQL = "q.codec IN ('flac','alac')"
const lossyCodecSQL = "q.codec IN ('mp3','aac','opus','vorbis')"
const effectiveTrackTypeSQL = "COALESCE(NULLIF(t.user_track_type,''),NULLIF(t.track_type,''),'regular')"

func appendTrackFocus(clauses *[]string, args *[]any, f TrackFocus) {
	add := func(s string, a ...any) { *clauses = append(*clauses, s); *args = append(*args, a...) }
	in := func(expr string, values []string) string {
		if len(values) == 0 {
			return ""
		}
		for _, v := range values {
			*args = append(*args, v)
		}
		return expr + " (" + strings.TrimSuffix(strings.Repeat("?,", len(values)), ",") + ")"
	}
	if len(f.Decades) > 0 {
		parts := []string{}
		for _, d := range f.Decades {
			parts = append(parts, "COALESCE(a.user_release_year,a.release_year) BETWEEN ? AND ?")
			*args = append(*args, d, d+9)
		}
		*clauses = append(*clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if f.YearFrom > 0 {
		add("COALESCE(a.user_release_year,a.release_year)>=?", f.YearFrom)
	}
	if f.YearTo > 0 {
		add("COALESCE(a.user_release_year,a.release_year)<=?", f.YearTo)
	}
	for _, condition := range []struct {
		expr   string
		values []string
	}{{effectiveTrackTypeSQL + " IN", f.TrackTypes}, {effectiveTrackTypeSQL + " NOT IN", f.ExcludeTypes}} {
		if s := in(condition.expr, condition.values); s != "" {
			*clauses = append(*clauses, s)
		}
	}
	if len(f.TieupRoles)+len(f.WorkTypes) > 0 {
		s := "EXISTS(SELECT 1 FROM work_tracks wt JOIN works w ON w.id=wt.work_id WHERE wt.track_id=t.id"
		if x := in("wt.role IN", f.TieupRoles); x != "" {
			s += " AND " + x
		}
		if x := in("w.type IN", f.WorkTypes); x != "" {
			s += " AND " + x
		}
		*clauses = append(*clauses, s+")")
	}
	if f.Quality != "" || len(f.Formats) > 0 {
		s := "EXISTS(SELECT 1 FROM audio_files q WHERE q.track_id=t.id AND q.status='available'"
		switch f.Quality {
		case "lossless":
			s += " AND " + losslessCodecSQL
		case "hires":
			s += " AND " + losslessCodecSQL + " AND (q.bit_depth>16 OR q.sample_rate>44100)"
		case "lossy":
			s += " AND " + lossyCodecSQL
		}
		if x := in("q.codec IN", f.Formats); x != "" {
			s += " AND " + x
		}
		*clauses = append(*clauses, s+")")
	}
	if f.CreditArtistID > 0 {
		add(`t.id IN (SELECT track_id FROM track_artists WHERE role IN ('lyricist','composer','arranger','producer') AND artist_id IN (WITH RECURSIVE m(id) AS (SELECT ? UNION SELECT a.id FROM artists a JOIN m ON a.merged_into_artist_id=m.id) SELECT id FROM m))`, f.CreditArtistID)
	}
	for _, c := range f.Credits { // Resolve both the requested merge chain and all historical incoming IDs in SQL.
		roleSQL := "role=?"
		roleArgs := []any{c.Role, c.ArtistID}
		if c.Role == "any" {
			roleSQL = "role IN ('composer','lyricist','arranger','producer')"
			roleArgs = []any{c.ArtistID}
		}
		add(`t.id IN (SELECT track_id FROM track_artists WHERE `+roleSQL+` AND artist_id IN (WITH RECURSIVE up(id,next) AS (SELECT id,merged_into_artist_id FROM artists WHERE id=? UNION SELECT a.id,a.merged_into_artist_id FROM artists a JOIN up ON a.id=up.next), m(id) AS (SELECT id FROM up WHERE next IS NULL UNION SELECT a.id FROM artists a JOIN m ON a.merged_into_artist_id=m.id) SELECT id FROM m))`, roleArgs...)
	}
}
func (s *Store) hydrateTrackCredits(ctx context.Context, items []Track) error {
	for start := 0; start < len(items); start += 500 {
		end := start + 500
		if end > len(items) {
			end = len(items)
		}
		ids := []int64{}
		byID := map[int64]*Track{}
		for i := start; i < end; i++ {
			ids = append(ids, items[i].ID)
			items[i].Credits = nil
			byID[items[i].ID] = &items[i]
		}
		p, args := inClause(ids)
		rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE resolved(old,id,next) AS (SELECT id,id,merged_into_artist_id FROM artists WHERE id IN (SELECT DISTINCT artist_id FROM track_artists WHERE track_id IN (`+p+`) AND role IN ('lyricist','composer','arranger','producer')) UNION SELECT r.old,a.id,a.merged_into_artist_id FROM resolved r JOIN artists a ON a.id=r.next) SELECT ta.track_id,ta.role,a.id,COALESCE(a.user_display_name,a.display_name) FROM track_artists ta JOIN resolved r ON r.old=ta.artist_id AND r.next IS NULL JOIN artists a ON a.id=r.id WHERE ta.role IN ('lyricist','composer','arranger','producer') AND ta.track_id IN (`+p+`) ORDER BY ta.track_id,CASE ta.role WHEN 'lyricist' THEN 0 WHEN 'composer' THEN 1 WHEN 'arranger' THEN 2 ELSE 3 END,ta.position,a.id`, append(append([]any(nil), args...), args...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var role string
			var a ArtistRef
			if err = rows.Scan(&id, &role, &a.ID, &a.Name); err != nil {
				break
			}
			t := byID[id]
			if len(t.Credits) == 0 || t.Credits[len(t.Credits)-1].Role != role {
				t.Credits = append(t.Credits, TrackCredit{Role: role})
			}
			c := &t.Credits[len(t.Credits)-1]
			duplicate := false
			for _, old := range c.Artists {
				if old.ID == a.ID {
					duplicate = true
				}
			}
			if !duplicate {
				c.Artists = append(c.Artists, a)
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

type CreditRoleCount struct {
	Role  string `json:"role"`
	Count int64  `json:"count"`
}

func (s *Store) ArtistCreditRoles(ctx context.Context, id int64) ([]CreditRoleCount, error) {
	canonical, err := s.CanonicalArtistID(ctx, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE m(id) AS (SELECT ? UNION SELECT a.id FROM artists a JOIN m ON a.merged_into_artist_id=m.id) SELECT ta.role,COUNT(DISTINCT ta.track_id) FROM track_artists ta JOIN tracks t ON t.id=ta.track_id JOIN albums a ON a.id=t.album_id WHERE ta.artist_id IN (SELECT id FROM m) AND ta.role IN ('lyricist','composer','arranger','producer') GROUP BY ta.role ORDER BY CASE ta.role WHEN 'lyricist' THEN 0 WHEN 'composer' THEN 1 WHEN 'arranger' THEN 2 ELSE 3 END`, canonical)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CreditRoleCount{}
	for rows.Next() {
		var c CreditRoleCount
		if err := rows.Scan(&c.Role, &c.Count); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (s *Store) canonicalTrackFocus(ctx context.Context, f Filters) (Filters, error) {
	f.Focus.Credits = append([]CreditFilter(nil), f.Focus.Credits...)
	for i := range f.Focus.Credits {
		id, err := s.CanonicalArtistID(ctx, f.Focus.Credits[i].ArtistID)
		if err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return f, err
		}
		f.Focus.Credits[i].ArtistID = id
	}
	return f, nil
}
