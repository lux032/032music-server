package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSimilarityGraphFactorsAndSeedDedupe(t *testing.T) {
	s := similarityFixture(t)
	for i, title := range []string{"Song", "Song (Remastered)", "other", "third"} {
		addSimilarityTrack(t, s, i+1, title, "regular", true)
	}
	ctx := context.Background()
	for _, q := range []string{`INSERT INTO track_artists(track_id,artist_id,role) VALUES(1,1,'composer'),(3,1,'composer')`, `INSERT INTO genres(id,name) VALUES(1,'A'),(2,'B')`, `INSERT INTO track_genres(track_id,genre_id) VALUES(1,1),(3,1)`, `INSERT INTO track_genre_overrides(track_id,genre_id) VALUES(3,2)`, `INSERT INTO works(id,title,normalized_title,type) VALUES(1,'Work','work','anime')`, `INSERT INTO work_tracks(work_id,track_id,role,source) VALUES(1,1,'op','manual'),(1,3,'op','manual')`, `INSERT INTO playlists(id,name) VALUES(1,'Playlist'),(2,'Playlist 2')`, `INSERT INTO playlist_items(playlist_id,track_id,position) VALUES(1,1,0),(1,3,1),(2,1,0),(2,3,1)`} {
		if _, e := s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	items, e := s.SimilarTracks(ctx, 1, 30)
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range items {
		if item.Track.ID == 2 {
			t.Fatal("seed version was recommended")
		}
		if item.Track.ID == 3 {
			if !strings.Contains(strings.Join(item.Reasons, ","), "sharedCredit") || !strings.Contains(strings.Join(item.Reasons, ","), "sameWork") || !strings.Contains(strings.Join(item.Reasons, ","), "coPlaylist") || strings.Contains(strings.Join(item.Reasons, ","), "genre") {
				t.Fatalf("reasons %v", item.Reasons)
			}
		}
	}
	path, complete, e := s.TrackPath(ctx, 1, 3, 3, time.Second)
	if e != nil || !complete || len(path) > 3 {
		t.Fatalf("path=%v complete=%v err=%v", path, complete, e)
	}
	// Destination shares only the intermediate's singer: it cannot be reached directly.
	if _, e = s.db.ExecContext(ctx, `INSERT INTO artists(id,display_name,sort_name,identity_key) VALUES(3,'Bridge','bridge','bridge')`); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{`DELETE FROM track_artists WHERE track_id=4`, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(4,3,'primary'),(3,3,'primary')`, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(3,3,'composer'),(4,3,'composer')`, `INSERT INTO work_tracks(work_id,track_id) VALUES(1,4)`} {
		if _, e = s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	// 1 and 4 share a work; 4 and 3 share primary singer. Remove the direct 1→3 link.
	for _, q := range []string{`DELETE FROM track_artists WHERE track_id=3 AND artist_id=1 AND role='primary'`, `DELETE FROM work_tracks WHERE track_id=3`, `DELETE FROM track_genre_overrides WHERE track_id=3`, `DELETE FROM track_genres WHERE track_id=3`, `DELETE FROM track_artists WHERE track_id=3 AND artist_id=1 AND role='composer'`, `DELETE FROM playlist_items WHERE track_id=3`} {
		if _, e = s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	path, complete, e = s.TrackPath(ctx, 1, 3, 3, time.Second)
	if e != nil || !complete || len(path) != 3 || path[0].ID != 1 || path[1].ID != 4 || path[2].ID != 3 {
		t.Fatalf("limit=3 path=%v complete=%v err=%v", path, complete, e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = s.SimilarTracks(cancelled, 1, 30); e == nil {
		t.Fatal("expected cancelled query")
	}
}
