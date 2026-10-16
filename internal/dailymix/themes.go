// Package dailymix generates the daily recommendation mixes (docs/daily-mix-design.md
// §4–6): candidate themes from the tag vocabulary and the library, Thompson sampling
// over theme statistics weighted by the listening profile, optional model-written
// titles (the model only picks among server-computed themes; it never picks tracks),
// then per-mix weighted sampling under quotas and constraints and energy-curve ordering.
package dailymix

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// Theme types.
const (
	TypeMood       = "mood"
	TypeStyle      = "style"
	TypeMoodStyle  = "mood_style"
	TypeWork       = "work"
	TypeComposer   = "composer"
	TypeArtist     = "artist"
	TypeRediscover = "rediscover"
)

// Core reports whether the theme type fills the mood/style slots.
func Core(typ string) bool { return typ == TypeMood || typ == TypeStyle || typ == TypeMoodStyle }

// adjacentMoods is the neighbourhood of design §2.2.1.
var adjacentMoods = map[string][]string{
	"hotblooded": {"uplifting", "epic"},
	"uplifting":  {"hotblooded", "cheerful"},
	"cheerful":   {"uplifting", "adventure", "playful", "cool"},
	"adventure":  {"cheerful", "epic"},
	"playful":    {"cheerful"},
	"cool":       {"dreamy", "cheerful"},
	"epic":       {"hotblooded", "adventure"},
	"romantic":   {"healing", "nostalgic"},
	"healing":    {"relaxed", "romantic"},
	"relaxed":    {"healing", "dreamy"},
	"dreamy":     {"relaxed", "cool"},
	"nostalgic":  {"romantic"},
}

// adjacentStyles groups related styles for pool expansion.
var adjacentStyles = map[string][]string{
	"pop":           {"ballad", "idol"},
	"rock":          {"punk", "metal"},
	"metal":         {"rock"},
	"punk":          {"rock"},
	"electronic":    {"dance", "ambient"},
	"dance":         {"electronic", "denpa"},
	"ballad":        {"pop", "piano"},
	"idol":          {"pop", "denpa"},
	"folk_acoustic": {"ballad", "ethnic"},
	"ethnic":        {"folk_acoustic", "orchestral"},
	"orchestral":    {"piano", "ethnic"},
	"piano":         {"orchestral", "ballad"},
	"jazz":          {"citypop_funk"},
	"citypop_funk":  {"jazz", "pop"},
	"hiphop":        {"electronic"},
	"denpa":         {"idol", "dance"},
	"ambient":       {"electronic", "experimental"},
	"experimental":  {"ambient", "rock"},
}

// lowEnergyMoods may take mildly sad tracks (design §2.2.1).
var lowEnergyMoods = map[string]bool{"healing": true, "dreamy": true, "nostalgic": true}

// Theme is one candidate theme.
type Theme struct {
	Type  string
	Key   string
	Mood  string
	Style string
	RefID int64
	Name  string // display name of the work / artist / composer
	Pool  []Candidate
	// Familiar/Explore count the pool by bucket.
	Familiar, Explore int
	// Lift is the listening-profile lift of the theme (1 = average).
	Lift float64
}

// ID is the stable "type:key" identifier used for stats and the planner.
func (t *Theme) ID() string { return t.Type + ":" + t.Key }

// Label is the short human label (no decoration).
func (t *Theme) Label() string {
	switch t.Type {
	case TypeMood:
		return tagging.TagLabel("mood", t.Mood)
	case TypeStyle:
		return tagging.TagLabel("style", t.Style)
	case TypeMoodStyle:
		return tagging.TagLabel("mood", t.Mood) + " · " + tagging.TagLabel("style", t.Style)
	case TypeWork:
		return "《" + t.Name + "》"
	case TypeComposer, TypeArtist:
		return t.Name
	case TypeRediscover:
		return "好久不见 · " + tagging.TagLabel("mood", t.Mood)
	}
	return t.Key
}

// TypeLabel names the theme type in Chinese.
func TypeLabel(typ string) string {
	return map[string]string{TypeMood: "情绪", TypeStyle: "风格", TypeMoodStyle: "情绪 × 风格", TypeWork: "作品漫游",
		TypeComposer: "创作者", TypeArtist: "歌手电台", TypeRediscover: "重新发现"}[typ]
}

