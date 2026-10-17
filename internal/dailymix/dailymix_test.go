package dailymix

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

var testNow = time.Date(2026, 10, 16, 7, 0, 0, 0, Zone)

// synthLibrary: n tracks over 60 artists / albums of 4, cycling through moods and styles.
func synthLibrary(n int) []*storage.RecTrack {
	moods := []string{"healing", "hotblooded", "cheerful", "relaxed", "epic", "romantic"}
	styles := []string{"pop", "rock", "piano", "orchestral"}
	var out []*storage.RecTrack
	for i := 0; i < n; i++ {
		t := &storage.RecTrack{ID: int64(i + 1), AlbumID: int64(i/4 + 1), Title: fmt.Sprintf("Song %d", i), ArtistIDs: []int64{int64(i%60 + 1)},
			ArtistNames: []string{fmt.Sprintf("Artist %d", i%60+1)}, TrackType: "regular", DurationMs: int64(200_000 + (i%7)*20_000),
			AddedAt: testNow.AddDate(-1, 0, 0), Energy: i%5 + 1, TagSource: "llm"}
		t.TitleKey = t.Title + fmt.Sprint(t.ArtistIDs)
		m := moods[i%len(moods)]
		t.Moods = map[string]storage.RecTag{m: {Weight: 0.7, Confidence: 0.85, Source: "llm"}}
		t.Styles = map[string]storage.RecTag{styles[(i/len(moods))%len(styles)]: {Weight: 1, Confidence: 0.8, Source: "llm"}}
		if i%3 == 0 {
			t.PlayCount = int64(2 + i%9)
			t.LastPlayed = testNow.AddDate(0, 0, -(i % 120))
		}
		out = append(out, t)
	}
	return out
}

func testBuilder(tracks []*storage.RecTrack, length int) *builder {
	p := BuildProfile(testNow, tracks, nil)
	return &builder{tracks: tracks, length: length, profile: p, names: func(string, []int64) map[int64]string { return map[int64]string{} }}
}

func TestSlotsAndDay(t *testing.T) {
	for n, want := range map[int][2]int{1: {1, 0}, 3: {2, 1}, 5: {4, 1}, 6: {4, 2}, 8: {6, 2}} {
		if c, a := Slots(n); c != want[0] || a != want[1] {
			t.Fatalf("Slots(%d) = %d,%d", n, c, a)
		}
	}
	if d := Day(time.Date(2026, 10, 16, 4, 59, 0, 0, Zone), 5); d != "2026-10-15" {
		t.Fatalf("before 05:00 = %s", d)
	}
	if d := Day(time.Date(2026, 10, 15, 21, 0, 0, 0, time.UTC), 5); d != "2026-10-16" { // 05:00 UTC+8
		t.Fatalf("at 05:00 = %s", d)
	}
}

func TestCandidatesAndNegativeRules(t *testing.T) {
	tracks := synthLibrary(600)
	// A clearly sad track and a mildly sad healing track.
	tracks[0].Moods = map[string]storage.RecTag{"sad": {Weight: 0.7, Confidence: 0.9, Source: "llm"}, "healing": {Weight: 0.3, Confidence: 0.8, Source: "llm"}}
	tracks[6].Moods = map[string]storage.RecTag{"healing": {Weight: 0.6, Confidence: 0.9, Source: "llm"}, "sad": {Weight: 0.4, Confidence: 0.8, Source: "llm"}}
	tracks[12].Moods = map[string]storage.RecTag{"hotblooded": {Weight: 0.6, Confidence: 0.9, Source: "llm"}, "sad": {Weight: 0.4, Confidence: 0.8, Source: "llm"}}
	if eligible(tracks[0], false) || !eligible(tracks[0], true) {
		t.Fatal("primary sad ≥0.5 is excluded unless allowed")
	}
	var el []*storage.RecTrack
	for _, tr := range tracks {
		if eligible(tr, false) {
			el = append(el, tr)
		}
	}
	cands := testBuilder(el, 25).Candidates()
	byID := map[string]*Theme{}
	for _, c := range cands {
		byID[c.ID()] = c
	}
	for _, want := range []string{"mood:healing", "mood:hotblooded", "style:pop", "style:piano"} {
		if byID[want] == nil {
			t.Fatalf("missing theme %s; have %d", want, len(cands))
		}
	}
	if byID["mood:sad"] != nil || byID["mood:dreamy"] != nil {
		t.Fatal("negative or empty moods must not be themes")
	}
	inPool := func(th *Theme, id int64) bool {
		for _, c := range th.Pool {
			if c.T.ID == id {
				return true
			}
		}
		return false
	}
	if !inPool(byID["mood:healing"], 7) {
		t.Fatal("mildly sad healing track may join the healing theme")
	}
	if inPool(byID["mood:hotblooded"], 13) {
		t.Fatal("mildly sad track must not join a high-energy theme")
	}
}

