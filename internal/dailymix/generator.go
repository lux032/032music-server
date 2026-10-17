package dailymix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// GeneratorVersion is stored with every mix; bump it when the algorithm changes.
const GeneratorVersion = 4

// Zone is the day boundary zone (UTC+8, like the home rotation).
var Zone = time.FixedZone("UTC+8", 8*60*60)

// Day returns the recommendation day: the UTC+8 date, switching at the
// pregeneration hour (before 05:00 you still get yesterday's mixes).
func Day(now time.Time, hour int) string {
	return now.In(Zone).Add(-time.Duration(hour) * time.Hour).Format("2006-01-02")
}

// ErrNotReady means the library cannot support a single theme yet.
var ErrNotReady = errors.New("not enough tagged tracks for any theme")

func seeded(parts ...any) *rand.Rand {
	h := fnv.New64a()
	fmt.Fprint(h, parts...)
	s := h.Sum64()
	return rand.New(rand.NewPCG(s, s^0x9e3779b97f4a7c15))
}

// Plan is the stored plan_json of a mix (explanations for the admin page).
type Plan struct {
	ThemeID  string         `json:"themeId"`
	Label    string         `json:"label"`
	Mood     string         `json:"mood,omitempty"`
	Style    string         `json:"style,omitempty"`
	RefID    int64          `json:"refId,omitempty"`
	Pool     int            `json:"pool"`
	Direct   int            `json:"direct"`
	Familiar int            `json:"familiar"`
	Explore  int            `json:"explore"`
	Sample   float64        `json:"sample"`
	Lift     float64        `json:"lift"`
	Score    float64        `json:"score"`
	Cold     bool           `json:"cold,omitempty"`
	Fallback bool           `json:"fallback,omitempty"`
	ByVia    map[string]int `json:"byVia,omitempty"`
}

// RunInfo summarises a generation for the admin page.
type RunInfo struct {
	Day          string    `json:"day"`
	Kind         string    `json:"kind"` // day | replace
	StartedAt    time.Time `json:"startedAt"`
	Millis       int64     `json:"millis"`
	Tracks       int       `json:"tracks"`     // playable tracks considered
	Tagged       int       `json:"tagged"`     // with effective tags
	Acoustic     int       `json:"acoustic"`   // with embeddings
	Candidates   int       `json:"candidates"` // feasible themes
	Feedback     int       `json:"feedback"`   // tracks with implicit feedback from daily mix plays
	Eligible     int       `json:"eligible"`   // after cooldowns
	Mixes        int       `json:"mixes"`
	LLM          string    `json:"llm"`
	LLMUsed      bool      `json:"llmUsed"`
	Shortlist    []string  `json:"shortlist"`
	Skipped      []string  `json:"skipped,omitempty"` // themes that could not fill a mix
	Error        string    `json:"error,omitempty"`
	ProfileMood  []string  `json:"profileMoods,omitempty"`
	ProfileStyle []string  `json:"profileStyles,omitempty"`
}

// Generator builds mixes from the store.
type Generator struct {
	Store *storage.Store
	// Chat returns the model client, or nil when titles by model are off/not configured.
	Chat func(storage.AISettings) (tagging.Chat, error)
	// LLMTimeout bounds the planning call.
	LLMTimeout time.Duration
}

// prepared is the shared state of one generation.
type prepared struct {
	tracks   []*storage.RecTrack
	cands    []*Theme
	profile  *Profile
	acoustic *storage.AcousticSnapshot
	stats    map[string]storage.ThemeStat
	recent   []storage.ThemeUse
	recentMx map[int64]string
	affinity storage.RecAffinities
	info     RunInfo
}

