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

	"github.com/dhowden/tag"
)

type AudioMetadata struct {
	Title          string
	Album          string
	Artists        []string
	AlbumArtists   []string
	Composer       string
	Lyricist       string
	Arranger       string
	Producer       string
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

	result := AudioMetadata{
		Title:        strings.TrimSpace(parsed.Title()),
		Album:        strings.TrimSpace(parsed.Album()),
		Composer:     strings.TrimSpace(parsed.Composer()),
		Lyrics:       strings.TrimSpace(parsed.Lyrics()),
		Year:         parsed.Year(),
		Container:    strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		MIMEType:     mime.TypeByExtension(strings.ToLower(filepath.Ext(path))),
		Raw:          flattenRaw(parsed.Raw()),
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

func readFLAC(file *os.File) (AudioMetadata, error) {
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil || string(magic[:]) != "fLaC" {
		return AudioMetadata{}, fmt.Errorf("invalid FLAC signature")
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
		"カラオケ":         "off_vocal",
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
