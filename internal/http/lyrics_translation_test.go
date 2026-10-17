package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// fakeTranslator answers with "<model>:<line>" for every line it receives.
type fakeTranslator struct {
	mu     sync.Mutex
	calls  int
	models []string
	fail   bool
}

func (f *fakeTranslator) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	f.mu.Lock()
	f.calls++
	f.models = append(f.models, req.Model)
	fail := f.fail
	f.mu.Unlock()
	if fail {
		return tagging.ChatResult{}, &tagging.LLMError{Kind: tagging.KindAuth, Status: 401, Message: "invalid key"}
	}
	var in struct {
		Lines []string `json:"lines"`
	}
	_ = json.Unmarshal([]byte(req.User), &in)
	out := make([]string, len(in.Lines))
	for i, l := range in.Lines {
		out[i] = req.Model + ":" + l
	}
	raw, _ := json.Marshal(map[string]any{"lines": out})
	return tagging.ChatResult{Content: string(raw), Usage: storage.AIUsage{Requests: 1, PromptTokens: 50, CompletionTokens: 20}}, nil
}

func translateRequest(handler http.Handler, trackID int64, lang string) (*lyricsTranslationResponse, int, string) {
	raw, _ := json.Marshal(map[string]any{"targetLanguage": lang})
	rec := apiRequest(handler, http.MethodPost, "/api/v1/tracks/"+strconv.FormatInt(trackID, 10)+"/lyrics/translation", string(raw))
	if rec.Code != http.StatusOK {
		return nil, rec.Code, rec.Body.String()
	}
	var body lyricsTranslationResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return &body, rec.Code, rec.Body.String()
}

// lyricsTestApp is an admin-logged-in app with one track whose embedded lyrics are given.
func lyricsTestApp(t *testing.T, title, embedded string) (*App, *storage.Store, http.Handler, *http.Cookie, int64) {
	t.Helper()
	store := credentialTestStore(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 10, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: title, Album: "Album", Artists: []string{"Band"}, AlbumArtists: []string{"Band"}, DiscNumber: 1, TrackNumber: 1,
			DurationMillis: 200000, Lyrics: embedded}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil || len(tracks.Items) != 1 {
		t.Fatalf("tracks = %v %v", tracks, err)
	}
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	return app, store, handler, mustLogin(t, handler, "admin", testAdminPassword), tracks.Items[0].ID
}

