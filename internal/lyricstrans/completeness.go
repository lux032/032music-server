package lyricstrans

import (
	"strings"
	"unicode"
)

// incompleteLines is deliberately conservative: script is not a language
// detector. Copied English sentences and kana are checked for Chinese targets;
// names, isolated words, vocalizations and already-Chinese lines remain allowed.
// Empty translations are invalid for every target language.
func incompleteLines(source, translated []string, target string) []int {
	var missing []int
	tag := strings.ToLower(target)
	chinese := tag == "zh" || strings.HasPrefix(tag, "zh-")
	for i, src := range source {
		dst := strings.TrimSpace(translated[i])
		if dst == "" || (chinese && normalizedText(src) == normalizedText(dst) && foreignSentence(src)) {
			missing = append(missing, i+1)
		}
	}
	return missing
}

func normalizedText(s string) string {
	return strings.ToLower(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s))
}

func foreignSentence(s string) bool {
	kana, han := false, false
	for _, r := range s {
		kana = kana || unicode.In(r, unicode.Hiragana, unicode.Katakana)
		han = han || unicode.Is(unicode.Han, r)
	}
	if kana {
		return true
	}
	if han {
		return false
	} // Chinese may legitimately retain English names.
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) })
	if len(words) < 3 {
		return false
	}
	// Common grammatical words distinguish sentences from most names and
	// non-lexical refrains. This is a leak detector, not universal language ID.
	for _, word := range words {
		switch word {
		case "i", "you", "we", "they", "he", "she", "it", "the", "a", "an", "my", "your", "our", "their", "is", "are", "was", "were", "have", "has", "don", "doesn", "didn", "won", "can", "cannot", "will", "would", "should":
			return true
		}
	}
	return false
}
