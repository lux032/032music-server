package dailymix

import (
	"encoding/json"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// MinTracks is the shortest mix worth generating (design §5.6).
const MinTracks = 12

// Duration bounds of a mix (design §5.4).
const (
	minMixDuration = 60 * time.Minute
	maxMixDuration = 120 * time.Minute
)

// selectEnv is the shared state of one day's selection.
type selectEnv struct {
	now        time.Time
	day        string
	explore    float64
	length     int
	recentMix  map[int64]string // track → latest day in a mix (before today)
	usedToday  map[int64]bool   // tracks already in today's other mixes
	usedTitles map[string]bool
	acoustic   *storage.AcousticSnapshot
	profile    *Profile
	allowSad   bool
	// affinity is the decayed implicit feedback per track (plays from daily mixes).
	affinity map[int64]float64
}

// Pick is one selected track with its explanation.
type Pick struct {
	T      *storage.RecTrack
	Bucket string
	Reason Reason
}

// Reason explains why a track was picked (stored as reason_json, shown on the admin page).
type Reason struct {
	Via        string  `json:"via"`
	Relevance  float64 `json:"rel"`
	Preference float64 `json:"pref"`
	Novelty    float64 `json:"nov"`
	Energy     int     `json:"energy"`
	// Level is the ordering intensity on the 1–9 arousal scale: the acoustic arousal
	// when analysed (continuous, far finer than the model's five energy levels, which
	// cluster at 3–4), else the energy mapped onto 1–9.
	Level float64 `json:"level"`
	// Feedback is the weight factor from earlier plays in daily mixes (1 = none).
	Feedback float64 `json:"fb,omitempty"`
}

// feedbackFactor maps a track's affinity onto a selection weight: one quick skip
// (−1) ≈ ×0.74, a completed play (+1) ≈ ×1.35; bounded so a few skips never ban a
// track and a few plays never pin it.
func feedbackFactor(score float64) float64 {
	return math.Max(0.5, math.Min(1.6, math.Exp(0.3*score)))
}

// level returns the ordering intensity of a track (see Reason.Level).
func level(t *storage.RecTrack) float64 {
	if t.Arousal != nil && *t.Arousal > 0 {
		return round2(math.Max(1, math.Min(9, *t.Arousal)))
	}
	return float64(2*Energy(t) - 1)
}

func (r Reason) JSON() string {
	b, _ := json.Marshal(r)
	return string(b)
}

// preference: plays (log), favourite, playlists, skip ratio.
func preference(t *storage.RecTrack) float64 {
	p := 0.7 + 0.25*math.Log1p(float64(t.PlayCount))
	if t.Favorite {
		p += 0.5
	}
	if t.InPlaylist {
		p += 0.2
	}
	if total := t.PlayCount + t.SkipCount; total >= 3 {
		p -= 0.6 * float64(t.SkipCount) / float64(total)
	}
	return math.Max(0.2, math.Min(2.5, p))
}

// novelty: recently recommended or just played tracks step back.
func (e *selectEnv) novelty(t *storage.RecTrack) float64 {
	n := 1.0
	if day, ok := e.recentMix[t.ID]; ok {
		if day >= storage.DayAfter(e.day, -7) {
			n *= 0.3
		} else if day >= storage.DayAfter(e.day, -14) {
			n *= 0.6
		}
	}
	if !t.LastPlayed.IsZero() && e.now.Sub(t.LastPlayed) < 24*time.Hour {
		n *= 0.5
	}
	return n
}

type weighted struct {
	c      Candidate
	key    float64
	bucket string
	reason Reason
}

// build selects and orders the tracks of one theme. rng is seeded per (day, slot,
// generation) so a mix is reproducible.
func (e *selectEnv) build(th *Theme, rng *rand.Rand) []Pick {
	length := e.length
	exploreN := int(math.Round(float64(length) * e.explore))
	if th.Type == TypeRediscover {
		exploreN = 0
	}
	familiarN := length - exploreN

	// Direct hits fill the quotas first; adjacent-tag and sound-alike tracks only fill
	// what is left (otherwise a theme with few familiar direct hits would be filled
	// with familiar neighbours instead of its own tracks).
	var fam, exp, extra []weighted
	for _, c := range th.Pool {
		t := c.T
		if e.usedToday[t.ID] || e.usedTitles[t.TitleKey] {
			continue
		}
		r := Reason{Via: c.Via, Relevance: round2(c.Rel), Preference: round2(preference(t)), Novelty: round2(e.novelty(t)), Energy: Energy(t), Level: level(t)}
		w := c.Rel * c.Rel * r.Preference * r.Novelty
		if score, ok := e.affinity[t.ID]; ok {
			r.Feedback = round2(feedbackFactor(score))
			w *= r.Feedback
		}
		switch t.Version {
		case "live":
			w *= 0.35
		case "remix":
			w *= 0.75
		}
		bucket := "explore"
		if familiar(t) {
			bucket = "familiar"
		} else if e.now.Sub(t.AddedAt) < 30*24*time.Hour {
			w *= 1.3 // new arrivals get a nudge
		}
		if w <= 0 {
			continue
		}
		// Efraimidis–Spirakis: key = U^(1/w); the largest keys win.
		item := weighted{c, math.Pow(rng.Float64(), 1/w), bucket, r}
		if c.Via == "adjacent" || c.Via == "acoustic" {
			extra = append(extra, item)
		} else if bucket == "familiar" {
			fam = append(fam, item)
		} else {
			exp = append(exp, item)
		}
	}
	byKey := func(list []weighted) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].key == list[j].key {
				return list[i].c.T.ID < list[j].c.T.ID
			}
			return list[i].key > list[j].key
		})
	}
	byKey(fam)
	byKey(exp)
	byKey(extra)

	artistCap, albumCap := 3, 2
	switch th.Type {
	case TypeArtist:
		artistCap, albumCap = max(3, length*2/5), 3
	case TypeWork, TypeComposer:
		artistCap, albumCap = 4, 4
	}
	artists := map[int64]int{}
	albums := map[int64]int{}
	titles := map[string]bool{}
	var dur time.Duration
	var chosen []weighted
	accept := func(w weighted) bool {
		t := w.c.T
		if titles[t.TitleKey] || albums[t.AlbumID] >= albumCap || dur+time.Duration(t.DurationMs)*time.Millisecond > maxMixDuration {
			return false
		}
		for _, a := range t.ArtistIDs {
			limit := artistCap
			if th.Type == TypeArtist && a != th.RefID {
				limit = 3
			}
			if artists[a] >= limit {
				return false
			}
		}
		titles[t.TitleKey] = true
		albums[t.AlbumID]++
		for _, a := range t.ArtistIDs {
			artists[a]++
		}
		dur += time.Duration(t.DurationMs) * time.Millisecond
		chosen = append(chosen, w)
		return true
	}
	take := func(list []weighted, n int) []weighted {
		var rest []weighted
		got := 0
		for _, w := range list {
			if got < n && accept(w) {
				got++
				continue
			}
			rest = append(rest, w)
		}
		return rest
	}
	famRest := take(fam, familiarN)
	expRest := take(exp, exploreN)
	count := func(bucket string) int {
		n := 0
		for _, w := range chosen {
			if w.bucket == bucket {
				n++
			}
		}
		return n
	}
	split := func(list []weighted, bucket string) (match, rest []weighted) {
		for _, w := range list {
			if w.bucket == bucket {
				match = append(match, w)
			} else {
				rest = append(rest, w)
			}
		}
		return
	}
	// Each bucket's shortfall is first filled from the theme's neighbours (adjacent
	// tags, sound-alikes) of the same bucket, so the familiar/explore split holds even
	// when the theme's own tracks are mostly unheard (or mostly familiar).
	for _, bucket := range []string{"familiar", "explore"} {
		want := familiarN
		if bucket == "explore" {
			want = exploreN
		}
		if short := want - count(bucket); short > 0 {
			match, rest := split(extra, bucket)
			match = take(match, short)
			extra = append(rest, match...)
			byKey(extra)
		}
	}
	// Familiar floor: a few familiar tracks close to the theme open a mix whose own
	// tracks are (almost) all unheard, e.g. the weekly cold theme.
	if need := min(familiarN, bridgeNeed(length)) - count("familiar"); need > 0 && len(th.Bridge) > 0 {
		var bridge []weighted
		for _, c := range th.Bridge {
			t := c.T
			if e.usedToday[t.ID] || e.usedTitles[t.TitleKey] {
				continue
			}
			r := Reason{Via: c.Via, Relevance: round2(c.Rel), Preference: round2(preference(t)), Novelty: round2(e.novelty(t)), Energy: Energy(t), Level: level(t)}
			if w := c.Rel * r.Preference * r.Novelty; w > 0 {
				bridge = append(bridge, weighted{c, math.Pow(rng.Float64(), 1/w), "familiar", r})
			}
		}
		byKey(bridge)
		take(bridge, need)
	}
	// Then the other bucket's own tracks, then any neighbour.
	if short := length - len(chosen); short > 0 {
		famRest = take(famRest, short)
	}
	if short := length - len(chosen); short > 0 {
		expRest = take(expRest, short)
	}
	if short := length - len(chosen); short > 0 {
		extra = take(extra, short)
	}
	// Too short in time: allow a few extra tracks.
	for _, list := range [][]weighted{famRest, expRest, extra} {
		for _, w := range list {
			if dur >= minMixDuration || len(chosen) >= length+8 {
				break
			}
			accept(w)
		}
	}
	if len(chosen) < MinTracks {
		return nil
	}
	picks := make([]Pick, len(chosen))
	for i, w := range chosen {
		picks[i] = Pick{T: w.c.T, Bucket: w.bucket, Reason: w.reason}
	}
	return e.order(picks, th)
}

