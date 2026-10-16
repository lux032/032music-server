package analysis

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// State is the worker's current activity.
type State string

const (
	StateNotConfigured       State = "not_configured" // MUSIC_SERVER_ANALYZER_URL is empty
	StateDisabled            State = "disabled"
	StatePaused              State = "paused"
	StateWaitingWindow       State = "waiting_window"
	StateYielding            State = "yielding" // transcoding or a library scan is running
	StateAnalyzerUnavailable State = "analyzer_unavailable"
	StateAnalyzerLoading     State = "analyzer_loading"
	StateCalibrating         State = "calibrating"
	StateFitting             State = "fitting"
	StateRunning             State = "running"
	StateIdle                State = "idle" // everything analysed; waiting for new tracks
	StateError               State = "error"
)

const (
	DefaultCalibrationTarget = 300
	DefaultMinCalibration    = 40
	healthTTL                = time.Minute
	statsTTL                 = 3 * time.Second
	backfillBatch            = 500
	fitRetryDelay            = time.Hour
)

// Options configures a Service.
type Options struct {
	// Client is nil when the sidecar is not deployed (MUSIC_SERVER_ANALYZER_URL empty).
	Client Client
	// Busy returns a non-empty reason while analysis should yield (transcoding, scans).
	Busy func() string
	// Zone is the time zone of the run window (the app uses a fixed UTC+8).
	Zone              *time.Location
	Now               func() time.Time
	CalibrationTarget int
	MinCalibration    int
}

// Current is the track being analysed right now.
type Current struct {
	TrackID   int64     `json:"trackId"`
	Title     string    `json:"title"`
	Path      string    `json:"path"`
	Mode      string    `json:"mode"`
	StartedAt time.Time `json:"startedAt"`
}

// Calibration summarises the arousal/valence calibration for the current version.
type Calibration struct {
	Target            int             `json:"target"`
	Done              int64           `json:"done"`
	FullModeAvailable bool            `json:"fullModeAvailable"`
	ModelKind         string          `json:"modelKind,omitempty"`
	ModelSamples      int             `json:"modelSamples,omitempty"`
	ModelCreatedAt    string          `json:"modelCreatedAt,omitempty"`
	Metrics           json.RawMessage `json:"metrics,omitempty"`
}

// Snapshot is the externally visible status (admin page, API).
type Snapshot struct {
	Configured     bool                          `json:"configured"`
	State          State                         `json:"state"`
	Detail         string                        `json:"detail,omitempty"`
	Settings       storage.AudioAnalysisSettings `json:"-"`
	Enabled        bool                          `json:"enabled"`
	Paused         bool                          `json:"paused"`
	WindowStart    *int                          `json:"windowStartMinute,omitempty"`
	WindowEnd      *int                          `json:"windowEndMinute,omitempty"`
	FirstEnabledAt string                        `json:"firstEnabledAt,omitempty"`
	Current        *Current                      `json:"current,omitempty"`
	Analyzer       *Health                       `json:"analyzer,omitempty"`
	Version        string                        `json:"analysisVersion,omitempty"`
	Stats          *storage.AudioAnalysisStats   `json:"stats,omitempty"`
	Pending        int64                         `json:"pending"`
	ETASeconds     int64                         `json:"etaSeconds,omitempty"`
	Calibration    *Calibration                  `json:"calibration,omitempty"`
	LastError      string                        `json:"lastError,omitempty"`
	LastErrorAt    *time.Time                    `json:"lastErrorAt,omitempty"`
	Processed      int64                         `json:"processedSinceStart"`
}

// Service is the background worker. One analysis runs at a time.
type Service struct {
	store  *storage.Store
	logger *slog.Logger
	opts   Options
	wake   chan struct{}
	wg     sync.WaitGroup

	mu          sync.Mutex
	state       State
	detail      string
	current     *Current
	health      *Health
	healthAt    time.Time
	healthFails int
	version     string
	lastErr     string
	lastErrAt   time.Time
	labelsSaved map[string]bool
	fitRetryAt  time.Time
	processed   int64
	avgMs       float64
	statsCache  *storage.AudioAnalysisStats
	statsKey    string
	statsAt     time.Time
	eligible    int64
	eligibleAt  time.Time
}

