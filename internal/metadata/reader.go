package metadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dhowden/tag"
)

type InvolvedPerson struct {
	Role string
	Name string
}

type AudioMetadata struct {
	Title          string
	Album          string
	Artists        []string
	AlbumArtists   []string
	Composer       string
	Lyricist       string
	Arranger       string
	Producer       string
	InvolvedPeople []InvolvedPerson
	Genres         []string
	Lyrics         string
	Year           int
	TrackNumber    int
	TrackTotal     int
	DiscNumber     int
	DiscTotal      int
	DurationMillis int64
	Container      string
	MIMEType       string
	TrackType      string // regular, instrumental, off_vocal, tv_size, drama_track, remix
	Raw            map[string][]string
	Artwork        []byte
	ArtworkMIME    string
	ArtworkExt     string

	// Sort/reading keys for Japanese ordering
	ArtistSort      string
	AlbumArtistSort string
	TitleSort       string
	AlbumSort       string
}

var supportedExtensions = map[string]struct{}{
	".flac": {}, ".mp3": {}, ".m4a": {}, ".mp4": {}, ".aac": {},
	".ogg": {}, ".oga": {}, ".opus": {},
}

func IsSupported(path string) bool {
	_, ok := supportedExtensions[strings.ToLower(filepath.Ext(path))]
	return ok
}

func Read(path string) (AudioMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return AudioMetadata{}, fmt.Errorf("open audio file: %w", err)
	}
	defer file.Close()
	if strings.EqualFold(filepath.Ext(path), ".flac") {
		result, err := readFLAC(file)
		if err != nil {
			return AudioMetadata{}, fmt.Errorf("read FLAC metadata: %w", err)
		}
		return applyFallbacks(result, path), nil
	}

	parsed, err := tag.ReadFrom(file)
	if err != nil {
		return AudioMetadata{}, fmt.Errorf("read embedded metadata: %w", err)
	}
	raw := parsed.Raw()

	result := AudioMetadata{
		Title:        strings.TrimSpace(parsed.Title()),
		Album:        strings.TrimSpace(parsed.Album()),
		Composer:     strings.TrimSpace(parsed.Composer()),
		Lyrics:       strings.TrimSpace(parsed.Lyrics()),
		Year:         parsed.Year(),
		Container:    strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		MIMEType:     mime.TypeByExtension(strings.ToLower(filepath.Ext(path))),
		Raw:          flattenRaw(raw),
		Artists:      splitPeople(parsed.Artist()),
		AlbumArtists: splitPeople(parsed.AlbumArtist()),
		Genres:       splitValues(parsed.Genre()),
	}
	result.TrackNumber, result.TrackTotal = parsed.Track()
	result.DiscNumber, result.DiscTotal = parsed.Disc()

	// Extract credits and sort keys from raw tags (ID3v2 / MP4)
	rawFirst := func(keys ...string) string {
		for _, key := range keys {
			if vals := result.Raw[key]; len(vals) > 0 && strings.TrimSpace(vals[0]) != "" {
				return strings.TrimSpace(vals[0])
			}
		}
		return ""
	}
	result.Lyricist = rawFirst("LYRICIST", "TEXT")
	result.Arranger = rawFirst("ARRANGER")
	result.Producer = rawFirst("PRODUCER")
	if strings.EqualFold(filepath.Ext(path), ".mp3") {
		// dhowden/tag flattens text frames and loses the NUL separators which
		// define TIPL/TMCL/IPLS role/name pairs, so MP3 credits must come from
		// the original ID3v2 frame bytes rather than parsed.Raw().
		if _, seekErr := file.Seek(0, io.SeekStart); seekErr == nil {
			result.InvolvedPeople = parseID3v2InvolvedPeople(file)
		}
	} else {
		result.InvolvedPeople = parseInvolvedPeopleRaw(raw)
	}
	applyInvolvedPeople(&result)
	result.ArtistSort = rawFirst("ARTISTSORT", "SOAR", "TSOP")
	result.AlbumArtistSort = rawFirst("ALBUMARTISTSORT", "SOAA", "TSO2")
	result.TitleSort = rawFirst("TITLESORT", "SONM", "TSOT")
	result.AlbumSort = rawFirst("ALBUMSORT", "SOAL", "TSOA")

	if picture := parsed.Picture(); picture != nil && len(picture.Data) > 0 {
		result.Artwork = picture.Data
		result.ArtworkMIME = strings.TrimSpace(picture.MIMEType)
		result.ArtworkExt = extensionForMIME(result.ArtworkMIME)
	}
	result.DurationMillis = probeDuration(path, result.Container)

	return applyFallbacks(result, path), nil
}

