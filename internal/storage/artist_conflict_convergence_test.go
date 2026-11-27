package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestConvergeDeterministicArtistConflicts(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}
	var libID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM libraries WHERE root_path='/music'`).Scan(&libID); err != nil {
		t.Fatalf("library id: %v", err)
	}
	const (
		mbid1 = "11111111-1111-4111-8111-111111111111"
		mbid2 = "22222222-2222-4222-8222-222222222222"
	)

	importTrack := func(path, artistName, tagMBID string) int64 {
		t.Helper()
		raw := map[string][]string{"ARTIST": {artistName}}
		if tagMBID != "" {
			raw["MUSICBRAINZ_ARTISTID"] = []string{tagMBID}
		}
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
				Container:    "flac",
				Artists:      []string{artistName},
				AlbumArtists: []string{artistName},
				Raw:          raw,
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

	// Owner 1: 梶浦由記 (mbid1)
	owner1 := importTrack("o1.flac", "梶浦由記", mbid1)
	if err := s.UpsertExternalArtistProfile(ctx, owner1, ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  mbid1,
		DisplayName: "梶浦由記",
		Country:     "JP",
		Aliases:     []string{"Yuki Kajiura", "Kajiura Yuki"},
	}); err != nil {
		t.Fatalf("upsert owner1: %v", err)
	}

	// Owner 2: 澤野弘之 (mbid2)
	owner2 := importTrack("o2.flac", "澤野弘之", mbid2)
	if err := s.UpsertExternalArtistProfile(ctx, owner2, ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  mbid2,
		DisplayName: "澤野弘之",
		Country:     "JP",
		Aliases:     []string{"Hiroyuki Sawano"},
	}); err != nil {
		t.Fatalf("upsert owner2: %v", err)
	}

	// Duplicate A: pending score=100 conflict candidate for mbid1 -> SHOULD converge.
	dupCand := importTrack("dupA.flac", "Yuki Kajiura", "")
	if err := s.ReplaceArtistCandidates(ctx, dupCand, []ArtistCandidate{{
		ArtistID:    dupCand,
		Source:      "musicbrainz",
		ExternalID:  mbid1,
		DisplayName: "梶浦由記",
		MBID:        mbid1,
		Score:       100,
		Payload:     []byte(`{}`),
	}}); err != nil {
		t.Fatalf("seed dupA candidate: %v", err)
	}

	// Duplicate B: no candidates yet, but TaggedMBID=mbid2 and name matches alias -> SHOULD converge.
	dupTag := importTrack("dupB.flac", "Hiroyuki Sawano", mbid2)

	// Duplicate C: TaggedMBID=mbid1 but UNRELATED name -> MUST NOT converge.
	_ = importTrack("dupC.flac", "Misattributed Singer", mbid1)

	// Duplicate D: composite credit with pending score=100 candidate -> MUST NOT converge.
	dupComp := importTrack("dupD.flac", "Yuki Kajiura feat. Guest", "")
	_ = s.ReplaceArtistCandidates(ctx, dupComp, []ArtistCandidate{{
		ArtistID:    dupComp,
		Source:      "musicbrainz",
		ExternalID:  mbid1,
		DisplayName: "梶浦由記",
		MBID:        mbid1,
		Score:       100,
		Payload:     []byte(`{}`),
	}})

	// Duplicate E: previously rolled back against owner1 -> MUST NOT converge.
	dupRolledBack := importTrack("dupE.flac", "Kajiura Yuki", mbid1)
	rbOp, err := s.MergeArtists(ctx, dupRolledBack, owner1)
	if err != nil {
		t.Fatalf("seed manual merge for rollback: %v", err)
	}
	if err = s.RollbackArtistMerge(ctx, rbOp); err != nil {
		t.Fatalf("rollback dupE: %v", err)
	}

	count, err := s.CountConvergeableArtistConflicts(ctx)
	if err != nil {
		t.Fatalf("CountConvergeableArtistConflicts: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected exactly 2 convergeable conflicts (dupA + dupB), got %d", count)
	}

	res, err := s.ConvergeDeterministicArtistConflicts(ctx)
	if err != nil {
		t.Fatalf("ConvergeDeterministicArtistConflicts: %v", err)
	}
	if res.Merged != 2 || res.Failed != 0 {
		t.Fatalf("expected Merged=2 Failed=0, got %+v", res)
	}

	// Verify both dupA and dupB are merged into their canonical owners with trigger_source='backfill'.
	dA, _ := s.ArtistDetail(ctx, dupCand)
	dB, _ := s.ArtistDetail(ctx, dupTag)
	if dA.MergedIntoID != owner1 || dB.MergedIntoID != owner2 {
		t.Fatalf("unexpected merge targets: dupA->%d (want %d), dupB->%d (want %d)", dA.MergedIntoID, owner1, dB.MergedIntoID, owner2)
	}
	ops, _ := s.MergeOperations(ctx)
	backfillOps := 0
	for _, op := range ops {
		if op.TriggerSource == MergeTriggerBackfill && op.Status == "merged" {
			backfillOps++
		}
	}
	if backfillOps != 2 {
		t.Fatalf("expected 2 backfill merge ops, got %d (%+v)", backfillOps, ops)
	}

	// Repeat count should be 0.
	countAfter, err := s.CountConvergeableArtistConflicts(ctx)
	if err != nil || countAfter != 0 {
		t.Fatalf("expected 0 convergeable conflicts after run, got %d err=%v", countAfter, err)
	}
}
