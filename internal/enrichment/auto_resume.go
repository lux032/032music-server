package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// 限流自动恢复：预算耗尽的限流任务持久化 waiting_until 后暂停，管理器按
// 该截止时间注册定时器，到点后自动继续（复用手动 resume 的全部状态保护）。
// 手动暂停、存储错误与轮次用尽的 run 永远不会被自动启动。
type autoResumeEntry struct{ cancel context.CancelFunc }

const (
	artistAutoResumeKind     = "artist"
	enrichmentAutoResumeKind = "enrichment"
)

func autoResumeKey(kind string, runID int64) string {
	return kind + ":" + strconv.FormatInt(runID, 10)
}

// armAutoResume registers (or replaces) the wake-up timer for one run. The
// wait uses m.sleep so tests can drive it with the same fake clock as the
// rate-limit waits; the timer dies with baseCtx and never outlives shutdown.
func (m *Manager) armAutoResume(kind string, runID int64, deadline time.Time) {
	m.armAutoResumeAttempt(kind, runID, deadline, false)
}

// armAutoResumeAttempt is armAutoResume with the one-shot retry marker: a
// retried timer that fails again is logged and dropped instead of looping.
func (m *Manager) armAutoResumeAttempt(kind string, runID int64, deadline time.Time, retried bool) {
	key := autoResumeKey(kind, runID)
	ctx, cancel := context.WithCancel(m.baseCtx)
	entry := &autoResumeEntry{cancel: cancel}
	m.autoResumeMu.Lock()
	if old, ok := m.autoResumeTimers[key]; ok {
		old.cancel()
	}
	m.autoResumeTimers[key] = entry
	m.autoResumeMu.Unlock()
	m.goBackground("auto-resume-"+key, func() {
		defer func() {
			cancel()
			m.autoResumeMu.Lock()
			if m.autoResumeTimers[key] == entry {
				delete(m.autoResumeTimers, key)
			}
			m.autoResumeMu.Unlock()
		}()
		if delay := deadline.Sub(m.clockNow()); delay > 0 {
			if err := m.sleep(ctx, delay); err != nil {
				return
			}
		}
		m.fireAutoResume(ctx, kind, runID, retried)
	})
}

func (m *Manager) cancelAutoResume(kind string, runID int64) {
	key := autoResumeKey(kind, runID)
	m.autoResumeMu.Lock()
	if entry, ok := m.autoResumeTimers[key]; ok {
		delete(m.autoResumeTimers, key)
		entry.cancel()
	}
	m.autoResumeMu.Unlock()
}

// scheduleArtistAutoResume arms a timer only when the persisted run is still
// a paused rate-limit run with a deadline; anything else is left to the user.
func (m *Manager) scheduleArtistAutoResume(runID int64) {
	run, err := m.store.DurableArtistRun(context.Background(), runID)
	if err != nil || !storage.ArtistRunAutoResumeEligible(run) {
		return
	}
	deadline, err := time.Parse(time.RFC3339Nano, run.WaitingUntil)
	if err != nil {
		m.logger.Error("artist auto resume skipped: unreadable persisted deadline", "runId", runID, "error", err)
		return
	}
	m.armAutoResume(artistAutoResumeKind, runID, deadline)
}

func (m *Manager) scheduleEnrichmentAutoResume(runID int64) {
	run, err := m.store.DurableEnrichmentRun(context.Background(), runID)
	if err != nil || !storage.EnrichmentRunAutoResumeEligible(run) {
		return
	}
	deadline, err := time.Parse(time.RFC3339Nano, run.WaitingUntil)
	if err != nil {
		m.logger.Error("enrichment auto resume skipped: unreadable persisted deadline", "runId", runID, "error", err)
		return
	}
	m.armAutoResume(enrichmentAutoResumeKind, runID, deadline)
}

// ScanAutoResumeRuns arms timers for rate-limit runs paused before a restart.
// It runs after New's recovery step (production calls it from main); manual
// pauses and storage errors are never picked up.
func (m *Manager) ScanAutoResumeRuns() {
	ctx := context.Background()
	artistRuns, err := m.store.ArtistRunsAwaitingAutoResume(ctx)
	if err != nil {
		m.logger.Error("scan artist runs awaiting auto resume", "error", err)
	} else {
		for _, run := range artistRuns {
			m.scheduleArtistAutoResume(run.ID)
		}
	}
	enrichmentRuns, err := m.store.EnrichmentRunsAwaitingAutoResume(ctx)
	if err != nil {
		m.logger.Error("scan enrichment runs awaiting auto resume", "error", err)
	} else {
		for _, run := range enrichmentRuns {
			m.scheduleEnrichmentAutoResume(run.ID)
		}
	}
}

