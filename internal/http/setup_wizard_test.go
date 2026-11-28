package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

// newFreshSetupApp builds an App backed by a brand-new migrated database whose
// setup_wizard_state starts in the natural 'pending' state, plus an active
// admin session cookie and CSRF token.
func newFreshSetupApp(t *testing.T, modifyCfg func(*config.Config)) (*App, *storage.Store, *http.Cookie, string) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "setup-wizard.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	musicDir := t.TempDir()
	albumDir := filepath.Join(musicDir, "Artist - Album")
	if err := os.MkdirAll(albumDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(albumDir, "01.flac"), []byte("fLaC"), 0o644); err != nil {
		t.Fatal(err)
	}

	token := "setup-test-token-at-least-24-chars"
	cfg := config.Config{
		APIToken:       token,
		MediaToken:     token,
		CookieSecure:   false,
		MusicDirectory: musicDir,
		DataDirectory:  t.TempDir(),
		LibraryName:    "Test Library",
		WatchInterval:  config.DefaultWatchInterval,
	}
	if modifyCfg != nil {
		modifyCfg(&cfg)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(cfg, store, nil, nil, logger, "1.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	loginRec := httptest.NewRecorder()
	sess, err := app.sessions.create(loginRec, "admin")
	if err != nil {
		t.Fatal(err)
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("missing session cookie")
	}
	return app, store, cookies[0], sess.CSRFToken
}

func setupJSONReq(t *testing.T, handler http.Handler, method, path string, body any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func extractSetupBootstrap(t *testing.T, html string) setupBootstrapPayload {
	t.Helper()
	re := regexp.MustCompile(`<script type="application/json" id="setup-bootstrap">([\s\S]*?)</script>`)
	m := re.FindStringSubmatch(html)
	if len(m) != 2 {
		t.Fatalf("missing setup-bootstrap script block in HTML")
	}
	var payload setupBootstrapPayload
	if err := json.Unmarshal([]byte(m[1]), &payload); err != nil {
		t.Fatalf("unmarshal bootstrap JSON: %v\nraw: %s", err, m[1])
	}
	return payload
}

func TestSetupWizardRedirectAndAuth(t *testing.T) {
	app, store, cookie, _ := newFreshSetupApp(t, nil)
	handler := app.Handler()

	// Unauthenticated requests are gated.
	unauthPage := httptest.NewRequest(http.MethodGet, "/admin/setup", nil)
	unauthPage.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, unauthPage)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/login") {
		t.Fatalf("unauth GET /admin/setup = %d %q, want 303 /admin/login", rec.Code, rec.Header().Get("Location"))
	}

	unauthAPI := setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/1", map[string]any{"skip": true}, nil, "")
	if unauthAPI.Code != http.StatusUnauthorized {
		t.Fatalf("unauth POST /admin/setup/step/1 = %d, want 401", unauthAPI.Code)
	}

	// Authenticated fresh pending instance redirects /admin and /admin/home -> /admin/setup.
	for _, landing := range []string{"/admin", "/admin/home"} {
		req := httptest.NewRequest(http.MethodGet, landing, nil)
		req.AddCookie(cookie)
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, req)
		if r.Code != http.StatusSeeOther || r.Header().Get("Location") != "/admin/setup" {
			t.Fatalf("pending GET %s = %d %q, want 303 /admin/setup", landing, r.Code, r.Header().Get("Location"))
		}
	}

	// Non-landing admin deep links stay directly reachable while pending.
	deepReq := httptest.NewRequest(http.MethodGet, "/admin/settings/security", nil)
	deepReq.AddCookie(cookie)
	deepRec := httptest.NewRecorder()
	handler.ServeHTTP(deepRec, deepReq)
	if deepRec.Code != http.StatusOK {
		t.Fatalf("pending GET /admin/settings/security = %d, want 200", deepRec.Code)
	}

	// After completion, landing routes render normally.
	if err := store.MarkSetupWizardCompleted(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, landing := range []string{"/admin", "/admin/home"} {
		req := httptest.NewRequest(http.MethodGet, landing, nil)
		req.AddCookie(cookie)
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, req)
		if r.Code != http.StatusOK {
			t.Fatalf("completed GET %s = %d, want 200", landing, r.Code)
		}
	}
}