func (g *Generator) prepare(ctx context.Context, now time.Time, day string, set storage.DailyMixSettings) (*prepared, error) {
	lib, err := g.Store.RecLibrary(ctx)
	if err != nil {
		return nil, err
	}
	p := &prepared{info: RunInfo{Day: day, StartedAt: now}}
	for i := range lib {
		t := &lib[i]
		if len(t.Moods) > 0 || len(t.Styles) > 0 {
			p.info.Tagged++
		}
		if eligible(t, set.AllowSad) {
			p.tracks = append(p.tracks, t)
		}
	}
	p.info.Tracks = len(lib)
	if p.acoustic, err = g.Store.AcousticIndex(ctx); err != nil {
		return nil, err
	}
	p.info.Acoustic = p.acoustic.Len()
	sessions, err := g.Store.RecentPlays(ctx, now.Add(-90*24*time.Hour))
	if err != nil {
		return nil, err
	}
	p.profile = BuildProfile(now, p.tracks, sessions)
	if p.affinity, err = g.Store.RecAffinities(ctx, now); err != nil {
		return nil, err
	}
	p.profile.SetTagAffinity(p.affinity.Tags)
	p.info.Feedback = len(p.affinity.Tracks)
	p.info.ProfileMood, p.info.ProfileStyle = p.profile.Summary(4)
	b := &builder{tracks: p.tracks, allowSad: set.AllowSad, length: set.TrackCount, acoustic: p.acoustic, profile: p.profile, today: day,
		names: func(kind string, ids []int64) map[int64]string {
			out := map[int64]string{}
			if kind == "work" {
				works, err := g.Store.WorkTitles(ctx, ids)
				if err == nil {
					for id, w := range works {
						out[id] = w.Title
					}
				}
				return out
			}
			names, err := g.Store.ArtistDisplayNames(ctx, ids)
			if err == nil {
				out = names
			}
			return out
		}}
	p.cands = b.Candidates()
	p.info.Candidates = len(p.cands)
	if p.stats, err = g.Store.ThemeStats(ctx); err != nil {
		return nil, err
	}
	if p.recent, err = g.Store.RecentThemes(ctx, storage.DayAfter(day, -30)); err != nil {
		return nil, err
	}
	if p.recentMx, err = g.Store.RecentMixTracks(ctx, storage.DayAfter(day, -14), day); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *prepared) env(now time.Time, day string, set storage.DailyMixSettings) *selectEnv {
	return &selectEnv{now: now, day: day, explore: float64(set.ExplorePercent) / 100, length: set.TrackCount, recentMix: p.recentMx,
		usedToday: map[int64]bool{}, usedTitles: map[string]bool{}, acoustic: p.acoustic, profile: p.profile, allowSad: set.AllowSad, affinity: p.affinity.Tracks}
}

func toMix(c Chosen, slot int, picks []Pick) storage.NewDailyMix {
	th := c.Theme
	plan := Plan{ThemeID: th.ID(), Label: th.Label(), Mood: th.Mood, Style: th.Style, RefID: th.RefID, Pool: len(th.Pool), Direct: th.Direct, Familiar: th.Familiar, Explore: th.Explore,
		Sample: round2(c.Sample), Lift: round2(th.Lift), Score: round2(c.Score), Cold: c.Forced, ByVia: map[string]int{}}
	m := storage.NewDailyMix{Slot: slot, ThemeType: th.Type, ThemeKey: th.Key, Title: c.Title, Description: c.Description, TitleSource: c.TitleSource,
		GeneratorVersion: GeneratorVersion, CooldownDays: 7}
	if m.Title == "" {
		m.Title, m.TitleSource = th.TemplateTitle(), "template"
	}
	if m.Description == "" {
		m.Description = th.TemplateDescription(len(picks))
	}
	for i, pk := range picks {
		m.Items = append(m.Items, storage.DailyMixItem{Position: i, TrackID: pk.T.ID, Bucket: pk.Bucket, ReasonJSON: pk.Reason.JSON()})
		m.DurationMs += pk.T.DurationMs
		plan.ByVia[pk.Reason.Via]++
	}
	raw, _ := json.Marshal(plan)
	m.PlanJSON = string(raw)
	return m
}

