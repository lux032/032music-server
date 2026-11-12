package dailymix

// Prompt playlists (POST /api/v1/ai/playlists): the user describes a playlist in one
// sentence. The model only reads that sentence into search conditions — vocabulary
// moods/styles, artists and works that exist in the library, energy and familiarity —
// and writes a title; it never picks tracks. The tracks are selected by the same
// machinery as the daily mixes (tag relevance, acoustic expansion, familiar/explore
// quotas, artist/album caps, energy-arc ordering) from a theme built for the prompt.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// TypePrompt is the theme type of prompt playlists (never stored as a daily mix).
const TypePrompt = "prompt"

// Prompt limits.
const (
	PromptMaxRunes      = 200
	PromptDefaultTracks = 30
	PromptMaxTracks     = 50
	// promptPeople / promptWorks bound the library names sent to the model.
	promptPeople  = 300
	promptWorks   = 150
	maxPromptTags = 3
)

var (
	// ErrModelUnavailable: no OpenAI-compatible model service is configured.
	ErrModelUnavailable = errors.New("AI model service is not configured")
	// ErrPromptUnmatched: the model's reading matched no tag, artist or work.
	ErrPromptUnmatched = errors.New("the prompt matched nothing in the library")
	// ErrPromptTooFew: conditions were understood but too few tracks match them.
	ErrPromptTooFew = errors.New("too few library tracks match the prompt")
)

// ModelError wraps a failed model call (network, quota, invalid output).
type ModelError struct{ Err error }

func (e *ModelError) Error() string { return "model call failed: " + e.Err.Error() }
func (e *ModelError) Unwrap() error { return e.Err }

// Energy and familiarity values of an intent.
const (
	EnergyAny  = "any"
	EnergyLow  = "low"
	EnergyMid  = "mid"
	EnergyHigh = "high"

	FamiliarityFamiliar = "familiar"
	FamiliarityMixed    = "mixed"
	FamiliarityExplore  = "explore"
)

// PromptIntent is the validated reading of a prompt.
type PromptIntent struct {
	Moods       []string // vocabulary ids; may include the negative markers when asked for
	Styles      []string
	PersonIDs   []int64 // performers or composers
	WorkIDs     []int64
	Energy      string
	Familiarity string
}

func (in PromptIntent) empty() bool {
	return len(in.Moods) == 0 && len(in.Styles) == 0 && len(in.PersonIDs) == 0 && len(in.WorkIDs) == 0
}

// wantsSad reports whether the user explicitly asked for a negative mood.
func (in PromptIntent) wantsSad() bool {
	for _, m := range in.Moods {
		if t, ok := tagging.MoodByID(m); ok && t.Negative {
			return true
		}
	}
	return false
}

// PromptPlaylist is a generated (unsaved) prompt playlist.
type PromptPlaylist struct {
	Title       string
	Description string
	TitleSource string // llm | template
	Intent      PromptIntent
	PersonNames map[int64]string
	WorkNames   map[int64]string
	Picks       []Pick
	Pool        int
	LLM         LLMResult
}

// ---------------------------------------------------------------- model call

const promptSystem = `你是一位中文音乐电台的编辑。用户用一句话描述想听的歌单，你把它解析为检索条件并起名。你不选曲：曲目由服务端按这些条件从用户自己的曲库中挑选。
规则：
1. moods 与 styles 只能使用给出的词表 id，各 0–3 个，只选与描述明确相关的；描述只涉及情绪就不要硬选风格，反之亦然。只有用户明确想听伤感、孤独或黑暗的歌时才可以选 sad、lonely、dark。
2. people 与 works 只能原样使用 people / works 列表中的名称（可识别常见别名、译名并换成列表中的写法）；用户没有点名具体歌手、创作者或作品时留空，不要因为风格相近就加入。
3. energy：安静、助眠、放松 → low；运动、燃、蹦迪 → high；中等 → mid；不明确 → any。
4. familiarity：想听常听的、老歌、收藏 → familiar；想发现没听过的新歌 → explore；否则 mixed。
5. 描述较模糊时，也要选出最接近的情绪或风格，不要全部留空。
6. title 为中文标题（2–12 个字，有画面感，不用引号和表情符号）；description 为一句简介（不超过 36 个字），只描述听感，不编造曲目。
7. 只输出 JSON。`

type promptTagJSON struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

