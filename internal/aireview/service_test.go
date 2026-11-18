package aireview

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/aiclient"
	"github.com/lux032/032music-server/internal/artistaliases"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

type fakeChat struct {
	fn func(tagging.ChatRequest) (tagging.ChatResult, error)
}

func (f fakeChat) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	return f.fn(req)
}

type fakeWeb struct {
	results []artistaliases.SearchResult
	calls   int
}

func (w *fakeWeb) Search(context.Context, string) ([]artistaliases.SearchResult, error) {
	w.calls++
	return w.results, nil
}

func (w *fakeWeb) Fetch(context.Context, string) (string, error) { return "", nil }

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	ctx := context.Background()
	s, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAISettings(ctx, storage.AISettings{
		BaseURL:           "https://api.example.test/v1",
		APIKey:            "sk-test",
		Model:             "lite",
		ReviewModel:       "pro",
		RequestsPerMinute: 6000,
		Concurrency:       2,
	}); err != nil {
		t.Fatal(err)
	}
	rev := storage.DefaultAIReviewSettings()
	rev.Enabled = true
	if err = s.SaveAIReviewSettings(ctx, rev); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAlbumAutoConfirmAndRevert(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	s.EnsureLibrary(ctx, "Music", "/music")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if err := s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "Amore", Album: "Amore (初回盤)", Artists: []string{"ReoNa"}, AlbumArtists: []string{"ReoNa"}, DiscNumber: 1, TrackNumber: 1},
	}); err != nil {
		t.Fatal(err)
	}
	albums, err := s.ListAlbums(ctx, storage.Filters{})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums=%v err=%v", albums, err)
	}
	albumID := albums[0].ID
	if err := s.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{
		{
			ExternalID: "1001", Title: "Amore", Artist: "ReoNa", Score: 75,
			Tieups: []storage.BangumiTieup{{SubjectID: 9001, Title: "きみが死ぬまで恋をしたい", Type: "anime", Role: "op"}},
		},
		{
			ExternalID: "1002", Title: "Amore (通常盤)", Artist: "ReoNa", Score: 75,
			Tieups: []storage.BangumiTieup{{SubjectID: 9001, Title: "きみが死ぬまで恋をしたい", Type: "anime", Role: "op"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	cands, err := s.AlbumSubjectCandidates(ctx, albumID)
	if err != nil || len(cands) != 2 {
		t.Fatalf("cands=%v err=%v", cands, err)
	}

	svc := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Limiter: aiclient.NewLimiter(6000),
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeChat{fn: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
				if req.Model != "pro" {
					t.Fatalf("expected review model 'pro', got %q", req.Model)
				}
				return tagging.ChatResult{
					Content: `{"decision":"select","candidateId":` + itoa(cands[0].ID) + `,"workSubjectIds":[9001],"confidence":94,"needWebSearch":false,"searchQuery":"","quote":"","reason":"两版单曲同指动画 OP","evidence":["标题与歌手吻合"]}`,
					Usage:   storage.AIUsage{Requests: 1, PromptTokens: 200, CompletionTokens: 40},
				}, nil
			}}, nil
		},
	})

	res, err := svc.EvaluateTarget(ctx, storage.AITargetAlbumSubject, albumID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || res.Action != storage.AIActionAutoConfirm {
		t.Fatalf("expected auto confirm, got %+v", res)
	}
	works, err := s.WorksForAlbum(ctx, albumID)
	if err != nil || len(works) != 1 {
		t.Fatalf("expected 1 linked work, got %+v err=%v", works, err)
	}

	// Revert should remove AI-added link and restore candidate to pending.
	dec, ok, err := s.AIReviewDecisionByTarget(ctx, storage.AITargetAlbumSubject, albumID)
	if err != nil || !ok {
		t.Fatalf("missing decision ok=%v err=%v", ok, err)
	}
	if err := s.RevertAIReviewDecision(ctx, dec.ID); err != nil {
		t.Fatal(err)
	}
	works, _ = s.WorksForAlbum(ctx, albumID)
	if len(works) != 0 {
		t.Fatalf("expected 0 works after revert, got %+v", works)
	}
	candsAfter, _ := s.AlbumSubjectCandidates(ctx, albumID)
	for _, c := range candsAfter {
		if c.Status != "candidate" {
			t.Fatalf("expected restored candidate status, got %s", c.Status)
		}
	}
}

