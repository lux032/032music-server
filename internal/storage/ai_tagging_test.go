package storage

import (
	"testing"
	"time"
)

func taggingIDs(t *testing.T, s *Store, audioActive bool, now time.Time) map[int64]string {
	t.Helper()
	cands, err := s.NextTaggingCandidates(t.Context(), audioActive, 1, 1, now, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]string{}
	for _, c := range cands {
		out[c.TrackID] = c.Stamp
	}
	return out
}

func TestTaggingReadinessFollowsAudioAnalysis(t *testing.T) {
	s, ctx, _ := audioAnalysisFixture(t, 3)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if got := taggingIDs(t, s, false, now); len(got) != 3 {
		t.Fatalf("audio inactive: every track is ready, got %d", len(got))
	}
	if got := taggingIDs(t, s, true, now); len(got) != 0 {
		t.Fatalf("audio active: nothing is ready before analysis, got %d", len(got))
	}
	// Track 0: full-mode features (with arousal) → ready.
	c0, _, _ := s.NextAudioAnalysisCandidate(ctx, testAnalysisVersion, now)
	if err := s.SaveTrackAudioFeatures(ctx, testFeatures(c0, "full")); err != nil {
		t.Fatal(err)
	}
	// Track 1: fast features without arousal while an AV model exists → waits for backfill.
	c1, _, _ := s.NextAudioAnalysisCandidate(ctx, testAnalysisVersion, now)
	if err := s.SaveTrackAudioFeatures(ctx, testFeatures(c1, "fast")); err != nil {
		t.Fatal(err)
	}
	modelID, err := s.SaveAudioAVModel(ctx, AudioAVModel{AnalysisVersion: testAnalysisVersion, Kind: "embedding", Dims: 1280, Weights: make([]byte, 1280*2*4), Samples: 40, MetricsJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	// Track 2: permanent analysis failure → tagged without audio.
	c2, _, _ := s.NextAudioAnalysisCandidate(ctx, testAnalysisVersion, now)
	if err := s.RecordAudioAnalysisFailure(ctx, c2, testAnalysisVersion, "decode_failed", true, now); err != nil {
		t.Fatal(err)
	}
	got := taggingIDs(t, s, true, now)
	if _, ok := got[c1.TrackID]; ok || len(got) != 2 {
		t.Fatalf("ready = %v (c0=%d c1=%d c2=%d)", got, c0.TrackID, c1.TrackID, c2.TrackID)
	}
	st, err := s.TaggingStats(ctx, true, 1, 1, now)
	if err != nil || st.Eligible != 3 || st.WaitingAudio != 1 {
		t.Fatalf("stats = %+v %v", st, err)
	}
	stamp0 := got[c0.TrackID]
	if err := s.ApplyAudioAVRegression(ctx, testAnalysisVersion, modelID, []AudioAVValue{{TrackID: c1.TrackID, Valence: 4, Arousal: 5}}); err != nil {
		t.Fatal(err)
	}
	if got = taggingIDs(t, s, true, now); len(got) != 3 {
		t.Fatalf("after backfill all ready, got %v", got)
	}

	// Tag track 0, then link a work: the stamp changes → candidate again.
	if err := s.SaveTrackTagResult(ctx, TrackTagMeta{TrackID: c0.TrackID, SourceStamp: stamp0, InputHash: "h", VocabVersion: 1, PromptVersion: 1, Model: "m"},
		[]TrackTag{{Kind: "mood", Tag: "epic", Weight: 1, Confidence: 0.8, Source: "llm"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := taggingIDs(t, s, true, now)[c0.TrackID]; ok {
		t.Fatal("tagged track must not be a candidate")
	}
	work, err := s.CreateWork(ctx, WorkInput{Title: "作品", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddWorkTrack(ctx, work.ID, WorkTrackInput{TrackID: c0.TrackID, Role: "op"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := taggingIDs(t, s, true, now)[c0.TrackID]; !ok {
		t.Fatal("linking a work must re-queue the track")
	}
	info, err := s.TaggingTrackInfo(ctx, c0.TrackID)
	if err != nil || len(info.Works) != 1 || info.Works[0].Role != "op" || info.Works[0].Type != "anime" {
		t.Fatalf("info = %+v %v", info, err)
	}
	// Versions bump → everything is a candidate again.
	cands, _ := s.NextTaggingCandidates(ctx, true, 2, 1, now, nil, 100)
	if len(cands) != 3 {
		t.Fatalf("vocab bump: %d", len(cands))
	}
	// Exclusion list.
	cands, _ = s.NextTaggingCandidates(ctx, true, 2, 1, now, []int64{c0.TrackID, c1.TrackID}, 100)
	if len(cands) != 1 || cands[0].TrackID != c2.TrackID {
		t.Fatalf("exclude: %+v", cands)
	}
}

func TestTaggingFailuresAndUserTags(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 1)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	stamp := taggingIDs(t, s, false, now)[ids[0]]
	for i := 1; i <= 3; i++ {
		attempts, permanent, err := s.RecordTaggingFailure(ctx, ids[0], stamp, "bad", now, 3)
		if err != nil || attempts != i || permanent != (i == 3) {
			t.Fatalf("attempt %d: %d %v %v", i, attempts, permanent, err)
		}
	}
	if len(taggingIDs(t, s, false, now.Add(48*time.Hour))) != 0 {
		t.Fatal("permanent failure must block the same stamp")
	}
	if attempts, _, _ := s.RecordTaggingFailure(ctx, ids[0], "other-stamp", "bad", now, 3); attempts != 1 {
		t.Fatal("a new stamp restarts the attempt count")
	}
	if err := s.SetUserTrackTags(ctx, ids[0], "genre", nil); err == nil {
		t.Fatal("invalid kind")
	}
	if err := s.SetUserTrackTags(ctx, 999999, "mood", nil); err == nil {
		t.Fatal("missing track")
	}
	if err := s.SetUserTrackTags(ctx, ids[0], "style", []TrackTag{{Tag: "rock", Weight: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTrackTagResult(ctx, TrackTagMeta{TrackID: ids[0], SourceStamp: stamp, InputHash: "h", VocabVersion: 1, PromptVersion: 1, Model: "m"},
		[]TrackTag{{Kind: "style", Tag: "pop", Weight: 1, Confidence: 0.9, Source: "llm"}, {Kind: "mood", Tag: "epic", Weight: 1, Confidence: 0.9, Source: "llm"}},
		[]TrackTag{{Kind: "style", Tag: "pop", Weight: 0.6, Confidence: 0.6, Source: "audio"}}); err != nil {
		t.Fatal(err)
	}
	tags, _ := s.TrackTags(ctx, ids[0])
	eff := EffectiveTags(tags)
	if len(tags) != 4 || len(eff) != 2 || eff[0].Source != "user" || eff[0].Tag != "rock" || eff[1].Tag != "epic" {
		t.Fatalf("tags = %+v eff = %+v", tags, eff)
	}
	if fails, _ := s.TaggingFailures(ctx, 10); len(fails) != 0 {
		t.Fatal("a saved result clears failures")
	}
	if err := s.AddAIUsage(ctx, now, "m", "tag", AIUsage{Requests: 1, PromptTokens: 2000, CompletionTokens: 100}); err != nil {
		t.Fatal(err)
	}
	_ = s.AddAIUsage(ctx, now, "m", "tag", AIUsage{Requests: 1, PromptTokens: 1000})
	rows, _ := s.AIUsageTotals(ctx, "")
	if len(rows) != 1 || rows[0].Usage.Requests != 2 || rows[0].Usage.PromptTokens != 3000 {
		t.Fatalf("usage = %+v", rows)
	}
	if FormatTokenCount(45_000_000) != "4500.0 万" || FormatTokenCount(350_000_000) != "3.50 亿" || FormatTokenCount(999) != "999" {
		t.Fatal("token formatting")
	}
}
