package metadata

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxMP4AtomDepth      = 16
	maxMP4SingleAtomSize = 4 << 20
	maxMP4TotalReadBytes = 16 << 20
	maxOggCommentBytes   = 16 << 20
)

// readMP4CustomAtoms parses iTunes freeform ("----") atoms from an MP4/M4A file,
// skipping the full 8-byte data header (4-byte type/flags + 4-byte locale) and
// keeping distinct namespaces separate. Returns false when malformed or
// resource-exceeded atoms are encountered.
func readMP4CustomAtoms(file *os.File) (map[string][]string, bool) {
	info, err := file.Stat()
	if err != nil {
		return nil, false
	}
	atoms := map[string][]string{}
	budget := int64(maxMP4TotalReadBytes)
	ok := walkMP4CustomAtoms(file, 0, info.Size(), 0, &budget, atoms)
	return atoms, ok
}

func walkMP4CustomAtoms(file *os.File, start, end int64, depth int, budget *int64, out map[string][]string) bool {
	if depth > maxMP4AtomDepth {
		return false
	}
	for offset := start; offset+8 <= end; {
		header := make([]byte, 16)
		if _, err := file.ReadAt(header[:8], offset); err != nil {
			return false
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:8])
		headerSize := int64(8)
		if size == 1 {
			if offset+16 > end {
				return false
			}
			if _, err := file.ReadAt(header[8:16], offset+8); err != nil {
				return false
			}
			size = int64(binary.BigEndian.Uint64(header[8:16]))
			headerSize = 16
		} else if size == 0 {
			size = end - offset
		}
		if size < headerSize || offset+size > end {
			return false
		}
		payloadStart := offset + headerSize
		payloadEnd := offset + size
		switch kind {
		case "moov", "udta", "ilst":
			if !walkMP4CustomAtoms(file, payloadStart, payloadEnd, depth+1, budget, out) {
				return false
			}
		case "meta":
			if payloadStart+4 > payloadEnd {
				return false
			}
			if !walkMP4CustomAtoms(file, payloadStart+4, payloadEnd, depth+1, budget, out) {
				return false
			}
		case "----":
			key, values, ok := parseMP4FreeformAtom(file, payloadStart, payloadEnd, budget)
			if !ok {
				return false
			}
			if key != "" && len(values) > 0 {
				out[key] = append(out[key], values...)
			}
		}
		offset += size
	}
	return true
}

func parseMP4FreeformAtom(file *os.File, start, end int64, budget *int64) (string, []string, bool) {
	var mean, name string
	var values []string
	var invalidText bool
	for offset := start; offset+8 <= end; {
		var header [8]byte
		if _, err := file.ReadAt(header[:], offset); err != nil {
			return "", nil, false
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:8])
		if size < 8 || offset+size > end {
			return "", nil, false
		}
		payloadLen := size - 8
		if payloadLen > maxMP4SingleAtomSize || payloadLen > *budget {
			return "", nil, false
		}
		*budget -= payloadLen
		buf := make([]byte, payloadLen)
		if _, err := file.ReadAt(buf, offset+8); err != nil {
			return "", nil, false
		}
		switch kind {
		case "mean":
			if len(buf) < 4 {
				return "", nil, false
			}
			mean = string(buf[4:])
		case "name":
			if len(buf) < 4 {
				return "", nil, false
			}
			name = string(buf[4:])
		case "data":
			if len(buf) < 8 {
				invalidText = true
				break
			}
			dataType := binary.BigEndian.Uint32(buf[:4]) & 0x00ffffff
			payload := buf[8:]
			switch dataType {
			case 1: // UTF-8
				if !utf8.Valid(payload) {
					invalidText = true
					break
				}
				values = append(values, strings.TrimSpace(string(payload)))
			case 2: // UTF-16BE
				decoded, valid := decodeMP4UTF16BE(payload)
				if !valid {
					invalidText = true
					break
				}
				values = append(values, strings.TrimSpace(decoded))
			default:
				invalidText = true
			}
		}
		offset += size
	}
	upperName := strings.ToUpper(strings.TrimSpace(name))
	if upperName == "" {
		return "", nil, true
	}
	if invalidText {
		if isIdentityMP4Key(upperName) {
			return "", nil, false
		}
		return "", nil, true
	}
	switch mean {
	case "", "com.apple.iTunes":
		return upperName, values, true
	case "org.musicbrainz.iTunes":
		return "----:ORG.MUSICBRAINZ.ITUNES:" + upperName, values, true
	default:
		return "", nil, true
	}
}

