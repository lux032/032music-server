package metadata

import (
	"encoding/binary"
	"testing"
)

func TestFLACStreamInfoDuration(t *testing.T) {
	block := make([]byte, 34)
	packed := uint64(44100)<<44 | uint64(441000)
	binary.BigEndian.PutUint64(block[10:18], packed)
	if got := flacStreamInfoDuration(block); got != 10000 {
		t.Fatalf("duration = %d, want 10000", got)
	}
}

func TestMP3FrameInfo(t *testing.T) {
	header := uint32(0xffe00000) | 3<<19 | 1<<17 | 1<<16 | 9<<12
	frameSize, samples, sampleRate, ok := mp3FrameInfo(header)
	if !ok {
		t.Fatal("expected valid MPEG-1 Layer III frame")
	}
	if frameSize != 417 || samples != 1152 || sampleRate != 44100 {
		t.Fatalf("frame = (%d, %d, %d), want (417, 1152, 44100)", frameSize, samples, sampleRate)
	}
}
