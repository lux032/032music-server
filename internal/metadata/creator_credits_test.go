package metadata

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	if got := CreatorMusicBrainzIDs(map[string][]string{"MUSICBRAINZ_COMPOSERID": {a}, "MUSICBRAINZ COMPOSER ID": {b}}, "composer"); got != nil {
		t.Fatalf("conflicting alias accepted: %v", got)
	}
	if _, ok := RawCreatorNamesForIdentity(map[string][]string{"COMPOSERS": {"A; A; B"}}, "composer", "", false); ok {
		t.Fatal("duplicate raw creator slot accepted")
	}
	if _, ok := RawCreatorNamesForIdentity(map[string][]string{"COMPOSER": {"A, B"}}, "composer", "", false); ok {
		t.Fatal("comma-guessed creator accepted")
	}
	if _, ok := RawPrimaryArtistNamesForIdentity(map[string][]string{"ARTISTS": {"A; ; B"}}, nil, false); ok {
		t.Fatal("empty raw artist slot accepted")
	}
	if _, ok := RawPrimaryArtistNamesForIdentity(map[string][]string{"ALBUMARTIST": {"Album Singer"}}, []string{"Album Singer"}, false); ok {
		t.Fatal("album artist fallback accepted for real file")
	}

	// ID3v2.4 interior empty slot (A\x00\x00B) must NOT be compacted into [A, B].
	nulFrames := append(
		makeID3Frame(4, "TXXX", []byte("\x03COMPOSERS\x00A\x00\x00B"), 0),
		makeID3Frame(4, "TXXX", []byte("\x03MUSICBRAINZ_COMPOSERID\x00"+a+"\x00"+b), 0)...,
	)
	nulRaw := map[string][]string{}
	parseID3v2TextFrames(bytes.NewReader(makeID3Tag(4, nulFrames)), func(frame string, fields []string) {
		if frame == "TXXX" && len(fields) > 1 {
			nulRaw[fields[0]] = fields[1:]
		}
	})
	if _, ok := RawCreatorNamesForIdentity(nulRaw, "composer", "", false); ok {
		t.Fatalf("id3 interior empty slot accepted: %+v", nulRaw)
	}
}

