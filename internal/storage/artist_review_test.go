package storage

import (
	"fmt"
	"testing"
)

func TestPendingArtistReviewPageBeyondFiveHundred(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	var target int64
	for i := 0; i < 502; i++ {
		r, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?)`, fmt.Sprintf("Artist%03d", i), fmt.Sprint(i), fmt.Sprintf("review-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		target, _ = r.LastInsertId()
	}
	if err := s.ReplaceArtistCandidates(ctx, target, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "x", DisplayName: "Target", Score: 85}}); err != nil {
		t.Fatal(err)
	}
	page, err := s.PendingArtistReviewPage(ctx, ArtistReviewFilter{Limit: 1})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Artist.ID != target {
		t.Fatalf("%+v %v", page, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET user_display_name='Artist & Other' WHERE id=?`, target); err != nil {
		t.Fatal(err)
	}
	page, err = s.PendingArtistReviewPage(ctx, ArtistReviewFilter{Query: "Other", Source: "musicbrainz", Limit: 1})
	if err != nil || page.Total != 1 || !page.Items[0].NeedsCreditCorrection {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = s.PendingArtistReviewPage(ctx, ArtistReviewFilter{Limit: 1, Offset: 1})
	if err != nil || page.Total != 1 || len(page.Items) != 0 {
		t.Fatalf("%+v %v", page, err)
	}
	page, err = s.PendingArtistReviewPage(ctx, ArtistReviewFilter{Source: "lastfm"})
	if err != nil || page.Total != 0 {
		t.Fatalf("%+v %v", page, err)
	}
}

func TestReplaceArtistCandidatesStableAndScoped(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, artist := seedEnrichmentEntities(t, s, ctx)
	initial := []ArtistCandidate{{Source: "musicbrainz", ExternalID: "same", DisplayName: "Same", Score: 90}, {Source: "lastfm", ExternalID: "other", DisplayName: "Other", Score: 82}}
	if err := s.ReplaceArtistCandidates(ctx, artist, initial); err != nil {
		t.Fatal(err)
	}
	old, _ := s.ArtistCandidates(ctx, artist)
	if err := s.ReplaceArtistCandidatesForSources(ctx, artist, initial[:1], []string{"musicbrainz"}); err != nil {
		t.Fatal(err)
	}
	current, _ := s.ArtistCandidates(ctx, artist)
	if len(current) != 2 || current[0].ID != old[0].ID || current[1].ID != old[1].ID {
		t.Fatalf("%+v -> %+v", old, current)
	}
	if err := s.RejectArtistCandidate(ctx, artist, current[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceArtistCandidatesForSources(ctx, artist, initial[:1], []string{"musicbrainz"}); err != nil {
		t.Fatal(err)
	}
	current, _ = s.ArtistCandidates(ctx, artist)
	for _, c := range current {
		if c.ExternalID == "same" && c.Status != "rejected" {
			t.Fatal(c)
		}
	}
	if err := s.ReplaceArtistCandidatesForSources(ctx, artist, nil, []string{"lastfm"}); err != nil {
		t.Fatal(err)
	}
	current, _ = s.ArtistCandidates(ctx, artist)
	if len(current) != 1 || current[0].Status != "rejected" {
		t.Fatal(current)
	}
}

func TestArtistReviewStableMultiplePagesExcludesMerged(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	var ids []int64
	for i := 0; i < 4; i++ {
		r, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Same','same',?)`, fmt.Sprintf("page-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		ids = append(ids, id)
		if err := s.ReplaceArtistCandidates(ctx, id, []ArtistCandidate{{Source: "musicbrainz", ExternalID: fmt.Sprint(id), DisplayName: "Same"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET merged_into_artist_id=? WHERE id=?`, ids[0], ids[3]); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		page, err := s.PendingArtistReviewPage(ctx, ArtistReviewFilter{Limit: 1, Offset: i})
		if err != nil || page.Total != 3 || len(page.Items) != 1 || page.Items[0].Artist.ID != ids[i] {
			t.Fatalf("page%d %+v %v", i, page, err)
		}
	}
}