// GenerateDay builds the mixes of a day (not saved).
func (g *Generator) GenerateDay(ctx context.Context, now time.Time, day string, set storage.DailyMixSettings, ai storage.AISettings) ([]storage.NewDailyMix, RunInfo, error) {
	start := time.Now()
	p, err := g.prepare(ctx, now, day, set)
	if err != nil {
		return nil, RunInfo{Day: day, StartedAt: now, Error: err.Error()}, err
	}
	info := &p.info
	info.Kind = "day"
	rng := seeded("plan", day)
	ranked := rank(p.cands, planInput{day: day, stats: p.stats, recent: p.recent, excludeID: map[string]bool{}, excludeMood: map[string]bool{}, excludeStyle: map[string]bool{}}, rng)
	info.Eligible = len(ranked)
	if len(ranked) == 0 {
		if len(p.cands) == 0 {
			return nil, *info, ErrNotReady
		}
		// Everything is cooling down (tiny library): ignore weekly cooldowns.
		ranked = rank(p.cands, planInput{day: day, stats: map[string]storage.ThemeStat{}, excludeID: map[string]bool{}, excludeMood: map[string]bool{}, excludeStyle: map[string]bool{}}, rng)
	}
	core, aux := Slots(set.MixCount)
	list := shortlist(ranked, core, aux)
	// Weekly cold slot: when none of the last 6 days had one, the best cold theme is required.
	required := ""
	if !hadColdSince(p.recent, storage.DayAfter(day, -6), day) {
		for _, r := range ranked {
			if r.Cold {
				required = r.Theme.ID()
				if !inRanked(list, r.Theme) {
					list = append(list, r)
				}
				break
			}
		}
	}
	for _, r := range list {
		info.Shortlist = append(info.Shortlist, describe(r))
	}
	chosen := choose(list, core, aux)
	if required != "" {
		chosen = forceTheme(chosen, list, required)
	}
	for i := range chosen {
		chosen[i].Title, chosen[i].Description, chosen[i].TitleSource = chosen[i].Theme.TemplateTitle(), "", "template"
	}
	info.LLM = "未使用（未开启模型起名或模型服务未配置）"
	if set.LLMTitles && ai.Configured() && g.Chat != nil {
		if chat, err := g.Chat(ai); err == nil && chat != nil {
			lctx, cancel := context.WithTimeout(ctx, g.LLMTimeout)
			var res LLMResult
			chosen, res = planWithModel(lctx, chat, ai, list, chosen, required, core, aux, day, p.profile, recentTitles(p.recent, day))
			cancel()
			if res.Usage.Requests > 0 || res.Usage.PromptTokens > 0 {
				if res.Usage.Requests == 0 {
					res.Usage.Requests = 1
				}
				_ = g.Store.AddAIUsage(ctx, now, ai.Model, "mix", res.Usage)
			}
			info.LLM, info.LLMUsed = res.Note, res.Used
			if res.Used {
				info.LLM = fmt.Sprintf("%s（%s，%d ms）", res.Note, ai.Model, res.Millis)
			}
			for i := range chosen {
				if chosen[i].Theme.ID() == required {
					chosen[i].Forced = true
				}
			}
		} else if err != nil {
			info.LLM = "模型客户端无效：" + shortError(err)
		}
	}

	env := p.env(now, day, set)
	var mixes []storage.NewDailyMix
	usedTheme := map[*Theme]bool{}
	for _, c := range chosen {
		usedTheme[c.Theme] = true
	}
	spare := func() (Chosen, bool) {
		for _, r := range ranked {
			if !usedTheme[r.Theme] {
				usedTheme[r.Theme] = true
				return Chosen{Ranked: r, Title: r.Theme.TemplateTitle(), TitleSource: "template"}, true
			}
		}
		return Chosen{}, false
	}
	for slot := 0; slot < len(chosen) && len(mixes) < set.MixCount; slot++ {
		c := chosen[slot]
		for {
			picks := env.build(c.Theme, seeded("mix", day, len(mixes), 0, c.Theme.ID()))
			if picks != nil {
				for _, pk := range picks {
					env.usedToday[pk.T.ID] = true
					env.usedTitles[pk.T.TitleKey] = true
				}
				mixes = append(mixes, toMix(c, len(mixes), picks))
				break
			}
			info.Skipped = append(info.Skipped, c.Theme.Label())
			next, ok := spare()
			if !ok {
				break
			}
			c = next
		}
	}
	info.Mixes = len(mixes)
	info.Millis = time.Since(start).Milliseconds()
	if len(mixes) == 0 {
		return nil, *info, ErrNotReady
	}
	return mixes, *info, nil
}

