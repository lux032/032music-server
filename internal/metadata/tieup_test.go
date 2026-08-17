package metadata

import "testing"

func TestInferWorkAssociations(t *testing.T) {
	tests := []struct {
		name, title, album, path string
		raw                      map[string][]string
		wantTitle, wantType      string
		wantRole                 string
		wantSeason, wantSequence int
	}{
		{name: "explicit anime OP", title: "勇者", album: "Single", path: "Artist/Single/01.flac", raw: map[string][]string{"CONTENTGROUP": {"TVアニメ「葬送のフリーレン」OP1"}}, wantTitle: "葬送のフリーレン", wantType: "anime", wantRole: "op", wantSequence: 1},
		{name: "album soundtrack", title: "Zoltraak", album: "葬送のフリーレン Original Soundtrack", path: "OST/01.flac", raw: map[string][]string{}, wantTitle: "葬送のフリーレン", wantType: "other", wantRole: "ost"},
		{name: "season ED", title: "Ending", album: "TV Anime My Hero Season 2 ED2", path: "Anime/Album/01.flac", raw: map[string][]string{}, wantTitle: "My Hero", wantType: "anime", wantRole: "ed", wantSeason: 2, wantSequence: 2},
		{name: "generic album ignored", title: "Song", album: "Greatest Hits", path: "Artist/Greatest Hits/01.flac", raw: map[string][]string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := InferWorkAssociations(test.title, test.album, test.path, test.raw)
			if test.wantTitle == "" {
				if len(values) != 0 {
					t.Fatalf("got %#v, want no association", values)
				}
				return
			}
			if len(values) == 0 {
				t.Fatal("no association inferred")
			}
			got := values[0]
			if got.Title != test.wantTitle || got.Type != test.wantType || got.Role != test.wantRole || got.Season != test.wantSeason || got.Sequence != test.wantSequence {
				t.Fatalf("got %#v", got)
			}
		})
	}
}
