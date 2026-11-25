package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/loudness"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

type stubLoudnessMeasurer struct {
	available bool
	fn        func(ctx context.Context, path string) (loudness.Measurement, error)
}

func (s stubLoudnessMeasurer) Available() bool { return s.available }

func (s stubLoudnessMeasurer) Measure(ctx context.Context, path string) (loudness.Measurement, error) {
	if s.fn != nil {
		return s.fn(ctx, path)
	}
	lra := 6.2
	return loudness.Measurement{IntegratedLUFS: -9.5, TruePeakDB: -0.4, LoudnessRange: &lra}, nil
}

func attachLoudness(app *App, store *storage.Store, m loudness.Measurer) *loudness.Service {
	svc := loudness.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), loudness.Options{
		Measurer: m,
		Workers:  1,
	})
	app.SetLoudness(svc)
	return svc
}

func loudnessTestApp(t *testing.T) (*App, http.Handler, *storage.Store, *http.Cookie) {
	t.Helper()
	store := credentialTestStore(t)
	cfg := credentialTestConfig(t)
	if err := store.EnsureLibrary(context.Background(), cfg.LibraryName, cfg.MusicDirectory); err != nil {
		t.Fatal(err)
	}
	app := newCredentialTestApp(t, cfg, store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	return app, handler, store, cookie
}

func TestLoudnessCapabilitiesUnavailableAndAvailable(t *testing.T) {
	app, handler, store, _ := loudnessTestApp(t)

	// Without loudness service attached, capabilities.features.loudness is false.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+testEnvAPIToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d", rec.Code)
	}
	var capBody struct {
		Features map[string]bool `json:"features"`
		Loudness struct {
			Available         bool     `json:"available"`
			AnalyzedTracks    int64    `json:"analyzedTracks"`
			TotalTracks       int64    `json:"totalTracks"`
			LibraryMedianLUFS *float64 `json:"libraryMedianLufs"`
		} `json:"loudness"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &capBody); err != nil {
		t.Fatal(err)
	}
	if capBody.Features["loudness"] || capBody.Loudness.Available {
		t.Fatalf("expected loudness unavailable by default: %+v", capBody)
	}

	// Attach available measurer -> features.loudness is true.
	attachLoudness(app, store, stubLoudnessMeasurer{available: true})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if err := json.Unmarshal(rec.Body.Bytes(), &capBody); err != nil {
		t.Fatal(err)
	}
	if !capBody.Features["loudness"] || !capBody.Loudness.Available {
		t.Fatalf("expected loudness available: %+v", capBody)
	}
}

func TestLoudnessAdminControlsAuthAndCSRF(t *testing.T) {
	app, handler, store, cookie := loudnessTestApp(t)
	attachLoudness(app, store, stubLoudnessMeasurer{available: true})

	// Admin page renders and dashboard shows loudness summary chip.
	page := getWithCookie(handler, "/admin/loudness", cookie)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "响度分析") {
		t.Fatalf("loudness admin page failed: %d", page.Code)
	}
	dash := getWithCookie(handler, "/admin", cookie)
	if dash.Code != http.StatusOK || !strings.Contains(dash.Body.String(), `href="/admin/loudness"`) {
		t.Fatalf("dashboard missing loudness link: %d", dash.Code)
	}

	// Unauthenticated status call rejected; API bearer allowed on /api/v1/loudness.
	if rec := getWithCookie(handler, "/admin/loudness/status", nil); rec.Code == http.StatusOK {
		t.Fatalf("expected unauthenticated admin status rejected, got 200")
	}
	apiReq := httptest.NewRequest(http.MethodGet, "/api/v1/loudness", nil)
	apiReq.Header.Set("Authorization", "Bearer "+testEnvAPIToken)
	apiRec := httptest.NewRecorder()
	handler.ServeHTTP(apiRec, apiReq)
	if apiRec.Code != http.StatusOK || !strings.Contains(apiRec.Body.String(), `"available":true`) {
		t.Fatalf("api loudness status failed: %d %s", apiRec.Code, apiRec.Body.String())
	}

	// Pause without CSRF fails with 403.
	badReq := httptest.NewRequest(http.MethodPost, "/admin/loudness/pause", nil)
	badReq.AddCookie(cookie)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusForbidden {
		t.Fatalf("pause without CSRF = %d, want 403", badRec.Code)
	}

	// Pause -> resume -> retry with valid CSRF succeed.
	postSecurity(t, app, handler, cookie, "/admin/loudness/pause", nil)
	if st, err := store.AudioLoudnessSettings(context.Background()); err != nil || !st.Paused {
		t.Fatalf("expected paused=true, got %+v err=%v", st, err)
	}
	postSecurity(t, app, handler, cookie, "/admin/loudness/resume", nil)
	if st, err := store.AudioLoudnessSettings(context.Background()); err != nil || st.Paused {
		t.Fatalf("expected paused=false, got %+v err=%v", st, err)
	}
	rec := postSecurity(t, app, handler, cookie, "/admin/loudness/failures/retry", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("retry redirect = %d", rec.Code)
	}
}

func TestLoudnessEndToEndPipelineToAPI(t *testing.T) {
	app, handler, store, _ := loudnessTestApp(t)
	ctx := context.Background()
	lib, err := store.LibraryByRoot(ctx, app.config.MusicDirectory)
	if err != nil {
		t.Fatal(err)
	}

	for i, tc := range []struct {
		rel, title string
	}{
		{"alb/01.flac", "Track One"},
		{"alb/02.flac", "Track Two"},
	} {
		if err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    lib.ID,
			RelativePath: tc.rel,
			FileSize:     int64(1000 + i),
			ModifiedAtNS: 99,
			Metadata: metadata.AudioMetadata{
				Title:          tc.title,
				Album:          "Loudness Album",
				Artists:        []string{"Tester"},
				AlbumArtists:   []string{"Tester"},
				DiscNumber:     1,
				TrackNumber:    i + 1,
				DurationMillis: 200_000,
			},
			AudioProps: metadata.AudioProps{Codec: "flac", SampleRate: 44100, BitDepth: 16},
		}); err != nil {
			t.Fatal(err)
		}
	}

	svc := attachLoudness(app, store, stubLoudnessMeasurer{
		available: true,
		fn: func(_ context.Context, path string) (loudness.Measurement, error) {
			lra := 6.0
			if strings.HasSuffix(path, "01.flac") {
				return loudness.Measurement{IntegratedLUFS: -8.0, TruePeakDB: 0.0, LoudnessRange: &lra}, nil
			}
			return loudness.Measurement{IntegratedLUFS: -12.0, TruePeakDB: -1.0, LoudnessRange: &lra}, nil
		},
	})
	for i := 0; i < 2; i++ {
		if d := svc.Step(ctx); d != 0 {
			t.Fatalf("step %d returned delay %v", i, d)
		}
	}

	getJSON := func(path string) []byte {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+testEnvAPIToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	// 1. Capabilities exposes analyzedTracks=2, totalTracks=2, median=-10.0.
	var capResp struct {
		Features map[string]bool `json:"features"`
		Loudness struct {
			Available         bool     `json:"available"`
			AnalyzedTracks    int64    `json:"analyzedTracks"`
			TotalTracks       int64    `json:"totalTracks"`
			LibraryMedianLUFS *float64 `json:"libraryMedianLufs"`
		} `json:"loudness"`
	}
	if err := json.Unmarshal(getJSON("/api/v1/capabilities"), &capResp); err != nil {
		t.Fatal(err)
	}
	if !capResp.Features["loudness"] || capResp.Loudness.AnalyzedTracks != 2 || capResp.Loudness.TotalTracks != 2 ||
		capResp.Loudness.LibraryMedianLUFS == nil || *capResp.Loudness.LibraryMedianLUFS != -10.0 {
		t.Fatalf("unexpected capabilities loudness: %+v", capResp.Loudness)
	}

	// 2. /api/v1/browse/tracks returns loudness fields and album aggregate.
	var browseResp struct {
		Items []storage.Track `json:"items"`
	}
	if err := json.Unmarshal(getJSON("/api/v1/browse/tracks"), &browseResp); err != nil {
		t.Fatal(err)
	}
	if len(browseResp.Items) != 2 {
		t.Fatalf("browse items = %d", len(browseResp.Items))
	}
	albumID := browseResp.Items[0].AlbumID
	for _, tr := range browseResp.Items {
		if tr.LoudnessLUFS == nil || tr.TruePeakDB == nil || tr.AlbumLoudnessLUFS == nil || tr.AlbumTruePeakDB == nil {
			t.Fatalf("missing loudness on browse track %+v", tr)
		}
	}

	// 3. /api/v1/albums/{id} returns loudness on embedded tracks.
	var albumResp struct {
		Tracks []storage.Track `json:"tracks"`
	}
	if err := json.Unmarshal(getJSON(fmt.Sprintf("/api/v1/albums/%d", albumID)), &albumResp); err != nil {
		t.Fatal(err)
	}
	if len(albumResp.Tracks) != 2 || albumResp.Tracks[0].AlbumLoudnessLUFS == nil {
		t.Fatalf("missing loudness on album detail tracks: %+v", albumResp.Tracks)
	}

	// 4. /api/v1/sync/tracks returns loudness for Android sync clients.
	var syncResp struct {
		Items []storage.SyncTrack `json:"items"`
	}
	if err := json.Unmarshal(getJSON("/api/v1/sync/tracks"), &syncResp); err != nil {
		t.Fatal(err)
	}
	if len(syncResp.Items) != 2 || syncResp.Items[0].LoudnessLUFS == nil || syncResp.Items[0].AlbumTruePeakDB == nil {
		t.Fatalf("missing loudness on sync tracks: %+v", syncResp.Items)
	}
}
