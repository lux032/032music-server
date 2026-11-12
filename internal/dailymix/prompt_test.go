package dailymix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

func promptEnv(b *builder, length int) *selectEnv {
	return &selectEnv{now: testNow, day: "2026-10-16", explore: 0.3, length: length, recentMix: map[int64]string{},
		usedToday: map[int64]bool{}, usedTitles: map[string]bool{}, profile: b.profile}
}

func TestPromptThemeRequiresMoodAndStyle(t *testing.T) {
	b := testBuilder(synthLibrary(900), 30)
	th := b.promptTheme(PromptIntent{Moods: []string{"healing"}, Styles: []string{"piano"}, Energy: EnergyAny, Familiarity: FamiliarityMixed})
	picks := promptEnv(b, 30).build(th, seeded("t"))
	if len(picks) < MinTracks {
		t.Fatalf("picks = %d (pool %d)", len(picks), len(th.Pool))
	}
	// The synthetic library has only 5 artists with healing piano tracks; at 3 tracks
	// per artist that is 15 direct hits, the rest are neighbours (one side matches).
	both := 0
	for _, p := range picks {
		mood, style := moodRel(p.T, "healing") >= minRel, styleRel(p.T, "piano") >= minStyleRel
		if mood && style {
			both++
		} else if p.Reason.Via != "adjacent" {
			t.Fatalf("pick %d via %s matches neither condition fully", p.T.ID, p.Reason.Via)
		}
	}
	if both < 15 {
		t.Fatalf("only %d of %d picks are healing piano", both, len(picks))
	}
}

func TestPromptThemeNamedArtistFillsPlaylist(t *testing.T) {
	tracks := synthLibrary(900)
	b := testBuilder(tracks, 30)
	th := b.promptTheme(PromptIntent{PersonIDs: []int64{5}, Energy: EnergyAny, Familiarity: FamiliarityMixed})
	picks := promptEnv(b, 30).build(th, seeded("t"))
	if len(picks) < MinTracks {
		t.Fatalf("picks = %d (pool %d)", len(picks), len(th.Pool))
	}
	for _, p := range picks {
		if p.T.ArtistIDs[0] != 5 {
			t.Fatalf("pick by artist %d in a single-artist request", p.T.ArtistIDs[0])
		}
	}
}

func TestPromptThemeEnergyShiftsSelection(t *testing.T) {
	tracks := synthLibrary(900)
	for i, tr := range tracks {
		tr.Energy = (i/60)%5 + 1 // independent of the artist (synthLibrary ties them)
	}
	b := testBuilder(tracks, 25)
	avg := func(energy string) float64 {
		th := b.promptTheme(PromptIntent{Moods: []string{"cheerful"}, Energy: energy, Familiarity: FamiliarityMixed})
		picks := promptEnv(b, 25).build(th, seeded("e", energy))
		sum := 0
		for _, p := range picks {
			sum += Energy(p.T)
		}
		return float64(sum) / float64(len(picks))
	}
	low, high := avg(EnergyLow), avg(EnergyHigh)
	if !(low+1 < high) {
		t.Fatalf("energy low avg %.2f, high avg %.2f", low, high)
	}
}

func TestPromptThemeSadOnlyWhenAsked(t *testing.T) {
	tracks := synthLibrary(900)
	for i, tr := range tracks {
		if i%3 == 0 {
			tr.Moods = map[string]storage.RecTag{"sad": {Weight: 0.9, Confidence: 0.9, Source: "llm"}}
		}
	}
	b := testBuilder(tracks, 25)
	for _, p := range b.promptTheme(PromptIntent{Moods: []string{"healing"}, Energy: EnergyAny}).Pool {
		if _, sad := p.T.Moods["sad"]; sad {
			t.Fatalf("sad track %d in a healing prompt", p.T.ID)
		}
	}
	th := b.promptTheme(PromptIntent{Moods: []string{"sad"}, Energy: EnergyAny})
	if countVia(th.Pool, "tag") < 100 {
		t.Fatalf("sad prompt pool = %d", countVia(th.Pool, "tag"))
	}
	if b.allowSad {
		t.Fatal("promptTheme must restore the builder flag")
	}
}

