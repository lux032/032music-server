package dailymix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// Ranked is a candidate theme with its sampled score.
type Ranked struct {
	Theme  *Theme
	Sample float64 // Thompson sample from Beta(α, β)
	Score  float64 // Sample × Lift
	Cold   bool    // eligible for the weekly “cold theme” slot
}

// Chosen is a theme picked for a slot.
type Chosen struct {
	Ranked
	Title, Description string
	TitleSource        string // template | llm
	Forced             bool   // the weekly cold slot
}

// betaSample draws from Beta(a, b) via two gamma draws (Marsaglia–Tsang).
func betaSample(rng *rand.Rand, a, b float64) float64 {
	x, y := gammaSample(rng, a), gammaSample(rng, b)
	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

func gammaSample(rng *rand.Rand, k float64) float64 {
	if k < 1 {
		return gammaSample(rng, k+1) * math.Pow(rng.Float64(), 1/k)
	}
	d := k - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := rng.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := rng.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}

// planInput is everything the planner needs besides the candidates.
type planInput struct {
	day                       string
	stats                     map[string]storage.ThemeStat
	recent                    []storage.ThemeUse // last 30 days (any status)
	excludeID                 map[string]bool    // themes already used today
	excludeMood, excludeStyle map[string]bool
}

// rank applies cooldowns and samples a score for every candidate.
func rank(cands []*Theme, in planInput, rng *rand.Rand) []Ranked {
	yesterday := storage.DayAfter(in.day, -1)
	weekAgo := storage.DayAfter(in.day, -7)
	monthAgo := storage.DayAfter(in.day, -30)
	usedWeek := map[string]bool{}
	usedMonth := map[string]bool{}
	yesterdayMoods := map[string]bool{}
	for _, u := range in.recent {
		if u.Day == in.day {
			continue
		}
		id := u.ThemeType + ":" + u.ThemeKey
		if u.Day >= weekAgo {
			usedWeek[id] = true
		}
		if u.Day >= monthAgo {
			usedMonth[id] = true
		}
		if u.Day == yesterday {
			th := &Theme{Type: u.ThemeType, Key: u.ThemeKey}
			if m := themeMood(th); m != "" {
				yesterdayMoods[m] = true
			}
		}
	}
	var out []Ranked
	for _, th := range cands {
		id := th.ID()
		st, ok := in.stats[id]
		if !ok {
			st = storage.ThemeStat{Alpha: 1, Beta: 1}
		}
		if in.excludeID[id] || usedWeek[id] || (st.CooldownUntil != "" && in.day < st.CooldownUntil) {
			continue
		}
		if th.Mood != "" && (yesterdayMoods[th.Mood] || in.excludeMood[th.Mood]) {
			continue
		}
		if th.Style != "" && in.excludeStyle[th.Style] {
			continue
		}
		s := betaSample(rng, math.Max(st.Alpha, 0.1), math.Max(st.Beta, 0.1))
		out = append(out, Ranked{Theme: th, Sample: s, Score: s * th.Lift, Cold: !usedMonth[id] && th.Lift <= 1.0 && Core(th.Type)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Theme.ID() < out[j].Theme.ID()
		}
		return out[i].Score > out[j].Score
	})
	return out
}

// themeMood recovers the mood of a stored theme key.
func themeMood(th *Theme) string {
	switch th.Type {
	case TypeMood, TypeRediscover:
		return th.Key
	case TypeMoodStyle:
		m, _, _ := strings.Cut(th.Key, "+")
		return m
	}
	return ""
}

// shortlist picks up to 2×need themes without repeating a mood or style, keeping
// the core/auxiliary ratio.
func shortlist(ranked []Ranked, core, aux int) []Ranked {
	moods, styles := map[string]bool{}, map[string]bool{}
	var out []Ranked
	nCore, nAux := 0, 0
	for _, r := range ranked {
		th := r.Theme
		if (th.Mood != "" && moods[th.Mood]) || (th.Style != "" && styles[th.Style]) {
			continue
		}
		if Core(th.Type) {
			if nCore >= 2*core+aux { // spare core themes replace missing auxiliary ones
				continue
			}
			nCore++
		} else {
			if nAux >= 2*aux {
				continue
			}
			nAux++
		}
		if th.Mood != "" {
			moods[th.Mood] = true
		}
		if th.Style != "" {
			styles[th.Style] = true
		}
		out = append(out, r)
	}
	return out
}

// choose takes the best themes in order: core slots first, then auxiliary ones
// (filled with core themes when there are not enough).
func choose(list []Ranked, core, aux int) []Chosen {
	var out []Chosen
	nCore, nAux := 0, 0
	for _, r := range list {
		if Core(r.Theme.Type) && nCore < core {
			out = append(out, Chosen{Ranked: r})
			nCore++
		} else if !Core(r.Theme.Type) && nAux < aux {
			out = append(out, Chosen{Ranked: r})
			nAux++
		}
	}
	for _, r := range list {
		if len(out) >= core+aux {
			break
		}
		if Core(r.Theme.Type) && !containsTheme(out, r.Theme) {
			out = append(out, Chosen{Ranked: r})
		}
	}
	return out
}

func containsTheme(list []Chosen, th *Theme) bool {
	for _, c := range list {
		if c.Theme == th {
			return true
		}
	}
	return false
}

// Slots splits N into core (mood/style) and auxiliary slots: ⌈2N/3⌉ + the rest.
func Slots(n int) (core, aux int) {
	core = (2*n + 2) / 3
	return core, n - core
}

// ---------------------------------------------------------------- titles by model

// bannedTitleWords keeps titles positive (no negative themes are shown).
var bannedTitleWords = []string{"伤感", "悲伤", "孤独", "黑暗", "忧郁", "抑郁", "难过", "哭", "绝望", "痛苦", "寂寞", "压抑"}

func validTitle(title string, maxRunes int) bool {
	title = strings.TrimSpace(title)
	if n := utf8.RuneCountInString(title); n < 2 || n > maxRunes {
		return false
	}
	for _, w := range bannedTitleWords {
		if strings.Contains(title, w) {
			return false
		}
	}
	return !strings.ContainsAny(title, "\n\r")
}

// validDescription: may be empty (the template is used), at most 50 characters, positive.
func validDescription(d string) bool {
	d = strings.TrimSpace(d)
	return d == "" || validTitle(d, 50)
}

const plannerSystem = `你是一位中文音乐电台的编辑，负责给“每日推荐歌单”选主题并起名。
规则：
1. 只能从 candidates 中选择主题 id，不能编造 id，不能重复；必须恰好选 need.total 个，其中情绪/风格类（core=true）恰好 need.core 个；required 中的 id 必须选上。
2. 优先选与听歌偏好（profile）相符的主题，但也要有变化；不要与 recentTitles 中的标题雷同。
3. 每个歌单起一个中文标题（2–12 个字，有画面感、不要书名号以外的引号、不要表情符号），以及一句简介（不超过 36 个字）。
4. 标题和简介只描述主题和听感；作品名、歌手名只能使用候选中给出的名称；不要出现伤感、孤独、黑暗等负面词。
5. 只输出 JSON。`

type plannerCandidate struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Core     bool     `json:"core"`
	Label    string   `json:"label"`
	Tracks   int      `json:"tracks"`
	Examples []string `json:"examples"`
}