func TestTrackMultiTitleWebGroundedAndSourcePreserved(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	rev, _ := s.AIReviewSettings(ctx)
	rev.WebSearchEnabled = true
	if err := s.SaveAIReviewSettings(ctx, rev); err != nil {
		t.Fatal(err)
	}

	s.EnsureLibrary(ctx, "Music", "/music")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if err := s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "残響散歌", Album: "残響散歌 / 朝が来る", Artists: []string{"Aimer"}, AlbumArtists: []string{"Aimer"}, DiscNumber: 1, TrackNumber: 1},
	}); err != nil {
		t.Fatal(err)
	}
	tracks, _ := s.ListTracks(ctx, storage.Filters{})
	trackID := tracks[0].ID
	if err := s.SaveTrackSubjectCandidates(ctx, trackID, []storage.TrackSubjectCandidate{{
		ExternalID: "2001", Title: "残響散歌 / 朝が来る", Artist: "Aimer", MatchKind: "multi_title",
		Tieups: []storage.BangumiTieup{{SubjectID: 8001, Title: "鬼滅の刃 遊郭編", Type: "anime", Role: "other"}},
	}}); err != nil {
		t.Fatal(err)
	}
	cands, _ := s.TrackSubjectCandidates(ctx, trackID)

	calls := 0
	web := &fakeWeb{results: []artistaliases.SearchResult{{
		URL:     "https://example.test/zankyo",
		Title:   "鬼滅の刃 遊郭編",
		Content: "Aimer「残響散歌」はテレビアニメ『鬼滅の刃 遊郭編』オープニングテーマ。",
	}}}
	svc := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Limiter: aiclient.NewLimiter(6000),
		NewWeb: func(storage.ArtistAliasSettings, storage.AISettings) (artistaliases.Web, error) {
			return web, nil
		},
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeChat{fn: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
				calls++
				if calls == 1 {
					return tagging.ChatResult{
						Content: `{"decision":"uncertain","candidateId":0,"links":[],"confidence":45,"needWebSearch":true,"searchQuery":"残響散歌 Aimer OP","quote":"","reason":"需要核对具体是 OP 还是 ED","evidence":[]}`,
						Usage:   storage.AIUsage{Requests: 1},
					}, nil
				}
				return tagging.ChatResult{
					Content: `{"decision":"select","candidateId":` + itoa(cands[0].ID) + `,"links":[{"workSubjectId":8001,"role":"op"}],"confidence":96,"needWebSearch":false,"searchQuery":"","quote":"Aimer「残響散歌」はテレビアニメ『鬼滅の刃 遊郭編』オープニングテーマ。","reason":"网页明确指出残響散歌为游郭篇 OP","evidence":["オープニングテーマ"]}`,
					Usage:   storage.AIUsage{Requests: 1},
				}, nil
			}}, nil
		},
	})

	res, err := svc.EvaluateTarget(ctx, storage.AITargetTrackSubject, trackID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || !res.WebSearched || web.calls != 1 {
		t.Fatalf("expected web-grounded auto confirm, got %+v webCalls=%d", res, web.calls)
	}
	role, _, _, err := s.TrackBangumiLink(ctx, trackID)
	if err != nil || role != "op" {
		t.Fatalf("expected bangumi role=op (not manual), got role=%q err=%v", role, err)
	}
}

