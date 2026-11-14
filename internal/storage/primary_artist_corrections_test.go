package storage

import (
	"errors"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestPrimaryArtistCorrectionRescanResetUndoAndConflict(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: library.ID, RelativePath: "duet.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Duet", Album: "Album", Artists: []string{"A, B"}, AlbumArtists: []string{"Album Singer"}, DiscNumber: 1, TrackNumber: 1}}
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	tracks, err := s.ListTracks(ctx, Filters{Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("%+v %v", tracks, err)
	}
	id := tracks[0].ID
	var ids []int64
	for _, name := range []string{"A", "B"} {
		r, e := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?)`, name, name, name)
		if e != nil {
			t.Fatal(e)
		}
		v, _ := r.LastInsertId()
		ids = append(ids, v)
	}
	before, err := s.PrimaryArtistCorrection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetPrimaryArtistCorrection(ctx, id, before.Token, ids, false); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPrimaryArtistCorrection(ctx, id, before.Token, ids, false); !errors.Is(err, ErrArtistCorrectionConflict) {
		t.Fatalf("stale token: %v", err)
	}
	check := func(active bool, names ...string) PrimaryArtistCorrection {
		t.Helper()
		v, e := s.PrimaryArtistCorrection(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if v.Active != active || len(v.Artists) != len(names) {
			t.Fatalf("%+v", v)
		}
		for i, name := range names {
			if v.Artists[i].Name != name {
				t.Fatalf("%+v", v)
			}
		}
		return v
	}
	active := check(true, "A", "B")
	input.Metadata.Artists = []string{"New tagged singer"}
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	check(true, "A", "B")
	if err = s.SetPrimaryArtistCorrection(ctx, id, active.Token, nil, false); !errors.Is(err, ErrArtistCorrectionConflict) {
		t.Fatalf("rescan conflict: %v", err)
	}
	active = check(true, "A", "B")
	if err = s.SetPrimaryArtistCorrection(ctx, id, active.Token, nil, false); err != nil {
		t.Fatal(err)
	}
	reset := check(false, "New tagged singer")
	if err = s.SetPrimaryArtistCorrection(ctx, id, reset.Token, nil, true); err != nil {
		t.Fatal(err)
	}
	check(true, "A", "B")
	if err = s.CleanupOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	check(true, "A", "B")
	albums, err := s.ListAlbums(ctx, Filters{Limit: 10})
	if err != nil || len(albums) != 1 || albums[0].Artist != "Album Singer" {
		t.Fatalf("album changed: %+v %v", albums, err)
	}
}

func TestImportLegacyMusicAutoTaggerCollaborators(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	input := ImportInput{LibraryID: lib.ID, RelativePath: "duet.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Duet", Album: "Album", DiscNumber: 1, Artists: []string{"Mori Calliope, 常闇トワ"}, AlbumArtists: []string{"Album Singer"}, Raw: map[string][]string{"ARTIST": {"Mori Calliope, 常闇トワ"}, "MUSICBRAINZ_ARTISTID": {"11111111-1111-4111-8111-111111111111; 22222222-2222-4222-8222-222222222222"}}}}
	for i := 0; i < 2; i++ {
		if err := s.ImportTrack(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	synced, err := s.SyncTracks(ctx, SyncTracksParams{Limit: 10})
	if err != nil || len(synced.Items) != 1 {
		t.Fatalf("%+v %v", synced, err)
	}
	refs := synced.Items[0].Artists
	if len(refs) != 2 || refs[0].Name != "Mori Calliope" || refs[1].Name != "常闇トワ" {
		t.Fatalf("%+v", refs)
	}
	for _, ref := range refs {
		matching, e := s.ArtistForMatching(ctx, ref.ID)
		if e != nil || matching.TaggedMBID == "" {
			t.Fatalf("identity evidence missing: %+v %v", matching, e)
		}
		tracks, _, e := s.ArtistTracks(ctx, ref.ID, ArtistTrackQuery{Limit: 10})
		if e != nil || len(tracks) != 1 {
			t.Fatalf("artist %d: %+v %v", ref.ID, tracks, e)
		}
	}
}