type plannerInput struct {
	Date         string              `json:"date"`
	Weekday      string              `json:"weekday"`
	Profile      map[string][]string `json:"profile"`
	RecentTitles []string            `json:"recentTitles"`
	Need         map[string]int      `json:"need"`
	Required     []string            `json:"required"`
	Candidates   []plannerCandidate  `json:"candidates"`
}

type plannerOutput struct {
	Mixes []struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	} `json:"mixes"`
}

var plannerSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["mixes"],"properties":{"mixes":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","title","description"],"properties":{"id":{"type":"string"},"title":{"type":"string"},"description":{"type":"string"}}}}}}`)

var weekdays = []string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}

// examples lists a few representative tracks of a theme (best relevance × preference).
func examples(th *Theme, n int) []string {
	pool := append([]Candidate(nil), th.Pool...)
	sort.Slice(pool, func(i, j int) bool {
		a, b := pool[i].Rel*preference(pool[i].T), pool[j].Rel*preference(pool[j].T)
		return a > b || (a == b && pool[i].T.ID < pool[j].T.ID)
	})
	var out []string
	seen := map[string]bool{}
	for _, c := range pool {
		if len(out) >= n {
			break
		}
		if seen[c.T.TitleKey] {
			continue
		}
		seen[c.T.TitleKey] = true
		s := c.T.Title
		if len(c.T.ArtistNames) > 0 {
			s += " - " + strings.Join(c.T.ArtistNames, "、")
		}
		out = append(out, s)
	}
	return out
}

