package metadata

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"strconv"
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

type mp4CustomResult struct {
	atoms        map[string][]string
	identityBad  bool
	limitTripped bool
}

// readMP4CustomAtoms parses iTunes freeform ("----") atoms from an MP4/M4A file,
// skipping the full 8-byte data header (4-byte type/flags + 4-byte locale) and
// keeping distinct namespaces separate.
func readMP4CustomAtoms(file *os.File) mp4CustomResult {
	res := mp4CustomResult{atoms: map[string][]string{}}
	info, err := file.Stat()
	if err != nil {
		res.limitTripped = true
		return res
	}
	budget := int64(maxMP4TotalReadBytes)
	walkMP4CustomAtoms(file, 0, info.Size(), 0, &budget, &res)
	return res
}

func walkMP4CustomAtoms(file *os.File, start, end int64, depth int, budget *int64, res *mp4CustomResult) bool {
	if depth > maxMP4AtomDepth {
		res.limitTripped = true
		return false
	}
	for offset := start; offset+8 <= end; {
		header := make([]byte, 16)
		if _, err := file.ReadAt(header[:8], offset); err != nil {
			res.identityBad = true
			return false
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:8])
		headerSize := int64(8)
		if size == 1 {
			if offset+16 > end {
				res.identityBad = true
				return false
			}
			if _, err := file.ReadAt(header[8:16], offset+8); err != nil {
				res.identityBad = true
				return false
			}
			size = int64(binary.BigEndian.Uint64(header[8:16]))
			headerSize = 16
		} else if size == 0 {
			size = end - offset
		}
		if size < headerSize || offset+size > end {
			res.identityBad = true
			return false
		}
		payloadStart := offset + headerSize
		payloadEnd := offset + size
		switch kind {
		case "moov", "udta", "ilst":
			if !walkMP4CustomAtoms(file, payloadStart, payloadEnd, depth+1, budget, res) {
				return false
			}
		case "meta":
			if payloadStart+4 > payloadEnd {
				res.identityBad = true
				return false
			}
			if !walkMP4CustomAtoms(file, payloadStart+4, payloadEnd, depth+1, budget, res) {
				return false
			}
		case "----":
			key, values, ok, limited := parseMP4FreeformAtom(file, payloadStart, payloadEnd, budget)
			if limited {
				res.limitTripped = true
				return false
			}
			if !ok {
				res.identityBad = true
			} else if key != "" && len(values) > 0 {
				res.atoms[key] = append(res.atoms[key], values...)
			}
		}
		offset += size
	}
	return true
}

