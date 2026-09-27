//go:build performance

// Performance SLA tests. Excluded from ordinary and -race runs by the
// "performance" build tag; the thresholds assume a normal optimized build.
// Run explicitly with:
//
//	go test -tags=performance -count=1 ./internal/storage -run '^TestSimilarTracksSyntheticPerformance$'

package storage

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestSimilarTracksSyntheticPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("large synthetic fixture")
	}
	s := similarityFixture(t)
	ctx := context.Background()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	for i := 3; i <= 502; i++ {
		_, e = tx.ExecContext(ctx, `INSERT INTO artists(id,display_name,sort_name,identity_key) VALUES(?,?,?,?)`, i, fmt.Sprint(i), fmt.Sprint(i), fmt.Sprint(i))
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 1; i <= 20; i++ {
		_, e = tx.ExecContext(ctx, `INSERT INTO genres(id,name) VALUES(?,?)`, i, fmt.Sprint(i))
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 1; i <= 50; i++ {
		_, e = tx.ExecContext(ctx, `INSERT INTO playlists(id,name) VALUES(?,?)`, i, fmt.Sprint(i))
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 1; i <= 50000; i++ {
		title := fmt.Sprintf("song %d", i)
		for _, v := range []struct {
			q    string
			args []any
		}{{`INSERT INTO tracks(id,album_id,title,sort_title) VALUES(?,2,?,?)`, []any{i, title, title}}, {`INSERT INTO track_artists(track_id,artist_id) VALUES(?,?)`, []any{i, 3 + (i % 500)}}, {`INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(1,?,?,1,1)`, []any{i, title}}} {
			if _, e = tx.ExecContext(ctx, v.q, v.args...); e != nil {
				t.Fatal(e)
			}
		}
		genre := 1 + i%20
		q := `INSERT INTO track_genres(track_id,genre_id) VALUES(?,?)`
		if i%2 == 0 {
			q = `INSERT INTO track_genre_overrides(track_id,genre_id) VALUES(?,?)`
		}
		if _, e = tx.ExecContext(ctx, q, i, genre); e != nil {
			t.Fatal(e)
		}
		if i <= 15000 {
			playlist := 1 + (i-1)/300
			if _, e = tx.ExecContext(ctx, `INSERT INTO playlist_items(playlist_id,track_id,position) VALUES(?,?,?)`, playlist, i, (i-1)%300); e != nil {
				t.Fatal(e)
			}
		}
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO works(id,title,normalized_title,type) VALUES(1,'Perf work','perf work','anime')`); e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 200; i++ {
		if _, e = tx.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id) VALUES(1,?)`, i); e != nil {
			t.Fatal(e)
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(?,3,'composer')`, i); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE audio_files SET status='missing' WHERE track_id=50000`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	durations := make([]time.Duration, 20)
	for i := range durations {
		start := time.Now()
		items, e := s.SimilarTracks(ctx, int64(1+i*20), 30)
		durations[i] = time.Since(start)
		if e != nil || len(items) == 0 {
			t.Fatalf("items=%d error=%v", len(items), e)
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("50000 tracks similar p50=%s p95=%s", durations[10], durations[18])
	// A single scheduler stall is not a query regression; the median of 20
	// independent lookups still exposes sustained slowdowns.
	if durations[10] > 600*time.Millisecond {
		t.Fatalf("similar p50=%s (>600ms)", durations[10])
	}
	// TrackPath has a single sample: retry one slow run to distinguish a
	// momentary CPU pause from consistently excessive work. The search budget
	// is well above pathLimit so a stalled run still completes the full path
	// (a budget cut would shorten it and fail the length check without retry).
	const pathLimit = 4 * time.Second
	for attempt := 1; attempt <= 2; attempt++ {
		start := time.Now()
		path, _, err := s.TrackPath(ctx, 1, 50000, 25, 5*pathLimit)
		elapsed := time.Since(start)
		t.Logf("path attempt=%d limit=25 steps=%d duration=%s", attempt, len(path)-1, elapsed)
		if err != nil || len(path) != 25 {
			t.Fatalf("path tracks=%d duration=%s err=%v", len(path), elapsed, err)
		}
		if elapsed <= pathLimit {
			break
		}
		if attempt == 2 {
			t.Fatalf("path exceeded %s twice; last duration=%s", pathLimit, elapsed)
		}
	}
}