func TestLyricsTranslationFlow(t *testing.T) {
	app, store, handler, cookie, trackID := lyricsTestApp(t, "夏の歌", `[00:01.00]hello
[00:02.00]
[00:03.00] world `)
	fake := &fakeTranslator{}
	app.newTranslationChat = func(storage.AISettings) (tagging.Chat, error) { return fake, nil }
	ctx := context.Background()

	// Off by default: no capability, API refuses, dashboard links the page.
	if capabilityFeature(t, handler, "lyricsTranslation") {
		t.Fatal("capability must be off by default")
	}
	if _, code, body := translateRequest(handler, trackID, "en"); code != http.StatusConflict || !strings.Contains(body, "translation_disabled") {
		t.Fatalf("disabled: %d %s", code, body)
	}
	if dash := getWithCookie(handler, "/admin", cookie).Body.String(); !strings.Contains(dash, `href="/admin/lyrics-translations"`) || !strings.Contains(dash, "未开启 · 已缓存 0 条翻译") {
		t.Fatal("dashboard card missing")
	}

	// Enabling needs the shared model service settings.
	rec := postSecurity(t, app, handler, cookie, lyricsTranslationPath+"/settings", url.Values{"enabled": {"on"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("开启翻译前需要")) {
		t.Fatalf("location = %s", loc)
	}
	if err := store.SaveAISettings(ctx, storage.AISettings{BaseURL: "https://x.test/v1", APIKey: "sk-secret", Model: "lite", RequestsPerMinute: 60, Concurrency: 4}); err != nil {
		t.Fatal(err)
	}
	rec = postSecurity(t, app, handler, cookie, lyricsTranslationPath+"/settings", url.Values{"enabled": {"on"}, "model": {"flash"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("歌词翻译已开启")) {
		t.Fatalf("location = %s", loc)
	}
	if !capabilityFeature(t, handler, "lyricsTranslation") {
		t.Fatal("capability should be on")
	}
	if st, _ := store.AISettings(ctx); st.TaggingEnabled {
		t.Fatal("enabling translation must not enable tagging")
	}

	// Validation.
	if _, code, body := translateRequest(handler, trackID, "en; drop"); code != http.StatusBadRequest || !strings.Contains(body, "invalid_language") {
		t.Fatalf("invalid language: %d %s", code, body)
	}
	if _, code, _ := translateRequest(handler, trackID+999, "en"); code != http.StatusNotFound {
		t.Fatalf("unknown track: %d", code)
	}

	// First request translates, second is served from cache.
	got, code, body := translateRequest(handler, trackID, "zh-Hans")
	if code != http.StatusOK || got.Cached || strings.Join(got.Lines, "|") != "flash:hello||flash:world" || got.Model != "flash" {
		t.Fatalf("first: %d %s", code, body)
	}
	calls := fake.calls
	got, _, _ = translateRequest(handler, trackID, "zh-Hans")
	if !got.Cached || fake.calls != calls {
		t.Fatalf("second request must hit the cache (calls %d → %d)", calls, fake.calls)
	}
	if usage, _ := store.AIUsageTotals(ctx, ""); len(usage) != 1 || usage[0].Purpose != "translate" || usage[0].Model != "flash" {
		t.Fatalf("usage = %+v", usage)
	}

	// Concurrent identical requests share one model call.
	fake.calls = 0
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); translateRequest(handler, trackID, "ja") }()
	}
	wg.Wait()
	if fake.calls != 1 {
		t.Fatalf("concurrent identical requests made %d calls", fake.calls)
	}

	// Model errors are reported and not cached.
	fake.fail = true
	if _, code, body := translateRequest(handler, trackID, "fr"); code != http.StatusBadGateway || !strings.Contains(body, "invalid key") || strings.Contains(body, "sk-secret") {
		t.Fatalf("failure: %d %s", code, body)
	}
	fake.fail = false

	// Admin list, detail, retranslate with another model, delete.
	page := getWithCookie(handler, lyricsTranslationPath, cookie).Body.String()
	for _, want := range []string{"2 条翻译", "zh-Hans", `name="model" value="flash"`, "data-check-all"} {
		if !strings.Contains(page, want) {
			t.Fatalf("list page missing %q", want)
		}
	}
	if strings.Contains(page, "sk-secret") {
		t.Fatal("API key rendered")
	}
	items, _, _ := store.ListLyricsTranslations(ctx, storage.LyricsTranslationFilter{Language: "zh-Hans"})
	detailPath := lyricsTranslationPath + "/" + strconv.FormatInt(items[0].ID, 10)
	detail := getWithCookie(handler, detailPath, cookie).Body.String()
	if !strings.Contains(detail, "flash:hello") || !strings.Contains(detail, "Simplified Chinese") {
		t.Fatal("detail page missing translation")
	}
	rec = postSecurity(t, app, handler, cookie, detailPath+"/retranslate", url.Values{"model": {"pro"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("已用 pro 重新翻译")) {
		t.Fatalf("retranslate location = %s", loc)
	}
	if got, _, _ = translateRequest(handler, trackID, "zh-Hans"); !got.Cached || got.Lines[0] != "pro:hello" || got.Model != "pro" {
		t.Fatalf("after retranslate = %+v", got)
	}
	// Disabling keeps the cache but blocks clients.
	postSecurity(t, app, handler, cookie, lyricsTranslationPath+"/settings", url.Values{"model": {"flash"}})
	if capabilityFeature(t, handler, "lyricsTranslation") {
		t.Fatal("capability should be off after disabling")
	}
	if _, code, _ := translateRequest(handler, trackID, "zh-Hans"); code != http.StatusConflict {
		t.Fatalf("disabled request = %d", code)
	}
	rec = postSecurity(t, app, handler, cookie, lyricsTranslationPath+"/delete", url.Values{"id": {strconv.FormatInt(items[0].ID, 10)}, "return": {"//evil.test"}})
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, lyricsTranslationPath) || !strings.Contains(loc, url.QueryEscape("已删除 1 条")) {
		t.Fatalf("delete location = %s", loc)
	}
	if _, total, _ := store.ListLyricsTranslations(ctx, storage.LyricsTranslationFilter{}); total != 1 {
		t.Fatalf("total after delete = %d", total)
	}

	// Non-admin sessions cannot reach the admin page.
	if rec := getWithCookie(handler, lyricsTranslationPath, nil); rec.Code == http.StatusOK {
		t.Fatal("admin page must require login")
	}
}