func TestPromptResolveValidatesAgainstVocabularyAndLibrary(t *testing.T) {
	b := testBuilder(synthLibrary(240), 25)
	b.names = func(kind string, ids []int64) map[int64]string {
		out := map[int64]string{}
		for _, id := range ids {
			if kind == "artist" {
				out[id] = fmt.Sprintf("Artist %d", id)
			}
		}
		return out
	}
	cat := b.catalog()
	if len(cat.people) != 60 || cat.people[0] == "" {
		t.Fatalf("people = %d", len(cat.people))
	}
	in, dropped := cat.resolve(promptModelOutput{Moods: []string{"healing", "joy", "healing"}, Styles: []string{"piano"},
		People: []string{"  artist   7 ", "Nobody"}, Works: []string{"Unknown Work"}, Energy: "loud", Familiarity: "explore"})
	if strings.Join(in.Moods, ",") != "healing" || strings.Join(in.Styles, ",") != "piano" {
		t.Fatalf("tags = %v %v", in.Moods, in.Styles)
	}
	if len(in.PersonIDs) != 1 || in.PersonIDs[0] != 7 || len(in.WorkIDs) != 0 {
		t.Fatalf("entities = %v %v", in.PersonIDs, in.WorkIDs)
	}
	if in.Energy != EnergyAny || in.Familiarity != FamiliarityExplore {
		t.Fatalf("energy/familiarity = %s %s", in.Energy, in.Familiarity)
	}
	if len(dropped) != 3 {
		t.Fatalf("dropped = %v", dropped)
	}
	if !(PromptIntent{}).empty() || in.empty() {
		t.Fatal("empty()")
	}
}

func TestPromptSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(promptSchema(), &v); err != nil {
		t.Fatal(err)
	}
}

type promptChat struct {
	reply string
	err   error
	last  tagging.ChatRequest
}

func (f *promptChat) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	f.last = req
	return tagging.ChatResult{Content: f.reply, Usage: storage.AIUsage{Requests: 1, PromptTokens: 900, CompletionTokens: 80}}, f.err
}

func TestServiceFromPrompt(t *testing.T) {
	s, _ := storeLibrary(t, 400)
	ctx := context.Background()
	c := &clock{time.Date(2026, 10, 16, 9, 0, 0, 0, Zone)}
	chat := &promptChat{reply: `{"title":"午后钢琴","description":"安静的钢琴陪你度过午后","moods":["healing","relaxed"],"styles":["piano"],"people":[],"works":[],"energy":"low","familiarity":"mixed"}`}
	svc := newTestService(s, c, chat)

	if _, err := svc.FromPrompt(ctx, "下午安静的钢琴", 20); !errors.Is(err, ErrModelUnavailable) {
		t.Fatalf("unconfigured: %v", err)
	}
	if svc.PromptAvailable(ctx) {
		t.Fatal("available without a model")
	}
	ai := storage.DefaultAISettings()
	ai.BaseURL, ai.Model, ai.APIKey = "https://x.test/v1", "lite", "k"
	if err := s.SaveAISettings(ctx, ai); err != nil {
		t.Fatal(err)
	}
	if !svc.PromptAvailable(ctx) {
		t.Fatal("not available with a model")
	}

	pl, err := svc.FromPrompt(ctx, "下午安静的钢琴", 20)
	if err != nil {
		t.Fatalf("FromPrompt: %v (%s)", err, pl.LLM.Note)
	}
	if pl.Title != "午后钢琴" || pl.TitleSource != "llm" || pl.Description == "" {
		t.Fatalf("title = %q (%s) %q", pl.Title, pl.TitleSource, pl.Description)
	}
	if len(pl.Picks) < MinTracks || len(pl.Picks) > 20+8 {
		t.Fatalf("picks = %d", len(pl.Picks))
	}
	for _, p := range pl.Picks {
		if _, ok := p.T.Styles["piano"]; !ok && p.Reason.Via == "tag" {
			t.Fatalf("direct pick %d without piano", p.T.ID)
		}
	}
	var in promptModelInput
	if err := json.Unmarshal([]byte(chat.last.User), &in); err != nil || in.Prompt != "下午安静的钢琴" || len(in.People) == 0 || len(in.Moods) != len(tagging.Moods) {
		t.Fatalf("model input = %+v %v", in, err)
	}
	rows, err := s.AIUsageTotals(ctx, "2026-10-01")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		found = found || r.Purpose == "playlist"
	}
	if !found {
		t.Fatalf("usage not recorded: %+v", rows)
	}

	// A reading that matches nothing in the vocabulary or library.
	chat.reply = `{"title":"x","description":"","moods":["joy"],"styles":[],"people":["Nobody"],"works":[],"energy":"any","familiarity":"mixed"}`
	if _, err := svc.FromPrompt(ctx, "???", 20); !errors.Is(err, ErrPromptUnmatched) {
		t.Fatalf("unmatched: %v", err)
	}
	// Model failures are reported as such.
	chat.err = errors.New("boom")
	var me *ModelError
	if _, err := svc.FromPrompt(ctx, "钢琴", 20); !errors.As(err, &me) {
		t.Fatalf("model error: %v", err)
	}
}
