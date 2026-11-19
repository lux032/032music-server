package metadata

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

var textJoinPhrase = regexp.MustCompile(`(?i)(?:[^\pL\pN]+|[\pZ\s]+(?:feat\.?|featuring|ft\.?|vs\.?|versus|and|with|x|pres\.?|presents)[\pZ\s]+)`)

// ArtistJoinPhrases returns the exact text preceding each artist in a
// validated display credit. The first entry is always empty.
func ArtistJoinPhrases(raw map[string][]string, names []string, album bool) []string {
	if len(names) < 2 {
		return nil
	}
	displayKeys := []string{"ARTIST", "TPE1", "TP1", "\xa9ART", "©ART"}
	sourceKey := "MUSICBRAINZ_ARTIST_CREDIT"
	if album {
		displayKeys = []string{"ALBUMARTIST", "ALBUM ARTIST", "TPE2", "TP2", "AART"}
		sourceKey = "MUSICBRAINZ_ALBUM_ARTIST_CREDIT"
	}
	values := tagValues(raw, displayKeys...)
	if len(values) != 1 {
		return nil
	}
	display := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(values[0]), "\ufeff"))
	if display == "" || strings.ContainsFunc(display, unicode.IsControl) {
		return nil
	}
	if source := tagValues(raw, sourceKey); len(source) == 1 {
		var credit struct {
			Display string   `json:"display"`
			Names   []string `json:"names"`
			Before  []string `json:"before"`
		}
		if json.Unmarshal([]byte(source[0]), &credit) == nil && credit.Display == display &&
			len(credit.Names) == len(names) && len(credit.Before) == len(names) && credit.Before[0] == "" {
			valid := true
			for i, name := range names {
				valid = valid && strings.EqualFold(credit.Names[i], name) &&
					!strings.ContainsFunc(credit.Before[i], unicode.IsControl)
			}
			if valid {
				return credit.Before
			}
		}
	}

	// Legacy tags may only contain the joined text. Anchor every known name
	// and only accept recognized joins so an extra credited name can't be
	// mistaken for part of a connector.
	var pattern strings.Builder
	pattern.WriteString("(?i)^")
	for i, name := range names {
		if i > 0 {
			pattern.WriteString("(" + textJoinPhrase.String() + ")")
		}
		pattern.WriteString(regexp.QuoteMeta(name))
	}
	pattern.WriteByte('$')
	matcher, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil
	}
	if match := matcher.FindStringSubmatch(display); match != nil {
		match[0] = ""
		return match
	}
	return nil
}
