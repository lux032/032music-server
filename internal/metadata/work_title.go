package metadata

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/tagclean"
)

var (
	workOST              = regexp.MustCompile(`(?i)(?:オリジナル[\s・･]*サウンド[\s・･]*トラックス?|サウンド[\s・･]*トラックス?|サウンドセレクション|サントラ|(?:(?:Original|Official|Complete)\s+)?(?:(?:(?:Video\s+)?Game|Motion\s+Picture|Anime|TV)\s+)?Sound\s*Tracks?\b|O\.S\.T\.|\bOST\b)`)
	workRole             = regexp.MustCompile(`(?i)(?:オープニングテーマ|OPテーマ|\bOP\d*\b|エンディングテーマ|EDテーマ|\bED\d*\b|主題歌|テーマソング|Theme Song|挿入歌|劇中歌|Insert Song|キャラクターソング|キャラソン|Character Song|イメージソング|Image Song)`)
	workDrama            = regexp.MustCompile(`(?i)ドラマCD|ボイスドラマ`)
	workMedia            = regexp.MustCompile(`(?i)TVアニメ|テレビアニメ|アニメ|TV Anime|劇場版|映画|\bMovie\b|ゲーム|\bGAME\b|\bOVA\b|\bOAD\b`)
	workCompilation      = regexp.MustCompile(`(?i)コレクション|Collection|ベスト|Best of|Selection`)
	workCrossCompilation = regexp.MustCompile(`(?i)アニソン|Anison|Various Artists|V\.A\.|オムニバス|東方`)
	workCrossTitles      = regexp.MustCompile(`(?i)[A-Z0-9]+\d\w*\s*[&＆×]\s*[A-Z0-9]+\d`)
	workSeason           = regexp.MustCompile(`(?i)(?:Season\s*(\d+)|第\s*(\d+)\s*[期季]|(\d+)期)`)
	workQuote            = regexp.MustCompile(`[「『【“"]([^」』】”"]+)[」』】”"]`)
	workPrefix           = regexp.MustCompile(`(?i)^(?:TVアニメ|テレビアニメ|アニメ|TV Anime|ゲーム|GAME|OVA|OAD)\s*`)
)

// StripWorkSeason removes the season marker so different spellings of the
// same season ("Season 2", "第2期", "2期") share one identity. Display
// titles keep their original spelling; this is only used for matching.
func StripWorkSeason(title string) string {
	return strings.Join(strings.Fields(workSeason.ReplaceAllString(title, " ")), " ")
}

// WorkSeasonNumber extracts the season number from a work title (0 when absent).
func WorkSeasonNumber(title string) int {
	if m := workSeason.FindStringSubmatch(title); m != nil {
		for _, n := range m[1:] {
			if n != "" {
				season, _ := strconv.Atoi(n)
				return season
			}
		}
	}
	return 0
}

