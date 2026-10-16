package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

type httpFakeChat struct {
	mu    sync.Mutex
	calls int
}

func (f *httpFakeChat) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	return tagging.ChatResult{Content: `{"moods":[{"tag":"cheerful","weight":1,"confidence":0.9}],"styles":[{"tag":"pop","confidence":0.8}],"idol":"no","vocal":"vocal","energy":4}`,
		Usage: storage.AIUsage{Requests: 1, PromptTokens: 2100, CompletionTokens: 95}, Latency: 800 * time.Millisecond}, nil
}

func aiTaggingTestApp(t *testing.T) (*App, *storage.Store, http.Handler, *http.Cookie, *tagging.Service, *httpFakeChat, int64) {
	t.Helper()
	store := credentialTestStore(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 10, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "夏の歌", Album: "Album", Artists: []string{"Band"}, AlbumArtists: []string{"Band"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	cands, err := store.NextTaggingCandidates(ctx, false, tagging.VocabVersion, tagging.PromptVersion, time.Now(), nil, 10)
	if err != nil || len(cands) != 1 {
		t.Fatalf("candidates = %v %v", cands, err)
	}
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	chat := &httpFakeChat{}
	svc := tagging.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), tagging.Options{
		NewChat: func(storage.AISettings) (tagging.Chat, error) { return chat, nil },
		Lyrics:  func(context.Context, storage.TaggingTrackInfo) string { return "" },
	})
	app.SetAITagging(svc)
	handler := app.Handler()
	return app, store, handler, mustLogin(t, handler, "admin", testAdminPassword), svc, chat, cands[0].TrackID
}

