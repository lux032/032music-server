package metadata

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// WorkAssociation is a work/tie-up inferred from embedded tags or the file path.
type WorkAssociation struct {
	Title    string
	Type     string
	Role     string
	Season   int
	Sequence int
}

var (
	rolePattern          = regexp.MustCompile(`(?i)(?:\bOP(?:ENING)?|オープニング|主題歌|\bED(?:ENDING)?|エンディング|挿入歌|劇中歌|INSERT(?: SONG)?|IMAGE SONG|イメージソング|CHARACTER SONG|キャラクターソング|キャラソン|ORIGINAL SOUNDTRACK|SOUNDTRACK|\bOST\b)\s*([0-9]+)?`)
	seasonPattern        = regexp.MustCompile(`(?i)(?:SEASON|第)\s*([0-9]+)\s*(?:期|季)?`)
	quotedWorkPattern    = regexp.MustCompile(`[「『【](.+?)[」』】]`)
	separatorPattern     = regexp.MustCompile(`\s*(?:[-–—:：|｜/／])\s*`)
	cleanupPrefixPattern = regexp.MustCompile(`(?i)^(?:TV\s*)?(?:ANIME|アニメ|テレビアニメ|TVアニメ|劇場版|映画|MOVIE|GAME|ゲーム|ドラマ|DRAMA)\s*`)
	cleanupSuffixPattern = regexp.MustCompile(`(?i)\s*(?:ORIGINAL\s+SOUNDTRACK|SOUNDTRACK|OST|オリジナル\s*サウンドトラック|サウンドトラック|主題歌|テーマソング|THEME\s*SONG|OP(?:ENING)?|ED(?:ENDING)?|オープニング|エンディング|挿入歌|劇中歌|INSERT(?:\s+SONG)?|IMAGE\s+SONG|イメージソング|CHARACTER\s+SONG|キャラクターソング|キャラソン)\s*[0-9]*\s*$`)
)

// InferWorkAssociations aggressively infers a work and track role. Explicit
// CONTENTGROUP/SUBTITLE tags are preferred, followed by album and folder names.
func InferWorkAssociations(title, album, path string, raw map[string][]string) []WorkAssociation {
	candidates := make([]string, 0, 8)
	for _, key := range []string{"WORK", "WORKTITLE", "CONTENTGROUP", "GROUPING", "SUBTITLE", "DESCRIPTION"} {
		candidates = append(candidates, raw[key]...)
	}
	candidates = append(candidates, album)
	folder := filepath.Base(filepath.Dir(path))
	parent := filepath.Base(filepath.Dir(filepath.Dir(path)))
	candidates = append(candidates, folder, parent)

	seen := map[string]struct{}{}
	result := make([]WorkAssociation, 0, 2)
	for _, candidate := range candidates {
		association, ok := inferWorkAssociation(candidate, title)
		if !ok {
			continue
		}
		key := Normalize(association.Title) + "|" + association.Role + "|" + strconv.Itoa(association.Season) + "|" + strconv.Itoa(association.Sequence)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, association)
		// Explicit sources should win over noisy album/folder inference.
		if len(result) == 1 && candidate != album && candidate != folder && candidate != parent {
			return result
		}
	}
	return result
}

func inferWorkAssociation(value, trackTitle string) (WorkAssociation, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return WorkAssociation{}, false
	}
	combined := value + " " + trackTitle
	role, sequence := inferWorkRole(combined)
	workType := inferWorkType(value, role)
	if role == "other" && workType == "other" && quotedWorkPattern.FindStringSubmatch(value) == nil {
		return WorkAssociation{}, false
	}

	season := 0
	if match := seasonPattern.FindStringSubmatch(value); len(match) > 1 {
		season, _ = strconv.Atoi(match[1])
	}

	workTitle := value
	if match := quotedWorkPattern.FindStringSubmatch(value); len(match) > 1 {
		workTitle = match[1]
	} else {
		parts := separatorPattern.Split(value, -1)
		for _, part := range parts {
			if _, seq := inferWorkRole(part); seq > 0 || inferRoleOnly(part) != "other" {
				continue
			}
			if cleaned := cleanWorkTitle(part); cleaned != "" {
				workTitle = cleaned
				break
			}
		}
	}
	workTitle = cleanWorkTitle(workTitle)
	if workTitle == "" || isGenericWorkTitle(workTitle) {
		return WorkAssociation{}, false
	}
	return WorkAssociation{Title: workTitle, Type: workType, Role: role, Season: season, Sequence: sequence}, true
}

