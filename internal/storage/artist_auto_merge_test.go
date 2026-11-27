package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestAutoMergeArtistIdentityConflictAndGuards(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	var libID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM libraries WHERE root_path='/music'`).Scan(&libID); err != nil {
		t.Fatalf("library id: %v", err)
	}
	const mbid = "11111111-1111-4111-8111-111111111111"

	importOne := func(path, artistName string) int64 {
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
				Artists:      []string{artistName},
				AlbumArtists: []string{artistName},
			},
		}); err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		var id int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key=?`, metadata.Normalize(artistName)).Scan(&id); err != nil {
			t.Fatalf("find artist %s: %v", artistName, err)
		}
		return id
	}

	jpID := importOne("jp.flac", "梶浦由記")
	romajiID := importOne("romaji.flac", "Yuki Kajiura")
	wrongID := importOne("wrong.flac", "Wrong Artist")
	compID := importOne("comp.flac", "Yuki Kajiura feat. Guest")
	lowScoreID := importOne("low.flac", "Y. Kajiura")

	profile := ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "梶浦由記",
		Country:     "JP",
		Aliases:     []string{"Yuki Kajiura"},
	}
	if err := s.UpsertExternalArtistProfile(ctx, jpID, profile); err != nil {
		t.Fatalf("seed jp owner profile: %v", err)
	}

	// 1. High-confidence (>=92), non-manual-review, non-composite conflict auto-merges.
	if err := s.ReplaceArtistCandidates(ctx, romajiID, []ArtistCandidate{{
		ArtistID:    romajiID,
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "梶浦由記",
		MBID:        mbid,
		Score:       100,
		Payload:     []byte(`{}`),
	}}); err != nil {
		t.Fatalf("seed romaji candidate: %v", err)
	}
	res, err := s.AutoBindOrMergeArtistCandidate(ctx, romajiID, profile)
	if err != nil {
		t.Fatalf("AutoBindOrMergeArtistCandidate: %v", err)
	}
	if !res.Merged || res.Bound || res.TargetArtistID != jpID || res.MergeOperationID <= 0 {
		t.Fatalf("expected auto-merge into %d, got %+v", jpID, res)
	}
	ops, err := s.MergeOperations(ctx)
	if err != nil || len(ops) != 1 || ops[0].TriggerSource != MergeTriggerAutoMatch || ops[0].Status != "merged" {
		t.Fatalf("expected 1 auto_match merge op, got %+v err=%v", ops, err)
	}

	// 2. requires_manual_review=1 blocks auto-merge.
	if err := s.ReplaceArtistCandidates(ctx, wrongID, []ArtistCandidate{{
		ArtistID:             wrongID,
		Source:               "musicbrainz",
		ExternalID:           mbid,
		DisplayName:          "梶浦由記",
		MBID:                 mbid,
		Score:                100,
		RequiresManualReview: true,
		Payload:              []byte(`{}`),
	}}); err != nil {
		t.Fatalf("seed wrong candidate: %v", err)
	}
	resWrong, err := s.AutoBindOrMergeArtistCandidate(ctx, wrongID, profile)
	if err != nil || resWrong.Merged || resWrong.Bound {
		t.Fatalf("expected manual-review candidate blocked, got %+v err=%v", resWrong, err)
	}

	// 3. Composite credit blocks auto-merge.
	if err := s.ReplaceArtistCandidates(ctx, compID, []ArtistCandidate{{
		ArtistID:    compID,
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "梶浦由記",
		MBID:        mbid,
		Score:       100,
		Payload:     []byte(`{}`),
	}}); err != nil {
		t.Fatalf("seed composite candidate: %v", err)
	}
	resComp, err := s.AutoBindOrMergeArtistCandidate(ctx, compID, profile)
	if err != nil || resComp.Merged || resComp.Bound {
		t.Fatalf("expected composite credit blocked, got %+v err=%v", resComp, err)
	}

	// 4. Low-score (<92) candidate does NOT auto-merge.
	if err := s.ReplaceArtistCandidates(ctx, lowScoreID, []ArtistCandidate{{
		ArtistID:    lowScoreID,
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "梶浦由記",
		MBID:        mbid,
		Score:       85,
		Payload:     []byte(`{}`),
	}}); err != nil {
		t.Fatalf("seed low-score candidate: %v", err)
	}
	resLow, err := s.AutoBindOrMergeArtistCandidate(ctx, lowScoreID, profile)
	if err != nil || resLow.Merged || resLow.Bound {
		t.Fatalf("expected low-score candidate blocked, got %+v err=%v", resLow, err)
	}
}