func TestM4AFreeformAtomStripsLocaleAndOggKeepsMultiValues(t *testing.T) {
	const a = "11111111-1111-4111-8111-111111111111"
	const b = "22222222-2222-4222-8222-222222222222"
	// Build a minimal M4A containing moov/udta/meta/ilst with ---- freeform atoms
	// carrying the 8-byte data header (type=1 + 4-byte locale=0).
	box := func(kind string, payload []byte) []byte {
		var buf bytes.Buffer
		binary.Write(&buf, binary.BigEndian, uint32(8+len(payload)))
		buf.WriteString(kind)
		buf.Write(payload)
		return buf.Bytes()
	}
	freeform := func(name, val string) []byte {
		mean := box("mean", append([]byte{0, 0, 0, 0}, []byte("com.apple.iTunes")...))
		nm := box("name", append([]byte{0, 0, 0, 0}, []byte(name)...))
		dataHeader := []byte{0, 0, 0, 1, 0, 0, 0, 0}
		dt := box("data", append(dataHeader, []byte(val)...))
		return box("----", append(append(mean, nm...), dt...))
	}
	ilst := box("ilst", append(
		append(freeform("ARTISTS", "Singer A; Singer B"), freeform("MUSICBRAINZ_ARTISTID", a+"; "+b)...),
		append(freeform("COMPOSERS", "Comp A; Comp B"), freeform("MUSICBRAINZ_COMPOSERID", a+"; "+b)...)...,
	))
	meta := box("meta", append([]byte{0, 0, 0, 0}, ilst...))
	moov := box("moov", box("udta", meta))
	ftyp := box("ftyp", []byte("M4A \x00\x00\x00\x00isomiso2"))
	m4aPath := filepath.Join(t.TempDir(), "test.m4a")
	if err := os.WriteFile(m4aPath, append(ftyp, moov...), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Read(m4aPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(CreatorMusicBrainzIDs(m.Raw, "composer"), []string{a, b}) {
		t.Fatalf("m4a composer ids=%v raw=%v", CreatorMusicBrainzIDs(m.Raw, "composer"), m.Raw)
	}
	if !reflect.DeepEqual(ArtistMusicBrainzIDs(m.Raw, "MUSICBRAINZ_ARTISTID", "MUSICBRAINZ ARTIST ID"), []string{a, b}) {
		t.Fatalf("m4a artist ids=%v", ArtistMusicBrainzIDs(m.Raw, "MUSICBRAINZ_ARTISTID", "MUSICBRAINZ ARTIST ID"))
	}

	// Build a minimal Ogg Vorbis stream with repeated ARTIST and MUSICBRAINZ_COMPOSERID comments.
	oggCRC := func(page []byte) uint32 {
		var crc uint32
		for _, b := range page {
			crc ^= uint32(b) << 24
			for i := 0; i < 8; i++ {
				if crc&0x80000000 != 0 {
					crc = (crc << 1) ^ 0x04c11db7
				} else {
					crc <<= 1
				}
			}
		}
		return crc
	}
	oggPage := func(granule uint64, seq uint32, bos bool, packet []byte) []byte {
		var buf bytes.Buffer
		buf.WriteString("OggS")
		buf.WriteByte(0)
		var hdrType byte
		if bos {
			hdrType = 0x02
		}
		buf.WriteByte(hdrType)
		binary.Write(&buf, binary.LittleEndian, granule)
		binary.Write(&buf, binary.LittleEndian, uint32(1))
		binary.Write(&buf, binary.LittleEndian, seq)
		binary.Write(&buf, binary.LittleEndian, uint32(0))
		var laces []byte
		rem := len(packet)
		for rem >= 255 {
			laces = append(laces, 255)
			rem -= 255
		}
		laces = append(laces, byte(rem))
		buf.WriteByte(byte(len(laces)))
		buf.Write(laces)
		buf.Write(packet)
		raw := buf.Bytes()
		binary.LittleEndian.PutUint32(raw[22:26], oggCRC(raw))
		return raw
	}
	var idPkt bytes.Buffer
	idPkt.WriteByte(1)
	idPkt.WriteString("vorbis")
	binary.Write(&idPkt, binary.LittleEndian, uint32(0))
	idPkt.WriteByte(2)
	binary.Write(&idPkt, binary.LittleEndian, uint32(44100))
	binary.Write(&idPkt, binary.LittleEndian, int32(0))
	binary.Write(&idPkt, binary.LittleEndian, int32(128000))
	binary.Write(&idPkt, binary.LittleEndian, int32(0))
	idPkt.WriteByte(0xb8)
	idPkt.WriteByte(1)

	var vc bytes.Buffer
	vc.WriteByte(3)
	vc.WriteString("vorbis")
	binary.Write(&vc, binary.LittleEndian, uint32(4))
	vc.WriteString("test")
	comments := []string{"TITLE=Song", "ARTIST=Singer A", "ARTIST=Singer B", "COMPOSER=Comp A", "COMPOSER=Comp B", "MUSICBRAINZ_COMPOSERID=" + a, "MUSICBRAINZ_COMPOSERID=" + b}
	binary.Write(&vc, binary.LittleEndian, uint32(len(comments)))
	for _, c := range comments {
		binary.Write(&vc, binary.LittleEndian, uint32(len(c)))
		vc.WriteString(c)
	}
	vc.WriteByte(1)

	oggPath := filepath.Join(t.TempDir(), "test.ogg")
	var oggData bytes.Buffer
	oggData.Write(oggPage(0, 0, true, idPkt.Bytes()))
	oggData.Write(oggPage(0, 1, false, vc.Bytes()))
	if err := os.WriteFile(oggPath, oggData.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	oggMeta, err := Read(oggPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oggMeta.Artists, []string{"Singer A", "Singer B"}) {
		t.Fatalf("ogg artists=%v raw=%v", oggMeta.Artists, oggMeta.Raw)
	}
	if !reflect.DeepEqual(CreatorMusicBrainzIDs(oggMeta.Raw, "composer"), []string{a, b}) {
		t.Fatalf("ogg composer ids=%v", CreatorMusicBrainzIDs(oggMeta.Raw, "composer"))
	}

	// Interleaved Ogg Vorbis streams + multi-page VorbisComment packet.
	longPad := strings.Repeat("Z", 300)
	var multiVC bytes.Buffer
	multiVC.WriteByte(3)
	multiVC.WriteString("vorbis")
	binary.Write(&multiVC, binary.LittleEndian, uint32(4))
	multiVC.WriteString("test")
	multiComments := []string{"TITLE=Song", "ARTIST=Singer A", "ARTIST=Singer B", "COMPOSER=Comp A", "COMPOSER=Comp B", "MUSICBRAINZ_COMPOSERID=" + a, "MUSICBRAINZ_COMPOSERID=" + b, "COMMENT=" + longPad}
	binary.Write(&multiVC, binary.LittleEndian, uint32(len(multiComments)))
	for _, c := range multiComments {
		binary.Write(&multiVC, binary.LittleEndian, uint32(len(c)))
		multiVC.WriteString(c)
	}
	multiVC.WriteByte(1)

	oggSerialPage := func(serial uint32, seq uint32, hdrType byte, body []byte) []byte {
		var laces []byte
		rem := len(body)
		for rem >= 255 {
			laces = append(laces, 255)
			rem -= 255
		}
		laces = append(laces, byte(rem))
		var buf bytes.Buffer
		buf.WriteString("OggS")
		buf.WriteByte(0)
		buf.WriteByte(hdrType)
		binary.Write(&buf, binary.LittleEndian, uint64(0))
		binary.Write(&buf, binary.LittleEndian, serial)
		binary.Write(&buf, binary.LittleEndian, seq)
		binary.Write(&buf, binary.LittleEndian, uint32(0))
		buf.WriteByte(byte(len(laces)))
		buf.Write(laces)
		buf.Write(body)
		raw := buf.Bytes()
		binary.LittleEndian.PutUint32(raw[22:26], oggCRC(raw))
		return raw
	}
	interleavedPath := filepath.Join(t.TempDir(), "interleaved.ogg")
	var il bytes.Buffer
	il.Write(oggSerialPage(1, 0, 0x02, idPkt.Bytes()))
	// Secondary interleaved stream (serial=99) BOS + data packet in the middle!
	il.Write(oggSerialPage(99, 0, 0x02, []byte("othe")))
	vcBytes := multiVC.Bytes()
	// Split vcBytes into two pages without ending the first packet on page 1:
	// first page has 1 segment of 255 bytes (no <255 terminating lace).
	splitPage1 := func(serial, seq uint32, hdrType byte, chunk []byte) []byte {
		var buf bytes.Buffer
		buf.WriteString("OggS")
		buf.WriteByte(0)
		buf.WriteByte(hdrType)
		binary.Write(&buf, binary.LittleEndian, uint64(0))
		binary.Write(&buf, binary.LittleEndian, serial)
		binary.Write(&buf, binary.LittleEndian, seq)
		binary.Write(&buf, binary.LittleEndian, uint32(0))
		buf.WriteByte(1)
		buf.WriteByte(255)
		buf.Write(chunk[:255])
		raw := buf.Bytes()
		binary.LittleEndian.PutUint32(raw[22:26], oggCRC(raw))
		return raw
	}
	il.Write(splitPage1(1, 1, 0x00, vcBytes))
	il.Write(oggSerialPage(99, 1, 0x00, []byte("more")))
	il.Write(oggSerialPage(1, 2, 0x01, vcBytes[255:]))
	if err := os.WriteFile(interleavedPath, il.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ilMeta, err := Read(interleavedPath)
	if err != nil {
		t.Fatal(err)
	}
	if ilMeta.UnsafeIdentity {
		t.Fatal("valid interleaved multi-page ogg flagged UnsafeIdentity")
	}
	if !reflect.DeepEqual(CreatorMusicBrainzIDs(ilMeta.Raw, "composer"), []string{a, b}) {
		t.Fatalf("interleaved ogg composer ids=%v", CreatorMusicBrainzIDs(ilMeta.Raw, "composer"))
	}

	// Conflicting MP4 namespaces (com.apple.iTunes vs org.musicbrainz.iTunes) must be rejected.
	mbFreeform := func(meanVal, name, val string) []byte {
		mean := box("mean", append([]byte{0, 0, 0, 0}, []byte(meanVal)...))
		nm := box("name", append([]byte{0, 0, 0, 0}, []byte(name)...))
		dt := box("data", append([]byte{0, 0, 0, 1, 0, 0, 0, 0}, []byte(val)...))
		return box("----", append(append(mean, nm...), dt...))
	}
	conflictIlst := box("ilst", append(
		append(mbFreeform("com.apple.iTunes", "COMPOSERS", "Comp A"), mbFreeform("com.apple.iTunes", "MUSICBRAINZ_COMPOSERID", a)...),
		mbFreeform("org.musicbrainz.iTunes", "MUSICBRAINZ_COMPOSERID", b)...,
	))
	conflictM4A := filepath.Join(t.TempDir(), "conflict_ns.m4a")
	if err := os.WriteFile(conflictM4A, append(ftyp, box("moov", box("udta", box("meta", append([]byte{0, 0, 0, 0}, conflictIlst...))))...), 0o600); err != nil {
		t.Fatal(err)
	}
	cm, err := Read(conflictM4A)
	if err != nil {
		t.Fatal(err)
	}
	if got := CreatorMusicBrainzIDs(cm.Raw, "composer"); got != nil {
		t.Fatalf("conflicting mp4 namespaces accepted: %v", got)
	}
}