// InferAlbumWork only trusts an album title (or a missing title's folder), never a track title.
func InferAlbumWork(albumTitle, folder string, isCompilation bool) (WorkAssociation, bool) {
	value := strings.TrimSpace(albumTitle)
	if value == "" {
		value = strings.TrimSpace(folder)
	}
	value = strings.NewReplacer("‐", "-", "‑", "-", "–", "-", "—", "-", "’", "'", "‘", "'").Replace(value)
	if value == "" || workCrossCompilation.MatchString(value) {
		return WorkAssociation{}, false
	}
	role := "other"
	if workDrama.MatchString(value) {
		role = "other"
	} else if workOST.MatchString(value) {
		role = "ost"
	} else if m := workRole.FindString(value); m != "" {
		role = inferRoleOnly(m)
	}
	mediaValue := workOST.ReplaceAllString(value, "")
	media := workMedia.FindString(mediaValue)
	typ := "other"
	switch {
	case workDrama.MatchString(value):
	case strings.Contains(media, "劇場版") || strings.Contains(media, "映画") || strings.EqualFold(media, "Movie"):
		typ = "movie"
	case strings.Contains(media, "ゲーム") || strings.EqualFold(media, "GAME"):
		typ = "game"
	case media != "":
		typ = "anime"
	case role != "ost" && role != "other":
		typ = "anime"
	}
	season := 0
	if m := workSeason.FindStringSubmatch(value); m != nil {
		for _, n := range m[1:] {
			if n != "" {
				season, _ = strconv.Atoi(n)
				break
			}
		}
	}
	// A quoted work must immediately follow a media prefix or precede the role.
	roleLoc := workRole.FindStringIndex(value)
	if roleLoc == nil {
		roleLoc = workOST.FindStringIndex(value)
	}
	var chosen string
	for _, loc := range workQuote.FindAllStringSubmatchIndex(value, -1) {
		before := strings.TrimSpace(value[:loc[0]])
		mediaBefore := workMedia.FindStringIndex(before)
		validPrefix := mediaBefore != nil && mediaBefore[1] == len(before) || workDrama.MatchString(before)
		validRole := roleLoc != nil && loc[1] <= roleLoc[0] && strings.TrimSpace(value[loc[1]:roleLoc[0]]) == ""
		if (validPrefix || validRole) && (chosen == "" || validRole) {
			chosen = value[loc[2]:loc[3]]
		}
	}
	if role == "other" && !workDrama.MatchString(value) && chosen == "" {
		return WorkAssociation{}, false
	}
	if workCompilation.MatchString(value) {
		mediaQuoted := 0
		for _, loc := range workQuote.FindAllStringIndex(value, -1) {
			prefix := strings.TrimSpace(value[:loc[0]])
			m := workMedia.FindStringIndex(prefix)
			if m != nil && m[1] == len(prefix) {
				mediaQuoted++
			}
		}
		if len(workQuote.FindAllStringIndex(value, -1)) > 1 {
			return WorkAssociation{}, false
		}
		if mediaQuoted > 1 {
			return WorkAssociation{}, false
		}
		beforeMarker := role == "ost" && roleLoc != nil && strings.TrimSpace(value[:roleLoc[0]]) != "" && !workCompilation.MatchString(value[:roleLoc[0]])
		// Collection/Best may name a single work only when its release
		// identity is unambiguous; thematic OP/ED compilations stay excluded.
		if mediaQuoted != 1 && !beforeMarker {
			return WorkAssociation{}, false
		}
	}
	outsideQuotes := workQuote.ReplaceAllString(value, "")
	if isCompilation && strings.ContainsAny(outsideQuotes, "&＆×") && chosen == "" && (strings.Contains(strings.ToUpper(outsideQuotes), "BOX") || workCrossTitles.MatchString(outsideQuotes)) {
		return WorkAssociation{}, false
	}
	title := value
	if chosen != "" {
		title = chosen
		if season > 0 && !workSeason.MatchString(title) {
			title += " " + workSeason.FindString(value)
		}
		if typ == "movie" && !strings.Contains(title, "劇場版") && strings.Contains(value, "劇場版") {
			title = "劇場版 " + title
		}
	} else {
		// If the only quote follows a role, it is a song, not a work.
		if workQuote.MatchString(value) && roleLoc != nil && workQuote.FindStringIndex(value)[0] > roleLoc[0] && workPrefix.MatchString(value) == false && strings.TrimSpace(value[:roleLoc[0]]) == "" {
			return WorkAssociation{}, false
		}
		marker := workOST.FindStringIndex(title)
		if marker == nil {
			marker = workRole.FindStringIndex(title)
		}
		if marker == nil {
			marker = workDrama.FindStringIndex(title)
		}
		if marker != nil && strings.TrimSpace(title[:marker[0]]) != "" {
			title = title[:marker[0]]
		} else {
			title = workOST.ReplaceAllString(title, " ")
			title = workRole.ReplaceAllString(title, " ")
			title = workDrama.ReplaceAllString(title, " ")
		}
		title = workPrefix.ReplaceAllString(title, " ")
		// A quoted song after a role does not replace the work title.
		if loc := workQuote.FindStringIndex(title); loc != nil {
			title = strings.TrimSpace(title[:loc[0]])
		}
		if workMedia.MatchString(title) && strings.TrimSpace(workMedia.ReplaceAllString(title, "")) == "" {
			return WorkAssociation{}, false
		}
	}
	title = strings.TrimSpace(strings.Trim(title, " -:：()（）[]【】「」『』“”\""))
	title = strings.Join(strings.Fields(title), " ")
	if title == "" || isGenericWorkTitle(title) {
		return WorkAssociation{}, false
	}
	sequence := 0
	if m := rolePattern.FindStringSubmatch(value); len(m) > 1 {
		sequence, _ = strconv.Atoi(m[1])
	}
	return WorkAssociation{Title: title, Type: typ, Role: role, Season: season, Sequence: sequence}, true
}

func InferTrackWorkFromTags(raw map[string][]string, trackTitle string) []WorkAssociation {
	for _, key := range []string{"WORKTITLE", "CONTENTGROUP", "GROUPING", "WORK"} {
		for field, values := range raw {
			if !strings.EqualFold(field, key) {
				continue
			}
			for _, value := range values {
				// D-19: raw tag values may carry NUL separators and C0 control
				// characters; the inference input must be cleaned first.
				value = tagclean.Value(value)
				if value == "" {
					continue
				}
				if key == "WORK" && Normalize(value) == Normalize(trackTitle) {
					continue
				}
				if a, ok := InferAlbumWork(value, "", false); ok {
					return []WorkAssociation{a}
				}
			}
		}
	}
	return nil
}
