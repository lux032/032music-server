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
		r := Reason{Via: c.Via, Relevance: round2(c.Rel), Preference: round2(preference(t)), Novelty: round2(e.novelty(t)), Energy: Energy(t)}
		w := c.Rel * c.Rel * r.Preference * r.Novelty
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
	// Keep the exploration floor with neighbours when the direct hits are all familiar.
	got := 0
	for _, w := range chosen {
		if w.bucket == "explore" {
			got++
		}
	}
	if got < exploreN {
		var exploreExtra, others []weighted
		for _, w := range extra {
			if w.bucket == "explore" {
				exploreExtra = append(exploreExtra, w)
			} else {
				others = append(others, w)
			}
		}
		exploreExtra = take(exploreExtra, exploreN-got)
		extra = append(others, exploreExtra...)
		byKey(extra)
	}
	// Fill shortfalls from the other bucket, then from the neighbours (keeping the
	// exploration floor where possible).
	if short := length - len(chosen); short > 0 {
		expRest = take(expRest, short)
	}
	if short := length - len(chosen); short > 0 {
		famRest = take(famRest, short)
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
	n := len(picks)
	sorted := append([]Pick(nil), picks...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Reason.Energy != sorted[j].Reason.Energy {
			return sorted[i].Reason.Energy < sorted[j].Reason.Energy
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

	shares := func(a, b *storage.RecTrack) bool {
		for _, x := range a.ArtistIDs {
			for _, y := range b.ArtistIDs {
				if x == y {
					return true
				}
			}
		}
		return false
	}
	bad := func(i int) bool {
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
	near := func(a, b Pick, tol int) bool {
		d := a.Reason.Energy - b.Reason.Energy
		return d >= -tol && d <= tol
	}
	for pass := 0; pass < 3; pass++ {
		changed := false
		for i := 0; i < n; i++ {
			if !bad(i) {
				continue
			}
			fixed := false
			for _, tol := range []int{1, 2} {
				for d := 1; d < n && !fixed; d++ {
					for _, j := range []int{i + d, i - d} {
						if j < 0 || j >= n || !near(seq[i], seq[j], tol) {
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

// chainRuns reorders each run of equal energy greedily by acoustic similarity to
// the previous track (when embeddings exist).
func (e *selectEnv) chainRuns(list []Pick) []Pick {
	if e.acoustic.Len() == 0 || len(list) < 3 {
		return list
	}
	out := make([]Pick, 0, len(list))
	for start := 0; start < len(list); {
		end := start
		for end < len(list) && list[end].Reason.Energy == list[start].Reason.Energy {
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

func round2(v float64) float64 { return math.Round(v*100) / 100 }