func TestSetupWizardPageAndAlerts(t *testing.T) {
	ctx := context.Background()
	app, store, cookie, _ := newFreshSetupApp(t, func(cfg *config.Config) {
		cfg.MediaTokenGenerated = true
	})
	handler := app.Handler()

	// Seed secret-bearing settings to confirm the setup page never leaks secrets.
	_ = store.SaveMetadataSourceSetting(ctx, storage.MetadataSourceSetting{
		Source: "lastfm", Enabled: true, APIKey: "SECRET-LASTFM-KEY-123", CacheDays: 30,
	})
	aiSt, _ := store.AISettings(ctx)
	aiSt.BaseURL = "https://api.openai.com/v1"
	aiSt.APIKey = "SECRET-LLM-KEY-456"
	aiSt.Model = "gpt-4o-mini"
	aiSt.ProxyURL = "http://user:pass@127.0.0.1:7890"
	_ = store.SaveAISettings(ctx, aiSt)
	ws, _ := store.WebSearchSettings(ctx)
	ws.TavilyKeys = []string{"SECRET-TAVILY-KEY-789"}
	ws.DeepSeekKey = "SECRET-DEEPSEEK-KEY-012"
	_ = store.SaveWebSearchSettings(ctx, ws)
	imgSt, _ := store.ArtistImageSettings(ctx)
	imgSt.FanartAPIKey = "SECRET-FANART-KEY-345"
	imgSt.FanartEnabled = true
	_ = store.SaveArtistImageSettings(ctx, imgSt)
	_ = store.SetListenBrainzConnection(ctx, "https://api.listenbrainz.org", "SECRET-LB-TOKEN-678", "alice")
	_ = store.SaveLastFMScrobblePreferences(ctx, true, true, "SECRET-LFM-SHARED-901")
	_ = store.UpdateAcquisitionSettings(ctx, storage.AcquisitionSettingsUpdate{
		Enabled:       false,
		JackettURL:    "http://127.0.0.1:9117",
		JackettAPIKey: "SECRET-JACKETT-KEY-234",
		QBURL:         "http://127.0.0.1:8080",
		QBUsername:    "admin",
		QBPassword:    "SECRET-QB-PASS-567",
	})

	req := httptest.NewRequest(http.MethodGet, "/admin/setup", nil)
	req.RemoteAddr = "198.51.100.7:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/setup = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// Verify strict CSP compliance: versioned assets referenced, no inline executable JS.
	for _, want := range []string{
		app.assets.assetURL("tokens.css"),
		app.assets.assetURL("setup.css"),
		app.assets.assetURL("setup.js"),
		app.assets.assetURL("theme.js"),
		`<meta name="csrf-token" content="`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /admin/setup HTML missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"<script>", "onclick=", "onchange=",
		"SECRET-LASTFM-KEY-123", "SECRET-LLM-KEY-456", "user:pass@127.0.0.1:7890",
		"SECRET-TAVILY-KEY-789", "SECRET-DEEPSEEK-KEY-012", "SECRET-FANART-KEY-345",
		"SECRET-LB-TOKEN-678", "SECRET-LFM-SHARED-901", "SECRET-JACKETT-KEY-234", "SECRET-QB-PASS-567",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("GET /admin/setup HTML leaked forbidden string %q", forbidden)
		}
	}

	bootstrap := extractSetupBootstrap(t, body)
	if bootstrap.Deployment.ProxyPeer != "198.51.100.7" {
		t.Errorf("ProxyPeer = %q, want 198.51.100.7", bootstrap.Deployment.ProxyPeer)
	}
	if !bootstrap.Deployment.MediaTokenEphemeral {
		t.Errorf("MediaTokenEphemeral = false, want true")
	}
	if !bootstrap.Step1.LastFMKeySet || !bootstrap.Step1.FanartKeySet || !bootstrap.Step2.KeySet || !bootstrap.Step2.ProxySet || bootstrap.Step2.TavilyKeyCount != 1 || !bootstrap.Step2.DeepSeekKeySet || !bootstrap.Step4.LBTokenSet || !bootstrap.Step4.LFMSecretSet || !bootstrap.Step4.JackettKeySet || !bootstrap.Step4.QBPasswordSet {
		t.Errorf("expected key-set flags to be true in bootstrap: %+v", bootstrap)
	}
	if !bootstrap.Preflight.OK || bootstrap.Preflight.Status != "ok" {
		t.Errorf("preflight = %+v, want ok", bootstrap.Preflight)
	}

	// Verify embedded setup.css and setup.js are served with immutable cache headers.
	for name, wantPrefix := range map[string]string{
		"setup.css": "text/css",
		"setup.js":  "javascript",
	} {
		aReq := httptest.NewRequest(http.MethodGet, app.assets.assetURL(name), nil)
		aRec := httptest.NewRecorder()
		handler.ServeHTTP(aRec, aReq)
		if aRec.Code != http.StatusOK {
			t.Fatalf("GET asset %s = %d", name, aRec.Code)
		}
		if !strings.Contains(aRec.Header().Get("Content-Type"), wantPrefix) {
			t.Errorf("asset %s Content-Type = %q", name, aRec.Header().Get("Content-Type"))
		}
		if got := aRec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("asset %s Cache-Control = %q", name, got)
		}
	}
}

