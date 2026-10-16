package analysis

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
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

const fakeVersion = "fake-v1"

// ---------- helpers ----------

func float32ToF16(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int((b>>23)&0xff) - 127 + 15
	frac := uint16((b >> 13) & 0x3ff)
	if f == 0 {
		return sign
	}
	if exp <= 0 || exp >= 31 {
		panic("test encoder only handles normal half values")
	}
	return sign | uint16(exp)<<10 | frac
}

func f16b64(values []float32) string {
	raw := make([]byte, 2*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint16(raw[2*i:], float32ToF16(v))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func vec(n int, v float32) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = v
	}
	return out
}

type fakeClient struct {
	mu          sync.Mutex
	health      Health
	healthErr   error
	analyzeErr  map[string]error // by path
	globalErr   error
	resultHook  func(*AnalyzeResult)
	fitErr      error
	analyzed    []AnalyzeRequest
	fitSamples  []FitSample
	labelsCalls int
}

func newFakeClient() *fakeClient {
	return &fakeClient{health: Health{Status: "ready", AnalysisVersion: fakeVersion, FullModeAvailable: true}, analyzeErr: map[string]error{}}
}

func (f *fakeClient) Health(context.Context) (Health, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health, f.healthErr
}

func (f *fakeClient) Labels(context.Context) (json.RawMessage, error) {
	f.mu.Lock()
	f.labelsCalls++
	f.mu.Unlock()
	return json.RawMessage(`{"genre":["g"],"heads":["voice"]}`), nil
}

func (f *fakeClient) Analyze(_ context.Context, req AnalyzeRequest) (AnalyzeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.analyzed = append(f.analyzed, req)
	if f.globalErr != nil {
		return AnalyzeResult{}, f.globalErr
	}
	if err := f.analyzeErr[req.Path]; err != nil {
		return AnalyzeResult{}, err
	}
	// voice head encodes the "truth" so the fake fit can recover it: arousal = 2 + 6*voice
	voice := float64(len(f.analyzed)%10) / 10
	r := AnalyzeResult{AnalysisVersion: f.health.AnalysisVersion, Mode: req.Mode, DurationMs: 180000, Windows: 3, Patches: 87,
		Genre: f16b64(vec(GenreDims, 0.01)), MoodTheme: f16b64(vec(MoodThemeDims, 0.02)), Instrument: f16b64(vec(InstrumentDims, 0.03)),
		Embedding: f16b64(vec(EmbeddingDims, 0.5)), Heads: map[string]float64{"voice": voice, "danceable": 0.2}}
	if req.Mode == "full" {
		v, a := 4.0, 2+6*voice
		r.Valence, r.Arousal = &v, &a
	}
	if f.resultHook != nil {
		f.resultHook(&r)
	}
	return r, nil
}

func (f *fakeClient) Fit(_ context.Context, samples []FitSample) (FitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fitSamples = samples
	if f.fitErr != nil {
		return FitResult{}, f.fitErr
	}
	w := make([]byte, 8)
	binary.LittleEndian.PutUint32(w[0:], math.Float32bits(0))
	binary.LittleEndian.PutUint32(w[4:], math.Float32bits(6))
	return FitResult{AnalysisVersion: f.health.AnalysisVersion, Kind: "heads", Features: []string{"voice"}, Dims: 1,
		Weights: base64.StdEncoding.EncodeToString(w), Bias: [2]float64{4, 2}, Samples: len(samples), Metrics: json.RawMessage(`{"heads":{"arousalR":0.99}}`)}, nil
}

type fixture struct {
	store  *storage.Store
	svc    *Service
	client *fakeClient
	now    time.Time
	busy   string
	ids    []int64
	ctx    context.Context
}

func newFixture(t *testing.T, tracks int, opts Options) *fixture {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "a.db"))
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
		if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("A%02d/t.flac", i), FileSize: int64(10 + i), ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("T%02d", i), Album: fmt.Sprintf("A%02d", i), Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
	}
	fx := &fixture{store: store, client: newFakeClient(), now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), ctx: ctx}
	if opts.Client == nil {
		opts.Client = fx.client
	}
	opts.Now = func() time.Time { return fx.now }
	opts.Busy = func() string { return fx.busy }
	fx.svc = New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	return fx
}

