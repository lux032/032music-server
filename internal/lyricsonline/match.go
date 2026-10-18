package lyricsonline

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Match thresholds. Scores are 0..1 where 0 is a perfect match (same scale the
// client shows as "match %").
const (
	// MaxDurationDiffMs rejects candidates whose length differs more than this
	// (different version, TV size, live...). Only applied when both are known.
	MaxDurationDiffMs = 15000
	minTitleSim       = 0.75
	minArtistSim      = 0.5
	// A near-identical title with a near-identical length is accepted even if
	// the artist is written differently (e.g. 米津玄師 vs Kenshi Yonezu).
	strongTitleSim     = 0.9
	strongDurationDiff = 2000
	// Among accepted candidates a synced one wins over a slightly better
	// unsynced one: known-unsynced candidates are ranked as if their score
	// were syncedPreference worse, unknown ones (NetEase, decided only after
	// download) half of that. Equal effective scores prefer synced.
	syncedPreference = 0.1
)

// bracketed matches trailing / embedded version notes: (TV size) [Remaster] （…） 【…】 ～…～ - xxx ver.
var bracketed = regexp.MustCompile(`[\(\[（【「『〔～~].*?[\)\]）】」』〕～~]`)

// normalize folds width/case and drops punctuation and spaces.
func normalize(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// stringSim is 1 for equal normalized strings, 0 for nothing in common.
func stringSim(a, b string) float64 {
	na, nb := []rune(normalize(a)), []rune(normalize(b))
	if len(na) == 0 || len(nb) == 0 {
		return 0
	}
	if string(na) == string(nb) {
		return 1
	}
	sim := 1 - float64(levenshtein(na, nb))/float64(max(len(na), len(nb)))
	// "Song" vs "Song (Album Version)" after normalization becomes containment.
	short, long := na, nb
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) >= 2 && strings.Contains(string(long), string(short)) && sim < 0.85 {
		sim = 0.85
	}
	return sim
}

// strippedPenalty keeps an exact title ahead of one that only matches once
// version notes are removed ("Song" vs "Song (TV size)").
const strippedPenalty = 0.97

// TitleSim compares titles with and without bracketed version notes.
func TitleSim(a, b string) float64 {
	sim := stringSim(a, b)
	sa, sb := strings.TrimSpace(bracketed.ReplaceAllString(a, "")), strings.TrimSpace(bracketed.ReplaceAllString(b, ""))
	if sa != "" && sb != "" {
		sim = max(sim, stringSim(sa, sb)*strippedPenalty)
	}
	return sim
}

var artistSep = regexp.MustCompile(`(?i)\s*(?:,|、|&|/|／|;|＆|×|\bx\b|\bfeat\.?|\bft\.?|\bwith\b|\band\b|・)\s*`)

// PrimaryArtist is the first artist of a joined artist string ("A, B feat. C" -> "A").
// Searching with every artist often finds nothing; matching still compares them all.
func PrimaryArtist(a string) string {
	for _, part := range artistSep.Split(a, -1) {
		if p := strings.TrimSpace(part); p != "" {
			return p
		}
	}
	return strings.TrimSpace(a)
}

// ArtistSim is the best similarity between any artist of a and any artist of b.
func ArtistSim(a, b string) float64 {
	best := stringSim(a, b)
	for _, x := range artistSep.Split(a, -1) {
		for _, y := range artistSep.Split(b, -1) {
			if strings.TrimSpace(x) != "" && strings.TrimSpace(y) != "" {
				best = max(best, stringSim(x, y))
			}
		}
	}
	return best
}

// Score rates a candidate for q. ok is false when it should not be used automatically.
func Score(q Query, c Candidate) (score float64, ok bool) {
	durationKnown := q.DurationMs > 0 && c.DurationMs > 0
	diff := int64(0)
	if durationKnown {
		diff = q.DurationMs - c.DurationMs
		if diff < 0 {
			diff = -diff
		}
	}
	title := TitleSim(q.Title, c.Title)
	artist := 1.0
	if strings.TrimSpace(q.Artist) != "" {
		artist = ArtistSim(q.Artist, c.Artist)
	}
	score = 1 - (0.6*title + 0.4*artist)
	if durationKnown && diff <= 3000 {
		// Same length: halve the distance (keeps the order among good matches).
		score *= 0.5
	}
	score = max(0, min(1, score))
	if durationKnown && diff > MaxDurationDiffMs {
		return 1, false
	}
	switch {
	case title >= minTitleSim && artist >= minArtistSim:
		ok = true
	case title >= strongTitleSim && durationKnown && diff <= strongDurationDiff:
		ok = true
	}
	return score, ok
}

// Rank scores candidates (setting Score) and orders them best first. The
// returned accepted slice holds only automatically usable candidates, with a
// synced candidate preferred over a slightly better unsynced one.
func Rank(q Query, cands []Candidate) (all, accepted []Candidate) {
	all = make([]Candidate, len(cands))
	copy(all, cands)
	okBy := make(map[int]bool, len(all))
	for i := range all {
		var ok bool
		all[i].Score, ok = Score(q, all[i])
		all[i].Accepted = ok
		okBy[i] = ok
	}
	for i := range all {
		if okBy[i] {
			accepted = append(accepted, all[i])
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score < all[j].Score })
	// A total order (effective score, then sync tier, then score) so the sort is well defined.
	sort.SliceStable(accepted, func(i, j int) bool {
		a, b := accepted[i], accepted[j]
		ta, tb := syncTier(a), syncTier(b)
		ea, eb := a.Score+float64(ta)*syncedPreference/2, b.Score+float64(tb)*syncedPreference/2
		if ea != eb {
			return ea < eb
		}
		if ta != tb {
			return ta < tb
		}
		return a.Score < b.Score
	})
	return all, accepted
}

// syncTier: 0 known synced, 1 unknown, 2 known unsynced.
func syncTier(c Candidate) int {
	switch {
	case c.Synced == nil:
		return 1
	case *c.Synced:
		return 0
	}
	return 2
}
