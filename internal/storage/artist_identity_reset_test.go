package storage

import (
	"errors"
	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestResetIdentityPreservesLibraryAndOtherSources(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, track, _, artist := seedEnrichmentEntities(t, s, ctx)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id) VALUES(?,?)`, track, artist); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"musicbrainz", "lastfm"} {
		if err := s.UpsertExternalArtistProfile(ctx, artist, ExternalArtistProfile{Source: source, ExternalID: "wrong", DisplayName: "Wrong"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ReplaceArtistCandidates(ctx, artist, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "wrong", DisplayName: "Wrong", Score: 100}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artists SET user_biography='Keep me',biography='Wrong bio' WHERE id=?`, artist); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetArtistIdentity(ctx, artist, "musicbrainz", "changed"); !errors.Is(err, ErrIdentityConflictStale) {
		t.Fatal(err)
	}
	if err := s.ResetArtistIdentity(ctx, artist, "musicbrainz", "wrong"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArtistExternalID(ctx, artist, "lastfm"); err != nil {
		t.Fatal("other source removed", err)
	}
	var credits int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_artists WHERE track_id=? AND artist_id=?`, track, artist).Scan(&credits); err != nil || credits != 1 {
		t.Fatal(credits, err)
	}
	detail, err := s.ArtistDetail(ctx, artist)
	if err != nil || detail.Biography != "Keep me" {
		t.Fatalf("%+v %v", detail, err)
	}
}
func TestCompositeCreditCannotBindOrMergeIndividualIdentity(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, person := seedEnrichmentEntities(t, s, ctx)
	r, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Aiobahn +81 feat. Mori Calliope','duet','duet')`)
	if err != nil {
		t.Fatal(err)
	}
	duet, _ := r.LastInsertId()
	if err = s.UpsertExternalArtistProfile(ctx, duet, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mori", DisplayName: "Mori Calliope"}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceArtistCandidates(ctx, person, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "mori", DisplayName: "Mori Calliope", Score: 98}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := s.ArtistCandidates(ctx, person)
	conflict, err := s.ArtistIdentityConflict(ctx, person, candidates[0].ID)
	if err != nil || conflict.CanMerge {
		t.Fatalf("conflict=%+v err=%v", conflict, err)
	}
	if _, err = s.MergeArtistsForIdentityConflict(ctx, person, candidates[0].ID, duet); !errors.Is(err, ErrIdentityConflictStale) {
		t.Fatal(err)
	}
	if err = s.ReplaceArtistCandidates(ctx, duet, []ArtistCandidate{{Source: "musicbrainz", ExternalID: "other", DisplayName: "Mori Calliope", Score: 85}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ = s.ArtistCandidates(ctx, duet)
	if err = s.ConfirmArtistCandidate(ctx, duet, candidates[0].ID); !errors.Is(err, ErrCompositeArtistIdentity) {
		t.Fatal(err)
	}
}

func TestImportStructuredCollaborators(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: library.ID, RelativePath: "duet.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Duet", Album: "Album", Artists: []string{"Aiobahn +81 feat. Mori Calliope"}, AlbumArtists: []string{"Aiobahn +81"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"ARTIST": {"Aiobahn +81 feat. Mori Calliope"}, "ARTISTS": {"Aiobahn +81", "Mori Calliope"}}}}
	for i := 0; i < 2; i++ {
		if err = s.ImportTrack(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.display_name FROM track_artists ta JOIN artists a ON a.id=ta.artist_id WHERE ta.role='primary' ORDER BY ta.position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if len(names) != 2 || names[0] != "Aiobahn +81" || names[1] != "Mori Calliope" {
		t.Fatal(names)
	}
}
