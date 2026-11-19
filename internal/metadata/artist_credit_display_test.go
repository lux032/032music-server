package metadata

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestArtistJoinPhrasesValidatesOrderedNames(t *testing.T) {
	for _, tc := range []struct {
		display, source string
		names, want     []string
	}{
		{"A & the Band feat. B & C", "", []string{"A & the Band", "B", "C"}, []string{"", " feat. ", " & "}},
		{"A, B", "", []string{"A", "B"}, []string{"", ", "}},
		{"A feat. C & B", "", []string{"A", "B"}, nil},
		{"A feat. B", "", []string{"B", "A"}, nil},
		{"Alias と B", `{"display":"Alias と B","names":["A","B"],"before":[""," と "]}`, []string{"A", "B"}, []string{"", " と "}},
		{"Alias と B", `{"display":"Alias と B","names":["A","B"],"before":[""," と "]}`, []string{"B", "A"}, nil},
	} {
		raw := map[string][]string{"ARTIST": {tc.display}, "MUSICBRAINZ_ARTIST_CREDIT": {tc.source}}
		if got := ArtistJoinPhrases(raw, tc.names, false); !slices.Equal(got, tc.want) {
			t.Errorf("%s / %v: got %q, want %q", tc.display, tc.names, got, tc.want)
		}
	}
}

func TestArtistJoinPhrasesReadsNativeMP4DisplayAtoms(t *testing.T) {
	atom := func(kind string, data []byte) []byte {
		box := make([]byte, 8+len(data))
		binary.BigEndian.PutUint32(box, uint32(len(box)))
		copy(box[4:8], kind)
		copy(box[8:], data)
		return box
	}
	text := func(value string) []byte {
		return atom("data", append([]byte{0, 0, 0, 1, 0, 0, 0, 0}, value...))
	}
	freeform := func(name, value string) []byte {
		data := atom("mean", append(make([]byte, 4), "com.apple.iTunes"...))
		data = append(data, atom("name", append(make([]byte, 4), name...))...)
		return atom("----", append(data, text(value)...))
	}

	fields := atom("\xa9ART", text("Alias と B"))
	fields = append(fields, atom("aART", text("A feat. B"))...)
	fields = append(fields, freeform("ARTISTS", "A; B")...)
	fields = append(fields, freeform("MUSICBRAINZ_ARTIST_CREDIT",
		`{"display":"Alias と B","names":["A","B"],"before":[""," と "]}`)...)
	data := atom("ftyp", []byte("M4A \x00\x00\x00\x00M4A "))
	data = append(data, atom("moov", atom("udta", atom("meta", append(make([]byte, 4), atom("ilst", fields)...))))...)
	path := filepath.Join(t.TempDir(), "feature.m4a")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := ArtistJoinPhrases(m.Raw, m.Artists, false); !slices.Equal(got, []string{"", " と "}) {
		t.Fatalf("MP4 track join phrase: %q (raw: %v)", got, m.Raw)
	}
	if got := ArtistJoinPhrases(m.Raw, []string{"A", "B"}, true); !slices.Equal(got, []string{"", " feat. "}) {
		t.Fatalf("MP4 album join phrase: %q", got)
	}
}