func (fx *fixture) enable(t *testing.T) {
	t.Helper()
	if err := fx.store.SaveAudioAnalysisSettings(fx.ctx, storage.AudioAnalysisSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

// drain runs steps until the worker asks to wait; returns the last delay and state.
func (fx *fixture) drain(t *testing.T, maxSteps int) (time.Duration, State) {
	t.Helper()
	for i := 0; i < maxSteps; i++ {
		if d := fx.svc.Step(fx.ctx); d > 0 {
			return d, fx.svc.state
		}
	}
	t.Fatalf("worker did not settle within %d steps (state %s)", maxSteps, fx.svc.state)
	return 0, ""
}

func (fx *fixture) stats(t *testing.T) storage.AudioAnalysisStats {
	t.Helper()
	st, err := fx.store.AudioAnalysisStats(fx.ctx, fakeVersion, fx.now)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// ---------- tests ----------

func TestNotConfiguredAndDisabledDoNothing(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, 2, Options{})
	unconfigured := New(fx.store, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{})
	if d := unconfigured.Step(ctx); d != time.Hour || unconfigured.state != StateNotConfigured || unconfigured.Configured() {
		t.Fatalf("unconfigured: delay=%s state=%s", d, unconfigured.state)
	}
	if d := fx.svc.Step(ctx); d != time.Hour || fx.svc.state != StateDisabled {
		t.Fatalf("disabled by default: delay=%s state=%s", d, fx.svc.state)
	}
	if len(fx.client.analyzed) != 0 {
		t.Fatal("disabled service must not analyse")
	}
	_ = fx.store.SaveAudioAnalysisSettings(ctx, storage.AudioAnalysisSettings{Enabled: true, Paused: true})
	if fx.svc.Step(ctx); fx.svc.state != StatePaused || len(fx.client.analyzed) != 0 {
		t.Fatalf("paused: state=%s", fx.svc.state)
	}
}

func TestCalibrationFitAndFastAnalysis(t *testing.T) {
	fx := newFixture(t, 50, Options{CalibrationTarget: 45, MinCalibration: 40})
	fx.enable(t)
	delay, state := fx.drain(t, 200)
	if state != StateIdle || delay != 30*time.Minute {
		t.Fatalf("final state=%s delay=%s, want idle", state, delay)
	}
	full, fast := 0, 0
	for _, r := range fx.client.analyzed {
		if r.Mode == "full" {
			full++
		} else {
			fast++
		}
		if r.DurationMs != 0 || r.Channels != 0 || !strings.HasSuffix(r.Path, "/t.flac") {
			t.Fatalf("unexpected request %+v", r)
		}
	}
	if full != 45 || fast != 5 {
		t.Fatalf("full=%d fast=%d, want 45 calibration + 5 fast", full, fast)
	}
	if len(fx.client.fitSamples) != 45 || fx.client.labelsCalls != 1 {
		t.Fatalf("fit samples=%d labels calls=%d", len(fx.client.fitSamples), fx.client.labelsCalls)
	}
	st := fx.stats(t)
	if st.Eligible != 50 || st.Analyzed != 50 || st.Calibrated != 45 || st.MissingAV != 0 {
		t.Fatalf("stats = %+v", st)
	}
	model, found, _ := fx.store.ActiveAudioAVModel(fx.ctx, fakeVersion)
	if !found || model.Kind != "heads" || model.Samples != 45 {
		t.Fatalf("model = %+v found=%v", model, found)
	}
	// fast tracks got regression values: arousal = 2 + 6*voice (clamped to 1–9)
	pendingCheck := 0
	for i := 1; i <= 50; i++ {
		f, ok, _ := fx.store.TrackAudioFeaturesByID(fx.ctx, int64(i))
		if !ok || f.Arousal == nil {
			continue
		}
		var heads map[string]float64
		_ = json.Unmarshal([]byte(f.HeadsJSON), &heads)
		if f.AVSource == "regression" {
			pendingCheck++
			if math.Abs(*f.Arousal-(2+6*heads["voice"])) > 1e-6 || *f.Valence != 4 {
				t.Fatalf("regression value %v/%v for voice %v", *f.Valence, *f.Arousal, heads["voice"])
			}
		}
	}
	if pendingCheck != 5 {
		t.Fatalf("regression tracks = %d, want 5", pendingCheck)
	}
	snap, err := fx.svc.Snapshot(fx.ctx)
	if err != nil || snap.Version != fakeVersion || snap.Pending != 0 || snap.Calibration.ModelKind != "heads" || snap.Processed != 50 {
		t.Fatalf("snapshot = %+v err=%v", snap, err)
	}
	// A new file arrives and is analysed fast with regression; the scan hook only needs Wake.
	lib, _ := fx.store.LibraryByRoot(fx.ctx, "/music")
	_ = fx.store.ImportTrack(fx.ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "New/n.flac", FileSize: 5, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "New", Album: "New", Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 1}})
	fx.svc.Wake()
	if _, state = fx.drain(t, 5); state != StateIdle {
		t.Fatalf("state=%s", state)
	}
	if last := fx.client.analyzed[len(fx.client.analyzed)-1]; last.Path != "New/n.flac" || last.Mode != "fast" {
		t.Fatalf("new track request = %+v", last)
	}
}

