package enrichment

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// B+2 已匹配艺术家头像补全：持久化 run+items、启动恢复、逐项 claim、
// 暂停/继续/停止、限流等待预算与自动恢复。与身份匹配 durable run 互为独立
// 任务（可同时运行），带宽共享由现有来源限流原语保证（waitMBRateLimit 的
// 请求间隔、blockedUntil 共享冷却桶）；补全绝不改写身份，也绝不进入身份
// 匹配 run 的等待预算通道。

// ErrNoArtistImageBackfillCandidates 表示当前没有需要补全头像的艺术家。
var ErrNoArtistImageBackfillCandidates = errors.New("没有需要补全头像的艺术家")

// artistImageBackfillInterval 是逐项之间的请求间隔（下载与 Last.fm 查询
// 没有自带的请求间隔限制器，这里是唯一的节奏控制）；测试用 fake clock 观测。
const artistImageBackfillInterval = 300 * time.Millisecond

// hasValidArtistImage 报告艺术家当前是否已有有效头像（自定义或磁盘文件
// 确实存在的自动缓存）。“缓存行在、文件丢”按缺失处理（Medium-5 自愈）。
// 只读本地状态，不访问网络。
func (m *Manager) hasValidArtistImage(ctx context.Context, artistID int64) (bool, error) {
	path, _, isCustom, err := m.store.ArtistImagePath(ctx, artistID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if isCustom {
		// DB 级候选已排除自定义头像；这里兜底并发竞态：视作“已有图”。
		return true, nil
	}
	// cache_path 可能为相对路径（数据目录相对配置时）：按现状 as-is 解析，
	// 与 handleArtistImage 的读盘行为保持一致。
	if _, statErr := os.Stat(path); statErr == nil {
		return true, nil
	}
	return false, nil
}

// scanArtistImageBackfill 批量列出 D50 候选并做磁盘存在性检查（一次 SQL
// 取全部候选的有效缓存路径，循环内只做 os.Stat，避免逐艺术家 N+1 查询）。
// 返回候选总数、本地有效缓存数与缺失候选列表。只读 DB 与磁盘，不访问网络。
func (m *Manager) scanArtistImageBackfill(ctx context.Context) (total, cached int, missing []storage.ArtistImageBackfillCandidate, err error) {
	rows, err := m.store.ArtistImageBackfillCacheScan(ctx)
	if err != nil {
		return 0, 0, nil, err
	}
	for _, row := range rows {
		total++
		if row.CachePath != "" {
			// cache_path 可能为相对路径（数据目录相对配置时）：按现状 as-is
			// 解析，与 handleArtistImage 的读盘行为保持一致。
			if _, statErr := os.Stat(row.CachePath); statErr == nil {
				cached++
				continue
			}
		}
		missing = append(missing, row.Candidate)
	}
	return total, cached, missing, nil
}

// ArtistImageBackfillStats 统计已确认身份且无自定义头像的艺术家中，本地
// 有效头像的覆盖情况。只读 DB 与磁盘，不访问网络。
func (m *Manager) ArtistImageBackfillStats(ctx context.Context) (cached, total int, err error) {
	total, cached, _, err = m.scanArtistImageBackfill(ctx)
	return cached, total, err
}

// cacheAutoMatchedArtistImage 在批量自动匹配成功绑定身份后尽力缓存头像
// （B+2 目标 1）。冷却期内直接短路；所有错误只记日志——绝不传播进 run 的
// 限流/错误通道，绝不撤销已提交的身份。库存无 URL 时静默跳过。
func (m *Manager) cacheAutoMatchedArtistImage(ctx context.Context, artistID int64) {
	if m.checkSourceCooldown("artist image") != nil {
		return
	}
	if err := m.CacheArtistImage(ctx, artistID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		m.logger.Warn("cache artist image after automatic match", "artistId", artistID, "error", err)
	}
}

// StartArtistImageBackfill 启动一轮持久化头像补全：候选为已确认身份、未
// 合并、本人与合并来源均无自定义头像且本地无有效缓存的艺术家。已有活动
// 或暂停中的补全任务时返回 storage.ErrArtistImageBackfillState。
func (m *Manager) StartArtistImageBackfill(ctx context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.imageBackfillRunning {
		return 0, ErrRunNotActive
	}
	if _, err := m.store.UnfinishedArtistImageBackfillRun(ctx); err == nil {
		return 0, storage.ErrArtistImageBackfillState
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	_, _, items, err := m.scanArtistImageBackfill(ctx)
	if err != nil {
		return 0, err
	}
	if len(items) == 0 {
		return 0, ErrNoArtistImageBackfillCandidates
	}
	runID, err := m.store.CreateArtistImageBackfillRun(ctx, items)
	if err != nil {
		return 0, err
	}
	m.launchArtistImageBackfillLocked(runID)
	return runID, nil
}

func (m *Manager) launchArtistImageBackfillLocked(runID int64) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	done := make(chan struct{})
	m.imageBackfillRunning = true
	m.imageBackfillRunID = runID
	m.imageBackfillCancel = cancel
	m.imageBackfillDone = done
	m.goBackground("artist-image-backfill", func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				m.artistImageBackfillFailure(runID, fmt.Errorf("worker panic: %v", recovered))
			}
			cancel()
			m.mu.Lock()
			m.imageBackfillRunning = false
			m.imageBackfillRunID = 0
			m.imageBackfillCancel = nil
			m.mu.Unlock()
			close(done)
		}()
		m.executeArtistImageBackfill(ctx, runID)
	})
}