func apiRequest(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testEnvAPIToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func capabilityFeature(t *testing.T, handler http.Handler, name string) bool {
	t.Helper()
	rec := apiRequest(handler, http.MethodGet, "/api/v1/capabilities", "")
	var body struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Features[name]
}

func TestAITaggingSettingsAndPage(t *testing.T) {
	app, store, handler, cookie, svc, chat, trackID := aiTaggingTestApp(t)
	ctx := context.Background()
	body := getWithCookie(handler, "/admin/ai-tagging", cookie).Body.String()
	for _, want := range []string{"未配置模型服务", `name="base_url"`, "generativelanguage.googleapis.com", `name="include_lyrics" checked`, "测试连接"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if dash := getWithCookie(handler, "/admin", cookie).Body.String(); !strings.Contains(dash, `href="/admin/ai-tagging"`) || !strings.Contains(dash, "未配置") {
		t.Fatal("dashboard card missing")
	}

	// Enabling without a key is refused.
	rec := postSecurity(t, app, handler, cookie, "/admin/ai-tagging/settings", url.Values{"enabled": {"on"}, "base_url": {"https://x.test/v1"}, "model": {"lite"}, "include_lyrics": {"on"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("需要填写接口地址、模型和 API Key")) {
		t.Fatalf("location = %s", loc)
	}
	// Invalid base URL and proxy.
	rec = postSecurity(t, app, handler, cookie, "/admin/ai-tagging/settings", url.Values{"base_url": {"https://u:p@x.test/v1"}, "model": {"lite"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("不能包含账号密码")) {
		t.Fatalf("location = %s", loc)
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/ai-tagging/settings", url.Values{"base_url": {"https://x.test/v1"}, "proxy_url": {"ftp://p"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("代理地址无效")) {
		t.Fatalf("location = %s", loc)
	}

	form := url.Values{"enabled": {"on"}, "base_url": {"https://x.test/v1/"}, "model": {"lite"}, "api_key": {"sk-secret-value"}, "include_lyrics": {"on"},
		"rpm": {"120"}, "concurrency": {"2"}, "reasoning_effort": {"none"}, "proxy_url": {"http://192.168.1.2:7890"}}
	rec = postSecurity(t, app, handler, cookie, "/admin/ai-tagging/settings", form)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("开始首次全量打标")) {
		t.Fatalf("location = %s", loc)
	}
	st, _ := store.AISettings(ctx)
	if st.BaseURL != "https://x.test/v1" || st.APIKey != "sk-secret-value" || st.RequestsPerMinute != 120 || st.Concurrency != 2 || st.ReasoningEffort != "none" || !st.TaggingEnabled || st.ProxyURL == "" {
		t.Fatalf("settings = %+v", st)
	}
	if !capabilityFeature(t, handler, "trackTags") {
		t.Fatal("trackTags capability")
	}
	page := getWithCookie(handler, "/admin/ai-tagging", cookie).Body.String()
	if strings.Contains(page, "sk-secret-value") || strings.Contains(page, "192.168.1.2") || !strings.Contains(page, "已设置（留空保持不变）") {
		t.Fatal("API key and proxy must never be rendered")
	}
	// Saving with an empty key keeps it; clear_api_key removes it (and tagging can no longer stay enabled).
	form.Set("api_key", "")
	form.Set("proxy_url", "")
	postSecurity(t, app, handler, cookie, "/admin/ai-tagging/settings", form)
	if st, _ = store.AISettings(ctx); st.APIKey != "sk-secret-value" || st.ProxyURL == "" {
		t.Fatal("empty fields must keep the stored secrets")
	}

	// Run the worker: the track gets tagged.
	for i := 0; i < 5; i++ {
		if svc.Step(ctx) > 0 {
			break
		}
	}
	if chat.calls != 1 {
		t.Fatalf("calls = %d", chat.calls)
	}
	status := getWithCookie(handler, "/admin/ai-tagging/status", cookie)
	var payload struct {
		Status tagging.Snapshot `json:"status"`
		View   aiTaggingView    `json:"view"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &payload); err != nil || payload.Status.Stats == nil || payload.Status.Stats.Tagged != 1 || payload.View.StateLabel != "已完成，新歌会自动打标" {
		t.Fatalf("status = %s", status.Body.String())
	}
	if !strings.Contains(payload.View.EstimateText, "2100 输入 + 95 输出") {
		t.Fatalf("estimate = %q", payload.View.EstimateText)
	}
	if rec := apiRequest(handler, http.MethodGet, "/api/v1/ai/tagging", ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("api status %d", rec.Code)
	}
	page = getWithCookie(handler, "/admin/ai-tagging", cookie).Body.String()
	if !strings.Contains(page, "元气欢快") || !strings.Contains(page, "/admin/tracks/") || !strings.Contains(page, "累计 1 次请求") {
		t.Fatal("page must list recent results and usage")
	}

	// Pause / resume.
	postSecurity(t, app, handler, cookie, "/admin/ai-tagging/pause", nil)
	if st, _ = store.AISettings(ctx); !st.TaggingPaused {
		t.Fatal("pause")
	}
	postSecurity(t, app, handler, cookie, "/admin/ai-tagging/resume", nil)
	if st, _ = store.AISettings(ctx); st.TaggingPaused {
		t.Fatal("resume")
	}

	// Connection test.
	rec = postSecurity(t, app, handler, cookie, "/admin/ai-tagging/test", nil)
	if loc, _ := url.QueryUnescape(rec.Header().Get("Location")); !strings.Contains(loc, "lite 正常") || !strings.Contains(loc, "元气欢快") {
		t.Fatalf("test notice = %s", loc)
	}
	_ = trackID
}

func TestTrackTagsAPIAndPage(t *testing.T) {
	app, store, handler, cookie, svc, _, trackID := aiTaggingTestApp(t)
	ctx := context.Background()
	st := storage.DefaultAISettings()
	st.BaseURL, st.Model, st.APIKey, st.TaggingEnabled = "https://x.test/v1", "lite", "k", true
	if err := store.SaveAISettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5 && svc.Step(ctx) == 0; i++ {
	}
	id := strconv.FormatInt(trackID, 10)
	rec := apiRequest(handler, http.MethodGet, "/api/v1/tracks/"+id+"/tags", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"label":"元气欢快"`) || !strings.Contains(rec.Body.String(), `"model":"lite"`) {
		t.Fatalf("get tags %d %s", rec.Code, rec.Body.String())
	}
	if rec = apiRequest(handler, http.MethodGet, "/api/v1/tracks/999999/tags", ""); rec.Code != 404 {
		t.Fatalf("missing track = %d", rec.Code)
	}
	for _, bad := range []string{`{"moods":["angry"]}`, `{"styles":["pop","rock","jazz","metal"]}`, `{"foo":1}`} {
		if rec = apiRequest(handler, http.MethodPut, "/api/v1/tracks/"+id+"/tags", bad); rec.Code != 400 {
			t.Fatalf("%s accepted: %d", bad, rec.Code)
		}
	}
	rec = apiRequest(handler, http.MethodPut, "/api/v1/tracks/"+id+"/tags", `{"moods":["relaxed","sad"]}`)
	var payload struct {
		Effective []trackTagsJSON `json:"effective"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || rec.Code != 200 {
		t.Fatalf("put %d %s", rec.Code, rec.Body.String())
	}
	var moods, styles []string
	for _, tg := range payload.Effective {
		if tg.Kind == "mood" {
			moods = append(moods, tg.Tag+":"+tg.Source)
			if tg.Tag == "sad" && !tg.Internal {
				t.Fatal("negative markers must be flagged internal")
			}
		} else {
			styles = append(styles, tg.Tag+":"+tg.Source)
		}
	}
	if strings.Join(moods, ",") != "relaxed:user,sad:user" || strings.Join(styles, ",") != "pop:llm" {
		t.Fatalf("effective = %v %v", moods, styles)
	}
	// Clearing restores the AI result.
	apiRequest(handler, http.MethodPut, "/api/v1/tracks/"+id+"/tags", `{"moods":[]}`)
	tags, _ := store.TrackTags(ctx, trackID)
	if eff := storage.EffectiveTags(tags); eff[0].Source != "llm" {
		t.Fatalf("after clear = %+v", eff)
	}

	// Page: correction form, retag.
	page := getWithCookie(handler, "/admin/tracks/"+id+"/tags", cookie).Body.String()
	for _, want := range []string{"夏の歌", "AI 结果", `name="mood" value="cheerful" checked`, "伤感（内部）", "重新打标"} {
		if !strings.Contains(page, want) {
			t.Fatalf("track page missing %q", want)
		}
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/tracks/"+id+"/tags", url.Values{"mood": {"epic"}, "action": {"save"}})
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, url.QueryEscape("都至少选择 1 个")) {
		t.Fatalf("location = %s", loc)
	}
	postSecurity(t, app, handler, cookie, "/admin/tracks/"+id+"/tags", url.Values{"mood": {"epic"}, "style": {"orchestral"}, "action": {"save"}})
	tags, _ = store.TrackTags(ctx, trackID)
	if eff := storage.EffectiveTags(tags); len(eff) != 2 || eff[0].Source != "user" {
		t.Fatalf("form save = %+v", eff)
	}
	postSecurity(t, app, handler, cookie, "/admin/tracks/"+id+"/tags", url.Values{"action": {"reset"}})
	if tags, _ = store.TrackTags(ctx, trackID); storage.EffectiveTags(tags)[0].Source != "llm" {
		t.Fatal("reset must clear user tags")
	}
	postSecurity(t, app, handler, cookie, "/admin/tracks/"+id+"/tags/retag", nil)
	if _, ok, _ := store.TrackTagMeta(ctx, trackID); ok {
		t.Fatal("retag must drop the meta row")
	}
	if lib := getWithCookie(handler, "/admin/tracks", cookie).Body.String(); !strings.Contains(lib, "/admin/tracks/"+id+"/tags") {
		t.Fatal("library drawer must link to the tags page")
	}
}
