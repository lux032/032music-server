package tagging

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// ---------------------------------------------------------------- pure functions

func TestParseResultValidation(t *testing.T) {
	ev := &AudioEvidence{StyleHints: []Scored{{"orchestral", 0.82}, {"pop", 0.2}}, Locked: []string{"orchestral"}, Energy: 2}
	r, err := ParseResult("```json\n"+`{"moods":[{"tag":"epic","weight":3,"confidence":0.9},{"tag":"nostalgic","weight":1,"confidence":0.9},{"tag":"angry","weight":1,"confidence":1},{"tag":"epic","weight":1,"confidence":1}],
		"styles":[{"tag":"pop","confidence":0.7},{"tag":"rock","confidence":0.6},{"tag":"jazz","confidence":0.5},{"tag":"vaporwave","confidence":0.9}],
		"idol":"maybe","vocal":"VOCAL","energy":9}`+"\n```", ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Moods) != 2 || r.Moods[0].Tag != "epic" || r.Moods[0].Weight != 0.75 || r.Moods[1].Confidence != 0.5 {
		t.Fatalf("moods = %+v", r.Moods)
	}
	if len(r.Styles) != 3 || r.Styles[0].Tag != "orchestral" || r.Styles[0].Confidence != 0.82 || len(r.Forced) != 1 {
		t.Fatalf("styles = %+v forced=%v", r.Styles, r.Forced)
	}
	for _, s := range r.Styles {
		if s.Tag == "jazz" {
			t.Fatal("lowest non-locked style must be dropped to keep 3")
		}
	}
	if strings.Join(r.Dropped, ",") != "angry,vaporwave" || r.Idol != "unsure" || r.Vocal != "vocal" || r.Energy != 2 {
		t.Fatalf("result = %+v", r)
	}
	if reasons := ReviewReasons(r, ev); strings.Join(reasons, ",") != "idol_unsure,locked_style" {
		t.Fatalf("reasons = %v", reasons)
	}

	idol, err := ParseResult(`{"moods":[{"tag":"cheerful","weight":1,"confidence":0.3}],"styles":[{"tag":"pop","confidence":0.8}],"idol":"yes","vocal":"vocal","energy":4}`, nil)
	if err != nil || idol.Styles[len(idol.Styles)-1].Tag != "idol" {
		t.Fatalf("idol yes must add the idol style: %+v %v", idol, err)
	}
	notIdol, _ := ParseResult(`{"moods":[{"tag":"cheerful","weight":1,"confidence":0.8}],"styles":[{"tag":"idol","confidence":0.8},{"tag":"pop","confidence":0.6}],"idol":"no","vocal":"vocal","energy":4}`, nil)
	if len(notIdol.Styles) != 1 || notIdol.Styles[0].Tag != "pop" {
		t.Fatalf("idol no must drop the idol style: %+v", notIdol.Styles)
	}
	unsure, _ := ParseResult(`{"moods":[{"tag":"cheerful","weight":0,"confidence":0.4}],"styles":[{"tag":"pop","confidence":0.6}],"idol":"unsure","vocal":"vocal","energy":5}`,
		&AudioEvidence{Arousal: ptr(3.0)})
	if got := strings.Join(ReviewReasons(unsure, &AudioEvidence{Arousal: ptr(3.0)}), ","); got != "low_confidence,idol_unsure,energy_conflict" || unsure.Moods[0].Weight != 1 {
		t.Fatalf("reasons = %s moods=%+v", got, unsure.Moods)
	}
	for _, bad := range []string{"not json", `{"moods":[{"tag":"x","weight":1,"confidence":1}],"styles":[]}`, `{"moods":[]}`} {
		if _, err := ParseResult(bad, nil); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("%q: err = %v", bad, err)
		}
	}
}

