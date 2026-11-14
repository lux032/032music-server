package metadata

import (
	"bytes"
	"reflect"
	"testing"
)

func TestMusicAutoTaggerID3ArtistFixtures(t *testing.T) {
	// Generated with jaudiotagger 3.0.1 using FieldKey.ARTISTS set/addField.
	// Synthetic tags plus zero padding only; contains no copyrighted audio.
	for _, path := range []string{"testdata/structured-artists-id3v23.mp3", "testdata/structured-artists-id3v24.mp3"} {
		m, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m.Artists, []string{"Mori Calliope", "常闇トワ"}) {
			t.Fatalf("%s: %+v", path, m.Artists)
		}
	}
}

func TestID3StructuredArtistFramesPreserveNUL(t *testing.T) {
	frames := makeID3Frame(4, "TXXX", []byte("\x03ARTISTS\x00Mori Calliope\x00常闇トワ"), 0)
	raw := map[string][]string{}
	parseID3v2TextFrames(bytes.NewReader(makeID3Tag(4, frames)), func(frame string, fields []string) {
		if frame == "TXXX" && len(fields) > 1 {
			raw[fields[0]] = fields[1:]
		}
	})
	got := ArtistNames(raw, nil)
	if !reflect.DeepEqual(got, []string{"Mori Calliope", "常闇トワ"}) {
		t.Fatal(got)
	}
}

func TestStructuredAlbumArtistNames(t *testing.T) {
	raw := map[string][]string{"ALBUMARTISTS": {"Release A; Release B"}, "ARTISTS": {"Track Singer"}}
	got := AlbumArtistNames(raw, nil)
	if !reflect.DeepEqual(got, []string{"Release A", "Release B"}) {
		t.Fatal(got)
	}
}

func TestStructuredArtistNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string][]string
		want []string
	}{
		{"featured", map[string][]string{"ARTIST": {"Aiobahn +81 feat. Mori Calliope"}, "ARTISTS": {"Aiobahn +81", "Mori Calliope"}}, []string{"Aiobahn +81", "Mori Calliope"}},
		{"multiple", map[string][]string{"ARTIST": {"ACE", "CHiCO"}}, []string{"ACE", "CHiCO"}},
		{"comma not heuristic", map[string][]string{"ARTIST": {"ACME, Inc."}}, []string{"ACME, Inc."}},
		{"band", map[string][]string{"ARTIST": {"Simon & Garfunkel"}}, []string{"Simon & Garfunkel"}},
		{"legacy tagger", map[string][]string{"ARTIST": {"Mori Calliope, 常闇トワ"}, "MUSICBRAINZ_ARTISTID": {"11111111-1111-4111-8111-111111111111; 22222222-2222-4222-8222-222222222222"}}, []string{"Mori Calliope", "常闇トワ"}},
		{"ambiguous comma", map[string][]string{"ARTIST": {"Earth, Wind & Fire, Singer"}, "MUSICBRAINZ_ARTISTID": {"11111111-1111-4111-8111-111111111111; 22222222-2222-4222-8222-222222222222"}}, []string{"Earth, Wind & Fire, Singer"}},
		{"invalid identity", map[string][]string{"ARTIST": {"A, B"}, "MUSICBRAINZ_ARTISTID": {"bad; bad"}}, []string{"A, B"}},
		{"structured punctuation", map[string][]string{"ARTISTS": {"Earth, Wind & Fire", "AC/DC"}}, []string{"Earth, Wind & Fire", "AC/DC"}},
		{"id3 repeated bom", map[string][]string{"ARTISTS": {"\ufeffMori Calliope", "\ufeff常闇トワ"}}, []string{"Mori Calliope", "常闇トワ"}},
		{"id3 nul", map[string][]string{"ARTISTS": {"A\x00B"}}, []string{"A", "B"}},
		{"mp4", map[string][]string{"----:com.apple.iTunes:ARTISTS": {"A; B"}}, []string{"A", "B"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := applyFallbacks(AudioMetadata{Raw: tc.raw}, "song.flac")
			if !reflect.DeepEqual(result.Artists, tc.want) {
				t.Fatalf("%v want %v", result.Artists, tc.want)
			}
		})
	}
}
