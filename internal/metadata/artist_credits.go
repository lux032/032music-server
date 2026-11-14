package metadata

import (
	"sort"
	"strings"
)

// ArtistNames prefers a structured list over a display credit. Legacy
// MusicAutoTagger files join names with comma-space and MBIDs with semicolons.
// Recover that representation only when all IDs are valid and counts agree.
func ArtistNames(raw map[string][]string, fallback []string) []string {
	return creditedNames(raw, fallback, []string{"ARTISTS"}, []string{"ARTIST", "TPE1", "TP1", "\xa9ART", "©ART"}, []string{"MUSICBRAINZ_ARTISTID", "MUSICBRAINZ ARTIST ID"})
}

// AlbumArtistNames never substitutes recording artists for release artists.
func AlbumArtistNames(raw map[string][]string, fallback []string) []string {
	return creditedNames(raw, fallback, []string{"ALBUMARTISTS", "ALBUM_ARTISTS", "ALBUM ARTISTS"}, []string{"ALBUMARTIST", "ALBUM ARTIST", "TPE2", "TP2", "AART"}, []string{"MUSICBRAINZ_ALBUMARTISTID", "MUSICBRAINZ ALBUM ARTIST ID"})
}

// tagValues accepts Vorbis keys, ID3 descriptions and MP4 reverse-DNS atoms.
// Sorting makes aliases deterministic even when the reader returns a map.
func tagValues(raw map[string][]string, aliases ...string) []string {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, alias := range aliases {
		for _, key := range keys {
			normalized := strings.ToUpper(strings.TrimSpace(key))
			if i := strings.LastIndex(normalized, ":"); i >= 0 {
				normalized = normalized[i+1:]
			}
			if normalized == alias && len(raw[key]) > 0 {
				return raw[key]
			}
		}
	}
	return nil
}

func creditedNames(raw map[string][]string, fallback, structured, display, mbidKeys []string) []string {
	if values := tagValues(raw, structured...); len(values) > 0 {
		// An explicit ARTISTS value is one person even when the name contains
		// punctuation. Only NUL and semicolon encode multiple names here.
		if names := cleanPeople(values, false); len(names) > 0 {
			return names
		}
	}
	values := tagValues(raw, display...)
	if len(values) == 0 {
		values = fallback
	}
	names := cleanPeople(values, true)
	ids := ArtistMusicBrainzIDs(raw, mbidKeys...)
	if len(names) == 1 && len(ids) > 1 {
		parts := strings.Split(names[0], ", ")
		if len(parts) == len(ids) {
			recovered := cleanPeople(parts, false)
			if len(recovered) == len(ids) {
				return recovered
			}
		}
	}
	if len(names) > 0 {
		return names
	}
	return fallback
}

func cleanPeople(values []string, legacy bool) []string {
	var names []string
	seen := map[string]bool{}
	for _, value := range values {
		separators := []string{"\x00", ";"}
		if legacy {
			separators = append(separators, " / ", "、")
		}
		for _, name := range splitOnSeparators(value, separators) {
			// ID3 UTF-16 values may each repeat the byte-order mark.
			name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "\ufeff"))
			key := Normalize(name)
			if key != "" && !seen[key] {
				names = append(names, name)
				seen[key] = true
			}
		}
	}
	return names
}

// ArtistMusicBrainzIDs retains order and rejects the entire list on malformed,
// empty or duplicate identities rather than shifting the name/ID alignment.
func ArtistMusicBrainzIDs(raw map[string][]string, aliases ...string) []string {
	var result []string
	seen := map[string]bool{}
	for _, value := range tagValues(raw, aliases...) {
		value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "\ufeff"))
		for _, part := range strings.Split(strings.NewReplacer("\x00", ";", "/", ";").Replace(value), ";") {
			id, valid := NormalizeMusicBrainzID(part)
			if !valid || id == "00000000-0000-0000-0000-000000000000" || seen[id] {
				return nil
			}
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

// CompositeArtistCredit flags display credits, not a license to split them.
func CompositeArtistCredit(name string) bool {
	lower := strings.ToLower(name)
	for _, separator := range []string{", ", " feat.", " feat ", " featuring ", " ft.", " ft ", " & ", " / ", "、", ";"} {
		if strings.Contains(lower, separator) {
			return true
		}
	}
	return false
}