func checkMix(t *testing.T, picks []Pick, length int, th *Theme) {
	t.Helper()
	if len(picks) < MinTracks || len(picks) > length+8 {
		t.Fatalf("len = %d", len(picks))
	}
	artists, albums, titles := map[int64]int{}, map[int64]int{}, map[string]bool{}
	var dur int64
	explore := 0
	for i, p := range picks {
		for _, a := range p.T.ArtistIDs {
			artists[a]++
			if artists[a] > 3 {
				t.Fatalf("artist %d > 3", a)
			}
		}
		albums[p.T.AlbumID]++
		if albums[p.T.AlbumID] > 2 {
			t.Fatalf("album %d > 2", p.T.AlbumID)
		}
		if titles[p.T.TitleKey] {
			t.Fatal("duplicate title")
		}
		titles[p.T.TitleKey] = true
		dur += p.T.DurationMs
		if p.Bucket == "explore" {
			explore++
		}
		if i < 2 && p.Bucket != "familiar" {
			t.Fatalf("opener %d is %s", i, p.Bucket)
		}
		if i > 0 && p.T.ArtistIDs[0] == picks[i-1].T.ArtistIDs[0] {
			t.Fatal("same artist back to back")
		}
	}
	if time.Duration(dur)*time.Millisecond > maxMixDuration {
		t.Fatalf("duration %v", time.Duration(dur)*time.Millisecond)
	}
	if explore < len(picks)/5 {
		t.Fatalf("exploration %d of %d", explore, len(picks))
	}
}

func TestBuildConstraintsAndDeterminism(t *testing.T) {
	tracks := synthLibrary(900)
	b := testBuilder(tracks, 25)
	cands := b.Candidates()
	env := &selectEnv{now: testNow, day: "2026-10-16", explore: 0.3, length: 25, recentMix: map[int64]string{}, usedToday: map[int64]bool{}, usedTitles: map[string]bool{}, profile: b.profile}
	var th *Theme
	for _, c := range cands {
		if c.ID() == "mood:healing" {
			th = c
		}
	}
	a := env.build(th, seeded("x"))
	checkMix(t, a, 25, th)
	again := env.build(th, seeded("x"))
	for i := range a {
		if a[i].T.ID != again[i].T.ID {
			t.Fatal("same seed must give the same mix")
		}
	}
	other := env.build(th, seeded("y"))
	same := 0
	for i := range a {
		if a[i].T.ID == other[i].T.ID {
			same++
		}
	}
	if same == len(a) {
		t.Fatal("different seeds should differ")
	}
	// Recently recommended tracks step back.
	for _, p := range a {
		env.recentMix[p.T.ID] = "2026-10-15"
	}
	overlap := 0
	for _, p := range env.build(th, seeded("next day")) {
		if env.recentMix[p.T.ID] != "" {
			overlap++
		}
	}
	if overlap > len(a)/2 {
		t.Fatalf("novelty: %d of %d repeated", overlap, len(a))
	}
	// Energy: no jump above 2 when avoidable (synthetic energies cycle 1–5).
	jumps := 0
	for i := 1; i < len(a); i++ {
		if d := a[i].Reason.Energy - a[i-1].Reason.Energy; d > 2 || d < -2 {
			jumps++
		}
	}
	if jumps > 1 {
		t.Fatalf("energy jumps = %d", jumps)
	}
}

type fakeChat struct {
	reply string
	calls int
	last  tagging.ChatRequest
}

func (f *fakeChat) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	f.calls++
	f.last = req
	return tagging.ChatResult{Content: f.reply, Usage: storage.AIUsage{Requests: 1, PromptTokens: 1500, CompletionTokens: 200}}, nil
}

