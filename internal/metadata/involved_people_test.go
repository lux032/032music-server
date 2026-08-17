package metadata

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
	"unicode/utf16"
)

func TestParseInvolvedPeopleRaw(t *testing.T) {
	raw := map[string]interface{}{
		"TIPL":          "lyricist\x00Alice\x00composer\x00Bob",
		"TMCL":          []string{"guitar", "Carol", "drums", "Dave"},
		"TIPL:producer": []string{"Eve"},
		"TMCL.bass":     "Frank",
	}
	got := parseInvolvedPeopleRaw(raw)
	want := []InvolvedPerson{
		{Role: "lyricist", Name: "Alice"},
		{Role: "composer", Name: "Bob"},
		{Role: "guitar", Name: "Carol"},
		{Role: "drums", Name: "Dave"},
		{Role: "producer", Name: "Eve"},
		{Role: "bass", Name: "Frank"},
	}
	if !samePeople(got, want) {
		t.Fatalf("parseInvolvedPeopleRaw() = %#v, want %#v", got, want)
	}
}

func TestParseInvolvedPeopleRawMapsAndMalformedPairs(t *testing.T) {
	raw := map[string]interface{}{
		"TIPL": map[string]interface{}{
			"arranger": []string{"Alice", "Bob"},
		},
		"TMCL_violín": "Carol",
		"IPLS":        "orphan-role",
		"OTHER":       "ignored",
	}
	got := parseInvolvedPeopleRaw(raw)
	want := []InvolvedPerson{
		{Role: "arranger", Name: "Alice"},
		{Role: "arranger", Name: "Bob"},
		{Role: "violín", Name: "Carol"},
	}
	if !samePeople(got, want) {
		t.Fatalf("parseInvolvedPeopleRaw() = %#v, want %#v", got, want)
	}
}

func TestApplyInvolvedPeopleFillsOnlyEmptyDisplayFields(t *testing.T) {
	meta := AudioMetadata{
		Composer: "Existing Composer",
		InvolvedPeople: []InvolvedPerson{
			{Role: "lyricist", Name: "Alice"},
			{Role: "composer", Name: "Bob"},
			{Role: "arranged by", Name: "Carol"},
			{Role: "producer", Name: "Dave"},
			{Role: "guitar", Name: "Eve"},
		},
	}
	applyInvolvedPeople(&meta)
	if meta.Lyricist != "Alice" || meta.Composer != "Existing Composer" || meta.Arranger != "Carol" || meta.Producer != "Dave" {
		t.Fatalf("unexpected display credits: %#v", meta)
	}
	if len(meta.InvolvedPeople) != 5 || meta.InvolvedPeople[4].Role != "guitar" {
		t.Fatalf("instrument credit not retained: %#v", meta.InvolvedPeople)
	}
}