func TestFitFailureKeepsAnalysingAndBackfillsLater(t *testing.T) {
	fx := newFixture(t, 45, Options{CalibrationTarget: 40, MinCalibration: 40})
	fx.enable(t)
	fx.client.fitErr = &APIError{Status: 422, Code: "fit_failed", Message: "nope"}
	if _, state := fx.drain(t, 200); state != StateIdle {
		t.Fatalf("state = %s", state)
	}
	if st := fx.stats(t); st.Analyzed != 45 || st.MissingAV != 5 {
		t.Fatalf("stats = %+v, want 5 tracks without AV", st)
	}
	if snap, _ := fx.svc.Snapshot(fx.ctx); !strings.Contains(snap.LastError, "校准失败") {
		t.Fatalf("last error = %q", snap.LastError)
	}
	// Within the retry delay nothing is fitted again.
	fx.client.fitErr = nil
	fx.now = fx.now.Add(30 * time.Minute)
	fx.drain(t, 10)
	if _, found, _ := fx.store.ActiveAudioAVModel(fx.ctx, fakeVersion); found {
		t.Fatal("fit must wait for the retry delay")
	}
	fx.now = fx.now.Add(time.Hour)
	fx.drain(t, 10)
	if st := fx.stats(t); st.MissingAV != 0 {
		t.Fatalf("backfill expected after successful fit, stats = %+v", st)
	}
}

