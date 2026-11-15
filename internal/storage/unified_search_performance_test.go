package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Matches the reported library size; importing tiny fixtures cannot reveal
// accidentally repeated full-library work inside correlated search queries.
func largeSearchTestStore(t testing.TB) *Store {
	t.Helper()
	// Benchmarks use their own DB too, without invoking the scanner or external services.
	s, err := Open(t.TempDir() + "/search.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`INSERT INTO libraries(id,name,root_path) VALUES(1,'Large search fixture','/music')`,
		`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<3600) INSERT INTO artists(id,display_name,sort_name,identity_key) SELECT i,printf('Artist %04d',i),printf('Artist %04d',i),printf('artist-%d',i) FROM n`,
		`UPDATE artists SET display_name='ナナヲアカリ' WHERE id=1`,
		`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1800) INSERT INTO albums(id,library_id,title,sort_title,grouping_key) SELECT i,1,printf('Album %04d',i),printf('Album %04d',i),printf('album-%d',i) FROM n`,
		`INSERT INTO album_artists(album_id,artist_id) SELECT id,id FROM albums`,
		`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<18000) INSERT INTO tracks(id,album_id,title,sort_title,track_number) SELECT i,(i-1)/10+1,printf('Song %05d',i),printf('Song %05d',i),(i-1)%10+1 FROM n`,
		`INSERT INTO track_artists(track_id,artist_id,role) SELECT id,album_id,'primary' FROM tracks`,
		`INSERT INTO track_artists(track_id,artist_id,role) SELECT id,album_id+1800,'composer' FROM tracks`,
		`INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,container) SELECT 1,id,printf('%d.flac',id),100,1,'flac' FROM tracks`,
		`INSERT INTO artist_external_profiles(artist_id,source,external_id,display_name,aliases_json,raw_json,fetched_at) SELECT id,'musicbrainz',printf('mb-%d',id),display_name,json_array(display_name,display_name||' alias',printf('Artist %04d',id)),'{}','2026-11-15T00:00:00Z' FROM artists`,
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestUnifiedSearchLargeLibrary(t *testing.T) {
	s := largeSearchTestStore(t)
	for _, query := range []string{"ナナヲアカリ", "no-such-artist", "Artist"} {
		t.Run(query, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			result, err := s.Search(ctx, SearchOptions{Query: query})
			if err != nil {
				t.Fatalf("search of 18,000 tracks took %s: %v", time.Since(start), err)
			}
			t.Logf("18,000 tracks search %q: %s", query, time.Since(start))
			if query == "ナナヲアカリ" && (result.Artists.Total != 1 || result.Albums.Total != 1 || result.Tracks.Total != 10) {
				t.Fatalf("unexpected result: %+v", result)
			}
			if query == "no-such-artist" && result.Artists.Total+result.Albums.Total+result.Tracks.Total+result.Composers.Total != 0 {
				t.Fatal("absent query matched content")
			}
			if query == "Artist" && (result.Artists.Total != 1800 || result.Tracks.Total != 18000 || result.Composers.Total != 1800) {
				t.Fatalf("broad query totals: artists=%d tracks=%d composers=%d", result.Artists.Total, result.Tracks.Total, result.Composers.Total)
			}
		})
	}
}

func BenchmarkUnifiedSearchLargeLibrary(b *testing.B) {
	s := largeSearchTestStore(b)
	for _, query := range []string{"ナナヲアカリ", "no-such-artist", "Artist"} {
		b.Run(fmt.Sprintf("18000/%s", query), func(b *testing.B) {
			for b.Loop() {
				if _, err := s.Search(context.Background(), SearchOptions{Query: query}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
