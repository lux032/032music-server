package lastfm

import (
	"context"
	"errors"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// flushLoves submits queued favourite changes and advances a running backfill.
// It reports whether any request was made, so the caller can avoid sending a
// scrobble batch in the same pass.
//
// The walk is deliberately one request at a time with a pause between calls:
// Last.fm's documentation warns that an application continuously making
// several calls per second may have its account suspended.
func (s *Service) flushLoves(ctx context.Context, settings storage.LastFMScrobbleSettings) (time.Duration, bool) {
	if !settings.LoveSyncReady() {
		return 0, false
	}
	client := s.newClient(settings.APIKey, settings.APISecret)
	worked := false
	if delay, ok := s.submitLoves(ctx, client, settings.SessionKey); ok {
		worked = true
		if delay > 0 {
			return delay, worked
		}
	}
	if delay, ok := s.runLoveBackfill(ctx, client, settings.SessionKey); ok {
		worked = true
		return delay, worked
	}
	// Nothing due: wake up for the next scheduled retry, if any.
	next, err := s.store.NextLastFMLoveAttempt(ctx)
	if err != nil || next.IsZero() {
		return 0, worked
	}
	return max(next.Sub(s.now()), time.Second), worked
}

// submitLoves sends the due queue entries. It stops at the first transient
// failure so the backoff schedule applies to everything behind it.
func (s *Service) submitLoves(ctx context.Context, client *Client, sessionKey string) (time.Duration, bool) {
	worked := false
	for ctx.Err() == nil {
		items, err := s.store.DueLastFMLoves(ctx, s.now(), 1)
		if err != nil {
			s.logger.Warn("load Last.fm love queue", "error", err)
			return errorRecheck, worked
		}
		if len(items) == 0 {
			return 0, worked
		}
		item := items[0]
		if item.Artist == "" || item.Track == "" || item.Artist == "Unknown Artist" {
			// Deleted or unknown-artist track: Last.fm has nothing to match.
			s.logger.Warn("dropping Last.fm love change without usable metadata", "trackId", item.TrackID)
			_ = s.store.DropLastFMLove(ctx, item.TrackID, "")
			worked = true
			continue
		}
		s.waitLoveSlot(ctx, loveMinInterval)
		if ctx.Err() != nil {
			return 0, worked
		}
		err = s.sendLove(ctx, client, sessionKey, item)
		worked = true
		if err == nil {
			_ = s.store.CompleteLastFMLove(ctx, item.TrackID, item.Desired)
			_ = s.store.SetLastFMLoveError(ctx, "")
			continue
		}
		if ctx.Err() != nil {
			return 0, worked
		}
		var apiErr *APIError
		isAPIErr := errors.As(err, &apiErr)
		switch {
		case isAPIErr && apiErr.AuthorizationBroken():
			s.handleBrokenAuthorization(ctx, apiErr)
			return notReadyRecheck, worked
		case isAPIErr && !apiErr.Temporary():
			// An unknown/renamed track can never be loved: drop it instead of
			// retrying forever, and keep the reason on the settings card.
			s.logger.Warn("Last.fm rejected love change", "trackId", item.TrackID, "artist", item.Artist, "track", item.Track, "error", err)
			_ = s.store.DropLastFMLove(ctx, item.TrackID, "放弃同步《"+item.Track+"》："+err.Error())
			return 0, worked
		}
		delay := retryDelay(item.Attempts)
		s.logger.Warn("Last.fm love sync failed; will retry", "trackId", item.TrackID, "retryIn", delay.String(), "error", err)
		_ = s.store.DeferLastFMLove(ctx, item.TrackID, s.now().Add(delay), err.Error())
		_ = s.store.SetLastFMLoveError(ctx, err.Error())
		return delay, worked
	}
	return 0, worked
}

func (s *Service) sendLove(ctx context.Context, client *Client, sessionKey string, item storage.LastFMLoveItem) error {
	if item.Desired {
		return client.Love(ctx, sessionKey, item.Artist, item.Track)
	}
	return client.Unlove(ctx, sessionKey, item.Artist, item.Track)
}

// waitLoveSlot enforces the minimum spacing between two write calls. It is a
// no-op when the previous call is already far enough in the past.
func (s *Service) waitLoveSlot(ctx context.Context, interval time.Duration) {
	s.mu.Lock()
	last := s.lastLoveAt
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
	s.lastLoveAt = s.now()
	s.mu.Unlock()
}

// runLoveBackfill pushes one favourite per pass while a run is active. The run
// is explicit (the admin starts it) and can be cancelled at any time; its
// cursor is persisted so a restart resumes instead of starting over.
//
// Backfill failures are counted in memory: a transient outage retries the same
// track, and a track Last.fm keeps rejecting is skipped after
// maxPermanentAttempts so the run can finish.
func (s *Service) runLoveBackfill(ctx context.Context, client *Client, sessionKey string) (time.Duration, bool) {
	run, err := s.store.LastFMLoveBackfill(ctx)
	if err != nil || run.State != storage.LoveBackfillRunning {
		return 0, false
	}
	trackID, err := s.store.NextLoveBackfillTrack(ctx, run.Cursor)
	if err != nil {
		s.logger.Warn("load Last.fm love backfill page", "error", err)
		return errorRecheck, false
	}
	if trackID == 0 {
		if err := s.store.FinishLastFMLoveBackfill(ctx, storage.LoveBackfillDone, ""); err != nil {
			s.logger.Warn("finish Last.fm love backfill", "error", err)
		}
		s.resetLoveBackfillFailures()
		return 0, false
	}
	metadata, err := s.store.LastFMNowPlayingTrack(ctx, trackID)
	if err != nil || metadata.Artist == "" || metadata.Track == "" || metadata.Artist == "Unknown Artist" {
		// Nothing Last.fm could match; skip it and keep the run moving.
		_ = s.store.AdvanceLastFMLoveBackfill(ctx, trackID, 1)
		return loveBackfillMinInterval, true
	}
	s.waitLoveSlot(ctx, loveBackfillMinInterval)
	if ctx.Err() != nil {
		return 0, false
	}
	if err := client.Love(ctx, sessionKey, metadata.Artist, metadata.Track); err != nil {
		if ctx.Err() != nil {
			return 0, false
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.AuthorizationBroken() {
			s.handleBrokenAuthorization(ctx, apiErr)
			return notReadyRecheck, true
		}
		failures := s.noteLoveBackfillFailure(trackID)
		if failures >= maxPermanentAttempts {
			s.logger.Warn("skipping Last.fm love backfill track", "trackId", trackID, "error", err)
			_ = s.store.AdvanceLastFMLoveBackfill(ctx, trackID, 1)
			_ = s.store.SetLastFMLoveError(ctx, "回填时跳过《"+metadata.Track+"》："+err.Error())
			return loveBackfillMinInterval, true
		}
		_ = s.store.SetLastFMLoveError(ctx, err.Error())
		return max(retryDelay(failures-1), loveBackfillMinInterval), true
	}
	s.clearLoveBackfillFailure(trackID)
	if err := s.store.AdvanceLastFMLoveBackfill(ctx, trackID, 0); err != nil {
		s.logger.Warn("advance Last.fm love backfill", "error", err)
	}
	_ = s.store.SetLastFMLoveError(ctx, "")
	// The last track finishes the run in this pass, so the card never shows a
	// completed walk stuck at "进行中" until the next tick.
	if next, nextErr := s.store.NextLoveBackfillTrack(ctx, trackID); nextErr == nil && next == 0 {
		if err := s.store.FinishLastFMLoveBackfill(ctx, storage.LoveBackfillDone, ""); err != nil {
			s.logger.Warn("finish Last.fm love backfill", "error", err)
		}
		s.resetLoveBackfillFailures()
	}
	return loveBackfillMinInterval, true
}

func (s *Service) noteLoveBackfillFailure(trackID int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loveBackfillFailures == nil {
		s.loveBackfillFailures = map[int64]int{}
	}
	s.loveBackfillFailures[trackID]++
	return s.loveBackfillFailures[trackID]
}

func (s *Service) clearLoveBackfillFailure(trackID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.loveBackfillFailures, trackID)
}

func (s *Service) resetLoveBackfillFailures() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loveBackfillFailures = nil
}