func TestLyricsExcerpt(t *testing.T) {
	lrc := "[ti:曲名]\n[00:01.00]作词：某人\n[00:02.00]作曲：某人\n"
	for i := 0; i < 20; i++ {
		lrc += fmt.Sprintf("[00:%02d.00]第%d行\n", 10+i, i)
		if i == 14 || i == 18 {
			lrc += fmt.Sprintf("[00:%02d.50]副歌 la la\n", 10+i)
		}
	}
	got := LyricsExcerpt(lrc, 600)
	lines := strings.Split(got, "\n")
	if strings.Contains(got, "作词") || strings.Contains(got, "[") || lines[0] != "第0行" || lines[len(lines)-1] != "副歌 la la" {
		t.Fatalf("excerpt = %q", got)
	}
	if strings.Count(got, "副歌") != 1 {
		t.Fatal("repeated lines must appear once")
	}
	if n := len([]rune(LyricsExcerpt(strings.Repeat("长歌词", 400), 600))); n != 600 {
		t.Fatalf("cap = %d", n)
	}
	if LyricsExcerpt("  ", 600) != "" {
		t.Fatal("empty lyrics")
	}
	info := storage.TaggingTrackInfo{Title: "x", TrackType: "instrumental"}
	if in := BuildInput(info, nil, nil, "歌词", true); in.HasLyrics || in.Lyrics != "" {
		t.Fatal("instrumental tracks must not send lyrics")
	}
	info.TrackType = "regular"
	if in := BuildInput(info, nil, nil, "歌词", false); !in.HasLyrics || in.Lyrics != "" {
		t.Fatal("lyrics disabled: hasLyrics true but no text")
	}
}

func TestStyleBucketAndEvidence(t *testing.T) {
	for label, want := range map[string]string{
		"Rock---Heavy Metal": "metal", "Rock---Pop Punk": "punk", "Rock---Post Rock": "experimental", "Electronic---Trance": "dance",
		"Electronic---Ambient": "ambient", "Pop---J-pop": "pop", "Pop---City Pop": "citypop_funk", "Classical---Baroque": "orchestral",
		"Stage & Screen---Score": "orchestral", "Folk, World, & Country---Folk": "folk_acoustic", "Folk, World, & Country---Chinese": "ethnic",
		"Electronic---Chiptune": "electronic,denpa", "Non-Music---Field Recording": "",
	} {
		if got := strings.Join(StyleBucket(label), ","); got != want {
			t.Errorf("%s -> %q, want %q", label, got, want)
		}
	}
	labels := Labels{Genre: []string{"Classical---Baroque", "Classical---Romantic", "Pop---J-pop"}, MoodTheme: []string{"epic", "calm", "film"},
		Instrument: []string{"piano", "strings", "voice"}}
	a := 3.2
	f := storage.TrackAudioFeatures{Genre: f16([]float32{0.4, 0.3, 0.05}), MoodTheme: f16([]float32{0.2, 0.1, 0.3}), Instrument: f16([]float32{0.2, 0.7, 0.05}),
		HeadsJSON: `{"voice":0.1,"danceable":0.2,"relaxed":0.8}`, Arousal: &a}
	ev := BuildAudioEvidence(f, labels)
	if ev.StyleHints[0].Label != "orchestral" || strings.Join(ev.Locked, ",") != "orchestral" || ev.Energy != 2 || ev.MoodTheme[0].Label != "film" {
		t.Fatalf("evidence = %+v", ev)
	}
	if len(ev.Instruments) != 2 || ev.Instruments[0].Label != "strings" {
		t.Fatalf("instruments = %+v", ev.Instruments)
	}
	tags := ev.AudioTags()
	if len(tags) == 0 || tags[0].Source != "audio" {
		t.Fatalf("audio tags = %+v", tags)
	}
	var nilEv *AudioEvidence
	if nilEv.AudioTags() != nil {
		t.Fatal("nil evidence has no audio tags")
	}
	if s := SystemPrompt(); !strings.Contains(s, "hotblooded：热血") || !strings.Contains(s, "citypop_funk") {
		t.Fatal("system prompt must list the vocabulary")
	}
	var schema map[string]any
	if err := json.Unmarshal(ResponseSchema(), &schema); err != nil {
		t.Fatal(err)
	}
}

