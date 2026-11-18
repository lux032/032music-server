package storage

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestArtistIdentityConflictMergeAndRollback(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, track, _, source := seedEnrichmentEntities(t, s, ctx)
	result, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Owner','owner','owner')`)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := result.LastInsertId()
	if _, err = s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(?,?,'primary')`, track, source); err != nil {
		t.Fatal(err)
	}
	if err = s.UpsertExternalArtistProfile(ctx, owner, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "id", DisplayName: "Owner"}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceArtistCandidates(ctx, source, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "id", DisplayName: "Owner", Score: 100}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := s.ArtistCandidates(ctx, source)
	candidate := candidates[0].ID
	var conflict *ArtistExternalIDConflictError
	if err = s.ConfirmArtistCandidate(ctx, source, candidate); !errors.As(err, &conflict) || conflict.OwnerArtistID != owner {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
	current, _ := s.ArtistCandidates(ctx, source)
	if current[0].Status != "candidate" {
		t.Fatal("conflict changed status")
	}
	if _, err = s.MergeArtistsForIdentityConflict(ctx, source, candidate, source); !errors.Is(err, ErrIdentityConflictStale) {
		t.Fatal(err)
	}
	op, err := s.MergeArtistsForIdentityConflict(ctx, source, candidate, owner)
	if err != nil {
		t.Fatal(err)
	}
	current, _ = s.ArtistCandidates(ctx, source)
	if current[0].Status != "candidate" {
		t.Fatal("merge confirmed candidate")
	}
	profileOwner, _ := s.ExternalProfileOwner(ctx, "musicbrainz", "id")
	if profileOwner != owner {
		t.Fatal("identity moved")
	}
	var credited int64
	if err = s.db.QueryRowContext(ctx, `SELECT artist_id FROM track_artists WHERE track_id=?`, track).Scan(&credited); err != nil || credited != owner {
		t.Fatalf("credit=%d err=%v", credited, err)
	}
	if err = s.RollbackArtistMerge(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT artist_id FROM track_artists WHERE track_id=?`, track).Scan(&credited); err != nil || credited != source {
		t.Fatalf("rollback=%d err=%v", credited, err)
	}
	// A merged-away owner must link its canonical target, but cannot merge
	// into the source itself. A changed canonical target invalidates old forms.
	r, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Canonical','canonical','canonical')`)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := r.LastInsertId()
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=? WHERE id=?`, canonical, owner); err != nil {
		t.Fatal(err)
	}
	live, err := s.ArtistIdentityConflict(ctx, source, candidate)
	if err != nil || live.TargetArtistID != canonical || !live.CanMerge {
		t.Fatalf("canonical conflict=%+v err=%v", live, err)
	}
	if _, err = s.MergeArtistsForIdentityConflict(ctx, source, candidate, owner); !errors.Is(err, ErrIdentityConflictStale) {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=? WHERE id=?`, source, owner); err != nil {
		t.Fatal(err)
	}
	live, err = s.ArtistIdentityConflict(ctx, source, candidate)
	if err != nil || live.CanMerge {
		t.Fatalf("self conflict=%+v err=%v", live, err)
	}
	if _, err = s.MergeArtistsForIdentityConflict(ctx, source, candidate, source); !errors.Is(err, ErrIdentityConflictStale) {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=NULL WHERE id=?`, owner); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"rejected", "confirmed"} {
		if _, err = s.db.ExecContext(ctx, `UPDATE artist_match_candidates SET status=? WHERE id=?`, status, candidate); err != nil {
			t.Fatal(err)
		}
		if _, err = s.MergeArtistsForIdentityConflict(ctx, source, candidate, owner); !errors.Is(err, ErrIdentityConflictStale) {
			t.Fatalf("%s: %v", status, err)
		}
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artist_match_candidates SET status='candidate' WHERE id=?`, candidate); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM artist_external_profiles WHERE artist_id=?`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MergeArtistsForIdentityConflict(ctx, source, candidate, owner); !errors.Is(err, ErrIdentityConflictStale) {
		t.Fatal(err)
	}
	if err = s.ConfirmArtistCandidate(ctx, source, candidate); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfirmArtistCandidate(ctx, source, candidate); err != nil {
		t.Fatal("repeat", err)
	}
}

func TestArtistTaggedMBIDConservativeSingleAndBatch(t *testing.T) {
	const id = "abcdef12-1234-4234-8234-abcdef123456"
	const other = "22222222-2222-4222-8222-222222222222"
	for _, tc := range []struct {
		name, role  string
		values      []string
		extraArtist bool
		want        string
	}{
		{"single", "primary", []string{id}, false, id},
		{"uppercase", "primary", []string{"ABCDEF12-1234-4234-8234-ABCDEF123456"}, false, id},
		{"composer", "composer", []string{id}, false, ""},
		{"arranger", "arranger", []string{id}, false, ""},
		{"invalid", "primary", []string{"not-an-id"}, false, ""},
		{"multi-values", "primary", []string{id, other}, false, ""},
		{"mp4", "primary", []string{id + ";" + other}, false, ""},
		{"multi-artists", "primary", []string{id}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx := openEnrichmentTestStore(t)
			album, track, _, artist := seedEnrichmentEntities(t, s, ctx)
			if _, err := s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(?,?,?)`, track, artist, tc.role); err != nil {
				t.Fatal(err)
			}
			if tc.extraArtist {
				result, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Other','other','other')`)
				if err != nil {
					t.Fatal(err)
				}
				otherArtist, _ := result.LastInsertId()
				if _, err = s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,1,'primary')`, track, otherArtist); err != nil {
					t.Fatal(err)
				}
			}
			var library int64
			if err := s.db.QueryRowContext(ctx, `SELECT library_id FROM albums WHERE id=?`, album).Scan(&library); err != nil {
				t.Fatal(err)
			}
			r, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(?,?,'song.flac',1,1)`, library, track)
			if err != nil {
				t.Fatal(err)
			}
			file, _ := r.LastInsertId()
			for i, value := range tc.values {
				if _, err = s.db.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) VALUES(?,'MUSICBRAINZ_ARTISTID',?,?)`, file, value, i); err != nil {
					t.Fatal(err)
				}
			}
			check := func(want string) {
				t.Helper()
				single, err := s.ArtistForMatching(ctx, artist)
				if err != nil || single.TaggedMBID != want {
					t.Fatalf("single=%+v err=%v want=%s", single, err, want)
				}
				batch, err := s.ArtistsForMatching(ctx)
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range batch {
					if a.ID == artist && a.TaggedMBID != want {
						t.Fatalf("batch=%+v", a)
					}
				}
			}
			check(tc.want)
			if tc.want != "" {
				r, err = s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(?,?,?,1,1)`, library, track, fmt.Sprintf("%d-copy.flac", file))
				if err != nil {
					t.Fatal(err)
				}
				copyFile, _ := r.LastInsertId()
				if _, err = s.db.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value) VALUES(?,'MUSICBRAINZ ARTIST ID',?)`, copyFile, other); err != nil {
					t.Fatal(err)
				}
				check("")
			}
		})
	}
}

func TestCreatorTaggedMBIDAlignmentAndOverrides(t *testing.T) {
	const singerID = "11111111-1111-4111-8111-111111111111"
	const comp1ID = "22222222-2222-4222-8222-222222222222"
	const comp2ID = "33333333-3333-4333-8333-333333333333"
	const lyrID = "44444444-4444-4444-8444-444444444444"
	const arrID = "55555555-5555-4555-8555-555555555555"

	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	err = s.ImportTrack(ctx, ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "Album/01.flac",
		FileSize:     100,
		ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title:        "Song",
			Album:        "Album",
			Artists:      []string{"Singer"},
			AlbumArtists: []string{"Singer"},
			Composer:     "Comp One, Comp Two",
			Lyricist:     "Lyricist One",
			Arranger:     "Arranger One",
			DiscNumber:   1,
			TrackNumber:  1,
			Raw: map[string][]string{
				"MUSICBRAINZ_ARTISTID":   {singerID},
				"COMPOSERS":              {"Comp One; Comp Two"},
				"MUSICBRAINZ_COMPOSERID": {comp1ID + "; " + comp2ID},
				"LYRICISTS":              {"Lyricist One"},
				"MUSICBRAINZ_LYRICISTID": {lyrID},
				"ARRANGERS":              {"Arranger One"},
				"MUSICBRAINZ_ARRANGERID": {arrID},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	all, err := s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		got[a.Name] = a.TaggedMBID
		single, err := s.ArtistForMatching(ctx, a.ID)
		if err != nil || single.TaggedMBID != a.TaggedMBID {
			t.Fatalf("single=%+v batch=%+v err=%v", single, a, err)
		}
	}
	want := map[string]string{
		"Singer":       singerID,
		"Comp One":     comp1ID,
		"Comp Two":     comp2ID,
		"Lyricist One": lyrID,
		"Arranger One": arrID,
	}
	for name, wantMBID := range want {
		if got[name] != wantMBID {
			t.Fatalf("%s TaggedMBID=%q want %q (all=%v)", name, got[name], wantMBID, got)
		}
	}

	// Primary artist override suppresses primary identity evidence, but keeps
	// verified creator MBIDs on the same file.
	var trackID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM tracks LIMIT 1`).Scan(&trackID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_primary_artist_state(track_id,active) VALUES(?,1)`, trackID); err != nil {
		t.Fatal(err)
	}
	all, err = s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gotAfterOverride := map[string]string{}
	for _, a := range all {
		gotAfterOverride[a.Name] = a.TaggedMBID
	}
	if gotAfterOverride["Singer"] != "" || gotAfterOverride["Comp One"] != comp1ID || gotAfterOverride["Arranger One"] != arrID {
		t.Fatalf("unexpected override result: %v", gotAfterOverride)
	}
}

func TestCreatorCrossRoleConflictAndPrimaryOverrideIsolation(t *testing.T) {
	const idX = "11111111-1111-4111-8111-111111111111"
	const idY = "22222222-2222-4222-8222-222222222222"
	const idZ = "33333333-3333-4333-8333-333333333333"

	// Case 1: Cross-role conflict (A is X in primary A;B and Z in single composer A)
	// must NOT fall back to single-composer Z.
	s, ctx := openEnrichmentTestStore(t)
	_ = s.EnsureLibrary(ctx, "Test", "/music")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "01.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "S1", Album: "A1", Artists: []string{"ArtistA", "ArtistB"}, AlbumArtists: []string{"ArtistA"},
			Composer: "ArtistA", DiscNumber: 1, TrackNumber: 1,
			Raw: map[string][]string{
				"ARTISTS":                {"ArtistA; ArtistB"},
				"MUSICBRAINZ_ARTISTID":   {idX + "; " + idY},
				"COMPOSERS":              {"ArtistA"},
				"MUSICBRAINZ_COMPOSERID": {idZ},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	byName := func() map[string]string {
		all, err := s.ArtistsForMatching(ctx)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, a := range all {
			m[a.Name] = a.TaggedMBID
		}
		return m
	}
	got := byName()
	if got["ArtistA"] != "" || got["ArtistB"] != idY {
		t.Fatalf("cross-role conflict leaked: %v", got)
	}

	// Case 2: After primary artist override, ArtistA's primary conflict is suppressed
	// so ArtistA's composer identity Z can take effect, while another file's
	// overridden primary X cannot borrow composer C's X via instr().
	var t1 int64
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM tracks LIMIT 1`).Scan(&t1)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_primary_artist_state(track_id,active) VALUES(?,1)`, t1); err != nil {
		t.Fatal(err)
	}
	got = byName()
	if got["ArtistA"] != idZ || got["ArtistB"] != "" {
		t.Fatalf("after override expected ArtistA=Z ArtistB='': %v", got)
	}

	// Track 2: primary P1; P2 -> X; Y, composer C -> X, then override Track 2 primary.
	// Neither P1 nor P2 may get X!
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "02.flac", FileSize: 2, ModifiedAtNS: 2,
		Metadata: metadata.AudioMetadata{
			Title: "S2", Album: "A2", Artists: []string{"P1", "P2"}, AlbumArtists: []string{"P1"},
			Composer: "CompC", DiscNumber: 1, TrackNumber: 1,
			Raw: map[string][]string{
				"ARTISTS":                {"P1; P2"},
				"MUSICBRAINZ_ARTISTID":   {idX + "; " + idY},
				"COMPOSERS":              {"CompC"},
				"MUSICBRAINZ_COMPOSERID": {idX},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var t2 int64
	_ = s.db.QueryRowContext(ctx, `SELECT track_id FROM audio_files WHERE relative_path='02.flac'`).Scan(&t2)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_primary_artist_state(track_id,active) VALUES(?,1)`, t2); err != nil {
		t.Fatal(err)
	}
	got = byName()
	if got["P1"] != "" || got["P2"] != "" || got["CompC"] != idX {
		t.Fatalf("primary override bypassed via composer X: %v", got)
	}
}

