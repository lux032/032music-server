package dailymix

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

// Status values of the day's mixes.
const (
	StatusReady      = "ready"
	StatusGenerating = "generating"
	StatusDisabled   = "disabled"
	StatusNotReady   = "not_ready"
)

var (
	// ErrReplaceLimit: the day's “换一个” budget is used up.
	ErrReplaceLimit = errors.New("daily replacement limit reached")
	// ErrNotActive: the mix was already replaced or belongs to another day.
	ErrNotActive = errors.New("mix is not one of today's active mixes")
)

// notReadyRetry spaces out generation attempts while the library is not ready.
const notReadyRetry = 10 * time.Minute

// Options configures the service.
type Options struct {
	Now func() time.Time
	// NewChat creates the model client from the AI settings (default: tagging.NewChatClient).
	NewChat func(storage.AISettings) (tagging.Chat, error)
	// LLMTimeout bounds the title/planning call (default 6 s, within the 8 s budget of
	// a first request).
	LLMTimeout time.Duration
}

// Service pre-generates the day's mixes at the configured hour, generates them
// lazily on the first request otherwise, and handles “换一个”.
type Service struct {
	store  *storage.Store
	logger *slog.Logger
	opts   Options
	gen    *Generator

	mu       sync.Mutex
	rootCtx  context.Context
	flights  map[string]chan struct{}
	failedAt map[string]time.Time
	lastRun  *RunInfo
	lastErr  string
	// lastFeedback is when implicit playback feedback was last folded in.
	lastFeedback time.Time
	replace      sync.Mutex
	wake         chan struct{}
	wg           sync.WaitGroup
}

func New(store *storage.Store, logger *slog.Logger, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.LLMTimeout <= 0 {
		opts.LLMTimeout = 6 * time.Second
	}
	if opts.NewChat == nil {
		opts.NewChat = func(st storage.AISettings) (tagging.Chat, error) {
			return tagging.NewChatClient(st.BaseURL, st.APIKey, st.ProxyURL, 30*time.Second)
		}
	}
	s := &Service{store: store, logger: logger, opts: opts, rootCtx: context.Background(), flights: map[string]chan struct{}{},
		failedAt: map[string]time.Time{}, wake: make(chan struct{}, 1)}
	s.gen = &Generator{Store: store, Chat: opts.NewChat, LLMTimeout: opts.LLMTimeout}
	return s
}

// Start runs the pre-generation loop until ctx ends.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	s.rootCtx = ctx
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		timer := time.NewTimer(30 * time.Second) // let startup work settle first
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			case <-s.wake:
			}
			s.tick(ctx)
			timer.Reset(time.Minute)
		}
	}()
}

// Wait blocks until the loop and running generations have stopped.
func (s *Service) Wait() { s.wg.Wait() }

// Wake re-checks immediately (settings changed).
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// feedbackInterval is how often implicit playback feedback is aggregated (§7.4).
const feedbackInterval = time.Hour

// ProcessFeedback folds ended daily-mix plays into the feedback tables. force runs it
// regardless of the hourly cadence (before a generation).
func (s *Service) ProcessFeedback(ctx context.Context, force bool) {
	s.mu.Lock()
	due := force || s.opts.Now().Sub(s.lastFeedback) >= feedbackInterval
	if due {
		s.lastFeedback = s.opts.Now()
	}
	s.mu.Unlock()
	if !due {
		return
	}
	for i := 0; i < 20; i++ { // bounded: 20 × 2000 sessions per pass
		run, err := s.store.ProcessPlaybackFeedback(ctx, s.opts.Now())
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("daily mix feedback aggregation failed", "error", err)
			}
			return
		}
		if run.Sessions > 0 {
			s.logger.Debug("daily mix feedback aggregated", "sessions", run.Sessions, "mixes", run.Mixes)
		}
		if run.Sessions < 2000 {
			return
		}
	}
}

func (s *Service) tick(ctx context.Context) {
	set, err := s.store.DailyMixSettings(ctx)
	if err != nil || !set.Enabled {
		return
	}
	s.ProcessFeedback(ctx, false)
	day := Day(s.opts.Now(), set.PregenerateHour)
	mixes, err := s.store.DailyMixes(ctx, day)
	if err != nil || len(mixes) > 0 || s.coolingDown(day) {
		return
	}
	<-s.start(day)
}

func (s *Service) coolingDown(day string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.failedAt[day]
	return ok && s.opts.Now().Sub(at) < notReadyRetry
}

// start launches (or joins) the generation of a day; the channel closes when done.
func (s *Service) start(day string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.flights[day]; ok {
		return ch
	}
	ch := make(chan struct{})
	s.flights[day] = ch
	ctx := s.rootCtx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.generate(ctx, day)
		s.mu.Lock()
		delete(s.flights, day)
		if err != nil {
			s.failedAt[day] = s.opts.Now()
		} else {
			delete(s.failedAt, day)
		}
		s.mu.Unlock()
		close(ch)
	}()
	return ch
}

func (s *Service) generate(ctx context.Context, day string) error {
	s.ProcessFeedback(ctx, true)
	set, err := s.store.DailyMixSettings(ctx)
	if err != nil {
		return err
	}
	ai, err := s.store.AISettings(ctx)
	if err != nil {
		return err
	}
	mixes, info, err := s.gen.GenerateDay(ctx, s.opts.Now(), day, set, ai)
	if err == nil {
		err = s.store.SaveDailyMixes(ctx, day, mixes)
		if errors.Is(err, storage.ErrDailyMixesExist) {
			err = nil
		}
	}
	s.record(info, err)
	if err != nil && ctx.Err() == nil {
		if errors.Is(err, ErrNotReady) {
			s.logger.Info("daily mixes not generated: library not ready", "day", day, "tagged", info.Tagged, "candidates", info.Candidates)
		} else {
			s.logger.Warn("daily mix generation failed", "day", day, "error", err)
		}
	} else if err == nil {
		s.logger.Info("daily mixes generated", "day", day, "mixes", info.Mixes, "ms", info.Millis, "llm", info.LLMUsed)
	}
	return err
}