// order arranges the picks along an energy arc (start → rise → peak → fall): the
// tracks sorted by energy are split into a rising part (two thirds, ascending) and a
// falling part (one third, every third track, descending), which keeps adjacent
// energy steps small whatever the distribution. Equal-energy runs are chained by
// acoustic similarity; then local swaps between tracks of near-equal energy fix
// the rules: familiar openers, no artist back to back, exploration and mildly sad
// tracks away from the ends.
func (e *selectEnv) order(picks []Pick, th *Theme) []Pick {
	// The two calmest familiar tracks open the mix; the arc is built from the rest
	// (so a few familiar tracks in a mostly unheard mix do not get swapped in from
	// the peak).
	var openers []Pick
	if len(picks) >= 6 {
		fam := []int{}
		for i, p := range picks {
			if p.Bucket == "familiar" {
				fam = append(fam, i)
			}
		}
		sort.SliceStable(fam, func(a, b int) bool { return picks[fam[a]].Reason.Level < picks[fam[b]].Reason.Level })
		second := -1
		for k := 1; k < len(fam); k++ {
			if !sharesArtist(picks[fam[0]].T, picks[fam[k]].T) {
				second = fam[k]
				break
			}
		}
		if second >= 0 {
			skip := map[int]bool{fam[0]: true, second: true}
			openers = []Pick{picks[fam[0]], picks[second]}
			rest := make([]Pick, 0, len(picks)-2)
			for i, p := range picks {
				if !skip[i] {
					rest = append(rest, p)
				}
			}
			picks = rest
		}
	}
	seq := e.arrange(picks)
	if len(openers) == 0 {
		return seq
	}
	return e.fixRules(append(openers, seq...), 2)
}