func parseMP4FreeformAtom(file *os.File, start, end int64, budget *int64) (key string, values []string, ok bool, limited bool) {
	var mean, name string
	type rawDataSlice struct {
		offset int64
		size   int64
	}
	var dataBoxes []rawDataSlice
	for offset := start; offset+8 <= end; {
		var header [8]byte
		if _, err := file.ReadAt(header[:], offset); err != nil {
			return "", nil, false, false
		}
		size := int64(binary.BigEndian.Uint32(header[:4]))
		kind := string(header[4:8])
		if size < 8 || offset+size > end {
			return "", nil, false, false
		}
		payloadLen := size - 8
		switch kind {
		case "mean", "name":
			if payloadLen > maxMP4SingleAtomSize || payloadLen > *budget {
				return "", nil, false, true
			}
			*budget -= payloadLen
			buf := make([]byte, payloadLen)
			if _, err := file.ReadAt(buf, offset+8); err != nil || len(buf) < 4 {
				return "", nil, false, false
			}
			if kind == "mean" {
				mean = string(buf[4:])
			} else {
				name = string(buf[4:])
			}
		case "data":
			dataBoxes = append(dataBoxes, rawDataSlice{offset: offset + 8, size: payloadLen})
		}
		offset += size
	}

	upperName := strings.ToUpper(strings.TrimSpace(name))
	if upperName == "" {
		return "", nil, true, false
	}
	isSupportedMean := mean == "" || mean == "com.apple.iTunes" || mean == "org.musicbrainz.iTunes"
	if !isSupportedMean {
		return "", nil, true, false
	}
	// Skip reading payload bytes of unrelated binary iTunes atoms without wasting budget.
	isIdentity := isIdentityMP4Key(upperName)
	for _, box := range dataBoxes {
		if box.size > maxMP4SingleAtomSize || box.size > *budget {
			if isIdentity {
				return "", nil, false, true
			}
			continue
		}
		*budget -= box.size
		buf := make([]byte, box.size)
		if _, err := file.ReadAt(buf, box.offset); err != nil {
			if isIdentity {
				return "", nil, false, false
			}
			continue
		}
		if len(buf) < 8 {
			if isIdentity {
				return "", nil, false, false
			}
			continue
		}
		dataType := binary.BigEndian.Uint32(buf[:4]) & 0x00ffffff
		payload := buf[8:]
		switch dataType {
		case 1: // UTF-8
			if !utf8.Valid(payload) {
				if isIdentity {
					return "", nil, false, false
				}
				continue
			}
			values = append(values, strings.TrimSpace(string(payload)))
		case 2: // UTF-16BE
			decoded, valid := decodeMP4UTF16BE(payload)
			if !valid {
				if isIdentity {
					return "", nil, false, false
				}
				continue
			}
			values = append(values, strings.TrimSpace(decoded))
		default:
			if isIdentity {
				return "", nil, false, false
			}
		}
	}
	switch mean {
	case "", "com.apple.iTunes":
		return upperName, values, true, false
	case "org.musicbrainz.iTunes":
		return "----:ORG.MUSICBRAINZ.ITUNES:" + upperName, values, true, false
	default:
		return "", nil, true, false
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
	runes := make([]rune, 0, len(u16))
	for i := 0; i < len(u16); i++ {
		r := rune(u16[i])
		if utf16.IsSurrogate(r) {
			if r < 0xd800 || r > 0xdbff || i+1 >= len(u16) {
				return "", false
			}
			low := rune(u16[i+1])
			if low < 0xdc00 || low > 0xdfff {
				return "", false
			}
			runes = append(runes, utf16.DecodeRune(r, low))
			i++
			continue
		}
		runes = append(runes, r)
	}
	return string(runes), true
}

// readOggVorbisComments extracts the full multi-valued VorbisComment block
// from the target audio logical stream in an Ogg Vorbis or Opus container,
// handling interleaved logical streams, verifying page CRCs/sequences, and
// enforcing a cumulative packet budget.
func readOggVorbisComments(file *os.File) (raw map[string][]string, ok bool, limitTripped bool) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false, false
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
			return nil, false, false
		}
		if string(header[:4]) != "OggS" {
			return nil, false, false
		}
		segments := int(header[26])
		laces := make([]byte, segments)
		if _, err := io.ReadFull(file, laces); err != nil {
			return nil, false, false
		}
		pagePayloadLen := 0
		for _, l := range laces {
			pagePayloadLen += int(l)
		}
		payload := make([]byte, pagePayloadLen)
		if _, err := io.ReadFull(file, payload); err != nil {
			return nil, false, false
		}
		serial := binary.LittleEndian.Uint32(header[14:18])
		seq := binary.LittleEndian.Uint32(header[18:22])
		hdrType := header[5]
		isContinued := hdrType&0x01 != 0
		isBOS := hdrType&0x02 != 0

		if !hasTarget {
			if !isBOS {
				return nil, false, false
			}
			if isOggAudioBOS(payload, laces) {
				if !verifyOggPageCRC(header, laces, payload) {
					return nil, false, false
				}
				hasTarget = true
				targetSerial = serial
				expectedSeq = seq
			} else {
				continue
			}
		} else if serial != targetSerial {
			continue
		} else {
			if !verifyOggPageCRC(header, laces, payload) || seq != expectedSeq {
				return nil, false, false
			}
			if isContinued != inPacket {
				return nil, false, false
			}
		}
		expectedSeq++

		pos := 0
		for _, l := range laces {
			segLen := int(l)
			inPacket = true
			if packetIndex == 1 {
				if current.Len()+segLen > maxOggCommentBytes {
					return nil, false, true
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
		return nil, false, false
	}
	parsed := AudioMetadata{Raw: map[string][]string{}}
	if err := parseVorbisComments(commentBlock, &parsed); err != nil {
		return nil, false, false
	}
	return parsed.Raw, true, false
}

func applyVorbisCommentFields(result *AudioMetadata, raw map[string][]string) {
	result.Raw = raw
	first := func(keys ...string) string {
		for _, k := range keys {
			if vals := raw[k]; len(vals) > 0 {
				return vals[0]
			}
		}
		return ""
	}
	result.Title = first("TITLE")
	result.Album = first("ALBUM")
	result.Composer = first("COMPOSER")
	result.Lyrics = first("LYRICS", "UNSYNCEDLYRICS")
	date := first("DATE", "YEAR")
	if len(date) >= 4 {
		result.Year, _ = strconv.Atoi(date[:4])
	}
	result.TrackNumber, result.TrackTotal = parseNumberPair(first("TRACKNUMBER"), first("TRACKTOTAL", "TOTALTRACKS"))
	result.DiscNumber, result.DiscTotal = parseNumberPair(first("DISCNUMBER"), first("DISCTOTAL", "TOTALDISCS"))
	var genres []string
	for _, g := range raw["GENRE"] {
		genres = append(genres, splitValues(g)...)
	}
	result.Genres = genres
	if vals := raw["ARTIST"]; len(vals) > 0 {
		result.Artists = splitPeople(strings.Join(vals, ";"))
	} else {
		result.Artists = nil
	}
	if vals := raw["ALBUMARTIST"]; len(vals) > 0 {
		result.AlbumArtists = splitPeople(strings.Join(vals, ";"))
	} else if vals := raw["ALBUM ARTIST"]; len(vals) > 0 {
		result.AlbumArtists = splitPeople(strings.Join(vals, ";"))
	} else {
		result.AlbumArtists = nil
	}
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

func stripMP4ManagedKeys(flatRaw map[string][]string, raw map[string]interface{}) {
	for k := range flatRaw {
		if isMP4ManagedKey(k) {
			delete(flatRaw, k)
		}
	}
	for k := range raw {
		if isMP4ManagedKey(k) {
			delete(raw, k)
		}
	}
}

func isMP4ManagedKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if strings.HasPrefix(upper, "----:") || strings.Contains(upper, "MUSICBRAINZ") {
		return true
	}
	switch upper {
	case "ARTISTS", "ALBUMARTISTS", "ALBUM_ARTISTS", "ALBUM ARTISTS",
		"COMPOSER", "COMPOSERS",
		"LYRICIST", "LYRICISTS", "TEXT",
		"ARRANGER", "ARRANGERS",
		"PRODUCER", "PRODUCERS",
		"TIPL", "TMCL", "IPLS":
		return true
	}
	for _, prefix := range []string{"TIPL:", "TIPL.", "TIPL/", "TIPL_", "TMCL:", "TMCL.", "TMCL/", "TMCL_", "IPLS:", "IPLS.", "IPLS/", "IPLS_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}
