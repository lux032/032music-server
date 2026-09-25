package metadata

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProbeAudioHeaders(t *testing.T) {
	flac := make([]byte, 4+4+34+100)
	copy(flac, "fLaC")
	flac[4] = 0x80
	flac[7] = 34
	packed := uint64(96000)<<44 | uint64(1)<<41 | uint64(23)<<36 | 96000
	binary.BigEndian.PutUint64(flac[18:26], packed)
	mp3 := make([]byte, 417)
	copy(mp3, []byte{0xff, 0xfb, 0x90, 0x64})
	adts := []byte{0xff, 0xf1, 0x50, 0x80, 0x01, 0x7f, 0xfc}
	ogg := make([]byte, 27+1+19)
	copy(ogg, "OggS")
	ogg[26] = 1
	ogg[27] = 19
	copy(ogg[28:], "OpusHead")
	ogg[37] = 2
	vorbis := make([]byte, 27+1+30)
	copy(vorbis, "OggS")
	vorbis[26] = 1
	vorbis[27] = 30
	vorbis[28] = 1
	copy(vorbis[29:], "vorbis")
	vorbis[39] = 2
	binary.LittleEndian.PutUint32(vorbis[40:44], 44100)
	// moov/stsd/mp4a AudioSampleEntry with 44.1 kHz.
	entry := make([]byte, 36)
	binary.BigEndian.PutUint32(entry[:4], 36)
	copy(entry[4:], "mp4a")
	binary.BigEndian.PutUint16(entry[24:26], 2)
	binary.BigEndian.PutUint32(entry[32:36], 44100<<16)
	stsd := make([]byte, 16)
	binary.BigEndian.PutUint32(stsd[:4], uint32(len(stsd)+len(entry)))
	copy(stsd[4:], "stsd")
	binary.BigEndian.PutUint32(stsd[12:], 1)
	stsd = append(stsd, entry...)
	moov := make([]byte, 8)
	binary.BigEndian.PutUint32(moov[:4], uint32(len(moov)+len(stsd)))
	copy(moov[4:], "moov")
	moov = append(moov, stsd...)
	// The MP4 parser must select a sound track, not top-level sample tables.
	atom := func(kind string, payload []byte) []byte {
		out := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(out[:4], uint32(len(out)))
		copy(out[4:8], kind)
		copy(out[8:], payload)
		return out
	}
	hdlr := make([]byte, 12)
	copy(hdlr[8:], "soun")
	moov = atom("moov", atom("trak", atom("mdia", append(atom("hdlr", hdlr), atom("stbl", stsd)...))))
	for _, tc := range []struct {
		name  string
		data  []byte
		codec string
		rate  int
	}{{"flac", flac, "flac", 96000}, {"mp3", mp3, "mp3", 44100}, {"aac", adts, "aac", 44100}, {"opus", ogg, "opus", 48000}, {"ogg", vorbis, "vorbis", 44100}, {"m4a", moov, "aac", 44100}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audio."+tc.name)
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			p := ProbeAudio(path, tc.name)
			if p.Codec != tc.codec || p.SampleRate != tc.rate {
				t.Fatalf("probe=%+v", p)
			}
		})
	}
}

func TestProbeAudioFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		ffmpeg = `C:\ffmpeg\bin\ffmpeg.exe`
		if _, err = os.Stat(ffmpeg); err != nil {
			t.Skip("ffmpeg unavailable")
		}
	}
	cases := []struct {
		name, ext, encoder, rate string
		expect                   AudioProps
	}{
		{"flac", "flac", "flac", "96000", AudioProps{Codec: "flac", SampleRate: 96000, BitDepth: 24, Channels: 2}},
		{"mp3_vbr", "mp3", "libmp3lame", "44100", AudioProps{Codec: "mp3", SampleRate: 44100, Channels: 2}},
		{"opus", "opus", "libopus", "44100", AudioProps{Codec: "opus", SampleRate: 48000, Channels: 2}},
		{"vorbis", "ogg", "libvorbis", "44100", AudioProps{Codec: "vorbis", SampleRate: 44100, Channels: 2}},
		{"alac882", "m4a", "alac", "88200", AudioProps{Codec: "alac", SampleRate: 88200, BitDepth: 24, Channels: 2}},
		{"alac96", "m4a", "alac", "96000", AudioProps{Codec: "alac", SampleRate: 96000, BitDepth: 24, Channels: 2}},
		{"alac192", "m4a", "alac", "192000", AudioProps{Codec: "alac", SampleRate: 192000, BitDepth: 24, Channels: 2}},
		{"aac", "m4a", "aac", "44100", AudioProps{Codec: "aac", SampleRate: 44100, Channels: 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name+"."+tc.ext)
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=" + tc.rate + ":duration=2", "-ac", "2", "-c:a", tc.encoder}
			if tc.encoder == "alac" {
				args = append(args, "-sample_fmt", "s32p")
			} else if tc.encoder == "flac" {
				args = append(args, "-sample_fmt", "s32")
			} else if tc.encoder == "libmp3lame" {
				args = append(args, "-q:a", "3")
			}
			args = append(args, "-y", path)
			if output, err := exec.Command(ffmpeg, args...).CombinedOutput(); err != nil {
				t.Skipf("ffmpeg codec unavailable: %v: %s", err, output)
			}
			got := ProbeAudio(path, tc.ext)
			if got.Codec != tc.expect.Codec || got.SampleRate != tc.expect.SampleRate || got.Channels != tc.expect.Channels || tc.expect.BitDepth != 0 && got.BitDepth != tc.expect.BitDepth {
				t.Fatalf("%s: %+v, want %+v", tc.name, got, tc.expect)
			}
			if got.BitrateKbps <= 0 && tc.encoder != "libopus" {
				t.Fatalf("%s: no bitrate: %+v", tc.name, got)
			}
		})
	}
}
func TestProbeMP4STSZFallbackAndMalformed(t *testing.T) {
	atom := func(kind string, b []byte) []byte {
		v := make([]byte, 8+len(b))
		binary.BigEndian.PutUint32(v[:4], uint32(len(v)))
		copy(v[4:8], kind)
		copy(v[8:], b)
		return v
	}
	mdhd := make([]byte, 20)
	binary.BigEndian.PutUint32(mdhd[12:16], 44100)
	binary.BigEndian.PutUint32(mdhd[16:20], 44100)
	mp4a := make([]byte, 28)
	binary.BigEndian.PutUint16(mp4a[16:18], 2)
	binary.BigEndian.PutUint32(mp4a[24:28], 44100<<16)
	stsz := make([]byte, 20)
	binary.BigEndian.PutUint32(stsz[8:12], 2)
	binary.BigEndian.PutUint32(stsz[12:16], 10000)
	binary.BigEndian.PutUint32(stsz[16:20], 10000)
	stsd := append(make([]byte, 8), atom("mp4a", mp4a)...)
	hdlr := make([]byte, 12)
	copy(hdlr[8:], "soun")
	media := append(atom("mdhd", mdhd), atom("hdlr", hdlr)...)
	media = append(media, atom("stbl", append(atom("stsd", stsd), atom("stsz", stsz)...))...)
	data := atom("moov", atom("trak", atom("mdia", media)))
	path := filepath.Join(t.TempDir(), "fallback.m4a")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got := ProbeAudio(path, "m4a")
	if got.BitrateKbps != 160 || got.Codec != "aac" {
		t.Fatalf("stsz fallback: %+v", got)
	}
	for _, broken := range [][]byte{nil, []byte("moov"), {0xff, 0xff, 0xff, 0xff, 'm', 'o', 'o', 'v'}, data[:len(data)-1]} {
		if err := os.WriteFile(path, broken, 0600); err != nil {
			t.Fatal(err)
		}
		_ = ProbeAudio(path, "m4a")
	}
}

