package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func openCompositeSplitTestStore(t *testing.T) (*Store, Library) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "split.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	return s, lib
}

func artistIDByName(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRowContext(context.Background(), `SELECT id FROM artists WHERE display_name=?`, name).Scan(&id); err != nil {
		t.Fatalf("artist %q not found: %v", name, err)
	}
	return id
}

func trackRoleArtistNames(t *testing.T, s *Store, trackID int64, role string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT COALESCE(a.user_display_name,a.display_name) FROM track_artists ta JOIN artists a ON a.id=ta.artist_id WHERE ta.track_id=? AND ta.role=? ORDER BY ta.position,a.id`, trackID, role)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

func albumArtistNames(t *testing.T, s *Store, albumID int64) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `SELECT COALESCE(a.user_display_name,a.display_name) FROM album_artists aa JOIN artists a ON a.id=aa.artist_id WHERE aa.album_id=? ORDER BY aa.position,a.id`, albumID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	return out
}

func TestApplyCompositeCreditSplitBackstageAndMixedRoles(t *testing.T) {
	ctx := context.Background()
	s, lib := openCompositeSplitTestStore(t)

	// Track 1: backstage composer-only composite credit ("ACE, 亀岡夏海").
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "OST/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Battle Theme", Album: "Xenoblade OST",
			Artists: []string{"Main Performer"}, AlbumArtists: []string{"Main Performer"},
			Composer: "ACE, 亀岡夏海",
			DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	// Track 2: mixed primary + lyricist + album_artist composite credit ("Aimer feat. Sawano").
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "Collab/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Collab Song", Album: "Collab Single",
			Artists: []string{"Aimer feat. Sawano"}, AlbumArtists: []string{"Aimer feat. Sawano"},
			Lyricist: "Aimer feat. Sawano",
			DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}

	var track1ID, track2ID, album2ID int64
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE title='Battle Theme'`).Scan(&track1ID)
	_ = s.db.QueryRowContext(ctx, `SELECT id,album_id FROM tracks WHERE title='Collab Song'`).Scan(&track2ID, &album2ID)

	compBackstageID := artistIDByName(t, s, "ACE, 亀岡夏海")
	compMixedID := artistIDByName(t, s, "Aimer feat. Sawano")

	// Flag both in _composite skip state so they appear on Tab 2.
	for _, cid := range []int64{compBackstageID, compMixedID} {
		input, err := s.ArtistForMatching(ctx, cid)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SaveArtistCompositeSkip(ctx, input, nil); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Split backstage-only composer credit.
	res1, err := s.ApplyCompositeCreditSplit(ctx, compBackstageID, []string{"ACE", "亀岡夏海"})
	if err != nil {
		t.Fatalf("backstage split failed: %v", err)
	}
	if res1.TracksUpdated != 1 || res1.AlbumsUpdated != 0 {
		t.Fatalf("unexpected backstage split result: %+v", res1)
	}
	if got := trackRoleArtistNames(t, s, track1ID, "composer"); !reflect.DeepEqual(got, []string{"ACE", "亀岡夏海"}) {
		t.Fatalf("composer credits = %v, want [ACE 亀岡夏海]", got)
	}
	var rem1 int
	_ = s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM track_artists WHERE artist_id=?)+(SELECT count(*) FROM album_artists WHERE artist_id=?)`, compBackstageID, compBackstageID).Scan(&rem1)
	if rem1 != 0 {
		t.Fatalf("expected 0 remaining refs for backstage composite, got %d", rem1)
	}

	// 2. Split mixed primary + lyricist + album_artist composite credit.
	res2, err := s.ApplyCompositeCreditSplit(ctx, compMixedID, []string{"Aimer", "澤野弘之"})
	if err != nil {
		t.Fatalf("mixed split failed: %v", err)
	}
	if res2.TracksUpdated != 1 || res2.AlbumsUpdated != 1 {
		t.Fatalf("unexpected mixed split result: %+v", res2)
	}
	if got := trackRoleArtistNames(t, s, track2ID, "primary"); !reflect.DeepEqual(got, []string{"Aimer", "澤野弘之"}) {
		t.Fatalf("primary credits = %v", got)
	}
	if got := trackRoleArtistNames(t, s, track2ID, "lyricist"); !reflect.DeepEqual(got, []string{"Aimer", "澤野弘之"}) {
		t.Fatalf("lyricist credits = %v", got)
	}
	if got := albumArtistNames(t, s, album2ID); !reflect.DeepEqual(got, []string{"Aimer", "澤野弘之"}) {
		t.Fatalf("album artists = %v", got)
	}

	// Both must be gone from Tab 2 review queue.
	_, creditsPage, err := s.PendingArtistReviewSplitPages(ctx, ArtistReviewFilter{Limit: 50}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if creditsPage.Total != 0 {
		t.Fatalf("expected 0 remaining credit review items, got %d (%+v)", creditsPage.Total, creditsPage.Items)
	}
}

func TestApplyCompositeCreditSplitRemoteSourcesNoResurrection(t *testing.T) {
	ctx := context.Background()
	s, lib := openCompositeSplitTestStore(t)

	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "V/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Sourced Track", Album: "VGM Album",
			Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"},
			DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	var trackID, albumID int64
	_ = s.db.QueryRowContext(ctx, `SELECT id,album_id FROM tracks WHERE title='Sourced Track'`).Scan(&trackID, &albumID)

	// Add a remote (non-file_tag) enrichment credit in track_artist_sources + track_artists.
	if err := s.AddEnrichmentCredits(ctx, albumID, []EnrichmentCredit{{
		TrackID: trackID, Role: "arranger", Name: "ARM, まろん", Position: 0, ExternalID: "vgmdb-1",
	}}, "vgmdb", 0); err != nil {
		t.Fatal(err)
	}
	compID := artistIDByName(t, s, "ARM, まろん")

	res, err := s.ApplyCompositeCreditSplit(ctx, compID, []string{"ARM", "まろん"})
	if err != nil {
		t.Fatalf("split remote-sourced credit failed: %v", err)
	}
	if res.TracksUpdated != 1 {
		t.Fatalf("expected 1 updated track, got %+v", res)
	}

	// Trigger restore_sourced_track_artist_after_delete must NOT have resurrected compID!
	if got := trackRoleArtistNames(t, s, trackID, "arranger"); !reflect.DeepEqual(got, []string{"ARM", "まろん"}) {
		t.Fatalf("arranger credits after split = %v, want [ARM まろん]", got)
	}
	var srcRemaining int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artist_sources WHERE artist_id=?`, compID).Scan(&srcRemaining)
	if srcRemaining != 0 {
		t.Fatalf("expected 0 remaining track_artist_sources for composite artist, got %d", srcRemaining)
	}
}