func TestFullModeUnavailableSkipsCalibration(t *testing.T) {
	fx := newFixture(t, 5, Options{})
	fx.client.health.FullModeAvailable = false
	fx.enable(t)
	fx.drain(t, 20)
	for _, r := range fx.client.analyzed {
		if r.Mode != "fast" {
			t.Fatalf("full mode must not be used: %+v", r)
		}
	}
	if st := fx.stats(t); st.Analyzed != 5 || st.MissingAV != 5 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestErrorHandling(t *testing.T) {
	fx := newFixture(t, 3, Options{})
	fx.client.health.FullModeAvailable = false
	fx.enable(t)

	// Permanent: recorded, worker moves on to the next track.
	fx.client.analyzeErr["A02/t.flac"] = &APIError{Status: 422, Code: "decode_failed", Message: "broken"}
	if d := fx.svc.Step(fx.ctx); d != 0 {
		t.Fatalf("permanent failure delay = %s", d)
	}
	failures, _ := fx.store.AudioAnalysisFailures(fx.ctx, fakeVersion, 10)
	if len(failures) != 1 || !failures[0].Permanent {
		t.Fatalf("failures = %+v", failures)
	}

	// Busy: wait Retry-After, no failure.
	fx.client.analyzeErr["A01/t.flac"] = &APIError{Status: 503, Code: "busy", RetryAfter: 2 * time.Second}
	if d := fx.svc.Step(fx.ctx); d != 2*time.Second {
		t.Fatalf("busy delay = %s", d)
	}
	// 401: unavailable with a token hint.
	fx.client.analyzeErr["A01/t.flac"] = &APIError{Status: 401, Code: "unauthorized"}
	if d := fx.svc.Step(fx.ctx); d != 5*time.Minute || fx.svc.state != StateAnalyzerUnavailable || !strings.Contains(fx.svc.detail, "ANALYZER_TOKEN") {
		t.Fatalf("401: delay=%s state=%s detail=%s", d, fx.svc.state, fx.svc.detail)
	}
	// Connection error: whole-service backoff, not a track failure.
	delete(fx.client.analyzeErr, "A01/t.flac")
	fx.client.globalErr = errors.New("connection refused")
	if d := fx.svc.Step(fx.ctx); d != 30*time.Second || fx.svc.state != StateAnalyzerUnavailable {
		t.Fatalf("network: delay=%s state=%s", d, fx.svc.state)
	}
	if failures, _ = fx.store.AudioAnalysisFailures(fx.ctx, fakeVersion, 10); len(failures) != 1 {
		t.Fatalf("connection errors must not be recorded per track: %+v", failures)
	}
	// Health failures back off exponentially.
	fx.client.globalErr = nil
	fx.client.healthErr = errors.New("dial tcp: refused")
	d1 := fx.svc.Step(fx.ctx)
	d2 := fx.svc.Step(fx.ctx)
	if d1 != 30*time.Second || d2 != time.Minute {
		t.Fatalf("health backoff %s, %s", d1, d2)
	}
	fx.client.healthErr = nil

	// Invalid vectors: transient failure.
	fx.client.resultHook = func(r *AnalyzeResult) { r.Embedding = "AAAA" }
	fx.svc.Step(fx.ctx)
	failures, _ = fx.store.AudioAnalysisFailures(fx.ctx, fakeVersion, 10)
	if len(failures) != 2 {
		t.Fatalf("invalid result must be recorded: %+v", failures)
	}
	// Version change between health and analysis: re-check, nothing stored.
	fx.client.resultHook = func(r *AnalyzeResult) { r.AnalysisVersion = "other" }
	if d := fx.svc.Step(fx.ctx); d != 0 || fx.svc.health != nil {
		t.Fatal("version mismatch must invalidate health")
	}
	fx.client.resultHook = nil

	// Retry clears failures; transient one is analysed again.
	if n, err := fx.svc.RetryFailures(fx.ctx); err != nil || n != 2 {
		t.Fatalf("retry: n=%d err=%v", n, err)
	}
	delete(fx.client.analyzeErr, "A02/t.flac")
	if _, state := fx.drain(t, 10); state != StateIdle {
		t.Fatalf("state=%s", state)
	}
	if st := fx.stats(t); st.Analyzed != 3 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestYieldsWhileBusyAndOutsideWindow(t *testing.T) {
	fx := newFixture(t, 1, Options{})
	fx.enable(t)
	fx.busy = "转码进行中"
	if d := fx.svc.Step(fx.ctx); d != 15*time.Second || fx.svc.state != StateYielding || fx.svc.detail != "转码进行中" {
		t.Fatalf("busy: delay=%s state=%s", d, fx.svc.state)
	}
	fx.busy = ""
	start, end := 60, 7*60 // 01:00–07:00 UTC+8; fixture time is 20:00 UTC+8
	_ = fx.store.SaveAudioAnalysisSettings(fx.ctx, storage.AudioAnalysisSettings{Enabled: true, WindowStart: &start, WindowEnd: &end})
	if d := fx.svc.Step(fx.ctx); d != 10*time.Minute || fx.svc.state != StateWaitingWindow || fx.svc.detail != "01:00" {
		t.Fatalf("window: delay=%s state=%s detail=%s", d, fx.svc.state, fx.svc.detail)
	}
	if len(fx.client.analyzed) != 0 {
		t.Fatal("must not analyse while yielding or outside the window")
	}
}

func TestWindowWait(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*3600)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 16, h, m, 0, 0, zone) }
	p := func(v int) *int { return &v }
	cases := []struct {
		now        time.Time
		start, end *int
		want       time.Duration
	}{
		{at(12, 0), nil, nil, 0},
		{at(2, 0), p(60), p(420), 0},
		{at(0, 30), p(60), p(420), 30 * time.Minute},
		{at(7, 0), p(60), p(420), 18 * time.Hour},
		{at(23, 30), p(22 * 60), p(6 * 60), 0},            // overnight window
		{at(5, 59), p(22 * 60), p(6 * 60), 0},             // overnight window, morning side
		{at(6, 0), p(22 * 60), p(6 * 60), 16 * time.Hour}, // closed at end
	}
	for _, c := range cases {
		if got := WindowWait(c.now, c.start, c.end, zone); got != c.want {
			t.Fatalf("WindowWait(%s) = %s, want %s", c.now.Format("15:04"), got, c.want)
		}
	}
}