func TestProbeFLACExcludesLargePicture(t *testing.T) {
	// A one-second FLAC with 1000 audio bytes and a 300-KiB picture retains the same audio bitrate.
	stream := make([]byte, 34)
	packed := uint64(44100)<<44 | uint64(1)<<41 | uint64(15)<<36 | 44100
	binary.BigEndian.PutUint64(stream[10:18], packed)
	makeFile := func(picture int) []byte {
		out := append([]byte("fLaC"), 0, 0, 0, 34)
		out = append(out, stream...)
		if picture > 0 {
			out = append(out, 0x86, byte(picture>>16), byte(picture>>8), byte(picture))
			out = append(out, make([]byte, picture)...)
		} else {
			out[4] = 0x80
		}
		return append(out, make([]byte, 1000)...)
	}
	path := filepath.Join(t.TempDir(), "picture.flac")
	if err := os.WriteFile(path, makeFile(0), 0600); err != nil {
		t.Fatal(err)
	}
	plain := ProbeAudio(path, "flac")
	if err := os.WriteFile(path, makeFile(300*1024), 0600); err != nil {
		t.Fatal(err)
	}
	withPicture := ProbeAudio(path, "flac")
	if plain.BitrateKbps != 8 || withPicture.BitrateKbps != plain.BitrateKbps {
		t.Fatalf("picture changes audio bitrate: plain=%+v withPicture=%+v", plain, withPicture)
	}
	broken := makeFile(300 * 1024)
	broken = broken[:46]
	if err := os.WriteFile(path, broken, 0600); err != nil {
		t.Fatal(err)
	}
	partial := ProbeAudio(path, "flac")
	if partial.Codec != "flac" || partial.BitrateKbps != 0 {
		t.Fatalf("truncated picture: %+v", partial)
	}
}
func TestProbeMP4IgnoresNonAudioTracks(t *testing.T) {
	atom := func(kind string, payload []byte) []byte {
		b := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
		copy(b[4:8], kind)
		copy(b[8:], payload)
		return b
	}
	track := func(handler string, rate, bytes int, fixed bool) []byte {
		h := make([]byte, 12)
		copy(h[8:], handler)
		mdhd := make([]byte, 20)
		binary.BigEndian.PutUint32(mdhd[12:16], uint32(rate))
		binary.BigEndian.PutUint32(mdhd[16:20], uint32(rate))
		entry := make([]byte, 28)
		binary.BigEndian.PutUint16(entry[16:18], 2)
		binary.BigEndian.PutUint32(entry[24:28], uint32(rate)<<16)
		stsd := append(make([]byte, 8), atom("mp4a", entry)...)
		size := make([]byte, 12)
		binary.BigEndian.PutUint32(size[8:12], 2)
		if fixed {
			binary.BigEndian.PutUint32(size[4:8], uint32(bytes/2))
		} else {
			for _, n := range []int{bytes / 2, bytes - bytes/2} {
				var item [4]byte
				binary.BigEndian.PutUint32(item[:], uint32(n))
				size = append(size, item[:]...)
			}
		}
		stbl := atom("stbl", append(atom("stsd", stsd), atom("stsz", size)...))
		mdia := append(atom("hdlr", h), atom("mdhd", mdhd)...)
		mdia = append(mdia, stbl...)
		return atom("trak", atom("mdia", mdia))
	}
	for _, fixed := range []bool{false, true} {
		data := atom("moov", append(track("text", 1000, 900000, fixed), track("soun", 44100, 20000, fixed)...))
		path := filepath.Join(t.TempDir(), "tracks.m4a")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		p := ProbeAudio(path, "m4a")
		if p.BitrateKbps != 160 || p.SampleRate != 44100 {
			t.Fatalf("fixed=%v: %+v", fixed, p)
		}
	}
}
func TestProbeMP3XingFramesWithoutBytesAndADTSID3(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	id := make([]byte, 10)
	copy(id, "ID3")
	mp3 := make([]byte, 417*10)
	copy(mp3, []byte{0xff, 0xfb, 0x90, 0x64})
	copy(mp3[36:], "Xing")
	binary.BigEndian.PutUint32(mp3[40:44], 1)
	binary.BigEndian.PutUint32(mp3[44:48], 10)
	if err := os.WriteFile(path, append(id, mp3...), 0600); err != nil {
		t.Fatal(err)
	}
	p := ProbeAudio(path, "mp3")
	if p.BitrateKbps < 120 || p.BitrateKbps > 130 {
		t.Fatalf("Xing frames-only bitrate: %+v", p)
	}
	aac := []byte{0xff, 0xf1, 0x50, 0x80, 0x01, 0x7f, 0xfc, 0, 0, 0, 0}
	path = filepath.Join(t.TempDir(), "track.aac")
	if err := os.WriteFile(path, append(id, aac...), 0600); err != nil {
		t.Fatal(err)
	}
	p = ProbeAudio(path, "aac")
	if p.Codec != "aac" || p.SampleRate != 44100 || p.BitrateKbps == 0 {
		t.Fatalf("ADTS after ID3: %+v", p)
	}
}

func TestProbeMP4SoundHandlerSurvivesMinfDataHandler(t *testing.T) {
	atom := func(kind string, body []byte) []byte {
		b := make([]byte, 8+len(body))
		binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
		copy(b[4:8], kind)
		copy(b[8:], body)
		return b
	}
	handler := func(kind string) []byte { b := make([]byte, 12); copy(b[8:], kind); return atom("hdlr", b) }
	entry := make([]byte, 28)
	binary.BigEndian.PutUint16(entry[16:18], 2)
	binary.BigEndian.PutUint32(entry[24:28], 44100<<16)
	stsd := atom("stsd", append(make([]byte, 8), atom("mp4a", entry)...))
	mdia := append(handler("soun"), atom("minf", append(handler("alis"), atom("stbl", stsd)...))...)
	data := atom("moov", atom("trak", atom("mdia", mdia)))
	path := filepath.Join(t.TempDir(), "handler.m4a")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got := ProbeAudio(path, "m4a")
	if got.Codec != "aac" || got.SampleRate != 44100 {
		t.Fatalf("minf handler overwrote soun: %+v", got)
	}
}
func TestProbeFLACID3v2Prefix(t *testing.T) {
	stream := make([]byte, 34)
	binary.BigEndian.PutUint64(stream[10:18], uint64(44100)<<44|uint64(1)<<41|uint64(15)<<36|44100)
	file := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00fLaC\x80\x00\x00\x22"), stream...)
	file = append(file, make([]byte, 1000)...)
	path := filepath.Join(t.TempDir(), "tagged.flac")
	if err := os.WriteFile(path, file, 0600); err != nil {
		t.Fatal(err)
	}
	got := ProbeAudio(path, "flac")
	if got.Codec != "flac" || got.SampleRate != 44100 || got.BitrateKbps != 8 {
		t.Fatalf("ID3-prefixed FLAC: %+v", got)
	}
}