func TestApplyCompositeCreditSplitCanonicalDedupeAndValidation(t *testing.T) {
	ctx := context.Background()
	s, lib := openCompositeSplitTestStore(t)

	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "M/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Alias Song", Album: "Alias Album",
			Artists: []string{"Sawano", "Aimer feat. Sawano"}, AlbumArtists: []string{"Hiroyuki Sawano"},
			DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	sawanoAliasID := artistIDByName(t, s, "Sawano")
	sawanoCanonicalID := artistIDByName(t, s, "Hiroyuki Sawano")
	compID := artistIDByName(t, s, "Aimer feat. Sawano")

	// Merge "Sawano" -> "Hiroyuki Sawano".
	if _, err := s.MergeArtists(ctx, sawanoAliasID, sawanoCanonicalID); err != nil {
		t.Fatal(err)
	}

	// Self-referential split must fail.
	if _, err := s.ApplyCompositeCreditSplit(ctx, compID, []string{"Aimer feat. Sawano"}); !errors.Is(err, ErrInvalidCreditSplit) {
		t.Fatalf("expected ErrInvalidCreditSplit for self-reference, got %v", err)
	}
	// Empty parts must fail.
	if _, err := s.ApplyCompositeCreditSplit(ctx, compID, []string{"  "}); !errors.Is(err, ErrInvalidCreditSplit) {
		t.Fatalf("expected ErrInvalidCreditSplit for empty parts, got %v", err)
	}

	// Splitting into ["Aimer", "Sawano"] must canonicalize "Sawano" -> "Hiroyuki Sawano" and deduplicate without error.
	res, err := s.ApplyCompositeCreditSplit(ctx, compID, []string{"Aimer", "Sawano"})
	if err != nil {
		t.Fatalf("split with merged alias failed: %v", err)
	}
	if res.TracksUpdated != 1 {
		t.Fatalf("expected 1 updated track, got %+v", res)
	}
	var trackID int64
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE title='Alias Song'`).Scan(&trackID)
	if got := trackRoleArtistNames(t, s, trackID, "primary"); !reflect.DeepEqual(got, []string{"Hiroyuki Sawano", "Aimer"}) {
		t.Fatalf("primary credits after canonical dedupe = %v, want [Hiroyuki Sawano Aimer]", got)
	}

	// Re-applying on now-zero-ref composite artist returns ErrNoCompositeReferences.
	if _, err := s.ApplyCompositeCreditSplit(ctx, compID, []string{"Aimer", "Hiroyuki Sawano"}); !errors.Is(err, ErrNoCompositeReferences) {
		t.Fatalf("expected ErrNoCompositeReferences on second split, got %v", err)
	}
}

func TestImportTrackCompositeSplitReplay(t *testing.T) {
	ctx := context.Background()
	s, lib := openCompositeSplitTestStore(t)

	input1 := ImportInput{
		LibraryID: lib.ID, RelativePath: "R/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Replay Song 1", Album: "Replay Album",
			Artists:      []string{"ACAね, Mori Calliope"},
			AlbumArtists: []string{"ACAね, Mori Calliope"},
			Composer:     "ACE, 亀岡夏海",
			InvolvedPeople: []metadata.InvolvedPerson{
				{Role: "guitar", Name: "ACE, 亀岡夏海"},
			},
			DiscNumber: 1, TrackNumber: 1,
		},
	}
	if err := s.ImportTrack(ctx, input1); err != nil {
		t.Fatal(err)
	}
	compPrimaryID := artistIDByName(t, s, "ACAね, Mori Calliope")
	compComposerID := artistIDByName(t, s, "ACE, 亀岡夏海")

	if _, err := s.ApplyCompositeCreditSplit(ctx, compPrimaryID, []string{"ACAね", "Mori Calliope"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCompositeCreditSplit(ctx, compComposerID, []string{"ACE", "亀岡夏海"}); err != nil {
		t.Fatal(err)
	}

	// Full rescan of the same file with unmodified composite tags must preserve all splits!
	input1.ModifiedAtNS = 2
	if err := s.ImportTrack(ctx, input1); err != nil {
		t.Fatalf("rescan ImportTrack failed: %v", err)
	}
	var track1ID, albumID int64
	_ = s.db.QueryRowContext(ctx, `SELECT id,album_id FROM tracks WHERE title='Replay Song 1'`).Scan(&track1ID, &albumID)
	if got := trackRoleArtistNames(t, s, track1ID, "primary"); !reflect.DeepEqual(got, []string{"ACAね", "Mori Calliope"}) {
		t.Fatalf("rescan primary = %v", got)
	}
	if got := albumArtistNames(t, s, albumID); !reflect.DeepEqual(got, []string{"ACAね", "Mori Calliope"}) {
		t.Fatalf("rescan album_artists = %v", got)
	}
	if got := trackRoleArtistNames(t, s, track1ID, "composer"); !reflect.DeepEqual(got, []string{"ACE", "亀岡夏海"}) {
		t.Fatalf("rescan composer = %v", got)
	}
	if got := trackRoleArtistNames(t, s, track1ID, "instrument:guitar"); !reflect.DeepEqual(got, []string{"ACE", "亀岡夏海"}) {
		t.Fatalf("rescan instrument:guitar = %v", got)
	}

	// Newly imported track using the same composite credits also expands automatically.
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "R/02.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Replay Song 2", Album: "Replay Album",
			Artists: []string{"ACAね, Mori Calliope"}, AlbumArtists: []string{"ACAね, Mori Calliope"},
			Composer: "ACE, 亀岡夏海", DiscNumber: 1, TrackNumber: 2,
		},
	}); err != nil {
		t.Fatal(err)
	}
	var track2ID int64
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE title='Replay Song 2'`).Scan(&track2ID)
	if got := trackRoleArtistNames(t, s, track2ID, "primary"); !reflect.DeepEqual(got, []string{"ACAね", "Mori Calliope"}) {
		t.Fatalf("new track primary = %v", got)
	}
	if got := trackRoleArtistNames(t, s, track2ID, "composer"); !reflect.DeepEqual(got, []string{"ACE", "亀岡夏海"}) {
		t.Fatalf("new track composer = %v", got)
	}
}

