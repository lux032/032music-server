package metadata

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
)

type AudioProps struct {
	Codec                                       string
	SampleRate, BitDepth, BitrateKbps, Channels int
}

// ProbeAudio reads bounded headers; malformed or unsupported files return zero properties.
func ProbeAudio(path, container string) AudioProps {
	f, err := os.Open(path)
	if err != nil {
		return AudioProps{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return AudioProps{}
	}
	switch strings.ToLower(container) {
	case "flac":
		return probeFLAC(f, info.Size())
	case "mp3":
		return probeMP3(f, info.Size())
	case "aac":
		return probeADTS(f)
	case "ogg", "oga", "opus":
		return probeOgg(f)
	case "m4a", "mp4":
		return probeMP4(f, info.Size())
	}
	return AudioProps{}
}
func probeFLAC(f *os.File, size int64) AudioProps {
	var id [10]byte
	_, _ = f.ReadAt(id[:], 0)
	start := id3v2Offset(id[:], size)
	if start > size-4 {
		return AudioProps{}
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return AudioProps{}
	}
	var h [4]byte
	if _, err := io.ReadFull(f, h[:]); err != nil || string(h[:]) != "fLaC" {
		return AudioProps{}
	}
	var p AudioProps
	var samples uint64
	offset := start + 4
	complete := false
	for i := 0; i < 100 && offset+4 <= size; i++ {
		if _, err := io.ReadFull(f, h[:]); err != nil {
			return p
		}
		n := int64(h[1])<<16 | int64(h[2])<<8 | int64(h[3])
		offset += 4
		if n > size-offset {
			return p
		} // Retain STREAMINFO, but never infer bitrate from a corrupt block chain.
		if h[0]&0x7f == 0 {
			if n < 18 {
				return p
			}
			var b [18]byte
			if _, err := io.ReadFull(f, b[:]); err != nil {
				return p
			}
			packed := binary.BigEndian.Uint64(b[10:18])
			p = AudioProps{Codec: "flac", SampleRate: int(packed >> 44), Channels: int((packed>>41)&7) + 1, BitDepth: int((packed>>36)&31) + 1}
			samples = packed & ((uint64(1) << 36) - 1)
		}
		offset += n
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return p
		}
		if h[0]&0x80 != 0 {
			complete = true
			break
		}
	}
	if complete && samples > 0 && p.SampleRate > 0 && size > offset {
		p.BitrateKbps = int(float64(size-offset) * 8 * float64(p.SampleRate) / float64(samples) / 1000)
	}
	return p
}
func probeMP3(f *os.File, size int64) AudioProps {
	var b [10]byte
	_, _ = f.ReadAt(b[:], 0)
	start := id3v2Offset(b[:], size)
	end := size
	if end >= 128 {
		var tag [3]byte
		_, _ = f.ReadAt(tag[:], end-128)
		if string(tag[:]) == "TAG" {
			end -= 128
		}
	}
	// Read the bounded sync window once rather than issuing one ReadAt per byte.
	if end <= start {
		return AudioProps{}
	}
	window := min(end-start, int64(1024*1024+3))
	buf := make([]byte, window)
	n, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return AudioProps{}
	}
	buf = buf[:n]
	for i := 0; i+4 <= len(buf) && i < 1024*1024; i++ {
		pos := start + int64(i)
		v := binary.BigEndian.Uint32(buf[i : i+4])
		_, _, rate, ok := mp3FrameInfo(v)
		if !ok || ((v>>17)&3) != 1 {
			continue
		}
		idx := int((v >> 12) & 15)
		version := (v >> 19) & 3
		rates := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}
		if version != 3 {
			rates = []int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160}
		}
		p := AudioProps{Codec: "mp3", SampleRate: rate, BitrateKbps: rates[idx], Channels: 2}
		if (v>>6)&3 == 3 {
			p.Channels = 1
		}
		side := 32
		if version != 3 {
			side = 17
		}
		if p.Channels == 1 {
			if version == 3 {
				side = 17
			} else {
				side = 9
			}
		}
		crc := 0
		if v&0x100 == 0 {
			crc = 2
		}
		var x [8]byte
		_, _ = f.ReadAt(x[:], pos+4+int64(crc+side))
		if string(x[:4]) == "Xing" || string(x[:4]) == "Info" {
			flags := binary.BigEndian.Uint32(x[4:8])
			cursor := pos + 4 + int64(crc+side) + 8
			var frames, bytes uint32
			var n [4]byte
			if flags&1 != 0 {
				if _, err := f.ReadAt(n[:], cursor); err == nil {
					frames = binary.BigEndian.Uint32(n[:])
				}
				cursor += 4
			}
			if flags&2 != 0 {
				if _, err := f.ReadAt(n[:], cursor); err == nil {
					bytes = binary.BigEndian.Uint32(n[:])
				}
			}
			if frames > 0 {
				samples := 1152
				if version != 3 {
					samples = 576
				}
				length := float64(bytes)
				if bytes == 0 && end > start {
					length = float64(end - start)
				}
				if length > 0 {
					p.BitrateKbps = int(length * 8 * float64(rate) / (float64(frames) * float64(samples)) / 1000)
				}
			}
		}
		var vb [18]byte
		_, _ = f.ReadAt(vb[:], pos+36)
		if string(vb[:4]) == "VBRI" {
			bytes := binary.BigEndian.Uint32(vb[10:14])
			frames := binary.BigEndian.Uint32(vb[14:18])
			if frames > 0 {
				length := float64(bytes)
				if bytes == 0 && end > start {
					length = float64(end - start)
				}
				samples := 1152
				if version != 3 {
					samples = 576
				}
				p.BitrateKbps = int(length * 8 * float64(rate) / (float64(frames) * float64(samples)) / 1000)
			}
		}
		return p
	}
	return AudioProps{}
}
func id3v2Offset(header []byte, size int64) int64 {
	if len(header) < 10 || string(header[:3]) != "ID3" {
		return 0
	}
	for _, v := range header[6:10] {
		if v&0x80 != 0 {
			return size
		}
	}
	offset := int64(10) + int64(header[6])<<21 + int64(header[7])<<14 + int64(header[8])<<7 + int64(header[9])
	if header[5]&16 != 0 {
		offset += 10
	}
	return offset
}
func probeADTS(f *os.File) AudioProps {
	info, err := f.Stat()
	if err != nil {
		return AudioProps{}
	}
	size := info.Size()
	var id [10]byte
	_, _ = f.ReadAt(id[:], 0)
	start := id3v2Offset(id[:], size)
	if start >= size {
		return AudioProps{}
	}
	rates := []int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	var p AudioProps
	var samples int64
	// A bounded prefix is enough to estimate bitrate without scanning a huge file.
	const maxADTSFrames = 4096
	const maxADTSBytes int64 = 16 << 20
	var scannedBytes int64
	for offset, frames := start, 0; offset+7 <= size && frames < maxADTSFrames && scannedBytes < maxADTSBytes; frames++ {
		var b [7]byte
		if _, err := f.ReadAt(b[:], offset); err != nil {
			break
		}
		if b[0] != 255 || b[1]&0xf6 != 0xf0 {
			if offset == start {
				return AudioProps{}
			}
			break
		}
		idx := int(b[2] >> 2 & 15)
		length := int64(b[3]&3)<<11 | int64(b[4])<<3 | int64(b[5]>>5)
		if idx >= len(rates) || length < 7 {
			break
		}
		if length > size-offset {
			// A truncated first frame still exposes format properties, not bitrate.
			if p.Codec == "" {
				p = AudioProps{Codec: "aac", SampleRate: rates[idx], Channels: int(b[2]&1)<<2 | int(b[3]>>6)}
			}
			return p
		}
		if p.Codec == "" {
			p = AudioProps{Codec: "aac", SampleRate: rates[idx], Channels: int(b[2]&1)<<2 | int(b[3]>>6)}
		}
		samples += int64((b[6]&3)+1) * 1024
		offset += length
		scannedBytes += length
	}
	if samples > 0 && p.SampleRate > 0 && scannedBytes > 0 {
		p.BitrateKbps = int(float64(scannedBytes) * 8 * float64(p.SampleRate) / float64(samples) / 1000)
	}
	return p
}
func probeOgg(f *os.File) AudioProps {
	var h [27]byte
	if _, err := io.ReadFull(f, h[:]); err != nil || string(h[:4]) != "OggS" {
		return AudioProps{}
	}
	laces := make([]byte, int(h[26]))
	if _, err := io.ReadFull(f, laces); err != nil {
		return AudioProps{}
	}
	n := 0
	for _, v := range laces {
		n += int(v)
	}
	if n > 65536 {
		return AudioProps{}
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(f, b); err != nil {
		return AudioProps{}
	}
	if len(b) >= 19 && string(b[:8]) == "OpusHead" {
		return AudioProps{Codec: "opus", SampleRate: 48000, Channels: int(b[9])}
	}
	if len(b) >= 30 && b[0] == 1 && string(b[1:7]) == "vorbis" {
		return AudioProps{Codec: "vorbis", Channels: int(b[11]), SampleRate: int(binary.LittleEndian.Uint32(b[12:16])), BitrateKbps: max(0, int(int32(binary.LittleEndian.Uint32(b[20:24]))/1000))}
	}
	return AudioProps{}
}

// mp4AudioState accumulates mdhd and stsd/stsz from the same audio track.
// mdhd generally precedes stsd, and stsz generally follows it.
type mp4AudioState struct {
	props           AudioProps
	durationSeconds float64
	mdhdTimescale   int
	sampleBytes     uint64
	hasCookie       bool
	handler         string
}

func probeMP4(f *os.File, size int64) AudioProps {
	if size <= 0 {
		return AudioProps{}
	}
	state := &mp4AudioState{}
	mp4PropsBoxes(f, 0, size, 0, state, "")
	if state.props.Codec == "" {
		return AudioProps{}
	}
	if state.props.Codec == "alac" && !state.hasCookie && state.mdhdTimescale > 0 {
		// AudioSampleEntry's 16.16 sample rate is unreliable above 65535 Hz.
		state.props.SampleRate = state.mdhdTimescale
	}
	if state.props.BitrateKbps == 0 && state.durationSeconds > 0 && state.sampleBytes > 0 {
		state.props.BitrateKbps = int(float64(state.sampleBytes) * 8 / state.durationSeconds / 1000)
	}
	return state.props
}
func mp4PropsBoxes(f *os.File, start, end int64, depth int, state *mp4AudioState, parent string) {
	if depth > 8 {
		return
	}
	for off := start; off+8 <= end; {
		var h [16]byte
		if _, err := f.ReadAt(h[:8], off); err != nil {
			return
		}
		n := int64(binary.BigEndian.Uint32(h[:4]))
		header := int64(8)
		if n == 1 {
			if _, err := f.ReadAt(h[8:], off+8); err != nil {
				return
			}
			u := binary.BigEndian.Uint64(h[8:])
			if u > uint64(end-off) {
				return
			}
			n = int64(u)
			header = 16
		} else if n == 0 {
			n = end - off
		}
		if n < header || n > end-off {
			return
		}
		kind := string(h[4:8])
		body := off + header
		switch kind {
		case "trak":
			// Never combine mdhd from one track with sample sizes from another.
			track := &mp4AudioState{}
			mp4PropsBoxes(f, body, off+n, depth+1, track, "trak")
			if track.handler == "soun" && track.props.Codec != "" && state.props.Codec == "" {
				*state = *track
			}
		case "moov", "mdia", "minf", "stbl":
			mp4PropsBoxes(f, body, off+n, depth+1, state, kind)
		case "hdlr":
			// minf can contain a QuickTime data handler (dhlr/alis/url);
			// only the mdia-level handler identifies the media track.
			if parent == "mdia" && n >= header+12 {
				var b [12]byte
				if _, err := f.ReadAt(b[:], body); err == nil {
					state.handler = string(b[8:12])
				}
			}
		case "stsd":
			if n >= header+8 {
				mp4PropsBoxes(f, body+8, off+n, depth+1, state, "stsd")
			}
		case "mp4a":
			if n >= header+28 {
				var b [28]byte
				if _, err := f.ReadAt(b[:], body); err != nil {
					return
				}
				state.props.Codec = "aac"
				state.props.Channels = int(binary.BigEndian.Uint16(b[16:18]))
				state.props.SampleRate = int(binary.BigEndian.Uint32(b[24:28]) >> 16)
				mp4PropsBoxes(f, body+28, off+n, depth+1, state, kind)
			}
		case "alac":
			// An ALAC sample entry (>=36 bytes) wraps a child alac magic-cookie atom.
			if n >= header+28 && isALACEntry(f, body) {
				var entry [28]byte
				if _, err := f.ReadAt(entry[:], body); err != nil {
					return
				}
				state.props.Codec = "alac"
				state.props.Channels = int(binary.BigEndian.Uint16(entry[16:18]))
				state.props.BitDepth = int(binary.BigEndian.Uint16(entry[18:20]))
				state.props.SampleRate = int(binary.BigEndian.Uint32(entry[24:28]) >> 16)
				mp4PropsBoxes(f, body+28, off+n, depth+1, state, kind)
				break
			}
			// ALAC magic cookie is stored in an alac child atom.
			if n >= header+28 {
				var cookie [24]byte
				if _, err := f.ReadAt(cookie[:], body+4); err == nil {
					// ALACSpecificConfig: frameLength(4), compatibleVersion(1), bitDepth(1),
					// pb/mb/kb(3), channels(1), maxRun(2), maxFrameBytes(4), avgBitRate(4), sampleRate(4).
					bits, channels, rate := int(cookie[5]), int(cookie[9]), int(binary.BigEndian.Uint32(cookie[20:24]))
					if bits > 0 && channels > 0 && rate > 0 {
						state.props.BitDepth = bits
						state.props.Channels = channels
						state.props.SampleRate = rate
						state.hasCookie = true
					}
				}
			}
		case "esds":
			if n > header+4 && n < header+4096 {
				b := make([]byte, n-header)
				if _, err := f.ReadAt(b, body); err != nil {
					return
				}
				// DecoderConfigDescriptor (tag 4), ISO/IEC 14496 variable-length size.
				for i := 4; i+3 < len(b); i++ {
					if b[i] != 4 {
						continue
					}
					j := i + 1
					for k := 0; k < 4 && j < len(b); k++ {
						v := b[j]
						j++
						if v&128 == 0 {
							break
						}
					}
					if j+13 <= len(b) && b[j] == 0x40 {
						avg := binary.BigEndian.Uint32(b[j+9 : j+13])
						if avg > 0 {
							state.props.BitrateKbps = int(avg / 1000)
						}
						break
					}
				}
			}
		case "stsz":
			if n >= header+12 {
				var b [12]byte
				if _, err := f.ReadAt(b[:], body); err != nil {
					return
				}
				fixed, count := binary.BigEndian.Uint32(b[4:8]), binary.BigEndian.Uint32(b[8:12])
				if fixed > 0 {
					state.sampleBytes = uint64(fixed) * uint64(count)
				} else if uint64(count) <= uint64((off+n-body-12)/4) {
					// Read entries in bounded batches; never allocate using an untrusted sample count.
					var entries [4096]byte
					at := body + 12
					for remain := int64(count); remain > 0; {
						batch := min(remain, int64(len(entries)/4))
						if _, err := f.ReadAt(entries[:batch*4], at); err != nil {
							return
						}
						for i := int64(0); i < batch; i++ {
							part := uint64(binary.BigEndian.Uint32(entries[i*4 : i*4+4]))
							if state.sampleBytes > ^uint64(0)-part {
								return
							}
							state.sampleBytes += part
						}
						at += batch * 4
						remain -= batch
					}
				}
			}
		case "mdhd":
			var b [32]byte
			length := min(n-header, int64(len(b)))
			if length >= 20 {
				if _, err := f.ReadAt(b[:length], body); err != nil {
					return
				}
				var timescale uint32
				var duration uint64
				if b[0] == 1 {
					if length < 32 {
						break
					}
					timescale = binary.BigEndian.Uint32(b[20:24])
					duration = binary.BigEndian.Uint64(b[24:32])
				} else {
					timescale = binary.BigEndian.Uint32(b[12:16])
					duration = uint64(binary.BigEndian.Uint32(b[16:20]))
				}
				if timescale > 0 {
					state.mdhdTimescale = int(timescale)
					state.durationSeconds = float64(duration) / float64(timescale)
				}
			}
		}
		off += n
	}
}

func isALACEntry(f *os.File, body int64) bool {
	var b [28]byte
	if _, err := f.ReadAt(b[:], body); err != nil {
		return false
	}
	return binary.BigEndian.Uint32(b[:4]) == 0 && binary.BigEndian.Uint16(b[6:8]) == 1 && binary.BigEndian.Uint16(b[16:18]) > 0
}
