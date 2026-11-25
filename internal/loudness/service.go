package loudness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// State describes the current lifecycle state of the background loudness worker pool.
type State string

const (
	StateUnavailable State = "unavailable"
	StatePaused      State = "paused"
	StateYielding    State = "yielding"
	StateRunning     State = "running"
	StateIdle        State = "idle"
	StateError       State = "error"
)

const (
	maxWorkers     = 4
	claimBatchSize = 24
	statsCacheTTL  = 3 * time.Second
	idlePollDelay  = 5 * time.Minute
	yieldDelay     = 10 * time.Second
	pausedDelay    = time.Hour
	errorDelay     = time.Minute
)

// Options configures a loudness background Service.
type Options struct {
	Measurer Measurer
	// Workers controls concurrency (1..4). 0 defaults to clamp(NumCPU/2, 1, 2).
	Workers int
	// Busy returns a non-empty reason while loudness analysis should yield (scan, transcode).
	Busy func() string
	Now  func() time.Time
}

// Current identifies one track actively being measured by a worker.
type Current struct {
	AudioFileID int64     `json:"audioFileId"`
	TrackID     int64     `json:"trackId"`
	Title       string    `json:"title"`
	Path        string    `json:"path"`
	StartedAt   time.Time `json:"startedAt"`
}

// Snapshot is the observable status exposed to the admin page and status JSON endpoints.
type Snapshot struct {
	Available   bool                        `json:"available"`
	State       State                       `json:"state"`
	Detail      string                      `json:"detail,omitempty"`
	Paused      bool                        `json:"paused"`
	Workers     int                         `json:"workers"`
	Current     []Current                   `json:"current,omitempty"`
	Stats       *storage.AudioLoudnessStats `json:"stats,omitempty"`
	Pending     int64                       `json:"pending"`
	ETASeconds  int64                       `json:"etaSeconds,omitempty"`
	LastError   string                      `json:"lastError,omitempty"`
	LastErrorAt *time.Time                  `json:"lastErrorAt,omitempty"`
	Processed   int64                       `json:"processedSinceStart"`
}

// DefaultWorkers computes clamp(NumCPU/2, 1, 2), leaving headroom on low-power NAS CPUs.
func DefaultWorkers(numCPU int) int {
	n := numCPU / 2
	if n < 1 {
		return 1
	}
	if n > 2 {
		return 2
	}
	return n
}

// ClampWorkers bounds configured concurrency to [1, 4], using DefaultWorkers when <= 0.
func ClampWorkers(configured int) int {
	if configured <= 0 {
		return DefaultWorkers(runtime.NumCPU())
	}
	if configured > maxWorkers {
		return maxWorkers
	}
	return configured
}

// Service manages background EBU R128 loudness measurement workers.
type Service struct {
	store   *storage.Store
	logger  *slog.Logger
	opts    Options
	workers int
	wg      sync.WaitGroup

	claimMu    sync.Mutex
	mu         sync.Mutex
	wakeCh     chan struct{}
	state      State
	detail     string
	paused     bool
	inFlight   map[int64]Current
	buffer     []storage.AudioLoudnessCandidate
	lastErr    string
	lastErrAt  time.Time
	processed  int64
	avgMs      float64
	statsCache *storage.AudioLoudnessStats
	statsAt    time.Time
}

// New creates a loudness Service; call Start to launch background workers.
func New(store *storage.Store, logger *slog.Logger, opts Options) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Busy == nil {
		opts.Busy = func() string { return "" }
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	state := StateIdle
	if opts.Measurer == nil || !opts.Measurer.Available() {
		state = StateUnavailable
	}
	return &Service{
		store:    store,
		logger:   logger,
		opts:     opts,
		workers:  ClampWorkers(opts.Workers),
		wakeCh:   make(chan struct{}),
		state:    state,
		inFlight: map[int64]Current{},
	}
}

// Available reports whether the configured measurer (ffmpeg ebur128) is usable.
func (s *Service) Available() bool {
	return s != nil && s.opts.Measurer != nil && s.opts.Measurer.Available()
}

// Workers returns the effective worker pool size.
func (s *Service) Workers() int { return s.workers }

// Start spawns the configured worker pool until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	if !s.Available() {
		return
	}
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.runWorker(ctx)
		}()
	}
}

// Wait blocks until all worker goroutines have exited.
func (s *Service) Wait() { s.wg.Wait() }

// Wake notifies sleeping workers to re-evaluate immediately (after scan, resume, retry).
func (s *Service) Wake() {
	s.mu.Lock()
	s.buffer = nil
	s.statsCache = nil
	close(s.wakeCh)
	s.wakeCh = make(chan struct{})
	s.mu.Unlock()
}

func (s *Service) wakeSignal() <-chan struct{} {
	s.mu.Lock()
	ch := s.wakeCh
	s.mu.Unlock()
	return ch
}

