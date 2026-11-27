package aireview

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/lux032/032music-server/internal/artistaliases"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

func TestEvalArtistConflictMergeModesAndGuardrails(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.EnsureLibrary(ctx, "main", "/music"); err != nil {
		t.Fatalf("ensure library: %v", err)
	}

	const mbid = "44444444-4444-4444-8444-444444444444"
	importArtist := func(path, name string) int64 {
		t.Helper()
		if err := s.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    1,
			RelativePath: path,
			FileSize:     100,
			ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title:        path,
				Album:        "OST " + path,
				DiscNumber:   1,
				TrackNumber:  1,
				Artists:      []string{name},
				AlbumArtists: []string{name},
			},
		}); err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		all, err := s.ArtistsForMatching(ctx)
		if err != nil {
			t.Fatalf("ArtistsForMatching: %v", err)
		}
		for _, a := range all {
			if a.Name == name {
				return a.ID
			}
		}
		t.Fatalf("artist %s not found", name)
		return 0
	}

	jpID := importArtist("jp.flac", "梶浦由記")
	if err := s.UpsertExternalArtistProfile(ctx, jpID, storage.ExternalArtistProfile{
		Source:      "musicbrainz",
		ExternalID:  mbid,
		DisplayName: "梶浦由記",
		Country:     "JP",
		Aliases:     []string{"Yuki Kajiura"},
	}); err != nil {
		t.Fatalf("seed jp owner profile: %v", err)
	}

	seedConflictCandidate := func(artistID int64) int64 {
		t.Helper()
		if err := s.ReplaceArtistCandidates(ctx, artistID, []storage.ArtistCandidate{{
			ArtistID:    artistID,
			Source:      "musicbrainz",
			ExternalID:  mbid,
			DisplayName: "梶浦由記",
			MBID:        mbid,
			Score:       85,
			Payload:     []byte(`{"aliases":["Yuki Kajiura"]}`),
		}}); err != nil {
			t.Fatalf("seed candidate: %v", err)
		}
		cands, _ := s.ArtistCandidates(ctx, artistID)
		return cands[0].ID
	}

	newSvc := func(respJSON string, web *fakeWeb, onConfirmed *int64) *Service {
		return New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
			NewChat: func(storage.AISettings) (tagging.Chat, error) {
				return fakeChat{fn: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
					return tagging.ChatResult{Content: respJSON}, nil
				}}, nil
			},
			NewWeb: func(storage.ArtistAliasSettings, storage.AISettings) (artistaliases.Web, error) {
				return web, nil
			},
			OnArtistConfirmed: func(_ context.Context, id int64) {
				if onConfirmed != nil {
					*onConfirmed = id
				}
			},
		})
	}

	saveMergeMode := func(mode string, webEnabled bool) {
		t.Helper()
		rev := storage.DefaultAIReviewSettings()
		rev.Enabled = true
		rev.WebSearchEnabled = webEnabled
		rev.ArtistMode = storage.AIReviewModeAuto
		rev.ArtistMergeMode = mode
		if err := s.SaveAIReviewSettings(ctx, rev); err != nil {
			t.Fatalf("SaveAIReviewSettings(%s): %v", mode, err)
		}
	}

	// 1. auto mode + high confidence (>=92) -> auto-merges into jpID and invokes OnArtistConfirmed(jpID).
	a1 := importArtist("a1.flac", "Yuki Kajiura")
	c1 := seedConflictCandidate(a1)
	saveMergeMode(storage.AIReviewModeAuto, false)
	var confirmedTarget int64
	resp1, _ := json.Marshal(map[string]any{"decision": "select", "candidateId": c1, "confidence": 95, "reason": "Yuki Kajiura 即梶浦由記的罗马音，应合并至梶浦由記", "evidence": []string{"官方别名一致"}})
	res1, err := newSvc(string(resp1), &fakeWeb{}, &confirmedTarget).EvaluateTarget(ctx, storage.AITargetArtistMatch, a1, true)
	if err != nil {
		t.Fatalf("eval auto high confidence: %v", err)
	}
	if !res1.Applied || res1.Action != storage.AIActionAutoConfirm || confirmedTarget != jpID {
		t.Fatalf("expected auto-merged into %d, got res=%+v confirmedTarget=%d", jpID, res1, confirmedTarget)
	}

	// 2. auto mode + lower confidence (90 < 92) -> downgrades to recommend_accept with conflictMerge payload, does NOT merge.
	a2 := importArtist("a2.flac", "Y. Kajiura")
	c2 := seedConflictCandidate(a2)
	resp2, _ := json.Marshal(map[string]any{"decision": "select", "candidateId": c2, "confidence": 90, "reason": "疑似同一艺人", "evidence": []string{"缩写匹配"}})
	res2, err := newSvc(string(resp2), &fakeWeb{}, nil).EvaluateTarget(ctx, storage.AITargetArtistMatch, a2, true)
	if err != nil {
		t.Fatalf("eval auto low confidence: %v", err)
	}
	if res2.Applied || res2.Action != storage.AIActionRecommendAccept {
		t.Fatalf("expected recommend_accept without auto-apply when confidence=90, got %+v", res2)
	}
	dec2, ok, _ := s.AIReviewDecisionByTarget(ctx, storage.AITargetArtistMatch, a2)
	if !ok {
		t.Fatalf("expected saved decision for a2")
	}
	var p2 struct {
		ConflictMerge    bool   `json:"conflictMerge"`
		TargetArtistID   int64  `json:"targetArtistId"`
		TargetArtistName string `json:"targetArtistName"`
	}
	_ = json.Unmarshal(dec2.Payload, &p2)
	if !p2.ConflictMerge || p2.TargetArtistID != jpID || p2.TargetArtistName != "梶浦由記" {
		t.Fatalf("expected conflictMerge payload targeting 梶浦由記, got %s", string(dec2.Payload))
	}

	// 3. recommend mode + confidence 98 -> recommends only, never auto-merges.
	saveMergeMode(storage.AIReviewModeRecommend, false)
	resp3, _ := json.Marshal(map[string]any{"decision": "select", "candidateId": c2, "confidence": 98, "reason": "同一艺人", "evidence": []string{"别名"}})
	res3, err := newSvc(string(resp3), &fakeWeb{}, nil).EvaluateTarget(ctx, storage.AITargetArtistMatch, a2, true)
	if err != nil || res3.Applied || res3.Action != storage.AIActionRecommendAccept {
		t.Fatalf("expected recommend_accept in recommend mode, got %+v err=%v", res3, err)
	}

	// 4. off mode -> forces uncertain and does not recommend or merge.
	saveMergeMode(storage.AIReviewModeOff, false)
	res4, err := newSvc(string(resp3), &fakeWeb{}, nil).EvaluateTarget(ctx, storage.AITargetArtistMatch, a2, true)
	if err != nil || res4.Applied || res4.Action != storage.AIActionUncertain {
		t.Fatalf("expected uncertain in off mode, got %+v err=%v", res4, err)
	}

	// 5. Rolled-back merge pair -> forced uncertain even in auto mode with confidence 99.
	ops, _ := s.MergeOperations(ctx)
	if len(ops) == 0 {
		t.Fatalf("expected recorded merge op for a1")
	}
	if err = s.RollbackArtistMerge(ctx, ops[0].ID); err != nil {
		t.Fatalf("rollback a1 merge: %v", err)
	}
	c1After := seedConflictCandidate(a1)
	saveMergeMode(storage.AIReviewModeAuto, false)
	resp5, _ := json.Marshal(map[string]any{"decision": "select", "candidateId": c1After, "confidence": 99, "reason": "同人", "evidence": []string{"别名"}})
	res5, err := newSvc(string(resp5), &fakeWeb{}, nil).EvaluateTarget(ctx, storage.AITargetArtistMatch, a1, true)
	if err != nil || res5.Applied || res5.Action != storage.AIActionUncertain {
		t.Fatalf("expected rolled-back pair forced uncertain, got %+v err=%v", res5, err)
	}

	// 6. Web search with ungrounded quote -> does NOT auto-merge.
	a6 := importArtist("a6.flac", "Kajiura Yuki")
	c6 := seedConflictCandidate(a6)
	saveMergeMode(storage.AIReviewModeAuto, true)
	callNum := 0
	svcWeb := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeChat{fn: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
				callNum++
				if callNum == 1 {
					b, _ := json.Marshal(map[string]any{"decision": "uncertain", "confidence": 60, "needWebSearch": true, "searchQuery": "Kajiura Yuki", "reason": "需要联网确认"})
					return tagging.ChatResult{Content: string(b)}, nil
				}
				b, _ := json.Marshal(map[string]any{"decision": "select", "candidateId": c6, "confidence": 96, "quote": "HALLUCINATED QUOTE NOT IN PAGE", "reason": "同一艺人"})
				return tagging.ChatResult{Content: string(b)}, nil
			}}, nil
		},
		NewWeb: func(storage.ArtistAliasSettings, storage.AISettings) (artistaliases.Web, error) {
			return &fakeWeb{results: []artistaliases.SearchResult{{URL: "https://example.com", Title: "Yuki Kajiura", Content: "Yuki Kajiura is a Japanese composer."}}}, nil
		},
	})
	res6, err := svcWeb.EvaluateTarget(ctx, storage.AITargetArtistMatch, a6, true)
	if err != nil || res6.Applied || res6.Action != storage.AIActionRecommendAccept {
		t.Fatalf("expected ungrounded web quote downgraded to recommend_accept without merge, got %+v err=%v", res6, err)
	}
}
