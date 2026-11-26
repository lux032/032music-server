package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

const (
	artistImageAutoQueueMaxAttempts = 3
	artistImageAutoQueuePace        = 250 * time.Millisecond
)

// ArtistImageAutoQueueStatus exposes live status of the persistent async
// post-confirmation avatar quality-gating & backfill worker to the admin UI.
type ArtistImageAutoQueueStatus struct {
	Enabled       bool   `json:"enabled"`
	Pending       int    `json:"pending"`
	Running       int    `json:"running"`
	Failed        int    `json:"failed"`
	CurrentArtist string `json:"currentArtist,omitempty"`
	Processed     int    `json:"processed"`
	Cached        int    `json:"cached"`
	Kept          int    `json:"kept"`
	Replaced      int    `json:"replaced"`
	Cleared       int    `json:"cleared"`
	Candidate     int    `json:"candidate"`
	Skipped       int    `json:"skipped"`
	Errored       int    `json:"errored"`
}

type artistImageAutoQueueState struct {
	mu            sync.Mutex
	workerRunning bool
	workerGen     uint64
	retryTimer    *time.Timer
	currentArtist string
	processed     int
	cached        int
	kept          int
	replaced      int
	cleared       int
	candidate     int
	skipped       int
	errored       int
	retryBackoff  func(attempt int) time.Duration
	testItemHook  func(artistID int64)
}

func newArtistImageAutoQueueState() *artistImageAutoQueueState {
	return &artistImageAutoQueueState{
		retryBackoff: defaultAutoImageRetryBackoff,
	}
}

func defaultAutoImageRetryBackoff(attempt int) time.Duration {
	switch attempt {
	case 0:
		return 1 * time.Minute
	case 1:
		return 5 * time.Minute
	default:
		return 15 * time.Minute
	}
}

func (m *Manager) ensureAutoImageQueue() *artistImageAutoQueueState {
	if m.autoImageQueue == nil {
		m.autoImageQueue = newArtistImageAutoQueueState()
	}
	return m.autoImageQueue
}

// QueueAutoConfirmedArtistImage enqueues a newly MusicBrainz-confirmed artist
// (across any role: performer, album artist, composer/lyricist/etc.) into the
// persistent async avatar pipeline and wakes the drain-on-demand background
// worker. Returns true if the artist was queued (or already queued) for async
// handling; returns false if auto_ingest_ai_enabled is off or the artist lacks
// a confirmed MusicBrainz profile / has a custom avatar.
func (m *Manager) QueueAutoConfirmedArtistImage(ctx context.Context, artistID int64, triggerSource string) bool {
	if artistID <= 0 {
		return false
	}
	imgSettings, err := m.store.ArtistImageSettings(ctx)
	if err != nil || !imgSettings.AutoIngestAIEnabled {
		return false
	}
	enqueued, err := m.store.EnqueueArtistImageAutoQueue(ctx, artistID, triggerSource)
	if err != nil {
		m.logger.Warn("enqueue artist image auto queue", "artistId", artistID, "trigger", triggerSource, "error", err)
		return false
	}
	if !enqueued {
		return false
	}
	m.WakeAutoImageQueue()
	return true
}

// WakeAutoImageQueue starts the drain-on-demand background worker if not
// already running and baseCtx is still live.
func (m *Manager) WakeAutoImageQueue() {
	if m.baseCtx.Err() != nil {
		return
	}
	q := m.ensureAutoImageQueue()
	q.mu.Lock()
	if q.retryTimer != nil {
		q.retryTimer.Stop()
		q.retryTimer = nil
	}
	if q.workerRunning {
		q.mu.Unlock()
		return
	}
	q.workerGen++
	gen := q.workerGen
	q.workerRunning = true
	q.mu.Unlock()

	m.goBackground("artist-image-auto-queue", func() {
		m.runArtistImageAutoQueue(gen)
	})
}

