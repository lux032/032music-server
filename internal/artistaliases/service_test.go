package artistaliases

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

type fakeWeb struct {
	searches, fetches int
	body              string
}

func (f *fakeWeb) Search(context.Context, string) ([]SearchResult, error) {
	f.searches++
	return []SearchResult{{URL: "https://example.test/artist", Title: "歌手"}, {URL: "https://example.test/artist", Title: "重复"}, {URL: "https://example.test/other", Title: "其他"}, {URL: "https://example.test/last", Title: "更多"}}, nil
}
func (f *fakeWeb) Fetch(context.Context, string) (string, error) { f.fetches++; return f.body, nil }

type fakeChat struct {
	calls   int
	content string
	err     error
}

func (f *fakeChat) Complete(context.Context, tagging.ChatRequest) (tagging.ChatResult, error) {
	f.calls++
	return tagging.ChatResult{Content: f.content, Usage: storage.AIUsage{Requests: 1, PromptTokens: 10}}, f.err
}
func testService(t *testing.T, sources storage.ArtistAliasSourceData, w *fakeWeb, c *fakeChat) (*Service, *storage.Store, int64) {
	t.Helper()
	ctx := context.Background()
	s, e := storage.Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	s.EnsureLibrary(ctx, "Music", "/music")
	lib, _ := s.LibraryByRoot(ctx, "/music")
	if e = s.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "a.flac", FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"星街すいせい"}, AlbumArtists: []string{"星街すいせい"}, DiscNumber: 1, TrackNumber: 1}}); e != nil {
		t.Fatal(e)
	}
	artists, e := s.ListArtists(ctx, storage.Filters{})
	if e != nil || len(artists) != 1 {
		t.Fatal(artists, e)
	}
	id := artists[0].ID
	if e = s.UpsertExternalArtistProfile(ctx, id, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mb-test", DisplayName: "星街すいせい"}); e != nil {
		t.Fatal(e)
	}
	st, _ := s.ArtistAliasSettings(ctx)
	st.Enabled = true
	st.DeepSeekKey = "web-secret"
	s.SaveArtistAliasSettings(ctx, st)
	ai := storage.DefaultAISettings()
	ai.BaseURL = "https://model.test/v1"
	ai.APIKey = "model-secret"
	ai.Model = "test"
	s.SaveAISettings(ctx, ai)
	svc, e := New(s, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Sources: func(context.Context, string) (storage.ArtistAliasSourceData, error) { return sources, nil }, Web: func(storage.ArtistAliasSettings, storage.AISettings) (Web, error) { return w, nil }, NewChat: func(storage.AISettings) (tagging.Chat, error) { return c, nil }})
	if e != nil {
		t.Fatal(e)
	}
	return svc, s, id
}
func TestBoundChineseSourcesConvertAndAvoidModel(t *testing.T) {
	w, c := &fakeWeb{}, &fakeChat{}
	svc, s, id := testService(t, storage.ArtistAliasSourceData{Names: []storage.ArtistAliasSourceName{{Value: "星街彗星", Language: "zh", Source: "musicbrainz", URL: "https://musicbrainz.org/artist/mb-test"}, {Value: "東山奈央", Language: "ja", Source: "musicbrainz"}}}, w, c)
	did, e := svc.Step(context.Background())
	if e != nil || !did {
		t.Fatal(did, e)
	}
	aliases, e := s.ArtistSearchAliases(context.Background(), id)
	if e != nil || len(aliases) != 1 || aliases[0].Value != "星街彗星" {
		t.Fatal(aliases, e)
	}
	if w.searches != 0 || w.fetches != 0 || c.calls != 0 {
		t.Fatal(w, c)
	}
	svc.Step(context.Background())
	jobs, _ := s.ArtistAliasJobs(context.Background(), 10)
	if len(jobs) != 1 {
		t.Fatal(jobs)
	}
	converted, e := svc.converter.Convert("東山奈央")
	if e != nil || converted != "东山奈央" {
		t.Fatal(converted, e)
	}
}
func TestUntrustedWebNameRequiresReviewAndBoundedCalls(t *testing.T) {
	w := &fakeWeb{body: "星街すいせい，中文名星街彗星，是歌手。"}
	c := &fakeChat{content: `{"sameArtist":true,"aliases":[{"original":"星街彗星","language":"zh","page":0,"quote":"星街すいせい，中文名星街彗星，是歌手。","explicitChinese":true}]}`}
	svc, s, id := testService(t, storage.ArtistAliasSourceData{IdentityNames: []string{"星街すいせい"}}, w, c)
	if _, e := svc.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	aliases, _ := s.ArtistSearchAliases(context.Background(), id)
	if len(aliases) != 0 {
		t.Fatal(aliases)
	}
	pending, e := s.ArtistAliasCandidates(context.Background(), "pending", 0, 10)
	if e != nil || len(pending) != 1 {
		t.Fatal(pending, e)
	}
	if w.searches > 2 || w.fetches > 3 || c.calls > 2 {
		t.Fatal(w, c)
	}
	if e = s.ReviewArtistAlias(context.Background(), pending[0].ID, true, ""); e != nil {
		t.Fatal(e)
	}
	aliases, _ = s.ArtistSearchAliases(context.Background(), id)
	if len(aliases) != 1 {
		t.Fatal(aliases)
	}
	usage, e := s.AIUsageTotals(context.Background(), "")
	if e != nil || len(usage) != 1 || usage[0].Purpose != "artist_alias" {
		t.Fatal(usage, e)
	}
}
func TestOfficialNameAppliesButInventedQuoteDoesNot(t *testing.T) {
	for _, quote := range []string{"星街すいせい，中文名星街彗星。", "不存在的中文名星街彗星"} {
		t.Run(quote, func(t *testing.T) {
			w := &fakeWeb{body: "星街すいせい，中文名星街彗星。"}
			c := &fakeChat{content: `{"sameArtist":true,"aliases":[{"original":"星街彗星","language":"zh","page":0,"quote":"` + quote + `","explicitChinese":true}]}`}
			svc, s, id := testService(t, storage.ArtistAliasSourceData{OfficialURLs: []string{"https://example.test/artist"}, IdentityNames: []string{"星街すいせい"}}, w, c)
			if _, e := svc.Step(context.Background()); e != nil {
				t.Fatal(e)
			}
			aliases, _ := s.ArtistSearchAliases(context.Background(), id)
			if quote == "不存在的中文名星街彗星" && len(aliases) != 0 {
				t.Fatal(aliases)
			}
			if quote != "不存在的中文名星街彗星" && len(aliases) != 1 {
				t.Fatal(aliases)
			}
		})
	}
}
func TestMissingResultNotAutomaticallyRequeued(t *testing.T) {
	svc, s, _ := testService(t, storage.ArtistAliasSourceData{}, &fakeWeb{}, &fakeChat{})
	svc.Step(context.Background())
	svc.Step(context.Background())
	jobs, _ := s.ArtistAliasJobs(context.Background(), 10)
	if len(jobs) != 1 {
		t.Fatal(jobs)
	}
	n, e := svc.Backfill(context.Background())
	if e != nil || n != 1 {
		t.Fatal(n, e)
	}
	jobs, _ = s.ArtistAliasJobs(context.Background(), 10)
	if len(jobs) != 2 {
		t.Fatal(jobs)
	}
}
func TestInterruptedBudgetSurvivesRecovery(t *testing.T) {
	svc, s, _ := testService(t, storage.ArtistAliasSourceData{}, &fakeWeb{}, &fakeChat{})
	list, _ := s.ArtistAliasSubjects(context.Background(), true)
	s.EnqueueArtistAlias(context.Background(), list[0], "auto")
	j, _ := s.ClaimArtistAliasJob(context.Background())
	s.ReserveArtistAliasCall(context.Background(), j, "model", 2)
	s.ReserveArtistAliasCall(context.Background(), j, "model", 2)
	s.RecoverArtistAliasJobs(context.Background())
	j, _ = s.ClaimArtistAliasJob(context.Background())
	if e := s.ReserveArtistAliasCall(context.Background(), j, "model", 2); !errors.Is(e, storage.ErrAliasBudget) {
		t.Fatal(e)
	}
	_ = svc
}

