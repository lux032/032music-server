package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// 歌手简介补全：持久化 run+items、启动恢复、逐项 claim、暂停/继续/停止、
// 限流等待预算与自动恢复。与身份匹配/头像补全 durable run 互为独立任务
// （可并行），带宽共享由现有来源限流原语保证（waitMBRateLimit 的请求间隔、
// blockedUntil 共享冷却桶）；补全只消费已确认身份，绝不改写身份，绝不进入
// 身份匹配 run 的等待预算通道，也绝不触碰 artists.user_biography。

// ErrNoArtistBiographyBackfillCandidates 表示当前没有需要补全简介的歌手。
var ErrNoArtistBiographyBackfillCandidates = errors.New("没有需要补全简介的歌手")

// ErrNoBiographyLanguages 表示简介语言配置为空且未启用英文回退（P2-6），
// 与“未确认身份”的 sql.ErrNoRows 明确区分。
var ErrNoBiographyLanguages = errors.New("no biography languages configured")

// artistBiographyBackfillInterval 是逐项之间的请求间隔（Wikipedia/Wikidata/
// Last.fm 没有自带的请求间隔限制器，这里是唯一的节奏控制）；测试用 fake
// clock 观测。
const artistBiographyBackfillInterval = 300 * time.Millisecond

// SetArtistBiographyBackfillTestHook 安装简介补全的测试钩子（在补全项的
// 资格/新鲜度复查与身份快照之后、网络抓取之前调用）。只供测试使用，让
// 并发解除/改认身份或暂停任务的写入拒绝成为确定性事件。
func (m *Manager) SetArtistBiographyBackfillTestHook(hook func(artistID int64)) {
	m.testBioBackfillHook = hook
}

// refreshAutoMatchedArtistBiographies 在批量自动匹配成功绑定身份后尽力补
// 简介（分叉 1A）。相关来源冷却期内直接短路；所有错误只记日志——绝不传播
// 进身份匹配 run 的限流/失败通道（H3），绝不撤销已提交的身份。瞬时失败留
// 下的缺口由 /admin/matches 的歌手简介补全持久化任务兜底。
func (m *Manager) refreshAutoMatchedArtistBiographies(ctx context.Context, artistID int64) {
	// P1-1：范围仅歌手——纯幕后（仅作曲/作词/编曲/制作）绝不发简介请求。
	performer, err := m.store.ArtistIsPerformer(ctx, artistID)
	if err != nil {
		m.logger.Warn("check performer for biography refresh after automatic match", "artistId", artistID, "error", err)
		return
	}
	if !performer {
		return
	}
	for _, source := range []string{"musicbrainz", "wikidata", "wikipedia", "lastfm"} {
		if m.checkSourceCooldown(source) != nil {
			return
		}
	}
	if err := m.RefreshArtistBiographies(ctx, artistID, false); err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrNoBiographyLanguages) {
		m.logger.Warn("refresh biographies after automatic match", "artistId", artistID, "error", RedactSourceError(err))
	}
}

// ArtistBiographyBackfillStats 统计未合并歌手总数与其中缺简介（候选口径）
// 的数量。只读 DB，不访问网络。
func (m *Manager) ArtistBiographyBackfillStats(ctx context.Context) (missing, total int, err error) {
	total, err = m.store.ArtistBiographyPerformerCount(ctx)
	if err != nil {
		return 0, 0, err
	}
	candidates, err := m.store.ArtistBiographyBackfillCandidates(ctx)
	if err != nil {
		return 0, 0, err
	}
	return len(candidates), total, nil
}

// StartArtistBiographyBackfill 启动一轮持久化歌手简介补全：候选为未合并、
// 有实际演唱/专辑关系、本行无人工简介、本人与合并来源均无 found 版本行的
// 歌手。已有活动或暂停中的补全任务时返回
// storage.ErrArtistBiographyBackfillState。
func (m *Manager) StartArtistBiographyBackfill(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bioBackfillRunning {
		return 0, ErrRunNotActive
	}
	if _, err := m.store.UnfinishedArtistBiographyBackfillRun(ctx); err == nil {
		return 0, storage.ErrArtistBiographyBackfillState
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	items, err := m.store.ArtistBiographyBackfillCandidates(ctx)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, ErrNoArtistBiographyBackfillCandidates
	}
	runID, err := m.store.CreateArtistBiographyBackfillRun(ctx, items)
	if err != nil {
		return 0, err
	}
	m.launchArtistBiographyBackfillLocked(runID)
	return runID, nil
}