func TestPlanWithModelValidation(t *testing.T) {
	tracks := synthLibrary(900)
	b := testBuilder(tracks, 25)
	ranked := rank(b.Candidates(), planInput{day: "2026-10-16", stats: map[string]storage.ThemeStat{}, excludeID: map[string]bool{}, excludeMood: map[string]bool{}, excludeStyle: map[string]bool{}}, seeded("p"))
	list := shortlist(ranked, 2, 1)
	fallback := choose(list, 2, 1)
	if len(fallback) != 3 {
		t.Fatalf("fallback = %d", len(fallback))
	}
	var coreIDs, auxIDs []string
	for _, r := range list {
		if Core(r.Theme.Type) {
			coreIDs = append(coreIDs, r.Theme.ID())
		} else {
			auxIDs = append(auxIDs, r.Theme.ID())
		}
	}
	reply := func(items ...[3]string) string {
		var mixes []map[string]string
		for _, it := range items {
			mixes = append(mixes, map[string]string{"id": it[0], "title": it[1], "description": it[2]})
		}
		raw, _ := json.Marshal(map[string]any{"mixes": mixes})
		return string(raw)
	}
	third := fallback[2].Theme.ID()
	chat := &fakeChat{reply: reply([3]string{coreIDs[len(coreIDs)-1], "晚风里的温柔", "适合傍晚的轻柔旋律"}, [3]string{coreIDs[0], "燃起来吧", ""}, [3]string{third, "伤感夜曲", "x"})}
	p := BuildProfile(testNow, tracks, nil)
	got, res := planWithModel(context.Background(), chat, storage.AISettings{Model: "m"}, list, append([]Chosen(nil), fallback...), "", 2, 1, "2026-10-16", p, nil)
	if !res.Used || len(got) != 3 || !strings.Contains(res.Note, "模型选择") {
		t.Fatalf("res = %+v got %d", res, len(got))
	}
	if got[0].Title != "晚风里的温柔" || got[0].TitleSource != "llm" || got[2].TitleSource != "template" {
		t.Fatalf("titles = %q/%s %q/%s", got[0].Title, got[0].TitleSource, got[2].Title, got[2].TitleSource)
	}
	if !strings.Contains(chat.last.User, `"core":2`) || !strings.Contains(chat.last.System, "不能编造 id") {
		t.Fatal("prompt must carry the constraints")
	}
	// Unknown id → server choice with whatever titles were valid.
	chat.reply = reply([3]string{"mood:made_up", "x", ""}, [3]string{fallback[0].Theme.ID(), "好标题", "好简介"})
	got, res = planWithModel(context.Background(), chat, storage.AISettings{Model: "m"}, list, append([]Chosen(nil), fallback...), "", 2, 1, "2026-10-16", p, nil)
	if got[0].Theme != fallback[0].Theme || got[0].Title != "好标题" || !strings.Contains(res.Note, "编造或未知的主题") || !strings.Contains(res.Note, "补齐") || len(got) != 3 {
		t.Fatalf("fallback = %+v %s", got[0], res.Note)
	}
	// The same mood twice: the second is dropped and replaced, keeping the first.
	var sameMood []Ranked
	for _, r := range ranked {
		if r.Theme.Mood == fallback[0].Theme.Mood && r.Theme != fallback[0].Theme {
			sameMood = append(sameMood, r)
		}
	}
	if fallback[0].Theme.Mood != "" && len(sameMood) > 0 {
		byID := map[string]bool{}
		for _, r := range list {
			byID[r.Theme.ID()] = true
		}
		if byID[sameMood[0].Theme.ID()] {
			chat.reply = reply([3]string{fallback[0].Theme.ID(), "第一", ""}, [3]string{sameMood[0].Theme.ID(), "第二", ""}, [3]string{fallback[2].Theme.ID(), "第三", ""})
			got, res = planWithModel(context.Background(), chat, storage.AISettings{Model: "m"}, list, append([]Chosen(nil), fallback...), "", 2, 1, "2026-10-16", p, nil)
			if len(got) != 3 || !strings.Contains(res.Note, "情绪或风格重复") {
				t.Fatalf("same mood: %d %s", len(got), res.Note)
			}
		}
	}
	// Garbage → templates.
	chat.reply = "not json"
	got, res = planWithModel(context.Background(), chat, storage.AISettings{Model: "m"}, list, append([]Chosen(nil), fallback...), "", 2, 1, "2026-10-16", p, nil)
	if res.Used || got[0].TitleSource != "" && got[0].TitleSource != "template" {
		t.Fatalf("garbage: %+v", res)
	}
}