// ArtistImageAutoQueueStatus returns current persistent backlog counts and
// live session metrics for the admin workbench status chip.
func (m *Manager) ArtistImageAutoQueueStatus(ctx context.Context) ArtistImageAutoQueueStatus {
	out := ArtistImageAutoQueueStatus{}
	if imgSettings, err := m.store.ArtistImageSettings(ctx); err == nil {
		out.Enabled = imgSettings.AutoIngestAIEnabled
	}
	if stats, err := m.store.ArtistImageAutoQueueStats(ctx); err == nil {
		out.Pending = stats.Pending
		out.Running = stats.Running
		out.Failed = stats.Failed
	}
	q := m.ensureAutoImageQueue()
	q.mu.Lock()
	out.CurrentArtist = q.currentArtist
	out.Processed = q.processed
	out.Cached = q.cached
	out.Kept = q.kept
	out.Replaced = q.replaced
	out.Cleared = q.cleared
	out.Candidate = q.candidate
	out.Skipped = q.skipped
	out.Errored = q.errored
	q.mu.Unlock()
	return out
}

func (m *Manager) runArtistImageAutoQueue(gen uint64) {
	q := m.ensureAutoImageQueue()
	defer func() {
		q.mu.Lock()
		if q.workerGen == gen && q.workerRunning {
			q.currentArtist = ""
			q.workerRunning = false
		}
		q.mu.Unlock()
	}()

	for {
		if m.baseCtx.Err() != nil {
			return
		}
		item, err := m.store.ClaimNextArtistImageAutoQueueItem(m.baseCtx)
		if errors.Is(err, sql.ErrNoRows) {
			stats, statsErr := m.store.ArtistImageAutoQueueStats(m.baseCtx)
			q.mu.Lock()
			q.currentArtist = ""
			q.workerRunning = false
			if statsErr == nil && stats.NextRetryAt != "" && m.baseCtx.Err() == nil {
				if nextAt, parseErr := time.Parse(time.RFC3339Nano, stats.NextRetryAt); parseErr == nil {
					delay := time.Until(nextAt)
					if delay < 50*time.Millisecond {
						delay = 50 * time.Millisecond
					}
					if q.retryTimer != nil {
						q.retryTimer.Stop()
					}
					q.retryTimer = time.AfterFunc(delay, func() {
						if m.baseCtx.Err() == nil {
							m.WakeAutoImageQueue()
						}
					})
				}
			}
			q.mu.Unlock()
			return
		}
		if err != nil {
			m.logger.Warn("claim artist image auto queue item", "error", err)
			return
		}

		q.mu.Lock()
		q.currentArtist = item.ArtistName
		hook := q.testItemHook
		backoffFn := q.retryBackoff
		q.mu.Unlock()

		if hook != nil {
			hook(item.ArtistID)
		}

		outcome, errText, rate := m.processAutoQueuedArtistImage(m.baseCtx, item)
		if m.baseCtx.Err() != nil {
			// Leave row running so startup recovery resets it to pending cleanly.
			return
		}

		if rate != nil || outcome == "failed" {
			if backoffFn == nil {
				backoffFn = defaultAutoImageRetryBackoff
			}
			delay := backoffFn(item.Attempts)
			if rate != nil {
				if shared := m.sourceBlockedUntil(rate.Source); shared.After(m.clockNow()) {
					if d := shared.Sub(m.clockNow()); d > delay {
						delay = d
					}
				}
				if errText == "" {
					errText = rate.Error()
				}
			}
			terminalFail, retryErr := m.store.RetryArtistImageAutoQueueItem(m.baseCtx, item.ArtistID, delay, artistImageAutoQueueMaxAttempts, RedactSourceError(errors.New(errText)).Error())
			if retryErr != nil {
				m.logger.Warn("retry artist image auto queue item", "artistId", item.ArtistID, "error", retryErr)
			}
			if terminalFail {
				q.mu.Lock()
				q.processed++
				q.errored++
				q.mu.Unlock()
			}
		} else {
			if compErr := m.store.CompleteArtistImageAutoQueueItem(m.baseCtx, item.ArtistID); compErr != nil {
				m.logger.Warn("complete artist image auto queue item", "artistId", item.ArtistID, "error", compErr)
			}
			q.mu.Lock()
			q.processed++
			switch outcome {
			case "cached":
				q.cached++
			case "kept":
				q.kept++
			case "replaced":
				q.replaced++
			case "cleared":
				q.cleared++
			case "candidate":
				q.candidate++
			default:
				q.skipped++
			}
			q.mu.Unlock()
		}

		if m.sleep(m.baseCtx, artistImageAutoQueuePace) != nil {
			return
		}
	}
}