// New creates the service; call Start to run it.
func New(store *storage.Store, logger *slog.Logger, opts Options) *Service {
	if opts.Zone == nil {
		opts.Zone = time.FixedZone("UTC+8", 8*60*60)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Busy == nil {
		opts.Busy = func() string { return "" }
	}
	if opts.CalibrationTarget <= 0 {
		opts.CalibrationTarget = DefaultCalibrationTarget
	}
	if opts.MinCalibration <= 0 {
		opts.MinCalibration = DefaultMinCalibration
	}
	state := StateDisabled
	if opts.Client == nil {
		state = StateNotConfigured
	}
	return &Service{store: store, logger: logger, opts: opts, wake: make(chan struct{}, 1), state: state, labelsSaved: map[string]bool{}}
}

// Configured reports whether the sidecar address is set.
func (s *Service) Configured() bool { return s.opts.Client != nil }

// Start runs the worker until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	if v, err := s.store.LatestAudioAnalysisVersion(ctx); err == nil {
		s.mu.Lock()
		s.version = v
		s.mu.Unlock()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

// Wait blocks until the worker has stopped.
func (s *Service) Wait() { s.wg.Wait() }

// Wake asks the worker to re-evaluate now (settings changed, scan finished).
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) run(ctx context.Context) {
	for {
		delay := s.Step(ctx)
		if ctx.Err() != nil {
			return
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Service) set(state State, detail string) {
	s.mu.Lock()
	s.state, s.detail = state, detail
	s.mu.Unlock()
}

func (s *Service) fail(state State, err error) {
	s.mu.Lock()
	s.state, s.detail = state, err.Error()
	s.lastErr, s.lastErrAt = err.Error(), s.opts.Now()
	s.mu.Unlock()
}

// Step performs one unit of work and returns how long to wait before the next one
// (0 = continue immediately). Exported so tests can drive the worker synchronously.
func (s *Service) Step(ctx context.Context) time.Duration {
	if s.opts.Client == nil {
		s.set(StateNotConfigured, "")
		return time.Hour
	}
	settings, err := s.store.AudioAnalysisSettings(ctx)
	if err != nil {
		s.fail(StateError, fmt.Errorf("读取设置失败：%w", err))
		return time.Minute
	}
	if !settings.Enabled {
		s.set(StateDisabled, "")
		return time.Hour
	}
	if settings.Paused {
		s.set(StatePaused, "")
		return time.Hour
	}
	now := s.opts.Now()
	if wait := WindowWait(now, settings.WindowStart, settings.WindowEnd, s.opts.Zone); wait > 0 {
		s.set(StateWaitingWindow, FormatMinute(*settings.WindowStart))
		return min(wait, 10*time.Minute)
	}
	if reason := s.opts.Busy(); reason != "" {
		s.set(StateYielding, reason)
		return 15 * time.Second
	}
	health, delay, err := s.ensureHealth(ctx)
	if err != nil {
		s.fail(StateAnalyzerUnavailable, err)
		return delay
	}
	if health.Status != "ready" {
		if health.Status == "loading" {
			s.set(StateAnalyzerLoading, "")
		} else {
			s.fail(StateAnalyzerUnavailable, fmt.Errorf("分析服务异常：%s", health.Error))
		}
		s.invalidateHealth()
		return 30 * time.Second
	}
	version := health.AnalysisVersion
	s.ensureLabels(ctx, version)

	model, err := s.activeModel(ctx, version)
	if err != nil {
		s.fail(StateError, err)
		return time.Minute
	}
	if model != nil {
		if n := s.backfill(ctx, version, model); n > 0 {
			return 0
		}
	}
	if model == nil && health.FullModeAvailable {
		target, err := s.calibrationTarget(ctx, now)
		if err != nil {
			s.fail(StateError, err)
			return time.Minute
		}
		c, ok, err := s.store.NextAudioCalibrationCandidate(ctx, version, target, now)
		if err != nil {
			s.fail(StateError, err)
			return time.Minute
		}
		if ok {
			return s.analyze(ctx, c, version, "full", nil)
		}
		if now.After(s.fitRetryAt) {
			if fitted, delay := s.fit(ctx, version); fitted || delay > 0 {
				return delay
			}
		}
	}
	c, ok, err := s.store.NextAudioAnalysisCandidate(ctx, version, now)
	if err != nil {
		s.fail(StateError, err)
		return time.Minute
	}
	if !ok {
		s.set(StateIdle, "")
		return 30 * time.Minute
	}
	return s.analyze(ctx, c, version, "fast", model)
}

func (s *Service) invalidateHealth() {
	s.mu.Lock()
	s.health = nil
	s.mu.Unlock()
}

func (s *Service) ensureHealth(ctx context.Context) (Health, time.Duration, error) {
	s.mu.Lock()
	if s.health != nil && s.health.Status == "ready" && s.opts.Now().Sub(s.healthAt) < healthTTL {
		h := *s.health
		s.mu.Unlock()
		return h, 0, nil
	}
	s.mu.Unlock()
	h, err := s.opts.Client.Health(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.healthFails++
		backoff := min(30*time.Second<<min(s.healthFails-1, 5), 10*time.Minute)
		return h, backoff, fmt.Errorf("无法连接分析服务：%w", err)
	}
	if h.Status == "ready" && h.AnalysisVersion == "" {
		s.healthFails++
		return h, time.Minute, errors.New("分析服务没有返回分析版本")
	}
	s.healthFails = 0
	s.health, s.healthAt = &h, s.opts.Now()
	if h.AnalysisVersion != "" {
		s.version = h.AnalysisVersion
	}
	return h, 0, nil
}

func (s *Service) ensureLabels(ctx context.Context, version string) {
	s.mu.Lock()
	done := s.labelsSaved[version]
	s.mu.Unlock()
	if done {
		return
	}
	if _, found, err := s.store.AudioAnalysisLabels(ctx, version); err == nil && found {
		s.mu.Lock()
		s.labelsSaved[version] = true
		s.mu.Unlock()
		return
	}
	labels, err := s.opts.Client.Labels(ctx)
	if err != nil {
		s.logger.Warn("fetch analyzer labels", "error", err)
		return
	}
	if err := s.store.SaveAudioAnalysisLabels(ctx, version, string(labels)); err != nil {
		s.logger.Error("save analyzer labels", "error", err)
		return
	}
	s.mu.Lock()
	s.labelsSaved[version] = true
	s.mu.Unlock()
}

func (s *Service) activeModel(ctx context.Context, version string) (*regressionModel, error) {
	m, found, err := s.store.ActiveAudioAVModel(ctx, version)
	if err != nil || !found {
		return nil, err
	}
	r, err := newRegressionModel(m)
	if err != nil {
		s.logger.Error("invalid arousal/valence model; recalibration needed", "id", m.ID, "error", err)
		return nil, nil
	}
	return r, nil
}

// calibrationTarget is min(target, eligible tracks); the eligible count is cached briefly.
func (s *Service) calibrationTarget(ctx context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	eligible, at := s.eligible, s.eligibleAt
	s.mu.Unlock()
	if at.IsZero() || now.Sub(at) > 5*time.Minute {
		stats, err := s.store.AudioAnalysisStats(ctx, "", now)
		if err != nil {
			return 0, err
		}
		eligible = stats.Eligible
		s.mu.Lock()
		s.eligible, s.eligibleAt = eligible, now
		s.mu.Unlock()
	}
	return int(min(int64(s.opts.CalibrationTarget), eligible)), nil
}

func (s *Service) backfill(ctx context.Context, version string, model *regressionModel) int {
	pending, err := s.store.AudioAVPendingTracks(ctx, version, backfillBatch)
	if err != nil || len(pending) == 0 {
		return 0
	}
	values := make([]storage.AudioAVValue, 0, len(pending))
	for _, p := range pending {
		v, a, err := model.predict(p.Embedding, p.HeadsJSON)
		if err != nil {
			s.logger.Warn("arousal/valence regression skipped", "trackId", p.TrackID, "error", err)
			continue
		}
		values = append(values, storage.AudioAVValue{TrackID: p.TrackID, Valence: v, Arousal: a})
	}
	if err := s.store.ApplyAudioAVRegression(ctx, version, model.id, values); err != nil {
		s.logger.Error("apply arousal/valence regression", "error", err)
		return 0
	}
	return len(values)
}

func (s *Service) fit(ctx context.Context, version string) (bool, time.Duration) {
	samples, err := s.store.AudioCalibrationSamples(ctx, version)
	if err != nil {
		s.fail(StateError, err)
		return false, time.Minute
	}
	if len(samples) < s.opts.MinCalibration {
		return false, 0 // too few (small library or failures): continue in fast mode
	}
	s.set(StateFitting, fmt.Sprintf("%d 个校准样本", len(samples)))
	payload := make([]FitSample, 0, len(samples))
	for _, v := range samples {
		var heads map[string]float64
		if err := json.Unmarshal([]byte(v.HeadsJSON), &heads); err != nil {
			continue
		}
		payload = append(payload, FitSample{Embedding: base64.StdEncoding.EncodeToString(v.Embedding), Heads: heads, Valence: v.Valence, Arousal: v.Arousal})
	}
	res, err := s.opts.Client.Fit(ctx, payload)
	if err == nil {
		err = validateFit(res, version)
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, 0
		}
		s.mu.Lock()
		s.fitRetryAt = s.opts.Now().Add(fitRetryDelay)
		s.mu.Unlock()
		s.fail(StateError, fmt.Errorf("唤醒度/效价校准失败（1 小时后重试）：%w", err))
		return false, 0
	}
	weights, _ := base64.StdEncoding.DecodeString(res.Weights)
	id, err := s.store.SaveAudioAVModel(ctx, storage.AudioAVModel{AnalysisVersion: version, Kind: res.Kind, Features: res.Features, Dims: res.Dims,
		Weights: weights, BiasValence: res.Bias[0], BiasArousal: res.Bias[1], Samples: res.Samples, MetricsJSON: string(res.Metrics)})
	if err != nil {
		s.fail(StateError, err)
		return false, time.Minute
	}
	s.logger.Info("arousal/valence calibration fitted", "modelId", id, "kind", res.Kind, "samples", res.Samples, "metrics", string(res.Metrics))
	return true, 0
}

func validateFit(r FitResult, version string) error {
	if r.AnalysisVersion != "" && r.AnalysisVersion != version {
		return fmt.Errorf("model version %q does not match %q", r.AnalysisVersion, version)
	}
	if r.Kind != "embedding" && r.Kind != "heads" {
		return fmt.Errorf("unknown model kind %q", r.Kind)
	}
	raw, err := base64.StdEncoding.DecodeString(r.Weights)
	if err != nil || r.Dims <= 0 || len(raw) != r.Dims*8 {
		return errors.New("model weights do not match dims")
	}
	for i := 0; i < r.Dims*2; i++ {
		if f := math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:])); math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return errors.New("model weights contain non-finite values")
		}
	}
	if !json.Valid(r.Metrics) {
		return errors.New("invalid metrics")
	}
	return nil
}

