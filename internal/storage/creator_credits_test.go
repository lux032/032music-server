package storage

import (
	"github.com/lux032/032music-server/internal/metadata"
	"reflect"
	"testing"
)

func TestCreatorCreditsRescanReplacesLegacyCommaNames(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if err := s.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	input := ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Queen of the Night", Album: "Album", DiscNumber: 1, Artists: []string{"Singer"}, AlbumArtists: []string{"VA"}, Composer: "紅音乃詩, STAND ALONE", Lyricist: "Berry Risa, Steph Sings!", Arranger: "A, B"}}
	for i := 0; i < 2; i++ {
		if err := s.ImportTrack(ctx, input); err != nil {
			t.Fatal(err)
		}
	}
	tracks, err := s.ListTracks(ctx, Filters{Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("%+v %v", tracks, err)
	}
	check := func(want map[string][]string) {
		t.Helper()
		track, e := s.TrackByID(ctx, tracks[0].ID)
		if e != nil {
			t.Fatal(e)
		}
		got := map[string][]string{}
		for _, c := range track.Credits {
			for _, a := range c.Artists {
				got[c.Role] = append(got[c.Role], a.Name)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("credits=%v want=%v", got, want)
		}
	}
	check(map[string][]string{"composer": {"紅音乃詩, STAND ALONE"}, "lyricist": {"Berry Risa, Steph Sings!"}, "arranger": {"A, B"}})
	input.LegacyCreatorConversion = true
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.LegacyCreatorConversion = false
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	check(map[string][]string{"composer": {"紅音乃詩", "STAND ALONE"}, "lyricist": {"Berry Risa", "Steph Sings!"}, "arranger": {"A", "B"}})
	input.Metadata.Raw = map[string][]string{"DATE": {"2026"}}
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	check(map[string][]string{"composer": {"紅音乃詩", "STAND ALONE"}, "lyricist": {"Berry Risa", "Steph Sings!"}, "arranger": {"A", "B"}})
	input.Metadata.Composer = "New, Name"
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	check(map[string][]string{"composer": {"New, Name"}, "lyricist": {"Berry Risa", "Steph Sings!"}, "arranger": {"A", "B"}})
	input.Metadata.Raw = map[string][]string{"COMPOSERS": {"Last, First; B"}, "LYRICISTS": {"Berry Risa; Steph Sings!"}, "ARRANGERS": {"A; B"}}
	if err = s.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	check(map[string][]string{"composer": {"Last, First", "B"}, "lyricist": {"Berry Risa", "Steph Sings!"}, "arranger": {"A", "B"}})
}