// maxLeadingID3Tags bounds how many stacked ID3v2 tags are skipped before the
// FLAC stream marker; some taggers prepend more than one.
const maxLeadingID3Tags = 4

// seekFLACStream positions file at the "fLaC" marker. Some download tools
// prepend an ID3v2 tag to FLAC files; the audio stream is intact after it, so
// it is skipped (the same way probeFLAC already does) instead of rejecting
// the whole file.
func seekFLACStream(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	var offset int64
	for i := 0; i <= maxLeadingID3Tags; i++ {
		var header [10]byte
		n, _ := file.ReadAt(header[:], offset)
		if n >= 4 && string(header[:4]) == "fLaC" {
			_, err = file.Seek(offset+4, io.SeekStart)
			return err
		}
		if n < 10 || string(header[:3]) != "ID3" || i == maxLeadingID3Tags {
			return fmt.Errorf("invalid FLAC signature (file starts with %q at offset %d)", header[:min(n, 4)], offset)
		}
		next := offset + id3v2Offset(header[:], size-offset)
		if next <= offset || next > size-4 {
			return fmt.Errorf("invalid FLAC signature (ID3v2 tag at offset %d is malformed or truncated)", offset)
		}
		offset = next
	}
	return fmt.Errorf("invalid FLAC signature")
}

func readFLAC(file *os.File) (AudioMetadata, error) {
	if err := seekFLACStream(file); err != nil {
		return AudioMetadata{}, err
	}
	result := AudioMetadata{Container: "flac", MIMEType: "audio/flac", Raw: map[string][]string{}}
	for {
		var header [4]byte
		if _, err := io.ReadFull(file, header[:]); err != nil {
			return AudioMetadata{}, err
		}
		last := header[0]&0x80 != 0
		blockType := header[0] & 0x7f
		length := int(header[1])<<16 | int(header[2])<<8 | int(header[3])
		block := make([]byte, length)
		if _, err := io.ReadFull(file, block); err != nil {
			return AudioMetadata{}, err
		}
		switch blockType {
		case 0:
			result.DurationMillis = flacStreamInfoDuration(block)
		case 4:
			if err := parseVorbisComments(block, &result); err != nil {
				return AudioMetadata{}, err
			}
		case 6:
			parseFLACPicture(block, &result)
		}
		if last {
			break
		}
	}
	values := func(keys ...string) string {
		for _, key := range keys {
			if list := result.Raw[key]; len(list) > 0 {
				return list[0]
			}
		}
		return ""
	}
	result.Title = values("TITLE")
	result.Album = values("ALBUM")
	result.Artists = splitPeople(values("ARTIST"))
	result.AlbumArtists = splitPeople(values("ALBUMARTIST", "ALBUM ARTIST"))
	result.Composer = values("COMPOSER")
	result.Lyricist = values("LYRICIST")
	result.Arranger = values("ARRANGER")
	result.Producer = values("PRODUCER")
	result.Genres = splitValues(values("GENRE"))
	result.Lyrics = values("LYRICS", "UNSYNCEDLYRICS")
	date := values("DATE", "YEAR")
	if len(date) >= 4 {
		result.Year, _ = strconv.Atoi(date[:4])
	}
	result.TrackNumber, result.TrackTotal = parseNumberPair(values("TRACKNUMBER"), values("TRACKTOTAL", "TOTALTRACKS"))
	result.DiscNumber, result.DiscTotal = parseNumberPair(values("DISCNUMBER"), values("DISCTOTAL", "TOTALDISCS"))

	// Sort/reading keys for Japanese ordering
	result.ArtistSort = values("ARTISTSORT")
	result.AlbumArtistSort = values("ALBUMARTISTSORT")
	result.TitleSort = values("TITLESORT")
	result.AlbumSort = values("ALBUMSORT")

	return result, nil
}