// TemplateTitle / TemplateDescription are used when no model is configured or its
// answer is rejected.
func (t *Theme) TemplateTitle() string {
	switch t.Type {
	case TypeMood:
		return tagging.TagLabel("mood", t.Mood) + " · 今日精选"
	case TypeStyle:
		return tagging.TagLabel("style", t.Style) + " · 今日精选"
	case TypeMoodStyle:
		return tagging.TagLabel("mood", t.Mood) + " × " + tagging.TagLabel("style", t.Style)
	case TypeWork:
		return "《" + t.Name + "》音乐漫游"
	case TypeComposer:
		return t.Name + " 作品选"
	case TypeArtist:
		return "从 " + t.Name + " 出发"
	case TypeRediscover:
		return "好久不见 · " + tagging.TagLabel("mood", t.Mood)
	}
	return t.Key
}

func (t *Theme) TemplateDescription(n int) string {
	switch t.Type {
	case TypeMood:
		return fmt.Sprintf("以「%s」为主线的 %d 首歌", tagging.TagLabel("mood", t.Mood), n)
	case TypeStyle:
		return fmt.Sprintf("%d 首%s，情绪在相近的范围里流动", n, tagging.TagLabel("style", t.Style))
	case TypeMoodStyle:
		return fmt.Sprintf("「%s」的%s，共 %d 首", tagging.TagLabel("mood", t.Mood), tagging.TagLabel("style", t.Style), n)
	case TypeWork:
		return fmt.Sprintf("把《%s》里的歌重新串一遍，共 %d 首", t.Name, n)
	case TypeComposer:
		return fmt.Sprintf("%s 写下的 %d 首歌", t.Name, n)
	case TypeArtist:
		return fmt.Sprintf("%s 和听感相近的歌，共 %d 首", t.Name, n)
	case TypeRediscover:
		return fmt.Sprintf("曾经常听、最近没再听的 %d 首", n)
	}
	return ""
}

// Tags returns the vocabulary tags the theme is about (for clients).
func (t *Theme) Tags() [][2]string {
	var out [][2]string
	if t.Mood != "" {
		out = append(out, [2]string{"mood", t.Mood})
	}
	if t.Style != "" {
		out = append(out, [2]string{"style", t.Style})
	}
	return out
}

// ParseThemeID splits "type:key" (key may contain ':').
func ParseThemeID(id string) (typ, key string) {
	typ, key, _ = strings.Cut(id, ":")
	return
}

// Candidate is a track in a theme's pool.
type Candidate struct {
	T   *storage.RecTrack
	Rel float64 // theme relevance 0–1
	Via string  // tag | adjacent | acoustic | work | composer | artist | similar
}

// ---------------------------------------------------------------- relevance

func moodRel(t *storage.RecTrack, mood string) float64 {
	tg, ok := t.Moods[mood]
	if !ok {
		return 0
	}
	return math.Min(1, tg.Weight/0.6) * tg.Confidence
}

func styleRel(t *storage.RecTrack, style string) float64 {
	tg, ok := t.Styles[style]
	if !ok {
		return 0
	}
	return tg.Confidence
}

// negativeWeight is the largest weight among the internal negative markers.
func negativeWeight(t *storage.RecTrack) float64 {
	var w float64
	for _, m := range []string{"sad", "dark", "lonely"} {
		if tg, ok := t.Moods[m]; ok && tg.Source != "audio" && tg.Weight > w {
			w = tg.Weight
		}
	}
	return w
}

// primaryMoodNegative reports whether the strongest mood is a negative marker.
func primaryMoodNegative(t *storage.RecTrack) bool {
	best, bestW := "", -1.0
	for id, tg := range t.Moods {
		if tg.Weight > bestW || (tg.Weight == bestW && id < best) {
			best, bestW = id, tg.Weight
		}
	}
	m, ok := tagging.MoodByID(best)
	return ok && m.Negative && t.Moods[best].Source != "audio"
}

