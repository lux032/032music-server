package metadata

import (
	"reflect"
	"testing"
)

// Synthetic ID3 tags generated with jaudiotagger 3.0.1; no audio content.
func TestCreatorTaggerID3Fixtures(t *testing.T) {
	for _, path := range []string{"testdata/structured-creators-id3v23.mp3", "testdata/structured-creators-id3v24.mp3"} {
		m, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}
		for role, want := range map[string][]string{"composer": {"紅音乃詩", "STAND ALONE"}, "lyricist": {"Berry Risa", "Steph Sings!"}, "arranger": {"Last, First", "B"}} {
			got := CreatorNames(m.Raw, role, "", false)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s %s: %v", path, role, got)
			}
		}
	}
}

func TestCreatorNamesLegacyAndStructured(t *testing.T) {
	for _, tc := range []struct {
		name, role, fallback string
		raw                  map[string][]string
		want                 []string
	}{
		{name: "legacy lyricists", role: "lyricist", fallback: "Berry Risa, Steph Sings!", want: []string{"Berry Risa", "Steph Sings!"}},
		{name: "legacy composers", role: "composer", fallback: "紅音乃詩, STAND ALONE", want: []string{"紅音乃詩", "STAND ALONE"}},
		{name: "fullwidth and duplicates", role: "arranger", fallback: " A，B,A,, ", want: []string{"A", "B"}},
		{name: "producer", role: "producer", fallback: "A,B", want: []string{"A", "B"}},
		{name: "explicit punctuation", role: "composer", raw: map[string][]string{"COMPOSERS": {"Last, First; B"}}, want: []string{"Last, First", "B"}},
		{name: "native multi", role: "lyricist", raw: map[string][]string{"LYRICIST": {"Last, First", "B"}}, want: []string{"Last, First", "B"}},
		{name: "id3", role: "composer", raw: map[string][]string{"TCOM": {"\ufeffLast, First\x00B"}}, want: []string{"Last, First", "B"}},
		{name: "mp4", role: "arranger", raw: map[string][]string{"----:com.apple.iTunes:ARRANGERS": {"Last, First; B"}}, want: []string{"Last, First", "B"}},
		{name: "tag only", role: "lyricist", raw: map[string][]string{"LYRICISTS": {"A; B"}}, want: []string{"A", "B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CreatorNames(tc.raw, tc.role, tc.fallback, true)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got=%v want=%v", got, tc.want)
			}
		})
	}
	if got := CreatorNames(nil, "composer", "Last, First", false); !reflect.DeepEqual(got, []string{"Last, First"}) {
		t.Fatalf("default split a comma name: %v", got)
	}
	if got := ArtistNames(map[string][]string{"ARTIST": {"A, B"}}, nil); !reflect.DeepEqual(got, []string{"A, B"}) {
		t.Fatalf("performer rule changed: %v", got)
	}
}