func parseVorbisComments(block []byte, result *AudioMetadata) error {
	reader := bytes.NewReader(block)
	var vendorLength uint32
	if err := binary.Read(reader, binary.LittleEndian, &vendorLength); err != nil {
		return err
	}
	if int64(vendorLength) > int64(reader.Len()) {
		return fmt.Errorf("invalid Vorbis vendor length")
	}
	reader.Seek(int64(vendorLength), io.SeekCurrent)
	var count uint32
	if err := binary.Read(reader, binary.LittleEndian, &count); err != nil {
		return err
	}
	for i := uint32(0); i < count; i++ {
		var length uint32
		if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
			return err
		}
		if int64(length) > int64(reader.Len()) {
			return fmt.Errorf("invalid Vorbis comment length")
		}
		data := make([]byte, length)
		if _, err := io.ReadFull(reader, data); err != nil {
			return err
		}
		key, value, ok := strings.Cut(string(data), "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		result.Raw[key] = append(result.Raw[key], strings.TrimSpace(value))
	}
	return nil
}

func parseFLACPicture(block []byte, result *AudioMetadata) {
	reader := bytes.NewReader(block)
	readText := func() (string, bool) {
		var length uint32
		if binary.Read(reader, binary.BigEndian, &length) != nil || int64(length) > int64(reader.Len()) {
			return "", false
		}
		data := make([]byte, length)
		_, err := io.ReadFull(reader, data)
		return string(data), err == nil
	}
	var pictureType uint32
	if binary.Read(reader, binary.BigEndian, &pictureType) != nil {
		return
	}
	mimeType, ok := readText()
	if !ok {
		return
	}
	if _, ok = readText(); !ok {
		return
	}
	var ignored [4]uint32
	for i := range ignored {
		if binary.Read(reader, binary.BigEndian, &ignored[i]) != nil {
			return
		}
	}
	var dataLength uint32
	if binary.Read(reader, binary.BigEndian, &dataLength) != nil || int64(dataLength) > int64(reader.Len()) {
		return
	}
	data := make([]byte, dataLength)
	if _, err := io.ReadFull(reader, data); err != nil {
		return
	}
	if len(result.Artwork) == 0 || pictureType == 3 {
		result.Artwork = data
		result.ArtworkMIME = mimeType
		result.ArtworkExt = extensionForMIME(mimeType)
	}
}

func parseInvolvedPeopleRaw(raw map[string]interface{}) []InvolvedPerson {
	var people []InvolvedPerson
	for key, value := range raw {
		base, role, keyed := splitInvolvedKey(strings.TrimSpace(key))
		if base != "TIPL" && base != "TMCL" && base != "IPLS" {
			continue
		}
		if keyed {
			for _, name := range rawStringValues(value) {
				people = appendInvolved(people, role, name)
			}
			continue
		}
		people = append(people, parseInvolvedValue(value)...)
	}
	return dedupeInvolved(people)
}

func splitInvolvedKey(key string) (base, role string, keyed bool) {
	for _, separator := range []string{":", ".", "/", "_"} {
		if head, tail, ok := strings.Cut(key, separator); ok {
			upperHead := strings.ToUpper(strings.TrimSpace(head))
			if upperHead == "TIPL" || upperHead == "TMCL" || upperHead == "IPLS" {
				return upperHead, strings.TrimSpace(tail), strings.TrimSpace(tail) != ""
			}
		}
	}
	return strings.ToUpper(strings.TrimSpace(key)), "", false
}

func rawStringValues(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return typed
	case []any:
		var values []string
		for _, item := range typed {
			values = append(values, rawStringValues(item)...)
		}
		return values
	default:
		return SplitRawValue(value)
	}
}

func parseInvolvedValue(value any) []InvolvedPerson {
	// Some tag readers expose keyed role/name maps rather than the ID3 text list.
	switch typed := value.(type) {
	case map[string]string:
		var people []InvolvedPerson
		for role, name := range typed {
			people = appendInvolved(people, role, name)
		}
		return people
	case map[string][]string:
		var people []InvolvedPerson
		for role, names := range typed {
			for _, name := range names {
				people = appendInvolved(people, role, name)
			}
		}
		return people
	case map[string]interface{}:
		var people []InvolvedPerson
		for role, names := range typed {
			for _, name := range rawStringValues(names) {
				people = appendInvolved(people, role, name)
			}
		}
		return people
	}

	values := rawStringValues(value)
	var fields []string
	for _, value := range values {
		fields = append(fields, splitInvolvedFields(value)...)
	}
	var people []InvolvedPerson
	for i := 0; i+1 < len(fields); i += 2 {
		people = appendInvolved(people, fields[i], fields[i+1])
	}
	return people
}