func TestCreatorDuplicateAndLegacyGuessBlockedFromMBID(t *testing.T) {
	const idX = "11111111-1111-4111-8111-111111111111"
	const idY = "22222222-2222-4222-8222-222222222222"
	s, ctx := openEnrichmentTestStore(t)
	_ = s.EnsureLibrary(ctx, "Test", "/music")
	lib, _ := s.LibraryByRoot(ctx, "/music")

	// Duplicate raw name slot "DupA; DupA; DupB" with 2 IDs must not misalign.
	if err := s.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "dup.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Dup", Album: "Alb", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"},
			Composer: "DupA, DupB", DiscNumber: 1, TrackNumber: 1,
			Raw: map[string][]string{
				"COMPOSERS":              {"DupA; DupA; DupB"},
				"MUSICBRAINZ_COMPOSERID": {idX + "; " + idY},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// LegacyCreatorConversion comma guess must never align MBIDs (nor on rescan).
	legacyInput := ImportInput{
		LibraryID: lib.ID, RelativePath: "legacy.flac", FileSize: 2, ModifiedAtNS: 2, LegacyCreatorConversion: true,
		Metadata: metadata.AudioMetadata{
			Title: "Leg", Album: "Alb", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"},
			Composer: "LegA, LegB", DiscNumber: 1, TrackNumber: 2,
			Raw: map[string][]string{
				"COMPOSER":               {"LegA, LegB"},
				"MUSICBRAINZ_COMPOSERID": {idX + "; " + idY},
			},
		},
	}
	if err := s.ImportTrack(ctx, legacyInput); err != nil {
		t.Fatal(err)
	}
	legacyInput.LegacyCreatorConversion = false
	if err := s.ImportTrack(ctx, legacyInput); err != nil {
		t.Fatal(err)
	}
	all, err := s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range all {
		if a.Name != "Singer" && a.TaggedMBID != "" {
			t.Fatalf("unexpected TaggedMBID for %s: %s", a.Name, a.TaggedMBID)
		}
	}
}

func TestArtistTaggedMBIDQueryPlansCoverAllProductionSQL(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	explain := func(query string, args ...any) string {
		t.Helper()
		rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var plans []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err = rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			plans = append(plans, detail)
		}
		return strings.Join(plans, "\n")
	}

	singleEvidence := explain(taggedIdentityQuery(true), 1)
	if !strings.Contains(singleEvidence, "idx_file_artist_identity_artist") || strings.Contains(singleEvidence, "USE TEMP B-TREE") {
		t.Fatalf("unexpected singleEvidence plan:\n%s", singleEvidence)
	}
	singleLegacy := explain(legacyTaggedIdentityQuery(true), 1)
	if !strings.Contains(singleLegacy, "idx_track_artists_artist") || strings.Contains(singleLegacy, "USE TEMP B-TREE") {
		t.Fatalf("unexpected singleLegacy plan:\n%s", singleLegacy)
	}
	batchEvidence := explain(taggedIdentityQuery(false))
	if strings.Contains(batchEvidence, "USE TEMP B-TREE") {
		t.Fatalf("unexpected temp sort in batchEvidence:\n%s", batchEvidence)
	}
	batchLegacy := explain(legacyTaggedIdentityQuery(false))
	if !strings.Contains(batchLegacy, "idx_audio_file_tags_field_value") || strings.Contains(batchLegacy, "USE TEMP B-TREE") {
		t.Fatalf("unexpected batchLegacy plan:\n%s", batchLegacy)
	}
	singleReverify := explain(reverifyIdentityQuery(true), 1)
	if !strings.Contains(singleReverify, "idx_file_artist_reverify_artist") || strings.Contains(singleReverify, "USE TEMP B-TREE") {
		t.Fatalf("unexpected singleReverify plan:\n%s", singleReverify)
	}
	batchReverify := explain(reverifyIdentityQuery(false))
	if strings.Contains(batchReverify, "USE TEMP B-TREE") {
		t.Fatalf("unexpected temp sort in batchReverify:\n%s", batchReverify)
	}
}

