package storage

import (
	"database/sql"
	"errors"
	"testing"
)

func TestAutoBindArtistCandidateManualDecisionsWin(t *testing.T) {
	for _, decision := range []string{"reject", "confirm-other", "existing-other", "bind"} {
		t.Run(decision, func(t *testing.T) {
			s, ctx := openEnrichmentTestStore(t)
			_, _, _, artist := seedEnrichmentEntities(t, s, ctx)
			if err := s.ReplaceArtistCandidates(ctx, artist, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Artist", Score: 100}, {Source: "musicbrainz", ExternalID: "manual", DisplayName: "Artist", Score: 90}}); err != nil {
				t.Fatal(err)
			}
			candidates, _ := s.ArtistCandidates(ctx, artist)
			switch decision {
			case "reject":
				if err := s.RejectArtistCandidate(ctx, artist, candidates[0].ID); err != nil {
					t.Fatal(err)
				}
			case "confirm-other":
				if err := s.ConfirmArtistCandidate(ctx, artist, candidates[1].ID); err != nil {
					t.Fatal(err)
				}
			case "existing-other":
				if err := s.UpsertExternalArtistProfile(ctx, artist, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "manual", DisplayName: "Artist"}); err != nil {
					t.Fatal(err)
				}
			}
			bound, err := s.AutoBindArtistCandidate(ctx, artist, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Artist"})
			if err != nil {
				t.Fatal(err)
			}
			if bound != (decision == "bind") {
				t.Fatalf("bound=%v", bound)
			}
			id, err := s.ArtistExternalID(ctx, artist, "musicbrainz")
			if decision == "reject" {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("id=%s err=%v", id, err)
				}
				return
			}
			expected := "manual"
			if decision == "bind" {
				expected = "auto"
			}
			if err != nil || id != expected {
				t.Fatalf("id=%s err=%v", id, err)
			}
		})
	}
}

func TestAutoBindArtistCandidateProfileAndStatusRollbackTogether(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, artist := seedEnrichmentEntities(t, s, ctx)
	if err := s.ReplaceArtistCandidates(ctx, artist, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Artist", Score: 100}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_auto_confirmation BEFORE UPDATE OF status ON artist_match_candidates WHEN NEW.status='confirmed' BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	bound, err := s.AutoBindArtistCandidate(ctx, artist, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Artist"})
	if err == nil || bound {
		t.Fatalf("bound=%v err=%v", bound, err)
	}
	if _, err = s.ArtistExternalID(ctx, artist, "musicbrainz"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("profile escaped rollback", err)
	}
	candidates, _ := s.ArtistCandidates(ctx, artist)
	if candidates[0].Status != "candidate" {
		t.Fatal(candidates)
	}
}