func (s *Service) analyze(ctx context.Context, c storage.AudioAnalysisCandidate, version, mode string, model *regressionModel) time.Duration {
	state := StateRunning
	if mode == "full" {
		state = StateCalibrating
	}
	started := s.opts.Now()
	s.mu.Lock()
	s.state, s.detail = state, ""
	s.current = &Current{TrackID: c.TrackID, Title: c.Title, Path: c.RelativePath, Mode: mode, StartedAt: started}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.current = nil
		s.mu.Unlock()
	}()

	res, err := s.opts.Client.Analyze(ctx, AnalyzeRequest{Path: c.RelativePath, DurationMs: c.DurationMs, Channels: c.Channels, Mode: mode})
	if ctx.Err() != nil {
		return 0
	}
	if err != nil {
		return s.handleAnalyzeError(ctx, c, version, err)
	}
	if res.AnalysisVersion != version {
		// The sidecar was upgraded between health check and analysis: re-check first.
		s.invalidateHealth()
		return 0
	}
	f, err := buildFeatures(c, version, mode, res)
	if err != nil {
		s.recordFailure(ctx, c, version, fmt.Errorf("分析结果无效：%w", err), false)
		return time.Second
	}
	if mode == "fast" && model != nil {
		if v, a, err := model.predict(f.Embedding, f.HeadsJSON); err == nil {
			f.Valence, f.Arousal, f.AVSource, f.AVModelID = &v, &a, "regression", &model.id
		}
	}
	elapsed := s.opts.Now().Sub(started)
	f.AnalyzeMs = elapsed.Milliseconds()
	if err := s.store.SaveTrackAudioFeatures(ctx, f); err != nil {
		s.fail(StateError, err)
		return time.Minute
	}
	s.mu.Lock()
	s.processed++
	if mode == "fast" {
		if s.avgMs == 0 {
			s.avgMs = float64(f.AnalyzeMs)
		} else {
			s.avgMs = 0.9*s.avgMs + 0.1*float64(f.AnalyzeMs)
		}
	}
	s.statsAt = time.Time{}
	s.mu.Unlock()
	return 0
}