func (s *Service) record(info RunInfo, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		info.Error = err.Error()
		if errors.Is(err, ErrNotReady) {
			info.Error = NotReadyText
		}
	}
	s.lastRun = &info
	s.lastErr = info.Error
}

// NotReadyText explains why no mix could be generated.
const NotReadyText = "曲库里还没有足够的已打标曲目组成任何主题（每个情绪/风格主题至少需要约 40 首）。请先完成声学分析和 AI 打标。"

// Today returns the current day's mixes, generating them if needed and waiting up
// to wait. status is one of the Status* values.
func (s *Service) Today(ctx context.Context, wait time.Duration) (string, string, []storage.DailyMix, error) {
	set, err := s.store.DailyMixSettings(ctx)
	if err != nil {
		return "", "", nil, err
	}
	day := Day(s.opts.Now(), set.PregenerateHour)
	if !set.Enabled {
		return StatusDisabled, day, []storage.DailyMix{}, nil
	}
	mixes, err := s.store.DailyMixes(ctx, day)
	if err != nil || len(mixes) > 0 {
		return StatusReady, day, mixes, err
	}
	if s.coolingDown(day) {
		return StatusNotReady, day, []storage.DailyMix{}, nil
	}
	ch := s.start(day)
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
		return StatusGenerating, day, []storage.DailyMix{}, nil
	case <-ctx.Done():
		return StatusGenerating, day, []storage.DailyMix{}, ctx.Err()
	}
	mixes, err = s.store.DailyMixes(ctx, day)
	if err != nil {
		return "", day, nil, err
	}
	if len(mixes) == 0 {
		return StatusNotReady, day, mixes, nil
	}
	return StatusReady, day, mixes, nil
}

// CurrentDay returns the recommendation day for now.
func (s *Service) CurrentDay(ctx context.Context) (string, storage.DailyMixSettings, error) {
	set, err := s.store.DailyMixSettings(ctx)
	return Day(s.opts.Now(), set.PregenerateHour), set, err
}

// ReplacementsLeft returns how many “换一个” remain today.
func (s *Service) ReplacementsLeft(ctx context.Context, day string, set storage.DailyMixSettings) (int, error) {
	used, err := s.store.ReplacementsOn(ctx, day)
	return max(0, set.RegenerateLimit-used), err
}

// Replace swaps one of today's mixes for a new theme. admin bypasses the daily limit.
func (s *Service) Replace(ctx context.Context, mixID int64, admin bool) (storage.DailyMix, RunInfo, error) {
	s.replace.Lock()
	defer s.replace.Unlock()
	day, set, err := s.CurrentDay(ctx)
	if err != nil {
		return storage.DailyMix{}, RunInfo{}, err
	}
	old, err := s.store.DailyMixByID(ctx, mixID)
	if err != nil {
		return storage.DailyMix{}, RunInfo{}, err
	}
	if old.Status != "active" || old.Day != day {
		return storage.DailyMix{}, RunInfo{}, ErrNotActive
	}
	if !admin {
		left, err := s.ReplacementsLeft(ctx, day, set)
		if err != nil {
			return storage.DailyMix{}, RunInfo{}, err
		}
		if left <= 0 {
			return storage.DailyMix{}, RunInfo{}, ErrReplaceLimit
		}
	}
	today, err := s.store.DailyMixes(ctx, day)
	if err != nil {
		return storage.DailyMix{}, RunInfo{}, err
	}
	ai, err := s.store.AISettings(ctx)
	if err != nil {
		return storage.DailyMix{}, RunInfo{}, err
	}
	mix, info, err := s.gen.GenerateReplacement(ctx, s.opts.Now(), old, today, set, ai)
	s.record(info, err)
	if err != nil {
		return storage.DailyMix{}, info, err
	}
	id, err := s.store.ReplaceDailyMix(ctx, old.ID, mix)
	if err != nil {
		return storage.DailyMix{}, info, err
	}
	m, err := s.store.DailyMixByID(ctx, id)
	return m, info, err
}

// RegenerateToday deletes today's mixes and generates them again (admin), waiting
// for the result.
func (s *Service) RegenerateToday(ctx context.Context) error {
	day, set, err := s.CurrentDay(ctx)
	if err != nil {
		return err
	}
	if !set.Enabled {
		return errors.New("每日推荐已关闭")
	}
	s.mu.Lock()
	if _, running := s.flights[day]; running {
		s.mu.Unlock()
		return errors.New("正在生成，请稍后再试")
	}
	delete(s.failedAt, day)
	s.mu.Unlock()
	if err := s.store.DeleteDailyMixes(ctx, day); err != nil {
		return err
	}
	select {
	case <-s.start(day):
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastRun != nil && s.lastRun.Day == day && s.lastRun.Error != "" {
		return errors.New(s.lastRun.Error)
	}
	return nil
}

// Snapshot is the service state for the admin page.
type Snapshot struct {
	Day        string   `json:"day"`
	Generating bool     `json:"generating"`
	LastRun    *RunInfo `json:"lastRun,omitempty"`
}

func (s *Service) Snapshot(ctx context.Context) Snapshot {
	day, _, _ := s.CurrentDay(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, gen := s.flights[day]
	snap := Snapshot{Day: day, Generating: gen}
	if s.lastRun != nil {
		r := *s.lastRun
		snap.LastRun = &r
	}
	return snap
}