// Energy returns the 1–5 energy of a track (tag, then arousal, else 3).
func Energy(t *storage.RecTrack) int {
	if t.Energy >= 1 && t.Energy <= 5 {
		return t.Energy
	}
	if t.Arousal != nil {
		return int(math.Max(1, math.Min(5, math.Round((*t.Arousal-1)/8*4+1))))
	}
	return 3
}

// familiar: played at least twice, favourite or in a playlist.
func familiar(t *storage.RecTrack) bool { return t.PlayCount >= 2 || t.Favorite || t.InPlaylist }

// eligible applies the global filters: blocked, non-main versions, extreme
// durations and (unless allowed) clearly sad tracks.
func eligible(t *storage.RecTrack, allowSad bool) bool {
	if t.Blocked || t.NonMain() || t.DurationMs < 60_000 || t.DurationMs > 15*60_000 {
		return false
	}
	if !allowSad && primaryMoodNegative(t) && negativeWeight(t) >= 0.5 {
		return false
	}
	return true
}

// mildlySad: may only join low-energy mood themes (design §2.2.1).
func mildlySad(t *storage.RecTrack, allowSad bool) bool {
	if allowSad {
		return false
	}
	w := negativeWeight(t)
	return w >= 0.3
}

// ---------------------------------------------------------------- candidate themes

// Pool size requirements (design §4.2), scaled up for long mixes.
func minPool(typ string, length int) int {
	switch typ {
	case TypeMood, TypeStyle:
		return max(40, length*8/5)
	case TypeMoodStyle:
		return max(30, length*6/5)
	case TypeRediscover:
		return max(20, length*4/5)
	default:
		return max(15, length*3/5)
	}
}

const minRel = 0.35

type builder struct {
	tracks   []*storage.RecTrack
	allowSad bool
	length   int
	acoustic *storage.AcousticSnapshot
	names    func(kind string, ids []int64) map[int64]string
	profile  *Profile
	today    string
}

func (b *builder) moodPool(mood string, withAdjacent bool) []Candidate {
	var out []Candidate
	for _, t := range b.tracks {
		if mildlySad(t, b.allowSad) && !lowEnergyMoods[mood] {
			continue
		}
		if r := moodRel(t, mood); r >= minRel {
			out = append(out, Candidate{t, r, "tag"})
			continue
		}
		if !withAdjacent {
			continue
		}
		var best float64
		for _, adj := range adjacentMoods[mood] {
			best = math.Max(best, moodRel(t, adj))
		}
		if best >= 0.6 {
			out = append(out, Candidate{t, 0.45 * best, "adjacent"})
		}
	}
	return out
}

func (b *builder) stylePool(style string, withAdjacent bool) []Candidate {
	var out []Candidate
	for _, t := range b.tracks {
		if mildlySad(t, b.allowSad) {
			continue
		}
		if r := styleRel(t, style); r >= minRel {
			out = append(out, Candidate{t, r, "tag"})
			continue
		}
		if !withAdjacent {
			continue
		}
		var best float64
		for _, adj := range adjacentStyles[style] {
			best = math.Max(best, styleRel(t, adj))
		}
		if best >= 0.7 {
			out = append(out, Candidate{t, 0.4 * best, "adjacent"})
		}
	}
	return out
}

// expandAcoustic adds untagged (or audio-only tagged) tracks that sound like the core
// of the pool, so the mix still works while AI tagging is incomplete.
func (b *builder) expandAcoustic(pool []Candidate, limit int) []Candidate {
	if b.acoustic.Len() == 0 || limit <= 0 {
		return pool
	}
	core := append([]Candidate(nil), pool...)
	sort.Slice(core, func(i, j int) bool {
		return core[i].Rel > core[j].Rel || (core[i].Rel == core[j].Rel && core[i].T.ID < core[j].T.ID)
	})
	var ids []int64
	in := map[int64]bool{}
	for i, c := range core {
		in[c.T.ID] = true
		if i < 30 && c.Via == "tag" {
			ids = append(ids, c.T.ID)
		}
	}
	if len(ids) < 8 {
		return pool
	}
	centroid := b.acoustic.Centroid(ids)
	type scored struct {
		t *storage.RecTrack
		c float64
	}
	var extra []scored
	for _, t := range b.tracks {
		if in[t.ID] || (t.TagSource != "" && t.TagSource != "audio") || mildlySad(t, b.allowSad) {
			continue
		}
		if cos, ok := b.acoustic.CosineTo(t.ID, centroid); ok && storage.AcousticScore(cos) >= 0.5 {
			extra = append(extra, scored{t, cos})
		}
	}
	sort.Slice(extra, func(i, j int) bool {
		return extra[i].c > extra[j].c || (extra[i].c == extra[j].c && extra[i].t.ID < extra[j].t.ID)
	})
	for i, e := range extra {
		if i >= limit {
			break
		}
		pool = append(pool, Candidate{e.t, 0.5 * storage.AcousticScore(e.c), "acoustic"})
	}
	return pool
}

