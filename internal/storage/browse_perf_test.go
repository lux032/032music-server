//go:build performance

// Performance SLA tests for artist pages and deep track pages. Excluded from
// ordinary and -race runs by the "performance" build tag. Run explicitly with:
//
//	go test -tags=performance -count=1 ./internal/storage -run '^TestBrowseSyntheticPerformance$'

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

const (
	perfAlbums         = 3000
	perfTracksPerAlbum = 10
	perfArtists        = 300
	// perfProlific is the album artist of every tenth album and sings one
	// track on every fifth of the others.
	perfProlific = 1
)

func browsePerfFixture(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "browse-perf.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO libraries(id,name,root_path) VALUES(1,'perf','/music')`)
	for i := 1; i <= perfArtists; i++ {
		name := fmt.Sprintf("artist %03d", i)
		exec(`INSERT INTO artists(id,display_name,sort_name,identity_key) VALUES(?,?,?,?)`, i, name, name, name)
	}
	track := 0
	for a := 1; a <= perfAlbums; a++ {
		title := fmt.Sprintf("album %05d", a)
		exec(`INSERT INTO albums(id,library_id,title,sort_title,grouping_key,release_year) VALUES(?,1,?,?,?,?)`, a, title, title, title, 1990+a%35)
		owner := 2 + a%(perfArtists-1)
		if a%10 == 0 {
			owner = perfProlific
		}
		exec(`INSERT INTO album_artists(album_id,artist_id) VALUES(?,?)`, a, owner)
		for n := 1; n <= perfTracksPerAlbum; n++ {
			track++
			name := fmt.Sprintf("song %06d", track)
			exec(`INSERT INTO tracks(id,album_id,title,sort_title,disc_number,track_number) VALUES(?,?,?,?,1,?)`, track, a, name, name, n)
			exec(`INSERT INTO track_artists(track_id,artist_id) VALUES(?,?)`, track, owner)
			if owner != perfProlific && a%5 == 1 && n == 3 {
				exec(`INSERT INTO track_artists(track_id,artist_id,position) VALUES(?,?,1)`, track, perfProlific)
			}
			exec(`INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(1,?,?,1,1)`, track, name)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	return s
}

// medianOf runs op `runs` times and returns the median duration.
func medianOf(t *testing.T, runs int, op func() error) time.Duration {
	t.Helper()
	durations := make([]time.Duration, runs)
	for i := range durations {
		start := time.Now()
		if err := op(); err != nil {
			t.Fatal(err)
		}
		durations[i] = time.Since(start)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	return durations[runs/2]
}

func TestBrowseSyntheticPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("large synthetic fixture")
	}
	s := browsePerfFixture(t)
	ctx := context.Background()
	appearances := perfAlbums / 5 // a%5==1 albums, never owned by the prolific artist

	// Artist page: /albums?artist= (list + count) and the discography. A
	// correlated "track credit" subquery driven by the artist's tracks made
	// both cost albums x artist tracks (seconds for a prolific artist).
	f := Filters{ArtistID: perfProlific, Limit: 500}
	albumsByArtist := medianOf(t, 5, func() error {
		albums, err := s.ListAlbums(ctx, f)
		if err != nil {
			return err
		}
		total, err := s.CountAlbums(ctx, f)
		if err != nil {
			return err
		}
		if want := int64(perfAlbums/10 + appearances); total != want || len(albums) != 500 {
			return fmt.Errorf("albums=%d total=%d want total %d", len(albums), total, want)
		}
		return nil
	})
	discography := medianOf(t, 5, func() error {
		releases, err := s.ArtistDiscography(ctx, perfProlific)
		if want := perfAlbums/10 + appearances; err == nil && len(releases) != want {
			return fmt.Errorf("releases=%d want %d", len(releases), want)
		}
		return err
	})
	t.Logf("artist albums p50=%s discography p50=%s", albumsByArtist, discography)
	if albumsByArtist > time.Second {
		t.Fatalf("albums by artist p50=%s (>1s)", albumsByArtist)
	}
	if discography > 2*time.Second {
		t.Fatalf("discography p50=%s (>2s)", discography)
	}

	// Track pages: the display columns are computed for one page only, so
	// the last page costs about as much as the first.
	page := func(offset int) func() error {
		return func() error {
			tracks, err := s.ListTracks(ctx, Filters{Sort: "title", Limit: 500, Offset: offset})
			if err == nil && len(tracks) != 500 {
				return fmt.Errorf("offset %d: %d tracks", offset, len(tracks))
			}
			return err
		}
	}
	first := medianOf(t, 5, page(0))
	last := medianOf(t, 5, page(perfAlbums*perfTracksPerAlbum-500))
	t.Logf("tracks first page p50=%s last page p50=%s", first, last)
	if last > 3*first+100*time.Millisecond {
		t.Fatalf("last track page p50=%s vs first %s: deep pages grow with the offset", last, first)
	}
}