// GenerateReplacement builds a new mix for the slot of old (“换一个”): a different
// theme from today's, of the same kind when possible, with tracks not in today's
// other mixes.
func (g *Generator) GenerateReplacement(ctx context.Context, now time.Time, old storage.DailyMix, today []storage.DailyMix, set storage.DailyMixSettings, ai storage.AISettings) (storage.NewDailyMix, RunInfo, error) {
	start := time.Now()
	day := old.Day
	p, err := g.prepare(ctx, now, day, set)
	if err != nil {
		return storage.NewDailyMix{}, RunInfo{Day: day, StartedAt: now, Error: err.Error()}, err
	}
	info := &p.info
	info.Kind = "replace"
	in := planInput{day: day, stats: p.stats, recent: p.recent, excludeID: map[string]bool{}, excludeMood: map[string]bool{}, excludeStyle: map[string]bool{}}
	for _, u := range p.recent {
		if u.Day == day {
			in.excludeID[u.ThemeType+":"+u.ThemeKey] = true
		}
	}
	env := p.env(now, day, set)
	for _, m := range today {
		th := &Theme{Type: m.ThemeType, Key: m.ThemeKey}
		if m.ID != old.ID {
			if mood := themeMood(th); mood != "" {
				in.excludeMood[mood] = true
			}
			if m.ThemeType == TypeStyle {
				in.excludeStyle[m.ThemeKey] = true
			}
		}
		items, err := g.Store.DailyMixItems(ctx, m.ID)
		if err != nil {
			return storage.NewDailyMix{}, *info, err
		}
		for _, it := range items {
			env.usedToday[it.TrackID] = true // the replaced mix's tracks are excluded too
		}
	}
	rng := seeded("replace", day, old.Slot, old.Generation+1)
	ranked := rank(p.cands, in, rng)
	info.Eligible = len(ranked)
	wantCore := Core(old.ThemeType)
	// Same kind first, then anything.
	var ordered []Ranked
	for _, r := range ranked {
		if Core(r.Theme.Type) == wantCore {
			ordered = append(ordered, r)
		}
	}
	for _, r := range ranked {
		if Core(r.Theme.Type) != wantCore {
			ordered = append(ordered, r)
		}
	}
	info.LLM = "未使用（未开启模型起名或模型服务未配置）"
	for _, r := range ordered {
		picks := env.build(r.Theme, seeded("mix", day, old.Slot, old.Generation+1, r.Theme.ID()))
		if picks == nil {
			info.Skipped = append(info.Skipped, r.Theme.Label())
			continue
		}
		c := Chosen{Ranked: r, Title: r.Theme.TemplateTitle(), TitleSource: "template"}
		info.Shortlist = []string{describe(r)}
		if set.LLMTitles && ai.Configured() && g.Chat != nil {
			if chat, err := g.Chat(ai); err == nil && chat != nil {
				lctx, cancel := context.WithTimeout(ctx, g.LLMTimeout)
				out, res := planWithModel(lctx, chat, ai, []Ranked{r}, []Chosen{c}, "", 1, 0, day, p.profile, recentTitles(p.recent, day))
				cancel()
				if res.Usage.Requests > 0 || res.Usage.PromptTokens > 0 {
					if res.Usage.Requests == 0 {
						res.Usage.Requests = 1
					}
					_ = g.Store.AddAIUsage(ctx, now, ai.Model, "mix", res.Usage)
				}
				info.LLM, info.LLMUsed = res.Note, res.Used
				if len(out) == 1 {
					c = out[0]
				}
			}
		}
		info.Mixes = 1
		info.Millis = time.Since(start).Milliseconds()
		return toMix(c, old.Slot, picks), *info, nil
	}
	info.Millis = time.Since(start).Milliseconds()
	return storage.NewDailyMix{}, *info, ErrNotReady
}

// recentTitles lists the titles of the last 14 days (the model should not repeat them).
func recentTitles(recent []storage.ThemeUse, day string) []string {
	since := storage.DayAfter(day, -14)
	seen := map[string]bool{}
	var out []string
	for _, u := range recent {
		if u.Day >= since && u.Title != "" && !seen[u.Title] && len(out) < 40 {
			seen[u.Title] = true
			out = append(out, u.Title)
		}
	}
	return out
}

func hadColdSince(recent []storage.ThemeUse, since, day string) bool {
	for _, u := range recent {
		if u.Day >= since && u.Day < day {
			var p Plan
			if json.Unmarshal([]byte(u.PlanJSON), &p) == nil && p.Cold {
				return true
			}
		}
	}
	return false
}

func inRanked(list []Ranked, th *Theme) bool {
	for _, r := range list {
		if r.Theme == th {
			return true
		}
	}
	return false
}

// forceTheme puts the required (cold) theme into the choice, replacing the last
// core theme, and marks it.
func forceTheme(chosen []Chosen, list []Ranked, id string) []Chosen {
	for i := range chosen {
		if chosen[i].Theme.ID() == id {
			chosen[i].Forced = true
			return chosen
		}
	}
	var req Ranked
	found := false
	for _, r := range list {
		if r.Theme.ID() == id {
			req, found = r, true
		}
	}
	if !found {
		return chosen
	}
	// Do not repeat a mood/style already chosen.
	for _, c := range chosen {
		if (req.Theme.Mood != "" && c.Theme.Mood == req.Theme.Mood) || (req.Theme.Style != "" && c.Theme.Style == req.Theme.Style) {
			return chosen
		}
	}
	for i := len(chosen) - 1; i >= 0; i-- {
		if Core(chosen[i].Theme.Type) {
			chosen[i] = Chosen{Ranked: req, Forced: true}
			return chosen
		}
	}
	return chosen
}
