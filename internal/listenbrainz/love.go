package listenbrainz

import (
	"context"
	"errors"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func (s *Service) flushLoves(ctx context.Context, settings storage.ListenBrainzSettings) (time.Duration, bool) {
	if !settings.LoveSyncReady() {
		return 0, false
	}
	client := s.newClient(settings.ServerURL)
	worked := false
	if delay, ok := s.submitLoves(ctx, client, &settings); ok {
		worked = true
		if delay > 0 {
			return delay, worked
		}
	}
	if settings.FeedbackUnsupported {
		return 0, worked
	}
	if delay, ok := s.runLoveBackfill(ctx, client, &settings); ok {
		worked = true
		return delay, worked
	}
	next, err := s.store.NextListenBrainzLoveAttempt(ctx)
	if err != nil || next.IsZero() {
		return 0, worked
	}
	return max(next.Sub(s.now()), time.Second), worked
}

func (s *Service) submitLoves(ctx context.Context, client *Client, settings *storage.ListenBrainzSettings) (time.Duration, bool) {
	worked := false
	for ctx.Err() == nil {
		items, err := s.store.DueListenBrainzLoves(ctx, s.now(), 1)
		if err != nil {
			s.logger.Warn("load ListenBrainz love queue", "error", err)
			return errorRecheck, worked
		}
		if len(items) == 0 {
			return 0, worked
		}
		item := items[0]
		if item.Artist == "" || item.Track == "" || item.Artist == "Unknown Artist" {
			_ = s.store.DropListenBrainzLove(ctx, item.TrackID, "")
			worked = true
			continue
		}
		mbid, msid, resolveErr := s.resolveTrackRecordingID(ctx, client, settings, item)
		if resolveErr != nil {
			worked = true
			if stop, delay := s.handleLoveError(ctx, item, resolveErr, false); stop {
				return delay, worked
			}
			continue
		}
		if mbid == "" && msid == "" {
			msg := "跳过《" + item.Track + "》：未匹配到 MusicBrainz/MessyBrainz Recording ID"
			if !item.Desired {
				msg = "跳过取消红心《" + item.Track + "》：未匹配到 Recording ID"
			}
			_ = s.store.DropListenBrainzLove(ctx, item.TrackID, msg)
			worked = true
			continue
		}

		s.waitAPISlot(ctx, minCallInterval)
		if ctx.Err() != nil {
			return 0, worked
		}
		score := 0
		if item.Desired {
			score = 1
		}
		err = client.SubmitRecordingFeedback(ctx, settings.UserToken, mbid, msid, score)
		worked = true
		if err == nil {
			_ = s.store.CompleteListenBrainzLove(ctx, item.TrackID, item.Desired)
			_ = s.store.SetListenBrainzLoveError(ctx, "")
			continue
		}
		if ctx.Err() != nil {
			return 0, worked
		}
		if stop, delay := s.handleLoveError(ctx, item, err, true); stop {
			return delay, worked
		}
	}
	return 0, worked
}

func (s *Service) handleLoveError(ctx context.Context, item storage.ListenBrainzLoveItem, err error, fromFeedback bool) (bool, time.Duration) {
	var apiErr *APIError
	isAPIErr := errors.As(err, &apiErr)
	switch {
	case isAPIErr && apiErr.AuthorizationBroken():
		s.handleBrokenAuthorization(ctx, apiErr)
		return true, notReadyRecheck
	case fromFeedback && isAPIErr && apiErr.Unsupported():
		msg := "当前 ListenBrainz 服务器不支持红心反馈接口（/1/feedback/recording-feedback），已暂停红心同步"
		s.logger.Warn("ListenBrainz feedback endpoint unsupported", "error", apiErr)
		_ = s.store.SetListenBrainzFeedbackUnsupported(ctx, true, msg)
		return true, time.Second
	case isAPIErr && !apiErr.Temporary():
		s.logger.Warn("ListenBrainz rejected love change", "trackId", item.TrackID, "artist", item.Artist, "track", item.Track, "error", err)
		_ = s.store.DropListenBrainzLove(ctx, item.TrackID, "放弃同步《"+item.Track+"》："+err.Error())
		return false, 0
	}
	delay := retryDelay(item.Attempts)
	if isAPIErr && apiErr.RetryAfter > delay {
		delay = min(apiErr.RetryAfter, maxRetryDelay)
	}
	s.logger.Warn("ListenBrainz love sync failed; will retry", "trackId", item.TrackID, "retryIn", delay.String(), "error", err)
	_ = s.store.DeferListenBrainzLove(ctx, item.TrackID, s.now().Add(delay), err.Error())
	_ = s.store.SetListenBrainzLoveError(ctx, err.Error())
	return true, delay
}

func (s *Service) resolveTrackRecordingID(ctx context.Context, client *Client, settings *storage.ListenBrainzSettings, item storage.ListenBrainzLoveItem) (string, string, error) {
	if item.TagRecordingMBID != "" {
		return item.TagRecordingMBID, "", nil
	}
	cache, err := s.store.ListenBrainzTrackIDCache(ctx, item.TrackID, item.Artist, item.Track, item.Album)
	if err != nil {
		return "", "", err
	}
	if cache.RecordingMBID != "" {
		return cache.RecordingMBID, "", nil
	}
	if cache.RecordingMSID != "" {
		return "", cache.RecordingMSID, nil
	}
	if settings.LookupUnsupported {
		return "", "", nil
	}
	now := s.now()
	if cache.LookupCheckedAt > 0 && now.Sub(time.Unix(cache.LookupCheckedAt, 0)) < lookupRetryInterval {
		return "", "", nil
	}

	s.waitAPISlot(ctx, minCallInterval)
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	mbid, err := client.LookupMetadata(ctx, settings.UserToken, item.Artist, item.Track, item.Album)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Unsupported() {
			s.logger.Warn("ListenBrainz metadata lookup endpoint unsupported; degrading gracefully", "error", apiErr)
			settings.LookupUnsupported = true
			_ = s.store.SetListenBrainzLookupUnsupported(ctx, true)
			return "", "", nil
		}
		return "", "", err
	}
	_ = s.store.UpsertListenBrainzLookupResult(ctx, item.TrackID, item.Artist, item.Track, item.Album, mbid, now)
	return mbid, "", nil
}