func TestAutoBindArtistCandidateConfirmedRefresh(t *testing.T) {
	for _, current := range []string{"same", "different", "missing"} {
		t.Run(current, func(t *testing.T) {
			s, ctx := openEnrichmentTestStore(t)
			_, _, _, artist := seedEnrichmentEntities(t, s, ctx)
			if err := s.ReplaceArtistCandidates(ctx, artist, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Artist", Score: 100}}); err != nil {
				t.Fatal(err)
			}
			candidates, _ := s.ArtistCandidates(ctx, artist)
			if err := s.ConfirmArtistCandidate(ctx, artist, candidates[0].ID); err != nil {
				t.Fatal(err)
			}
			switch current {
			case "same":
				if _, err := s.db.ExecContext(ctx, `UPDATE artist_external_profiles SET fetched_at='2000-01-01T00:00:00Z' WHERE artist_id=?`, artist); err != nil {
					t.Fatal(err)
				}
			case "different":
				if _, err := s.db.ExecContext(ctx, `UPDATE artist_external_profiles SET external_id='manual' WHERE artist_id=?`, artist); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if _, err := s.db.ExecContext(ctx, `DELETE FROM artist_external_profiles WHERE artist_id=?`, artist); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.ReplaceArtistCandidates(ctx, artist, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Artist", Score: 100}, {Source: "musicbrainz", ExternalID: "other", DisplayName: "Other", Score: 90}}); err != nil {
				t.Fatal(err)
			}
			bound, err := s.AutoBindArtistCandidate(ctx, artist, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Refreshed", FetchedAt: "2026-10-03T00:00:00Z"})
			if err != nil || bound != (current == "same") {
				t.Fatalf("bound=%v err=%v", bound, err)
			}
			if current != "same" {
				return
			}
			var fetched, name string
			if err := s.db.QueryRowContext(ctx, `SELECT fetched_at,display_name FROM artist_external_profiles WHERE artist_id=? AND source='musicbrainz'`, artist).Scan(&fetched, &name); err != nil {
				t.Fatal(err)
			}
			if fetched != "2026-10-03T00:00:00Z" || name != "Refreshed" {
				t.Fatalf("%s %s", fetched, name)
			}
			candidates, _ = s.ArtistCandidates(ctx, artist)
			for _, candidate := range candidates {
				expected := "rejected"
				if candidate.ExternalID == "auto" {
					expected = "confirmed"
				}
				if candidate.Status != expected {
					t.Fatalf("%+v", candidate)
				}
			}
		})
	}
}

func TestAutoBindArtistCandidateExpectedRefusals(t *testing.T) {
	for _, refusal := range []string{"owner-conflict", "merged", "composite", "deleted"} {
		t.Run(refusal, func(t *testing.T) {
			s, ctx := openEnrichmentTestStore(t)
			_, _, _, artist := seedEnrichmentEntities(t, s, ctx)
			if err := s.ReplaceArtistCandidates(ctx, artist, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Artist", Score: 100}}); err != nil {
				t.Fatal(err)
			}
			result, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Owner','owner','owner')`)
			if err != nil {
				t.Fatal(err)
			}
			owner, _ := result.LastInsertId()
			switch refusal {
			case "owner-conflict":
				err = s.UpsertExternalArtistProfile(ctx, owner, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Owner"})
			case "merged":
				_, err = s.db.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=? WHERE id=?`, owner, artist)
			case "composite":
				_, err = s.db.ExecContext(ctx, `UPDATE artists SET user_display_name='Artist, Other' WHERE id=?`, artist)
			case "deleted":
				// An absent ID follows the same refusal path, without deleting library data.
				artist = 999999
			}
			if err != nil {
				t.Fatal(err)
			}
			bound, err := s.AutoBindArtistCandidate(ctx, artist, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "auto", DisplayName: "Artist"})
			if bound || err != nil {
				t.Fatalf("bound=%v err=%v", bound, err)
			}
			if _, err := s.ArtistExternalID(ctx, artist, "musicbrainz"); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("unexpected profile: %v", err)
			}
			if refusal != "deleted" {
				candidates, _ := s.ArtistCandidates(ctx, artist)
				if len(candidates) != 1 || candidates[0].Status != "candidate" {
					t.Fatal(candidates)
				}
			}
		})
	}
}

func TestRefreshArtistCandidateCannotRestoreResetIdentity(t *testing.T) {
	for _, change := range []string{"missing", "different", "rejected"} {
		t.Run(change, func(t *testing.T) {
			s, ctx := openEnrichmentTestStore(t)
			_, _, _, artist := seedEnrichmentEntities(t, s, ctx)
			p := ExternalArtistProfile{Source: "lastfm", ExternalID: "id", DisplayName: "Artist"}
			if err := s.ReplaceArtistCandidates(ctx, artist, []ArtistCandidate{{Source: "lastfm", ExternalID: "id", DisplayName: "Artist", Score: 82}}); err != nil {
				t.Fatal(err)
			}
			if change != "missing" {
				if err := s.UpsertExternalArtistProfile(ctx, artist, p); err != nil {
					t.Fatal(err)
				}
			}
			if change == "different" {
				if _, err := s.db.ExecContext(ctx, `UPDATE artist_external_profiles SET external_id='other' WHERE artist_id=?`, artist); err != nil {
					t.Fatal(err)
				}
			}
			if change == "rejected" {
				c, _ := s.ArtistCandidates(ctx, artist)
				if err := s.RejectArtistCandidate(ctx, artist, c[0].ID); err != nil {
					t.Fatal(err)
				}
			}
			bound, err := s.RefreshArtistCandidate(ctx, artist, p)
			if err != nil || bound {
				t.Fatalf("bound=%v err=%v", bound, err)
			}
		})
	}
}
