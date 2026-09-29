package metadata

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/tagclean"
)

var (
	// v6 (D70): English OST suffixes allow a leading "The" and the media word
	// "Series"; サウンドトラック allows a コンプリート prefix. Standalone ASCII
	// OP/ED moved out of the case-insensitive role regex (F4).
	workOST       = regexp.MustCompile(`(?i)(?:コンプリート[\s・･]*サウンド[\s・･]*トラックス?|オリジナル[\s・･]*サウンド[\s・･]*トラックス?|サウンド[\s・･]*トラックス?|サウンドセレクション|サントラ|(?:(?:\bThe\s+)?(?:Original|Official|Complete)\s+|\bThe\s+)?(?:\b(?:(?:Video\s+)?Game|Motion\s+Picture|Anime|TV|Series)\s+)?Sound\s*Tracks?\b|O\.S\.T\.|\bOST\b)`)
	workRole      = regexp.MustCompile(`(?i)(?:オープニングテーマ|OPテーマ|エンディングテーマ|EDテーマ|主題歌|テーマソング|Theme Song|挿入歌|劇中歌|Insert Song|キャラクターソング|キャラソン|Character Song|イメージソング|Image Song)`)
	workRoleASCII = regexp.MustCompile(`\bOP\d*\b|\bED\d*\b`)
	workDrama     = regexp.MustCompile(`(?i)ドラマCD|ボイスドラマ`)
	workMedia     = regexp.MustCompile(`(?i)TVアニメ|テレビアニメ|アニメ|TV Anime|劇場版|映画|\bMovie\b|ゲーム|\bGAME\b|\bOVA\b|\bOAD\b`)
	// workOSTGroup brackets: a bracketed segment that itself contains an OST
	// keyword is release metadata ("(Original Series Soundtrack)"), never part
	// of the work title; other bracketed segments stay ("[Unlimited Blade
	// Works]", "(Deluxe Edition)").
	workOSTGroup         = regexp.MustCompile(`[(\[（【][^)\]）】]*[)\]）】]`)
	workCompilation      = regexp.MustCompile(`(?i)コレクション|Collection|ベスト|Best of|Selection`)
	workCrossCompilation = regexp.MustCompile(`(?i)アニソン|Anison|Various Artists|V\.A\.|オムニバス|東方`)
	workCrossTitles      = regexp.MustCompile(`(?i)[A-Z0-9]+\d\w*\s*[&＆×]\s*[A-Z0-9]+\d`)
	workSeason           = regexp.MustCompile(`(?i)(?:Season\s*(\d+)|第\s*(\d+)\s*[期季]|(\d+)期)`)
	workQuote            = regexp.MustCompile(`[「『【“"]([^」』】”"]+)[」』】”"]`)
	workPrefix           = regexp.MustCompile(`(?i)^(?:TVアニメ|テレビアニメ|アニメ|TV Anime|ゲーム|GAME|OVA|OAD)\s*`)

	// workOSTV5/workRoleV5 freeze the v5 rule set so the rule-upgrade carryover
	// (D71) can still compute the identity a library was tagged with before the
	// v6 edge-trim/bracket/generic fixes. Never use them for new inference.
	workOSTV5  = regexp.MustCompile(`(?i)(?:オリジナル[\s・･]*サウンド[\s・･]*トラックス?|サウンド[\s・･]*トラックス?|サウンドセレクション|サントラ|(?:(?:Original|Official|Complete)\s+)?(?:(?:(?:Video\s+)?Game|Motion\s+Picture|Anime|TV)\s+)?Sound\s*Tracks?\b|O\.S\.T\.|\bOST\b)`)
	workRoleV5 = regexp.MustCompile(`(?i)(?:オープニングテーマ|OPテーマ|\bOP\d*\b|エンディングテーマ|EDテーマ|\bED\d*\b|主題歌|テーマソング|Theme Song|挿入歌|劇中歌|Insert Song|キャラクターソング|キャラソン|Character Song|イメージソング|Image Song)`)
)

// workInferRules parameterizes album-work inference so the frozen v5 rules and
// the current v6 rules share one implementation (D71). generic's bool flags
// the "keyword at the start, residue only" branch, where a pure-digit leftover
// ("OST 2" → "2") is still generic; elsewhere digits are valid work names
// (86, 1917 — the v5 behavior).
type workInferRules struct {
	ost       *regexp.Regexp
	roleCI    *regexp.Regexp
	roleASCII *regexp.Regexp // case-sensitive standalone OP/ED; nil in v5
	ostGroup  bool           // cut at the first bracketed segment containing an OST keyword
	edgeTrim  func(string) string
	generic   func(string, bool) bool
}

var (
	workRulesV6 = workInferRules{ost: workOST, roleCI: workRole, roleASCII: workRoleASCII, ostGroup: true, edgeTrim: trimWorkTitleEdges, generic: isGenericWorkTitle}
	workRulesV5 = workInferRules{ost: workOSTV5, roleCI: workRoleV5, edgeTrim: trimWorkTitleEdgesV5, generic: isGenericWorkTitleV5}
)

