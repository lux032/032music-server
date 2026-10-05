package storage

import (
	"context"
	"log/slog"
	"time"
)

// PlaybackSweeper finalizes expired sessions and purges old ended ones.
// Correctness never depends on it — reads derive the effective state from
// last_heartbeat_at — so the sweeper only persists what the derivation
// already decided (B1). It runs once at startup (covering downtime) and
// then on its tickers until the context is cancelled at shutdown.
type PlaybackSweeper struct {
	store  *Store
	logger *slog.Logger
	// ExpireInterval / PurgeInterval are fields so tests can shorten them.
	ExpireInterval time.Duration
	PurgeInterval  time.Duration
}

func NewPlaybackSweeper(store *Store, logger *slog.Logger) *PlaybackSweeper {
	return &PlaybackSweeper{store: store, logger: logger, ExpireInterval: 30 * time.Second, PurgeInterval: time.Hour}
}

// Start launches the sweeper goroutine; it stops when ctx is cancelled.
func (s *PlaybackSweeper) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *PlaybackSweeper) loop(ctx context.Context) {
	s.sweep(ctx)
	expireTicker := time.NewTicker(s.ExpireInterval)
	purgeTicker := time.NewTicker(s.PurgeInterval)
	defer expireTicker.Stop()
	defer purgeTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expireTicker.C:
			s.fixExpired(ctx)
		case <-purgeTicker.C:
			s.purgeEnded(ctx)
		}
	}
}

func (s *PlaybackSweeper) sweep(ctx context.Context) {
	s.fixExpired(ctx)
	s.purgeEnded(ctx)
}

func (s *PlaybackSweeper) fixExpired(ctx context.Context) {
	fixed, err := s.store.FixExpiredPlaybackSessions(ctx)
	if err != nil {
		s.warn("playback session expiry sweep failed", err)
		return
	}
	if fixed > 0 && s.logger != nil {
		s.logger.Info("playback sessions expired", "count", fixed)
	}
}

func (s *PlaybackSweeper) purgeEnded(ctx context.Context) {
	purged, err := s.store.PurgeEndedPlaybackSessions(ctx, time.Now())
	if err != nil {
		s.warn("playback session purge failed", err)
		return
	}
	if purged > 0 && s.logger != nil {
		s.logger.Info("playback sessions purged", "count", purged)
	}
}

func (s *PlaybackSweeper) warn(message string, err error) {
	if s.logger != nil {
		s.logger.Warn(message, "error", err)
	}
}
