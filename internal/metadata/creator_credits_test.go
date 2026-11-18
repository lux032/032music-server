package metadata

import (
	"bytes"
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

func TestCreatorMusicBrainzIDsAndID3TXXX(t *testing.T) {
	const a = "11111111-1111-4111-8111-111111111111"
	const b = "22222222-2222-4222-8222-222222222222"
	frames := append(makeID3Frame(4, "TXXX", []byte("\x03MUSICBRAINZ_COMPOSERID\x00"+a+"; "+b), 0),
		makeID3Frame(4, "TXXX", []byte("\x03MUSICBRAINZ_ARRANGERID\x00"+b), 0)...)
	raw := map[string][]string{}
	parseID3v2TextFrames(bytes.NewReader(makeID3Tag(4, frames)), func(frame string, fields []string) {
		if frame == "TXXX" && len(fields) > 1 {
			raw[fields[0]] = fields[1:]
		}
	})
	if got := CreatorMusicBrainzIDs(raw, "composer"); !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("composer ids=%v", got)
	}
	if got := CreatorMusicBrainzIDs(raw, "arranger"); !reflect.DeepEqual(got, []string{b}) {
		t.Fatalf("arranger ids=%v", got)
	}
	if got := CreatorMusicBrainzIDs(map[string][]string{"----:com.apple.iTunes:MUSICBRAINZ_LYRICISTID": {a + "; " + b}}, "lyricist"); !reflect.DeepEqual(got, []string{a, b}) {
		t.Fatalf("mp4 lyricist ids=%v", got)
	}
	if got := CreatorMusicBrainzIDs(map[string][]string{"MUSICBRAINZ PRODUCER ID": {a}}, "producer"); !reflect.DeepEqual(got, []string{a}) {
		t.Fatalf("producer ids=%v", got)
	}
	if got := CreatorMusicBrainzIDs(map[string][]string{"MUSICBRAINZ_COMPOSERID": {a + "; bad"}}, "composer"); got != nil {
		t.Fatalf("invalid id accepted: %v", got)
	}
}
