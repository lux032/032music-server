package metadata

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseID3v2UFIDAndFinalizeMP3(t *testing.T) {
	const recMBID = "12345678-1234-4123-8123-123456789abc"
	ufid := append([]byte("http://musicbrainz.org\x00"), []byte("12345678-1234-4123-8123-123456789ABC")...)
	other := append([]byte("https://example.org\x00"), []byte(recMBID)...)
	bad := append([]byte("http://musicbrainz.org\x00"), []byte("not-a-uuid")...)

	frames := append(makeID3Frame(4, "UFID", ufid, 0),
		append(makeID3Frame(4, "UFID", other, 0), makeID3Frame(4, "UFID", bad, 0)...)...)
	tagBytes := makeID3Tag(4, frames)

	var seen [][]string
	parseID3v2TextFrames(bytes.NewReader(tagBytes), func(frame string, fields []string) {
		if frame == "UFID" {
			seen = append(seen, fields)
		}
	})
	if len(seen) != 1 || seen[0][1] != recMBID {
		t.Fatalf("parsed UFID frames = %#v, want single %q", seen, recMBID)
	}

	mp3Path := filepath.Join(t.TempDir(), "track.mp3")
	// Minimal ID3v2.4 tag + valid MPEG frame header bytes so tag.ReadFrom succeeds.
	mp3Bytes := append(makeID3Tag(4, append(makeID3Frame(4, "TIT2", []byte("\x03Song"), 0), makeID3Frame(4, "UFID", ufid, 0)...)),
		0xff, 0xfb, 0x90, 0x00)
	if err := os.WriteFile(mp3Path, mp3Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	meta, err := Read(mp3Path)
	if err != nil {
		t.Fatalf("Read(%s): %v", mp3Path, err)
	}
	if got := RecordingMusicBrainzIDs(meta.Raw); !reflect.DeepEqual(got, []string{recMBID}) {
		t.Fatalf("RecordingMusicBrainzIDs = %v, want [%s] (raw=%v)", got, recMBID, meta.Raw)
	}
}
