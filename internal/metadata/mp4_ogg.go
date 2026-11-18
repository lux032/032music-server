package metadata

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strings"
	"unicode/utf16"
)

// readMP4CustomAtoms parses iTunes freeform ("----") atoms from an MP4/M4A file,
// skipping the full 8-byte data header (4-byte type/flags + 4-byte locale) that
// dhowden/tag leaves partially unconsumed.
func readMP4CustomAtoms(file *os.File) map[string][]string {
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	atoms := map[string][]string{}
	walkMP4CustomAtoms(file, 0, info.Size(), atoms)
	return atoms
}

func walkMP4CustomAtoms(file *os.File, start, end int64, out map[string][]string) {
	for offset := start; offset+8 <= end; {
		header := make([]byte, 16)
		if _, err := file.ReadAt(header[:8], offset); err != nil {
			return
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:8])
		headerSize := int64(8)
		if size == 1 {
			if offset+16 > end {
				return
			}
			if _, err := file.ReadAt(header[8:16], offset+8); err != nil {
				return
			}
			size = int64(binary.BigEndian.Uint64(header[8:16]))
			headerSize = 16
		} else if size == 0 {
			size = end - offset
		}
		if size < headerSize || offset+size > end {
			return
		}
		payloadStart := offset + headerSize
		payloadEnd := offset + size
		switch kind {
		case "moov", "udta", "ilst":
			walkMP4CustomAtoms(file, payloadStart, payloadEnd, out)
		case "meta":
			if payloadStart+4 <= payloadEnd {
				walkMP4CustomAtoms(file, payloadStart+4, payloadEnd, out)
			}
		case "----":
			key, values := parseMP4FreeformAtom(file, payloadStart, payloadEnd)
			if key != "" && len(values) > 0 {
				out[key] = append(out[key], values...)
			}
		}
		offset += size
	}
}

func parseMP4FreeformAtom(file *os.File, start, end int64) (string, []string) {
	var mean, name string
	var values []string
	for offset := start; offset+8 <= end; {
		var header [8]byte
		if _, err := file.ReadAt(header[:], offset); err != nil {
			return "", nil
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:8])
		if size < 8 || offset+size > end {
			return "", nil
		}
		payloadLen := size - 8
		if payloadLen > 4*1024*1024 {
			return "", nil
		}
		buf := make([]byte, payloadLen)
		if _, err := file.ReadAt(buf, offset+8); err != nil {
			return "", nil
		}
		switch kind {
		case "mean":
			if len(buf) >= 4 {
				mean = string(buf[4:])
			}
		case "name":
			if len(buf) >= 4 {
				name = string(buf[4:])
			}
		case "data":
			if len(buf) >= 8 {
				dataType := binary.BigEndian.Uint32(buf[:4]) & 0x00ffffff
				payload := buf[8:]
				switch dataType {
				case 2: // UTF-16BE
					values = append(values, strings.TrimSpace(decodeMP4UTF16BE(payload)))
				default:
					values = append(values, strings.TrimSpace(string(payload)))
				}
			}
		}
		offset += size
	}
	if name == "" || (mean != "" && mean != "com.apple.iTunes" && mean != "org.musicbrainz.iTunes") {
		return "", nil
	}
	return strings.ToUpper(strings.TrimSpace(name)), values
}

func decodeMP4UTF16BE(data []byte) string {
	if len(data)%2 != 0 {
		data = data[:len(data)-1]
	}
	u16 := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		u16 = append(u16, binary.BigEndian.Uint16(data[i:i+2]))
	}
	return string(utf16.Decode(u16))
}

// readOggVorbisComments extracts the full multi-valued VorbisComment block
// from an Ogg Vorbis or Opus stream without overwriting repeated keys.
func readOggVorbisComments(file *os.File, result *AudioMetadata) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return
	}
	packetIndex := 0
	var current bytes.Buffer
	var commentPacket []byte
	header := make([]byte, 27)
	for commentPacket == nil {
		if _, err := io.ReadFull(file, header); err != nil {
			return
		}
		if string(header[:4]) != "OggS" {
			return
		}
		segments := int(header[26])
		laces := make([]byte, segments)
		if _, err := io.ReadFull(file, laces); err != nil {
			return
		}
		pagePayloadLen := 0
		for _, l := range laces {
			pagePayloadLen += int(l)
		}
		payload := make([]byte, pagePayloadLen)
		if _, err := io.ReadFull(file, payload); err != nil {
			return
		}
		pos := 0
		for _, l := range laces {
			segLen := int(l)
			if packetIndex == 1 {
				current.Write(payload[pos : pos+segLen])
			}
			pos += segLen
			if l < 255 {
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
		return
	}
	parsed := AudioMetadata{Raw: map[string][]string{}}
	if err := parseVorbisComments(commentBlock, &parsed); err != nil {
		return
	}
	for k, v := range parsed.Raw {
		result.Raw[k] = v
	}
}