func TestWorkConflictAndArtistCreditSplitGuards(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	// 1. Work already bound elsewhere cannot be auto-confirmed by AI.
	boundWork, _ := s.CreateWork(ctx, storage.WorkInput{Title: "Existing Work", Type: "anime"})
	s.ConfirmWorkMatchCandidateByAI(ctx, boundWork.ID, func() int64 {
		_ = s.ReplaceWorkMatchCandidates(ctx, boundWork.ID, []storage.WorkMatchCandidate{{
			WorkID: boundWork.ID, Source: "bangumi", ExternalID: "555", Title: "Existing Work", Type: "anime", Score: 95,
		}})
		list, _ := s.WorkMatchCandidates(ctx, boundWork.ID)
		return list[0].ID
	}())

	otherWork, _ := s.CreateWork(ctx, storage.WorkInput{Title: "Duplicate Alias", Type: "anime"})
	_ = s.ReplaceWorkMatchCandidates(ctx, otherWork.ID, []storage.WorkMatchCandidate{{
		WorkID: otherWork.ID, Source: "bangumi", ExternalID: "555", Title: "Existing Work", Type: "anime", Score: 80,
	}})
	otherCands, _ := s.WorkMatchCandidates(ctx, otherWork.ID)

	// 2. Composite credit artist + non-composite unit.
	s.EnsureLibrary(ctx, "Music", "/music")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	_ = s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "B/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "Duet", Album: "Collab", Artists: []string{"Aimer feat. Sawano"}, AlbumArtists: []string{"Aimer"}, DiscNumber: 1, TrackNumber: 1},
	})
	artists, _ := s.ListArtists(ctx, storage.Filters{})
	var compositeID int64
	for _, a := range artists {
		if a.Name == "Aimer feat. Sawano" {
			compositeID = a.ID
		}
	}

	svc := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Limiter: aiclient.NewLimiter(6000),
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeChat{fn: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
				if req.Schema != nil && len(req.Schema) > 0 && string(req.Schema) == string(creditSplitSchema) {
					return tagging.ChatResult{
						Content: `{"isComposite":true,"parts":["Aimer","Sawano"],"confidence":95,"reason":"包含 feat. 合作署名"}`,
						Usage:   storage.AIUsage{Requests: 1},
					}, nil
				}
				return tagging.ChatResult{
					Content: `{"decision":"select","candidateId":` + itoa(otherCands[0].ID) + `,"confidence":99,"needWebSearch":false,"searchQuery":"","quote":"","reason":"同一部作品","evidence":[]}`,
					Usage:   storage.AIUsage{Requests: 1},
				}, nil
			}}, nil
		},
	})

	workRes, err := svc.EvaluateTarget(ctx, storage.AITargetWorkMatch, otherWork.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if workRes.Applied || workRes.Action != storage.AIActionUncertain {
		t.Fatalf("conflicting ownerWorkId must force uncertain, got %+v", workRes)
	}

	creditRes, err := svc.EvaluateTarget(ctx, storage.AITargetCreditSplit, compositeID, false)
	if err != nil {
		t.Fatal(err)
	}
	if creditRes.Applied || creditRes.Action != storage.AIActionRecommendAccept {
		t.Fatalf("expected recommend_accept without auto apply, got %+v", creditRes)
	}

	// Applying the split updates the track primary artists cleanly.
	updated, err := s.ApplyCompositeCreditSplit(ctx, compositeID, []string{"Aimer", "Sawano"})
	if err != nil || updated != 1 {
		t.Fatalf("expected 1 updated track, got %d err=%v", updated, err)
	}
}

