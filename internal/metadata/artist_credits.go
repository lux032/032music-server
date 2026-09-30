package metadata

import "strings"

// ArtistNames prefers structured artist lists over a formatted display credit.
// Never split commas or ampersands heuristically: both occur in real names.
func ArtistNames(raw map[string][]string, fallback []string) []string {
	for _, key := range []string{"ARTISTS", "ARTIST"} {
		values := raw[key]
		if len(values) == 0 {
			continue
		}
		names := []string{}
		seen := map[string]bool{}
		for _, value := range values {
			for _, name := range SplitPeople(value) {
				name = strings.TrimSpace(name)
				normalized := Normalize(name)
				if normalized != "" && !seen[normalized] {
					names = append(names, name)
					seen[normalized] = true
				}
			}
		}
		if len(names) > 0 {
			return names
		}
	}
	return fallback
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