func (s *Service) runLoveBackfill(ctx context.Context, client *Client, settings *storage.ListenBrainzSettings) (time.Duration, bool) {
	run, err := s.store.ListenBrainzLoveBackfill(ctx)
	if err != nil || run.State != storage.LoveBackfillRunning {
		return 0, false
	}
	trackID, err := s.store.NextListenBrainzLoveBackfillTrack(ctx, run.Cursor)
	if err != nil {
		s.logger.Warn("load ListenBrainz love backfill page", "error", err)
		return errorRecheck, false
	}
	if trackID == 0 {
		if err := s.store.FinishListenBrainzLoveBackfill(ctx, storage.LoveBackfillDone, ""); err != nil {
			s.logger.Warn("finish ListenBrainz love backfill", "error", err)
		}
		s.resetLoveBackfillFailures()
		return 0, false
	}
	item, err := s.store.ListenBrainzLoveTrack(ctx, trackID)
	if err != nil || item.Artist == "" || item.Track == "" || item.Artist == "Unknown Artist" {
		_ = s.store.AdvanceListenBrainzLoveBackfill(ctx, trackID, 1, 0)
		s.finishBackfillIfDone(ctx, trackID)
		return loveBackfillInterval, true
	}

	mbid, msid, resolveErr := s.resolveTrackRecordingID(ctx, client, settings, item)
	if resolveErr != nil {
		return s.handleBackfillFailure(ctx, trackID, item.Track, resolveErr, false)
	}
	if mbid == "" && msid == "" {
		_ = s.store.AdvanceListenBrainzLoveBackfill(ctx, trackID, 0, 1)
		_ = s.store.SetListenBrainzLoveError(ctx, "跳过《"+item.Track+"》：未匹配到 MusicBrainz/MessyBrainz Recording ID")
		s.finishBackfillIfDone(ctx, trackID)
		return loveBackfillInterval, true
	}

	s.waitAPISlot(ctx, loveBackfillInterval)
	if ctx.Err() != nil {
		return 0, false
	}
	if err := client.SubmitRecordingFeedback(ctx, settings.UserToken, mbid, msid, 1); err != nil {
		return s.handleBackfillFailure(ctx, trackID, item.Track, err, true)
	}

	s.clearLoveBackfillFailure(trackID)
	if err := s.store.AdvanceListenBrainzLoveBackfill(ctx, trackID, 0, 0); err != nil {
		s.logger.Warn("advance ListenBrainz love backfill", "error", err)
	}
	_ = s.store.SetListenBrainzLoveError(ctx, "")
	s.finishBackfillIfDone(ctx, trackID)
	return loveBackfillInterval, true
}

func (s *Service) handleBackfillFailure(ctx context.Context, trackID int64, trackTitle string, err error, fromFeedback bool) (time.Duration, bool) {
	if ctx.Err() != nil {
		return 0, false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.AuthorizationBroken() {
			s.handleBrokenAuthorization(ctx, apiErr)
			return notReadyRecheck, true
		}
		if fromFeedback && apiErr.Unsupported() {
			msg := "当前 ListenBrainz 服务器不支持红心反馈接口（/1/feedback/recording-feedback），已停止回填"
			_ = s.store.SetListenBrainzFeedbackUnsupported(ctx, true, msg)
			_ = s.store.FinishListenBrainzLoveBackfill(ctx, storage.LoveBackfillCancelled, msg)
			s.resetLoveBackfillFailures()
			return time.Second, true
		}
	}
	failures := s.noteLoveBackfillFailure(trackID)
	if failures >= maxPermanentAttempts {
		s.logger.Warn("skipping ListenBrainz love backfill track", "trackId", trackID, "error", err)
		_ = s.store.AdvanceListenBrainzLoveBackfill(ctx, trackID, 1, 0)
		_ = s.store.SetListenBrainzLoveError(ctx, "回填时跳过《"+trackTitle+"》："+err.Error())
		s.finishBackfillIfDone(ctx, trackID)
		return loveBackfillInterval, true
	}
	_ = s.store.SetListenBrainzLoveError(ctx, err.Error())
	return max(retryDelay(failures-1), loveBackfillInterval), true
}

func (s *Service) finishBackfillIfDone(ctx context.Context, trackID int64) {
	if next, err := s.store.NextListenBrainzLoveBackfillTrack(ctx, trackID); err == nil && next == 0 {
		if err := s.store.FinishListenBrainzLoveBackfill(ctx, storage.LoveBackfillDone, ""); err != nil {
			s.logger.Warn("finish ListenBrainz love backfill", "error", err)
		}
		s.resetLoveBackfillFailures()
	}
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
