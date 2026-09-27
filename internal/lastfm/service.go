package lastfm

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

const (
	idleRecheck          = 30 * time.Minute
	notReadyRecheck      = 10 * time.Minute
	errorRecheck         = time.Minute
	minRetryDelay        = time.Minute
	maxRetryDelay        = time.Hour
	maxPermanentAttempts = 5
	nowPlayingMinRepeat  = time.Minute
)

// Service owns the Last.fm outbox worker and now-playing notifications.
// Plays are written to the SQLite outbox by storage.RecordScrobble; the
// worker submits them in batches and retries with backoff, so a Last.fm
// outage or a server restart never loses a play.
type Service struct {
	store     *storage.Store
	logger    *slog.Logger
	newClient func(apiKey, apiSecret string) *Client
	now       func() time.Time

	baseCtx context.Context
	wake    chan struct{}
	wg      sync.WaitGroup

	mu          sync.Mutex
	lastNowPlay nowPlayingMark
	nowPlaySlot chan struct{}
}

type nowPlayingMark struct {
	trackID  int64
	at       time.Time
	duration time.Duration
}

func NewService(store *storage.Store, logger *slog.Logger) *Service {
	return &Service{
		store:       store,
		logger:      logger,
		newClient:   NewClient,
		now:         time.Now,
		baseCtx:     context.Background(),
		wake:        make(chan struct{}, 1),
		nowPlaySlot: make(chan struct{}, 2),
	}
}

// SetClientFactory replaces the API client constructor (tests).
func (s *Service) SetClientFactory(factory func(apiKey, apiSecret string) *Client) {
	s.newClient = factory
}

// Start runs the outbox worker until ctx is cancelled.
func (s *Service) Start(ctx context.Context) {
	s.baseCtx = ctx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

// Wait blocks until the worker and in-flight notifications have stopped.
func (s *Service) Wait() { s.wg.Wait() }

// Wake asks the worker to look at the outbox now.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) run(ctx context.Context) {
	for {
		delay := s.Flush(ctx)
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

// Flush submits every due play and returns how long to sleep before the next
// pass. It is exported so tests can drive the worker synchronously.
func (s *Service) Flush(ctx context.Context) time.Duration {
	settings, err := s.store.LastFMScrobbleSettings(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("load Last.fm scrobble settings", "error", err)
		}
		return errorRecheck
	}
	if !settings.Ready() {
		return notReadyRecheck
	}
	if dropped, dropErr := s.store.DropExpiredLastFMScrobbles(ctx, s.now()); dropErr != nil {
		s.logger.Warn("drop expired Last.fm scrobbles", "error", dropErr)
	} else if dropped > 0 {
		s.logger.Warn("dropped Last.fm scrobbles older than 14 days", "count", dropped)
	}
	client := s.newClient(settings.APIKey, settings.APISecret)
	for ctx.Err() == nil {
		items, err := s.store.DueLastFMScrobbles(ctx, s.now(), MaxBatch)
		if err != nil {
			s.logger.Warn("load Last.fm scrobble queue", "error", err)
			return errorRecheck
		}
		if len(items) == 0 {
			next, err := s.store.NextLastFMScrobbleAttempt(ctx)
			if err != nil || next.IsZero() {
				return idleRecheck
			}
			return max(next.Sub(s.now()), time.Second)
		}
		if stop, delay := s.submit(ctx, client, settings.SessionKey, items); stop {
			return delay
		}
	}
	return errorRecheck
}

// submit sends one batch. It returns stop=true when the pass should end.
func (s *Service) submit(ctx context.Context, client *Client, sessionKey string, items []storage.LastFMTrack) (bool, time.Duration) {
	tracks := make([]Track, len(items))
	for i, item := range items {
		tracks[i] = toTrack(item)
	}
	results, err := client.Scrobble(ctx, sessionKey, tracks)
	if err == nil {
		ids := make([]int64, len(items))
		for i, item := range items {
			ids[i] = item.ID
			if !results[i].Accepted {
				s.logger.Warn("Last.fm ignored scrobble", "artist", item.Artist, "track", item.Track, "code", results[i].IgnoredCode, "reason", results[i].IgnoredReason)
			}
		}
		if err := s.store.DeleteLastFMScrobbles(ctx, ids); err != nil {
			s.logger.Warn("remove submitted Last.fm scrobbles", "error", err)
			return true, errorRecheck
		}
		_ = s.store.SetLastFMStatus(ctx, true, "")
		return false, 0
	}
	if ctx.Err() != nil {
		return true, errorRecheck
	}

	var apiErr *APIError
	isAPIErr := errors.As(err, &apiErr)
	switch {
	case isAPIErr && apiErr.AuthorizationBroken():
		s.handleBrokenAuthorization(ctx, apiErr)
		return true, notReadyRecheck
	case isAPIErr && !apiErr.Temporary() && len(items) > 1:
		// A permanent rejection of a batch may be caused by a single bad
		// item; isolate it by resubmitting one play at a time.
		for _, item := range items {
			if stop, delay := s.submit(ctx, client, sessionKey, []storage.LastFMTrack{item}); stop {
				return true, delay
			}
		}
		return false, 0
	case isAPIErr && !apiErr.Temporary() && items[0].Attempts+1 >= maxPermanentAttempts:
		s.logger.Warn("giving up on Last.fm scrobble", "artist", items[0].Artist, "track", items[0].Track, "error", err)
		_ = s.store.DeleteLastFMScrobbles(ctx, []int64{items[0].ID})
		_ = s.store.SetLastFMStatus(ctx, false, "放弃提交《"+items[0].Track+"》："+err.Error())
		return false, 0
	}
	delay := retryDelay(items[0].Attempts)
	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	s.logger.Warn("Last.fm scrobble failed; will retry", "count", len(items), "retryIn", delay.String(), "error", err)
	_ = s.store.DeferLastFMScrobbles(ctx, ids, s.now().Add(delay), err.Error())
	_ = s.store.SetLastFMStatus(ctx, false, err.Error())
	if isAPIErr && !apiErr.Temporary() {
		// Items are deferred individually; keep submitting the rest.
		return false, 0
	}
	return true, delay
}

func (s *Service) handleBrokenAuthorization(ctx context.Context, apiErr *APIError) {
	switch apiErr.Code {
	case ErrInvalidSession, ErrAuthFailed:
		s.logger.Warn("Last.fm session rejected; account must be reconnected", "error", apiErr)
		_ = s.store.ClearLastFMSession(ctx, "Last.fm 会话已失效，请重新连接账号（"+apiErr.Error()+"）")
	default:
		s.logger.Warn("Last.fm rejected API credentials", "error", apiErr)
		_ = s.store.SetLastFMStatus(ctx, false, "API Key 或 Shared Secret 无效："+apiErr.Error())
	}
}

func retryDelay(attempts int) time.Duration {
	delay := minRetryDelay
	for i := 0; i < attempts && delay < maxRetryDelay; i++ {
		delay *= 2
	}
	return min(delay, maxRetryDelay)
}

func toTrack(item storage.LastFMTrack) Track {
	return Track{Artist: item.Artist, Track: item.Track, Album: item.Album, AlbumArtist: item.AlbumArtist, TrackNumber: item.TrackNumber, DurationSeconds: item.DurationSeconds, Timestamp: item.StartedAt}
}

// NowPlaying sends track.updateNowPlaying in the background. Clients report
// the timeline every few seconds; only the first "playing" report of a play
// (a new track, or the same track again after it could have finished) is
// forwarded. Failures are logged and never retried: now-playing is transient.
func (s *Service) NowPlaying(trackID int64) {
	if s == nil || trackID <= 0 {
		return
	}
	now := s.now()
	s.mu.Lock()
	last := s.lastNowPlay
	if last.trackID == trackID && now.Sub(last.at) < max(last.duration, nowPlayingMinRepeat) {
		s.mu.Unlock()
		return
	}
	s.lastNowPlay = nowPlayingMark{trackID: trackID, at: now, duration: nowPlayingMinRepeat}
	s.mu.Unlock()

	select {
	case s.nowPlaySlot <- struct{}{}:
	default:
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.nowPlaySlot }()
		ctx, cancel := context.WithTimeout(s.baseCtx, 15*time.Second)
		defer cancel()
		s.sendNowPlaying(ctx, trackID, now)
	}()
}

