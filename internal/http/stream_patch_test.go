package httpapi

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// buildFLACHeader assembles a minimal FLAC metadata section: STREAMINFO,
// a PICTURE block with the given picture type, then fake audio bytes.
func buildFLACHeader(pictureType uint32) []byte {
	var buf bytes.Buffer
	buf.WriteString("fLaC")

	// STREAMINFO block (type 0), 34 bytes of zeros.
	buf.Write([]byte{0x00, 0x00, 0x00, 0x22})
	buf.Write(make([]byte, 34))

	// PICTURE block (type 6, last), 4-byte picture type + 8 payload bytes.
	buf.Write([]byte{0x80 | 6, 0x00, 0x00, 0x0c})
	var pic [4]byte
	binary.BigEndian.PutUint32(pic[:], pictureType)
	buf.Write(pic[:])
	buf.Write(make([]byte, 8))

	buf.WriteString("AUDIODATA")
	return buf.Bytes()
}

func writeTempFLAC(t *testing.T, contents []byte) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.flac")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return file
}

func TestFlacMetadataPatchesDetectsInvalidPictureType(t *testing.T) {
	file := writeTempFLAC(t, buildFLACHeader(0xFFFFFFFF))
	patches := flacMetadataPatches(file)
	if len(patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(patches))
	}
	// PICTURE payload starts after magic(4) + STREAMINFO header(4) + 34 + block header(4).
	wantOffset := int64(4 + 4 + 34 + 4)
	patch, ok := patches[wantOffset]
	if !ok {
		t.Fatalf("expected patch at offset %d, got %v", wantOffset, patches)
	}
	if binary.BigEndian.Uint32(patch[:]) != 3 {
		t.Fatalf("patch value = %v, want front cover (3)", patch)
	}
}

func TestFlacMetadataPatchesLeavesValidFilesAlone(t *testing.T) {
	file := writeTempFLAC(t, buildFLACHeader(3))
	if patches := flacMetadataPatches(file); patches != nil {
		t.Fatalf("patches = %v, want nil", patches)
	}
}

func TestFlacMetadataPatchesIgnoresNonFLAC(t *testing.T) {
	file := writeTempFLAC(t, []byte("ID3\x04not a flac file at all"))
	if patches := flacMetadataPatches(file); patches != nil {
		t.Fatalf("patches = %v, want nil", patches)
	}
}

// id3v2Tag builds an ID3v2 tag with the given body size (zero padding),
// optionally flagged as having a 10-byte footer.
func id3v2Tag(version byte, bodySize int, footer bool) []byte {
	var flags byte
	if footer {
		flags = 0x10
	}
	tag := []byte{'I', 'D', '3', version, 0, flags,
		byte(bodySize >> 21 & 0x7f), byte(bodySize >> 14 & 0x7f), byte(bodySize >> 7 & 0x7f), byte(bodySize & 0x7f)}
	tag = append(tag, make([]byte, bodySize)...)
	if footer {
		tag = append(tag, '3', 'D', 'I', version, 0, flags, tag[6], tag[7], tag[8], tag[9])
	}
	return tag
}

func TestFlacMetadataPatchesSkipsLeadingID3(t *testing.T) {
	cases := []struct {
		name   string
		prefix []byte
	}{
		{"id3v2.3", id3v2Tag(3, 5034, false)},
		{"id3v2.4 padding", id3v2Tag(4, 1024, false)},
		{"id3v2.4 footer", id3v2Tag(4, 200, true)},
		{"stacked tags", append(id3v2Tag(3, 50, false), id3v2Tag(4, 60, false)...)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contents := append(append([]byte(nil), tc.prefix...), buildFLACHeader(0xFFFFFFFF)...)
			file := writeTempFLAC(t, contents)
			patches := flacMetadataPatches(file)
			wantOffset := int64(len(tc.prefix)) + 4 + 4 + 34 + 4
			if patch, ok := patches[wantOffset]; !ok || len(patches) != 1 || binary.BigEndian.Uint32(patch[:]) != 3 {
				t.Fatalf("patches = %v, want single front-cover patch at %d", patches, wantOffset)
			}

			reader := &patchedReadSeeker{source: file, size: int64(len(contents)), patches: patches}
			got, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			want := append([]byte(nil), contents...)
			binary.BigEndian.PutUint32(want[wantOffset:wantOffset+4], 3)
			if !bytes.Equal(got, want) {
				t.Fatal("patched output differs outside the patched range")
			}
		})
	}
}

func TestFlacMetadataPatchesID3WithValidPictureUntouched(t *testing.T) {
	contents := append(id3v2Tag(4, 1024, false), buildFLACHeader(3)...)
	if patches := flacMetadataPatches(writeTempFLAC(t, contents)); patches != nil {
		t.Fatalf("patches = %v, want nil", patches)
	}
}

func TestFlacMetadataPatchesRejectsBadID3Size(t *testing.T) {
	// Non-syncsafe size byte (high bit set) must not be trusted.
	contents := append([]byte{'I', 'D', '3', 4, 0, 0, 0x80, 0, 0, 0}, buildFLACHeader(0xFFFFFFFF)...)
	if patches := flacMetadataPatches(writeTempFLAC(t, contents)); patches != nil {
		t.Fatalf("patches = %v, want nil", patches)
	}
}

func TestPatchedReadSeekerRewritesBytes(t *testing.T) {
	contents := buildFLACHeader(0xFFFFFFFF)
	file := writeTempFLAC(t, contents)
	patches := flacMetadataPatches(file)

	reader := &patchedReadSeeker{source: file, size: int64(len(contents)), patches: patches}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(contents) {
		t.Fatalf("length = %d, want %d", len(got), len(contents))
	}

	offset := int64(4 + 4 + 34 + 4)
	if picType := binary.BigEndian.Uint32(got[offset : offset+4]); picType != 3 {
		t.Fatalf("picture type in output = %d, want 3", picType)
	}
	// Everything else must be byte-identical.
	want := append([]byte(nil), contents...)
	binary.BigEndian.PutUint32(want[offset:offset+4], 3)
	if !bytes.Equal(got, want) {
		t.Fatal("patched output differs outside the patched range")
	}
}

func TestPatchedReadSeekerSupportsPartialReadsAcrossPatch(t *testing.T) {
	contents := buildFLACHeader(0xFFFFFFFF)
	file := writeTempFLAC(t, contents)
	patches := flacMetadataPatches(file)
	offset := int64(4 + 4 + 34 + 4)

	reader := &patchedReadSeeker{source: file, size: int64(len(contents)), patches: patches}
	// Seek into the middle of the patched range, as a range request would.
	if _, err := reader.Seek(offset+2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	twoBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, twoBytes); err != nil {
		t.Fatal(err)
	}
	if twoBytes[0] != 0x00 || twoBytes[1] != 0x03 {
		t.Fatalf("bytes at patch tail = %v, want [0 3]", twoBytes)
	}
}
