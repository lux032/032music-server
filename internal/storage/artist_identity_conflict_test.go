package storage

import (
	"errors"
	"fmt"
	"testing"
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