func TestRankCooldowns(t *testing.T) {
	tracks := synthLibrary(900)
	cands := testBuilder(tracks, 25).Candidates()
	in := planInput{day: "2026-10-16", stats: map[string]storage.ThemeStat{"mood:epic": {Alpha: 1, Beta: 1, CooldownUntil: "2026-11-01"}},
		recent:    []storage.ThemeUse{{Day: "2026-10-15", ThemeType: TypeMood, ThemeKey: "healing"}, {Day: "2026-10-12", ThemeType: TypeStyle, ThemeKey: "rock"}},
		excludeID: map[string]bool{}, excludeMood: map[string]bool{}, excludeStyle: map[string]bool{}}
	for _, r := range rank(cands, in, seeded("r")) {
		switch {
		case r.Theme.ID() == "mood:epic":
			t.Fatal("cooling theme")
		case r.Theme.Mood == "healing":
			t.Fatal("yesterday's mood")
		case r.Theme.ID() == "style:rock":
			t.Fatal("used this week")
		}
	}
	// Beta sampling favours themes with a better record.
	good, bad := 0, 0
	rng := seeded("beta")
	for i := 0; i < 2000; i++ {
		if betaSample(rng, 8, 2) > betaSample(rng, 2, 8) {
			good++
		} else {
			bad++
		}
	}
	if good < 1900 {
		t.Fatalf("beta: %d/%d", good, bad)
	}
}

func TestGenericStylesAndFamiliarFill(t *testing.T) {
	tracks := synthLibrary(900)
	// Make "pop" generic: 40% of the library carries it confidently.
	for i, tr := range tracks {
		if i%5 < 2 {
			tr.Styles["pop"] = storage.RecTag{Weight: 0.5, Confidence: 0.9, Source: "llm"}
		}
		// Second styles below 0.6 confidence are not direct hits.
		if i%7 == 0 {
			tr.Styles["jazz"] = storage.RecTag{Weight: 0.5, Confidence: 0.5, Source: "llm"}
		}
	}
	b := testBuilder(tracks, 25)
	byID := map[string]*Theme{}
	for _, c := range b.Candidates() {
		byID[c.ID()] = c
	}
	if byID["style:pop"] != nil || !b.generic["pop"] {
		t.Fatal("a generic style is not a theme of its own")
	}
	if byID["style:jazz"] != nil {
		t.Fatal("low-confidence styles are not direct hits")
	}
	for _, th := range byID {
		if th.Type == TypeStyle || th.Type == TypeMood {
			if adj := countVia(th.Pool, "adjacent"); adj > th.Direct {
				t.Fatalf("%s: %d neighbours > %d direct", th.ID(), adj, th.Direct)
			}
		}
	}
	// A theme whose own tracks are all unheard still gets familiar tracks from its
	// neighbours: mark every healing track unheard, neighbours stay familiar.
	for _, tr := range tracks {
		if _, ok := tr.Moods["healing"]; ok {
			tr.PlayCount, tr.Favorite, tr.InPlaylist = 0, false, false
		}
	}
	b = testBuilder(tracks, 25)
	var th *Theme
	for _, c := range b.Candidates() {
		if c.ID() == "mood:healing" {
			th = c
		}
	}
	env := &selectEnv{now: testNow, day: "2026-10-16", explore: 0.3, length: 25, recentMix: map[int64]string{}, usedToday: map[int64]bool{}, usedTitles: map[string]bool{}, profile: b.profile}
	fam := 0
	for _, p := range env.build(th, seeded("f")) {
		if p.Bucket == "familiar" {
			fam++
		}
	}
	if fam < 15 {
		t.Fatalf("familiar = %d", fam)
	}
}

func TestVersionsWeighedDown(t *testing.T) {
	tracks := synthLibrary(900)
	for i, tr := range tracks {
		if (i/6)%2 == 0 { // half of every mood
			tr.Version = "live"
		}
	}
	b := testBuilder(tracks, 25)
	env := &selectEnv{now: testNow, day: "2026-10-16", explore: 0.3, length: 25, recentMix: map[int64]string{}, usedToday: map[int64]bool{}, usedTitles: map[string]bool{}, profile: b.profile}
	live, total := 0, 0
	for _, c := range b.Candidates() {
		if c.Type != TypeMood {
			continue
		}
		for _, p := range env.build(c, seeded(c.ID())) {
			total++
			if p.T.Version == "live" {
				live++
			}
		}
	}
	if total == 0 || float64(live)/float64(total) > 0.42 {
		t.Fatalf("live share %d/%d", live, total)
	}
}