func TestAutomaticCompletionWaitsForIdentityAndNeverRequeuesStock(t *testing.T) {
	svc, s, id := testService(t, storage.ArtistAliasSourceData{}, &fakeWeb{}, &fakeChat{})
	ctx := context.Background()
	// Remove identity before the first poll. The new-artist observation remains,
	// so confirming later is enough; no manual backfill is required for new rows.
	if e := s.ResetArtistIdentity(ctx, id, "musicbrainz", "mb-test"); e != nil {
		t.Fatal(e)
	}
	if did, e := svc.Step(ctx); e != nil || did {
		t.Fatal(did, e)
	}
	jobs, _ := s.ArtistAliasJobs(ctx, 10)
	if len(jobs) != 0 {
		t.Fatal(jobs)
	}
	if e := s.UpsertExternalArtistProfile(ctx, id, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "mb-test", DisplayName: "Singer"}); e != nil {
		t.Fatal(e)
	}
	if did, e := svc.Step(ctx); e != nil || !did {
		t.Fatal(did, e)
	}
	// A completed missing result stays terminal across later polls and refreshes.
	if did, e := svc.Step(ctx); e != nil || did {
		t.Fatal(did, e)
	}
	jobs, _ = s.ArtistAliasJobs(ctx, 10)
	if len(jobs) != 1 {
		t.Fatal(jobs)
	}
}

