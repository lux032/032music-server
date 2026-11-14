package storage

import (
	"github.com/lux032/032music-server/internal/metadata"
	"testing"
)

func TestPrimaryArtistCorrectionMergeRollbackGuard(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	for _, name := range []string{"A", "B", "C"} {
		if err := s.ImportTrack(ctx, ImportInput{LibraryID: lib.ID, RelativePath: name + ".flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: name, Album: "Album", DiscNumber: 1, Artists: []string{name}, AlbumArtists: []string{"Album"}}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListArtists(ctx, Filters{ArtistRole: "all", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, a := range rows {
		ids[a.Name] = a.ID
	}
	tracks, err := s.ListTracks(ctx, Filters{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	track := tracks[0].ID
	state, _ := s.PrimaryArtistCorrection(ctx, track)
	if err = s.SetPrimaryArtistCorrection(ctx, track, state.Token, []int64{ids["A"], ids["B"]}, false); err != nil {
		t.Fatal(err)
	}
	operation, err := s.MergeArtists(ctx, ids["A"], ids["C"])
	if err != nil {
		t.Fatal(err)
	}
	state, _ = s.PrimaryArtistCorrection(ctx, track)
	if len(state.Artists) != 2 || state.Artists[0].ID != ids["C"] {
		t.Fatalf("%+v", state)
	}
	if err = s.RollbackArtistMerge(ctx, operation); err != nil {
		t.Fatal(err)
	}
	state, _ = s.PrimaryArtistCorrection(ctx, track)
	if state.Artists[0].ID != ids["A"] {
		t.Fatalf("%+v", state)
	}
	operation, err = s.MergeArtists(ctx, ids["A"], ids["C"])
	if err != nil {
		t.Fatal(err)
	}
	state, _ = s.PrimaryArtistCorrection(ctx, track)
	if err = s.SetPrimaryArtistCorrection(ctx, track, state.Token, []int64{ids["B"]}, false); err != nil {
		t.Fatal(err)
	}
	if err = s.RollbackArtistMerge(ctx, operation); err == nil {
		t.Fatal("rollback must not overwrite later edit")
	}
}