func TestSetupWizardStepPersistence(t *testing.T) {
	ctx := context.Background()
	app, store, cookie, csrf := newFreshSetupApp(t, nil)
	handler := app.Handler()

	// Missing CSRF header must be rejected on all write routes.
	for _, path := range []string{"/admin/setup/step/1", "/admin/setup/step/2", "/admin/setup/step/3", "/admin/setup/step/4", "/admin/setup/complete", "/admin/setup/skip"} {
		rec := setupJSONReq(t, handler, http.MethodPost, path, map[string]any{}, cookie, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without CSRF = %d, want 403", path, rec.Code)
		}
	}

	// Step 1 validation: MusicBrainz enabled without contact -> 400.
	rec := setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/1", map[string]any{
		"musicbrainzEnabled": true,
		"musicbrainzContact": "   ",
	}, cookie, csrf)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "musicbrainzContact") {
		t.Fatalf("step 1 empty MB contact = %d %s", rec.Code, rec.Body.String())
	}

	// Step 1 validation: Last.fm enabled first time without API Key -> 400.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/1", map[string]any{
		"musicbrainzEnabled": false,
		"lastfmEnabled":      true,
		"lastfmApiKey":       "",
	}, cookie, csrf)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lastfmApiKey") {
		t.Fatalf("step 1 empty Last.fm key = %d %s", rec.Code, rec.Body.String())
	}

	// Step 1 valid save.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/1", map[string]any{
		"musicbrainzEnabled": true,
		"musicbrainzContact": "admin@example.com",
		"musicbrainzAuto":    true,
		"bangumiEnabled":     true,
		"bangumiAuto":        true,
		"lastfmEnabled":      true,
		"lastfmApiKey":       "lfm-key-from-step1",
		"bioTargetLanguage":  "ja",
		"wikipediaEnabled":   true,
		"fanartApiKey":       "fanart-key-1",
		"cacheDays":          45,
		"imgBangumi":         true,
		"imgDeezer":          false,
		"imgWikipedia":       true,
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 1 valid save = %d %s", rec.Code, rec.Body.String())
	}

	// Re-save Step 1 with blank keys -> stored secrets preserved.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/1", map[string]any{
		"musicbrainzEnabled": true,
		"musicbrainzContact": "admin@example.com",
		"musicbrainzAuto":    false,
		"bangumiEnabled":     true,
		"bangumiAuto":        true,
		"lastfmEnabled":      true,
		"lastfmApiKey":       "",
		"bioTargetLanguage":  "zh-Hans",
		"wikipediaEnabled":   true,
		"fanartApiKey":       "",
		"cacheDays":          30,
		"imgBangumi":         true,
		"imgDeezer":          true,
		"imgWikipedia":       true,
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 1 preserve-keys re-save = %d %s", rec.Code, rec.Body.String())
	}
	lfmMeta, _ := store.MetadataSourceSetting(ctx, "lastfm")
	if lfmMeta.APIKey != "lfm-key-from-step1" {
		t.Fatalf("Last.fm API key was overwritten: %q", lfmMeta.APIKey)
	}
	imgSt, _ := store.ArtistImageSettings(ctx)
	if imgSt.FanartAPIKey != "fanart-key-1" || !imgSt.FanartEnabled {
		t.Fatalf("Fanart key was overwritten: %+v", imgSt)
	}

	// Step 3 before LLM/WebSearch configured: server enforces prerequisite gates!
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/3", map[string]any{
		"aiReviewEnabled":    true,
		"aiReviewMode":       "auto",
		"aiReviewWeb":        true,
		"aliasEnabled":       true,
		"avatarWebAiEnabled": true,
		"bioAiEnabled":       true,
		"lyricsTransEnabled": true,
		"dailyMixEnabled":    true,
		"dailyMixLlmTitles":  true,
		"aiTaggingEnabled":   true,
		"acousticEnabled":    true,
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 3 save without LLM = %d %s", rec.Code, rec.Body.String())
	}
	if rev, _ := store.AIReviewSettings(ctx); rev.Enabled {
		t.Fatalf("AI review should stay disabled without LLM")
	}
	if alias, _ := store.ArtistAliasSettings(ctx); alias.Enabled {
		t.Fatalf("Alias should stay disabled without LLM+WebSearch")
	}
	if dm, _ := store.DailyMixSettings(ctx); !dm.Enabled || dm.LLMTitles {
		t.Fatalf("DailyMix = %+v, want Enabled=true LLMTitles=false", dm)
	}
	if ai, _ := store.AISettings(ctx); ai.TaggingEnabled {
		t.Fatalf("AI tagging should stay disabled without LLM")
	}

	// Step 2 save LLM + WebSearch (append+dedupe Tavily keys).
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/2", map[string]any{
		"baseUrl":           "https://api.openai.com/v1/",
		"apiKey":            "sk-primary-secret",
		"model":             "gpt-4o-mini",
		"reviewModel":       "gpt-4o",
		"fallbackModel":     "gpt-4.1-mini",
		"rpm":               90,
		"concurrency":       3,
		"reasoningEffort":   "low",
		"visionModel":       "gpt-4o",
		"webSearchProvider": "tavily_then_deepseek",
		"tavilyKeys":        "tvly-aaa\ntvly-bbb\ntvly-aaa",
		"tavilyKeysMode":    "append",
		"deepSeekKey":       "ds-key-1",
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 2 save = %d %s", rec.Code, rec.Body.String())
	}
	aiSaved, _ := store.AISettings(ctx)
	if aiSaved.BaseURL != "https://api.openai.com/v1" || aiSaved.APIKey != "sk-primary-secret" || aiSaved.Model != "gpt-4o-mini" || aiSaved.ReviewModel != "gpt-4o" || aiSaved.RequestsPerMinute != 90 || aiSaved.Concurrency != 3 {
		t.Fatalf("unexpected AISettings: %+v", aiSaved)
	}
	wsSaved, _ := store.WebSearchSettings(ctx)
	if len(wsSaved.TavilyKeys) != 2 || wsSaved.DeepSeekKey != "ds-key-1" {
		t.Fatalf("unexpected WebSearchSettings: %+v", wsSaved)
	}
	imgSt, _ = store.ArtistImageSettings(ctx)
	if imgSt.VisionModel != "gpt-4o" || !imgSt.VisionEnabled {
		t.Fatalf("unexpected VisionModel: %+v", imgSt)
	}

	// Step 2 re-save with blank secrets preserves them.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/2", map[string]any{
		"baseUrl":           "https://api.openai.com/v1",
		"apiKey":            "",
		"model":             "gpt-4o-mini",
		"reviewModel":       "gpt-4o",
		"rpm":               90,
		"concurrency":       3,
		"visionModel":       "gpt-4o",
		"webSearchProvider": "tavily_then_deepseek",
		"tavilyKeys":        "",
		"deepSeekKey":       "",
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 2 blank-secret re-save = %d %s", rec.Code, rec.Body.String())
	}
	aiSaved, _ = store.AISettings(ctx)
	wsSaved, _ = store.WebSearchSettings(ctx)
	if aiSaved.APIKey != "sk-primary-secret" || len(wsSaved.TavilyKeys) != 2 || wsSaved.DeepSeekKey != "ds-key-1" {
		t.Fatalf("blank re-save clobbered Step 2 secrets: ai=%+v ws=%+v", aiSaved, wsSaved)
	}

	// Step 3 now succeeds for LLM + WebSearch features.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/3", map[string]any{
		"aiReviewEnabled":    true,
		"aiReviewMode":       "auto",
		"aiReviewWeb":        true,
		"aliasEnabled":       true,
		"avatarWebAiEnabled": true,
		"bioAiEnabled":       true,
		"lyricsTransEnabled": true,
		"dailyMixEnabled":    true,
		"dailyMixLlmTitles":  true,
		"aiTaggingEnabled":   true,
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 3 with prereqs = %d %s", rec.Code, rec.Body.String())
	}
	if rev, _ := store.AIReviewSettings(ctx); !rev.Enabled || rev.AlbumMode != "auto" || !rev.WebSearchEnabled {
		t.Fatalf("unexpected AIReviewSettings: %+v", rev)
	}
	if alias, _ := store.ArtistAliasSettings(ctx); !alias.Enabled {
		t.Fatalf("expected alias enabled")
	}
	if tr, _ := store.LyricsTranslationSettings(ctx); !tr.Enabled {
		t.Fatalf("expected lyrics translation enabled")
	}
	if ai, _ := store.AISettings(ctx); !ai.TaggingEnabled {
		t.Fatalf("expected AI tagging enabled")
	}
	if wsState, _ := store.SetupWizardState(ctx); wsState.Status != storage.SetupStatusPending {
		t.Fatalf("wizard status after step 3 = %q, want pending", wsState.Status)
	}

	// Step 4 validation: acquisition enabled with missing URL -> 400.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/4", map[string]any{
		"acqEnabled": true,
		"jackettUrl": "",
		"qbUrl":      "",
	}, cookie, csrf)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("step 4 acq without URLs = %d, want 400", rec.Code)
	}

	// Step 4 valid save: also verify lfmApiKey syncs back to Step 1 metadata_source_settings.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/4", map[string]any{
		"lbEnabled":          true,
		"lbServerUrl":        "https://api.listenbrainz.org",
		"lbUserToken":        "lb-tok-123",
		"lbNowPlaying":       true,
		"lbLoveSync":         true,
		"lfmScrobbleEnabled": true,
		"lfmApiKey":          "lfm-key-synced-from-step4",
		"lfmSecret":          "lfm-shared-secret",
		"lfmNowPlaying":      true,
		"lfmLoveSync":        true,
		"acqEnabled":         true,
		"jackettUrl":         "http://127.0.0.1:9117",
		"jackettIndexer":     "jpopsuki",
		"jackettApiKey":      "jk-secret",
		"qbUrl":              "http://127.0.0.1:8080",
		"qbUsername":         "admin",
		"qbPassword":         "qb-secret",
		"qbCategory":         "music-in",
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("step 4 valid save = %d %s", rec.Code, rec.Body.String())
	}
	lfmMeta, _ = store.MetadataSourceSetting(ctx, "lastfm")
	if lfmMeta.APIKey != "lfm-key-synced-from-step4" {
		t.Fatalf("Step 4 lfmApiKey did not sync to metadata_source_settings: %q", lfmMeta.APIKey)
	}
	acqSt, _ := store.AcquisitionSettings(ctx)
	if !acqSt.Enabled || acqSt.QBCategory != "music-in" || !acqSt.KeySet || !acqSt.HasPassword {
		t.Fatalf("unexpected acquisition settings: %+v", acqSt)
	}

	// Complete setup, then re-edit Step 2: status must stay 'completed'.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/complete", map[string]any{
		"watchEnabled":         true,
		"watchIntervalSeconds": 300,
		"startScan":            false,
		"autoEnrich":           false,
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("complete = %d %s", rec.Code, rec.Body.String())
	}
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/2", map[string]any{
		"baseUrl": "https://api.openai.com/v1",
		"model":   "gpt-4.1-mini",
		"rpm":     120,
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-edit step 2 after complete = %d %s", rec.Code, rec.Body.String())
	}
	finalState, _ := store.SetupWizardState(ctx)
	if finalState.Status != storage.SetupStatusCompleted {
		t.Fatalf("status after post-completion edit = %q, want completed", finalState.Status)
	}
}