func splitInvolvedFields(value string) []string {
	value = strings.Trim(value, "\x00 \t\r\n")
	if value == "" {
		return nil
	}
	for _, separator := range []string{"\x00", "\x00\x00", "\t", "\n"} {
		if strings.Contains(value, separator) {
			parts := strings.Split(value, separator)
			return compactStrings(parts)
		}
	}
	// Human-readable Raw implementations commonly render TIPL/TMCL as role/name pairs.
	if strings.Count(value, "/")%2 == 1 {
		return compactStrings(strings.Split(value, "/"))
	}
	return []string{value}
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(strings.Trim(value, "\x00")); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func appendInvolved(people []InvolvedPerson, role, name string) []InvolvedPerson {
	role = strings.TrimSpace(strings.Trim(role, "\x00"))
	name = strings.TrimSpace(strings.Trim(name, "\x00"))
	if role == "" || name == "" {
		return people
	}
	return append(people, InvolvedPerson{Role: role, Name: name})
}

func dedupeInvolved(people []InvolvedPerson) []InvolvedPerson {
	seen := make(map[string]struct{}, len(people))
	result := make([]InvolvedPerson, 0, len(people))
	for _, person := range people {
		key := Normalize(person.Role) + "\x00" + Normalize(person.Name)
		if person.Role == "" || person.Name == "" || key == "\x00" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, person)
	}
	return result
}

func applyInvolvedPeople(result *AudioMetadata) {
	firstForRoles := func(roles ...string) string {
		for _, person := range result.InvolvedPeople {
			role := Normalize(person.Role)
			for _, candidate := range roles {
				if role == candidate {
					return person.Name
				}
			}
		}
		return ""
	}
	if result.Lyricist == "" {
		result.Lyricist = firstForRoles("lyricist", "lyrics", "words", "text")
	}
	if result.Composer == "" {
		result.Composer = firstForRoles("composer", "composed by", "music")
	}
	if result.Arranger == "" {
		result.Arranger = firstForRoles("arranger", "arranged by", "orchestrator")
	}
	if result.Producer == "" {
		result.Producer = firstForRoles("producer", "produced by")
	}
}

func parseID3v2InvolvedPeople(r io.Reader) []InvolvedPerson {
	header := make([]byte, 10)
	if _, err := io.ReadFull(r, header); err != nil || string(header[:3]) != "ID3" {
		return nil
	}
	version := header[3]
	if version != 3 && version != 4 {
		return nil
	}
	tagSize, ok := synchsafeSize(header[6:10])
	if !ok || tagSize <= 0 || tagSize > 32<<20 {
		return nil
	}
	body := make([]byte, tagSize)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil
	}
	tagUnsynchronised := header[5]&0x80 != 0
	offset := 0
	if header[5]&0x40 != 0 {
		if len(body) < 4 {
			return nil
		}
		if version == 3 {
			extended := int(binary.BigEndian.Uint32(body[:4]))
			if extended < 0 || extended > len(body)-4 {
				return nil
			}
			offset = 4 + extended
		} else {
			extended, valid := synchsafeSize(body[:4])
			if !valid || extended < 4 || extended > len(body) {
				return nil
			}
			offset = extended
		}
	}

	var people []InvolvedPerson
	for offset+10 <= len(body) {
		frameHeader := body[offset : offset+10]
		if bytes.Equal(frameHeader[:4], []byte{0, 0, 0, 0}) {
			break
		}
		frameID := string(frameHeader[:4])
		if !validID3FrameID(frameID) {
			break
		}
		var frameSize int
		if version == 4 {
			var valid bool
			frameSize, valid = synchsafeSize(frameHeader[4:8])
			if !valid {
				break
			}
		} else {
			frameSize = int(binary.BigEndian.Uint32(frameHeader[4:8]))
		}
		offset += 10
		if frameSize <= 0 || frameSize > len(body)-offset {
			break
		}
		payload := body[offset : offset+frameSize]
		offset += frameSize
		if tagUnsynchronised {
			payload = removeID3Unsynchronisation(payload)
		}
		if !isInvolvedPeopleFrame(version, frameID) {
			continue
		}
		flags := frameHeader[9]
		if (version == 3 && flags&0xc0 != 0) || (version == 4 && flags&0x0c != 0) {
			continue // compressed/encrypted data is deliberately unsupported
		}
		if version == 3 && flags&0x20 != 0 {
			if len(payload) < 1 {
				continue
			}
			payload = payload[1:]
		}
		if version == 4 {
			if flags&0x40 != 0 {
				if len(payload) < 1 {
					continue
				}
				payload = payload[1:]
			}
			if flags&0x01 != 0 {
				if len(payload) < 4 {
					continue
				}
				payload = payload[4:]
			}
			if flags&0x02 != 0 && !tagUnsynchronised {
				payload = removeID3Unsynchronisation(payload)
			}
		}
		fields, valid := decodeID3TextFields(payload)
		if !valid {
			continue
		}
		for i := 0; i+1 < len(fields); i += 2 {
			people = appendInvolved(people, fields[i], fields[i+1])
		}
	}
	return dedupeInvolved(people)
}

