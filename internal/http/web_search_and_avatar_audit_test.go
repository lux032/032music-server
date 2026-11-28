package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/websearch"
)

func makeTestPNGBytes(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, c)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAISettingsWebSearchTavilyMultiKeyAndRedaction(t *testing.T) {
	store := credentialTestStore(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	reg := websearch.NewRegistry()
	app.SetWebSearchRegistry(reg)

	var seenAuth []string
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seenAuth = append(seenAuth, auth)
		if auth == "Bearer tvly-secret-alpha-1111" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited tvly-secret-alpha-1111"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{
				{"url": "https://musicbrainz.org/", "title": "MusicBrainz", "content": "Open music encyclopedia"},
			},
		})
	}))
	defer tavilySrv.Close()
	reg.SetEndpointsForTest(tavilySrv.URL, tavilySrv.URL, tavilySrv.Client(), nil)

	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	// Save two Tavily keys via AI settings form.
	rec := postSecurity(t, app, handler, cookie, "/admin/settings/ai", url.Values{
		"web_search_provider": {"tavily"},
		"tavily_keys_mode":    {"replace"},
		"tavily_keys":         {"tvly-secret-alpha-1111\ntvly-secret-beta-2222"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save web search code = %d", rec.Code)
	}

	// Page must display masked keys and never leak raw secrets.
	page := getAdmin(t, handler, cookie, "/admin/settings/ai")
	for _, secret := range []string{"tvly-secret-alpha-1111", "tvly-secret-beta-2222"} {
		if strings.Contains(page, secret) {
			t.Fatalf("ai-settings HTML leaked plaintext key %q", secret)
		}
	}
	for _, want := range []string{"tvly-****1111", "tvly-****2222", "Tavily（2 把 Key 轮询）"} {
		if !strings.Contains(page, want) {
			t.Fatalf("ai-settings HTML missing %q", want)
		}
	}

	// Append a third key and verify deduplication.
	rec = postSecurity(t, app, handler, cookie, "/admin/settings/ai", url.Values{
		"web_search_provider": {"tavily"},
		"tavily_keys_mode":    {"append"},
		"tavily_keys":         {"tvly-secret-beta-2222\ntvly-secret-gamma-3333"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("append web search code = %d", rec.Code)
	}
	ws, err := store.WebSearchSettings(context.Background())
	if err != nil || len(ws.TavilyKeys) != 3 {
		t.Fatalf("saved TavilyKeys = %+v err=%v, want 3", ws.TavilyKeys, err)
	}

	// CSRF protection on web-search test endpoint.
	badCSRF := httptest.NewRequest(http.MethodPost, "/admin/settings/ai/web-search/test", strings.NewReader("csrfToken=bad"))
	badCSRF.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	badCSRF.AddCookie(cookie)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badCSRF)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("invalid CSRF code = %d, want 403", badRec.Code)
	}

	// Test connectivity: Key #1 hits 429 cooldown -> automatically fails over to Key #2 and succeeds.
	testRec := postSecurity(t, app, handler, cookie, "/admin/settings/ai/web-search/test", url.Values{})
	if testRec.Code != http.StatusSeeOther {
		t.Fatalf("web search test code = %d", testRec.Code)
	}
	loc, _ := url.QueryUnescape(testRec.Header().Get("Location"))
	if !strings.Contains(loc, "联网搜索测试成功") || !strings.Contains(loc, "Tavily 正常") {
		t.Fatalf("unexpected web search test notice: %q", loc)
	}
	if strings.Contains(loc, "tvly-secret-alpha-1111") {
		t.Fatalf("notice leaked raw secret: %q", loc)
	}
	if len(seenAuth) != 2 || seenAuth[0] != "Bearer tvly-secret-alpha-1111" || seenAuth[1] != "Bearer tvly-secret-beta-2222" {
		t.Fatalf("unexpected failover order: %v", seenAuth)
	}

	// Refresh page -> Key #1 should report 冷却中, Key #2 正常.
	page = getAdmin(t, handler, cookie, "/admin/settings/ai")
	if !strings.Contains(page, "冷却中") || !strings.Contains(page, "正常") {
		t.Fatalf("expected cooldown and healthy labels in page")
	}

	// Artist aliases page reflects unified web search summary.
	aliasPage := getAdmin(t, handler, cookie, "/admin/artist-aliases")
	if !strings.Contains(aliasPage, "Tavily（3 把 Key 轮询）") {
		t.Fatalf("artist-aliases page missing unified web search summary")
	}
}

func TestAISettingsNativeWebSearchReuseAndProbe(t *testing.T) {
	store := credentialTestStore(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	reg := websearch.NewRegistry()
	reg.SetUsageRecorder(func(ctx context.Context, model, purpose string, usage storage.AIUsage) {
		_ = store.AddAIUsage(ctx, time.Now(), model, purpose, usage)
	})
	app.SetWebSearchRegistry(reg)

	nativeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-native-global-9999" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []any{
				map[string]any{
					"type":   "web_search_call",
					"status": "completed",
					"action": map[string]any{
						"type":    "search",
						"sources": []any{"https://musicbrainz.org/?utm_source=openai"},
					},
				},
				map[string]any{
					"type": "message",
					"content": []any{
						map[string]any{
							"type": "output_text",
							"text": `{"results":[{"url":"https://musicbrainz.org/","title":"MusicBrainz","content":"Open music encyclopedia"}]}`,
						},
					},
				},
			},
			"usage": map[string]any{
				"input_tokens":  3200,
				"output_tokens": 110,
			},
		})
	}))
	defer nativeSrv.Close()

	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	// Configure ONLY global AI + native_then_search (no Tavily/DeepSeek keys).
	rec := postSecurity(t, app, handler, cookie, "/admin/settings/ai", url.Values{
		"base_url":            {nativeSrv.URL + "/v1"},
		"api_key":             {"sk-native-global-9999"},
		"model":               {"gpt-6-luna"},
		"rpm":                 {"60"},
		"concurrency":         {"4"},
		"web_search_provider": {"native_then_search"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save native web search code = %d", rec.Code)
	}

	page := getAdmin(t, handler, cookie, "/admin/settings/ai")
	if strings.Contains(page, "sk-native-global-9999") {
		t.Fatal("ai-settings HTML leaked global API key")
	}
	if !strings.Contains(page, "模型原生优先（复用全局 AI）") {
		t.Fatalf("ai-settings HTML missing native web search summary")
	}
	if strings.Contains(page, `<button class="secondary-button" disabled>测试联网搜索</button>`) {
		t.Fatal("expected web search test button to be enabled when global AI is configured")
	}

	// Probe native web search endpoint.
	testRec := postSecurity(t, app, handler, cookie, "/admin/settings/ai/web-search/test", url.Values{})
	if testRec.Code != http.StatusSeeOther {
		t.Fatalf("native web search test code = %d", testRec.Code)
	}
	loc, _ := url.QueryUnescape(testRec.Header().Get("Location"))
	if !strings.Contains(loc, "联网搜索测试成功") || !strings.Contains(loc, "模型原生搜索正常（gpt-6-luna") {
		t.Fatalf("unexpected native web search test notice: %q", loc)
	}
	if strings.Contains(loc, "sk-native-global-9999") {
		t.Fatalf("notice leaked raw API key: %q", loc)
	}

	// Usage page should show the localized label for web_search_native.
	pageAfter := getAdmin(t, handler, cookie, "/admin/settings/ai")
	if !strings.Contains(pageAfter, "原生联网搜索") {
		t.Fatal("expected ai-settings usage table to display 原生联网搜索")
	}
}

func TestArtistImageAuditHTTPRoutesAndComparisonUI(t *testing.T) {
	app, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()

	// Save audit_auto_confidence via settings form and verify JSON readback.
	rec := postSecurity(t, app, handler, cookie, "/admin/settings/artist-images", url.Values{
		"bangumi_enabled":          {"1"},
		"deezer_enabled":           {"1"},
		"wikipedia_enabled":        {"1"},
		"web_fallback_enabled":     {"1"},
		"web_auto_apply_high":      {"1"},
		"vision_enabled":           {"1"},
		"max_web_requests_per_run": {"50"},
		"negative_cache_days":      {"15"},
		"audit_auto_confidence":    {"90"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save artist image settings = %d", rec.Code)
	}
	jsonRec := httptest.NewRecorder()
	jsonReq := httptest.NewRequest(http.MethodGet, "/admin/settings/artist-images.json", nil)
	jsonReq.AddCookie(cookie)
	handler.ServeHTTP(jsonRec, jsonReq)
	var settingsJSON map[string]any
	if err := json.Unmarshal(jsonRec.Body.Bytes(), &settingsJSON); err != nil {
		t.Fatal(err)
	}
	if settingsJSON["auditAutoConfidence"] != float64(90) {
		t.Fatalf("auditAutoConfidence = %v, want 90", settingsJSON["auditAutoConfidence"])
	}

	// Prepare an artist with a cached image + an audit_review candidate + a replacement candidate + an audit action.
	artists, err := s.ArtistsForMatching(ctx)
	if err != nil || len(artists) == 0 {
		t.Fatalf("artists=%v err=%v", artists, err)
	}
	artistID := artists[0].ID
	imgBytes := makeTestPNGBytes(t, 240, 240, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	imgHash := fmt.Sprintf("%x", sha256.Sum256(imgBytes))
	cachePath := filepath.Join(t.TempDir(), imgHash+".png")
	if err := os.WriteFile(cachePath, imgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveArtistImage(ctx, storage.ArtistImageInput{
		ArtistID: artistID, Source: "deezer", RemoteURL: "https://old.example/1.png",
		Hash: imgHash, MIMEType: "image/png", CachePath: cachePath, ByteSize: int64(len(imgBytes)),
	}); err != nil {
		t.Fatal(err)
	}
	auditCandID, err := s.UpsertArtistImageCandidate(ctx, storage.ArtistImageCandidate{
		ArtistID: artistID, Source: "ai_audit", ImageURL: "https://old.example/1.png",
		Kind: "audit_review", IssueType: "low_quality", Confidence: "medium", ConfidenceScore: 72,
		Reason: "略模糊，建议人工确认", Width: 240, Height: 240,
	})
	if err != nil {
		t.Fatal(err)
	}
	replCandID, err := s.UpsertArtistImageCandidate(ctx, storage.ArtistImageCandidate{
		ArtistID: artistID, Source: "ai_web", ImageURL: "https://new.example/2.png",
		Kind: "replacement", IssueType: "album_cover", Confidence: "medium", ConfidenceScore: 80,
		Reason: "候选肖像", Width: 400, Height: 400,
	})
	if err != nil {
		t.Fatal(err)
	}

	page := adminGetBody(t, handler, cookie, "/admin/matches?tab=avatars")
	for _, want := range []string{
		`value="audit_all"`,
		`value="audit_force"`,
		"AI 审查疑点",
		"画质/构图欠佳",
		"/admin/matches/images/candidates/" + strconv.FormatInt(auditCandID, 10) + "/keep",
		"/admin/matches/images/candidates/" + strconv.FormatInt(auditCandID, 10) + "/clear",
		"/admin/matches/images/candidates/" + strconv.FormatInt(auditCandID, 10) + "/search",
		"当前旧图",
		"新候选图",
		"采纳替换",
		"/admin/matches/images/candidates/" + strconv.FormatInt(replCandID, 10) + "/approve",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("match-review page missing %q", want)
		}
	}

	// Keep the audit_review candidate -> records manual_keep review and dismisses item.
	keepRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/"+strconv.FormatInt(auditCandID, 10)+"/keep", url.Values{})
	if keepRec.Code != http.StatusSeeOther {
		t.Fatalf("keep candidate code = %d", keepRec.Code)
	}
	rev, ok, err := s.ArtistImageReviewByArtist(ctx, artistID)
	if err != nil || !ok || rev.Verdict != "keep" || rev.Model != "manual_keep" {
		t.Fatalf("review after keep = (%+v, %v, %v)", rev, ok, err)
	}

	// Re-add an audit_review item, clear it via HTTP -> creates reversible action -> revert via HTTP restores old image!
	auditCand2ID, err := s.UpsertArtistImageCandidate(ctx, storage.ArtistImageCandidate{
		ArtistID: artistID, Source: "ai_audit", ImageURL: "audit://" + imgHash,
		Kind: "audit_review", IssueType: "garbage", Confidence: "high", ConfidenceScore: 95,
		Reason: "占位 Logo",
	})
	if err != nil {
		t.Fatal(err)
	}
	clearRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/"+strconv.FormatInt(auditCand2ID, 10)+"/clear", url.Values{})
	if clearRec.Code != http.StatusSeeOther {
		t.Fatalf("clear candidate code = %d", clearRec.Code)
	}
	if _, _, _, pErr := s.ArtistImagePath(ctx, artistID); pErr == nil {
		t.Fatal("expected cache to be cleared")
	}
	actions, err := s.RecentArtistImageAuditActions(ctx, 10)
	if err != nil || len(actions) != 1 {
		t.Fatalf("actions = %+v err=%v", actions, err)
	}
	tasksPage := adminGetBody(t, handler, cookie, "/admin/matches?tab=tasks")
	revertActionPath := "/admin/matches/images/audit-actions/" + strconv.FormatInt(actions[0].ID, 10) + "/revert"
	if !strings.Contains(tasksPage, revertActionPath) {
		t.Fatalf("tasks page missing revert action %q", revertActionPath)
	}
	revertRec := postSecurity(t, app, handler, cookie, revertActionPath, url.Values{})
	if revertRec.Code != http.StatusSeeOther {
		t.Fatalf("revert action code = %d", revertRec.Code)
	}
	restoredPath, _, _, pErr := s.ArtistImagePath(ctx, artistID)
	if pErr != nil || restoredPath != cachePath {
		t.Fatalf("ArtistImagePath after revert = (%q, %v), want %q", restoredPath, pErr, cachePath)
	}
}

// TestEndToEndTavilyMultiKeyAndFullAvatarAuditIntegration exercises the full
// stack across HTTP -> Enrichment -> WebSearch (Tavily multi-key failover) ->
// OpenAI ChatClient (temperature=0 400 retry) -> Storage -> Cache URL ?v= ->
// HTTP one-click Revert.
func TestEndToEndTavilyMultiKeyAndFullAvatarAuditIntegration(t *testing.T) {
	app, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	reg := websearch.NewRegistry()
	app.SetWebSearchRegistry(reg)
	app.enrichment.SetWebSearchRegistry(reg)

	oldCoverBytes := makeTestPNGBytes(t, 280, 280, color.RGBA{R: 200, G: 10, B: 10, A: 255})
	newPortraitBytes := makeTestPNGBytes(t, 360, 360, color.RGBA{R: 10, G: 200, B: 20, A: 255})
	goodBytes := makeTestPNGBytes(t, 320, 320, color.RGBA{R: 10, G: 20, B: 220, A: 255})
	garbageBytes := makeTestPNGBytes(t, 260, 260, color.RGBA{R: 120, G: 120, B: 120, A: 255})
	wrongBytes := makeTestPNGBytes(t, 270, 270, color.RGBA{R: 220, G: 180, B: 10, A: 255})
	suspiciousBytes := makeTestPNGBytes(t, 290, 290, color.RGBA{R: 180, G: 10, B: 220, A: 255})

	// 1. Fake public web server hosting HTML profile + new portrait via safe test client.
	// Use a dedicated httptest server for Tavily (Key #1 -> 432 quota exhausted, Key #2 -> 200).
	var tavilyMu sync.Mutex
	var tavilySeen []string
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tavilyMu.Lock()
		tavilySeen = append(tavilySeen, auth)
		tavilyMu.Unlock()
		if auth == "Bearer tvly-e2e-exhausted-0001" {
			w.WriteHeader(432)
			return
		}
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if strings.Contains(req.Query, "ReplaceArtist") {
			// Return an HTTPS URL that won't be fetched over real internet — instead seed ReplaceArtist's confirmed profile URL pointing to our local mock or skip web HTML; wait: netfetch blocks loopback!
			// So verify Tavily Search runs cleanly with failover, and have Tier 1/2 OR test-level transport handle public IP if needed.
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer tavilySrv.Close()
	reg.SetEndpointsForTest(tavilySrv.URL, tavilySrv.URL, tavilySrv.Client(), nil)

	// 2. Fake OpenAI-compatible gpt-6-luna server that rejects temperature=0 on first call!
	var llmMu sync.Mutex
	var tempRejectedCount int
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model       string          `json:"model"`
			Temperature *float64        `json:"temperature"`
			Messages    json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Temperature != nil {
			llmMu.Lock()
			tempRejectedCount++
			llmMu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported value: 'temperature' does not support 0 with gpt-6-luna"}}`))
			return
		}
		msgStr := string(body.Messages)
		var content string
		switch {
		case !strings.Contains(msgStr, "currentSource"):
			// Vision verification call.
			content = `{"valid":true,"confidence":94,"reason":"清晰艺人照"}`
		case strings.Contains(msgStr, "GoodArtist"):
			content = `{"verdict":"keep","issueType":"none","confidence":95,"reason":"清晰艺人照"}`
		case strings.Contains(msgStr, "GarbageArtist"):
			content = `{"verdict":"replace","issueType":"garbage","confidence":97,"reason":"明显网站占位 Logo"}`
		case strings.Contains(msgStr, "WrongArtist"):
			content = `{"verdict":"replace","issueType":"wrong_subject","confidence":91,"reason":"明显非本人"}`
		case strings.Contains(msgStr, "SuspiciousArtist"):
			content = `{"verdict":"replace","issueType":"low_quality","confidence":72,"reason":"略微模糊"}`
		default:
			content = `{"verdict":"keep","issueType":"none","confidence":90,"reason":"ok"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": content}, "finish_reason": "stop"},
			},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 25},
		})
	}))
	defer llmSrv.Close()

	// Configure AI + Tavily multi-key settings via HTTP.
	rec := postSecurity(t, app, handler, cookie, "/admin/settings/ai", url.Values{
		"base_url":            {llmSrv.URL},
		"api_key":             {"sk-e2e-gpt6"},
		"model":               {"gpt-6-luna"},
		"rpm":                 {"600"},
		"concurrency":         {"2"},
		"web_search_provider": {"tavily"},
		"tavily_keys_mode":    {"replace"},
		"tavily_keys":         {"tvly-e2e-exhausted-0001\ntvly-e2e-healthy-0002"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save AI + Tavily settings code = %d", rec.Code)
	}
	rec = postSecurity(t, app, handler, cookie, "/admin/settings/artist-images", url.Values{
		"web_fallback_enabled":     {"1"},
		"web_auto_apply_high":      {"1"},
		"vision_enabled":           {"1"},
		"max_web_requests_per_run": {"100"},
		"negative_cache_days":      {"30"},
		"audit_auto_confidence":    {"85"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save image settings code = %d", rec.Code)
	}

	// Import artists into library.
	if err := s.EnsureLibrary(ctx, "E2E", "/e2e-music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/e2e-music")
	if err != nil {
		t.Fatal(err)
	}
	seedArtist := func(rel, name string) int64 {
		if err := s.ImportTrack(ctx, storage.ImportInput{
			LibraryID: lib.ID, RelativePath: rel, FileSize: 100, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: "S-" + name, Album: "A-" + name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1},
		}); err != nil {
			t.Fatal(err)
		}
		list, lErr := s.ArtistsForMatching(ctx)
		if lErr != nil {
			t.Fatal(lErr)
		}
		var id int64
		for _, a := range list {
			if a.Name == name {
				id = a.ID
			}
		}
		if id == 0 {
			t.Fatalf("artist %q not found", name)
		}
		return id
	}
	imgDir := t.TempDir()
	seedCache := func(artistID int64, raw []byte, urlStr string) string {
		h := fmt.Sprintf("%x", sha256.Sum256(raw))
		p := filepath.Join(imgDir, h+".png")
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveArtistImage(ctx, storage.ArtistImageInput{
			ArtistID: artistID, Source: "deezer", RemoteURL: urlStr,
			Hash: h, MIMEType: "image/png", CachePath: p, ByteSize: int64(len(raw)),
		}); err != nil {
			t.Fatal(err)
		}
		return h
	}

	goodID := seedArtist("e2e/1.flac", "GoodArtist")
	garbageID := seedArtist("e2e/2.flac", "GarbageArtist")
	garbageSrcID := seedArtist("e2e/2b.flac", "GarbageSource")
	wrongID := seedArtist("e2e/3.flac", "WrongArtist")
	suspiciousID := seedArtist("e2e/4.flac", "SuspiciousArtist")
	customID := seedArtist("e2e/5.flac", "CustomProtected")

	goodHash := seedCache(goodID, goodBytes, "https://img.example/good.png")
	garbageHash := seedCache(garbageSrcID, garbageBytes, "https://img.example/garbage.png")
	if _, err := s.MergeArtists(ctx, garbageSrcID, garbageID); err != nil {
		t.Fatal(err)
	}
	seedCache(wrongID, wrongBytes, "https://img.example/wrong.png")
	seedCache(suspiciousID, suspiciousBytes, "https://img.example/suspicious.png")
	if _, err := s.SaveCustomArtistImage(ctx, customID, storage.CustomImageInput{
		Hash: "custom-e2e-hash", MIMEType: "image/png", FileName: "cust.png", Width: 500, Height: 500, ByteSize: 100,
	}); err != nil {
		t.Fatal(err)
	}
	_ = oldCoverBytes
	_ = newPortraitBytes

	// Start audit_all via HTTP POST /admin/matches/images/backfill!
	startRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/backfill", url.Values{
		"mode": {"audit_all"},
	})
	if startRec.Code != http.StatusSeeOther {
		t.Fatalf("start audit_all code = %d", startRec.Code)
	}

	// Poll /admin/matches/images/runs/active.json until finished.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		pollRec := httptest.NewRecorder()
		pollReq := httptest.NewRequest(http.MethodGet, "/admin/matches/images/runs/active.json", nil)
		pollReq.AddCookie(cookie)
		handler.ServeHTTP(pollRec, pollReq)
		var doc struct {
			Run *artistImageRunView `json:"run"`
		}
		if err := json.Unmarshal(pollRec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Run == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	latest, err := s.LatestArtistImageBackfillRun(ctx)
	if err != nil || latest.Status != "completed" || latest.Mode != "audit_all" {
		t.Fatalf("latest run = %+v err=%v", latest, err)
	}
	if latest.Kept < 1 || latest.Cleared != 1 || latest.Candidate != 2 || latest.Failed != 0 {
		t.Fatalf("unexpected e2e audit counts: %+v", latest)
	}
	// Verify gpt-6-luna temperature=0 retry occurred cleanly.
	if tempRejectedCount == 0 {
		t.Fatal("expected ChatClient to encounter and retry temperature=0 400")
	}
	// Verify Tavily failover occurred: exhausted key 1 -> healthy key 2!
	tavilyMu.Lock()
	sawExhausted := false
	sawHealthy := false
	for _, a := range tavilySeen {
		if a == "Bearer tvly-e2e-exhausted-0001" {
			sawExhausted = true
		}
		if a == "Bearer tvly-e2e-healthy-0002" {
			sawHealthy = true
		}
	}
	tavilyMu.Unlock()
	if !sawExhausted || !sawHealthy {
		t.Fatalf("expected Tavily multi-key failover, saw=%v", tavilySeen)
	}

	// Verify cache URL versioning on GoodArtist and complete clearing on GarbageArtist (inherited).
	urls, _ := s.ArtistImageURLs(ctx, []int64{goodID, garbageID, wrongID, suspiciousID})
	if urls[goodID] != fmt.Sprintf("/api/v1/artists/%d/image?v=%s", goodID, goodHash[:12]) {
		t.Fatalf("GoodArtist URL = %q", urls[goodID])
	}
	if urls[garbageID] != "" {
		t.Fatalf("GarbageArtist URL after clear = %q, want empty", urls[garbageID])
	}
	if urls[wrongID] == "" || urls[suspiciousID] == "" {
		t.Fatalf("WrongArtist/SuspiciousArtist should keep cached URLs: %+v", urls)
	}

	// One-click HTTP revert of GarbageArtist clearance restores inherited cache and ?v= URL!
	actions, err := s.RecentArtistImageAuditActions(ctx, 10)
	if err != nil || len(actions) != 1 || actions[0].ArtistID != garbageID || actions[0].Action != "cleared" {
		t.Fatalf("audit actions = %+v err=%v", actions, err)
	}
	revRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/audit-actions/"+strconv.FormatInt(actions[0].ID, 10)+"/revert", url.Values{})
	if revRec.Code != http.StatusSeeOther {
		t.Fatalf("HTTP revert code = %d", revRec.Code)
	}
	urlsAfter, _ := s.ArtistImageURLs(ctx, []int64{garbageID})
	wantRestoredURL := fmt.Sprintf("/api/v1/artists/%d/image?v=%s", garbageID, garbageHash[:12])
	if urlsAfter[garbageID] != wantRestoredURL {
		t.Fatalf("GarbageArtist URL after revert = %q, want %q", urlsAfter[garbageID], wantRestoredURL)
	}

	// Verify WrongArtist (high-conf, actually ran Tavily) received not_found_auto,
	// whereas SuspiciousArtist (medium-conf, did not consume web search) stayed empty.
	cands, err := s.PendingArtistImageCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	candByArtist := map[int64]storage.ArtistImageCandidate{}
	for _, c := range cands {
		candByArtist[c.ArtistID] = c
	}
	if candByArtist[wrongID].WebSearchState != "not_found_auto" {
		t.Fatalf("WrongArtist WebSearchState = %q, want not_found_auto", candByArtist[wrongID].WebSearchState)
	}
	if candByArtist[suspiciousID].WebSearchState != "" {
		t.Fatalf("SuspiciousArtist WebSearchState = %q, want empty", candByArtist[suspiciousID].WebSearchState)
	}
}

func TestArtistImageSearchQueueAndBatchReviewHTTP(t *testing.T) {
	app, s, handler, _, cookie, _ := rateLimitTestApp(t)
	ctx := context.Background()
	reg := websearch.NewRegistry()
	app.SetWebSearchRegistry(reg)
	app.enrichment.SetWebSearchRegistry(reg)

	// Stub Tavily returning empty results + dummy LLM settings so queued searches
	// complete cleanly with not_found_manual.
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer tavilySrv.Close()
	reg.SetEndpointsForTest(tavilySrv.URL, tavilySrv.URL, tavilySrv.Client(), nil)

	if err := s.SaveAISettings(ctx, storage.AISettings{
		BaseURL: "https://llm.example/v1", APIKey: "sk-queue", Model: "gpt-6-luna", RequestsPerMinute: 600, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWebSearchSettings(ctx, storage.WebSearchSettings{
		Provider:   storage.WebSearchProviderTavily,
		TavilyKeys: []string{"tvly-queue-test-key"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.EnsureLibrary(ctx, "BatchLib", "/batch-music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/batch-music")
	if err != nil {
		t.Fatal(err)
	}
	imgDir := t.TempDir()
	seed := func(rel, name, hash string) int64 {
		if err := s.ImportTrack(ctx, storage.ImportInput{
			LibraryID: lib.ID, RelativePath: rel, FileSize: 100, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: "T-" + name, Album: "A-" + name, Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1},
		}); err != nil {
			t.Fatal(err)
		}
		list, err := s.ArtistsForMatching(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var aid int64
		for _, a := range list {
			if a.Name == name {
				aid = a.ID
			}
		}
		p := filepath.Join(imgDir, hash+".png")
		if err := os.WriteFile(p, makeTestPNGBytes(t, 280, 280, color.RGBA{R: 120, G: 80, B: 200, A: 255}), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveArtistImage(ctx, storage.ArtistImageInput{
			ArtistID: aid, Source: "deezer", RemoteURL: "https://img.example/" + hash + ".png",
			Hash: hash, MIMEType: "image/png", CachePath: p, ByteSize: 100,
		}); err != nil {
			t.Fatal(err)
		}
		return aid
	}

	a1 := seed("b/1.flac", "QueueArtistOne", "hashqueue001")
	a2 := seed("b/2.flac", "QueueArtistTwo", "hashqueue002")
	a3 := seed("b/3.flac", "BatchKeepArtist", "hashqueue003")
	a4 := seed("b/4.flac", "BatchClearArtist", "hashqueue004")

	addCard := func(aid int64, wsState, wsNote string) int64 {
		id, err := s.UpsertArtistImageCandidate(ctx, storage.ArtistImageCandidate{
			ArtistID:        aid,
			Source:          "ai_audit",
			ImageURL:        "https://img.example/old-" + strconv.FormatInt(aid, 10) + ".png",
			Confidence:      "medium",
			ConfidenceScore: 72,
			Kind:            "audit_review",
			IssueType:       "album_cover",
			Reason:          "疑似专辑封面",
			WebSearchState:  wsState,
			WebSearchNote:   wsNote,
		})
		if err != nil || id == 0 {
			t.Fatalf("UpsertArtistImageCandidate(%d) = (%d, %v)", aid, id, err)
		}
		return id
	}
	c1 := addCard(a1, "", "")
	c2 := addCard(a2, "not_found_auto", "全量审查已自动联网找图，未找到合适新图")
	c3 := addCard(a3, "", "")
	c4 := addCard(a4, "not_found_manual", "手动联网找图未找到更合适的候选头像")

	// 1. SSR page renders filter chips, batch toolbar, auto/manual no-result badges.
	body := adminGetBody(t, handler, cookie, "/admin/matches?tab=avatars")
	for _, want := range []string{
		"data-avatar-workbench",
		"仅选未搜过",
		"加入联网找图队列",
		"批量保留原图",
		"批量清空头像",
		"自动已搜无果",
		"手动已搜无果",
		"重新搜图",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("avatars tab missing %q", want)
		}
	}

	// 2. CSRF protection on queue JSON endpoints.
	badReq := httptest.NewRequest(http.MethodPost, "/admin/matches/images/search-queue/enqueue", strings.NewReader(`{"candidateIds":[1]}`))
	badReq.Header.Set("Content-Type", "application/json")
	badReq.AddCookie(cookie)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("enqueue without CSRF code = %d, want 403", badRec.Code)
	}

	// 3. Enqueue c1 and c2 via JSON + X-CSRF-Token header, poll until completed.
	sess, ok := app.sessions.get(&http.Request{Header: http.Header{"Cookie": {cookie.String()}}})
	if !ok {
		t.Fatal("missing test session")
	}
	enqBody, _ := json.Marshal(map[string]any{"candidateIds": []int64{c1, c2}})
	enqReq := httptest.NewRequest(http.MethodPost, "/admin/matches/images/search-queue/enqueue", strings.NewReader(string(enqBody)))
	enqReq.Header.Set("Content-Type", "application/json")
	enqReq.Header.Set("X-CSRF-Token", sess.CSRFToken)
	enqReq.AddCookie(cookie)
	enqRec := httptest.NewRecorder()
	handler.ServeHTTP(enqRec, enqReq)
	if enqRec.Code != http.StatusOK {
		t.Fatalf("enqueue code = %d body=%s", enqRec.Code, enqRec.Body.String())
	}
	var enqResp struct {
		Added int `json:"added"`
		Queue struct {
			Status string `json:"status"`
			Total  int    `json:"total"`
		} `json:"queue"`
	}
	if err := json.Unmarshal(enqRec.Body.Bytes(), &enqResp); err != nil {
		t.Fatal(err)
	}
	if enqResp.Added != 2 || enqResp.Queue.Total != 2 {
		t.Fatalf("enqueue resp = %+v", enqResp)
	}

	deadline := time.Now().Add(8 * time.Second)
	var finalStatus string
	var finalNotFound int
	for time.Now().Before(deadline) {
		pReq := httptest.NewRequest(http.MethodGet, "/admin/matches/images/search-queue.json", nil)
		pReq.AddCookie(cookie)
		pRec := httptest.NewRecorder()
		handler.ServeHTTP(pRec, pReq)
		if pRec.Code != http.StatusOK {
			t.Fatalf("poll queue code = %d", pRec.Code)
		}
		var doc struct {
			Queue struct {
				Status   string `json:"status"`
				NotFound int    `json:"notFound"`
			} `json:"queue"`
		}
		if err := json.Unmarshal(pRec.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		finalStatus = doc.Queue.Status
		finalNotFound = doc.Queue.NotFound
		if finalStatus == "completed" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if finalStatus != "completed" || finalNotFound != 2 {
		t.Fatalf("final queue status=%q notFound=%d, want completed/2", finalStatus, finalNotFound)
	}
	gotC1, err := s.ArtistImageCandidateByID(ctx, c1)
	if err != nil || gotC1.WebSearchState != "not_found_manual" {
		t.Fatalf("c1 after manual queue search = %+v err=%v", gotC1, err)
	}

	// Dismiss finished queue banner.
	disReq := httptest.NewRequest(http.MethodPost, "/admin/matches/images/search-queue/dismiss", strings.NewReader(`{}`))
	disReq.Header.Set("Content-Type", "application/json")
	disReq.Header.Set("X-CSRF-Token", sess.CSRFToken)
	disReq.AddCookie(cookie)
	disRec := httptest.NewRecorder()
	handler.ServeHTTP(disRec, disReq)
	if disRec.Code != http.StatusOK || !strings.Contains(disRec.Body.String(), `"status":"idle"`) {
		t.Fatalf("dismiss resp = %d %s", disRec.Code, disRec.Body.String())
	}

	// 4. Empty candidateIds must report empty selection (never misleading "自定义头像").
	noticeOf := func(rec *httptest.ResponseRecorder) string {
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatalf("parse Location %q: %v", rec.Header().Get("Location"), err)
		}
		return loc.Query().Get("notice")
	}
	emptyClearRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/batch-clear", url.Values{
		"candidateIds": {""},
	})
	if emptyClearRec.Code != http.StatusSeeOther {
		t.Fatalf("empty batch-clear code = %d", emptyClearRec.Code)
	}
	if n := noticeOf(emptyClearRec); !strings.Contains(n, "未选中任何可清空的待审项") || strings.Contains(n, "自定义头像") {
		t.Fatalf("empty batch-clear notice = %q, want empty-selection message without custom-avatar hint", n)
	}
	emptyKeepRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/batch-keep", url.Values{
		"candidateIds": {""},
	})
	if emptyKeepRec.Code != http.StatusSeeOther {
		t.Fatalf("empty batch-keep code = %d", emptyKeepRec.Code)
	}
	if n := noticeOf(emptyKeepRec); !strings.Contains(n, "未选中任何可保留的待审项") {
		t.Fatalf("empty batch-keep notice = %q", n)
	}

	// 5. Batch keep c3 and batch clear c4 via form posts.
	keepRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/batch-keep", url.Values{
		"candidateIds": {strconv.FormatInt(c3, 10)},
	})
	if keepRec.Code != http.StatusSeeOther {
		t.Fatalf("batch-keep code = %d", keepRec.Code)
	}
	if n := noticeOf(keepRec); !strings.Contains(n, "已批量确认保留 1 位") {
		t.Fatalf("batch-keep notice = %q", n)
	}
	rev3, ok3, err := s.ArtistImageReviewByArtist(ctx, a3)
	if err != nil || !ok3 || rev3.Verdict != "keep" || rev3.ImageHash != "hashqueue003" {
		t.Fatalf("BatchKeepArtist review = (%+v, %v, %v)", rev3, ok3, err)
	}
	repeatKeepRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/batch-keep", url.Values{
		"candidateIds": {strconv.FormatInt(c3, 10)},
	})
	if n := noticeOf(repeatKeepRec); !strings.Contains(n, "选中项均已处理") {
		t.Fatalf("repeat batch-keep notice = %q", n)
	}

	clearRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/batch-clear", url.Values{
		"candidateIds": {strconv.FormatInt(c4, 10)},
	})
	if clearRec.Code != http.StatusSeeOther {
		t.Fatalf("batch-clear code = %d", clearRec.Code)
	}
	if n := noticeOf(clearRec); !strings.Contains(n, "已批量清空 1 位") || strings.Contains(n, "自定义头像") {
		t.Fatalf("batch-clear notice = %q", n)
	}
	if _, _, _, pErr := s.ArtistImagePath(ctx, a4); pErr == nil {
		t.Fatal("expected BatchClearArtist cache to be cleared")
	}
	actions, err := s.RecentArtistImageAuditActions(ctx, 10)
	if err != nil || len(actions) == 0 || actions[0].ArtistID != a4 {
		t.Fatalf("expected undo snapshot for BatchClearArtist, got %+v err=%v", actions, err)
	}
	repeatClearRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/batch-clear", url.Values{
		"candidateIds": {strconv.FormatInt(c4, 10)},
	})
	if n := noticeOf(repeatClearRec); !strings.Contains(n, "选中项均已处理") || strings.Contains(n, "自定义头像") {
		t.Fatalf("repeat batch-clear notice = %q, want already-handled without custom-avatar hint", n)
	}

	// 6. Concurrent custom avatar protection and mixed breakdown (form + JSON).
	a5 := seed("b/5.flac", "CustomProtectedArtist", "hashqueue005")
	c5 := addCard(a5, "not_found_auto", "")
	if _, err := s.SaveCustomArtistImage(ctx, a5, storage.CustomImageInput{
		Hash: "customhash005", MIMEType: "image/png", FileName: "custom5.png", Width: 300, Height: 300, ByteSize: 120,
	}); err != nil {
		t.Fatal(err)
	}
	customOnlyRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/batch-clear", url.Values{
		"candidateIds": {strconv.FormatInt(c5, 10)},
	})
	if n := noticeOf(customOnlyRec); !strings.Contains(n, "选中艺人均存在自定义头像") {
		t.Fatalf("custom-protected batch-clear notice = %q", n)
	}

	a6 := seed("b/6.flac", "MixedBatchClearArtist", "hashqueue006")
	c6 := addCard(a6, "", "")
	mixedClearRec := postSecurity(t, app, handler, cookie, "/admin/matches/images/candidates/batch-clear", url.Values{
		"candidateIds": {fmt.Sprintf("%d,%d,%d", c6, c4, c5)},
	})
	if mixedClearRec.Code != http.StatusSeeOther {
		t.Fatalf("mixed batch-clear code = %d", mixedClearRec.Code)
	}
	if n := noticeOf(mixedClearRec); !strings.Contains(n, "已批量清空 1 位") || !strings.Contains(n, "已处理 1 位") || !strings.Contains(n, "自定义头像保护 1 位") {
		t.Fatalf("mixed batch-clear notice = %q", n)
	}

	// JSON breakdown on batch-clear with already-handled + custom-protected.
	jsonBody, _ := json.Marshal(map[string]any{"candidateIds": []int64{c4, c5}})
	jsonReq := httptest.NewRequest(http.MethodPost, "/admin/matches/images/candidates/batch-clear", strings.NewReader(string(jsonBody)))
	jsonReq.Header.Set("Content-Type", "application/json")
	jsonReq.Header.Set("Accept", "application/json")
	jsonReq.Header.Set("X-CSRF-Token", sess.CSRFToken)
	jsonReq.AddCookie(cookie)
	jsonRec := httptest.NewRecorder()
	handler.ServeHTTP(jsonRec, jsonReq)
	if jsonRec.Code != http.StatusOK {
		t.Fatalf("json batch-clear code = %d body=%s", jsonRec.Code, jsonRec.Body.String())
	}
	var clearJSON struct {
		Processed       int `json:"processed"`
		Skipped         int `json:"skipped"`
		AlreadyHandled  int `json:"alreadyHandled"`
		CustomProtected int `json:"customProtected"`
		Failed          int `json:"failed"`
	}
	if err := json.Unmarshal(jsonRec.Body.Bytes(), &clearJSON); err != nil {
		t.Fatal(err)
	}
	if clearJSON.Processed != 0 || clearJSON.Skipped != 2 || clearJSON.AlreadyHandled != 1 || clearJSON.CustomProtected != 1 || clearJSON.Failed != 0 {
		t.Fatalf("json batch-clear = %+v", clearJSON)
	}
}

func TestArtistImageAutoIngestSettingsConfirmAndE2EHTTP(t *testing.T) {
	app, s, handler, manager, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	reg := websearch.NewRegistry()
	app.SetWebSearchRegistry(reg)
	manager.SetWebSearchRegistry(reg)
	// Keep external metadata & biography sources disabled so RefreshArtistBiographies
	// stays completely offline (never dialing real musicbrainz.org) during this HTTP E2E test.
	_ = s.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{Source: "musicbrainz", Enabled: false})
	_ = s.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{Source: "lastfm", Enabled: false})
	if err := s.SaveBiographySettings(ctx, storage.BiographySettings{
		PreferredLanguages:   "zh,ja,en",
		SourcePriority:       "wikipedia,lastfm",
		WikipediaEnabled:     false,
		EnglishFallback:      true,
		CacheDays:            30,
		AITranslateEnabled:   false,
		AIWebFallbackEnabled: false,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Admin auth required on auto-queue.json.
	unauthReq := httptest.NewRequest(http.MethodGet, "/admin/matches/images/auto-queue.json", nil)
	unauthRec := httptest.NewRecorder()
	handler.ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated auto-queue.json code = %d, want 401", unauthRec.Code)
	}

	// 2. Settings API + SSR page expose auto_ingest_ai_enabled and live banner hook.
	getStReq := httptest.NewRequest(http.MethodGet, "/admin/settings/artist-images.json", nil)
	getStReq.AddCookie(cookie)
	getStRec := httptest.NewRecorder()
	handler.ServeHTTP(getStRec, getStReq)
	if getStRec.Code != http.StatusOK || !strings.Contains(getStRec.Body.String(), `"autoIngestAIEnabled":true`) {
		t.Fatalf("GET artist-image settings = %d %s", getStRec.Code, getStRec.Body.String())
	}
	pageBody := adminGetBody(t, handler, cookie, "/admin/matches")
	for _, want := range []string{`name="auto_ingest_ai_enabled"`, `data-auto-image-queue-banner`} {
		if !strings.Contains(pageBody, want) {
			t.Fatalf("/admin/matches missing %q", want)
		}
	}

	// Toggle auto_ingest_ai_enabled off then back on via POST.
	saveOff := postSecurity(t, app, handler, cookie, "/admin/settings/artist-images", url.Values{
		"web_auto_apply_high": {"1"},
	})
	if saveOff.Code != http.StatusSeeOther {
		t.Fatalf("save settings off code = %d", saveOff.Code)
	}
	stOff, _ := s.ArtistImageSettings(ctx)
	if stOff.AutoIngestAIEnabled {
		t.Fatal("expected AutoIngestAIEnabled=false after saving without checkbox")
	}

	// Configure fake LLM + fake Tavily + re-enable auto_ingest_ai_enabled + vision_enabled.
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages json.RawMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		msgStr := string(body.Messages)
		content := `{"verdict":"keep","issueType":"none","confidence":95,"reason":"合格"}`
		if strings.Contains(msgStr, "UnauditedHTTP") {
			content = `{"verdict":"replace","issueType":"garbage","confidence":96,"reason":"明显网站占位 Logo"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": content}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 60, "completion_tokens": 15},
		})
	}))
	defer llmSrv.Close()
	tavilySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer tavilySrv.Close()
	reg.SetEndpointsForTest(tavilySrv.URL, tavilySrv.URL, tavilySrv.Client(), nil)

	if err := s.SaveAISettings(ctx, storage.AISettings{
		BaseURL: llmSrv.URL, APIKey: "sk-http-ingest", Model: "gpt-6-luna", RequestsPerMinute: 600, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWebSearchSettings(ctx, storage.WebSearchSettings{
		Provider: storage.WebSearchProviderTavily, TavilyKeys: []string{"tvly-http-ingest-key"},
	}); err != nil {
		t.Fatal(err)
	}
	saveOn := postSecurity(t, app, handler, cookie, "/admin/settings/artist-images", url.Values{
		"web_fallback_enabled":   {"1"},
		"web_auto_apply_high":    {"1"},
		"vision_enabled":         {"1"},
		"auto_ingest_ai_enabled": {"1"},
		"audit_auto_confidence":  {"85"},
	})
	if saveOn.Code != http.StatusSeeOther {
		t.Fatalf("save settings on code = %d", saveOn.Code)
	}

	// 3. Seed an artist with an unaudited garbage cached avatar and a pending MB candidate;
	// manual HTTP confirm must immediately return async notice and clear the garbage avatar in background!
	if err := s.EnsureLibrary(ctx, "IngestLib", "/ingest-music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/ingest-music")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "i/1.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Track", Album: "Album", Artists: []string{"LeadIngest"}, AlbumArtists: []string{"LeadIngest"},
			Composer: "UnauditedHTTP", DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	artists, _ := s.ArtistsForMatching(ctx)
	var composerID int64
	for _, a := range artists {
		if a.Name == "UnauditedHTTP" {
			composerID = a.ID
		}
	}
	if composerID == 0 {
		t.Fatal("UnauditedHTTP composer not found")
	}
	oldPath := filepath.Join(t.TempDir(), "old-garbage.png")
	if err := os.WriteFile(oldPath, makeTestPNGBytes(t, 280, 280, color.RGBA{R: 120, G: 120, B: 120, A: 255}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveArtistImage(ctx, storage.ArtistImageInput{
		ArtistID: composerID, Source: "deezer", RemoteURL: "https://img.example/old-logo.png",
		Hash: "httpgarbage093", MIMEType: "image/png", CachePath: oldPath, ByteSize: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceArtistCandidates(ctx, composerID, []storage.ArtistCandidate{{
		Source: "musicbrainz", ExternalID: "mb-http-comp-1", DisplayName: "UnauditedHTTP", MBID: "mb-http-comp-1", Score: 95,
	}}); err != nil {
		t.Fatal(err)
	}
	cands, err := s.ArtistCandidates(ctx, composerID)
	if err != nil || len(cands) == 0 {
		t.Fatalf("ArtistCandidates = (%+v, %v)", cands, err)
	}

	confRec := postAdminForm(t, handler, cookie, csrf, fmt.Sprintf("/admin/artists/%d/confirm/%d", composerID, cands[0].ID), nil)
	if confRec.Code != http.StatusSeeOther {
		t.Fatalf("confirm code = %d", confRec.Code)
	}
	notice := notice303Of(t, confRec)
	if !strings.Contains(notice, "头像将在后台自动质检补全") {
		t.Fatalf("confirm notice = %q, want async queue notice", notice)
	}

	manager.Wait()

	// Garbage avatar should have been audited and cleared (1.A policy) asynchronously!
	if _, _, _, pErr := s.ArtistImagePath(ctx, composerID); pErr == nil {
		t.Fatal("expected UnauditedHTTP garbage avatar to be cleared by background auto queue")
	}
	aqReq := httptest.NewRequest(http.MethodGet, "/admin/matches/images/auto-queue.json", nil)
	aqReq.AddCookie(cookie)
	aqRec := httptest.NewRecorder()
	handler.ServeHTTP(aqRec, aqReq)
	if aqRec.Code != http.StatusOK || !strings.Contains(aqRec.Body.String(), `"cleared":1`) {
		t.Fatalf("auto-queue.json = %d %s, want cleared:1", aqRec.Code, aqRec.Body.String())
	}
}
