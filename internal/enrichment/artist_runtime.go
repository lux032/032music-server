package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func (m *Manager) launchArtistRunLocked(runID int64) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	done := make(chan struct{})
	m.running = true
	m.artistRunID = runID
	m.artistCancel = cancel
	m.artistDone = done
	m.goBackground("durable-artist-matching", func() {
		completed := false
		defer func() {
			if recovered := recover(); recovered != nil {
				completed = false
				m.artistRuntimeFailure(runID, fmt.Errorf("worker panic: %v", recovered))
			}
			cancel()
			m.mu.Lock()
			m.running = false
			m.artistRunID = 0
			m.artistCancel = nil
			shouldRerun := completed && m.artistAutoRerunPending && m.baseCtx.Err() == nil
			if shouldRerun {
				m.artistAutoRerunPending = false
			}
			m.mu.Unlock()
			close(done)
			if shouldRerun {
				m.startAutoArtist(m.baseCtx, true)
			}
		}()
		completed = m.executeArtistRun(ctx, runID)
	})
}
func (m *Manager) artistRuntimeFailure(runID int64, err error) {
	m.logger.Error("durable artist run persistence/execution failed", "runId", runID, "error", err)
	if e := m.store.TransitionArtistRun(context.Background(), runID, "pause", "storage_or_runtime_error"); e != nil && !errors.Is(e, storage.ErrArtistRunState) {
		m.logger.Error("pause artist run failed", "runId", runID, "error", e)
	}
}
func (m *Manager) executeArtistRun(ctx context.Context, runID int64) bool {
	for {
		if ctx.Err() != nil {
			if m.baseCtx.Err() != nil {
				if e := m.store.TransitionArtistRun(context.Background(), runID, "pause", "shutdown"); e != nil && !errors.Is(e, storage.ErrArtistRunState) {
					m.logger.Error("pause shutdown artist run", "error", e)
				}
			}
			return false
		}
		item, err := m.store.ClaimArtistRunItem(ctx, runID)
		if errors.Is(err, sql.ErrNoRows) {
			if e := m.store.TransitionArtistRun(context.Background(), runID, "complete", ""); e != nil {
				m.artistRuntimeFailure(runID, e)
				return false
			}
			return true
		}
		if err != nil {
			if !errors.Is(err, storage.ErrArtistRunState) {
				m.artistRuntimeFailure(runID, err)
			}
			return false
		}
		checkpoint := storage.ArtistRunCheckpoint{RunID: runID, ItemID: item.ID, ClaimToken: item.ClaimToken}
		runCtx := context.WithValue(ctx, artistCheckpointKey{}, &checkpoint)
		// Current effective inputs/configs are reloaded by matchArtist; snapshot is
		// an audit target, never permission to apply old identity evidence.
		rateAttempts := 0
		for {
			if item.MatchedFact {
				if err = m.store.CompleteArtistRunItem(ctx, checkpoint, "matched"); err != nil {
					if !errors.Is(err, storage.ErrArtistRunState) {
						m.artistRuntimeFailure(runID, err)
					}
					return false
				}
				break
			}
			r, e := m.store.DurableArtistRun(ctx, runID)
			if e != nil {
				m.artistRuntimeFailure(runID, e)
				return false
			}
			if r.WaitingUntil != "" {
				deadline, e := time.Parse(time.RFC3339Nano, r.WaitingUntil)
				if e != nil {
					m.artistRuntimeFailure(runID, e)
					return false
				}
				m.blockSourceUntil(r.WaitSource, deadline)
				if e = m.waitArtistSource(ctx, checkpoint, r.WaitSource, deadline); e != nil {
					return false
				}
			}
			result, matchErr := m.matchArtistWithMode(runCtx, item.ObjectID, true, storage.ParseArtistScanMode(r.ScanMode))
			if ctx.Err() != nil {
				if m.baseCtx.Err() != nil {
					m.storePauseShutdown(runID)
				}
				return false
			}
			if rate := asRateLimited(matchErr); rate != nil {
				if !rate.CooldownOnly {
					rateAttempts++
				}
				deadline := m.clockNow().Add(rateLimitBackoff(rateAttempts))
				shared := m.sourceBlockedUntil(rate.Source)
				if shared.After(deadline) {
					deadline = shared
				}
				if e = m.store.RecordArtistRunWait(ctx, checkpoint, rate.Source, deadline, 0, !rate.CooldownOnly); e != nil {
					if errors.Is(e, storage.ErrArtistWaitBudget) {
						// 预算耗尽已持久化暂停：到点后自动继续，不再等人。
						m.scheduleArtistAutoResume(runID)
					} else if !errors.Is(e, storage.ErrArtistRunState) {
						m.artistRuntimeFailure(runID, e)
					}
					return false
				}
				if e = m.waitArtistSource(ctx, checkpoint, rate.Source, deadline); e != nil {
					return false
				}
				continue
			}
			if matchErr != nil {
				if !result.AutoMatched && (errors.Is(matchErr, storage.ErrArtistRunState) || isArtistPersistenceError(matchErr)) {
					m.artistRuntimeFailure(runID, matchErr)
					return false
				}
				m.logger.Warn("artist item warning", "runId", runID, "error", matchErr)
			}
			if e = m.store.CompleteArtistRunItem(ctx, checkpoint, result.Outcome); e != nil {
				m.artistRuntimeFailure(runID, e)
				return false
			}
			// B+2/H1：自动匹配绑定成功后，尝试补全缺失头像和简介。
			// 身份已在 AutoBindArtistRunCandidate 事务内提交，此处的任何失败
			// 都不可能撤销身份；所有错误就地吞掉（Warn），绝不进入 run 的限
			// 流等待/失败通道（H3），绝不中止匹配。瞬时缺口由 /admin/matches
			// 的持久化补全任务兜底。
			if result.AutoMatched && !result.InlineAIConfirmed {
				m.cacheAutoMatchedArtistImage(ctx, item.ObjectID)
				m.refreshAutoMatchedArtistBiographies(ctx, item.ObjectID)
			}
			break
		}
	}
}
func (m *Manager) storePauseShutdown(runID int64) {
	if e := m.store.TransitionArtistRun(context.Background(), runID, "pause", "shutdown"); e != nil && !errors.Is(e, storage.ErrArtistRunState) {
		m.logger.Error("pause shutdown", "error", e)
	}
}
func isArtistPersistenceError(err error) bool {
	var coded interface{ Code() int }
	return errors.As(err, &coded)
}
func (m *Manager) waitArtistSource(ctx context.Context, c storage.ArtistRunCheckpoint, source string, deadline time.Time) error {
	for {
		if shared := m.sourceBlockedUntil(source); shared.After(deadline) {
			deadline = shared
		}
		now := m.clockNow()
		if !deadline.After(now) {
			return nil
		}
		r, err := m.store.DurableArtistRun(ctx, c.RunID)
		if err != nil {
			m.artistRuntimeFailure(c.RunID, err)
			return err
		}
		budget := 30*time.Minute - time.Duration(r.WaitTotalMS-r.BudgetBaselineMS)*time.Millisecond
		wait := deadline.Sub(now)
		if wait > budget {
			wait = budget
		}
		if wait < 0 {
			wait = 0
		}
		started := m.clockNow()
		sleepErr := m.sleep(ctx, wait)
		elapsed := m.clockNow().Sub(started)
		if elapsed < 0 {
			elapsed = 0
		}
		err = m.store.RecordArtistRunWait(context.Background(), c, source, deadline, elapsed, false)
		if err != nil {
			if errors.Is(err, storage.ErrArtistWaitBudget) {
				m.scheduleArtistAutoResume(c.RunID)
			} else if !errors.Is(err, storage.ErrArtistRunState) {
				m.artistRuntimeFailure(c.RunID, err)
			}
			return err
		}
		if sleepErr != nil {
			if m.baseCtx.Err() != nil {
				m.storePauseShutdown(c.RunID)
			}
			return sleepErr
		}
	}
}