// LLMResult describes the model call for the run log.
type LLMResult struct {
	Used   bool
	Model  string
	Note   string
	Usage  storage.AIUsage
	Millis int64
}

// planWithModel asks the model to pick need themes from the shortlist and title them.
// On any problem it returns the fallback choice with template titles and a note.
func planWithModel(ctx context.Context, chat tagging.Chat, st storage.AISettings, list []Ranked, fallback []Chosen, required string,
	core, aux int, day string, profile *Profile, recentTitles []string) ([]Chosen, LLMResult) {
	res := LLMResult{Model: st.Model}
	moods, styles := profile.Summary(4)
	in := plannerInput{Date: day, Profile: map[string][]string{"moods": moods, "styles": styles}, RecentTitles: recentTitles,
		Need: map[string]int{"total": len(fallback), "core": countCore(fallback)}}
	if t, err := time.Parse("2006-01-02", day); err == nil {
		in.Weekday = weekdays[t.Weekday()]
	}
	if required != "" {
		in.Required = []string{required}
	}
	byID := map[string]Ranked{}
	for _, r := range list {
		th := r.Theme
		byID[th.ID()] = r
		in.Candidates = append(in.Candidates, plannerCandidate{ID: th.ID(), Type: TypeLabel(th.Type), Core: Core(th.Type), Label: th.Label(), Tracks: len(th.Pool), Examples: examples(th, 3)})
	}
	user, _ := json.Marshal(in)
	start := time.Now()
	var out plannerOutput
	var callErr error
	for _, format := range []string{"json_schema", "json_object"} {
		r, err := chat.Complete(ctx, tagging.ChatRequest{Model: st.Model, System: plannerSystem, User: string(user), Schema: plannerSchema, Format: format, ReasoningEffort: st.ReasoningEffort})
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
		res.Note = "模型调用失败，使用模板标题：" + shortError(callErr)
		return fallback, res
	}
	// Validate the selection.
	var chosen []Chosen
	seen := map[string]bool{}
	moodsUsed, stylesUsed := map[string]bool{}, map[string]bool{}
	titled := map[string][2]string{}
	valid := true
	for _, m := range out.Mixes {
		r, ok := byID[m.ID]
		if !ok || seen[m.ID] {
			valid = false
			continue
		}
		seen[m.ID] = true
		th := r.Theme
		if (th.Mood != "" && moodsUsed[th.Mood]) || (th.Style != "" && stylesUsed[th.Style]) {
			valid = false
			continue
		}
		if th.Mood != "" {
			moodsUsed[th.Mood] = true
		}
		if th.Style != "" {
			stylesUsed[th.Style] = true
		}
		c := Chosen{Ranked: r, Title: th.TemplateTitle(), TitleSource: "template"}
		if validTitle(m.Title, 16) && validDescription(m.Description) {
			c.Title, c.Description, c.TitleSource = strings.TrimSpace(m.Title), strings.TrimSpace(m.Description), "llm"
			titled[m.ID] = [2]string{c.Title, c.Description}
		}
		chosen = append(chosen, c)
	}
	if valid && len(chosen) == len(fallback) && countCore(chosen) == countCore(fallback) && (required == "" || seen[required]) {
		res.Used = true
		res.Note = "模型选择了主题并起名"
		return chosen, res
	}
	// Invalid selection: keep the server's choice, reuse any valid titles.
	for i := range fallback {
		if t, ok := titled[fallback[i].Theme.ID()]; ok {
			fallback[i].Title, fallback[i].Description, fallback[i].TitleSource = t[0], t[1], "llm"
		}
	}
	res.Used = true
	res.Note = "模型的选择不符合约束，按服务端排序选择主题"
	return fallback, res
}

func countCore(list []Chosen) int {
	n := 0
	for _, c := range list {
		if Core(c.Theme.Type) {
			n++
		}
	}
	return n
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}

func shortError(err error) string {
	msg := err.Error()
	if utf8.RuneCountInString(msg) > 120 {
		msg = string([]rune(msg)[:120]) + "…"
	}
	return msg
}

// describe is a compact debug line for a ranked theme.
func describe(r Ranked) string {
	return fmt.Sprintf("%s（%d 首，抽样 %.2f × 偏好 %.2f = %.2f）", r.Theme.Label(), len(r.Theme.Pool), r.Sample, r.Theme.Lift, r.Score)
}
