package storage

import (
	"fmt"
	"testing"
	"time"
)

func TestRejectLastOrAllMarksRejectedStateAndMigration078(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, id := seedEnrichmentEntities(t, s, ctx)
	for _, src := range []string{"musicbrainz", "lastfm"} {
		st, err := s.MetadataSourceSetting(ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		st.Enabled = true
		st.AutoMatch = true
		if src == "lastfm" {
			st.APIKey = "test-key"
		}
		if err = s.SaveMetadataSourceSetting(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	input, err := s.ArtistForMatching(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	lf, _ := s.MetadataSourceSetting(ctx, "lastfm")

	if err = s.SaveArtistSourceCheck(ctx, input, mb, ArtistSourceSnapshot{
		Candidates: []ArtistCandidate{
			{Source: "musicbrainz", ExternalID: "mb-1", DisplayName: "MB One", Score: 85},
			{Source: "musicbrainz", ExternalID: "mb-2", DisplayName: "MB Two", Score: 80},
		},
	}); err != nil {
		t.Fatal(err)
	}
	cands, err := s.ArtistCandidates(ctx, id)
	if err != nil || len(cands) != 2 {
		t.Fatalf("candidates=%+v err=%v", cands, err)
	}
	// Rejecting the first candidate leaves one pending: state must stay 'review'.
	if err = s.RejectArtistCandidate(ctx, id, cands[0].ID); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err = s.db.QueryRowContext(ctx, `SELECT outcome FROM artist_source_match_state WHERE artist_id=? AND source='musicbrainz'`, id).Scan(&outcome); err != nil || outcome != "review" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	// Rejecting the last candidate marks outcome='rejected'.
	if err = s.RejectArtistCandidate(ctx, id, cands[1].ID); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT outcome FROM artist_source_match_state WHERE artist_id=? AND source='musicbrainz'`, id).Scan(&outcome); err != nil || outcome != "rejected" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
	// Even after TTL expiry, incremental mode skips rejected; full mode re-scans.
	if _, err = s.db.ExecContext(ctx, `UPDATE artist_source_match_state SET checked_at='2000-01-01T00:00:00Z' WHERE artist_id=?`, id); err != nil {
		t.Fatal(err)
	}
	checkInc, err := s.ArtistSourceCheckNeededForMode(ctx, input, mb, ArtistScanIncremental)
	if err != nil || checkInc.Eligible || checkInc.Snapshot != nil {
		t.Fatalf("incremental check=%+v err=%v", checkInc, err)
	}
	checkFull, err := s.ArtistSourceCheckNeededForMode(ctx, input, mb, ArtistScanFull)
	if err != nil || !checkFull.Eligible || checkFull.Snapshot != nil {
		t.Fatalf("full check=%+v err=%v", checkFull, err)
	}

	// RejectAllArtistCandidates with source="" preserves existing no_result on lastfm
	// and marks candidate-bearing/unchecked enabled sources as rejected.
	if input, err = s.ArtistMatchQueryContext(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArtistSourceCheck(ctx, input, lf, ArtistSourceSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if err = s.AddArtistCandidates(ctx, id, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "mb-3", DisplayName: "MB Three", Score: 84}}); err != nil {
		t.Fatal(err)
	}
	n, err := s.RejectAllArtistCandidates(ctx, id, "")
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT outcome FROM artist_source_match_state WHERE artist_id=? AND source='lastfm'`, id).Scan(&outcome); err != nil || outcome != "no_result" {
		t.Fatalf("lastfm outcome=%q err=%v", outcome, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT outcome FROM artist_source_match_state WHERE artist_id=? AND source='musicbrainz'`, id).Scan(&outcome); err != nil || outcome != "rejected" {
		t.Fatalf("mb outcome=%q err=%v", outcome, err)
	}

	// Migration 078 backfill: pre-078 review rows whose candidates are all rejected become 'rejected'.
	if _, err = s.db.ExecContext(ctx, `UPDATE artist_source_match_state SET outcome='review' WHERE artist_id=? AND source='musicbrainz'; DELETE FROM schema_migrations WHERE version>=78; DROP TABLE artist_match_run_modes`, id); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT outcome FROM artist_source_match_state WHERE artist_id=? AND source='musicbrainz'`, id).Scan(&outcome); err != nil || outcome != "rejected" {
		t.Fatalf("migrated outcome=%q err=%v", outcome, err)
	}

	// Legacy rejected candidates without an artist_source_match_state row (e.g. pre-036 or
	// manual search rejections) are still recognized in incremental mode, while ResetArtistIdentity remains eligible.
	if _, err = s.db.ExecContext(ctx, `DELETE FROM artist_source_match_state WHERE artist_id=? AND source='musicbrainz'`, id); err != nil {
		t.Fatal(err)
	}
	if chk, e := s.ArtistSourceCheckNeededForMode(ctx, input, mb, ArtistScanIncremental); e != nil || chk.Eligible {
		t.Fatalf("legacy rejected without state row must skip incremental: %+v %v", chk, e)
	}
	if chk, e := s.ArtistSourceCheckNeededForMode(ctx, input, mb, ArtistScanFull); e != nil || !chk.Eligible {
		t.Fatalf("legacy rejected without state row must run in full mode: %+v %v", chk, e)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artist_match_candidates SET evidence_json='["已手动解除为错误身份"]' WHERE artist_id=? AND source='musicbrainz'`, id); err != nil {
		t.Fatal(err)
	}
	if chk, e := s.ArtistSourceCheckNeededForMode(ctx, input, mb, ArtistScanIncremental); e != nil || !chk.Eligible {
		t.Fatalf("reset identity without state row must stay eligible: %+v %v", chk, e)
	}
}

func TestMergeArtistEffectiveIdentitySkipsScanAndStaleReview(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, sourceArtist := seedEnrichmentEntities(t, s, ctx)
	res, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Target Artist','target artist','target-artist')`)
	if err != nil {
		t.Fatal(err)
	}
	targetArtist, _ := res.LastInsertId()

	mb, err := s.MetadataSourceSetting(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	mb.Enabled = true
	mb.AutoMatch = true
	if err = s.SaveMetadataSourceSetting(ctx, mb); err != nil {
		t.Fatal(err)
	}

	// Source artist has a confirmed MusicBrainz profile; target has a pending candidate on MusicBrainz.
	if err = s.UpsertExternalArtistProfile(ctx, sourceArtist, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mbid-source-1", DisplayName: "Canonical"}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceArtistCandidates(ctx, targetArtist, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "mbid-source-1", MBID: "mbid-source-1", DisplayName: "Canonical", Score: 90}}); err != nil {
		t.Fatal(err)
	}

	op, err := s.MergeArtists(ctx, sourceArtist, targetArtist)
	if err != nil {
		t.Fatal(err)
	}

	effID, err := s.ArtistEffectiveExternalID(ctx, targetArtist, "musicbrainz")
	if err != nil || effID != "mbid-source-1" {
		t.Fatalf("effective id=%q err=%v", effID, err)
	}
	targetInput, err := s.ArtistForMatching(ctx, targetArtist)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []ArtistScanMode{ArtistScanIncremental, ArtistScanFull} {
		chk, e := s.ArtistSourceCheckNeededForMode(ctx, targetInput, mb, mode)
		if e != nil || chk.Eligible {
			t.Fatalf("mode=%s check=%+v err=%v", mode, chk, e)
		}
	}
	page, err := s.PendingArtistReviewPage(ctx, ArtistReviewFilter{})
	if err != nil || page.Total != 0 {
		t.Fatalf("review page=%+v err=%v", page, err)
	}
	pendingCount, err := s.PendingArtistCandidateCount(ctx, targetArtist)
	if err != nil || pendingCount != 0 {
		t.Fatalf("pendingCount=%d err=%v", pendingCount, err)
	}

	// Rolling back the merge restores targetArtist's pending candidate and scan eligibility.
	if err = s.RollbackArtistMerge(ctx, op); err != nil {
		t.Fatal(err)
	}
	page, err = s.PendingArtistReviewPage(ctx, ArtistReviewFilter{})
	if err != nil || page.Total != 1 {
		t.Fatalf("after rollback review page=%+v err=%v", page, err)
	}
}

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
