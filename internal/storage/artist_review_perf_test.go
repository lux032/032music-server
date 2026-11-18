//go:build performance

// Performance SLA test for PendingArtistReviewPage with merged artists.
// Excluded from ordinary and -race runs by the "performance" build tag.

package storage

import (
	"fmt"
	"testing"
	"time"
)

func TestPendingArtistReviewPageScaleLatency(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	const totalArtists = 3000
	const mergedCount = 500
	for i := 1; i <= totalArtists; i++ {
		res, e := tx.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?)`,
			fmt.Sprintf("Artist %04d", i), fmt.Sprintf("artist %04d", i), fmt.Sprintf("key-%04d", i))
		if e != nil {
			t.Fatal(e)
		}
		id, _ := res.LastInsertId()
		if _, e = tx.ExecContext(ctx, `INSERT INTO artist_match_candidates(artist_id,source,external_id,display_name,score,payload_json,status) VALUES(?,'musicbrainz',?,?,85,'{}','candidate')`,
			id, fmt.Sprintf("ext-%d", id), fmt.Sprintf("Cand %d", id)); e != nil {
			t.Fatal(e)
		}
		if i <= mergedCount {
			mRes, e2 := tx.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key,merged_into_artist_id) VALUES(?,?,?,?)`,
				fmt.Sprintf("Merged %04d", i), fmt.Sprintf("merged %04d", i), fmt.Sprintf("mkey-%04d", i), id)
			if e2 != nil {
				t.Fatal(e2)
			}
			mid, _ := mRes.LastInsertId()
			if _, e2 = tx.ExecContext(ctx, `INSERT INTO artist_external_profiles(artist_id,source,external_id,display_name,raw_json,fetched_at) VALUES(?,'musicbrainz',?,?, '{}','2026-01-01T00:00:00Z')`,
				mid, fmt.Sprintf("mbid-%d", mid), fmt.Sprintf("Merged %d", i)); e2 != nil {
				t.Fatal(e2)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	page, err := s.PendingArtistReviewPage(ctx, ArtistReviewFilter{Limit: 50})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != totalArtists-mergedCount || len(page.Items) != 50 {
		t.Fatalf("total=%d items=%d", page.Total, len(page.Items))
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("PendingArtistReviewPage too slow at scale: %v", elapsed)
	}
	t.Logf("PendingArtistReviewPage (%d artists, %d merged): %v", totalArtists+mergedCount, mergedCount, elapsed)
}