func (m *Manager) processAutoQueuedArtistImage(ctx context.Context, item storage.ArtistImageAutoQueueItem) (outcome, errText string, rate *RateLimitError) {
	imgSettings, err := m.store.ArtistImageSettings(ctx)
	if err != nil {
		return "failed", err.Error(), nil
	}
	if !imgSettings.AutoIngestAIEnabled {
		return "skipped", "自动入库补图开关已关闭", nil
	}
	if _, err := m.store.ArtistImageBackfillSubjectForMode(ctx, item.ArtistID, "matched"); errors.Is(err, sql.ErrNoRows) {
		return "skipped", "状态已变化（已合并/身份解除/已有自定义头像）", nil
	} else if err != nil {
		return "failed", err.Error(), nil
	}
	hasMB, err := m.store.ArtistHasConfirmedMusicBrainz(ctx, item.ArtistID)
	if err != nil {
		return "failed", err.Error(), nil
	}
	if !hasMB {
		return "skipped", "无已确认 MusicBrainz 身份", nil
	}
	hasPending, err := m.store.HasPendingArtistImageCandidate(ctx, item.ArtistID)
	if err != nil {
		return "failed", err.Error(), nil
	}
	if hasPending {
		return "skipped", "已有待审核候选头像", nil
	}

	aiSt, aiErr := m.store.AISettings(ctx)
	aiConfigured := aiErr == nil && strings.TrimSpace(aiSt.BaseURL) != "" && strings.TrimSpace(aiSt.APIKey) != "" && resolveArtistImageVisionModel(aiSt, imgSettings) != ""

	valid, err := m.hasValidArtistImage(ctx, item.ArtistID)
	if err != nil {
		return "failed", err.Error(), nil
	}
	if valid {
		// Policy 2B: already-kept cached avatars skip at zero token cost;
		// unaudited existing cached avatars receive one VLM audit + high-conf
		// replacement using the freshly confirmed MusicBrainz anchors.
		eff, effErr := m.store.ArtistEffectiveImageCache(ctx, item.ArtistID)
		if errors.Is(effErr, sql.ErrNoRows) {
			return "skipped", "缓存状态已变化", nil
		}
		if effErr != nil {
			return "failed", effErr.Error(), nil
		}
		if eff.ContentHash != "" {
			if rev, ok, rErr := m.store.ArtistImageReviewByArtist(ctx, item.ArtistID); rErr == nil && ok && rev.Verdict == "keep" && rev.ImageHash == eff.ContentHash {
				return "skipped", "当前头像已审查通过", nil
			}
		}
		if !aiConfigured {
			return "skipped", "未配置 AI 模型服务，跳过旧图审查", nil
		}
		return m.processAuditedArtistImageItemOpt(ctx, nil, item.ArtistID, false, false)
	}

	// Missing avatar: run unified Tier 1 -> Tier 2 -> Tier 3 acquisition with
	// VLM gate whenever AI is configured (or VisionEnabled is checked) and
	// automatic web fallback when shared web search is configured.
	requireVLM := imgSettings.VisionEnabled || aiConfigured
	return m.acquireArtistAvatarWithAIGate(ctx, item.ArtistID, avatarAcquireOptions{
		Checkpoint:        nil,
		RunMode:           "auto_ingest",
		AllowWebFallback:  true,
		RespectRunBudget:  false,
		RequireVLM:        requireVLM,
		RefreshProfiles:   item.TriggerSource != "auto_match",
		RecordMissCooling: true,
	})
}