func (m *Manager) artistImageBackfillFailure(runID int64, err error) {
	m.logger.Error("artist image backfill persistence/execution failed", "runId", runID, "error", err)
	if e := m.store.TransitionArtistImageBackfillRun(context.Background(), runID, "pause", "storage_or_runtime_error"); e != nil && !errors.Is(e, storage.ErrArtistImageBackfillState) {
		m.logger.Error("pause artist image backfill failed", "runId", runID, "error", e)
	}
}

func (m *Manager) executeArtistImageBackfill(ctx context.Context, runID int64) {
	for {
		if ctx.Err() != nil {
			if m.baseCtx.Err() != nil {
				m.pauseArtistImageBackfillShutdown(runID)
			}
			return
		}
		item, err := m.store.ClaimArtistImageBackfillItem(ctx, runID)
		if errors.Is(err, sql.ErrNoRows) {
			if e := m.store.TransitionArtistImageBackfillRun(context.Background(), runID, "complete", ""); e != nil {
				m.artistImageBackfillFailure(runID, e)
			}
			return
		}
		if err != nil {
			if !errors.Is(err, storage.ErrArtistImageBackfillState) {
				m.artistImageBackfillFailure(runID, err)
			}
			return
		}
		checkpoint := storage.ArtistImageBackfillCheckpoint{RunID: runID, ItemID: item.ID, ClaimToken: item.ClaimToken}
		rateAttempts := 0
		for {
			outcome, errText, rate := m.processArtistImageBackfillItem(ctx, checkpoint, item)
			if ctx.Err() != nil {
				if m.baseCtx.Err() != nil {
					m.pauseArtistImageBackfillShutdown(runID)
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
				if e := m.store.RecordArtistImageBackfillWait(ctx, checkpoint, rate.Source, deadline, 0, !rate.CooldownOnly); e != nil {
					if errors.Is(e, storage.ErrArtistImageBackfillBudget) {
						// 预算耗尽已持久化暂停：到点后自动继续，不再等人。
						m.scheduleArtistImageBackfillAutoResume(runID)
					} else if !errors.Is(e, storage.ErrArtistImageBackfillState) {
						m.artistImageBackfillFailure(runID, e)
					}
					return
				}
				if e := m.waitArtistImageBackfill(ctx, checkpoint, rate.Source, deadline); e != nil {
					return
				}
				continue
			}
			if e := m.store.CompleteArtistImageBackfillItem(ctx, checkpoint, outcome, errText); e != nil {
				// P2-1：状态冲突是人工暂停/停止/恢复抢先落地的预期竞态（item
				// 保留 in_progress 断点，恢复后重算），正常退出，绝不记 ERROR。
				if errors.Is(e, storage.ErrArtistImageBackfillState) {
					return
				}
				m.artistImageBackfillFailure(runID, e)
				return
			}
			break
		}
		// 逐项间隔是下载与 Last.fm 查询唯一的节奏控制；取消即停。
		if m.sleep(ctx, artistImageBackfillInterval) != nil {
			if m.baseCtx.Err() != nil {
				m.pauseArtistImageBackfillShutdown(runID)
			}
			return
		}
	}
}

func (m *Manager) pauseArtistImageBackfillShutdown(runID int64) {
	if e := m.store.TransitionArtistImageBackfillRun(context.Background(), runID, "pause", "shutdown"); e != nil && !errors.Is(e, storage.ErrArtistImageBackfillState) {
		m.logger.Error("pause shutdown artist image backfill", "error", e)
	}
}

// processArtistImageBackfillItem 处理一个补全项：先实时复查资格（合并/身份
// 解除/并发自定义头像/已补齐），库存 URL 优先直接下载；空 URL 仅向已确认
// 身份对应的启用来源查询资料取 URL 后下载。限流以 RateLimitError 返回供
// run 循环等待重试，其余失败归入各终态 outcome。
func (m *Manager) processArtistImageBackfillItem(ctx context.Context, checkpoint storage.ArtistImageBackfillCheckpoint, item storage.ArtistImageBackfillItem) (outcome, errText string, rate *RateLimitError) {
	if _, err := m.store.ArtistImageBackfillSubject(ctx, item.ArtistID); errors.Is(err, sql.ErrNoRows) {
		return "skipped", "状态已变化（已合并/身份解除/出现自定义头像）", nil
	} else if err != nil {
		return "failed", err.Error(), nil
	}
	valid, err := m.hasValidArtistImage(ctx, item.ArtistID)
	if err != nil {
		return "failed", err.Error(), nil
	}
	if valid {
		return "skipped", "已有有效头像", nil
	}

	source, externalID, remoteURL, err := m.store.ArtistImageProfileSource(ctx, item.ArtistID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "failed", err.Error(), nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		// 库存 URL 为空：只查询已确认身份对应的启用来源，绝不重新判定身份。
		if queryErr := m.queryConfirmedArtistImageURL(ctx, item.ArtistID, &checkpoint); queryErr != nil {
			if rateErr := asRateLimited(queryErr); rateErr != nil {
				return "", "", rateErr
			}
			if errors.Is(queryErr, storage.ErrArtistImageBackfillState) {
				// P1-1：网络查询期间身份被解除/改认或任务被暂停/停止，guarded
				// 写入已拒绝——绝不复活或覆盖身份，item 记 skipped。
				return "skipped", "身份或任务状态已变化，资料未写入", nil
			}
			if errors.Is(queryErr, sql.ErrNoRows) {
				return "no_url", "", nil
			}
			return "failed", queryErr.Error(), nil
		}
		source, externalID, remoteURL, err = m.store.ArtistImageProfileSource(ctx, item.ArtistID)
		if errors.Is(err, sql.ErrNoRows) {
			return "no_url", "", nil
		}
		if err != nil {
			return "failed", err.Error(), nil
		}
	}

	if m.testImageBackfillHook != nil {
		m.testImageBackfillHook(item.ArtistID)
	}
	if err = m.cacheArtistImageGuarded(ctx, item.ArtistID, source, externalID, remoteURL, &checkpoint); err != nil {
		if rateErr := asRateLimited(err); rateErr != nil {
			return "", "", rateErr
		}
		if errors.Is(err, storage.ErrArtistImageBackfillState) {
			return "skipped", "状态已变化，写入被安全拒绝", nil
		}
		return "failed", err.Error(), nil
	}
	return "cached", "", nil
}

// queryConfirmedArtistImageURL 仅在库存 URL 为空时调用：按已确认身份向
// 启用的来源（MusicBrainz 按已绑定 MBID 查档，Last.fm 按既有查询上下文）
// 重新取资料以获得图片地址。落库走 RefreshArtistProfileGuarded：单事务内
// 要求 checkpoint 有效且身份仍是读取时的 external_id 才 UPDATE 既有行——
// 网络查询窗口内身份被并发解除不得复活、被并发重新确认不得覆盖，暂停/停
// 止后不得写入；响应身份漂移（external_id 与已确认身份不一致）同样不落库。
// 没有可用来源返回 sql.ErrNoRows；状态冲突返回 ErrArtistImageBackfillState。
func (m *Manager) queryConfirmedArtistImageURL(ctx context.Context, artistID int64, checkpoint *storage.ArtistImageBackfillCheckpoint) error {
	refreshed := false
	mbid, err := m.store.ArtistExternalID(ctx, artistID, "musicbrainz")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && mbid != "" {
		if setting, settingErr := m.store.MetadataSourceSetting(ctx, "musicbrainz"); settingErr == nil && setting.Enabled {
			_, profile, lookupErr := m.musicBrainzLookup(ctx, mbid, setting)
			if lookupErr == nil {
				if profile.ExternalID == mbid {
					if upsertErr := m.store.RefreshArtistProfileGuarded(ctx, artistID, profile, mbid, checkpoint); upsertErr != nil {
						return upsertErr
					}
					refreshed = true
				} else {
					m.logger.Warn("musicbrainz identity drift during image backfill; profile not applied", "artistId", artistID, "confirmed", mbid, "returned", profile.ExternalID)
				}
			} else if asRateLimited(lookupErr) != nil {
				return lookupErr
			} else {
				m.logger.Warn("musicbrainz lookup for image backfill", "artistId", artistID, "error", RedactSourceError(lookupErr))
			}
		}
	}
	lastFMID, err := m.store.ArtistExternalID(ctx, artistID, "lastfm")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && lastFMID != "" {
		if setting, settingErr := m.store.MetadataSourceSetting(ctx, "lastfm"); settingErr == nil && setting.Enabled && setting.APIKey != "" {
			artist, artistErr := m.store.ArtistForMatching(ctx, artistID)
			if artistErr != nil {
				return artistErr
			}
			_, profile, lookupErr := m.lastFMInfo(ctx, artist, setting)
			if lookupErr == nil {
				if profile.ExternalID == lastFMID {
					if upsertErr := m.store.RefreshArtistProfileGuarded(ctx, artistID, profile, lastFMID, checkpoint); upsertErr != nil {
						return upsertErr
					}
					refreshed = true
				} else {
					m.logger.Warn("lastfm identity drift during image backfill; profile not applied", "artistId", artistID, "confirmed", lastFMID, "returned", profile.ExternalID)
				}
			} else if asRateLimited(lookupErr) != nil {
				return lookupErr
			} else if !errors.Is(lookupErr, errArtistNotFound) {
				// P2-4：网络层错误可能携带含 api_key 的请求 URL，先脱敏再记日志。
				m.logger.Warn("lastfm lookup for image backfill", "artistId", artistID, "error", RedactSourceError(lookupErr))
			}
		}
	}
	if !refreshed {
		return sql.ErrNoRows
	}
	return nil
}

// cacheArtistImageGuarded 下载并写入头像缓存：写入在事务内再次验证当前
// 状态与快照的 URL/身份一致且没有并发新增的自定义头像（含合并继承），
// checkpoint 提供纪元围栏防止旧 worker 写入。
func (m *Manager) cacheArtistImageGuarded(ctx context.Context, artistID int64, source, externalID, remoteURL string, checkpoint *storage.ArtistImageBackfillCheckpoint) error {
	data, mimeType, extension, err := m.downloadPublicImage(ctx, remoteURL, "artist image")
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	cachePath, err := writeCacheFile(m.imageDirectory, hash+extension, data)
	if err != nil {
		return err
	}
	return m.store.SaveArtistImageGuarded(ctx, storage.ArtistImageInput{ArtistID: artistID, ByteSize: int64(len(data)), Source: source, RemoteURL: remoteURL, Hash: hash, MIMEType: mimeType, CachePath: cachePath}, source, externalID, remoteURL, checkpoint)
}

// waitArtistImageBackfill 等待限流截止时间：共享冷却较长者顺延，等待切片
// 受 30 分钟窗口预算约束并逐段持久化（重启后可恢复）。
func (m *Manager) waitArtistImageBackfill(ctx context.Context, c storage.ArtistImageBackfillCheckpoint, source string, deadline time.Time) error {
	for {
		if shared := m.sourceBlockedUntil(source); shared.After(deadline) {
			deadline = shared
		}
		now := m.clockNow()
		if !deadline.After(now) {
			return nil
		}
		r, err := m.store.DurableArtistImageBackfillRun(ctx, c.RunID)
		if err != nil {
			m.artistImageBackfillFailure(c.RunID, err)
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
		err = m.store.RecordArtistImageBackfillWait(context.Background(), c, source, deadline, elapsed, false)
		if err != nil {
			if errors.Is(err, storage.ErrArtistImageBackfillBudget) {
				m.scheduleArtistImageBackfillAutoResume(c.RunID)
			} else if !errors.Is(err, storage.ErrArtistImageBackfillState) {
				m.artistImageBackfillFailure(c.RunID, err)
			}
			return err
		}
		if sleepErr != nil {
			if m.baseCtx.Err() != nil {
				m.pauseArtistImageBackfillShutdown(c.RunID)
			}
			return sleepErr
		}
	}
}

// PauseArtistImageBackfill 暂停进行中的补全任务；已完成的项目保留进度。
func (m *Manager) PauseArtistImageBackfill(runID int64) error {
	return m.stopArtistImageBackfill(runID, "pause")
}

// CancelArtistImageBackfill 停止补全任务（终态，不可继续）。
func (m *Manager) CancelArtistImageBackfill(runID int64) error {
	return m.stopArtistImageBackfill(runID, "cancel")
}

func (m *Manager) stopArtistImageBackfill(runID int64, action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.TransitionArtistImageBackfillRun(context.Background(), runID, action, "manual"); err != nil {
		if errors.Is(err, storage.ErrArtistImageBackfillState) {
			return ErrRunNotActive
		}
		return err
	}
	m.cancelAutoResume(artistImageBackfillAutoResumeKind, runID)
	if m.imageBackfillRunID == runID && m.imageBackfillCancel != nil {
		m.imageBackfillCancel()
	}
	return nil
}

// ResumeArtistImageBackfill 继续暂停中的补全任务：先等旧 worker 退出再
// 转移状态，旧 loop 不可能用新纪元的 token 写入。
func (m *Manager) ResumeArtistImageBackfill(ctx context.Context, runID int64) error {
	m.mu.Lock()
	done := m.imageBackfillDone
	oldID := m.imageBackfillRunID
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
	if m.imageBackfillRunning {
		return ErrRunNotActive
	}
	if err := m.store.TransitionArtistImageBackfillRun(ctx, runID, "resume", ""); err != nil {
		return err
	}
	m.cancelAutoResume(artistImageBackfillAutoResumeKind, runID)
	m.launchArtistImageBackfillLocked(runID)
	return nil
}
