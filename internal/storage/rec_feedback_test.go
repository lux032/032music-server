package storage

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestSessionSignal(t *testing.T) {
	cases := []struct {
		reason            string
		pos, initial, dur int64
		counted           bool
		want              float64
		played, quick     bool
	}{
		{"completed", 200000, 0, 200000, true, 1, true, false},
		{"skipped", 170000, 0, 200000, true, 1, true, false},    // ≥80%
		{"skipped", 120000, 0, 200000, true, 0, true, false},    // 50–80%
		{"skipped", 40000, 0, 200000, false, -0.4, true, false}, // 30 s – 50%
		{"skipped", 8000, 0, 200000, false, -1, true, true},     // 秒切
		{"stopped", 150000, 0, 200000, true, 0, true, false},    // leaving is neutral
		{"client_closed", 3000, 0, 200000, false, 0, false, false},
		{"expired", 90000, 60000, 200000, false, 0, true, false},
	}
	for _, c := range cases {
		got, played, quick := SessionSignal(c.reason, c.pos, c.initial, c.dur, c.counted)
		if got != c.want || played != c.played || quick != c.quick {
			t.Errorf("%s %d/%d = %v %v %v, want %v %v %v", c.reason, c.pos, c.dur, got, played, quick, c.want, c.played, c.quick)
		}
	}
}

func TestMixFeedbackTargets(t *testing.T) {
	if a, b := MixFeedbackTargets(MixPlayStats{Played: 10, Length: 25, Satisfaction: 0.6}); math.Abs(a-0.2) > 1e-9 || b != 0 {
		t.Fatalf("engaged = %v %v", a, b)
	}
	if a, _ := MixFeedbackTargets(MixPlayStats{Played: 4, Length: 25, Satisfaction: 1}); a != 0 {
		t.Fatal("fewer than 5 played earns nothing")
	}
	if a, b := MixFeedbackTargets(MixPlayStats{Played: 5, Length: 25, Satisfaction: -0.5, EarlyQuick: 3}); a != 0 || b != 0.7 {
		t.Fatalf("early quick skips = %v %v", a, b)
	}
}

func TestPlaybackContextValidationAndInheritance(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 200000)

	bad := sessionEvent(trackID, 1, "start", "playing", 0, 200000)
	bad.Context = &PlaybackContext{Type: "album", ID: 1}
	if _, err := s.RecordPlaybackEvent(ctx, bad); !errors.Is(err, ErrInvalidPlaybackEvent) {
		t.Fatalf("bad type: %v", err)
	}
	hb := sessionEvent(trackID, 2, "heartbeat", "playing", 0, 200000)
	hb.Context = &PlaybackContext{Type: "dailyMix", ID: 1}
	if _, err := s.RecordPlaybackEvent(ctx, hb); !errors.Is(err, ErrInvalidPlaybackEvent) {
		t.Fatalf("context on heartbeat: %v", err)
	}

	start := sessionEvent(trackID, 1, "start", "playing", 0, 200000)
	start.SessionID = "ctx-root-1"
	start.Context = &PlaybackContext{Type: "dailyMix", ID: 42, Position: 3}
	mustEvent(t, ctx, s, start)
	end := sessionEvent(trackID, 2, "end", "", 60000, 200000)
	end.SessionID, end.EndReason = "ctx-root-1", "client_closed"
	mustEvent(t, ctx, s, end)
	resume := sessionEvent(trackID, 1, "start", "playing", 60000, 200000)
	resume.SessionID, resume.ResumedFromSessionID = "ctx-resume-1", "ctx-root-1"
	mustEvent(t, ctx, s, resume)
	var typ string
	var id, pos int64
	if err := s.db.QueryRowContext(ctx, `SELECT context_type, context_id, context_position FROM playback_sessions WHERE session_id='ctx-resume-1'`).Scan(&typ, &id, &pos); err != nil {
		t.Fatal(err)
	}
	if typ != "dailyMix" || id != 42 || pos != 3 {
		t.Fatalf("inherited context = %s %d %d", typ, id, pos)
	}
}