func (m *Manager) launchArtistBiographyBackfillLocked(runID int64) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	done := make(chan struct{})
	m.bioBackfillRunning = true
	m.bioBackfillRunID = runID
	m.bioBackfillCancel = cancel
	m.bioBackfillDone = done
	m.goBackground("artist-biography-backfill", func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				m.artistBiographyBackfillFailure(runID, fmt.Errorf("worker panic: %v", recovered))
			}
			cancel()
			m.mu.Lock()
			m.bioBackfillRunning = false
			m.bioBackfillRunID = 0
			m.bioBackfillCancel = nil
			m.mu.Unlock()
			close(done)
		}()
		m.executeArtistBiographyBackfill(ctx, runID)
	})
}

func (m *Manager) artistBiographyBackfillFailure(runID int64, err error) {
	m.logger.Error("artist biography backfill persistence/execution failed", "runId", runID, "error", err)
	if e := m.store.TransitionArtistBiographyBackfillRun(context.Background(), runID, "pause", "storage_or_runtime_error"); e != nil && !errors.Is(e, storage.ErrArtistBiographyBackfillState) {
		m.logger.Error("pause artist biography backfill failed", "runId", runID, "error", e)
	}
}

func (m *Manager) executeArtistBiographyBackfill(ctx context.Context, runID int64) {
	for {
		if ctx.Err() != nil {
			if m.baseCtx.Err() != nil {
				m.pauseArtistBiographyBackfillShutdown(runID)
			}
			return
		}
		item, err := m.store.ClaimArtistBiographyBackfillItem(ctx, runID)
		if errors.Is(err, sql.ErrNoRows) {
			if e := m.store.TransitionArtistBiographyBackfillRun(context.Background(), runID, "complete", ""); e != nil {
				m.artistBiographyBackfillFailure(runID, e)
			}
			return
		}
		if err != nil {
			if !errors.Is(err, storage.ErrArtistBiographyBackfillState) {
				m.artistBiographyBackfillFailure(runID, err)
			}
			return
		}
		checkpoint := storage.ArtistBiographyBackfillCheckpoint{RunID: runID, ItemID: item.ID, ClaimToken: item.ClaimToken}
		rateAttempts := 0
		for {
			outcome, errText, rate := m.processArtistBiographyBackfillItem(ctx, checkpoint, item)
			if ctx.Err() != nil {
				if m.baseCtx.Err() != nil {
					m.pauseArtistBiographyBackfillShutdown(runID)
				}
				return
			}
			if rate != nil {
				if !rate.CooldownOnly {
					rateAttempts++
				}
				deadline := m.clockNow().Add(rateLimitBackoff(rateAttempts))
				if shared := m.sourceBlockedUntil(rate.Source); shared.After(deadline) {
					deadline = shared
				}
				if e := m.store.RecordArtistBiographyBackfillWait(ctx, checkpoint, rate.Source, deadline, 0, !rate.CooldownOnly); e != nil {
					if errors.Is(e, storage.ErrArtistBiographyBackfillBudget) {
						// 预算耗尽已持久化暂停：到点后自动继续，不再等人。
						m.scheduleArtistBiographyBackfillAutoResume(runID)
					} else if !errors.Is(e, storage.ErrArtistBiographyBackfillState) {
						m.artistBiographyBackfillFailure(runID, e)
					}
					return
				}
				if e := m.waitArtistBiographyBackfill(ctx, checkpoint, rate.Source, deadline); e != nil {
					return
				}
				continue
			}
			if e := m.store.CompleteArtistBiographyBackfillItem(ctx, checkpoint, outcome, errText); e != nil {
				// 状态冲突是人工暂停/停止/恢复抢先落地的预期竞态（item
				// 保留 in_progress 断点，恢复后重算），正常退出，绝不记 ERROR。
				if errors.Is(e, storage.ErrArtistBiographyBackfillState) {
					return
				}
				m.artistBiographyBackfillFailure(runID, e)
				return
			}
			break
		}
		// 逐项间隔是 Wikipedia/Wikidata/Last.fm 唯一的节奏控制；取消即停。
		if m.sleep(ctx, artistBiographyBackfillInterval) != nil {
			if m.baseCtx.Err() != nil {
				m.pauseArtistBiographyBackfillShutdown(runID)
			}
			return
		}
	}
}