type promptModelInput struct {
	Prompt  string              `json:"prompt"`
	Moods   []promptTagJSON     `json:"moods"`
	Styles  []promptTagJSON     `json:"styles"`
	People  []string            `json:"people"`
	Works   []string            `json:"works"`
	Profile map[string][]string `json:"profile"`
}

type promptModelOutput struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Moods       []string `json:"moods"`
	Styles      []string `json:"styles"`
	People      []string `json:"people"`
	Works       []string `json:"works"`
	Energy      string   `json:"energy"`
	Familiarity string   `json:"familiarity"`
}

func promptSchema() json.RawMessage {
	moods, styles := make([]string, 0, len(tagging.Moods)), make([]string, 0, len(tagging.Styles))
	for _, m := range tagging.Moods {
		moods = append(moods, m.ID)
	}
	for _, s := range tagging.Styles {
		styles = append(styles, s.ID)
	}
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"title", "description", "moods", "styles", "people", "works", "energy", "familiarity"},
		"properties": map[string]any{
			"title":       map[string]any{"type": "string"},
			"description": map[string]any{"type": "string"},
			"moods":       map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": moods}},
			"styles":      map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": styles}},
			"people":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"works":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"energy":      map[string]any{"type": "string", "enum": []string{EnergyAny, EnergyLow, EnergyMid, EnergyHigh}},
			"familiarity": map[string]any{"type": "string", "enum": []string{FamiliarityFamiliar, FamiliarityMixed, FamiliarityExplore}},
		},
	}
	raw, _ := json.Marshal(schema)
	return raw
}

func vocabJSON(tags []tagging.Tag) []promptTagJSON {
	out := make([]promptTagJSON, 0, len(tags))
	for _, t := range tags {
		out = append(out, promptTagJSON{ID: t.ID, Label: t.Label, Hint: t.Hint})
	}
	return out
}

// promptCatalog is the library side of the model input and of name resolution.
type promptCatalog struct {
	people     []string           // display names, most tracks first
	works      []string           // titles, most tracks first
	personByID map[int64]string   // every performer/composer in the library
	workByID   map[int64]string   // every work in the library
	personKeys map[string][]int64 // normalised name → ids (merged duplicates share a name)
	workKeys   map[string][]int64
}

func nameKey(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// catalog ranks the library's performers, composers and works by track count and
// resolves their names (one query each).
func (b *builder) catalog() *promptCatalog {
	people, works := map[int64]int{}, map[int64]int{}
	for _, t := range b.tracks {
		for _, a := range t.ArtistIDs {
			people[a]++
		}
		for _, c := range t.Composers {
			people[c]++
		}
		for _, w := range t.Works {
			works[w]++
		}
	}
	ranked := func(m map[int64]int) []int64 {
		ids := make([]int64, 0, len(m))
		for id := range m {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			return m[ids[i]] > m[ids[j]] || (m[ids[i]] == m[ids[j]] && ids[i] < ids[j])
		})
		return ids
	}
	c := &promptCatalog{personKeys: map[string][]int64{}, workKeys: map[string][]int64{}}
	pIDs, wIDs := ranked(people), ranked(works)
	c.personByID = b.names("artist", pIDs)
	c.workByID = b.names("work", wIDs)
	collect := func(ids []int64, names map[int64]string, keys map[string][]int64, limit int) []string {
		var list []string
		seen := map[string]bool{}
		for _, id := range ids {
			name := strings.TrimSpace(names[id])
			if name == "" {
				continue
			}
			k := nameKey(name)
			keys[k] = append(keys[k], id)
			if !seen[k] && len(list) < limit {
				seen[k] = true
				list = append(list, name)
			}
		}
		return list
	}
	c.people = collect(pIDs, c.personByID, c.personKeys, promptPeople)
	c.works = collect(wIDs, c.workByID, c.workKeys, promptWorks)
	return c
}

