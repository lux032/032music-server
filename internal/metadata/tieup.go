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
func isGenericWorkTitle(value string) bool {
	upper := strings.ToUpper(strings.TrimSpace(value))
	generic := map[string]bool{"ANIME": true, "TV ANIME": true, "GAME": true, "DRAMA": true, "MOVIE": true, "OST": true, "ORIGINAL SOUNDTRACK": true, "SOUNDTRACK": true}
	return generic[upper]
}