func (m *Manager) pauseArtistBiographyBackfillShutdown(runID int64) {
	if e := m.store.TransitionArtistBiographyBackfillRun(context.Background(), runID, "pause", "shutdown"); e != nil && !errors.Is(e, storage.ErrArtistBiographyBackfillState) {
		m.logger.Error("pause shutdown artist biography backfill", "error", e)
	}
}

// processArtistBiographyBackfillItem 处理一个补全项：先实时复查资格（合并/
// 关系变化/人工简介/已被其他路径补齐），再逐项重读设置并复查新鲜度（含
// fresh missing 不再反复打源），无已确认 MBID（含合并继承）计
// skipped_no_mbid 且零网络；抓取主体复用 refreshArtistBiographies 的
// force=false 变体并以 checkpoint+身份快照 guarded 落库。限流以
// RateLimitError 返回供 run 循环等待重试，其余失败归入各终态 outcome。
func (m *Manager) processArtistBiographyBackfillItem(ctx context.Context, checkpoint storage.ArtistBiographyBackfillCheckpoint, item storage.ArtistBiographyBackfillItem) (outcome, errText string, rate *RateLimitError) {
	if _, err := m.store.ArtistBiographyBackfillSubject(ctx, item.ArtistID); errors.Is(err, sql.ErrNoRows) {
		// P2-1：本项可能在限流前已写入部分 found 行（或并发路径已补齐），
		// 放宽复查（不含缺口条件）后继续以 force=false 补剩余语言，最终按
		// filled/missing 结算，绝不把部分完成误判为 skipped_state。
		if _, retryErr := m.store.ArtistBiographyBackfillRetrySubject(ctx, item.ArtistID); errors.Is(retryErr, sql.ErrNoRows) {
			return "skipped_state", "状态已变化（已合并/已有简介或人工简介/不再是歌手）", nil
		} else if retryErr != nil {
			return "failed", retryErr.Error(), nil
		}
	} else if err != nil {
		return "failed", err.Error(), nil
	}

	// 逐项重读配置（M6/R7）：语言优先级、来源启用/密钥、cache_days 绝不
	// 使用 run 创建时的快照。
	settings, err := m.store.BiographySettings(ctx)
	if err != nil {
		return "failed", err.Error(), nil
	}
	languages := languageList(settings.PreferredLanguages, settings.EnglishFallback)
	if len(languages) == 0 {
		return "skipped_source_disabled", "未配置简介语言", nil
	}
	lastFMSetting, lastFMErr := m.store.MetadataSourceSetting(ctx, "lastfm")
	if lastFMErr != nil {
		return "failed", lastFMErr.Error(), nil
	}
	wikipediaEnabled := settings.WikipediaEnabled
	lastFMEnabled := lastFMSetting.Enabled && lastFMSetting.APIKey != ""
	if !wikipediaEnabled && !lastFMEnabled {
		return "skipped_source_disabled", "所有简介来源均已禁用或未配置密钥", nil
	}
	wikipediaNeeded := wikipediaEnabled && m.biographySourceNeedsRefresh(ctx, item.ArtistID, "wikipedia", languages, settings.CacheDays)
	lastFMNeeded := lastFMEnabled && m.biographySourceNeedsRefresh(ctx, item.ArtistID, "lastfm", languages, settings.CacheDays)
	if !wikipediaNeeded && !lastFMNeeded {
		// M4：fresh missing 同样在缓存窗口内跳过，绝不每次点按钮反复打源。
		return "skipped_fresh", "", nil
	}

	identity, identityErr := m.store.ArtistBiographyIdentity(ctx, item.ArtistID)
	if identityErr != nil && !errors.Is(identityErr, sql.ErrNoRows) {
		return "failed", identityErr.Error(), nil
	}
	if identityErr != nil || identity.MBID == "" {
		// 无已确认 MBID（含合并继承）：计跳过、绝不匹配身份、零网络请求。
		return "skipped_no_mbid", "未确认 MusicBrainz 身份，无法补全简介", nil
	}

	if m.testBioBackfillHook != nil {
		m.testBioBackfillHook(item.ArtistID)
	}
	if err = m.refreshArtistBiographies(ctx, item.ArtistID, false, &checkpoint); err != nil {
		if rateErr := asRateLimited(err); rateErr != nil {
			return "", "", rateErr
		}
		if errors.Is(err, storage.ErrArtistBiographyBackfillState) {
			// 网络窗口内身份被解除/改认、歌手被合并或任务被暂停/停止，
			// guarded 写入已拒绝——绝不写旧身份的迟到简介，item 记 skipped。
			return "skipped_state", "身份或任务状态已变化，简介未写入", nil
		}
		if errors.Is(err, sql.ErrNoRows) {
			return "skipped_state", "身份在抓取前已解除", nil
		}
		// 错误文本可能来自含 api_key 的请求链路，先脱敏再持久化。
		return "failed", RedactSourceError(err).Error(), nil
	}
	found, err := m.store.ArtistBiographyFoundExists(ctx, item.ArtistID)
	if err != nil {
		return "failed", err.Error(), nil
	}
	// 分叉 C1：身份来自合并来源时在 item error 字段注明属主行，便于审计。
	note := ""
	if identity.OwnerID != item.ArtistID {
		note = fmt.Sprintf("身份来自合并来源 #%d", identity.OwnerID)
	}
	if !found {
		return "missing", note, nil
	}
	return "filled", note, nil
}