// fireAutoResume re-reads the run at fire time: a user cancel/pause or a
// completed run makes the timer a no-op. State conflicts are expected user
// races and must never be reported as storage_or_runtime_error. Transient
// storage errors retry exactly once; a timer is never dropped silently.
func (m *Manager) fireAutoResume(ctx context.Context, kind string, runID int64, retried bool) {
	if kind == artistAutoResumeKind {
		run, err := m.store.DurableArtistRun(ctx, runID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				m.logger.Info("artist auto resume skipped: run gone", "runId", runID)
			} else {
				m.retryAutoResumeOnce(kind, runID, retried, err)
			}
			return
		}
		if !storage.ArtistRunAutoResumeEligible(run) {
			m.logger.Info("artist auto resume skipped: run state changed", "runId", runID)
			return
		}
		m.mu.Lock()
		done, oldID := m.artistDone, m.artistRunID
		m.mu.Unlock()
		if oldID == runID && done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return
			}
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.running {
			// 单活跃 run 不变式下几乎不可达：paused run 不占 running 名额，
			// 同名任务此时只能是被手动 resume 抢先启动；该 run 已由新 worker
			// 接管，无需本定时器再做任何事（重启扫描与手动继续仍可兜底）。
			m.logger.Info("artist auto resume skipped: run already active", "runId", runID)
			return
		}
		if err = m.store.TransitionArtistRun(context.Background(), runID, "auto_resume", ""); err != nil {
			if errors.Is(err, storage.ErrArtistRunState) {
				m.logger.Info("artist auto resume skipped: run state changed", "runId", runID)
			} else {
				m.retryAutoResumeOnce(kind, runID, retried, err)
			}
			return
		}
		m.logger.Info("artist run auto resumed after rate-limit backoff", "runId", runID)
		m.launchArtistRunLocked(runID)
		return
	}
	run, err := m.store.DurableEnrichmentRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			m.logger.Info("enrichment auto resume skipped: run gone", "runId", runID)
		} else {
			m.retryAutoResumeOnce(kind, runID, retried, err)
		}
		return
	}
	if !storage.EnrichmentRunAutoResumeEligible(run) {
		m.logger.Info("enrichment auto resume skipped: run state changed", "runId", runID)
		return
	}
	m.phaseMu.Lock()
	done, oldID := m.phaseDone, m.phaseRunID
	m.phaseMu.Unlock()
	if oldID == runID && done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return
		}
	}
	m.phaseMu.Lock()
	defer m.phaseMu.Unlock()
	if m.phaseRunning {
		// 与 artist 分支同理：几乎不可达；真冲突说明任务已被接管。
		m.logger.Info("enrichment auto resume skipped: run already active", "runId", runID)
		return
	}
	if err = m.store.TransitionEnrichmentRun(context.Background(), runID, "auto_resume", ""); err != nil {
		if errors.Is(err, storage.ErrEnrichmentRunState) {
			m.logger.Info("enrichment auto resume skipped: run state changed", "runId", runID)
		} else {
			m.retryAutoResumeOnce(kind, runID, retried, err)
		}
		return
	}
	run, err = m.store.DurableEnrichmentRun(ctx, runID)
	if err != nil {
		m.retryAutoResumeOnce(kind, runID, retried, err)
		return
	}
	m.logger.Info("enrichment run auto resumed after rate-limit backoff", "runId", runID)
	m.launchDurablePhaseLocked(run)
}

// retryAutoResumeOnce re-arms the timer exactly once after a transient
// storage error; a second failure is logged as an error and left to the next
// startup scan or a manual resume, never retried forever.
func (m *Manager) retryAutoResumeOnce(kind string, runID int64, retried bool, err error) {
	if retried {
		m.logger.Error("auto resume retry failed; left to startup scan or manual resume", "kind", kind, "runId", runID, "error", err)
		return
	}
	m.logger.Warn("auto resume attempt failed; retrying once", "kind", kind, "runId", runID, "error", err)
	m.armAutoResumeAttempt(kind, runID, m.clockNow().Add(time.Minute), true)
}