func TestFirstRunScanReleaserHTTP(t *testing.T) {
	cases := []struct {
		name           string
		endpoint       string
		body           map[string]any
		wantStartScan  bool
		wantAutoEnrich bool
		wantStatus     string
		wantRedirect   string
	}{
		{
			name:           "complete with scan and auto enrich",
			endpoint:       "/admin/setup/complete",
			body:           map[string]any{"watchEnabled": true, "watchIntervalSeconds": 60, "startScan": true, "autoEnrich": true},
			wantStartScan:  true,
			wantAutoEnrich: true,
			wantStatus:     storage.SetupStatusCompleted,
			wantRedirect:   "/admin",
		},
		{
			name:           "complete with local-only scan",
			endpoint:       "/admin/setup/complete",
			body:           map[string]any{"watchEnabled": true, "watchIntervalSeconds": 120, "startScan": true, "autoEnrich": false},
			wantStartScan:  true,
			wantAutoEnrich: false,
			wantStatus:     storage.SetupStatusCompleted,
			wantRedirect:   "/admin",
		},
		{
			name:           "complete without scan",
			endpoint:       "/admin/setup/complete",
			body:           map[string]any{"watchEnabled": false, "watchIntervalSeconds": 60, "startScan": false, "autoEnrich": false},
			wantStartScan:  false,
			wantAutoEnrich: false,
			wantStatus:     storage.SetupStatusCompleted,
			wantRedirect:   "/admin/home",
		},
		{
			name:           "skip releases scan immediately",
			endpoint:       "/admin/setup/skip",
			body:           map[string]any{},
			wantStartScan:  true,
			wantAutoEnrich: true,
			wantStatus:     storage.SetupStatusSkipped,
			wantRedirect:   "/admin",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, store, cookie, csrf := newFreshSetupApp(t, nil)
			var called bool
			var gotStart, gotEnrich bool
			app.SetFirstRunScanReleaser(func(startScan, autoEnrich bool) {
				called = true
				gotStart = startScan
				gotEnrich = autoEnrich
			})

			rec := setupJSONReq(t, app.Handler(), http.MethodPost, tc.endpoint, tc.body, cookie, csrf)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST %s = %d %s", tc.endpoint, rec.Code, rec.Body.String())
			}
			var resp struct {
				OK         bool   `json:"ok"`
				RedirectTo string `json:"redirectTo"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if !called || gotStart != tc.wantStartScan || gotEnrich != tc.wantAutoEnrich {
				t.Fatalf("releaser called=%v start=%v enrich=%v, want true %v %v", called, gotStart, gotEnrich, tc.wantStartScan, tc.wantAutoEnrich)
			}
			if resp.RedirectTo != tc.wantRedirect {
				t.Fatalf("redirectTo = %q, want %q", resp.RedirectTo, tc.wantRedirect)
			}
			st, _ := store.SetupWizardState(context.Background())
			if st.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", st.Status, tc.wantStatus)
			}
		})
	}
}

func TestSetupWizardTestEndpointsAndPreflight(t *testing.T) {
	ctx := context.Background()

	// 1. Directory preflight: missing, empty, ok.
	t.Run("preflight", func(t *testing.T) {
		missingDir := filepath.Join(t.TempDir(), "does-not-exist")
		pf := probeMusicDirectory(ctx, missingDir)
		if pf.OK || pf.Status != "missing" {
			t.Fatalf("missing preflight = %+v", pf)
		}

		emptyDir := t.TempDir()
		pf = probeMusicDirectory(ctx, emptyDir)
		if pf.OK || pf.Status != "empty" {
			t.Fatalf("empty preflight = %+v", pf)
		}

		app, _, cookie, _ := newFreshSetupApp(t, nil)
		rec := setupJSONReq(t, app.Handler(), http.MethodGet, "/admin/setup/preflight/library", nil, cookie, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET preflight = %d %s", rec.Code, rec.Body.String())
		}
		var got setupLibraryPreflight
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.OK || got.Status != "ok" || got.SubdirCount != 1 || got.AudioFileSample != 1 {
			t.Fatalf("ok preflight = %+v", got)
		}
	})

	// 2. LLM test endpoint (mock OpenAI server: unsaved override + stored key fallback).
	t.Run("llm", func(t *testing.T) {
		llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-llm-key" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"message":"invalid key"}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"{\"ok\":true}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}}`)
		}))
		defer llmSrv.Close()

		app, store, cookie, csrf := newFreshSetupApp(t, nil)
		handler := app.Handler()

		// Missing CSRF -> 403.
		if rec := setupJSONReq(t, handler, http.MethodPost, "/admin/setup/test/llm", map[string]any{}, cookie, ""); rec.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF = %d, want 403", rec.Code)
		}

		// Unsaved override succeeds.
		rec := setupJSONReq(t, handler, http.MethodPost, "/admin/setup/test/llm", map[string]any{
			"baseUrl": llmSrv.URL,
			"apiKey":  "test-llm-key",
			"model":   "test-model",
		}, cookie, csrf)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("unsaved LLM test = %d %s", rec.Code, rec.Body.String())
		}

		// Persist credentials, then test with blank apiKey (falls back to stored key).
		aiSt, _ := store.AISettings(ctx)
		aiSt.BaseURL = llmSrv.URL
		aiSt.APIKey = "test-llm-key"
		aiSt.Model = "test-model"
		_ = store.SaveAISettings(ctx, aiSt)

		rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/test/llm", map[string]any{
			"baseUrl": llmSrv.URL,
			"apiKey":  "",
			"model":   "test-model",
		}, cookie, csrf)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("stored-key fallback LLM test = %d %s", rec.Code, rec.Body.String())
		}
	})

	// 3. Web search missing keys -> 400 not_configured.
	t.Run("web-search-missing", func(t *testing.T) {
		app, _, cookie, csrf := newFreshSetupApp(t, nil)
		rec := setupJSONReq(t, app.Handler(), http.MethodPost, "/admin/setup/test/web-search", map[string]any{
			"provider": "tavily",
		}, cookie, csrf)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not_configured") {
			t.Fatalf("web-search missing keys = %d %s", rec.Code, rec.Body.String())
		}
	})

	// 4. ListenBrainz test validates token and saves username.
	t.Run("listenbrainz", func(t *testing.T) {
		lbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/1/validate-token" && r.Header.Get("Authorization") == "Token lb-good" {
				_, _ = io.WriteString(w, `{"code":200,"valid":true,"user_name":"mizuki"}`)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":401,"error":"Invalid token"}`)
		}))
		defer lbSrv.Close()

		app, store, cookie, csrf := newFreshSetupApp(t, nil)
		rec := setupJSONReq(t, app.Handler(), http.MethodPost, "/admin/setup/test/listenbrainz", map[string]any{
			"serverUrl": lbSrv.URL,
			"userToken": "lb-good",
		}, cookie, csrf)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"username":"mizuki"`) {
			t.Fatalf("listenbrainz test = %d %s", rec.Code, rec.Body.String())
		}
		saved, _ := store.ListenBrainzSettings(ctx)
		if !saved.Connected() || saved.Username != "mizuki" {
			t.Fatalf("listenbrainz not saved: %+v", saved)
		}
	})

	// 5. Jackett and qBittorrent inline test endpoints.
	t.Run("jackett-and-qb", func(t *testing.T) {
		jkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("apikey") != "jk-good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
		}))
		defer jkSrv.Close()

		qbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v2/auth/login":
				http.SetCookie(w, &http.Cookie{Name: "SID", Value: "ok-sid", Path: "/"})
				_, _ = io.WriteString(w, "Ok.")
			case "/api/v2/app/version":
				_, _ = io.WriteString(w, "v4.6.5")
			case "/api/v2/torrents/categories":
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"music-in":{"name":"music-in","savePath":"/downloads/music"}}`)
			default:
				http.NotFound(w, r)
			}
		}))
		defer qbSrv.Close()

		app, _, cookie, csrf := newFreshSetupApp(t, nil)
		handler := app.Handler()

		rec := setupJSONReq(t, handler, http.MethodPost, "/admin/setup/test/jackett", map[string]any{
			"url":     jkSrv.URL,
			"indexer": "jpopsuki",
			"apiKey":  "jk-good",
		}, cookie, csrf)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("jackett test = %d %s", rec.Code, rec.Body.String())
		}

		rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/test/qb", map[string]any{
			"url":      qbSrv.URL,
			"username": "admin",
			"password": "secret",
			"category": "music-in",
		}, cookie, csrf)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
			t.Fatalf("qb valid category test = %d %s", rec.Code, rec.Body.String())
		}

		rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/test/qb", map[string]any{
			"url":      qbSrv.URL,
			"username": "admin",
			"password": "secret",
			"category": "nonexistent-cat",
		}, cookie, csrf)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":false`) {
			t.Fatalf("qb missing category test = %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestDashboardSetupBanner(t *testing.T) {
	app, _, cookie, csrf := newFreshSetupApp(t, nil)
	handler := app.Handler()

	// Skip setup -> dashboard renders dismissible resume banner + permanent reopen link.
	rec := setupJSONReq(t, handler, http.MethodPost, "/admin/setup/skip", map[string]any{}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("skip = %d %s", rec.Code, rec.Body.String())
	}

	dashReq := httptest.NewRequest(http.MethodGet, "/admin", nil)
	dashReq.AddCookie(cookie)
	dashRec := httptest.NewRecorder()
	handler.ServeHTTP(dashRec, dashReq)
	if dashRec.Code != http.StatusOK {
		t.Fatalf("GET /admin after skip = %d", dashRec.Code)
	}
	body := dashRec.Body.String()
	if !strings.Contains(body, `data-setup-resume-banner`) {
		t.Fatalf("expected resume banner on /admin after skip")
	}
	if !strings.Contains(body, `href="/admin/setup"`) || !strings.Contains(body, `data-no-pjax`) {
		t.Fatalf("expected data-no-pjax /admin/setup entry on /admin")
	}

	// Dismiss via standard HTML form POST (with csrfToken form field).
	form := url.Values{"csrfToken": {csrf}}
	dismissReq := httptest.NewRequest(http.MethodPost, "/admin/setup/banner/dismiss", strings.NewReader(form.Encode()))
	dismissReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	dismissReq.AddCookie(cookie)
	dismissRec := httptest.NewRecorder()
	handler.ServeHTTP(dismissRec, dismissReq)
	if dismissRec.Code != http.StatusSeeOther || dismissRec.Header().Get("Location") != "/admin" {
		t.Fatalf("dismiss form POST = %d %q", dismissRec.Code, dismissRec.Header().Get("Location"))
	}

	// Reload dashboard: banner is permanently gone, permanent "初始化向导" entry remains.
	dashReq2 := httptest.NewRequest(http.MethodGet, "/admin", nil)
	dashReq2.AddCookie(cookie)
	dashRec2 := httptest.NewRecorder()
	handler.ServeHTTP(dashRec2, dashReq2)
	body2 := dashRec2.Body.String()
	if strings.Contains(body2, `data-setup-resume-banner`) {
		t.Fatalf("resume banner still shown after dismiss")
	}
	if !strings.Contains(body2, `href="/admin/setup"`) {
		t.Fatalf("permanent /admin/setup button disappeared after dismiss")
	}
}