func isInvolvedPeopleFrame(version byte, frameID string) bool {
	if version == 3 {
		return frameID == "IPLS"
	}
	return version == 4 && (frameID == "TIPL" || frameID == "TMCL")
}

func synchsafeSize(value []byte) (int, bool) {
	if len(value) != 4 || value[0]&0x80 != 0 || value[1]&0x80 != 0 || value[2]&0x80 != 0 || value[3]&0x80 != 0 {
		return 0, false
	}
	return int(value[0])<<21 | int(value[1])<<14 | int(value[2])<<7 | int(value[3]), true
}

func validID3FrameID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for _, char := range []byte(id) {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func removeID3Unsynchronisation(value []byte) []byte {
	result := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		result = append(result, value[i])
		if value[i] == 0xff && i+1 < len(value) && value[i+1] == 0x00 {
			i++
		}
	}
	return result
}

func decodeID3TextFields(payload []byte) ([]string, bool) {
	if len(payload) == 0 || payload[0] > 3 {
		return nil, false
	}
	encoding := payload[0]
	data := payload[1:]
	var text string
	switch encoding {
	case 0:
		runes := make([]rune, len(data))
		for i, value := range data {
			runes[i] = rune(value)
		}
		text = string(runes)
	case 3:
		if !utf8.Valid(data) {
			return nil, false
		}
		text = string(data)
	case 1, 2:
		littleEndian := false
		if encoding == 1 {
			if len(data) < 2 {
				return nil, false
			}
			switch {
			case data[0] == 0xff && data[1] == 0xfe:
				littleEndian = true
			case data[0] == 0xfe && data[1] == 0xff:
			default:
				return nil, false
			}
			data = data[2:]
		}
		if len(data)%2 != 0 {
			return nil, false
		}
		units := make([]uint16, 0, len(data)/2)
		for len(data) >= 2 {
			if littleEndian {
				units = append(units, binary.LittleEndian.Uint16(data[:2]))
			} else {
				units = append(units, binary.BigEndian.Uint16(data[:2]))
			}
			data = data[2:]
		}
		text = string(utf16.Decode(units))
	}
	return compactStrings(strings.Split(strings.Trim(text, "\x00"), "\x00")), true
}

func parseNumberPair(number, total string) (int, int) {
	parse := func(value string) int {
		value, _, _ = strings.Cut(value, "/")
		v, _ := strconv.Atoi(strings.TrimSpace(value))
		return v
	}
	n := parse(number)
	t := parse(total)
	if t == 0 {
		_, tail, ok := strings.Cut(number, "/")
		if ok {
			t = parse(tail)
		}
	}
	return n, t
}

