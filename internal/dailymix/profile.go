package dailymix

import (
	"math"
	"sort"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// Profile is the listening profile: a decayed play weight per track (30-day
// half-life over the last 90 days, from counted sessions plus the aggregate play
// counter for older history), and the mood/style shares it implies compared with
// the library as a whole.
type Profile struct {
	Now       time.Time
	weight    map[int64]float64
	total     float64
	moodShare map[string]float64 // listened share / library share, per tag
	styleLift map[string]float64
	artists   map[int64]float64
	// tagAffinity is the decayed implicit feedback per tag ("mood:x" / "style:x").
	tagAffinity map[string]float64
}

// SetTagAffinity attaches the tag affinities from daily mix plays.
func (p *Profile) SetTagAffinity(tags map[string]float64) { p.tagAffinity = tags }

// tagFeedback maps a tag affinity onto a lift factor with a floor (design §7.5: a few
// skips never exclude a mood or style; only 👎 “theme” starts a long cooldown).
func (p *Profile) tagFeedback(kind, tag string) float64 {
	if tag == "" {
		return 1
	}
	v := p.tagAffinity[kind+":"+tag]
	return math.Max(0.85, math.Min(1.2, 1+0.05*v))
}

const profileHalfLife = 30 * 24 * time.Hour

func decay(age time.Duration) float64 {
	if age < 0 {
		age = 0
	}
	return math.Pow(0.5, float64(age)/float64(profileHalfLife))
}

// BuildProfile computes the profile. sessions: counted play times per track.
func BuildProfile(now time.Time, tracks []*storage.RecTrack, sessions map[int64][]time.Time) *Profile {
	p := &Profile{Now: now, weight: map[int64]float64{}, moodShare: map[string]float64{}, styleLift: map[string]float64{}, artists: map[int64]float64{}}
	horizon := now.Add(-90 * 24 * time.Hour)
	for _, t := range tracks {
		var w float64
		for _, at := range sessions[t.ID] {
			if at.After(horizon) {
				w += decay(now.Sub(at))
			}
		}
		// Older history only survives in the aggregate counter: a light weight decayed
		// by the last play.
		if w == 0 && t.PlayCount > 0 && !t.LastPlayed.IsZero() && t.LastPlayed.After(horizon) {
			w = 0.3 * math.Log1p(float64(t.PlayCount)) * decay(now.Sub(t.LastPlayed))
		}
		if t.Favorite {
			w += 0.5
		}
		if w > 0 {
			p.weight[t.ID] = w
			p.total += w
			for _, a := range t.ArtistIDs {
				p.artists[a] += w
			}
		}
	}
	libMood, libStyle := map[string]float64{}, map[string]float64{}
	lisMood, lisStyle := map[string]float64{}, map[string]float64{}
	var libN float64
	for _, t := range tracks {
		if len(t.Moods) == 0 && len(t.Styles) == 0 {
			continue
		}
		libN++
		w := p.weight[t.ID]
		for _, m := range tagging.Moods {
			if r := moodRel(t, m.ID); r > 0 {
				libMood[m.ID] += r
				lisMood[m.ID] += w * r
			}
		}
		for _, s := range tagging.Styles {
			if r := styleRel(t, s.ID); r > 0 {
				libStyle[s.ID] += r
				lisStyle[s.ID] += w * r
			}
		}
	}
	lift := func(lib, lis map[string]float64, out map[string]float64) {
		var lisTotal float64
		for _, v := range lis {
			lisTotal += v
		}
		var libTotal float64
		for _, v := range lib {
			libTotal += v
		}
		for k, v := range lib {
			if libTotal == 0 || lisTotal < 3 || v == 0 { // too little history: neutral
				out[k] = 1
				continue
			}
			// Smoothed ratio of listened share to library share.
			out[k] = (lis[k]/lisTotal + 0.02) / (v/libTotal + 0.02)
		}
	}
	lift(libMood, lisMood, p.moodShare)
	lift(libStyle, lisStyle, p.styleLift)
	return p
}

// Weight is the decayed play weight of a track.
func (p *Profile) Weight(id int64) float64 { return p.weight[id] }

// HasHistory reports whether there is enough listening to personalise.
func (p *Profile) HasHistory() bool { return p.total >= 3 }

// Lift returns the profile lift of a theme, clamped to [0.6, 1.6] (sqrt-damped).
func (p *Profile) Lift(th *Theme) float64 {
	l := 1.0
	switch th.Type {
	case TypeMood, TypeRediscover:
		l = p.mood(th.Mood) * p.tagFeedback("mood", th.Mood)
	case TypeStyle:
		l = p.style(th.Style) * p.tagFeedback("style", th.Style)
	case TypeMoodStyle:
		l = math.Sqrt(p.mood(th.Mood)*p.style(th.Style)) * math.Sqrt(p.tagFeedback("mood", th.Mood)*p.tagFeedback("style", th.Style))
	default:
		var w float64
		for _, c := range th.Pool {
			w += p.weight[c.T.ID]
		}
		if p.total > 0 && len(th.Pool) > 0 {
			l = 1 + 4*w/p.total // share of listening that falls into this work/artist
		}
	}
	return math.Max(0.6, math.Min(1.6, math.Sqrt(l)))
}

func (p *Profile) mood(id string) float64 {
	if v, ok := p.moodShare[id]; ok {
		return v
	}
	return 1
}

func (p *Profile) style(id string) float64 {
	if v, ok := p.styleLift[id]; ok {
		return v
	}
	return 1
}

// TopArtists returns the n most listened artists.
func (p *Profile) TopArtists(n int) map[int64]bool {
	type kv struct {
		id int64
		w  float64
	}
	var list []kv
	for id, w := range p.artists {
		list = append(list, kv{id, w})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].w > list[j].w || (list[i].w == list[j].w && list[i].id < list[j].id)
	})
	out := map[int64]bool{}
	for i := 0; i < n && i < len(list); i++ {
		out[list[i].id] = true
	}
	return out
}

// Summary lists the most over-represented moods and styles (for the planner prompt
// and the admin page).
func (p *Profile) Summary(n int) (moods, styles []string) {
	pick := func(m map[string]float64, kind string) []string {
		type kv struct {
			k string
			v float64
		}
		var list []kv
		for k, v := range m {
			if v > 1.1 {
				list = append(list, kv{k, v})
			}
		}
		sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v || (list[i].v == list[j].v && list[i].k < list[j].k) })
		var out []string
		for i := 0; i < n && i < len(list); i++ {
			out = append(out, tagging.TagLabel(kind, list[i].k))
		}
		return out
	}
	return pick(p.moodShare, "mood"), pick(p.styleLift, "style")
}
