package metadata

import (
	"regexp"
	"sort"
	"strings"
)

var musicBrainzUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func NormalizeMusicBrainzID(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	return value, musicBrainzUUID.MatchString(value)
}

// MusicBrainzFileIDs accepts only the two verified reader spellings per ID.
// All distinct valid values are retained: multiple RGs are conflict evidence.
func MusicBrainzFileIDs(raw map[string][]string) (groups, releases []string) {
	g, r := map[string]bool{}, map[string]bool{}
	for key, values := range raw {
		var dest map[string]bool
		switch strings.ToUpper(strings.TrimSpace(key)) {
		case "MUSICBRAINZ_RELEASEGROUPID", "MUSICBRAINZ RELEASE GROUP ID":
			dest = g
		case "MUSICBRAINZ_ALBUMID", "MUSICBRAINZ ALBUM ID":
			dest = r
		default:
			continue
		}
		for _, value := range values {
			if id, ok := NormalizeMusicBrainzID(value); ok {
				dest[id] = true
			}
		}
	}
	for id := range g {
		groups = append(groups, id)
	}
	for id := range r {
		releases = append(releases, id)
	}
	sort.Strings(groups)
	sort.Strings(releases)
	return
}