func TestParseID3v2InvolvedPeopleEncodings(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  byte
		frameID  string
		encoding byte
		fields   []string
	}{
		{name: "v23 IPLS latin1", version: 3, frameID: "IPLS", encoding: 0, fields: []string{"producer", "André"}},
		{name: "v23 IPLS utf16 bom", version: 3, frameID: "IPLS", encoding: 1, fields: []string{"arranger", "編曲者"}},
		{name: "v24 TMCL utf16be", version: 4, frameID: "TMCL", encoding: 2, fields: []string{"piano", "演奏者"}},
		{name: "v24 TIPL utf8", version: 4, frameID: "TIPL", encoding: 3, fields: []string{"lyricist", "作詞家"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := encodeTextFields(tc.encoding, tc.fields)
			data := makeID3Tag(tc.version, makeID3Frame(tc.version, tc.frameID, payload, 0))
			got := parseID3v2InvolvedPeople(bytes.NewReader(data))
			want := []InvolvedPerson{{Role: tc.fields[0], Name: tc.fields[1]}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("parseID3v2InvolvedPeople() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestParseID3v2InvolvedPeopleUsesVersionSpecificFrames(t *testing.T) {
	v23Wrong := makeID3Tag(3, makeID3Frame(3, "TIPL", encodeTextFields(0, []string{"producer", "Wrong"}), 0))
	v24Wrong := makeID3Tag(4, makeID3Frame(4, "IPLS", encodeTextFields(3, []string{"producer", "Wrong"}), 0))
	for _, data := range [][]byte{v23Wrong, v24Wrong} {
		if got := parseID3v2InvolvedPeople(bytes.NewReader(data)); len(got) != 0 {
			t.Fatalf("version-incompatible involved frame returned %#v", got)
		}
	}
}

func TestParseID3v2InvolvedPeoplePreservesNULTokens(t *testing.T) {
	payload := append([]byte{3}, []byte("producer\x00Alice\x00guitar\x00Bob")...)
	data := makeID3Tag(4, makeID3Frame(4, "TIPL", payload, 0))
	got := parseID3v2InvolvedPeople(bytes.NewReader(data))
	want := []InvolvedPerson{{Role: "producer", Name: "Alice"}, {Role: "guitar", Name: "Bob"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NUL token pairs = %#v, want %#v", got, want)
	}
}

func TestParseID3v2InvolvedPeopleMultipleFramesAndBoundaries(t *testing.T) {
	good := makeID3Frame(4, "TMCL", encodeTextFields(3, []string{"guitar", "Alice", "drums", "Bob"}), 0)
	odd := makeID3Frame(4, "TIPL", encodeTextFields(3, []string{"producer", "Carol", "orphan"}), 0)
	data := makeID3Tag(4, append(good, odd...))
	got := parseID3v2InvolvedPeople(bytes.NewReader(data))
	want := []InvolvedPerson{{Role: "guitar", Name: "Alice"}, {Role: "drums", Name: "Bob"}, {Role: "producer", Name: "Carol"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseID3v2InvolvedPeople() = %#v, want %#v", got, want)
	}
}

func TestParseID3v2InvolvedPeopleFailsSafe(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte("not id3"),
		makeID3Tag(4, []byte("short")),
		makeID3Tag(4, makeID3Frame(4, "TIPL", []byte{9, 'x'}, 0)),
		makeID3Tag(4, makeID3Frame(4, "TIPL", []byte{3, 0xff}, 0)),
	}
	oversized := makeID3Frame(4, "TIPL", encodeTextFields(3, []string{"guitar", "Alice"}), 0)
	for i, value := range []byte{0, 0, 1, 0} {
		oversized[4+i] = value
	}
	cases = append(cases, makeID3Tag(4, oversized))

	for i, data := range cases {
		if got := parseID3v2InvolvedPeople(bytes.NewReader(data)); len(got) != 0 {
			t.Errorf("case %d returned %#v, want empty", i, got)
		}
	}
}

func samePeople(got, want []InvolvedPerson) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[InvolvedPerson]int, len(got))
	for _, person := range got {
		counts[person]++
	}
	for _, person := range want {
		if counts[person] == 0 {
			return false
		}
		counts[person]--
	}
	return true
}

func makeID3Tag(version byte, frames []byte) []byte {
	header := []byte{'I', 'D', '3', version, 0, 0, 0, 0, 0, 0}
	copy(header[6:10], encodeSynchsafe(len(frames)))
	return append(header, frames...)
}

func makeID3Frame(version byte, id string, payload []byte, formatFlags byte) []byte {
	header := make([]byte, 10)
	copy(header[:4], id)
	if version == 4 {
		copy(header[4:8], encodeSynchsafe(len(payload)))
	} else {
		binary.BigEndian.PutUint32(header[4:8], uint32(len(payload)))
	}
	header[9] = formatFlags
	return append(header, payload...)
}

func encodeSynchsafe(size int) []byte {
	return []byte{byte(size >> 21), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
}

func encodeTextFields(encoding byte, fields []string) []byte {
	joined := ""
	for i, field := range fields {
		if i > 0 {
			joined += "\x00"
		}
		joined += field
	}
	payload := []byte{encoding}
	switch encoding {
	case 0:
		for _, r := range joined {
			payload = append(payload, byte(r))
		}
	case 1, 2:
		if encoding == 1 {
			payload = append(payload, 0xff, 0xfe)
		}
		for _, unit := range utf16.Encode([]rune(joined)) {
			var word [2]byte
			if encoding == 1 {
				binary.LittleEndian.PutUint16(word[:], unit)
			} else {
				binary.BigEndian.PutUint16(word[:], unit)
			}
			payload = append(payload, word[:]...)
		}
	case 3:
		payload = append(payload, joined...)
	}
	return payload
}