func TestAIReviewBacklogRun(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	w1, _ := s.CreateWork(ctx, storage.WorkInput{Title: "轻音少女", Type: "anime"})
	_ = s.ReplaceWorkMatchCandidates(ctx, w1.ID, []storage.WorkMatchCandidate{{
		WorkID: w1.ID, Source: "bangumi", ExternalID: "1424", Title: "けいおん！", TranslatedTitle: "轻音少女", Type: "anime", Score: 70,
	}})
	cands, _ := s.WorkMatchCandidates(ctx, w1.ID)

	svc := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Limiter: aiclient.NewLimiter(6000),
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeChat{fn: func(tagging.ChatRequest) (tagging.ChatResult, error) {
				return tagging.ChatResult{
					Content: `{"decision":"select","candidateId":` + itoa(cands[0].ID) + `,"confidence":97,"needWebSearch":false,"searchQuery":"","quote":"","reason":"中文译名精确对应","evidence":[]}`,
					Usage:   storage.AIUsage{Requests: 1},
				}, nil
			}}, nil
		},
	})

	run, err := svc.StartRun(ctx, "works_all", false)
	if err != nil {
		t.Fatal(err)
	}
	if run.Total != 1 {
		t.Fatalf("expected total=1, got %d", run.Total)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cur, err := s.AIReviewRunByID(ctx, run.ID)
		if err == nil && cur.Status == "completed" {
			if cur.AutoApplied != 1 {
				t.Fatalf("expected AutoApplied=1, got %+v", cur)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for AI review run completion")
}

func TestArtistConflictAndSeriesSuggestion(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	s.EnsureLibrary(ctx, "Music", "/music")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	_ = s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "C/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "S1", Album: "A1", Artists: []string{"Artist One"}, AlbumArtists: []string{"Artist One"}, DiscNumber: 1, TrackNumber: 1},
	})
	_ = s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "C/02.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "S2", Album: "A2", Artists: []string{"Artist Two"}, AlbumArtists: []string{"Artist Two"}, DiscNumber: 1, TrackNumber: 1},
	})
	artists, _ := s.ListArtists(ctx, storage.Filters{})
	var a1, a2 int64
	for _, a := range artists {
		if a.Name == "Artist One" {
			a1 = a.ID
		} else if a.Name == "Artist Two" {
			a2 = a.ID
		}
	}
	_ = s.ReplaceArtistCandidatesForSources(ctx, a1, []storage.ArtistCandidate{{
		Source: "musicbrainz", ExternalID: "mb-shared", DisplayName: "Artist", Score: 95,
	}}, []string{"musicbrainz"})
	c1, _ := s.ArtistCandidates(ctx, a1)
	if bound, _, err := s.ConfirmArtistCandidateByAI(ctx, a1, c1[0].ID); err != nil || !bound {
		t.Fatalf("expected initial artist bind, bound=%v err=%v", bound, err)
	}

	_ = s.ReplaceArtistCandidatesForSources(ctx, a2, []storage.ArtistCandidate{{
		Source: "musicbrainz", ExternalID: "mb-shared", DisplayName: "Artist", Score: 85,
	}}, []string{"musicbrainz"})
	c2, _ := s.ArtistCandidates(ctx, a2)

	wa, _ := s.CreateWork(ctx, storage.WorkInput{Title: "Work A", Type: "game", Year: 2020})
	wb, _ := s.CreateWork(ctx, storage.WorkInput{Title: "Work A Anime", Type: "anime", Year: 2022})
	_ = s.ReplaceSeriesSuggestions(ctx, 0, []storage.SeriesSuggestionInput{{
		WorkA: wa.ID, WorkB: wb.ID, SubjectA: 101, SubjectB: 202, RelationAB: "动画", RelationBA: "游戏", Kind: "cross",
	}}, []int64{wa.ID, wb.ID})
	suggIDs, _ := s.PendingSeriesSuggestionIDs(ctx)

	svc := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Limiter: aiclient.NewLimiter(6000),
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeChat{fn: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
				if string(req.Schema) == string(seriesReviewSchema) {
					return tagging.ChatResult{
						Content: `{"recommendation":"accept","suggestedTitle":"Work A 系列","confidence":95,"reason":"同原作跨媒体改编"}`,
						Usage:   storage.AIUsage{Requests: 1},
					}, nil
				}
				return tagging.ChatResult{
					Content: `{"decision":"select","candidateId":` + itoa(c2[0].ID) + `,"confidence":95,"needWebSearch":false,"searchQuery":"","quote":"","reason":"同名候选","evidence":[]}`,
					Usage:   storage.AIUsage{Requests: 1},
				}, nil
			}}, nil
		},
	})

	// Artist candidate conflicting with another artist's MBID must be forced to uncertain.
	arRes, err := svc.EvaluateTarget(ctx, storage.AITargetArtistMatch, a2, false)
	if err != nil {
		t.Fatal(err)
	}
	if arRes.Applied || arRes.Action != storage.AIActionUncertain {
		t.Fatalf("conflicting artist candidate must not auto-bind, got %+v", arRes)
	}

	// Series suggestion defaults to recommend-only without merging.
	srRes, err := svc.EvaluateTarget(ctx, storage.AITargetSeriesSuggestion, suggIDs[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if srRes.Applied || srRes.Action != storage.AIActionRecommendAccept {
		t.Fatalf("series suggestion default mode should recommend only, got %+v", srRes)
	}

	// Artist with both MusicBrainz and Last.fm candidates: AI confirming MB must
	// clear the artist completely from PendingArtistReviewPage (no leftover Last.fm row).
	_ = s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "D/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "Song C", Album: "Album C", Artists: []string{"Coro"}, AlbumArtists: []string{"Coro"}, DiscNumber: 1, TrackNumber: 1},
	})
	artists2, _ := s.ListArtists(ctx, storage.Filters{})
	var coroID int64
	for _, a := range artists2 {
		if a.Name == "Coro" {
			coroID = a.ID
		}
	}
	_ = s.ReplaceArtistCandidatesForSources(ctx, coroID, []storage.ArtistCandidate{
		{Source: "musicbrainz", ExternalID: "mb-coro", DisplayName: "Coro", Score: 85},
		{Source: "lastfm", ExternalID: "lfm-coro", DisplayName: "Coro Menap", Score: 82},
	}, []string{"musicbrainz", "lastfm"})
	coroCands, _ := s.ArtistCandidates(ctx, coroID)
	var mbCandID int64
	for _, c := range coroCands {
		if c.Source == "musicbrainz" {
			mbCandID = c.ID
		}
	}
	svcCoro := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Limiter: aiclient.NewLimiter(6000),
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeChat{fn: func(tagging.ChatRequest) (tagging.ChatResult, error) {
				return tagging.ChatResult{
					Content: `{"decision":"select","candidateId":` + itoa(mbCandID) + `,"confidence":95,"needWebSearch":false,"searchQuery":"","quote":"","reason":"名称吻合","evidence":[]}`,
					Usage:   storage.AIUsage{Requests: 1},
				}, nil
			}}, nil
		},
	})
	coroRes, err := svcCoro.EvaluateTarget(ctx, storage.AITargetArtistMatch, coroID, false)
	if err != nil || !coroRes.Applied {
		t.Fatalf("expected coro auto confirm, got %+v err=%v", coroRes, err)
	}
	page, err := s.PendingArtistReviewPage(ctx, storage.ArtistReviewFilter{Query: "Coro", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("confirmed artist should disappear from pending review page, got %+v", page.Items)
	}
}

