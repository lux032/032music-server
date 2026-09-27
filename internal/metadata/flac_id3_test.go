package metadata

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalFLAC builds a FLAC stream with STREAMINFO (10s @ 44.1kHz) and a
// Vorbis comment block carrying the given tags.
func minimalFLAC(tags ...string) []byte {
	stream := make([]byte, 34)
	binary.BigEndian.PutUint64(stream[10:18], uint64(44100)<<44|uint64(1)<<41|uint64(15)<<36|441000)
	var comment []byte
	le := func(v int) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, uint32(v)); return b }
	comment = append(comment, le(len("test"))...)
	comment = append(comment, "test"...)
	comment = append(comment, le(len(tags))...)
	for _, tag := range tags {
		comment = append(comment, le(len(tag))...)
		comment = append(comment, tag...)
	}
	out := []byte("fLaC")
	out = append(out, 0x00, 0x00, 0x00, 34)
	out = append(out, stream...)
	out = append(out, 0x84, byte(len(comment)>>16), byte(len(comment)>>8), byte(len(comment)))
	out = append(out, comment...)
	return append(out, make([]byte, 256)...)
}

// id3v2Tag returns an ID3v2.4 header followed by payloadSize padding bytes.
func id3v2Tag(payloadSize int) []byte {
	header := []byte{'I', 'D', '3', 4, 0, 0,
		byte(payloadSize >> 21 & 0x7f), byte(payloadSize >> 14 & 0x7f), byte(payloadSize >> 7 & 0x7f), byte(payloadSize & 0x7f)}
	return append(header, make([]byte, payloadSize)...)
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadFLACSkipsLeadingID3v2(t *testing.T) {
	flac := minimalFLAC("TITLE=I beg you", "ALBUM=Penny Rain", "ARTIST=Aimer", "TRACKNUMBER=2")
	cases := map[string][]byte{
		"plain":     flac,
		"one ID3":   append(id3v2Tag(300), flac...),
		"two ID3s":  append(append(id3v2Tag(20), id3v2Tag(4096)...), flac...),
		"empty ID3": append(id3v2Tag(0), flac...),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Read(writeTemp(t, "track.flac", data))
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if got.Title != "I beg you" || got.Album != "Penny Rain" || got.TrackNumber != 2 || got.Container != "flac" {
				t.Fatalf("metadata=%+v", got)
			}
			if got.DurationMillis != 10000 {
				t.Fatalf("duration=%d", got.DurationMillis)
			}
		})
	}
}

func TestReadFLACRejectsNonFLAC(t *testing.T) {
	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"mp3 renamed":   {[]byte("\xff\xfb\x90\x00" + strings.Repeat("\x00", 64)), `file starts with "\xff\xfb\x90\x00"`},
		"truncated ID3": {append(id3v2Tag(10)[:10], make([]byte, 4)...), "malformed or truncated"},
		"ID3 then junk": {append(id3v2Tag(16), []byte("RIFF\x00\x00\x00\x00")...), `file starts with "RIFF" at offset 26`},
		"empty file":    {nil, "invalid FLAC signature"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Read(writeTemp(t, "bad.flac", tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v, want containing %q", err, tc.want)
			}
		})
	}
}