func TestSetupWizardNativeWebSearchFlow(t *testing.T) {
	ctx := context.Background()
	nativeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer sk-wizard-native" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"output": []any{
				map[string]any{
					"type":   "web_search_call",
					"status": "completed",
					"action": map[string]any{"type": "search", "sources": []any{"https://musicbrainz.org/"}},
				},
				map[string]any{
					"type": "message",
					"content": []any{
						map[string]any{"type": "output_text", "text": `{"results":[{"url":"https://musicbrainz.org/","title":"MusicBrainz","content":"Music encyclopedia"}]}`},
					},
				},
			},
		})
	}))
	defer nativeSrv.Close()

	app, store, cookie, csrf := newFreshSetupApp(t, nil)
	handler := app.Handler()

	// 1. Inline test before saving Step 2 using unsaved llmBaseUrl/llmApiKey/llmModel.
	rec := setupJSONReq(t, handler, http.MethodPost, "/admin/setup/test/web-search", map[string]any{
		"provider":   "native_then_search",
		"llmBaseUrl": nativeSrv.URL + "/v1",
		"llmApiKey":  "sk-wizard-native",
		"llmModel":   "gpt-6-luna",
	}, cookie, csrf)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), "模型原生搜索正常") {
		t.Fatalf("setup native web search inline test = %d %s", rec.Code, rec.Body.String())
	}

	// 2. Save Step 2 with ONLY global AI + native_then_search (zero Tavily/DeepSeek keys).
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/2", map[string]any{
		"baseUrl":           nativeSrv.URL + "/v1",
		"apiKey":            "sk-wizard-native",
		"model":             "gpt-6-luna",
		"rpm":               60,
		"concurrency":       4,
		"webSearchProvider": "native_then_search",
	}, cookie, csrf)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"webSearchReady":true`) {
		t.Fatalf("setup step 2 native save = %d %s", rec.Code, rec.Body.String())
	}

	// 3. Step 3 must unlock web-search-dependent automation features without Tavily/DeepSeek keys.
	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/step/3", map[string]any{
		"aiReviewEnabled":    true,
		"aiReviewMode":       "recommend",
		"aiReviewWeb":        true,
		"aliasEnabled":       true,
		"avatarWebAiEnabled": true,
		"bioAiEnabled":       true,
	}, cookie, csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup step 3 save = %d %s", rec.Code, rec.Body.String())
	}
	rev, _ := store.AIReviewSettings(ctx)
	aliasSt, _ := store.ArtistAliasSettings(ctx)
	imgSt, _ := store.ArtistImageSettings(ctx)
	bioSt, _ := store.BiographySettings(ctx)
	if !rev.WebSearchEnabled || !aliasSt.Enabled || !imgSt.WebFallbackEnabled || !bioSt.AIWebFallbackEnabled {
		t.Fatalf("expected all web-search features unlocked under native_then_search: rev=%+v alias=%+v img=%+v bio=%+v", rev, aliasSt, imgSt, bioSt)
	}

	// 4. Security guard: testing a changed draft llmBaseUrl with blank llmApiKey must NEVER leak the stored API key.
	var draftLeaked bool
	draftSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			draftLeaked = true
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer draftSrv.Close()

	rec = setupJSONReq(t, handler, http.MethodPost, "/admin/setup/test/web-search", map[string]any{
		"provider":   "native_then_search",
		"llmBaseUrl": draftSrv.URL + "/v1",
		"llmApiKey":  "",
		"llmModel":   "gpt-6-luna",
	}, cookie, csrf)
	if rec.Code != http.StatusBadRequest || draftLeaked {
		t.Fatalf("changed draft endpoint probe = %d %s (draftLeaked=%v)", rec.Code, rec.Body.String(), draftLeaked)
	}
}