func f16(values []float32) []byte {
	out := make([]byte, 2*len(values))
	for i, v := range values {
		bits := math.Float32bits(v)
		sign := uint16(bits>>16) & 0x8000
		exp := int((bits>>23)&0xff) - 127 + 15
		mant := uint16((bits >> 13) & 0x3ff)
		var h uint16
		switch {
		case v == 0:
			h = sign
		case exp <= 0:
			h = sign
		default:
			h = sign | uint16(exp)<<10 | mant
		}
		binary.LittleEndian.PutUint16(out[2*i:], h)
	}
	return out
}

// ---------------------------------------------------------------- service

type fakeChat struct {
	mu      sync.Mutex
	calls   []ChatRequest
	respond func(req ChatRequest, n int) (ChatResult, error)
}

func (f *fakeChat) Complete(_ context.Context, req ChatRequest) (ChatResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	n := len(f.calls)
	f.mu.Unlock()
	return f.respond(req, n)
}

func (f *fakeChat) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func okResult(content string) (ChatResult, error) {
	return ChatResult{Content: content, Usage: storage.AIUsage{Requests: 1, PromptTokens: 2000, CompletionTokens: 100, CachedTokens: 1500}}, nil
}

const goodJSON = `{"moods":[{"tag":"cheerful","weight":0.7,"confidence":0.8},{"tag":"uplifting","weight":0.3,"confidence":0.6}],"styles":[{"tag":"pop","confidence":0.9}],"idol":"no","vocal":"vocal","energy":4}`

type fixture struct {
	ctx   context.Context
	store *storage.Store
	chat  *fakeChat
	svc   *Service
	now   time.Time
	audio bool
	ids   []int64
}

func newFixture(t *testing.T, tracks int) *fixture {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	for i := 0; i < tracks; i++ {
		md := metadata.AudioMetadata{Title: fmt.Sprintf("Song %02d", i), Album: "Album", Artists: []string{"Band"}, AlbumArtists: []string{"Band"},
			DiscNumber: 1, TrackNumber: i + 1, Genres: []string{"J-Pop"}, DurationMillis: 200000}
		if i%2 == 0 {
			md.Lyrics = "[00:01.00]夏の空に\n[00:05.00]約束した"
		}
		if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("A/%02d.flac", i), FileSize: int64(100 + i), ModifiedAtNS: 1, Metadata: md}); err != nil {
			t.Fatal(err)
		}
	}
	fx := &fixture{ctx: ctx, store: store, now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	fx.chat = &fakeChat{respond: func(ChatRequest, int) (ChatResult, error) { return okResult(goodJSON) }}
	fx.svc = New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		Now:         func() time.Time { return fx.now },
		AudioActive: func(context.Context) bool { return fx.audio },
		NewChat:     func(storage.AISettings) (Chat, error) { return fx.chat, nil },
		Lyrics:      func(_ context.Context, info storage.TaggingTrackInfo) string { return info.Lyrics },
	})
	rows, err := httpExecIDs(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	fx.ids = rows
	return fx
}

func httpExecIDs(ctx context.Context, store *storage.Store) ([]int64, error) {
	cands, err := store.NextTaggingCandidates(ctx, false, VocabVersion, PromptVersion, time.Now(), nil, 1000)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, c := range cands {
		ids = append(ids, c.TrackID)
	}
	return ids, nil
}

func (fx *fixture) settings(t *testing.T, mutate func(*storage.AISettings)) {
	t.Helper()
	v, err := fx.store.AISettings(fx.ctx)
	if err != nil {
		t.Fatal(err)
	}
	v.BaseURL, v.Model, v.APIKey, v.RequestsPerMinute = "https://example.test/v1", "lite", "sk-test", 10000
	if mutate != nil {
		mutate(&v)
	}
	time.Sleep(2 * time.Millisecond) // updated_at 精度为毫秒
	if err := fx.store.SaveAISettings(fx.ctx, v); err != nil {
		t.Fatal(err)
	}
}

func (fx *fixture) drain(t *testing.T, maxSteps int) (time.Duration, State) {
	t.Helper()
	for i := 0; i < maxSteps; i++ {
		if d := fx.svc.Step(fx.ctx); d > 0 {
			return d, fx.svc.state
		}
	}
	t.Fatalf("did not settle (state %s)", fx.svc.state)
	return 0, ""
}