// seedFeedbackMix stores one active mix of n tracks (200 s each) and returns its id and tracks.
func seedFeedbackMix(t *testing.T, s *Store, n int) (int64, []int64) {
	t.Helper()
	ctx := t.Context()
	var ids []int64
	var items []DailyMixItem
	for i := 0; i < n; i++ {
		id := seedSessionTrack(t, ctx, s, 200000)
		ids = append(ids, id)
		items = append(items, DailyMixItem{Position: i, TrackID: id, Bucket: "familiar", ReasonJSON: "{}"})
		if _, err := s.db.ExecContext(ctx, `INSERT INTO track_tags(track_id, kind, tag, weight, confidence, source) VALUES (?, 'mood', 'cheerful', 1, 0.9, 'llm')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveDailyMixes(ctx, "2026-10-17", []NewDailyMix{{Slot: 0, ThemeType: "mood", ThemeKey: "mood:cheerful", Title: "T", TitleSource: "template", PlanJSON: "{}", GeneratorVersion: 4, Items: items, DurationMs: int64(n) * 200000, CooldownDays: 7}}); err != nil {
		t.Fatal(err)
	}
	mixes, err := s.DailyMixes(ctx, "2026-10-17")
	if err != nil || len(mixes) != 1 {
		t.Fatalf("mixes: %v %d", err, len(mixes))
	}
	return mixes[0].ID, ids
}

func playInMix(t *testing.T, s *Store, session string, mixID, trackID int64, position int, endPos int64, reason string) {
	t.Helper()
	ctx := t.Context()
	start := sessionEvent(trackID, 1, "start", "playing", 0, 200000)
	start.SessionID = session
	start.Context = &PlaybackContext{Type: "dailyMix", ID: mixID, Position: position}
	mustEvent(t, ctx, s, start)
	end := sessionEvent(trackID, 2, "end", "", endPos, 200000)
	end.SessionID, end.EndReason = session, reason
	mustEvent(t, ctx, s, end)
}

func themeAlphaBeta(t *testing.T, s *Store) (float64, float64) {
	t.Helper()
	var a, b float64
	if err := s.db.QueryRowContext(t.Context(), `SELECT alpha, beta FROM daily_mix_theme_stats WHERE theme_type='mood' AND theme_key='mood:cheerful'`).Scan(&a, &b); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestProcessPlaybackFeedback(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	mixID, tracks := seedFeedbackMix(t, s, 10)
	now := time.Now()

	// Six completed plays, one played twice, one favourited while playing.
	for i := 0; i < 6; i++ {
		playInMix(t, s, fmt.Sprintf("fb-complete-%d", i), mixID, tracks[i], i, 200000, "completed")
	}
	playInMix(t, s, "fb-repeat-0", mixID, tracks[0], 0, 200000, "completed")
	if err := s.SetTrackFavorite(ctx, tracks[1], true); err != nil {
		t.Fatal(err)
	}
	// One quick skip, one session that never played (no evidence), one without context.
	playInMix(t, s, "fb-quick-7", mixID, tracks[7], 7, 5000, "skipped")
	playInMix(t, s, "fb-closed-8", mixID, tracks[8], 8, 2000, "client_closed")
	plain := sessionEvent(tracks[9], 1, "start", "playing", 0, 200000)
	plain.SessionID = "fb-plain-9"
	mustEvent(t, ctx, s, plain)

	run, err := s.ProcessPlaybackFeedback(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if run.Sessions != 9 || run.Mixes != 1 {
		t.Fatalf("run = %+v", run)
	}
	again, err := s.ProcessPlaybackFeedback(ctx, now)
	if err != nil || again.Sessions != 0 {
		t.Fatalf("second pass must be a no-op: %+v %v", again, err)
	}

	stats, err := s.DailyMixPlayStats(ctx, []int64{mixID})
	if err != nil {
		t.Fatal(err)
	}
	st := stats[mixID]
	// played: tracks 0–5 and 7 (the closed 2 s session is no evidence).
	if st.Played != 7 || st.Completes != 7 || st.QuickSkips != 1 || st.EarlyQuick != 0 || st.Length != 10 {
		t.Fatalf("stats = %+v", st)
	}
	// signal: 7 × 1 + repeat 0.5 + favourite 2 − 1 = 8.5
	if math.Abs(st.Signal-8.5) > 1e-9 {
		t.Fatalf("signal = %v", st.Signal)
	}
	// α: 0.5 × 7/10 = 0.35 on top of the prior 1.
	if a, b := themeAlphaBeta(t, s); math.Abs(a-1.35) > 1e-9 || b != 1 {
		t.Fatalf("theme = %v %v", a, b)
	}

	aff, err := s.RecAffinities(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(aff.Tracks[tracks[0]]-2.5) > 1e-6 || math.Abs(aff.Tracks[tracks[1]]-3) > 1e-6 || math.Abs(aff.Tracks[tracks[7]]+1) > 1e-6 {
		t.Fatalf("track affinity = %v", aff.Tracks)
	}
	if _, ok := aff.Tracks[tracks[9]]; ok {
		t.Fatal("plays without context never feed the recommendation affinity")
	}
	if math.Abs(aff.Tags["mood:cheerful"]-8.5) > 1e-6 {
		t.Fatalf("tag affinity = %v", aff.Tags)
	}
	// 30-day half-life.
	later, _ := s.RecAffinities(ctx, now.Add(RecAffinityHalfLife))
	if math.Abs(later.Tracks[tracks[0]]-1.25) > 1e-3 {
		t.Fatalf("decayed = %v", later.Tracks[tracks[0]])
	}

	// Three quick skips among the first five turn the mix into a β signal and the α
	// earned earlier is withdrawn once satisfaction drops below 0.5.
	mix2, t2 := seedFeedbackMix2(t, s)
	for i := 0; i < 3; i++ {
		playInMix(t, s, fmt.Sprintf("fb2-quick-%d", i), mix2, t2[i], i, 3000, "skipped")
	}
	if _, err := s.ProcessPlaybackFeedback(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, b := themeAlphaBeta(t, s); math.Abs(b-1.7) > 1e-9 {
		t.Fatalf("beta = %v", b)
	}
}

// seedFeedbackMix2 stores a second mix of the same theme on another day.
func seedFeedbackMix2(t *testing.T, s *Store) (int64, []int64) {
	t.Helper()
	ctx := t.Context()
	var ids []int64
	var items []DailyMixItem
	for i := 0; i < 6; i++ {
		id := seedSessionTrack(t, ctx, s, 200000)
		ids = append(ids, id)
		items = append(items, DailyMixItem{Position: i, TrackID: id, Bucket: "explore", ReasonJSON: "{}"})
	}
	if err := s.SaveDailyMixes(ctx, "2026-10-18", []NewDailyMix{{Slot: 0, ThemeType: "mood", ThemeKey: "mood:cheerful", Title: "T2", TitleSource: "template", PlanJSON: "{}", GeneratorVersion: 4, Items: items, DurationMs: 6 * 200000, CooldownDays: 7}}); err != nil {
		t.Fatal(err)
	}
	mixes, err := s.DailyMixes(ctx, "2026-10-18")
	if err != nil || len(mixes) != 1 {
		t.Fatalf("mixes: %v", err)
	}
	return mixes[0].ID, ids
}