func TestRolledBackMergeSuppressionBlocksReAutoMerge(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	var libID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM libraries WHERE root_path='/music'`).Scan(&libID); err != nil {
		t.Fatalf("library id: %v", err)
	}
	const mbid = "22222222-2222-4222-8222-222222222222"

	for i, name := range []string{"澤野弘之", "Hiroyuki Sawano"} {
		if err := s.ImportTrack(ctx, ImportInput{
			LibraryID:    libID,
			RelativePath: name + ".flac",
			FileSize:     100,
			ModifiedAtNS: int64(i + 1),
			Metadata: metadata.AudioMetadata{
				Title:        name,
				Album:        "Album " + name,
				DiscNumber:   1,
				TrackNumber:  1,
				Artists:      []string{name},
				AlbumArtists: []string{name},
			},
		}); err != nil {
			t.Fatalf("import %s: %v", name, err)
		}
	}
	var jpID, romajiID int64
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key='澤野弘之'`).Scan(&jpID)
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE identity_key='hiroyuki sawano'`).Scan(&romajiID)

	profile := ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "澤野弘之",
		Country:     "JP",
		Aliases:     []string{"Hiroyuki Sawano"},
	}
	if err := s.UpsertExternalArtistProfile(ctx, jpID, profile); err != nil {
		t.Fatalf("upsert jp profile: %v", err)
	}
	if err := s.ReplaceArtistCandidates(ctx, romajiID, []ArtistCandidate{{
		ArtistID:    romajiID,
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "澤野弘之",
		MBID:        mbid,
		Score:       100,
		Payload:     []byte(`{}`),
	}}); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}

	res, err := s.AutoBindOrMergeArtistCandidate(ctx, romajiID, profile)
	if err != nil || !res.Merged {
		t.Fatalf("initial auto-merge failed: %+v err=%v", res, err)
	}

	// Admin rolls back the merge.
	if err = s.RollbackArtistMerge(ctx, res.MergeOperationID); err != nil {
		t.Fatalf("rollback merge: %v", err)
	}

	// Check symmetric pair suppression in both directions.
	if suppressed, err := s.HasRolledBackArtistMergePair(ctx, romajiID, jpID); err != nil || !suppressed {
		t.Fatalf("expected (romaji, jp) suppressed, got %v err=%v", suppressed, err)
	}
	if suppressed, err := s.HasRolledBackArtistMergePair(ctx, jpID, romajiID); err != nil || !suppressed {
		t.Fatalf("expected (jp, romaji) symmetrically suppressed, got %v err=%v", suppressed, err)
	}

	// Re-seeding high-confidence candidate MUST NOT auto-merge again.
	if err = s.ReplaceArtistCandidates(ctx, romajiID, []ArtistCandidate{{
		ArtistID:    romajiID,
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "澤野弘之",
		MBID:        mbid,
		Score:       100,
		Payload:     []byte(`{}`),
	}}); err != nil {
		t.Fatalf("re-seed candidate: %v", err)
	}
	resAgain, err := s.AutoBindOrMergeArtistCandidate(ctx, romajiID, profile)
	if err != nil {
		t.Fatalf("second auto-bind error: %v", err)
	}
	if resAgain.Merged || resAgain.Bound {
		t.Fatalf("expected rolled-back pair suppressed from re-auto-merge, got %+v", resAgain)
	}
}