func TestRevertAICreditSplitFullRestoreAndManualGuard(t *testing.T) {
	ctx := context.Background()
	s, lib := openCompositeSplitTestStore(t)

	// Two tracks: track1 will stay untouched after AI split; track2 will be manually edited before revert.
	for i, title := range []string{"Track One", "Track Two"} {
		if err := s.ImportTrack(ctx, ImportInput{
			LibraryID: lib.ID, RelativePath: "U/0" + string(rune('1'+i)) + ".flac", FileSize: 100, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title: title, Album: "Undo Album",
				Artists:      []string{"Alpha & Beta"},
				AlbumArtists: []string{"Alpha & Beta"},
				Composer:     "Alpha & Beta",
				DiscNumber: 1, TrackNumber: i + 1,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var track1ID, track2ID, albumID int64
	_ = s.db.QueryRowContext(ctx, `SELECT id,album_id FROM tracks WHERE title='Track One'`).Scan(&track1ID, &albumID)
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM tracks WHERE title='Track Two'`).Scan(&track2ID)
	compID := artistIDByName(t, s, "Alpha & Beta")

	if err := s.AddEnrichmentCredits(ctx, albumID, []EnrichmentCredit{{
		TrackID: track1ID, Role: "arranger", Name: "Alpha & Beta", Position: 0, ExternalID: "vgmdb-undo",
	}}, "vgmdb", 0); err != nil {
		t.Fatal(err)
	}
	input, _ := s.ArtistForMatching(ctx, compID)
	_ = s.SaveArtistCompositeSkip(ctx, input, nil)

	// Execute AI split and save applied AI decision.
	splitRes, snapRaw, err := s.ApplyCompositeCreditSplitByAI(ctx, compID, []string{"Alpha", "Beta"})
	if err != nil {
		t.Fatalf("ApplyCompositeCreditSplitByAI failed: %v", err)
	}
	if splitRes.TracksUpdated != 2 || splitRes.AlbumsUpdated != 1 {
		t.Fatalf("unexpected split result: %+v", splitRes)
	}
	payload, _ := json.Marshal(map[string]any{"isComposite": true, "parts": []string{"Alpha", "Beta"}})
	decID, err := s.SaveAIReviewDecision(ctx, AIReviewDecision{
		TargetKind:      AITargetCreditSplit,
		TargetID:        compID,
		Fingerprint:     "fp-1",
		Action:          AIActionAutoConfirm,
		Payload:         payload,
		Confidence:      99,
		Reason:          "test auto split",
		Applied:         true,
		AppliedSnapshot: snapRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.LinkCreditSplitDecision(ctx, compID, decID)

	// Manually edit Track Two's primary artist to ["Manual Solo"] before reverting.
	tx, _ := s.db.BeginTx(ctx, nil)
	manualIDs, _ := ensureArtists(ctx, tx, []string{"Manual Solo"})
	_ = tx.Commit()
	corr2, err := s.PrimaryArtistCorrection(ctx, track2ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPrimaryArtistCorrection(ctx, track2ID, corr2.Token, manualIDs, false); err != nil {
		t.Fatal(err)
	}

	// Revert the AI credit split decision.
	if err := s.RevertAIReviewDecision(ctx, decID); err != nil {
		t.Fatalf("RevertAIReviewDecision(credit_split) failed: %v", err)
	}

	// Track One primary, composer, arranger (remote), and album_artists must be restored to "Alpha & Beta".
	if got := trackRoleArtistNames(t, s, track1ID, "primary"); !reflect.DeepEqual(got, []string{"Alpha & Beta"}) {
		t.Fatalf("reverted track1 primary = %v, want [Alpha & Beta]", got)
	}
	if got := trackRoleArtistNames(t, s, track1ID, "composer"); !reflect.DeepEqual(got, []string{"Alpha & Beta"}) {
		t.Fatalf("reverted track1 composer = %v, want [Alpha & Beta]", got)
	}
	if got := trackRoleArtistNames(t, s, track1ID, "arranger"); !reflect.DeepEqual(got, []string{"Alpha & Beta"}) {
		t.Fatalf("reverted track1 arranger = %v, want [Alpha & Beta]", got)
	}
	if got := albumArtistNames(t, s, albumID); !reflect.DeepEqual(got, []string{"Alpha & Beta"}) {
		t.Fatalf("reverted album_artists = %v, want [Alpha & Beta]", got)
	}
	// Track Two's manual primary edit must NOT have been clobbered!
	if got := trackRoleArtistNames(t, s, track2ID, "primary"); !reflect.DeepEqual(got, []string{"Manual Solo"}) {
		t.Fatalf("manual edit on track2 primary was overwritten: got %v, want [Manual Solo]", got)
	}

	// Split rule must be marked reverted, and artist must return to Tab 2 review queue.
	var ruleStatus string
	_ = s.db.QueryRowContext(ctx, `SELECT status FROM artist_credit_splits WHERE composite_artist_id=?`, compID).Scan(&ruleStatus)
	if ruleStatus != "reverted" {
		t.Fatalf("artist_credit_splits status = %q, want reverted", ruleStatus)
	}
	_, creditsPage, err := s.PendingArtistReviewSplitPages(ctx, ArtistReviewFilter{Limit: 50}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if creditsPage.Total != 1 || creditsPage.Items[0].Artist.ID != compID {
		t.Fatalf("expected reverted composite artist back on Tab 2, got %+v", creditsPage)
	}
}

func TestCompositeArtistExemption(t *testing.T) {
	ctx := context.Background()
	s, lib := openCompositeSplitTestStore(t)

	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "SG/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "The Sound of Silence", Album: "Sounds of Silence",
			Artists: []string{"Simon & Garfunkel"}, AlbumArtists: []string{"Simon & Garfunkel"},
			DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	sgID := artistIDByName(t, s, "Simon & Garfunkel")
	input, err := s.ArtistForMatching(ctx, sgID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveArtistCompositeSkip(ctx, input, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceArtistCandidatesForSources(ctx, sgID, []ArtistCandidate{{
		Source: "musicbrainz", ExternalID: "mb-sg", MBID: "mb-sg", DisplayName: "Simon & Garfunkel", Score: 98,
	}}, []string{"musicbrainz"}); err != nil {
		t.Fatal(err)
	}
	cands, _ := s.ArtistCandidates(ctx, sgID)
	candID := cands[0].ID

	// Before exemption: blocked on Tab 2, cannot confirm identity, classified as composite in FilterArtistsForScan.
	if err := s.ConfirmArtistCandidate(ctx, sgID, candID); !errors.Is(err, ErrCompositeArtistIdentity) {
		t.Fatalf("expected ErrCompositeArtistIdentity before exemption, got %v", err)
	}
	idPage, credPage, err := s.PendingArtistReviewSplitPages(ctx, ArtistReviewFilter{Limit: 50}, 50, 0)
	if err != nil || idPage.Total != 0 || credPage.Total != 1 {
		t.Fatalf("before exemption split pages = id:%d cred:%d err=%v", idPage.Total, credPage.Total, err)
	}

	// Exempt as single artist.
	if err := s.ExemptCompositeArtist(ctx, sgID, "official duo"); err != nil {
		t.Fatalf("ExemptCompositeArtist failed: %v", err)
	}
	if isComp, err := s.IsCompositeArtist(ctx, sgID, "Simon & Garfunkel"); err != nil || isComp {
		t.Fatalf("IsCompositeArtist after exemption = %v err=%v, want false", isComp, err)
	}

	// Now moves from Tab 2 (credits) to Tab 1 (identity) and becomes eligible for normal scan & binding!
	idPage, credPage, err = s.PendingArtistReviewSplitPages(ctx, ArtistReviewFilter{Limit: 50}, 50, 0)
	if err != nil || idPage.Total != 1 || credPage.Total != 0 {
		t.Fatalf("after exemption split pages = id:%d cred:%d err=%v", idPage.Total, credPage.Total, err)
	}
	eligible, composites, err := s.FilterArtistsForScan(ctx, []ArtistMatchInput{input}, []MetadataSourceSetting{{Source: "musicbrainz", Enabled: true, AutoMatch: true, CacheDays: 30}}, ArtistScanIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if len(composites) != 0 {
		t.Fatalf("exempted artist must not be classified as composite in FilterArtistsForScan, got %+v", composites)
	}
	_ = eligible

	// Identity confirmation now succeeds cleanly!
	if err := s.ConfirmArtistCandidate(ctx, sgID, candID); err != nil {
		t.Fatalf("ConfirmArtistCandidate after exemption failed: %v", err)
	}
	if extID, err := s.ArtistExternalID(ctx, sgID, "musicbrainz"); err != nil || extID != "mb-sg" {
		t.Fatalf("expected bound mbid mb-sg after exemption, got %q err=%v", extID, err)
	}
}