// resolve validates the model output against the vocabulary and the library.
func (c *promptCatalog) resolve(out promptModelOutput) (PromptIntent, []string) {
	var in PromptIntent
	var dropped []string
	seen := map[string]bool{}
	for _, m := range out.Moods {
		m = strings.TrimSpace(m)
		if _, ok := tagging.MoodByID(m); !ok {
			dropped = append(dropped, "情绪 "+m)
		} else if !seen["m:"+m] && len(in.Moods) < maxPromptTags {
			seen["m:"+m] = true
			in.Moods = append(in.Moods, m)
		}
	}
	for _, s := range out.Styles {
		s = strings.TrimSpace(s)
		if _, ok := tagging.StyleByID(s); !ok {
			dropped = append(dropped, "风格 "+s)
		} else if !seen["s:"+s] && len(in.Styles) < maxPromptTags {
			seen["s:"+s] = true
			in.Styles = append(in.Styles, s)
		}
	}
	ids := func(names []string, keys map[string][]int64, kind string, dst *[]int64) {
		have := map[int64]bool{}
		for _, n := range names {
			list, ok := keys[nameKey(n)]
			if !ok {
				if strings.TrimSpace(n) != "" {
					dropped = append(dropped, kind+" "+n)
				}
				continue
			}
			for _, id := range list {
				if !have[id] {
					have[id] = true
					*dst = append(*dst, id)
				}
			}
		}
	}
	ids(out.People, c.personKeys, "歌手", &in.PersonIDs)
	ids(out.Works, c.workKeys, "作品", &in.WorkIDs)
	switch out.Energy {
	case EnergyLow, EnergyMid, EnergyHigh:
		in.Energy = out.Energy
	default:
		in.Energy = EnergyAny
	}
	switch out.Familiarity {
	case FamiliarityFamiliar, FamiliarityExplore:
		in.Familiarity = out.Familiarity
	default:
		in.Familiarity = FamiliarityMixed
	}
	return in, dropped
}

func readPrompt(ctx context.Context, chat tagging.Chat, st storage.AISettings, input promptModelInput) (promptModelOutput, LLMResult, error) {
	res := LLMResult{Model: st.Model}
	user, _ := json.Marshal(input)
	schema := promptSchema()
	start := time.Now()
	var out promptModelOutput
	var callErr error
	for _, format := range []string{"json_schema", "json_object"} {
		r, err := chat.Complete(ctx, tagging.ChatRequest{Model: st.Model, System: promptSystem, User: string(user), Schema: schema, Format: format, ReasoningEffort: st.ReasoningEffort})
		res.Usage.Add(r.Usage)
		if err != nil {
			callErr = err
			var le *tagging.LLMError
			if errors.As(err, &le) && le.Kind == tagging.KindFormat && format == "json_schema" {
				continue
			}
			break
		}
		callErr = json.Unmarshal([]byte(stripFences(r.Content)), &out)
		break
	}
	res.Millis = time.Since(start).Milliseconds()
	if callErr != nil {
		res.Note = "模型调用失败：" + shortError(callErr)
		return out, res, &ModelError{callErr}
	}
	res.Used = true
	return out, res, nil
}

// ---------------------------------------------------------------- pool

// energyFit scales a track's relevance by how well its energy fits the request.
func energyFit(t *storage.RecTrack, want string) float64 {
	e := Energy(t)
	switch want {
	case EnergyLow:
		return [6]float64{0, 1, 1, 0.6, 0.3, 0.15}[e]
	case EnergyHigh:
		return [6]float64{0, 0.15, 0.3, 0.6, 1, 1}[e]
	case EnergyMid:
		return [6]float64{0, 0.5, 1, 1, 1, 0.5}[e]
	}
	return 1
}