func (s *Service) handleAnalyzeError(ctx context.Context, c storage.AudioAnalysisCandidate, version string, err error) time.Duration {
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Permanent():
		s.recordFailure(ctx, c, version, err, true)
		return 0
	case errors.As(err, &apiErr) && (apiErr.Code == "busy" || apiErr.Code == "loading" || apiErr.Status == 503):
		if apiErr.Code != "busy" {
			s.invalidateHealth()
		}
		if apiErr.RetryAfter > 0 {
			return apiErr.RetryAfter
		}
		return 5 * time.Second
	case errors.As(err, &apiErr) && apiErr.Status == 401:
		s.invalidateHealth()
		s.fail(StateAnalyzerUnavailable, errors.New("分析服务拒绝访问：请检查 MUSIC_SERVER_ANALYZER_TOKEN 与分析服务的 ANALYZER_TOKEN 是否一致"))
		return 5 * time.Minute
	case errors.As(err, &apiErr) && apiErr.Code == "full_mode_unavailable":
		s.invalidateHealth()
		return 0
	case errors.As(err, &apiErr):
		s.recordFailure(ctx, c, version, err, false)
		return 2 * time.Second
	case errors.Is(err, context.DeadlineExceeded):
		// This file timed out on its own; other files may still work.
		s.recordFailure(ctx, c, version, fmt.Errorf("分析超时：%w", err), false)
		return 0
	default:
		// Connection-level failure: not the track's fault; back off as a whole.
		s.invalidateHealth()
		s.fail(StateAnalyzerUnavailable, fmt.Errorf("无法连接分析服务：%w", err))
		return 30 * time.Second
	}
}

