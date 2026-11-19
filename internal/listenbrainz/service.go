package listenbrainz

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
	minCallInterval      = time.Second
	loveBackfillInterval = 1200 * time.Millisecond
	lookupRetryInterval  = 7 * 24 * time.Hour
)

// Service owns the ListenBrainz outbox worker, love-sync worker, and now-playing notifications.
type Service struct {
	store     *storage.Store
	logger    *slog.Logger
	newClient func(serverURL string) *Client
	now       func() time.Time

	baseCtx context.Context
	wake    chan struct{}
	wg      sync.WaitGroup

	mu                   sync.Mutex
	lastNowPlay          nowPlayingMark
	nowPlaySlot          chan struct{}
	lastAPICallAt        time.Time
	loveBackfillFailures map[int64]int
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

func (s *Service) SetClientFactory(factory func(serverURL string) *Client) {
	s.newClient = factory
}

func (s *Service) Start(ctx context.Context) {
	s.baseCtx = ctx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// ValidateToken checks token against serverURL using the service's client factory.
func (s *Service) ValidateToken(ctx context.Context, serverURL, token string) (string, string, error) {
	normalized, err := NormalizeServerURL(serverURL)
	if err != nil {
		return "", "", err
	}
	username, err := s.newClient(normalized).ValidateToken(ctx, token)
	if err != nil {
		return "", "", err
	}
	return normalized, username, nil
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

// Flush processes due love syncs, backfill, and scrobble batches.
func (s *Service) Flush(ctx context.Context) time.Duration {
	settings, err := s.store.ListenBrainzSettings(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("load ListenBrainz settings", "error", err)
		}
		return errorRecheck
	}
	loveDelay, loveWorked := s.flushLoves(ctx, settings)
	if loveWorked {
		return max(loveDelay, time.Second)
	}
	if !settings.Ready() {
		return minDelay(loveDelay, notReadyRecheck)
	}
	client := s.newClient(settings.ServerURL)
	for ctx.Err() == nil {
		items, err := s.store.DueListenBrainzScrobbles(ctx, s.now(), MaxImportBatch)
		if err != nil {
			s.logger.Warn("load ListenBrainz scrobble queue", "error", err)
			return errorRecheck
		}
		if len(items) == 0 {
			next, err := s.store.NextListenBrainzScrobbleAttempt(ctx)
			if err != nil || next.IsZero() {
				return minDelay(loveDelay, idleRecheck)
			}
			return minDelay(loveDelay, max(next.Sub(s.now()), time.Second))
		}
		if stop, delay := s.submit(ctx, client, settings.UserToken, items); stop {
			return minDelay(loveDelay, delay)
		}
	}
	return minDelay(loveDelay, errorRecheck)
}

func minDelay(love, other time.Duration) time.Duration {
	if love <= 0 {
		return other
	}
	if other <= 0 {
		return love
	}
	return min(love, other)
}

func (s *Service) waitAPISlot(ctx context.Context, interval time.Duration) {
	s.mu.Lock()
	last := s.lastAPICallAt
	s.mu.Unlock()
	if !last.IsZero() {
		if wait := interval - s.now().Sub(last); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}
	s.mu.Lock()
	s.lastAPICallAt = s.now()
	s.mu.Unlock()
}

func (s *Service) submit(ctx context.Context, client *Client, token string, items []storage.ListenBrainzTrack) (bool, time.Duration) {
	s.waitAPISlot(ctx, minCallInterval)
	if ctx.Err() != nil {
		return true, errorRecheck
	}
	tracks := make([]Track, len(items))
	for i, item := range items {
		tracks[i] = toTrack(item)
	}
	err := client.SubmitListens(ctx, token, tracks)
	if err == nil {
		ids := make([]int64, len(items))
		for i, item := range items {
			ids[i] = item.ID
		}
		if err := s.store.DeleteListenBrainzScrobbles(ctx, ids); err != nil {
			s.logger.Warn("remove submitted ListenBrainz listens", "error", err)
			return true, errorRecheck
		}
		_ = s.store.SetListenBrainzStatus(ctx, true, "")
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
		for _, item := range items {
			if stop, delay := s.submit(ctx, client, token, []storage.ListenBrainzTrack{item}); stop {
				return true, delay
			}
		}
		return false, 0
	case isAPIErr && !apiErr.Temporary() && items[0].Attempts+1 >= maxPermanentAttempts:
		s.logger.Warn("giving up on ListenBrainz listen", "artist", items[0].Artist, "track", items[0].Track, "error", err)
		_ = s.store.DeleteListenBrainzScrobbles(ctx, []int64{items[0].ID})
		_ = s.store.SetListenBrainzStatus(ctx, false, "放弃提交《"+items[0].Track+"》："+err.Error())
		return false, 0
	}

	delay := retryDelay(items[0].Attempts)
	if isAPIErr && apiErr.RetryAfter > delay {
		delay = min(apiErr.RetryAfter, maxRetryDelay)
	}
	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	s.logger.Warn("ListenBrainz submit failed; will retry", "count", len(items), "retryIn", delay.String(), "error", err)
	_ = s.store.DeferListenBrainzScrobbles(ctx, ids, s.now().Add(delay), err.Error())
	_ = s.store.SetListenBrainzStatus(ctx, false, err.Error())
	if isAPIErr && !apiErr.Temporary() {
		return false, 0
	}
	return true, delay
}

func (s *Service) handleBrokenAuthorization(ctx context.Context, apiErr *APIError) {
	s.logger.Warn("ListenBrainz token rejected; account must be reconnected", "error", apiErr)
	_ = s.store.ClearListenBrainzConnection(ctx, "ListenBrainz User Token 无效或已失效，请重新填写并保存（"+apiErr.Error()+"）")
}

func retryDelay(attempts int) time.Duration {
	delay := minRetryDelay
	for i := 0; i < attempts && delay < maxRetryDelay; i++ {
		delay *= 2
	}
	return min(delay, maxRetryDelay)
}

func toTrack(item storage.ListenBrainzTrack) Track {
	return Track{
		Artist:          item.Artist,
		Track:           item.Track,
		Album:           item.Album,
		TrackNumber:     item.TrackNumber,
		DurationSeconds: item.DurationSeconds,
		RecordingMBID:   item.RecordingMBID,
		ReleaseMBID:     item.ReleaseMBID,
		ArtistMBIDs:     item.ArtistMBIDs,
		Timestamp:       item.StartedAt,
	}
}

// NowPlaying reports playing_now in the background and persists any returned recording_msid.
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
	settings, err := s.store.ListenBrainzSettings(ctx)
	if err != nil || !settings.Ready() || !settings.NowPlaying {
		return
	}
	item, err := s.store.ListenBrainzNowPlayingTrack(ctx, trackID)
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
	client := s.newClient(settings.ServerURL)
	msid, err := client.SubmitNowPlaying(ctx, settings.UserToken, toTrack(item))
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Debug("ListenBrainz now playing failed", "trackId", trackID, "error", err)
		}
		return
	}
	if msid != "" {
		if err := s.store.UpsertListenBrainzRecordingMSID(ctx, trackID, item.Artist, item.Track, item.Album, msid); err != nil && ctx.Err() == nil {
			s.logger.Warn("cache ListenBrainz recording_msid", "trackId", trackID, "error", err)
		}
	}
}