func TestFloat16(t *testing.T) {
	for _, c := range []struct {
		h    uint16
		want float32
	}{{0x3C00, 1}, {0xC000, -2}, {0x3800, 0.5}, {0x0000, 0}, {0x7BFF, 65504}, {0x0001, 5.960464477539063e-08}} {
		if got := Float16ToFloat32(c.h); got != c.want {
			t.Fatalf("Float16ToFloat32(%#x) = %v, want %v", c.h, got, c.want)
		}
	}
	if !math.IsInf(float64(Float16ToFloat32(0x7C00)), 1) || !math.IsNaN(float64(Float16ToFloat32(0x7E00))) {
		t.Fatal("inf/nan conversion")
	}
	nan := base64.StdEncoding.EncodeToString([]byte{0x00, 0x7E})
	if _, err := decodeF16(nan, 1); err == nil {
		t.Fatal("NaN vectors must be rejected")
	}
}

func TestHTTPClient(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ready","analysisVersion":"v","fullModeAvailable":true}`))
		case "/v1/analyze":
			var req AnalyzeRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Path == "busy.flac" {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":{"code":"busy","message":"x"}}`))
				return
			}
			if req.Path == "bad.flac" {
				w.WriteHeader(422)
				_, _ = w.Write([]byte(`{"error":{"code":"decode_failed","message":"broken"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"analysisVersion":"v","mode":"fast"}`))
		default:
			w.WriteHeader(500)
			_, _ = w.Write([]byte("not json"))
		}
	}))
	defer server.Close()
	c := NewHTTPClient(server.URL, "s3cret")
	ctx := context.Background()
	if h, err := c.Health(ctx); err != nil || h.AnalysisVersion != "v" || gotAuth != "Bearer s3cret" {
		t.Fatalf("health=%+v err=%v auth=%q", h, err, gotAuth)
	}
	if r, err := c.Analyze(ctx, AnalyzeRequest{Path: "ok.flac", Mode: "fast"}); err != nil || r.AnalysisVersion != "v" {
		t.Fatalf("analyze=%+v err=%v", r, err)
	}
	var apiErr *APIError
	if _, err := c.Analyze(ctx, AnalyzeRequest{Path: "busy.flac"}); !errors.As(err, &apiErr) || apiErr.Code != "busy" || apiErr.RetryAfter != 7*time.Second || apiErr.Permanent() {
		t.Fatalf("busy err=%v", err)
	}
	if _, err := c.Analyze(ctx, AnalyzeRequest{Path: "bad.flac"}); !errors.As(err, &apiErr) || !apiErr.Permanent() {
		t.Fatalf("decode err=%v", err)
	}
	if _, err := c.Fit(ctx, nil); !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Code != "http_error" {
		t.Fatalf("non-JSON error body: %v", err)
	}
}

func TestHTTPClientIgnoresEnvironmentProxy(t *testing.T) {
	var proxied bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = true
		http.Error(w, "proxy", http.StatusBadGateway)
	}))
	defer proxy.Close()
	analyzer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready","analysisVersion":"v1"}`))
	}))
	defer analyzer.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	// Use a non-loopback-looking host so ProxyFromEnvironment would apply.
	client := NewHTTPClient(strings.Replace(analyzer.URL, "127.0.0.1", "localtest.invalid", 1), "tok")
	transport := client.HTTP.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("analyzer client must not use environment proxies")
	}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, strings.TrimPrefix(analyzer.URL, "http://"))
	}
	h, err := client.Health(context.Background())
	if err != nil || h.Status != "ready" || proxied {
		t.Fatalf("health=%+v err=%v proxied=%v", h, err, proxied)
	}
}
