package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestWorksCRUDSearchAndAssociations(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "works.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	work, err := store.CreateWork(ctx, WorkInput{Title: "葬送のフリーレン", ReadingTitle: "そうそうのフリーレン", TranslatedTitle: "Frieren", Type: "anime", Year: 2023})
	if err != nil {
		t.Fatal(err)
	}
	values, err := store.ListWorks(ctx, WorkFilters{Query: "ソウソウ", Index: "さ"})
	if err != nil || len(values) != 1 || values[0].ID != work.ID {
		t.Fatalf("kana search = %#v, err=%v", values, err)
	}
	if _, err := store.UpdateWork(ctx, work.ID, WorkInput{Title: work.Title, ReadingTitle: "sousou no frieren", Type: "anime", Year: 2023}); err != nil {
		t.Fatal(err)
	}
	values, err = store.ListWorks(ctx, WorkFilters{Query: "sousou", Index: "S"})
	if err != nil || len(values) != 1 {
		t.Fatalf("romaji search = %#v, err=%v", values, err)
	}

	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, _ := store.LibraryByRoot(ctx, "/music")
	input := ImportInput{LibraryID: library.ID, RelativePath: "Frieren/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "勇者", Album: "Single", Artists: []string{"YOASOBI"}, AlbumArtists: []string{"YOASOBI"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"CONTENTGROUP": {"TVアニメ「葬送のフリーレン」OP1"}}}}
	if err := store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.TracksForWork(ctx, work.ID)
	if err != nil || len(tracks) != 1 || tracks[0].Role != "op" || tracks[0].Sequence != 1 || tracks[0].Source != "auto" {
		t.Fatalf("tracks = %#v, err=%v", tracks, err)
	}
	if err := store.AddWorkTrack(ctx, work.ID, WorkTrackInput{TrackID: tracks[0].ID, Role: "theme"}); err != nil {
		t.Fatal(err)
	}
	input.ModifiedAtNS = 2
	if err := store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	tracks, _ = store.TracksForWork(ctx, work.ID)
	manualFound := false
	for _, track := range tracks {
		if track.Role == "theme" && track.Source == "manual" {
			manualFound = true
		}
	}
	if !manualFound {
		t.Fatalf("manual association lost after rescan: %#v", tracks)
	}
	if err := store.DeleteWork(ctx, work.ID); err != nil {
		t.Fatal(err)
	}
	if tracks, err := store.TracksForWork(ctx, work.ID); err != nil || len(tracks) != 0 {
		t.Fatalf("cascade tracks=%#v err=%v", tracks, err)
	}
}