func (s *Service) runWorker(ctx context.Context) {
	for {
		wake := s.wakeSignal()
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
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Service) setIdleState(state State, detail string, paused bool) {
	s.mu.Lock()
	s.paused = paused
	if len(s.inFlight) == 0 {
		s.state, s.detail = state, detail
	}
	s.mu.Unlock()
}

func (s *Service) recordError(err error) {
	s.mu.Lock()
	s.lastErr, s.lastErrAt = err.Error(), s.opts.Now()
	if len(s.inFlight) == 0 {
		s.state, s.detail = StateError, err.Error()
	}
	s.mu.Unlock()
}

func isPermanentMeasureError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrSilentTrack) || errors.Is(err, ErrDecodeFailed) || os.IsNotExist(err)
}

func (s *Service) claimCandidate(ctx context.Context) (storage.AudioLoudnessCandidate, bool, error) {
	s.claimMu.Lock()
	defer s.claimMu.Unlock()

	s.mu.Lock()
	for len(s.buffer) > 0 {
		c := s.buffer[0]
		s.buffer = s.buffer[1:]
		if _, busy := s.inFlight[c.AudioFileID]; busy {
			continue
		}
		s.inFlight[c.AudioFileID] = Current{
			AudioFileID: c.AudioFileID,
			TrackID:     c.TrackID,
			Title:       c.Title,
			Path:        c.RelativePath,
			StartedAt:   s.opts.Now(),
		}
		s.state, s.detail = StateRunning, ""
		s.mu.Unlock()
		return c, true, nil
	}
	exclude := make([]int64, 0, len(s.inFlight))
	for id := range s.inFlight {
		exclude = append(exclude, id)
	}
	s.mu.Unlock()

	batch, err := s.store.NextAudioLoudnessCandidates(ctx, storage.LoudnessAnalyzerVersion, s.opts.Now(), exclude, claimBatchSize)
	if err != nil {
		return storage.AudioLoudnessCandidate{}, false, err
	}
	if len(batch) == 0 {
		return storage.AudioLoudnessCandidate{}, false, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range batch {
		if _, busy := s.inFlight[c.AudioFileID]; busy {
			continue
		}
		s.inFlight[c.AudioFileID] = Current{
			AudioFileID: c.AudioFileID,
			TrackID:     c.TrackID,
			Title:       c.Title,
			Path:        c.RelativePath,
			StartedAt:   s.opts.Now(),
		}
		s.state, s.detail = StateRunning, ""
		if i+1 < len(batch) {
			s.buffer = append(s.buffer[:0], batch[i+1:]...)
		}
		return c, true, nil
	}
	return storage.AudioLoudnessCandidate{}, false, nil
}

func (s *Service) releaseCandidate(audioFileID int64, elapsedMs float64, measured bool) {
	s.mu.Lock()
	delete(s.inFlight, audioFileID)
	if measured {
		s.processed++
		if s.avgMs == 0 {
			s.avgMs = elapsedMs
		} else {
			s.avgMs = s.avgMs*0.85 + elapsedMs*0.15
		}
	}
	s.statsCache = nil
	if len(s.inFlight) == 0 && s.state == StateRunning {
		s.state, s.detail = StateIdle, ""
	}
	s.mu.Unlock()
}

// Step performs one unit of loudness analysis work and returns how long to wait
// before the next attempt (0 means continue immediately).
func (s *Service) Step(ctx context.Context) time.Duration {
	if !s.Available() {
		s.setIdleState(StateUnavailable, "未检测到支持 ebur128 滤镜的 ffmpeg", false)
		return pausedDelay
	}
	settings, err := s.store.AudioLoudnessSettings(ctx)
	if err != nil {
		s.recordError(fmt.Errorf("读取响度设置失败：%w", err))
		return errorDelay
	}
	if settings.Paused {
		s.setIdleState(StatePaused, "", true)
		return pausedDelay
	}
	if reason := s.opts.Busy(); reason != "" {
		s.setIdleState(StateYielding, reason, false)
		return yieldDelay
	}

	cand, ok, err := s.claimCandidate(ctx)
	if err != nil {
		s.recordError(fmt.Errorf("查询待分析曲目失败：%w", err))
		return errorDelay
	}
	if !ok {
		s.setIdleState(StateIdle, "", false)
		return idlePollDelay
	}

	fullPath := filepath.Join(cand.RootPath, filepath.FromSlash(cand.RelativePath))
	start := time.Now()
	meas, mErr := s.opts.Measurer.Measure(ctx, fullPath)
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		s.releaseCandidate(cand.AudioFileID, 0, false)
		return 0
	}
	if mErr != nil {
		permanent := isPermanentMeasureError(mErr)
		if rErr := s.store.RecordAudioLoudnessFailure(ctx, cand, storage.LoudnessAnalyzerVersion, mErr.Error(), permanent, s.opts.Now()); rErr != nil {
			s.logger.Warn("record loudness failure", "audioFileId", cand.AudioFileID, "error", rErr)
		}
		s.mu.Lock()
		s.lastErr, s.lastErrAt = mErr.Error(), s.opts.Now()
		s.mu.Unlock()
		s.releaseCandidate(cand.AudioFileID, 0, false)
		return 0
	}

	rec := storage.AudioLoudnessRecord{
		AudioFileID:     cand.AudioFileID,
		FileSize:        cand.FileSize,
		ModifiedAtNS:    cand.ModifiedAtNS,
		AnalyzerVersion: storage.LoudnessAnalyzerVersion,
		IntegratedLUFS:  meas.IntegratedLUFS,
		TruePeakDB:      meas.TruePeakDB,
		LoudnessRange:   meas.LoudnessRange,
		AnalyzeMs:       elapsed.Milliseconds(),
	}
	if err = s.store.SaveAudioLoudness(ctx, rec, cand.AlbumID); err != nil {
		s.logger.Error("save loudness measurement", "audioFileId", cand.AudioFileID, "error", err)
		s.recordError(err)
		s.releaseCandidate(cand.AudioFileID, 0, false)
		return errorDelay
	}
	s.releaseCandidate(cand.AudioFileID, float64(elapsed.Milliseconds()), true)
	return 0
}