func applyFallbacks(result AudioMetadata, path string) AudioMetadata {
	if result.TrackNumber < 0 {
		result.TrackNumber = 0
	}
	if result.DiscNumber <= 0 {
		result.DiscNumber = 1
	}
	if result.DiscTotal <= 0 {
		result.DiscTotal = 1
	}
	if result.MIMEType == "" {
		result.MIMEType = fallbackMIME(result.Container)
	}
	if result.Title == "" {
		result.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if result.Album == "" {
		result.Album = "Unknown Album"
	}
	if len(result.Artists) == 0 {
		result.Artists = append([]string(nil), result.AlbumArtists...)
	}
	if len(result.Artists) == 0 {
		result.Artists = []string{"Unknown Artist"}
	}
	if len(result.AlbumArtists) == 0 {
		result.AlbumArtists = append([]string(nil), result.Artists...)
	}
	// Infer track type from title, folder name, and tags
	if result.TrackType == "" {
		result.TrackType = InferTrackType(result.Title, path, result.Raw)
	}
	return result
}

// InferTrackType determines the track type from the title, file path, and raw tags.
// Returns one of: regular, instrumental, off_vocal, tv_size, drama_track, remix.
func InferTrackType(title, path string, raw map[string][]string) string {
	lower := strings.ToLower(title)
	folderName := strings.ToLower(filepath.Base(filepath.Dir(path)))

	// Check raw tags for content type hints
	for _, key := range []string{"CONTENTTYPE", "CONTENT TYPE"} {
		if vals, ok := raw[key]; ok {
			for _, v := range vals {
				vl := strings.ToLower(v)
				if strings.Contains(vl, "instrumental") {
					return "instrumental"
				}
				if strings.Contains(vl, "drama") {
					return "drama_track"
				}
				if strings.Contains(vl, "remix") {
					return "remix"
				}
			}
		}
	}

	// Title-based patterns (order matters: more specific first)
	titlePatterns := []struct {
		trackType string
		patterns  []string
	}{
		{"off_vocal", []string{
			"(off vocal)", "(off-vocal)", "(offvocal)",
			"[off vocal]", "[off-vocal]", "[offvocal]",
			"(backing track)", "[backing track]",
			"(minus one)", "[minus one]",
			"(カラオケ)", "[カラオケ]", "(からおけ)",
			"(off vo)", "[off vo]",
		}},
		{"instrumental", []string{
			"(instrumental)", "[instrumental]",
			"(inst)", "[inst]",
			"(inst.)", "[inst.]",
			" instrumental", // trailing
		}},
		{"tv_size", []string{
			"(tv size)", "[tv size]",
			"(tv ver.)", "[tv ver.]",
			"(tv ver)", "[tv ver]",
			"(tv version)", "[tv version]",
			"(tv edit)", "[tv edit]",
			"(anime ver.)", "[anime ver.]",
			"(anime ver)", "[anime ver]",
			"(anime version)", "[anime version]",
			"(short ver.)", "[short ver.]",
			"(short ver)", "[short ver]",
			"(short version)", "[short version]",
			"(tvサイズ)", "[tvサイズ]",
		}},
		{"drama_track", []string{
			"(drama)", "[drama]",
			"(ドラマ)", "[ドラマ]",
			"(skit)", "[skit]",
			"(寸劇)", "[寸劇]",
		}},
		{"remix", []string{
			"(remix)", "[remix]",
			"(remixed)", "[remixed]",
			" remix",
		}},
	}

	for _, group := range titlePatterns {
		for _, pattern := range group.patterns {
			if strings.Contains(lower, pattern) {
				return group.trackType
			}
		}
	}

	// Folder-based detection
	folderPatterns := map[string]string{
		"instrumental":  "instrumental",
		"instrumentals": "instrumental",
		"off vocal":     "off_vocal",
		"off-vocal":     "off_vocal",
		"offvocal":      "off_vocal",
		"カラオケ":          "off_vocal",
		"backing track": "off_vocal",
	}
	for pattern, trackType := range folderPatterns {
		if folderName == pattern || strings.Contains(folderName, pattern) {
			return trackType
		}
	}

	return "regular"
}

func Normalize(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return unicode.ToLower(r)
	}, strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func SplitRawValue(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return typed
	case int:
		return []string{strconv.Itoa(typed)}
	case int64:
		return []string{strconv.FormatInt(typed, 10)}
	case fmt.Stringer:
		return []string{typed.String()}
	default:
		return []string{fmt.Sprint(value)}
	}
}

func flattenRaw(raw map[string]interface{}) map[string][]string {
	result := make(map[string][]string, len(raw))
	for key, value := range raw {
		values := SplitRawValue(value)
		for index := range values {
			values[index] = strings.TrimSpace(values[index])
		}
		result[strings.ToUpper(strings.TrimSpace(key))] = values
	}
	return result
}

// SplitPeople splits a multi-person string on semicolons, slashes, and Japanese commas.
func SplitPeople(value string) []string {
	return splitOnSeparators(value, []string{";", " / ", "、"})
}

func splitPeople(value string) []string {
	return SplitPeople(value)
}

func splitValues(value string) []string {
	return splitOnSeparators(value, []string{";", ",", "/", "、"})
}

func splitOnSeparators(value string, separators []string) []string {
	parts := []string{value}
	for _, separator := range separators {
		var next []string
		for _, part := range parts {
			next = append(next, strings.Split(part, separator)...)
		}
		parts = next
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		key := Normalize(part)
		if part == "" || key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, part)
	}
	return result
}

func fallbackMIME(container string) string {
	switch container {
	case "flac":
		return "audio/flac"
	case "mp3":
		return "audio/mpeg"
	case "m4a", "mp4", "aac":
		return "audio/mp4"
	case "ogg", "oga":
		return "audio/ogg"
	case "opus":
		return "audio/opus"
	default:
		return "application/octet-stream"
	}
}

func extensionForMIME(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".img"
	}
}
