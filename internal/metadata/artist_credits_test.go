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
		{"feat mbid recovery", map[string][]string{"ARTIST": {"Aiobahn +81 feat. DEMONDICE"}, "MUSICBRAINZ_ARTISTID": {"a39cf062-a860-4e22-ac59-1a8ebc65a8ff; 62d713c3-b081-4390-a9c7-0b8978d5b76a"}}, []string{"Aiobahn +81", "DEMONDICE"}},
		{"feat case insensitive", map[string][]string{"ARTIST": {"Aiobahn +81 Feat. DEMONDICE"}, "MUSICBRAINZ_ARTISTID": {"a39cf062-a860-4e22-ac59-1a8ebc65a8ff; 62d713c3-b081-4390-a9c7-0b8978d5b76a"}}, []string{"Aiobahn +81", "DEMONDICE"}},
		{"feat preserves band comma", map[string][]string{"ARTIST": {"Earth, Wind & Fire feat. DEMONDICE"}, "MUSICBRAINZ_ARTISTID": {"11111111-1111-4111-8111-111111111111; 22222222-2222-4222-8222-222222222222"}}, []string{"Earth, Wind & Fire", "DEMONDICE"}},
		{"three way comma and feat", map[string][]string{"ARTIST": {"A, B feat. C"}, "MUSICBRAINZ_ARTISTID": {"11111111-1111-4111-8111-111111111111; 22222222-2222-4222-8222-222222222222; 33333333-3333-4333-8333-333333333333"}}, []string{"A", "B", "C"}},
		{"ampersand with two mbids", map[string][]string{"ARTIST": {"A & B"}, "MUSICBRAINZ_ARTISTID": {"11111111-1111-4111-8111-111111111111; 22222222-2222-4222-8222-222222222222"}}, []string{"A", "B"}},
		{"band ampersand single mbid", map[string][]string{"ARTIST": {"Simon & Garfunkel"}, "MUSICBRAINZ_ARTISTID": {"11111111-1111-4111-8111-111111111111"}}, []string{"Simon & Garfunkel"}},
		{"feat without mbid not split", map[string][]string{"ARTIST": {"Aiobahn +81 feat. DEMONDICE"}}, []string{"Aiobahn +81 feat. DEMONDICE"}},
		{"various artists with two mbids not split", map[string][]string{"ARTIST": {"Various Artists"}, "MUSICBRAINZ_ARTISTID": {"a39cf062-a860-4e22-ac59-1a8ebc65a8ff; 62d713c3-b081-4390-a9c7-0b8978d5b76a"}}, []string{"Various Artists"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := applyFallbacks(AudioMetadata{Raw: tc.raw}, "song.flac")
			if !reflect.DeepEqual(result.Artists, tc.want) {
				t.Fatalf("%v want %v", result.Artists, tc.want)
			}
		})
	}
}

func TestAlbumArtistAndIdentityFeatMBIDRecovery(t *testing.T) {
	raw := map[string][]string{
		"ALBUMARTIST":               {"Aiobahn +81 feat. DEMONDICE"},
		"MUSICBRAINZ_ALBUMARTISTID": {"a39cf062-a860-4e22-ac59-1a8ebc65a8ff; 62d713c3-b081-4390-a9c7-0b8978d5b76a"},
		"ARTIST":                    {"Aiobahn +81 feat. DEMONDICE"},
		"MUSICBRAINZ_ARTISTID":      {"a39cf062-a860-4e22-ac59-1a8ebc65a8ff; 62d713c3-b081-4390-a9c7-0b8978d5b76a"},
	}
	want := []string{"Aiobahn +81", "DEMONDICE"}
	if got := AlbumArtistNames(raw, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("AlbumArtistNames = %v, want %v", got, want)
	}
	if got, ok := RawPrimaryArtistNamesForIdentity(raw, nil, false); !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("RawPrimaryArtistNamesForIdentity = %v (%v), want %v (true)", got, ok, want)
	}

	vaRaw := map[string][]string{
		"ALBUMARTIST":               {"Various Artists"},
		"MUSICBRAINZ_ALBUMARTISTID": {"a39cf062-a860-4e22-ac59-1a8ebc65a8ff; 62d713c3-b081-4390-a9c7-0b8978d5b76a"},
	}
	if got := AlbumArtistNames(vaRaw, nil); !reflect.DeepEqual(got, []string{"Various Artists"}) {
		t.Fatalf("VA AlbumArtistNames = %v, want [Various Artists]", got)
	}
}

func TestRawAlbumArtistNamesForIdentity(t *testing.T) {
	const (
		id1 = "11111111-1111-4111-8111-111111111111"
		id2 = "22222222-2222-4222-8222-222222222222"
	)
	for _, tc := range []struct {
		name          string
		raw           map[string][]string
		fallback      []string
		allowFallback bool
		want          []string
		wantOK        bool
	}{
		{
			name:   "structured multi album artists",
			raw:    map[string][]string{"ALBUMARTISTS": {"Yuki Kajiura", "FictionJunction"}, "MUSICBRAINZ_ALBUMARTISTID": {id1, id2}},
			want:   []string{"Yuki Kajiura", "FictionJunction"},
			wantOK: true,
		},
		{
			name:   "single display album artist with mbid",
			raw:    map[string][]string{"ALBUMARTIST": {"Yuki Kajiura"}, "MUSICBRAINZ_ALBUMARTISTID": {id1}},
			want:   []string{"Yuki Kajiura"},
			wantOK: true,
		},
		{
			name:   "feat recovery with matching mbid count",
			raw:    map[string][]string{"ALBUMARTIST": {"Aiobahn +81 feat. DEMONDICE"}, "MUSICBRAINZ_ALBUMARTISTID": {id1 + "; " + id2}},
			want:   []string{"Aiobahn +81", "DEMONDICE"},
			wantOK: true,
		},
		{
			name:   "composite display with single mbid rejected",
			raw:    map[string][]string{"ALBUMARTIST": {"A feat. B"}, "MUSICBRAINZ_ALBUMARTISTID": {id1}},
			wantOK: false,
		},
		{
			name:   "unsplit va with two mbids rejected",
			raw:    map[string][]string{"ALBUMARTIST": {"Various Artists"}, "MUSICBRAINZ_ALBUMARTISTID": {id1 + "; " + id2}},
			wantOK: false,
		},
		{
			name:          "never falls back when recording artist exists",
			raw:           map[string][]string{"ARTIST": {"Track Singer"}, "MUSICBRAINZ_ALBUMARTISTID": {id1}},
			fallback:      []string{"Track Singer"},
			allowFallback: true,
			wantOK:        false,
		},
		{
			name:          "synthetic fallback allowed when no artist tags exist",
			raw:           map[string][]string{"MUSICBRAINZ_ALBUMARTISTID": {id1}},
			fallback:      []string{"Yuki Kajiura"},
			allowFallback: true,
			want:          []string{"Yuki Kajiura"},
			wantOK:        true,
		},
		{
			name:   "conflicting albumartist keys rejected",
			raw:    map[string][]string{"ALBUMARTIST": {"Yuki Kajiura"}, "TPE2": {"Other Artist"}, "MUSICBRAINZ_ALBUMARTISTID": {id1}},
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := RawAlbumArtistNamesForIdentity(tc.raw, tc.fallback, tc.allowFallback)
			if ok != tc.wantOK || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got (%v, %v), want (%v, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}