// SetPaused persists the paused flag and wakes workers when resuming.
func (s *Service) SetPaused(ctx context.Context, paused bool) error {
	if err := s.store.SetAudioLoudnessPaused(ctx, paused); err != nil {
		return err
	}
	s.mu.Lock()
	s.paused = paused
	if paused && len(s.inFlight) == 0 && s.Available() {
		s.state, s.detail = StatePaused, ""
	}
	s.mu.Unlock()
	s.Wake()
	return nil
}

// RetryFailures clears loudness failure records and wakes workers.
func (s *Service) RetryFailures(ctx context.Context) (int64, error) {
	n, err := s.store.ClearAudioLoudnessFailures(ctx)
	if err != nil {
		return 0, err
	}
	s.Wake()
	return n, nil
}

// Failures lists active loudness measurement failures for the admin page.
func (s *Service) Failures(ctx context.Context, limit int) ([]storage.AudioLoudnessFailure, error) {
	return s.store.AudioLoudnessFailures(ctx, storage.LoudnessAnalyzerVersion, limit)
}

func (s *Service) cachedStats(ctx context.Context) (storage.AudioLoudnessStats, error) {
	s.mu.Lock()
	if s.statsCache != nil && time.Since(s.statsAt) < statsCacheTTL {
		st := *s.statsCache
		s.mu.Unlock()
		return st, nil
	}
	s.mu.Unlock()

	st, err := s.store.AudioLoudnessStats(ctx, storage.LoudnessAnalyzerVersion, s.opts.Now())
	if err != nil {
		return st, err
	}
	s.mu.Lock()
	cp := st
	s.statsCache = &cp
	s.statsAt = time.Now()
	s.mu.Unlock()
	return st, nil
}

// CapabilitiesSummary returns lightweight stats for GET /api/v1/capabilities.
func (s *Service) CapabilitiesSummary(ctx context.Context) (available bool, stats storage.AudioLoudnessStats, err error) {
	st, err := s.cachedStats(ctx)
	if err != nil {
		return false, st, err
	}
	avail := s.Available() || st.Analyzed > 0
	return avail, st, nil
}

// Snapshot returns the full human-facing status snapshot for admin UI / polling.
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	settings, err := s.store.AudioLoudnessSettings(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	st, err := s.cachedStats(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	s.mu.Lock()
	snap := Snapshot{
		Available: s.Available(),
		State:     s.state,
		Detail:    s.detail,
		Paused:    settings.Paused,
		Workers:   s.workers,
		Stats:     &st,
		Processed: s.processed,
		LastError: s.lastErr,
	}
	if !s.lastErrAt.IsZero() {
		t := s.lastErrAt
		snap.LastErrorAt = &t
	}
	if len(s.inFlight) > 0 {
		snap.Current = make([]Current, 0, len(s.inFlight))
		for _, cur := range s.inFlight {
			snap.Current = append(snap.Current, cur)
		}
		sort.Slice(snap.Current, func(i, j int) bool { return snap.Current[i].AudioFileID < snap.Current[j].AudioFileID })
		snap.State = StateRunning
	} else if !snap.Available {
		snap.State = StateUnavailable
	} else if settings.Paused {
		snap.State = StatePaused
	} else if reason := s.opts.Busy(); reason != "" {
		snap.State = StateYielding
		snap.Detail = reason
	}
	avgMs := s.avgMs
	s.mu.Unlock()

	pending := st.Eligible - st.Analyzed - st.PermanentFailures - st.RetryPending
	if pending < 0 {
		pending = 0
	}
	snap.Pending = pending
	if avgMs <= 0 && st.RecentAvgMs > 0 {
		avgMs = float64(st.RecentAvgMs)
	}
	if pending > 0 && avgMs > 0 && s.workers > 0 {
		eta := int64((float64(pending) * avgMs / float64(s.workers)) / 1000.0)
		if eta < 1 {
			eta = 1
		}
		snap.ETASeconds = eta
	}
	return snap, nil
}
