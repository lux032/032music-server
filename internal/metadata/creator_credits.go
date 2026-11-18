package metadata

import "strings"

// CreatorNames uses structured credits by default. Comma splitting is allowed
// only for an explicitly requested legacy conversion, never ordinary scans.
func CreatorNames(raw map[string][]string, role, fallback string, legacy bool) []string {
	var structured, fields []string
	switch role {
	case "composer":
		structured = []string{"COMPOSERS"}
		fields = []string{"COMPOSER", "TCOM", "TCM", "\xa9WRT", "©WRT"}
	case "lyricist":
		structured = []string{"LYRICISTS"}
		fields = []string{"LYRICIST", "TEXT", "TXT"}
	case "arranger":
		structured = []string{"ARRANGERS"}
		fields = []string{"ARRANGER"}
	case "producer":
		structured = []string{"PRODUCERS"}
		fields = []string{"PRODUCER"}
	default:
		return nil
	}
	if values := tagValues(raw, structured...); len(values) > 0 {
		return cleanPeople(values, false)
	}
	values := tagValues(raw, fields...)
	if len(values) == 0 {
		values = []string{fallback}
	}
	if len(values) > 1 {
		return cleanPeople(values, false)
	}
	// NUL and semicolon are explicit list separators, including flattened MP4.
	if len(values) == 1 && strings.ContainsAny(values[0], "\x00;") {
		return cleanPeople(values, false)
	}
	if !legacy {
		return cleanPeople(values, true)
	}
	var names []string
	for _, value := range values {
		names = append(names, splitOnSeparators(value, []string{",", "，", ";", " / ", "、"})...)
	}
	return cleanPeople(names, false)
}

// CreatorMusicBrainzIDs returns aligned MusicBrainz artist IDs for a creator role.
func CreatorMusicBrainzIDs(raw map[string][]string, role string) []string {
	switch role {
	case "composer":
		return ArtistMusicBrainzIDs(raw, "MUSICBRAINZ_COMPOSERID", "MUSICBRAINZ COMPOSER ID")
	case "lyricist":
		return ArtistMusicBrainzIDs(raw, "MUSICBRAINZ_LYRICISTID", "MUSICBRAINZ LYRICIST ID")
	case "arranger":
		return ArtistMusicBrainzIDs(raw, "MUSICBRAINZ_ARRANGERID", "MUSICBRAINZ ARRANGER ID")
	case "producer":
		return ArtistMusicBrainzIDs(raw, "MUSICBRAINZ_PRODUCERID", "MUSICBRAINZ PRODUCER ID")
	default:
		return nil
	}
}
