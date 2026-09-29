package metadata

import (
	"regexp"
	"strings"
)

// WorkAssociation records a recognized title, medium and musical role.
type WorkAssociation struct {
	Title    string
	Type     string
	Role     string
	Season   int
	Sequence int
}

var rolePattern = regexp.MustCompile(`(?i)(?:\bOP(?:ENING)?|オープニング|主題歌|\bED(?:ENDING)?|エンディング|挿入歌|劇中歌|INSERT(?: SONG)?|IMAGE SONG|イメージソング|CHARACTER SONG|キャラクターソング|キャラソン|ORIGINAL SOUNDTRACK|SOUNDTRACK|\bOST\b)\s*([0-9]+)?`)

func inferRoleOnly(value string) string {
	upper := strings.ToUpper(value)
	switch {
	case strings.Contains(upper, "CHARACTER"), strings.Contains(value, "キャラクター"), strings.Contains(value, "キャラソン"):
		return "character"
	case strings.Contains(upper, "IMAGE"), strings.Contains(value, "イメージ"):
		return "image_song"
	case strings.Contains(upper, "SOUNDTRACK"), strings.Contains(upper, "OST"), strings.Contains(value, "サウンドトラック"), strings.Contains(value, "サントラ"):
		return "ost"
	case strings.Contains(upper, "INSERT"), strings.Contains(value, "挿入歌"), strings.Contains(value, "劇中歌"):
		return "insert"
	case strings.Contains(upper, "OPENING"), strings.Contains(value, "オープニング"), strings.HasPrefix(upper, "OP"):
		return "op"
	case strings.Contains(upper, "ENDING"), strings.Contains(value, "エンディング"), strings.HasPrefix(upper, "ED"):
		return "ed"
	case strings.Contains(upper, "THEME"), strings.Contains(value, "主題歌"), strings.Contains(value, "テーマソング"):
		return "theme"
	default:
		return "other"
	}
}

// isGenericWorkTitleV5 is the frozen v5 check: a fixed set of whole-string
// matches. Kept only for the D71 rule-upgrade carryover.
func isGenericWorkTitleV5(value string, _ bool) bool {
	upper := strings.ToUpper(strings.TrimSpace(value))
	generic := map[string]bool{"ANIME": true, "TV ANIME": true, "GAME": true, "DRAMA": true, "MOVIE": true, "OST": true, "ORIGINAL SOUNDTRACK": true, "SOUNDTRACK": true}
	return generic[upper]
}

// genericWorkWords are the words that, on their own, describe a release rather
// than a work (D70). The v6 check is word-based so leftover fragments like
// "Album" (from "Soundtrack Album") or "Vol.2" are rejected too. Roman
// numerals are deliberately absent: a single-letter work like "X" must survive.
var genericWorkWords = map[string]bool{
	"ANIME": true, "TV": true, "GAME": true, "DRAMA": true, "MOVIE": true,
	"OST": true, "ORIGINAL": true, "OFFICIAL": true, "SOUNDTRACK": true, "SOUNDTRACKS": true,
	"ALBUM": true, "COMPLETE": true, "VOCAL": true, "MINI": true, "BEST": true,
	"COLLECTION": true, "SELECTION": true, "MUSIC": true, "BGM": true,
	"EP": true, "SINGLE": true, "EDITION": true, "DELUXE": true, "LIMITED": true,
	"SPECIAL": true, "BOX": true, "SET": true, "THE": true, "&": true, "-": true,
	"アルバム": true, "ボーカル": true, "コンプリート": true, "劇伴": true,
}

var genericWorkVolumeWord = regexp.MustCompile(`(?i)^(?:VOL|DISC|DISK|CD)\.?\d*$`)

func isDigitsOnly(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

// isGenericWorkTitle reports whether every word of the leftover is a generic
// release word (or a volume/disc token), i.e. keyword removal consumed the
// whole title and there is no work name to infer. Pure digits count as generic
// only when digitsAreGeneric is set — the "keyword at the start, residue only"
// branch ("OST 2" → "2"); in the quote and marker-cut branches a pure-digit
// title is a valid work name (86, 1917 — the v5 behavior).
func isGenericWorkTitle(value string, digitsAreGeneric bool) bool {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return true
	}
	for _, field := range fields {
		if genericWorkWords[strings.ToUpper(field)] || genericWorkVolumeWord.MatchString(field) || digitsAreGeneric && isDigitsOnly(field) {
			continue
		}
		return false
	}
	return true
}