func TestDifferentChineseSourceNamesRequireReview(t *testing.T) {
	w, c := &fakeWeb{}, &fakeChat{}
	svc, s, id := testService(t, storage.ArtistAliasSourceData{Names: []storage.ArtistAliasSourceName{
		{Value: "中文名称甲", Language: "zh", Source: "musicbrainz", URL: "https://musicbrainz.org/artist/a"},
		{Value: "中文名称乙", Language: "zh-hans", Source: "wikidata", URL: "https://www.wikidata.org/wiki/Q1"},
	}}, w, c)
	if _, e := svc.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	a, _ := s.ArtistSearchAliases(context.Background(), id)
	if len(a) != 0 {
		t.Fatal(a)
	}
	pending, e := s.ArtistAliasCandidates(context.Background(), "pending", 0, 10)
	if e != nil || len(pending) != 2 {
		t.Fatal(pending, e)
	}
	if c.calls != 0 || w.searches != 0 {
		t.Fatal(c, w)
	}
}
func TestSourceCooldownKeepsQueueAndStopsHotLoop(t *testing.T) {
	svc, s, _ := testService(t, storage.ArtistAliasSourceData{}, &fakeWeb{}, &fakeChat{})
	svc.opts.Sources = func(context.Context, string) (storage.ArtistAliasSourceData, error) {
		return storage.ArtistAliasSourceData{}, &storage.AliasSourceRetry{}
	}
	if _, e := svc.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	jobs, _ := s.ArtistAliasJobs(context.Background(), 10)
	if len(jobs) != 1 || jobs[0].Status != "queued" {
		t.Fatal(jobs)
	}
	if did, e := svc.Step(context.Background()); e != nil || did {
		t.Fatal(did, e)
	}
}

type fallbackChat struct{ models []string }

func (c *fallbackChat) Complete(_ context.Context, r tagging.ChatRequest) (tagging.ChatResult, error) {
	c.models = append(c.models, r.Model)
	if r.Model == "test" {
		return tagging.ChatResult{}, &tagging.LLMError{Kind: tagging.KindNetwork}
	}
	return tagging.ChatResult{Content: `{"sameArtist":true,"aliases":[]}`}, nil
}
func TestAliasFallbackUsesSharedModelAndOneArtistBudget(t *testing.T) {
	svc, s, _ := testService(t, storage.ArtistAliasSourceData{}, &fakeWeb{body: "歌手正文"}, &fakeChat{})
	ctx := context.Background()
	ai, _ := s.AISettings(ctx)
	ai.FallbackModel = "backup"
	s.SaveAISettings(ctx, ai)
	st, _ := s.ArtistAliasSettings(ctx)
	st.RPM = 60
	s.SaveArtistAliasSettings(ctx, st)
	chat := &fallbackChat{}
	svc.opts.NewChat = func(storage.AISettings) (tagging.Chat, error) { return chat, nil }
	if _, e := svc.Step(ctx); e != nil {
		t.Fatal(e)
	}
	if len(chat.models) != 2 || chat.models[0] != "test" || chat.models[1] != "backup" {
		t.Fatal(chat.models)
	}
	jobs, _ := s.ArtistAliasJobs(ctx, 10)
	if jobs[0].ModelCalls != 2 {
		t.Fatal(jobs)
	}
}

func TestPauseInterruptsWorkerAndKeepsDurableQueue(t *testing.T) {
	svc, s, _ := testService(t, storage.ArtistAliasSourceData{}, &fakeWeb{}, &fakeChat{})
	started := make(chan struct{})
	svc.opts.Sources = func(ctx context.Context, _ string) (storage.ArtistAliasSourceData, error) {
		close(started)
		<-ctx.Done()
		return storage.ArtistAliasSourceData{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc.Start(ctx)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		svc.Wait()
		t.Fatal("worker did not start")
	}
	st, _ := s.ArtistAliasSettings(context.Background())
	st.Paused = true
	if e := s.SaveArtistAliasSettings(context.Background(), st); e != nil {
		t.Fatal(e)
	}
	svc.Interrupt()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		jobs, _ := s.ArtistAliasJobs(context.Background(), 10)
		if len(jobs) == 1 && jobs[0].Status == "queued" {
			cancel()
			svc.Wait()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	svc.Wait()
	t.Fatal("paused item was not retained")
}
