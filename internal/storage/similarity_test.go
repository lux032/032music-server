package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestScoreSimilarity(t *testing.T) {
	cases := []struct {
		name string
		f    similarityFactors
		want float64
	}{{"artist", similarityFactors{artist: 1}, .3}, {"credit", similarityFactors{credit: 1}, .2}, {"genre", similarityFactors{genre: 1}, .2}, {"era", similarityFactors{era: 1}, .1}, {"playlist", similarityFactors{playlist: 1}, .1}, {"work", similarityFactors{work: 1}, .1}, {"album", similarityFactors{artist: 1, sameAlbum: true}, .15}, {"clamp", similarityFactors{artist: 9, credit: 9, genre: 9, era: 9, playlist: 9, work: 9}, 1}, {"skip", similarityFactors{artist: 1, skipFactor: .5}, .15}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := scoreSimilarity(tc.f)
			if got < tc.want-1e-9 || got > tc.want+1e-9 {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
func TestNormalizedTitle(t *testing.T) {
	for _, tc := range [][2]string{{"ＨＥＬＬＯ　(TV Size)", "hello"}, {"勇者【カラオケ】", "勇者"}, {" Song [Remastered]", "song"}, {"炎（オフボーカル）", "炎"}, {"曲「インスト」", "曲"},{"Song (Instinct)","song (instinct)"},{"Song (TV Size (Remastered))","song"},{"(Instrumental)","(instrumental)"}} {
		if got := normalizedTitle(tc[0]); got != tc[1] {
			t.Errorf("%q => %q want %q", tc[0], got, tc[1])
		}
	}
}
func similarityFixture(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "sim.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	for _, q := range []string{`INSERT INTO libraries(id,name,root_path) VALUES(1,'test','/music')`, `INSERT INTO albums(id,library_id,title,sort_title,grouping_key) VALUES(1,1,'one','one','one'),(2,1,'two','two','two')`, `INSERT INTO artists(id,display_name,sort_name,identity_key) VALUES(1,'Singer','singer','singer'),(2,'Merged','merged','merged')`, `UPDATE artists SET merged_into_artist_id=1 WHERE id=2`} {
		if _, e := s.db.ExecContext(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	return s
}
func addSimilarityTrack(t *testing.T, s *Store, id int, title, kind string, available bool) {
	t.Helper()
	ctx := context.Background()
	album := 2
	if id == 1 {
		album = 1
	}
	_, e := s.db.ExecContext(ctx, `INSERT INTO tracks(id,album_id,title,sort_title,track_type) VALUES(?,?,?,?,?)`, id, album, title, title, kind)
	if e != nil {
		t.Fatal(e)
	}
	artist := 1
	if id == 2 {
		artist = 2
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id) VALUES(?,?)`, id, artist)
	if e != nil {
		t.Fatal(e)
	}
	status := "missing"
	if available {
		status = "available"
	}
	_, e = s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,status) VALUES(1,?,?,1,1,?)`, id, fmt.Sprint(id), status)
	if e != nil {
		t.Fatal(e)
	}
}
func TestSimilarTracksFilteringDeterminismAndPath(t *testing.T) {
	s := similarityFixture(t)
	addSimilarityTrack(t, s, 1, "Seed", "regular", true)
	addSimilarityTrack(t, s, 2, " ＳＯＮＧ (TV Size)", "regular", true)
	addSimilarityTrack(t, s, 3, "Song", "regular", true)
	addSimilarityTrack(t, s, 4, "Instrumental", "instrumental", true)
	addSimilarityTrack(t, s, 5, "Absent", "regular", false)
	ctx := context.Background()
	a, e := s.SimilarTracks(ctx, 1, 30)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.SimilarTracks(ctx, 1, 30)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(a, b) || len(a) != 1 || a[0].Track.ID != 2 || a[0].Track.Artist != "Merged" {
		t.Fatalf("results %#v / %#v", a, b)
	} // A merged singer remains represented by its credited display name, but scores as canonical singer.
	path, complete, e := s.TrackPath(ctx, 1, 2, 4, time.Second)
	if e != nil || !complete || path[0].ID != 1 || path[len(path)-1].ID != 2 {
		t.Fatalf("path=%v complete=%v err=%v", path, complete, e)
	}
	path, complete, e = s.TrackPath(ctx, 1, 5, 2, time.Second)
	if e != nil || complete || path[len(path)-1].ID != 5 {
		t.Fatalf("fallback %v %v %v", path, complete, e)
	}
	same, complete, e := s.TrackPath(ctx, 1, 1, 2, time.Second)
	if e != nil || !complete || len(same) != 1 {
		t.Fatalf("same %v %v %v", same, complete, e)
	}
	_, complete, e = s.TrackPath(ctx, 1, 2, 4, time.Nanosecond)
	if e != nil || complete {
		t.Fatalf("budget complete=%v err=%v", complete, e)
	}
	x, e := s.SimilarTracks(ctx, 4, 30)
	if e != nil || len(x) == 0 {
		t.Fatalf("non-main seed %v %v", x, e)
	}
}