// promptTheme builds the theme of an intent. Tag conditions combine as "any requested
// mood AND any requested style" (tracks matching only one side, or only an adjacent
// tag, are neighbours that fill what is left); named people and works are direct
// hits and may make up most of the playlist.
func (b *builder) promptTheme(in PromptIntent) *Theme {
	b.computeGeneric()
	people := map[int64]bool{}
	for _, id := range in.PersonIDs {
		people[id] = true
	}
	works := map[int64]bool{}
	for _, id := range in.WorkIDs {
		works[id] = true
	}
	lowMood := false
	for _, m := range in.Moods {
		lowMood = lowMood || lowEnergyMoods[m]
	}
	allowSad := b.allowSad || in.wantsSad()
	// Bridge and acoustic expansion read the builder's flag.
	saved := b.allowSad
	b.allowSad = allowSad
	defer func() { b.allowSad = saved }()
	hasTags := len(in.Moods) > 0 || len(in.Styles) > 0
	hasEntities := len(people) > 0 || len(works) > 0

	best := func(t *storage.RecTrack, ids []string, rel func(*storage.RecTrack, string) float64) float64 {
		var r float64
		for _, id := range ids {
			r = math.Max(r, rel(t, id))
		}
		return r
	}
	adjacent := func(t *storage.RecTrack) float64 {
		var r float64
		for _, m := range in.Moods {
			for _, adj := range adjacentMoods[m] {
				if v := moodRel(t, adj); v >= 0.6 {
					r = math.Max(r, 0.45*v)
				}
			}
		}
		for _, s := range in.Styles {
			for _, adj := range adjacentStyles[s] {
				if v := styleRel(t, adj); v >= 0.75 && !b.generic[adj] {
					r = math.Max(r, 0.4*v)
				}
			}
		}
		return r
	}
	// tagRel: direct relevance when the track satisfies every requested tag kind, else
	// a neighbour relevance.
	tagRel := func(t *storage.RecTrack) (float64, string) {
		mr, sr := best(t, in.Moods, moodRel), best(t, in.Styles, styleRel)
		mOK, sOK := mr >= minRel, sr >= minStyleRel
		switch {
		case len(in.Moods) > 0 && len(in.Styles) > 0:
			if mOK && sOK {
				return math.Sqrt(mr * sr), "tag"
			}
			if mOK {
				return 0.5 * mr, "adjacent"
			}
			if sOK {
				return 0.5 * sr, "adjacent"
			}
		case len(in.Moods) > 0:
			if mOK {
				return mr, "tag"
			}
		case len(in.Styles) > 0:
			if sOK {
				return sr, "tag"
			}
		}
		if r := adjacent(t); r > 0 {
			return r, "adjacent"
		}
		return 0, ""
	}

	var pool []Candidate
	for _, t := range b.tracks {
		if !allowSad && mildlySad(t, false) && !lowMood {
			continue
		}
		if !allowSad && !eligible(t, false) {
			continue
		}
		var rel float64
		var via string
		if hasEntities {
			named := false
			for _, a := range t.ArtistIDs {
				named = named || people[a]
			}
			for _, c := range t.Composers {
				named = named || people[c]
			}
			for _, w := range t.Works {
				named = named || works[w]
			}
			if named {
				rel, via = 1, "artist"
				if len(works) > 0 && len(people) == 0 {
					via = "work"
				}
				if hasTags {
					// Named tracks that also fit the mood/style rank first.
					r, v := tagRel(t)
					if v == "tag" {
						rel = 1
					} else {
						rel = 0.7 + 0.3*r
					}
				}
			} else if hasTags {
				// Other artists only join as neighbours of a mood/style request.
				if r, v := tagRel(t); v == "tag" {
					rel, via = 0.5*r, "adjacent"
				}
			}
		} else if hasTags {
			rel, via = tagRel(t)
		}
		if rel <= 0 {
			continue
		}
		rel *= energyFit(t, in.Energy)
		if rel > 0.02 {
			pool = append(pool, Candidate{t, rel, via})
		}
	}
	th := &Theme{Type: TypePrompt, Key: TypePrompt, Pool: pool}
	if len(in.Moods) > 0 {
		th.Mood = in.Moods[0]
	}
	if len(in.Styles) > 0 {
		th.Style = in.Styles[0]
	}
	if hasTags && !hasEntities {
		th.Pool = b.expandAcoustic(capAdjacent(th.Pool), b.length)
	}
	if hasEntities {
		// Named artists/works may fill the playlist (limits as for a work theme); the
		// album limit still keeps one album from dominating.
		th.ArtistCap, th.AlbumCap = 4, 4
		if n := len(in.PersonIDs); n > 0 {
			th.ArtistCaps = map[int64]int{}
			per := max(3, int(math.Ceil(float64(b.length)/float64(n))))
			for _, id := range in.PersonIDs {
				th.ArtistCaps[id] = per
			}
		}
	}
	th.Familiar, th.Explore = countBuckets(th.Pool)
	th.Direct = len(th.Pool) - countVia(th.Pool, "adjacent") - countVia(th.Pool, "acoustic")
	if th.Mood != "" || th.Style != "" {
		th.Bridge = b.bridge(th)
	}
	return th
}

// promptExplore is the explore share for a familiarity request.
func promptExplore(familiarity string, set storage.DailyMixSettings) float64 {
	switch familiarity {
	case FamiliarityFamiliar:
		return 0.1
	case FamiliarityExplore:
		return 0.6
	}
	return float64(set.ExplorePercent) / 100
}