// roleFindIndex locates the earliest role keyword across the case-insensitive
// and the case-sensitive ASCII patterns.
func (r workInferRules) roleFindIndex(value string) []int {
	ci := r.roleCI.FindStringIndex(value)
	var ascii []int
	if r.roleASCII != nil {
		ascii = r.roleASCII.FindStringIndex(value)
	}
	switch {
	case ci == nil:
		return ascii
	case ascii == nil:
		return ci
	case ascii[0] < ci[0]:
		return ascii
	default:
		return ci
	}
}

func (r workInferRules) roleFind(value string) string {
	loc := r.roleFindIndex(value)
	if loc == nil {
		return ""
	}
	return value[loc[0]:loc[1]]
}

func (r workInferRules) roleReplaceAll(value string) string {
	value = r.roleCI.ReplaceAllString(value, " ")
	if r.roleASCII != nil {
		value = r.roleASCII.ReplaceAllString(value, " ")
	}
	return value
}

// trimWorkTitleEdgesV5 is the frozen v5 edge cleanup: it blindly strips every
// listed character from both ends, which is what truncated titles like
// "Cyberpunk: Edgerunners (Original Series" (unbalanced bracket) and
// "イースX -NORDICS" (lost trailing dash). Kept only for the D71 carryover.
func trimWorkTitleEdgesV5(title string) string {
	return strings.TrimSpace(strings.Trim(title, " -:：()（）[]【】「」『』“”\""))
}

// trimWorkTitleEdges removes the dangling punctuation keyword removal leaves
// behind, without eating meaningful title characters (D70):
//   - edge separators (: ： / , 、 ・) and stray quotes are stripped;
//   - a bracket at the edge is stripped only when unpaired, so "[Unlimited
//     Blade Works]" and "(Deluxe Edition)" survive;
//   - a trailing "-" is kept only for the " -X-" connector form (an earlier
//     dash preceded by a space or the string start and immediately followed by
//     a non-space): "イースX -NORDICS-" keeps it, "Hollow Knight: Silksong -"
//     (dangling) and "86-エイティシックス-" (no spaced connector) drop it;
//   - a trailing ~/～ is kept only when paired (「ゼルダの伝説~風のタクト~」).
func trimWorkTitleEdges(title string) string {
	const edgePunct = ":：/,、・「」『』“”\""
	openFor := map[rune]rune{')': '(', '）': '（', ']': '[', '】': '【'}
	closeFor := map[rune]rune{'(': ')', '（': '）', '[': ']', '【': '】'}
	for {
		title = strings.TrimSpace(title)
		if title == "" {
			return title
		}
		if strings.HasSuffix(title, "-") {
			if keepTrailingWorkDash(title) {
				return title
			}
			title = strings.TrimSuffix(title, "-")
			continue
		}
		if strings.HasPrefix(title, "-") {
			title = strings.TrimPrefix(title, "-")
			continue
		}
		if last, size := utf8.DecodeLastRuneInString(title); last == '~' || last == '～' {
			// Strict pairing by count (D70): a trailing tilde stays only when the
			// title holds an even number of tildes (「ゼルダの伝説~風のタクト~」).
			body := title[:len(title)-size]
			if (strings.Count(title, "~")+strings.Count(title, "～"))%2 == 0 {
				return title
			}
			title = body
			continue
		}
		trimmed := false
		if r, size := utf8.DecodeLastRuneInString(title); strings.ContainsRune(edgePunct, r) {
			title = title[:len(title)-size]
			trimmed = true
		} else if open, isClose := openFor[r]; isClose {
			if strings.ContainsRune(title[:len(title)-size], open) {
				return title // paired closing bracket at the edge: keep
			}
			title = title[:len(title)-size]
			trimmed = true
		} else if _, isOpen := closeFor[r]; isOpen {
			// An opening bracket at the very end can never be paired.
			title = title[:len(title)-size]
			trimmed = true
		}
		if r, size := utf8.DecodeRuneInString(title); !trimmed && strings.ContainsRune(edgePunct, r) {
			title = title[size:]
			trimmed = true
		} else if !trimmed {
			if close, isOpen := closeFor[r]; isOpen {
				if strings.ContainsRune(title[size:], close) {
					return title // paired opening bracket at the edge: keep
				}
				title = title[size:]
				trimmed = true
			} else if _, isClose := openFor[r]; isClose {
				title = title[size:]
				trimmed = true
			}
		}
		if !trimmed {
			return title
		}
	}
}