func countBuckets(pool []Candidate) (fam, exp int) {
	for _, c := range pool {
		if familiar(c.T) {
			fam++
		} else {
			exp++
		}
	}
	return
}

func (b *builder) add(out *[]*Theme, th *Theme) {
	if len(th.Pool) < minPool(th.Type, b.length) {
		return
	}
	th.Familiar, th.Explore = countBuckets(th.Pool)
	th.Lift = b.profile.Lift(th)
	*out = append(*out, th)
}

// Candidates computes every feasible theme for the day.
func (b *builder) Candidates() []*Theme {
	var out []*Theme
	for _, m := range tagging.Moods {
		if m.Negative {
			continue
		}
		pool := b.moodPool(m.ID, true)
		if direct := countVia(pool, "tag"); direct >= minPool(TypeMood, b.length)/2 {
			pool = b.expandAcoustic(pool, b.length)
		}
		if countVia(pool, "tag") >= minPool(TypeMood, b.length)/2 {
			b.add(&out, &Theme{Type: TypeMood, Key: m.ID, Mood: m.ID, Pool: pool})
		}
	}
	for _, st := range tagging.Styles {
		pool := b.stylePool(st.ID, true)
		if countVia(pool, "tag") >= minPool(TypeStyle, b.length)/2 {
			pool = b.expandAcoustic(pool, b.length)
			b.add(&out, &Theme{Type: TypeStyle, Key: st.ID, Style: st.ID, Pool: pool})
		}
	}
	// mood × style combinations (direct hits only).
	for _, m := range tagging.Moods {
		if m.Negative {
			continue
		}
		for _, st := range tagging.Styles {
			var pool []Candidate
			for _, t := range b.tracks {
				if mildlySad(t, b.allowSad) && !lowEnergyMoods[m.ID] {
					continue
				}
				mr, sr := moodRel(t, m.ID), styleRel(t, st.ID)
				if mr >= minRel && sr >= minRel {
					pool = append(pool, Candidate{t, math.Sqrt(mr * sr), "tag"})
				}
			}
			b.add(&out, &Theme{Type: TypeMoodStyle, Key: m.ID + "+" + st.ID, Mood: m.ID, Style: st.ID, Pool: pool})
		}
	}
	b.auxiliary(&out)
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

func countVia(pool []Candidate, via string) int {
	n := 0
	for _, c := range pool {
		if c.Via == via {
			n++
		}
	}
	return n
}

// maxRadioArtists bounds the artist radios evaluated per day (each scans the whole
// acoustic index).
const maxRadioArtists = 12

// radioArtists picks the artists worth a radio: the most listened ones after the top
// three, then the ones with the most tracks.
func (b *builder) radioArtists(ids []int64, byArtist map[int64][]*storage.RecTrack, top map[int64]bool) []int64 {
	weight := map[int64]float64{}
	for _, id := range ids {
		weight[id] = b.profile.artists[id]
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !top[id] {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if weight[a] != weight[c] {
			return weight[a] > weight[c]
		}
		if len(byArtist[a]) != len(byArtist[c]) {
			return len(byArtist[a]) > len(byArtist[c])
		}
		return a < c
	})
	if len(out) > maxRadioArtists {
		out = out[:maxRadioArtists]
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// auxiliary builds work / composer / artist / rediscover themes.
func (b *builder) auxiliary(out *[]*Theme) {
	byWork := map[int64][]*storage.RecTrack{}
	byComposer := map[int64][]*storage.RecTrack{}
	byArtist := map[int64][]*storage.RecTrack{}
	for _, t := range b.tracks {
		if mildlySad(t, b.allowSad) {
			continue
		}
		for _, w := range t.Works {
			byWork[w] = append(byWork[w], t)
		}
		for _, c := range t.Composers {
			byComposer[c] = append(byComposer[c], t)
		}
		for _, a := range t.ArtistIDs {
			byArtist[a] = append(byArtist[a], t)
		}
	}
	idsOf := func(m map[int64][]*storage.RecTrack, min int) []int64 {
		var ids []int64
		for id, list := range m {
			if len(list) >= min {
				ids = append(ids, id)
			}
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return ids
	}
	pool := func(list []*storage.RecTrack, via string) []Candidate {
		out := make([]Candidate, len(list))
		for i, t := range list {
			out[i] = Candidate{t, 1, via}
		}
		return out
	}
	need := minPool(TypeWork, b.length)
	if ids := idsOf(byWork, need); len(ids) > 0 {
		names := b.names("work", ids)
		for _, id := range ids {
			if names[id] != "" {
				b.add(out, &Theme{Type: TypeWork, Key: strconv.FormatInt(id, 10), RefID: id, Name: names[id], Pool: pool(byWork[id], "work")})
			}
		}
	}
	if ids := idsOf(byComposer, need); len(ids) > 0 {
		names := b.names("artist", ids)
		for _, id := range ids {
			if names[id] != "" {
				b.add(out, &Theme{Type: TypeComposer, Key: strconv.FormatInt(id, 10), RefID: id, Name: names[id], Pool: pool(byComposer[id], "composer")})
			}
		}
	}
	// Artist radio: the artist's own tracks plus tracks that sound like them; skip the
	// three most played artists (they need no radio).
	top := b.profile.TopArtists(3)
	if b.acoustic.Len() > 0 {
		ids := b.radioArtists(idsOf(byArtist, 8), byArtist, top)
		names := b.names("artist", ids)
		for _, id := range ids {
			if names[id] == "" {
				continue
			}
			own := byArtist[id]
			var seedIDs []int64
			mine := map[int64]bool{}
			for _, t := range own {
				seedIDs = append(seedIDs, t.ID)
				mine[t.ID] = true
			}
			centroid := b.acoustic.Centroid(seedIDs)
			if centroid == nil {
				continue
			}
			p := pool(own, "artist")
			for _, t := range b.tracks {
				if mine[t.ID] || mildlySad(t, b.allowSad) {
					continue
				}
				if cos, ok := b.acoustic.CosineTo(t.ID, centroid); ok {
					if s := storage.AcousticScore(cos); s >= 0.6 {
						p = append(p, Candidate{t, 0.8 * s, "similar"})
					}
				}
			}
			if len(p)-len(own) >= b.length {
				b.add(out, &Theme{Type: TypeArtist, Key: strconv.FormatInt(id, 10), RefID: id, Name: names[id], Pool: p})
			}
		}
	}
	// Rediscover: often played (or favourite) but not in the last 60 days, by mood.
	cutoff := b.profile.Now.AddDate(0, 0, -60)
	for _, m := range tagging.Moods {
		if m.Negative {
			continue
		}
		var p []Candidate
		for _, t := range b.tracks {
			if !(t.PlayCount >= 3 || t.Favorite) || t.AddedAt.After(cutoff) || (!t.LastPlayed.IsZero() && t.LastPlayed.After(cutoff)) {
				continue
			}
			if mildlySad(t, b.allowSad) && !lowEnergyMoods[m.ID] {
				continue
			}
			r := moodRel(t, m.ID)
			for _, adj := range adjacentMoods[m.ID] {
				r = math.Max(r, 0.6*moodRel(t, adj))
			}
			if r >= minRel {
				p = append(p, Candidate{t, r, "tag"})
			}
		}
		b.add(out, &Theme{Type: TypeRediscover, Key: m.ID, Mood: m.ID, Pool: p})
	}
}