func (s *Service) sendNowPlaying(ctx context.Context, trackID int64, markedAt time.Time) {
	settings, err := s.store.LastFMScrobbleSettings(ctx)
	if err != nil || !settings.Ready() || !settings.NowPlaying {
		return
	}
	item, err := s.store.LastFMNowPlayingTrack(ctx, trackID)
	if err != nil || item.Artist == "" || item.Track == "" || item.Artist == "Unknown Artist" {
		return
	}
	if item.DurationSeconds > 0 {
		s.mu.Lock()
		if s.lastNowPlay.trackID == trackID && s.lastNowPlay.at.Equal(markedAt) {
			s.lastNowPlay.duration = time.Duration(item.DurationSeconds) * time.Second
		}
		s.mu.Unlock()
	}
	client := s.newClient(settings.APIKey, settings.APISecret)
	if err := client.UpdateNowPlaying(ctx, settings.SessionKey, toTrack(item)); err != nil && ctx.Err() == nil {
		s.logger.Debug("Last.fm now playing failed", "trackId", trackID, "error", err)
	}
}

// Connect exchanges an authorised token for a session and stores it.
func (s *Service) Connect(ctx context.Context, token string) (string, error) {
	settings, err := s.store.LastFMScrobbleSettings(ctx)
	if err != nil {
		return "", err
	}
	if !settings.HasAPIKey() || !settings.HasAPISecret() {
		return "", errors.New("请先填写 Last.fm API Key 与 Shared Secret")
	}
	username, sessionKey, err := s.newClient(settings.APIKey, settings.APISecret).GetSession(ctx, token)
	if err != nil {
		return "", err
	}
	if err := s.store.SetLastFMSession(ctx, username, sessionKey); err != nil {
		return "", err
	}
	s.Wake()
	return username, nil
}