func TestArtistMatchBlocksAutoConfirmOnRequiresManualReview(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.EnsureLibrary(ctx, "Music", "/music")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	_ = s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "a.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"LocalSinger"}, AlbumArtists: []string{"LocalSinger"}, DiscNumber: 1, TrackNumber: 1},
	})
	artists, _ := s.ListArtists(ctx, storage.Filters{})
	artistID := artists[0].ID
	_ = s.ReplaceArtistCandidates(ctx, artistID, []storage.ArtistCandidate{{
		Source: "musicbrainz", ExternalID: "mb-unsafe", MBID: "mb-unsafe",
		DisplayName: "Different Name", Score: 85, RequiresManualReview: true,
		Evidence: []string{"标签身份与歌手名称不一致，需要人工核对"},
	}})
	cands, _ := s.ArtistCandidates(ctx, artistID)
	candID := cands[0].ID

	rev, _ := s.AIReviewSettings(ctx)
	rev.Enabled = true
	rev.ArtistMode = storage.AIReviewModeAuto
	_ = s.SaveAIReviewSettings(ctx, rev)

	svc := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Limiter: aiclient.NewLimiter(6000),
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeChat{fn: func(tagging.ChatRequest) (tagging.ChatResult, error) {
				return tagging.ChatResult{
					Content: `{"decision":"select","candidateId":` + itoa(candID) + `,"confidence":99,"needWebSearch":false,"searchQuery":"","quote":"","reason":"test","evidence":[]}`,
					Usage:   storage.AIUsage{Requests: 1},
				}, nil
			}}, nil
		},
	})
	res, err := svc.EvaluateTarget(ctx, storage.AITargetArtistMatch, artistID, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || res.Action != storage.AIActionUncertain {
		t.Fatalf("expected uncertain and not applied, got %+v", res)
	}
}

func itoa(v int64) string {
	return string(func() []byte {
		if v == 0 {
			return []byte{'0'}
		}
		var b [20]byte
		i := len(b)
		for v > 0 {
			i--
			b[i] = byte('0' + v%10)
			v /= 10
		}
		return b[i:]
	}())
}