// templatePromptTitle names a playlist when the model's title is unusable.
func templatePromptTitle(in PromptIntent, people, works map[int64]string) string {
	var parts []string
	for _, id := range in.WorkIDs {
		if w := works[id]; w != "" {
			parts = append(parts, "《"+w+"》")
			break
		}
	}
	for _, id := range in.PersonIDs {
		if p := people[id]; p != "" {
			parts = append(parts, p)
			break
		}
	}
	for _, m := range in.Moods {
		parts = append(parts, tagging.TagLabel("mood", m))
		break
	}
	for _, s := range in.Styles {
		parts = append(parts, tagging.TagLabel("style", s))
		break
	}
	if len(parts) == 0 {
		return "AI 灵感歌单"
	}
	title := strings.Join(parts, " · ")
	if utf8.RuneCountInString(title) > 24 {
		title = string([]rune(title)[:24])
	}
	return title
}

// ---------------------------------------------------------------- generation

// GenerateFromPrompt builds an unsaved playlist for a one-sentence description.
func (g *Generator) GenerateFromPrompt(ctx context.Context, now time.Time, prompt string, length int, set storage.DailyMixSettings, ai storage.AISettings) (PromptPlaylist, error) {
	prompt = strings.TrimSpace(prompt)
	if !ai.Configured() || g.Chat == nil {
		return PromptPlaylist{}, ErrModelUnavailable
	}
	chat, err := g.Chat(ai)
	if err != nil || chat == nil {
		return PromptPlaylist{}, ErrModelUnavailable
	}
	if length <= 0 {
		length = PromptDefaultTracks
	}
	length = min(max(length, MinTracks), PromptMaxTracks)
	day := Day(now, set.PregenerateHour)
	// Sad tracks stay in the library snapshot; the theme drops them unless asked for.
	loose := set
	loose.AllowSad = true
	p, err := g.prepareBase(ctx, now, day, loose)
	if err != nil {
		return PromptPlaylist{}, err
	}
	p.b.allowSad = set.AllowSad
	p.b.length = length
	cat := p.b.catalog()
	moods, styles := p.profile.Summary(4)
	input := promptModelInput{Prompt: prompt, Moods: vocabJSON(tagging.Moods), Styles: vocabJSON(tagging.Styles),
		People: cat.people, Works: cat.works, Profile: map[string][]string{"moods": moods, "styles": styles}}
	if input.People == nil {
		input.People = []string{}
	}
	if input.Works == nil {
		input.Works = []string{}
	}
	lctx, cancel := context.WithTimeout(ctx, g.LLMTimeout)
	out, res, err := readPrompt(lctx, chat, ai, input)
	cancel()
	if res.Usage.Requests > 0 || res.Usage.PromptTokens > 0 {
		if res.Usage.Requests == 0 {
			res.Usage.Requests = 1
		}
		_ = g.Store.AddAIUsage(ctx, now, ai.Model, "playlist", res.Usage)
	}
	if err != nil {
		return PromptPlaylist{LLM: res}, err
	}
	intent, dropped := cat.resolve(out)
	pl := PromptPlaylist{Intent: intent, PersonNames: map[int64]string{}, WorkNames: map[int64]string{}, LLM: res}
	for _, id := range intent.PersonIDs {
		pl.PersonNames[id] = cat.personByID[id]
	}
	for _, id := range intent.WorkIDs {
		pl.WorkNames[id] = cat.workByID[id]
	}
	if len(dropped) > 0 {
		pl.LLM.Note = "已忽略不在词表或曲库中的条件：" + strings.Join(firstN(dropped, 5), "、")
	}
	if intent.empty() {
		return pl, ErrPromptUnmatched
	}
	th := p.b.promptTheme(intent)
	pl.Pool = len(th.Pool)
	env := p.env(now, day, set)
	env.length = length
	env.explore = promptExplore(intent.Familiarity, set)
	env.allowSad = set.AllowSad || intent.wantsSad()
	pl.Picks = env.build(th, seeded("prompt", prompt, now.UnixNano()))
	if pl.Picks == nil {
		return pl, ErrPromptTooFew
	}
	pl.Title, pl.TitleSource = templatePromptTitle(intent, pl.PersonNames, pl.WorkNames), "template"
	if validTitle(out.Title, 16) {
		pl.Title, pl.TitleSource = strings.TrimSpace(out.Title), "llm"
	}
	if validDescription(out.Description) {
		pl.Description = strings.TrimSpace(out.Description)
	} else {
		pl.Description = fmt.Sprintf("根据「%s」从你的曲库中挑选的 %d 首歌", truncateRunes(prompt, 20), len(pl.Picks))
	}
	return pl, nil
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