func TestLegacyMergeCannotTransferFileMBIDAnd074MigrationClassifiesRoles(t *testing.T) {
	const idX = "11111111-1111-4111-8111-111111111111"
	s, ctx := openEnrichmentTestStore(t)
	album, track, _, artistA := seedEnrichmentEntities(t, s, ctx)
	var lib int64
	_ = s.db.QueryRowContext(ctx, `SELECT library_id FROM albums WHERE id=?`, album).Scan(&lib)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(?,?,'composer')`, track, artistA); err != nil {
		t.Fatal(err)
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(?,?,'01.flac',1,1)`, lib, track)
	if err != nil {
		t.Fatal(err)
	}
	fileID, _ := r.LastInsertId()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) VALUES(?,'MUSICBRAINZ_COMPOSERID',?,0)`, fileID, idX); err != nil {
		t.Fatal(err)
	}
	resB, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('TargetB','targetb','targetb')`)
	if err != nil {
		t.Fatal(err)
	}
	artistB, _ := resB.LastInsertId()
	if _, err := s.MergeArtists(ctx, artistA, artistB); err != nil {
		t.Fatal(err)
	}
	gotB, err := s.ArtistForMatching(ctx, artistB)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.TaggedMBID != "" {
		t.Fatalf("merged target unexpectedly inherited legacy file MBID: %+v", gotB)
	}

	// Verify 074+075 migrations reclassify unambiguous pre-074 rows, quarantine merged/ambiguous rows, and backfill review flags.
	resC, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('PureCompC','purecompc','purecompc')`)
	if err != nil {
		t.Fatal(err)
	}
	artistC, _ := resC.LastInsertId()
	tr2, err := s.db.ExecContext(ctx, `INSERT INTO tracks(album_id,disc_number,track_number,title,sort_title) VALUES(?,1,2,'Track 2','track 2')`, album)
	if err != nil {
		t.Fatal(err)
	}
	track2, _ := tr2.LastInsertId()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(?,?,'composer')`, track2, artistC); err != nil {
		t.Fatal(err)
	}
	af2, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(?,?,'02.flac',1,1)`, lib, track2)
	if err != nil {
		t.Fatal(err)
	}
	file2ID, _ := af2.LastInsertId()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) VALUES(?,'MUSICBRAINZ_COMPOSERID',?,0)`, file2ID, idX); err != nil {
		t.Fatal(err)
	}
	// Simulate 073 state by rolling back 074 and 075 schema, inserting 073 data, and re-running Migrate.
	if _, err := s.db.ExecContext(ctx, `DROP TABLE audio_file_artist_reverify; DROP TABLE audio_file_identity_checks; DROP TABLE audio_file_artist_identities; CREATE TABLE audio_file_artist_identities (audio_file_id INTEGER NOT NULL, artist_id INTEGER NOT NULL, mbid TEXT NOT NULL, PRIMARY KEY(audio_file_id, artist_id)) STRICT; ALTER TABLE artist_match_candidates DROP COLUMN requires_manual_review; DELETE FROM schema_migrations WHERE version>=74`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_artist_identities(audio_file_id,artist_id,mbid) VALUES(?,?,?),(?,?,?)`, fileID, artistB, idX, file2ID, artistC, idX); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artist_match_candidates(artist_id,source,external_id,display_name,score,evidence_json,payload_json,status) VALUES(?,'musicbrainz',?,?,85,'["标签身份与歌手名称不一致，需要人工核对"]','{}','candidate')`, artistB, idX, "Wrong"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var countB int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_file_artist_identities WHERE audio_file_id=? AND artist_id=? AND status='valid'`, fileID, artistB).Scan(&countB); err != nil || countB != 0 {
		t.Fatalf("merged fileID/artistB unexpectedly kept valid row: count=%d err=%v", countB, err)
	}
	var migratedRole, migratedStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT role,status FROM audio_file_artist_identities WHERE audio_file_id=? AND artist_id=?`, file2ID, artistC).Scan(&migratedRole, &migratedStatus); err != nil {
		t.Fatal(err)
	}
	if migratedRole != "composer" || migratedStatus != "valid" {
		t.Fatalf("expected composer/valid after 074+075, got %s/%s", migratedRole, migratedStatus)
	}
	cands, err := s.ArtistCandidates(ctx, artistB)
	if err != nil || len(cands) != 1 || !cands[0].RequiresManualReview {
		t.Fatalf("expected RequiresManualReview backfilled on 074+075, got %+v err=%v", cands, err)
	}
	if bound, err := s.AutoBindArtistCandidate(ctx, artistB, ExternalArtistProfile{Source: "musicbrainz", ExternalID: idX, DisplayName: "Wrong"}); err != nil || bound {
		t.Fatalf("AutoBindArtistCandidate must reject RequiresManualReview=true, got bound=%v err=%v", bound, err)
	}
}

