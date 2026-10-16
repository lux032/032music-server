package dailymix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// storeLibrary imports n tagged tracks (moods/styles cycling) and returns the store.
func storeLibrary(t *testing.T, n int) (*storage.Store, []int64) {
	t.Helper()
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "mix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := s.LibraryByRoot(ctx, "/music")
	moods := []string{"healing", "hotblooded", "cheerful", "relaxed", "epic"}
	styles := []string{"pop", "rock", "piano"}
	for i := 0; i < n; i++ {
		if err = s.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("A%03d/%02d.flac", i/4, i%4), FileSize: int64(1000 + i), ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song %d", i), Album: fmt.Sprintf("Album %d", i/4), Artists: []string{fmt.Sprintf("Artist %d", i%50)},
				AlbumArtists: []string{fmt.Sprintf("Artist %d", i%50)}, DiscNumber: 1, TrackNumber: i%4 + 1, DurationMillis: int64(180_000 + (i%5)*15_000)}}); err != nil {
			t.Fatal(err)
		}
	}
	lib2, err := s.RecLibrary(ctx)
	if err != nil || len(lib2) != n {
		t.Fatalf("rec library = %d %v", len(lib2), err)
	}
	var ids []int64
	for i, tr := range lib2 {
		ids = append(ids, tr.ID)
		energy := i%5 + 1
		meta := storage.TrackTagMeta{TrackID: tr.ID, SourceStamp: "s", InputHash: "h", VocabVersion: 1, PromptVersion: 1, Model: "m", Energy: energy}
		tags := []storage.TrackTag{{Kind: "mood", Tag: moods[i%len(moods)], Weight: 1, Confidence: 0.9, Source: "llm"},
			{Kind: "style", Tag: styles[(i/len(moods))%len(styles)], Weight: 1, Confidence: 0.8, Source: "llm"}}
		if err := s.SaveTrackTagResult(ctx, meta, tags, nil); err != nil {
			t.Fatal(err)
		}
		if i%4 == 0 {
			if err := s.SetTrackFavorite(ctx, tr.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, ids
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestService(s *storage.Store, c *clock, chat tagging.Chat) *Service {
	return New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Now: c.now,
		NewChat: func(storage.AISettings) (tagging.Chat, error) { return chat, nil }})
}

func TestServiceGenerateReplaceAndFeedback(t *testing.T) {
	s, ids := storeLibrary(t, 400)
	ctx := context.Background()
	c := &clock{time.Date(2026, 10, 16, 9, 0, 0, 0, Zone)}
	chat := &fakeChat{reply: "{}"}
	svc := newTestService(s, c, chat)

	set, _ := s.DailyMixSettings(ctx)
	if !set.Enabled || set.MixCount != 5 || set.TrackCount != 25 {
		t.Fatalf("defaults = %+v", set)
	}
	set.MixCount = 3
	if err := s.SaveDailyMixSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	status, day, mixes, err := svc.Today(ctx, 30*time.Second)
	if err != nil || status != StatusReady || day != "2026-10-16" || len(mixes) != 3 {
		t.Fatalf("today = %s %s %d %v; run %+v", status, day, len(mixes), err, svc.Snapshot(ctx).LastRun)
	}
	if chat.calls != 0 {
		t.Fatal("no model calls without AI settings")
	}
	seen := map[int64]bool{}
	moods := map[string]bool{}
	for _, m := range mixes {
		items, _ := s.DailyMixItems(ctx, m.ID)
		if len(items) < MinTracks || m.TrackCount != len(items) || m.TitleSource != "template" || m.Description == "" {
			t.Fatalf("mix %+v items %d", m, len(items))
		}
		for _, it := range items {
			if seen[it.TrackID] {
				t.Fatal("tracks must be exclusive across the day's mixes")
			}
			seen[it.TrackID] = true
		}
		var plan Plan
		if err := json.Unmarshal([]byte(m.PlanJSON), &plan); err != nil || plan.ThemeID != m.ThemeType+":"+m.ThemeKey {
			t.Fatalf("plan = %s", m.PlanJSON)
		}
		if plan.Mood != "" {
			if moods[plan.Mood] {
				t.Fatal("a mood may only be used once a day")
			}
			moods[plan.Mood] = true
		}
	}
	// Second call: same mixes, no regeneration.
	if _, _, again, _ := svc.Today(ctx, time.Second); again[0].ID != mixes[0].ID {
		t.Fatal("mixes are frozen for the day")
	}

	// Replace: limit 3 per day for users, admin unlimited.
	set.RegenerateLimit = 1
	_ = s.SaveDailyMixSettings(ctx, set)
	repl, _, err := svc.Replace(ctx, mixes[0].ID, false)
	if err != nil || repl.Slot != mixes[0].Slot || repl.Generation != 1 || repl.ThemeKey == mixes[0].ThemeKey {
		t.Fatalf("replace = %+v %v", repl, err)
	}
	if _, _, err = svc.Replace(ctx, mixes[0].ID, true); !errors.Is(err, ErrNotActive) {
		t.Fatalf("replacing a replaced mix: %v", err)
	}
	if _, _, err = svc.Replace(ctx, repl.ID, false); !errors.Is(err, ErrReplaceLimit) {
		t.Fatalf("limit: %v", err)
	}
	if _, _, err = svc.Replace(ctx, repl.ID, true); err != nil {
		t.Fatalf("admin replace: %v", err)
	}
	newItems, _ := s.DailyMixItems(ctx, repl.ID)
	for _, it := range newItems {
		if seen[it.TrackID] {
			t.Fatal("a replacement uses tracks not in today's mixes")
		}
	}

	// Rating: like → α+1; switching to dislike(theme) reverts it and cools down 45 days.
	key := mixes[1].ThemeType + ":" + mixes[1].ThemeKey
	if _, err = s.RateDailyMix(ctx, mixes[1].ID, 1, nil, c.t); err != nil {
		t.Fatal(err)
	}
	st, _ := s.ThemeStats(ctx)
	if st[key].Alpha != 2 || st[key].Beta != 1 {
		t.Fatalf("like stats = %+v", st[key])
	}
	if _, err = s.RateDailyMix(ctx, mixes[1].ID, 1, []string{"theme"}, c.t); err == nil {
		t.Fatal("reasons only with a dislike")
	}
	if _, err = s.RateDailyMix(ctx, mixes[1].ID, -1, []string{"theme", "bogus"}, c.t); err == nil {
		t.Fatal("unknown reason")
	}
	m, err := s.RateDailyMix(ctx, mixes[1].ID, -1, []string{"theme", "flow"}, c.t)
	if err != nil || m.Rating != -1 || m.RatingReasons != "theme,flow" {
		t.Fatalf("dislike = %+v %v", m, err)
	}
	st, _ = s.ThemeStats(ctx)
	if st[key].Alpha != 1 || st[key].Beta != 4 || st[key].CooldownUntil != "2026-11-30" {
		t.Fatalf("dislike stats = %+v", st[key])
	}
	if _, err = s.RateDailyMix(ctx, mixes[1].ID, 0, nil, c.t); err != nil {
		t.Fatal(err)
	}
	st, _ = s.ThemeStats(ctx)
	if st[key].Alpha != 1 || st[key].Beta != 1 {
		t.Fatalf("cleared stats = %+v", st[key])
	}

	// Save as playlist: once counted.
	p, err := s.SaveDailyMixAsPlaylist(ctx, mixes[2].ID, "每日推荐", "")
	if err != nil || p.ItemCount != int64(mixes[2].TrackCount) {
		t.Fatalf("save = %+v %v", p, err)
	}
	_, _ = s.SaveDailyMixAsPlaylist(ctx, mixes[2].ID, "每日推荐 2", "")
	st, _ = s.ThemeStats(ctx)
	if k := mixes[2].ThemeType + ":" + mixes[2].ThemeKey; st[k].Alpha != 1.5 {
		t.Fatalf("save stats = %+v", st[k])
	}

	// Next day: blocked tracks never appear, yesterday's moods are skipped,
	// yesterday's tracks mostly step back.
	if err := s.SetRecommendationBlock(ctx, ids[0], true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecommendationBlock(ctx, 999999, true); err == nil {
		t.Fatal("unknown track")
	}
	yesterdayMoods := map[string]bool{}
	active, _ := s.DailyMixes(ctx, "2026-10-16")
	for _, m := range active {
		if mood := themeMood(&Theme{Type: m.ThemeType, Key: m.ThemeKey}); mood != "" {
			yesterdayMoods[mood] = true
		}
	}
	c.t = c.t.Add(24 * time.Hour)
	_, day2, mixes2, err := svc.Today(ctx, 30*time.Second)
	if err != nil || day2 != "2026-10-17" || len(mixes2) == 0 {
		t.Fatalf("day 2 = %s %d %v", day2, len(mixes2), err)
	}
	for _, m := range mixes2 {
		items, _ := s.DailyMixItems(ctx, m.ID)
		for _, it := range items {
			if it.TrackID == ids[0] {
				t.Fatal("blocked track recommended")
			}
		}
		var plan Plan
		_ = json.Unmarshal([]byte(m.PlanJSON), &plan)
		if plan.Mood != "" && yesterdayMoods[plan.Mood] {
			t.Fatalf("mood %s repeated the next day", plan.Mood)
		}
	}
}

func TestServiceWithModelTitlesAndNotReady(t *testing.T) {
	s, _ := storeLibrary(t, 400)
	ctx := context.Background()
	c := &clock{time.Date(2026, 10, 16, 9, 0, 0, 0, Zone)}
	ai := storage.DefaultAISettings()
	ai.BaseURL, ai.Model, ai.APIKey = "https://x.test/v1", "lite", "k"
	if err := s.SaveAISettings(ctx, ai); err != nil {
		t.Fatal(err)
	}
	// The model answers with the first two shortlist ids it sees, nicely titled.
	chat := &dynamicChat{}
	svc := newTestService(s, c, chat)
	set, _ := s.DailyMixSettings(ctx)
	set.MixCount = 2
	_ = s.SaveDailyMixSettings(ctx, set)
	status, _, mixes, err := svc.Today(ctx, 30*time.Second)
	if err != nil || status != StatusReady || len(mixes) != 2 {
		t.Fatalf("today = %s %d %v", status, len(mixes), err)
	}
	if chat.calls != 1 {
		t.Fatalf("calls = %d", chat.calls)
	}
	llm := 0
	for _, m := range mixes {
		if m.TitleSource == "llm" && strings.HasPrefix(m.Title, "标题") {
			llm++
		}
	}
	if llm != 2 || !svc.Snapshot(ctx).LastRun.LLMUsed {
		t.Fatalf("model titles: %d %+v", llm, svc.Snapshot(ctx).LastRun)
	}
	rows, _ := s.AIUsageTotals(ctx, "")
	if len(rows) != 1 || rows[0].Purpose != "mix" {
		t.Fatalf("usage = %+v", rows)
	}

	// An empty library is not ready, and is not retried on every request.
	empty, err := storage.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	_ = empty.Migrate(ctx)
	svc2 := newTestService(empty, c, chat)
	if status, _, _, _ := svc2.Today(ctx, 10*time.Second); status != StatusNotReady {
		t.Fatalf("empty = %s", status)
	}
	if snap := svc2.Snapshot(ctx); snap.LastRun == nil || snap.LastRun.Error != NotReadyText {
		t.Fatalf("snapshot = %+v", snap.LastRun)
	}
	if status, _, _, _ := svc2.Today(ctx, 10*time.Second); status != StatusNotReady {
		t.Fatal("cooling down")
	}
	set.Enabled = false
	_ = empty.SaveDailyMixSettings(ctx, set)
	if status, _, _, _ := svc2.Today(ctx, time.Second); status != StatusDisabled {
		t.Fatal("disabled")
	}
}

// dynamicChat picks the required number of ids from the candidates it is given.
type dynamicChat struct{ calls int }

func (d *dynamicChat) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	d.calls++
	var in plannerInput
	if err := json.Unmarshal([]byte(req.User), &in); err != nil {
		return tagging.ChatResult{}, err
	}
	var mixes []map[string]string
	core, total := in.Need["core"], in.Need["total"]
	moods := map[string]bool{}
	for _, c := range in.Candidates {
		if len(mixes) >= total || (c.Core && core == 0) || (!c.Core && total-len(mixes) <= core) {
			continue
		}
		if moods[c.Label] {
			continue
		}
		moods[c.Label] = true
		if c.Core {
			core--
		}
		mixes = append(mixes, map[string]string{"id": c.ID, "title": fmt.Sprintf("标题%d", len(mixes)+1), "description": "简介"})
	}
	raw, _ := json.Marshal(map[string]any{"mixes": mixes})
	return tagging.ChatResult{Content: string(raw), Usage: storage.AIUsage{Requests: 1, PromptTokens: 1200, CompletionTokens: 150}}, nil
}