func (fx *fixture) stats(t *testing.T) storage.TaggingStats {
	t.Helper()
	st, err := fx.store.TaggingStats(fx.ctx, fx.audio, VocabVersion, PromptVersion, fx.now)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestNotConfiguredOrDisabledMakesNoCalls(t *testing.T) {
	fx := newFixture(t, 3)
	if _, st := fx.drain(t, 3); st != StateNotConfigured {
		t.Fatalf("state = %s", st)
	}
	fx.settings(t, nil)
	if _, st := fx.drain(t, 3); st != StateDisabled || fx.chat.count() != 0 {
		t.Fatalf("state = %s calls = %d", st, fx.chat.count())
	}
	v, _ := fx.store.AISettings(fx.ctx)
	if !v.IncludeLyrics || v.Concurrency != 4 || v.TaggingEnabled {
		t.Fatalf("defaults = %+v", v)
	}
}

func TestTagsLibraryIdempotentlyAndRetagsOnChange(t *testing.T) {
	fx := newFixture(t, 5)
	fx.settings(t, func(v *storage.AISettings) { v.TaggingEnabled = true })
	if _, st := fx.drain(t, 10); st != StateIdle {
		t.Fatalf("state = %s", st)
	}
	if fx.chat.count() != 5 {
		t.Fatalf("calls = %d", fx.chat.count())
	}
	st := fx.stats(t)
	if st.Eligible != 5 || st.Tagged != 5 || st.AvgPrompt != 2000 {
		t.Fatalf("stats = %+v", st)
	}
	req := fx.chat.calls[0]
	if req.Model != "lite" || req.Format != "json_schema" || req.System != SystemPrompt() || !strings.Contains(req.User, `"title":"Song`) {
		t.Fatalf("request = %+v", req)
	}
	var withLyrics, without int
	for _, c := range fx.chat.calls {
		if strings.Contains(c.User, "夏の空に") {
			withLyrics++
		} else {
			without++
		}
	}
	if withLyrics != 3 || without != 2 {
		t.Fatalf("lyrics sent %d / %d", withLyrics, without)
	}
	tags, _ := fx.store.TrackTags(fx.ctx, fx.ids[0])
	if len(tags) != 3 || tags[0].Source != "llm" {
		t.Fatalf("tags = %+v", tags)
	}
	usage, _ := fx.store.AIUsageTotals(fx.ctx, "")
	if len(usage) != 1 || usage[0].Usage.Requests != 5 || usage[0].Usage.PromptTokens != 10000 || usage[0].Purpose != "tag" {
		t.Fatalf("usage = %+v", usage)
	}

	// 再跑一轮：没有变化，不调用模型。
	fx.drain(t, 3)
	if fx.chat.count() != 5 {
		t.Fatal("unchanged tracks must not be re-tagged")
	}
	// 只改 updated_at（内容不变）：只更新 stamp，不调用模型。
	info0, _ := fx.store.TaggingTrackInfo(fx.ctx, fx.ids[0])
	if err := fx.store.UpdateTrack(fx.ctx, fx.ids[0], info0.Title, 1, len(fx.ids), "", "", []string{"J-Pop"}); err != nil {
		t.Fatal(err)
	}
	fx.drain(t, 3)
	if fx.chat.count() != 5 || fx.stats(t).Tagged != 5 {
		t.Fatalf("same content must only refresh the stamp (calls %d)", fx.chat.count())
	}
	// 人工修正保留；改标题后重新打标。
	if err := fx.store.SetUserTrackTags(fx.ctx, fx.ids[1], "mood", []storage.TrackTag{{Tag: "relaxed", Weight: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.UpdateTrack(fx.ctx, fx.ids[1], "新标题", 1, len(fx.ids)-1, "", "", []string{"J-Pop"}); err != nil {
		t.Fatal(err)
	}
	fx.drain(t, 3)
	if fx.chat.count() != 6 || !strings.Contains(fx.chat.calls[5].User, "新标题") {
		t.Fatalf("changed track must be re-tagged (calls %d)", fx.chat.count())
	}
	tags, _ = fx.store.TrackTags(fx.ctx, fx.ids[1])
	eff := storage.EffectiveTags(tags)
	if eff[0].Source != "user" || eff[0].Tag != "relaxed" {
		t.Fatalf("user correction must win: %+v", eff)
	}
}

func TestWaitsForAudioWhenActive(t *testing.T) {
	fx := newFixture(t, 3)
	fx.audio = true
	fx.settings(t, func(v *storage.AISettings) { v.TaggingEnabled = true })
	if _, st := fx.drain(t, 3); st != StateIdle || fx.chat.count() != 0 {
		t.Fatalf("state %s calls %d", st, fx.chat.count())
	}
	if st := fx.stats(t); st.WaitingAudio != 3 {
		t.Fatalf("stats = %+v", st)
	}
	snap, err := fx.svc.Snapshot(fx.ctx)
	if err != nil || snap.Pending != 0 || !snap.AudioActive {
		t.Fatalf("snapshot = %+v %v", snap, err)
	}
}

func TestInvalidOutputRetriesThenFails(t *testing.T) {
	fx := newFixture(t, 1)
	fx.chat.respond = func(ChatRequest, int) (ChatResult, error) { return okResult(`{"moods":[]}`) }
	fx.settings(t, func(v *storage.AISettings) { v.TaggingEnabled = true })
	fx.drain(t, 3)
	if fx.chat.count() != 2 {
		t.Fatalf("invalid output must be retried once, calls = %d", fx.chat.count())
	}
	fails, _ := fx.store.TaggingFailures(fx.ctx, 10)
	if len(fails) != 1 || fails[0].Permanent || !strings.Contains(fails[0].LastError, "模型输出无效") {
		t.Fatalf("failures = %+v", fails)
	}
	for i := 0; i < permanentAfter; i++ {
		fx.now = fx.now.Add(25 * time.Hour)
		fx.drain(t, 3)
	}
	fails, _ = fx.store.TaggingFailures(fx.ctx, 10)
	if !fails[0].Permanent || fx.stats(t).Permanent != 1 {
		t.Fatalf("failures = %+v", fails)
	}
	if n, _ := fx.store.ClearTaggingFailures(fx.ctx); n != 1 {
		t.Fatal("retry all")
	}
}

func TestGlobalErrorsBlockOrBackOff(t *testing.T) {
	fx := newFixture(t, 2)
	fx.chat.respond = func(ChatRequest, int) (ChatResult, error) {
		return ChatResult{}, &LLMError{Kind: KindAuth, Status: 401, Message: "bad key"}
	}
	fx.settings(t, func(v *storage.AISettings) { v.TaggingEnabled = true; v.Concurrency = 1 })
	if d, st := fx.drain(t, 3); st != StateBlocked || d != time.Hour {
		t.Fatalf("state %s delay %s", st, d)
	}
	calls := fx.chat.count()
	fx.drain(t, 3)
	if fx.chat.count() != calls {
		t.Fatal("blocked: no calls until settings change")
	}
	if fails, _ := fx.store.TaggingFailures(fx.ctx, 10); len(fails) != 0 {
		t.Fatal("auth errors must not be recorded per track")
	}
	snap, _ := fx.svc.Snapshot(fx.ctx)
	if snap.State != StateBlocked || snap.Detail != "API Key 无效或没有权限" {
		t.Fatalf("snapshot = %+v", snap)
	}
	// 修改设置后解除阻塞；遇到限流进入退避。
	fx.chat.respond = func(ChatRequest, int) (ChatResult, error) {
		return ChatResult{}, &LLMError{Kind: KindRateLimit, Status: 429, Message: "slow down", RetryAfter: 2 * time.Minute}
	}
	fx.settings(t, func(v *storage.AISettings) { v.TaggingEnabled = true; v.Concurrency = 1; v.APIKey = "sk-new" })
	d, st := fx.drain(t, 3)
	if st != StateBackoff || d != 2*time.Minute {
		t.Fatalf("state %s delay %s", st, d)
	}
	calls = fx.chat.count()
	if d := fx.svc.Step(fx.ctx); d <= 0 || fx.chat.count() != calls {
		t.Fatal("no calls during backoff")
	}
	fx.now = fx.now.Add(3 * time.Minute)
	fx.chat.respond = func(ChatRequest, int) (ChatResult, error) { return okResult(goodJSON) }
	if _, st := fx.drain(t, 5); st != StateIdle || fx.stats(t).Tagged != 2 {
		t.Fatalf("state %s stats %+v", st, fx.stats(t))
	}
}

func TestJSONSchemaFallbackAndReviewCascade(t *testing.T) {
	fx := newFixture(t, 2)
	lowConf := `{"moods":[{"tag":"cheerful","weight":1,"confidence":0.3}],"styles":[{"tag":"rock","confidence":0.6}],"idol":"no","vocal":"vocal","energy":3}`
	fx.chat.respond = func(req ChatRequest, _ int) (ChatResult, error) {
		if req.Model == "lite" && req.Format == "json_schema" {
			return ChatResult{}, &LLMError{Kind: KindFormat, Status: 400, Message: "response_format json_schema not supported"}
		}
		if req.Model == "lite" {
			return okResult(lowConf)
		}
		return okResult(goodJSON)
	}
	fx.settings(t, func(v *storage.AISettings) { v.TaggingEnabled = true; v.ReviewModel = "pro" })
	fx.drain(t, 5)
	st := fx.stats(t)
	if st.Tagged != 2 || st.NeedsReview != 2 || st.Reviewed != 0 {
		t.Fatalf("review disabled: stats = %+v", st)
	}
	var schemaCalls int
	for _, c := range fx.chat.calls {
		if c.Model == "pro" {
			t.Fatal("review model must not be called while review is disabled")
		}
		if c.Format == "json_schema" {
			schemaCalls++
		}
	}
	if schemaCalls != 1 && schemaCalls != 2 { // 并发时两首可能同时先试一次
		t.Fatalf("json_schema attempts = %d", schemaCalls)
	}
	meta, _, _ := fx.store.TrackTagMeta(fx.ctx, fx.ids[0])
	if meta.ReviewReasons != "low_confidence" || meta.Model != "lite" {
		t.Fatalf("meta = %+v", meta)
	}
	// 开启复核：处理积压的待复核曲目。
	fx.settings(t, func(v *storage.AISettings) { v.TaggingEnabled = true; v.ReviewModel = "pro"; v.ReviewEnabled = true })
	if _, st := fx.drain(t, 5); st != StateIdle {
		t.Fatalf("state = %s", st)
	}
	meta, _, _ = fx.store.TrackTagMeta(fx.ctx, fx.ids[0])
	if meta.ReviewedBy != "pro" || meta.Model != "pro" || !meta.NeedsReview {
		t.Fatalf("meta after review = %+v", meta)
	}
	tags, _ := fx.store.TrackTags(fx.ctx, fx.ids[0])
	if tags[0].Tag != "cheerful" || tags[0].Confidence != 0.8 {
		t.Fatalf("review result must replace the primary one: %+v", tags)
	}
	// 新曲目：主模型结果把握低时立即复核。
	fx.chat.mu.Lock()
	fx.chat.calls = nil
	fx.chat.mu.Unlock()
	if err := fx.store.ResetTrackTagging(fx.ctx, fx.ids[1]); err != nil {
		t.Fatal(err)
	}
	fx.drain(t, 5)
	if fx.chat.count() != 2 || fx.chat.calls[1].Model != "pro" {
		t.Fatalf("cascade calls = %+v", fx.chat.calls)
	}
	usage, _ := fx.store.AIUsageTotals(fx.ctx, "")
	purposes := map[string]bool{}
	for _, u := range usage {
		purposes[u.Model+"/"+u.Purpose] = true
	}
	if !purposes["lite/tag"] || !purposes["pro/review"] {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestSampleComparison(t *testing.T) {
	fx := newFixture(t, 12)
	fx.chat.respond = func(req ChatRequest, _ int) (ChatResult, error) {
		if req.Model == "pro" {
			return okResult(`{"moods":[{"tag":"cheerful","weight":1,"confidence":0.9}],"styles":[{"tag":"rock","confidence":0.8}],"idol":"no","vocal":"vocal","energy":4}`)
		}
		return okResult(goodJSON)
	}
	fx.settings(t, nil)
	if err := fx.svc.RequestSample(fx.ctx, 10); err == nil {
		t.Fatal("sample requires a review model")
	}
	fx.settings(t, func(v *storage.AISettings) { v.ReviewModel = "pro" })
	if err := fx.svc.RequestSample(fx.ctx, 10); err != nil {
		t.Fatal(err)
	}
	if _, st := fx.drain(t, 20); st != StateDisabled {
		t.Fatalf("state after sample = %s", st)
	}
	run, ok, _ := fx.store.LatestAISampleRun(fx.ctx)
	if !ok || run.Status != "done" || run.Done != 10 || fx.chat.count() != 20 {
		t.Fatalf("run = %+v calls = %d", run, fx.chat.count())
	}
	items, _ := fx.store.AISampleItems(fx.ctx, run.ID)
	cmp := Compare(items)
	if cmp.N != 10 || cmp.PrimaryMoodAgree != 1 || cmp.PrimaryStyleAgree != 0 || cmp.MoodJaccard != 0.5 {
		t.Fatalf("compare = %+v", cmp)
	}
	if st := fx.stats(t); st.Tagged != 0 {
		t.Fatal("sampling must not write tags")
	}
}

// ---------------------------------------------------------------- HTTP client

func TestChatClient(t *testing.T) {
	var got map[string]any
	var auth string
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1beta/openai/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		switch mode {
		case "ok":
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":[{"type":"text","text":"{\"a\":1}"}]},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":2100,"completion_tokens":90,"prompt_tokens_details":{"cached_tokens":1500},"completion_tokens_details":{"reasoning_tokens":7}}}`)
		case "401":
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `[{"error":{"code":401,"message":"API key sk-secret-123 not valid","status":"UNAUTHENTICATED"}}]`)
		case "429":
			w.Header().Set("Retry-After", "42")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"error":{"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`)
		case "format":
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid response_format: json_schema is not supported"}}`)
		case "empty":
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":""},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":5}}`)
		}
	}))
	defer srv.Close()
	c, err := NewChatClient(srv.URL+"/v1beta/openai/", "sk-secret-123", "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Complete(context.Background(), ChatRequest{Model: "m", System: "s", User: "u", Schema: ResponseSchema(), ReasoningEffort: "none"})
	if err != nil || res.Content != `{"a":1}` || res.Usage.PromptTokens != 2100 || res.Usage.CachedTokens != 1500 || res.Usage.ReasoningTokens != 7 {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if auth != "Bearer sk-secret-123" || got["temperature"].(float64) != 0 || got["reasoning_effort"] != "none" {
		t.Fatalf("request = %v", got)
	}
	if rf := got["response_format"].(map[string]any); rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v", rf)
	}
	check := func(m string, kind ErrorKind, retry time.Duration) {
		t.Helper()
		mode = m
		_, err := c.Complete(context.Background(), ChatRequest{Model: "m", Format: "json_object"})
		var le *LLMError
		if !errors.As(err, &le) || le.Kind != kind || le.RetryAfter != retry {
			t.Fatalf("%s: err = %#v", m, err)
		}
		if strings.Contains(le.Error(), "sk-secret-123") {
			t.Fatalf("api key leaked: %s", le.Error())
		}
	}
	check("401", KindAuth, 0)
	check("429", KindQuota, 42*time.Second)
	check("format", KindFormat, 0)
	check("empty", KindFiltered, 0)
	if got["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatal("json_object format")
	}
	for _, bad := range []string{"ftp://x", "https://user:pw@x/v1", "https://x/v1?key=1", "x"} {
		if ValidateBaseURL(bad) == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if _, err := NewChatClient(srv.URL, "k", "ftp://proxy", time.Second); err == nil {
		t.Fatal("bad proxy accepted")
	}
}
