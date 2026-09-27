package storage

import (
	"context"
	"strings"
	"testing"
)

func TestSimilarityCandidateQueryPlans(t *testing.T) {
	s := similarityFixture(t)
	cases := []struct {
		q    string
		args []any
	}{
		{`SELECT id FROM artists WHERE id IN (?) OR merged_into_artist_id IN (?) ORDER BY id`, []any{1, 1}},
		{`SELECT DISTINCT ta.track_id FROM track_artists ta WHERE ta.role='primary' AND ta.artist_id IN (?) AND ta.track_id<>? ORDER BY ta.track_id LIMIT ?`, []any{1, 1, 200}},
		{`SELECT DISTINCT ta.track_id FROM track_artists ta WHERE ta.role IN ('composer','lyricist','arranger','producer') AND ta.artist_id IN (?) AND ta.track_id<>? ORDER BY ta.track_id LIMIT ?`, []any{1, 1, 200}},
		{`SELECT track_id FROM (SELECT track_id FROM work_tracks WHERE work_id=? UNION SELECT t.id FROM tracks t JOIN album_works aw ON aw.album_id=t.album_id WHERE aw.work_id=?) WHERE track_id<>? ORDER BY track_id LIMIT ?`, []any{1, 1, 1, 100}},
		{`SELECT track_id FROM playlist_items WHERE playlist_id=? AND track_id<>? ORDER BY position LIMIT ?`, []any{1, 1, 200}},
		{`SELECT track_id FROM track_genre_overrides WHERE genre_id=? AND track_id>=? AND track_id<>? ORDER BY track_id LIMIT ?`, []any{1, 1, 1, 300}},
		{`SELECT track_id FROM track_genres WHERE genre_id=? AND track_id>=? AND track_id<>? ORDER BY track_id LIMIT ?`, []any{1, 1, 1, 300}},
		{`SELECT track_id FROM track_genre_overrides WHERE genre_id=? AND track_id<? AND track_id<>? ORDER BY track_id LIMIT ?`, []any{1, 1, 1, 300}},
		{`SELECT track_id FROM track_genres WHERE genre_id=? AND track_id<? AND track_id<>? ORDER BY track_id LIMIT ?`, []any{1, 1, 1, 300}},
		{`SELECT t.id,t.album_id,COALESCE(t.user_title,t.title),COALESCE(NULLIF(t.user_track_type,''),NULLIF(t.track_type,''),'regular'),COALESCE(a.user_release_year,a.release_year,0) FROM tracks t JOIN albums a ON a.id=t.album_id WHERE t.id IN (?)`, []any{1}},
		{`SELECT ta.track_id,COALESCE(ar.merged_into_artist_id,ar.id) FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.role='primary' AND ta.track_id IN (?)`, []any{1}},
		{`SELECT ta.track_id,COALESCE(ar.merged_into_artist_id,ar.id) FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.role IN ('composer','lyricist','arranger','producer') AND ta.track_id IN (?)`, []any{1}},
		{`SELECT track_id,genre_id FROM track_genre_overrides WHERE track_id IN (?)`, []any{1}},
		{`SELECT g.track_id,g.genre_id FROM track_genres g WHERE g.track_id IN (?) AND NOT EXISTS(SELECT 1 FROM track_genre_overrides o WHERE o.track_id=g.track_id)`, []any{1}},
		{`SELECT track_id,work_id FROM work_tracks WHERE track_id IN (?) UNION SELECT t.id,aw.work_id FROM tracks t JOIN album_works aw ON aw.album_id=t.album_id WHERE t.id IN (?)`, []any{1, 1}},
		{`SELECT track_id,playlist_id FROM playlist_items WHERE track_id IN (?)`, []any{1}},
		{`SELECT track_id,1 FROM audio_files WHERE status='available' AND track_id IN (?)`, []any{1}},
	}
	for _, tc := range cases {
		rows, e := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+tc.q, tc.args...)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if e = rows.Scan(&id, &parent, &unused, &detail); e != nil {
				t.Fatal(e)
			}
			if strings.Contains(detail, "SCAN ") && !strings.Contains(detail, "SCAN (subquery-") && !strings.Contains(detail, "USING INDEX") && !strings.Contains(detail, "USING COVERING INDEX") && !strings.Contains(detail, "USING INTEGER PRIMARY KEY") && !strings.Contains(detail, "USING PRIMARY KEY") {
				t.Errorf("unindexed plan %q: %s", tc.q, detail)
			}
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
	}
}