func inferWorkRole(value string) (string, int) {
	match := rolePattern.FindStringSubmatch(value)
	if len(match) == 0 {
		return "other", 0
	}
	sequence := 0
	if len(match) > 1 {
		sequence, _ = strconv.Atoi(match[1])
	}
	return inferRoleOnly(match[0]), sequence
}

func inferRoleOnly(value string) string {
	upper := strings.ToUpper(value)
	switch {
	case strings.Contains(upper, "CHARACTER"), strings.Contains(value, "キャラクター"), strings.Contains(value, "キャラソン"):
		return "character"
	case strings.Contains(upper, "IMAGE"), strings.Contains(value, "イメージ"):
		return "image_song"
	case strings.Contains(upper, "SOUNDTRACK"), strings.Contains(upper, "OST"), strings.Contains(value, "サウンドトラック"):
		return "ost"
	case strings.Contains(upper, "INSERT"), strings.Contains(value, "挿入歌"), strings.Contains(value, "劇中歌"):
		return "insert"
	case strings.Contains(upper, "OPENING"), strings.Contains(value, "オープニング"), hasToken(upper, "OP"):
		return "op"
	case strings.Contains(upper, "ENDING"), strings.Contains(value, "エンディング"), hasToken(upper, "ED"):
		return "ed"
	case strings.Contains(upper, "THEME"), strings.Contains(value, "主題歌"):
		return "theme"
	default:
		return "other"
	}
}

func inferWorkType(value, role string) string {
	upper := strings.ToUpper(value)
	switch {
	case strings.Contains(upper, "GAME"), strings.Contains(value, "ゲーム"):
		return "game"
	case strings.Contains(upper, "DRAMA"), strings.Contains(value, "ドラマ"):
		return "drama"
	case strings.Contains(upper, "MOVIE"), strings.Contains(value, "映画"), strings.Contains(value, "劇場版"):
		return "movie"
	case strings.Contains(upper, "CM"), strings.Contains(upper, "COMMERCIAL"), strings.Contains(value, "広告"):
		return "commercial"
	case strings.Contains(upper, "ANIME"), strings.Contains(value, "アニメ"), role == "op", role == "ed", role == "insert", role == "character", role == "image_song":
		return "anime"
	default:
		return "other"
	}
}

func cleanWorkTitle(value string) string {
	value = cleanupPrefixPattern.ReplaceAllString(strings.TrimSpace(value), "")
	value = cleanupSuffixPattern.ReplaceAllString(value, "")
	value = seasonPattern.ReplaceAllString(value, "")
	value = strings.Trim(value, " \t-_–—:：|｜/／[]()（）「」『』【】")
	return strings.Join(strings.Fields(value), " ")
}

func isGenericWorkTitle(value string) bool {
	upper := strings.ToUpper(strings.TrimSpace(value))
	generic := map[string]struct{}{"ANIME": {}, "TV ANIME": {}, "GAME": {}, "DRAMA": {}, "MOVIE": {}, "OST": {}, "ORIGINAL SOUNDTRACK": {}, "SOUNDTRACK": {}}
	_, ok := generic[upper]
	return ok
}

func hasToken(value, token string) bool {
	for _, field := range strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if field == token || strings.HasPrefix(field, token) && strings.TrimPrefix(field, token) != "" && allDigits(strings.TrimPrefix(field, token)) {
			return true
		}
	}
	return false
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
