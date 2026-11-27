package storage

import (
	"encoding/json"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestAIArtistConflictMergeAndRevertBidirectional(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	var libID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM libraries WHERE root_path='/music'`).Scan(&libID); err != nil {
		t.Fatalf("library id: %v", err)
	}
	const mbid = "33333333-3333-4333-8333-333333333333"

	importOne := func(path, name string) int64 {
		t.Helper()
		if err := s.ImportTrack(ctx, ImportInput{
			LibraryID:    libID,
			RelativePath: path,
			FileSize:     100,
			ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title:        path,
				Album:        "Album " + path,
				DiscNumber:   1,
				TrackNumber:  1,
				Artists:      []string{name},
				AlbumArtists: []string{name},
			},
		}); err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key=?`, metadata.Normalize(name)).Scan(&id); err != nil {
			t.Fatalf("find artist %s: %v", name, err)
		}
		return id
	}

	jpID := importOne("jp.flac", "梶浦由記")
	romaji1ID := importOne("romaji1.flac", "Yuki Kajiura")
	romaji2ID := importOne("romaji2.flac", "Kajiura Yuki")

	if err := s.UpsertExternalArtistProfile(ctx, jpID, ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "梶浦由記",
		Country:     "JP",
		Aliases:     []string{"Yuki Kajiura"},
	}); err != nil {
		t.Fatalf("seed jp owner profile: %v", err)
	}

	seedCandidate := func(artistID int64) int64 {
		t.Helper()
		if err := s.ReplaceArtistCandidates(ctx, artistID, []ArtistCandidate{{
			ArtistID:    artistID,
			Source:      "musicbrainz",
			ExternalID:  mbid,
			DisplayName: "梶浦由記",
			MBID:        mbid,
			Score:       85,
			Payload:     []byte(`{"aliases":["Yuki Kajiura"]}`),
		}}); err != nil {
			t.Fatalf("seed candidate for %d: %v", artistID, err)
		}
		cands, err := s.ArtistCandidates(ctx, artistID)
		if err != nil || len(cands) != 1 {
			t.Fatalf("list candidates for %d: %v (len=%d)", artistID, err, len(cands))
		}
		return cands[0].ID
	}

	cand1 := seedCandidate(romaji1ID)
	cand2 := seedCandidate(romaji2ID)

	// Verify LoadArtistAIContext enriches ConflictOwnerName & ConflictOwnerAliases.
	aiCtx, err := s.LoadArtistAIContext(ctx, romaji1ID)
	if err != nil || len(aiCtx.Candidates) != 1 {
		t.Fatalf("LoadArtistAIContext: %+v err=%v", aiCtx, err)
	}
	if aiCtx.Candidates[0].ConflictOwnerID != jpID || aiCtx.Candidates[0].ConflictOwner != "梶浦由記" {
		t.Fatalf("unexpected conflict owner in AI context: %+v", aiCtx.Candidates[0])
	}

	// Direction 1: Merge via AI, then revert via RevertAIReviewDecision -> rolls back merge op & restores candidates.
	merged, snap, err := s.MergeArtistConflictByAI(ctx, romaji1ID, cand1, jpID)
	if err != nil || !merged {
		t.Fatalf("MergeArtistConflictByAI(1): merged=%v err=%v", merged, err)
	}
	var parsedSnap aiArtistConfirmSnapshot
	if err = json.Unmarshal(snap, &parsedSnap); err != nil || !parsedSnap.Merged || parsedSnap.MergeOperationID <= 0 || parsedSnap.TargetArtistID != jpID {
		t.Fatalf("unexpected AI merge snapshot: %s (%+v)", string(snap), parsedSnap)
	}
	decisionID, err := s.SaveAIReviewDecision(ctx, AIReviewDecision{
		TargetKind:          AITargetArtistMatch,
		TargetID:            romaji1ID,
		SelectedCandidateID: cand1,
		Fingerprint:         aiCtx.Fingerprint,
		Action:              AIActionAutoConfirm,
		Confidence:          96,
		Reason:              "同一艺术家罗马音",
		Applied:             true,
		AppliedSnapshot:     snap,
	})
	if err != nil {
		t.Fatalf("SaveAIReviewDecision(1): %v", err)
	}
	if err = s.RevertAIReviewDecision(ctx, decisionID); err != nil {
		t.Fatalf("RevertAIReviewDecision(1): %v", err)
	}
	var opStatus string
	if err = s.db.QueryRowContext(ctx, `SELECT status FROM artist_merge_operations WHERE id=?`, parsedSnap.MergeOperationID).Scan(&opStatus); err != nil || opStatus != "rolled_back" {
		t.Fatalf("expected merge op rolled_back after AI revert, got %q err=%v", opStatus, err)
	}
	if suppressed, _ := s.HasRolledBackArtistMergePair(ctx, romaji1ID, jpID); !suppressed {
		t.Fatalf("expected rolled-back suppression active after AI revert")
	}
	// Candidate restored to 'candidate', and repeat MergeArtistConflictByAI is blocked by rollback memory.
	mergedAgain, _, err := s.MergeArtistConflictByAI(ctx, romaji1ID, cand1, jpID)
	if err != nil || mergedAgain {
		t.Fatalf("expected suppressed pair NOT re-merged by AI, got merged=%v err=%v", mergedAgain, err)
	}

	// Direction 2: Merge via AI, then revert via RollbackArtistMerge (/admin/merges) -> syncs ai_review_decisions.reverted=1.
	merged2, snap2, err := s.MergeArtistConflictByAI(ctx, romaji2ID, cand2, jpID)
	if err != nil || !merged2 {
		t.Fatalf("MergeArtistConflictByAI(2): merged=%v err=%v", merged2, err)
	}
	var parsedSnap2 aiArtistConfirmSnapshot
	_ = json.Unmarshal(snap2, &parsedSnap2)
	decisionID2, err := s.SaveAIReviewDecision(ctx, AIReviewDecision{
		TargetKind:          AITargetArtistMatch,
		TargetID:            romaji2ID,
		SelectedCandidateID: cand2,
		Fingerprint:         "fp2",
		Action:              AIActionAutoConfirm,
		Confidence:          95,
		Reason:              "同一艺术家别名",
		Applied:             true,
		AppliedSnapshot:     snap2,
	})
	if err != nil {
		t.Fatalf("SaveAIReviewDecision(2): %v", err)
	}
	if err = s.RollbackArtistMerge(ctx, parsedSnap2.MergeOperationID); err != nil {
		t.Fatalf("RollbackArtistMerge(2): %v", err)
	}
	d2, err := s.AIReviewDecisionByID(ctx, decisionID2)
	if err != nil || !d2.Reverted {
		t.Fatalf("expected AI decision marked reverted=true after RollbackArtistMerge, got %+v err=%v", d2, err)
	}
}