// waitArtistBiographyBackfill 等待限流截止时间：共享冷却较长者顺延，等待
// 切片受 30 分钟窗口预算约束并逐段持久化（重启后可恢复）。
func (m *Manager) waitArtistBiographyBackfill(ctx context.Context, c storage.ArtistBiographyBackfillCheckpoint, source string, deadline time.Time) error {
	for {
		if shared := m.sourceBlockedUntil(source); shared.After(deadline) {
			deadline = shared
		}
		now := m.clockNow()
		if !deadline.After(now) {
			return nil
		}
		r, err := m.store.DurableArtistBiographyBackfillRun(ctx, c.RunID)
		if err != nil {
			m.artistBiographyBackfillFailure(c.RunID, err)
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
		err = m.store.RecordArtistBiographyBackfillWait(context.Background(), c, source, deadline, elapsed, false)
		if err != nil {
			if errors.Is(err, storage.ErrArtistBiographyBackfillBudget) {
				m.scheduleArtistBiographyBackfillAutoResume(c.RunID)
			} else if !errors.Is(err, storage.ErrArtistBiographyBackfillState) {
				m.artistBiographyBackfillFailure(c.RunID, err)
			}
			return err
		}
		if sleepErr != nil {
			if m.baseCtx.Err() != nil {
				m.pauseArtistBiographyBackfillShutdown(c.RunID)
			}
			return sleepErr
		}
	}
}

// PauseArtistBiographyBackfill 暂停进行中的补全任务；已完成的项目保留进度。
func (m *Manager) PauseArtistBiographyBackfill(runID int64) error {
	return m.stopArtistBiographyBackfill(runID, "pause")
}

// CancelArtistBiographyBackfill 停止补全任务（终态，不可继续）。
func (m *Manager) CancelArtistBiographyBackfill(runID int64) error {
	return m.stopArtistBiographyBackfill(runID, "cancel")
}

func (m *Manager) stopArtistBiographyBackfill(runID int64, action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.TransitionArtistBiographyBackfillRun(context.Background(), runID, action, "manual"); err != nil {
		if errors.Is(err, storage.ErrArtistBiographyBackfillState) {
			return ErrRunNotActive
		}
		return err
	}
	m.cancelAutoResume(artistBiographyBackfillAutoResumeKind, runID)
	if m.bioBackfillRunID == runID && m.bioBackfillCancel != nil {
		m.bioBackfillCancel()
	}
	return nil
}

// ResumeArtistBiographyBackfill 继续暂停中的补全任务：先等旧 worker 退出
// 再转移状态，旧 loop 不可能用新纪元的 token 写入。
func (m *Manager) ResumeArtistBiographyBackfill(ctx context.Context, runID int64) error {
	m.mu.Lock()
	done := m.bioBackfillDone
	oldID := m.bioBackfillRunID
	m.mu.Unlock()
	if oldID == runID && done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bioBackfillRunning {
		return ErrRunNotActive
	}
	if err := m.store.TransitionArtistBiographyBackfillRun(ctx, runID, "resume", ""); err != nil {
		return err
	}
	m.cancelAutoResume(artistBiographyBackfillAutoResumeKind, runID)
	m.launchArtistBiographyBackfillLocked(runID)
	return nil
}