func (s *Service) recordFailure(ctx context.Context, c storage.AudioAnalysisCandidate, version string, err error, permanent bool) {
	s.mu.Lock()
	s.lastErr, s.lastErrAt = fmt.Sprintf("%s：%v", c.RelativePath, err), s.opts.Now()
	s.statsAt = time.Time{}
	s.mu.Unlock()
	if rerr := s.store.RecordAudioAnalysisFailure(ctx, c, version, err.Error(), permanent, s.opts.Now()); rerr != nil {
		s.logger.Error("record analysis failure", "trackId", c.TrackID, "error", rerr)
	}
	s.logger.Warn("track analysis failed", "trackId", c.TrackID, "path", c.RelativePath, "permanent", permanent, "error", err)
}

func buildFeatures(c storage.AudioAnalysisCandidate, version, mode string, r AnalyzeResult) (storage.TrackAudioFeatures, error) {
	f := storage.TrackAudioFeatures{TrackID: c.TrackID, Fingerprint: c.Fingerprint, AnalysisVersion: version, Mode: mode,
		DurationMs: r.DurationMs, Windows: r.Windows, Patches: r.Patches}
	var err error
	if f.Genre, err = decodeF16(r.Genre, GenreDims); err != nil {
		return f, fmt.Errorf("genre: %w", err)
	}
	if f.MoodTheme, err = decodeF16(r.MoodTheme, MoodThemeDims); err != nil {
		return f, fmt.Errorf("moodtheme: %w", err)
	}
	if f.Instrument, err = decodeF16(r.Instrument, InstrumentDims); err != nil {
		return f, fmt.Errorf("instrument: %w", err)
	}
	if f.Embedding, err = decodeF16(r.Embedding, EmbeddingDims); err != nil {
		return f, fmt.Errorf("embedding: %w", err)
	}
	if len(r.Heads) == 0 {
		return f, errors.New("heads missing")
	}
	for k, v := range r.Heads {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return f, fmt.Errorf("head %s is not finite", k)
		}
	}
	heads, _ := json.Marshal(r.Heads)
	f.HeadsJSON = string(heads)
	if f.DurationMs < 0 || f.Windows < 0 || f.Patches < 0 {
		return f, errors.New("negative counters")
	}
	if mode == "full" {
		if r.Valence == nil || r.Arousal == nil || !finite(*r.Valence) || !finite(*r.Arousal) {
			return f, errors.New("full mode result has no valence/arousal")
		}
		v, a := *r.Valence, *r.Arousal
		f.MusicnnValence, f.MusicnnArousal, f.Valence, f.Arousal, f.AVSource = &v, &a, &v, &a, "musicnn"
	}
	return f, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Snapshot returns the current status. Database-derived figures are cached for a few seconds.
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	settings, err := s.store.AudioAnalysisSettings(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	snap := Snapshot{Configured: s.opts.Client != nil, State: s.state, Detail: s.detail, Settings: settings, Enabled: settings.Enabled,
		Paused: settings.Paused, WindowStart: settings.WindowStart, WindowEnd: settings.WindowEnd, FirstEnabledAt: settings.FirstEnabledAt,
		Version: s.version, LastError: s.lastErr, Processed: s.processed}
	if s.current != nil {
		cur := *s.current
		snap.Current = &cur
	}
	if s.health != nil {
		h := *s.health
		snap.Analyzer = &h
	}
	if !s.lastErrAt.IsZero() {
		at := s.lastErrAt
		snap.LastErrorAt = &at
	}
	avgMs := s.avgMs
	cached, key, at := s.statsCache, s.statsKey, s.statsAt
	s.mu.Unlock()
	// The worker only refreshes its state on the next step; reflect settings changes immediately.
	switch {
	case !snap.Configured:
		snap.State = StateNotConfigured
	case !settings.Enabled:
		snap.State, snap.Detail = StateDisabled, ""
	case settings.Paused && snap.Current == nil:
		snap.State, snap.Detail = StatePaused, ""
	}
	if snap.Version == "" {
		return snap, nil
	}
	now := s.opts.Now()
	stats := cached
	if stats == nil || key != snap.Version || now.Sub(at) > statsTTL {
		value, err := s.store.AudioAnalysisStats(ctx, snap.Version, now)
		if err != nil {
			return snap, err
		}
		stats = &value
		s.mu.Lock()
		s.statsCache, s.statsKey, s.statsAt = stats, snap.Version, now
		s.mu.Unlock()
	}
	snap.Stats = stats
	snap.Pending = max(stats.Eligible-stats.Analyzed-stats.PermanentFailures, 0)
	if avgMs == 0 {
		avgMs = float64(stats.RecentAvgMs)
	}
	if avgMs > 0 && snap.Pending > 0 {
		snap.ETASeconds = int64(float64(snap.Pending) * avgMs / 1000)
	}
	cal := &Calibration{Target: int(min(int64(s.opts.CalibrationTarget), stats.Eligible)), Done: stats.Calibrated, FullModeAvailable: snap.Analyzer == nil || snap.Analyzer.FullModeAvailable}
	if m, found, err := s.store.ActiveAudioAVModel(ctx, snap.Version); err == nil && found {
		cal.ModelKind, cal.ModelSamples, cal.ModelCreatedAt = m.Kind, m.Samples, m.CreatedAt
		if json.Valid([]byte(m.MetricsJSON)) {
			cal.Metrics = json.RawMessage(m.MetricsJSON)
		}
	}
	snap.Calibration = cal
	return snap, nil
}