func TestMigration075Fixes26f747bMisclassifiedRolesAndPrimaryOverride(t *testing.T) {
	const (
		idX = "11111111-1111-4111-8111-111111111111"
		idY = "22222222-2222-4222-8222-222222222222"
	)
	s, ctx := openEnrichmentTestStore(t)
	album, track, _, artistA := seedEnrichmentEntities(t, s, ctx)
	var lib int64
	_ = s.db.QueryRowContext(ctx, `SELECT library_id FROM albums WHERE id=?`, album).Scan(&lib)
	resB, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Comp B','comp b','compb')`)
	if err != nil {
		t.Fatal(err)
	}
	artistB, _ := resB.LastInsertId()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(?,?,'primary'),(?,?,'composer'),(?,?,'composer')`, track, artistA, track, artistA, track, artistB); err != nil {
		t.Fatal(err)
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(?,?,'01.flac',1,1)`, lib, track)
	if err != nil {
		t.Fatal(err)
	}
	fileID, _ := r.LastInsertId()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) VALUES(?,'MUSICBRAINZ_ARTISTID',?,0),(?,'MUSICBRAINZ_COMPOSERID',?,0)`, fileID, idX, fileID, idY+"; "+idX); err != nil {
		t.Fatal(err)
	}

	// Simulate buggy 26f747b 074 state: false composer/valid + spurious arranger/conflict + unflagged candidate.
	if _, err := s.db.ExecContext(ctx, `DROP TABLE audio_file_artist_reverify; DROP TABLE audio_file_identity_checks; DELETE FROM schema_migrations WHERE version>=75`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_artist_identities(audio_file_id,artist_id,role,mbid,status) VALUES(?,?,'primary',?,'valid'),(?,?,'composer',?,'valid'),(?,?,'arranger','','conflict')`, fileID, artistA, idX, fileID, artistA, idX, fileID, artistA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artist_match_candidates(artist_id,source,external_id,display_name,score,evidence_json,payload_json,status,requires_manual_review) VALUES(?,'musicbrainz',?,?,80,'["需要人工核对"]','{}','candidate',0)`, artistA, idX, "Candidate A"); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var rows int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_file_artist_identities WHERE audio_file_id=? AND artist_id=?`, fileID, artistA).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("expected only primary/valid to survive 075, count=%d err=%v", rows, err)
	}
	var survivingRole, survivingStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT role,status FROM audio_file_artist_identities WHERE audio_file_id=? AND artist_id=?`, fileID, artistA).Scan(&survivingRole, &survivingStatus); err != nil || survivingRole != "primary" || survivingStatus != "valid" {
		t.Fatalf("expected primary/valid after 075, got %s/%s err=%v", survivingRole, survivingStatus, err)
	}
	var compCheck string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM audio_file_identity_checks WHERE audio_file_id=? AND role='composer'`, fileID).Scan(&compCheck); err != nil || compCheck != "blocked" {
		t.Fatalf("expected composer check blocked, got %q err=%v", compCheck, err)
	}
	cands, err := s.ArtistCandidates(ctx, artistA)
	if err != nil || len(cands) != 1 || !cands[0].RequiresManualReview {
		t.Fatalf("expected 075 to backfill RequiresManualReview, got %+v err=%v", cands, err)
	}
	beforeOverride, err := s.ArtistForMatching(ctx, artistA)
	if err != nil || beforeOverride.TaggedMBID != idX {
		t.Fatalf("expected valid primary TaggedMBID before override, got %+v err=%v", beforeOverride, err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_primary_artist_state(track_id,active) VALUES(?,1)`, track); err != nil {
		t.Fatal(err)
	}
	afterOverride, err := s.ArtistForMatching(ctx, artistA)
	if err != nil || afterOverride.TaggedMBID != "" {
		t.Fatalf("primary override resurrected misclassified composer MBID: %+v err=%v", afterOverride, err)
	}
}

func TestUnsafeIdentityBlocksInvolvedPeopleFallbackViaIdentityChecks(t *testing.T) {
	const idX = "11111111-1111-4111-8111-111111111111"
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	meta := metadata.AudioMetadata{
		Title:          "Corrupt Ogg Track",
		Album:          "Album",
		DiscNumber:     1,
		TrackNumber:    1,
		Artists:        []string{"Singer"},
		Container:      "ogg",
		UnsafeIdentity: true,
		InvolvedPeople: []metadata.InvolvedPerson{{Role: "written by", Name: "Writer One"}},
		Raw: map[string][]string{
			"TIPL:written by":        {"Writer One"},
			"MUSICBRAINZ_LYRICISTID": {idX},
		},
	}
	if err := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: "bad.ogg", FileSize: 10, ModifiedAtNS: 10, Metadata: meta}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ArtistsForMatching(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var writerID int64
	for _, a := range all {
		if a.Name == "Writer One" {
			writerID = a.ID
			if a.TaggedMBID != "" {
				t.Fatalf("batch ArtistsForMatching leaked MBID for UnsafeIdentity TIPL writer: %+v", a)
			}
		}
	}
	if writerID == 0 {
		t.Fatal("expected Writer One artist to be materialized from TIPL")
	}
	single, err := s.ArtistForMatching(ctx, writerID)
	if err != nil {
		t.Fatal(err)
	}
	if single.TaggedMBID != "" {
		t.Fatalf("single ArtistForMatching leaked MBID for UnsafeIdentity TIPL writer: %+v", single)
	}
}

func TestMigration074And075PreserveCrossFileConflictsUntilRescan(t *testing.T) {
	const (
		idX = "11111111-1111-4111-8111-111111111111"
		idY = "22222222-2222-4222-8222-222222222222"
		idZ = "33333333-3333-4333-8333-333333333333"
	)
	for _, path := range []string{"073_to_075", "buggy_074_to_075"} {
		t.Run(path, func(t *testing.T) {
			s, ctx := openEnrichmentTestStore(t)
			album, track1, _, artistA := seedEnrichmentEntities(t, s, ctx)
			var lib int64
			_ = s.db.QueryRowContext(ctx, `SELECT library_id FROM albums WHERE id=?`, album).Scan(&lib)
			resB, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Collab B','collab b','collabb')`)
			if err != nil {
				t.Fatal(err)
			}
			artistB, _ := resB.LastInsertId()
			tr2, err := s.db.ExecContext(ctx, `INSERT INTO tracks(album_id,disc_number,track_number,title,sort_title) VALUES(?,1,2,'Track 2','track 2')`, album)
			if err != nil {
				t.Fatal(err)
			}
			track2, _ := tr2.LastInsertId()
			if _, err := s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,role) VALUES(?,?,'primary'),(?,?,'primary'),(?,?,'primary')`, track1, artistA, track2, artistA, track2, artistB); err != nil {
				t.Fatal(err)
			}
			af1, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(?,?,'01.flac',1,1)`, lib, track1)
			if err != nil {
				t.Fatal(err)
			}
			file1, _ := af1.LastInsertId()
			af2, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(?,?,'02.flac',1,1)`, lib, track2)
			if err != nil {
				t.Fatal(err)
			}
			file2, _ := af2.LastInsertId()
			if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) VALUES(?,'MUSICBRAINZ_ARTISTID',?,0),(?,'MUSICBRAINZ_ARTISTID',?,0)`, file1, idX, file2, idY+"; "+idZ); err != nil {
				t.Fatal(err)
			}

			if path == "073_to_075" {
				if _, err := s.db.ExecContext(ctx, `DROP TABLE audio_file_artist_reverify; DROP TABLE audio_file_identity_checks; DROP TABLE audio_file_artist_identities; CREATE TABLE audio_file_artist_identities (audio_file_id INTEGER NOT NULL, artist_id INTEGER NOT NULL, mbid TEXT NOT NULL, PRIMARY KEY(audio_file_id, artist_id)) STRICT; ALTER TABLE artist_match_candidates DROP COLUMN requires_manual_review; DELETE FROM schema_migrations WHERE version>=74`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_artist_identities(audio_file_id,artist_id,mbid) VALUES(?,?,?),(?,?,?)`, file1, artistA, idX, file2, artistA, idY); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.db.ExecContext(ctx, `DROP TABLE audio_file_artist_reverify; DROP TABLE audio_file_identity_checks; DELETE FROM schema_migrations WHERE version>=75`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_artist_identities(audio_file_id,artist_id,role,mbid,status) VALUES(?,?,'primary',?,'valid'),(?,?,'primary',?,'valid')`, file1, artistA, idX, file2, artistA, idY); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Migrate(ctx); err != nil {
				t.Fatal(err)
			}

			single, err := s.ArtistForMatching(ctx, artistA)
			if err != nil || single.TaggedMBID != "" {
				t.Fatalf("%s single leaked cross-file conflict MBID=%q err=%v", path, single.TaggedMBID, err)
			}
			batch, err := s.ArtistsForMatching(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range batch {
				if a.ID == artistA && a.TaggedMBID != "" {
					t.Fatalf("%s batch leaked cross-file conflict MBID=%q", path, a.TaggedMBID)
				}
			}

			// Primary override on track2 isolates file2's primary conflict and lets track1's X pass.
			if _, err := s.db.ExecContext(ctx, `INSERT INTO track_primary_artist_state(track_id,active) VALUES(?,1)`, track2); err != nil {
				t.Fatal(err)
			}
			overridden, err := s.ArtistForMatching(ctx, artistA)
			if err != nil || overridden.TaggedMBID != idX {
				t.Fatalf("%s expected track1 X when track2 primary overridden, got %+v err=%v", path, overridden, err)
			}

			// Undo override and rescan file2 cleanly with matching X; Z -> reverify cleared, X passes.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM track_primary_artist_state WHERE track_id=?`, track2); err != nil {
				t.Fatal(err)
			}
			if err := s.ImportTrack(ctx, ImportInput{
				LibraryID:    lib,
				RelativePath: "02.flac",
				FileSize:     2,
				ModifiedAtNS: 2,
				Metadata: metadata.AudioMetadata{
					Title:       "Track 2",
					Album:       "Existing Album",
					DiscNumber:  1,
					TrackNumber: 2,
					Artists:     []string{"Manual Artist", "Collab B"},
					Container:   "flac",
					Raw: map[string][]string{
						"ARTIST":               {"Manual Artist", "Collab B"},
						"MUSICBRAINZ_ARTISTID": {idX, idZ},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			afterRescan, err := s.ArtistForMatching(ctx, artistA)
			if err != nil || afterRescan.TaggedMBID != idX {
				t.Fatalf("%s expected TaggedMBID=%s after clean rescan of file2, got %+v err=%v", path, idX, afterRescan, err)
			}
		})
	}
}