func TestFamiliarFloorAndArousalOrder(t *testing.T) {
	tracks := synthLibrary(900)
	for i, tr := range tracks {
		a := 1 + float64(i%81)/10 // continuous 1.0–9.0
		tr.Arousal = &a
		tr.Energy = 4 // the model's levels cluster
		if _, ok := tr.Moods["cheerful"]; ok {
			tr.PlayCount, tr.Favorite, tr.InPlaylist = 0, false, false
		}
	}
	b := testBuilder(tracks, 25)
	var th *Theme
	for _, c := range b.Candidates() {
		if c.ID() == "mood_style:cheerful+pop" || (th == nil && c.Type == TypeMoodStyle && c.Mood == "cheerful") {
			th = c
		}
	}
	if th == nil || th.Familiar != 0 || len(th.Bridge) == 0 {
		t.Fatalf("theme = %+v", th)
	}
	env := &selectEnv{now: testNow, day: "2026-10-16", explore: 0.3, length: 25, recentMix: map[int64]string{}, usedToday: map[int64]bool{}, usedTitles: map[string]bool{}, profile: b.profile}
	picks := env.build(th, seeded("floor"))
	fam := 0
	for _, p := range picks {
		if p.Bucket == "familiar" {
			fam++
			if p.Reason.Via != "bridge" {
				t.Fatalf("familiar via %s", p.Reason.Via)
			}
		}
	}
	if fam != 4 || picks[0].Bucket != "familiar" || picks[1].Bucket != "familiar" {
		t.Fatalf("familiar floor = %d, openers %s/%s", fam, picks[0].Bucket, picks[1].Bucket)
	}
	// Ordering follows the continuous arousal: rises to a peak, then falls.
	peak := 0
	for i, p := range picks {
		if p.Reason.Level > picks[peak].Reason.Level {
			peak = i
		}
	}
	if peak < len(picks)/3 || peak > len(picks)-3 || picks[0].Reason.Level >= picks[peak].Reason.Level-2 {
		t.Fatalf("arc: peak at %d, start %.1f, peak %.1f", peak, picks[0].Reason.Level, picks[peak].Reason.Level)
	}
	big := 0
	for i := 1; i < len(picks); i++ {
		if math.Abs(picks[i].Reason.Level-picks[i-1].Reason.Level) > 3 {
			big++
		}
	}
	if big > 2 {
		t.Fatalf("%d big jumps", big)
	}
}

func TestGenericCombosAndMoodFirstBridge(t *testing.T) {
	tracks := synthLibrary(900)
	for i, tr := range tracks {
		if i%5 < 2 {
			tr.Styles["pop"] = storage.RecTag{Weight: 0.5, Confidence: 0.9, Source: "llm"}
		}
	}
	b := testBuilder(tracks, 25)
	for _, c := range b.Candidates() {
		if c.Style == "pop" {
			t.Fatalf("generic style in %s", c.ID())
		}
	}
	// Bridge for a mood × style theme: a familiar track with the mood but another
	// style ranks above one with only the style.
	moodOnly := &storage.RecTrack{ID: 99001, PlayCount: 5, ArtistIDs: []int64{1}, Moods: map[string]storage.RecTag{"relaxed": {Weight: 1, Confidence: 0.9, Source: "llm"}},
		Styles: map[string]storage.RecTag{"jazz": {Weight: 1, Confidence: 0.9, Source: "llm"}}}
	styleOnly := &storage.RecTrack{ID: 99002, PlayCount: 5, ArtistIDs: []int64{2}, Moods: map[string]storage.RecTag{"hotblooded": {Weight: 1, Confidence: 0.9, Source: "llm"}},
		Styles: map[string]storage.RecTag{"rock": {Weight: 1, Confidence: 0.9, Source: "llm"}}}
	b2 := testBuilder([]*storage.RecTrack{moodOnly, styleOnly}, 25)
	b2.computeGeneric()
	got := b2.bridge(&Theme{Type: TypeMoodStyle, Mood: "relaxed", Style: "rock"})
	if len(got) != 2 || got[0].T.ID != 99001 || got[0].Rel <= got[1].Rel {
		t.Fatalf("bridge = %+v", got)
	}
}