// keepTrailingWorkDash reports whether a title ending in "-" keeps it: only
// when an earlier dash forms a " -X-" connector (preceded by a space or the
// string start, immediately followed by a non-space). A dangling " -" and a
// trailing dash without a spaced connector are dropped.
func keepTrailingWorkDash(title string) bool {
	body := strings.TrimSuffix(title, "-")
	if body == "" || strings.HasSuffix(body, " ") {
		return false
	}
	for i := 0; i < len(body); i++ {
		if body[i] == '-' && (i == 0 || body[i-1] == ' ') && i+1 < len(body) && body[i+1] != ' ' {
			return true
		}
	}
	return false
}

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
	return inferAlbumWorkWith(workRulesV6, albumTitle, folder, isCompilation)
}

// InferAlbumWorkV5 reproduces the frozen v5 inference exactly. It exists only
// so the D71 rule-upgrade carryover can recognize identities stored under v5.
func InferAlbumWorkV5(albumTitle, folder string, isCompilation bool) (WorkAssociation, bool) {
	return inferAlbumWorkWith(workRulesV5, albumTitle, folder, isCompilation)
}

func inferAlbumWorkWith(rules workInferRules, albumTitle, folder string, isCompilation bool) (WorkAssociation, bool) {
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
	} else if rules.ost.MatchString(value) {
		role = "ost"
	} else if m := rules.roleFind(value); m != "" {
		role = inferRoleOnly(m)
	}
	mediaValue := rules.ost.ReplaceAllString(value, "")
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
	roleLoc := rules.roleFindIndex(value)
	if roleLoc == nil {
		roleLoc = rules.ost.FindStringIndex(value)
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
	digitsGeneric := false
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
		// D70/B1: the first bracketed segment containing an OST keyword starts
		// the release-metadata tail — cut there, dropping any "Vol.2" /
		// "[Deluxe Edition]" / "- EP" suffix with it (v5 behavior). Only when
		// nothing precedes it do we drop all OST bracket groups and fall
		// through to keyword replacement.
		cut := -1
		if rules.ostGroup {
			for _, loc := range workOSTGroup.FindAllStringIndex(title, -1) {
				if rules.ost.MatchString(title[loc[0]:loc[1]]) {
					cut = loc[0]
					break
				}
			}
		}
		if cut > 0 && strings.TrimSpace(title[:cut]) != "" {
			title = title[:cut]
			// The prefix itself may still carry an unbracketed OST/role/drama
			// keyword ("Title OST (Original Soundtrack)"); cut at the earliest
			// marker with a non-empty prefix so it cannot leak into the name.
			marker := rules.ost.FindStringIndex(title)
			if marker == nil {
				marker = rules.roleFindIndex(title)
			}
			if marker == nil {
				marker = workDrama.FindStringIndex(title)
			}
			if marker != nil && strings.TrimSpace(title[:marker[0]]) != "" {
				title = title[:marker[0]]
			}
		} else {
			if cut >= 0 {
				title = workOSTGroup.ReplaceAllStringFunc(title, func(group string) string {
					if rules.ost.MatchString(group) {
						return " "
					}
					return group
				})
			}
			marker := rules.ost.FindStringIndex(title)
			if marker == nil {
				marker = rules.roleFindIndex(title)
			}
			if marker == nil {
				marker = workDrama.FindStringIndex(title)
			}
			if marker != nil && strings.TrimSpace(title[:marker[0]]) != "" {
				title = title[:marker[0]]
			} else {
				title = rules.ost.ReplaceAllString(title, " ")
				title = rules.roleReplaceAll(title)
				title = workDrama.ReplaceAllString(title, " ")
				// Keyword-at-start residue: a pure-digit leftover is a volume
				// number ("OST 2"), not a work name; elsewhere digits are valid.
				digitsGeneric = true
			}
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
	title = rules.edgeTrim(title)
	title = strings.Join(strings.Fields(title), " ")
	if title == "" || rules.generic(title, digitsGeneric) {
		return WorkAssociation{}, false
	}
	sequence := 0
	if m := rolePattern.FindStringSubmatch(value); len(m) > 1 {
		sequence, _ = strconv.Atoi(m[1])
	}
	return WorkAssociation{Title: title, Type: typ, Role: role, Season: season, Sequence: sequence}, true
}

// InferTrackWorkFromTags reads explicit work tags (v6 rules).
func InferTrackWorkFromTags(raw map[string][]string, trackTitle string) []WorkAssociation {
	return inferTrackWorkFromTagsWith(workRulesV6, raw, trackTitle)
}

// InferTrackWorkFromTagsV5 is the frozen v5 variant, used only by the D71
// rule-upgrade carryover.
func InferTrackWorkFromTagsV5(raw map[string][]string, trackTitle string) []WorkAssociation {
	return inferTrackWorkFromTagsWith(workRulesV5, raw, trackTitle)
}

func inferTrackWorkFromTagsWith(rules workInferRules, raw map[string][]string, trackTitle string) []WorkAssociation {
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
				if a, ok := inferAlbumWorkWith(rules, value, "", false); ok {
					return []WorkAssociation{a}
				}
			}
		}
	}
	return nil
}