func isIdentityMP4Key(upperName string) bool {
	if strings.HasPrefix(upperName, "MUSICBRAINZ") {
		return true
	}
	switch upperName {
	case "ARTIST", "ARTISTS", "ALBUMARTIST", "ALBUM ARTIST", "ALBUMARTISTS", "ALBUM_ARTISTS", "ALBUM ARTISTS",
		"COMPOSER", "COMPOSERS", "LYRICIST", "LYRICISTS", "ARRANGER", "ARRANGERS", "PRODUCER", "PRODUCERS":
		return true
	default:
		return false
	}
}

func decodeMP4UTF16BE(data []byte) (string, bool) {
	if len(data)%2 != 0 {
		return "", false
	}
	u16 := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		u16 = append(u16, binary.BigEndian.Uint16(data[i:i+2]))
	}
	return string(utf16.Decode(u16)), true
}

// readOggVorbisComments extracts the full multi-valued VorbisComment block
// from the target audio logical stream in an Ogg Vorbis or Opus container,
// handling interleaved logical streams, verifying page CRCs/sequences, and
// enforcing a cumulative packet budget.
func readOggVorbisComments(file *os.File, result *AudioMetadata) bool {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false
	}
	var (
		hasTarget     bool
		targetSerial  uint32
		expectedSeq   uint32
		packetIndex   int
		inPacket      bool
		current       bytes.Buffer
		commentPacket []byte
	)
	header := make([]byte, 27)
	for commentPacket == nil {
		if _, err := io.ReadFull(file, header); err != nil {
			return false
		}
		if string(header[:4]) != "OggS" {
			return false
		}
		segments := int(header[26])
		laces := make([]byte, segments)
		if _, err := io.ReadFull(file, laces); err != nil {
			return false
		}
		pagePayloadLen := 0
		for _, l := range laces {
			pagePayloadLen += int(l)
		}
		payload := make([]byte, pagePayloadLen)
		if _, err := io.ReadFull(file, payload); err != nil {
			return false
		}
		serial := binary.LittleEndian.Uint32(header[14:18])
		seq := binary.LittleEndian.Uint32(header[18:22])
		hdrType := header[5]
		isContinued := hdrType&0x01 != 0
		isBOS := hdrType&0x02 != 0

		if !hasTarget {
			if !isBOS {
				return false
			}
			// Check whether this BOS page starts a Vorbis or Opus identification header.
			if isOggAudioBOS(payload, laces) {
				if !verifyOggPageCRC(header, laces, payload) {
					return false
				}
				hasTarget = true
				targetSerial = serial
				expectedSeq = seq
			} else {
				continue
			}
		} else if serial != targetSerial {
			// Interleaved secondary logical stream: skip without disturbing target state.
			continue
		} else {
			if !verifyOggPageCRC(header, laces, payload) || seq != expectedSeq {
				return false
			}
			if isContinued != inPacket {
				return false
			}
		}
		expectedSeq++

		pos := 0
		for _, l := range laces {
			segLen := int(l)
			inPacket = true
			if packetIndex == 1 {
				if current.Len()+segLen > maxOggCommentBytes {
					return false
				}
				current.Write(payload[pos : pos+segLen])
			}
			pos += segLen
			if l < 255 {
				inPacket = false
				if packetIndex == 1 {
					commentPacket = current.Bytes()
					break
				}
				packetIndex++
				current.Reset()
			}
		}
	}
	var commentBlock []byte
	switch {
	case len(commentPacket) > 7 && commentPacket[0] == 3 && string(commentPacket[1:7]) == "vorbis":
		commentBlock = commentPacket[7:]
	case len(commentPacket) > 8 && string(commentPacket[:8]) == "OpusTags":
		commentBlock = commentPacket[8:]
	default:
		return false
	}
	parsed := AudioMetadata{Raw: map[string][]string{}}
	if err := parseVorbisComments(commentBlock, &parsed); err != nil {
		return false
	}
	for k, v := range parsed.Raw {
		result.Raw[k] = v
	}
	return true
}

func isOggAudioBOS(payload []byte, laces []byte) bool {
	if len(laces) == 0 {
		return false
	}
	firstLen := int(laces[0])
	if firstLen > len(payload) {
		return false
	}
	first := payload[:firstLen]
	return (len(first) >= 7 && first[0] == 1 && string(first[1:7]) == "vorbis") ||
		(len(first) >= 8 && string(first[:8]) == "OpusHead")
}

func verifyOggPageCRC(header, laces, payload []byte) bool {
	want := binary.LittleEndian.Uint32(header[22:26])
	var crc uint32
	update := func(data []byte) {
		for _, b := range data {
			crc ^= uint32(b) << 24
			for i := 0; i < 8; i++ {
				if crc&0x80000000 != 0 {
					crc = (crc << 1) ^ 0x04c11db7
				} else {
					crc <<= 1
				}
			}
		}
	}
	update(header[:22])
	var zero [4]byte
	update(zero[:])
	update(header[26:])
	update(laces)
	update(payload)
	return crc == want
}