// arrange builds the energy arc of picks.
func (e *selectEnv) arrange(picks []Pick) []Pick {
	n := len(picks)
	sorted := append([]Pick(nil), picks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Reason.Level != sorted[j].Reason.Level {
			return sorted[i].Reason.Level < sorted[j].Reason.Level
		}
		if sorted[i].Reason.Preference != sorted[j].Reason.Preference {
			return sorted[i].Reason.Preference > sorted[j].Reason.Preference
		}
		return sorted[i].T.ID < sorted[j].T.ID
	})
	var rise, fall []Pick
	for i, p := range sorted {
		if n >= 6 && i%3 == 0 {
			fall = append(fall, p)
		} else {
			rise = append(rise, p)
		}
	}
	for i, j := 0, len(fall)-1; i < j; i, j = i+1, j-1 {
		fall[i], fall[j] = fall[j], fall[i]
	}
	seq := append(e.chainRuns(rise), e.chainRuns(fall)...)
	return e.fixRules(seq, 0)
}

// fixRules swaps tracks of near-equal intensity to satisfy the ordering rules;
// positions before keep are left alone.
func (e *selectEnv) fixRules(seq []Pick, keep int) []Pick {
	n := len(seq)

	shares := sharesArtist
	bad := func(i int) bool {
		if i < keep {
			return false
		}
		p := seq[i]
		frac := float64(i) / float64(max(1, n-1))
		switch {
		case i < 2 && p.Bucket != "familiar":
			return true
		case i >= n-2 && p.Bucket == "explore" && n > 6:
			return true
		case mildlySad(p.T, e.allowSad) && (frac < 0.3 || frac > 0.75):
			return true
		case i > 0 && shares(seq[i-1].T, p.T):
			return true
		}
		return false
	}
	near := func(a, b Pick, tol float64) bool {
		return math.Abs(a.Reason.Level-b.Reason.Level) <= tol
	}
	for pass := 0; pass < 3; pass++ {
		changed := false
		for i := 0; i < n; i++ {
			if !bad(i) {
				continue
			}
			fixed := false
			for _, tol := range []float64{0.75, 1.5, 3, 9} {
				for d := 1; d < n && !fixed; d++ {
					for _, j := range []int{i + d, i - d} {
						if j < keep || j >= n || !near(seq[i], seq[j], tol) {
							continue
						}
						seq[i], seq[j] = seq[j], seq[i]
						ok := !bad(i) && !bad(j) && (j+1 >= n || !bad(j+1)) && (i+1 >= n || !bad(i+1))
						if ok {
							fixed = true
							break
						}
						seq[i], seq[j] = seq[j], seq[i]
					}
				}
				if fixed {
					break
				}
			}
			changed = changed || fixed
		}
		if !changed {
			break
		}
	}
	return seq
}

// chainRuns reorders each run of near-equal intensity (within 0.6 on the 1–9 scale)
// greedily by acoustic similarity to the previous track (when embeddings exist).
func (e *selectEnv) chainRuns(list []Pick) []Pick {
	if e.acoustic.Len() == 0 || len(list) < 3 {
		return list
	}
	out := make([]Pick, 0, len(list))
	for start := 0; start < len(list); {
		end := start
		for end < len(list) && list[end].Reason.Level-list[start].Reason.Level <= 0.6 {
			end++
		}
		run := append([]Pick(nil), list[start:end]...)
		for len(run) > 0 {
			best := 0
			if len(out) > 0 {
				prev := out[len(out)-1].T.ID
				bestCos := -2.0
				for k, p := range run {
					if c, ok := e.acoustic.Cosine(prev, p.T.ID); ok && c > bestCos {
						best, bestCos = k, c
					}
				}
			}
			out = append(out, run[best])
			run = append(run[:best], run[best+1:]...)
		}
		start = end
	}
	return out
}

func sharesArtist(a, b *storage.RecTrack) bool {
	for _, x := range a.ArtistIDs {
		for _, y := range b.ArtistIDs {
			if x == y {
				return true
			}
		}
	}
	return false
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