// RetryFailures clears all failure records and wakes the worker.
func (s *Service) RetryFailures(ctx context.Context) (int64, error) {
	n, err := s.store.ClearAudioAnalysisFailures(ctx)
	if err == nil {
		s.mu.Lock()
		s.statsAt = time.Time{}
		s.mu.Unlock()
		s.Wake()
	}
	return n, err
}

// Failures lists active failures for the current version.
func (s *Service) Failures(ctx context.Context, limit int) ([]storage.AudioAnalysisFailure, error) {
	s.mu.Lock()
	version := s.version
	s.mu.Unlock()
	if version == "" {
		return nil, nil
	}
	return s.store.AudioAnalysisFailures(ctx, version, limit)
}

// WindowWait returns how long until the run window opens (0 = inside the window or no window).
func WindowWait(now time.Time, start, end *int, zone *time.Location) time.Duration {
	if start == nil || end == nil || *start == *end {
		return 0
	}
	local := now.In(zone)
	minute := local.Hour()*60 + local.Minute()
	inside := (*start < *end && minute >= *start && minute < *end) || (*start > *end && (minute >= *start || minute < *end))
	if inside {
		return 0
	}
	open := time.Date(local.Year(), local.Month(), local.Day(), *start/60, *start%60, 0, 0, zone)
	if !open.After(local) {
		open = open.Add(24 * time.Hour)
	}
	return open.Sub(local)
}

// FormatMinute formats minutes since midnight as HH:MM.
func FormatMinute(m int) string { return fmt.Sprintf("%02d:%02d", m/60, m%60) }
