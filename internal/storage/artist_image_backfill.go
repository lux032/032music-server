package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// B+2 已匹配艺术家头像补全的持久化任务层：完整镜像 artist_match_runs 的
// durable run 语义（claim token、限流等待预算、自动恢复轮次、启动恢复），
// 与身份匹配 run 表互相独立——补全只读写头像缓存，绝不可能触碰身份。

var ErrArtistImageBackfillState = errors.New("artist image backfill run state conflict")
var ErrArtistImageBackfillBudget = errors.New("artist image backfill rate limit budget exhausted")

// ArtistImageBackfillCheckpoint 是单个补全项的纪元围栏：claim_token 在
// claim/resume 时递增，旧 worker 的写入会因 token 失配被拒绝。
type ArtistImageBackfillCheckpoint struct {
	RunID, ItemID int64
	ClaimToken    int64
}

// ArtistImageBackfillCandidate 是 DB 级候选（D50 口径）：未合并、存在已确认
// 外部身份、本人与合并来源均无自定义头像。磁盘缓存有效性由调用方 os.Stat
// 判定（“缓存行在、文件丢”视作缺失）。
type ArtistImageBackfillCandidate struct {
	ID   int64
	Name string
}

type ArtistImageBackfillItem struct {
	ID, RunID, ArtistID int64
	ClaimToken          int64
	RateLimitCount      int
	ArtistName          string
	Status, Outcome     string
}

type ArtistImageBackfillRun struct {
	ID                                    int64
	Status                                string
	Total, Processed                      int
	Cached, NoURL, Skipped, Failed        int
	Current, ErrorMessage                 string
	PauseReason, WaitSource, WaitingUntil string
	WaitTotalMS, BudgetBaselineMS         int64
	AutoResumeCount                       int
}

// MaxArtistImageBackfillAutoResumeRounds 与身份匹配 run 一致：限流自动恢复
// 每任务最多 3 轮，用尽后停在终态等人工继续。
const MaxArtistImageBackfillAutoResumeRounds = 3

// ArtistImageBackfillAutoResumeEligible 报告暂停中的补全任务是否可由限流
// 自动恢复倒计时继续：必须带有限流截止时间且暂停原因不是人工/存储决定。
func ArtistImageBackfillAutoResumeEligible(r ArtistImageBackfillRun) bool {
	if r.Status != "paused" || r.WaitingUntil == "" || r.AutoResumeCount >= MaxArtistImageBackfillAutoResumeRounds {
		return false
	}
	switch r.PauseReason {
	case "rate_limit_count", "rate_limit_wait_budget", "server_restart":
		return true
	}
	return false
}

func artistImageBackfillWriteLock(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET status=status WHERE 0`)
	return err
}

func requireArtistImageBackfillCheckpoint(ctx context.Context, tx *sql.Tx, c *ArtistImageBackfillCheckpoint, artistID int64) error {
	if c == nil {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_image_backfill_items i JOIN artist_image_backfill_runs r ON r.id=i.run_id WHERE i.id=? AND r.id=? AND i.artist_id=? AND i.status='in_progress' AND r.status='running' AND i.claim_token=?`, c.ItemID, c.RunID, artistID, c.ClaimToken).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrArtistImageBackfillState
	}
	return nil
}

// ArtistImageBackfillCandidates 返回 DB 级候选：未合并、至少一条外部身份
// （已确认身份的物理表现）、本人与合并来源（最小 id 继承口径）均无自定义
// 头像。是否已有有效缓存不在 SQL 内判断——磁盘文件可能丢失，由调用方 stat。
func (s *Store) ArtistImageBackfillCandidates(ctx context.Context) ([]ArtistImageBackfillCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,COALESCE(a.user_display_name,a.display_name) FROM artists a WHERE a.merged_into_artist_id IS NULL AND EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id) ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ArtistImageBackfillCandidate
	for rows.Next() {
		var v ArtistImageBackfillCandidate
		if err = rows.Scan(&v.ID, &v.Name); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// ArtistImageBackfillSubject 是单项的 DB 级实时资格审查（与候选同口径），
// 供 worker 在任务提交前再次验证当前状态；不合格返回 sql.ErrNoRows。
func (s *Store) ArtistImageBackfillSubject(ctx context.Context, artistID int64) (ArtistImageBackfillCandidate, error) {
	var v ArtistImageBackfillCandidate
	err := s.db.QueryRowContext(ctx, `SELECT a.id,COALESCE(a.user_display_name,a.display_name) FROM artists a WHERE a.id=? AND a.merged_into_artist_id IS NULL AND EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id)`, artistID).Scan(&v.ID, &v.Name)
	return v, err
}

// ArtistImageProfileSource 按来源优先级返回第一个带图片地址的已确认身份
// （source、external_id、remote_image_url），供补全在下载前快照身份与 URL，
// 提交写入时再逐项比对。没有可用地址时返回 sql.ErrNoRows。
func (s *Store) ArtistImageProfileSource(ctx context.Context, artistID int64) (source, externalID, remoteURL string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT p.source,p.external_id,p.remote_image_url FROM artist_external_profiles p JOIN metadata_source_settings s ON s.source=p.source WHERE p.artist_id=? AND COALESCE(p.remote_image_url,'')<>'' ORDER BY s.priority LIMIT 1`, artistID).Scan(&source, &externalID, &remoteURL)
	return source, externalID, remoteURL, err
}

func (s *Store) CreateArtistImageBackfillRun(ctx context.Context, items []ArtistImageBackfillCandidate) (int64, error) {
	if len(items) == 0 {
		return 0, ErrArtistImageBackfillState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = artistImageBackfillWriteLock(ctx, tx); err != nil {
		return 0, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_image_backfill_runs WHERE status IN ('running','queued','paused')`).Scan(&active); err != nil {
		return 0, err
	}
	if active > 0 {
		return 0, ErrArtistImageBackfillState
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO artist_image_backfill_runs(status,total_items) VALUES('running',?)`, len(items))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_image_backfill_items(run_id,artist_id,artist_name) VALUES(?,?,?)`, id, item.ID, item.Name); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) ClaimArtistImageBackfillItem(ctx context.Context, runID int64) (ArtistImageBackfillItem, error) {
	var item ArtistImageBackfillItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if err = artistImageBackfillWriteLock(ctx, tx); err != nil {
		return item, err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_image_backfill_runs WHERE id=?`, runID).Scan(&status); err != nil {
		return item, err
	}
	if status != "running" {
		return item, ErrArtistImageBackfillState
	}
	var rateLimitCount int64
	err = tx.QueryRowContext(ctx, `SELECT id,run_id,artist_id,artist_name,status,COALESCE(outcome,''),claim_token,rate_limit_count FROM artist_image_backfill_items WHERE run_id=? AND status='pending' ORDER BY id LIMIT 1`, runID).Scan(&item.ID, &item.RunID, &item.ArtistID, &item.ArtistName, &item.Status, &item.Outcome, &item.ClaimToken, &rateLimitCount)
	if err != nil {
		return item, err
	}
	item.RateLimitCount = int(rateLimitCount)
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_items SET status='in_progress',claim_token=claim_token+1 WHERE id=?`, item.ID); err != nil {
		return item, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET current_artist=? WHERE id=?`, item.ArtistName, runID); err != nil {
		return item, err
	}
	item.Status = "in_progress"
	item.ClaimToken++
	return item, tx.Commit()
}

// CompleteArtistImageBackfillItem 幂等完成一个补全项并刷新 run 计数；
// 已完成的项目直接返回 nil（断点重放安全），checkpoint 失配拒绝旧 worker。
func (s *Store) CompleteArtistImageBackfillItem(ctx context.Context, c ArtistImageBackfillCheckpoint, outcome, errText string) error {
	switch outcome {
	case "cached", "no_url", "skipped", "failed":
	default:
		return ErrArtistImageBackfillState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistImageBackfillWriteLock(ctx, tx); err != nil {
		return err
	}
	var status string
	var artistID int64
	if err = tx.QueryRowContext(ctx, `SELECT status,artist_id FROM artist_image_backfill_items WHERE id=? AND run_id=?`, c.ItemID, c.RunID).Scan(&status, &artistID); err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	if err = requireArtistImageBackfillCheckpoint(ctx, tx, &c, artistID); err != nil {
		return err
	}
	if len(errText) > 500 {
		errText = errText[:500]
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_items SET status='completed',outcome=?,error=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, outcome, errText, c.ItemID); err != nil {
		return err
	}
	for _, field := range []struct{ column, outcome string }{{"cached_items", "cached"}, {"no_url_items", "no_url"}, {"skipped_items", "skipped"}, {"failed_items", "failed"}} {
		if _, err = tx.ExecContext(ctx, "UPDATE artist_image_backfill_runs SET "+field.column+"=(SELECT count(*) FROM artist_image_backfill_items WHERE run_id=? AND outcome=?) WHERE id=?", c.RunID, field.outcome, c.RunID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET processed_items=(SELECT count(*) FROM artist_image_backfill_items WHERE run_id=? AND status='completed'),wait_source='',waiting_until=NULL WHERE id=?`, c.RunID, c.RunID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TransitionArtistImageBackfillRun(ctx context.Context, id int64, action, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistImageBackfillWriteLock(ctx, tx); err != nil {
		return err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_image_backfill_runs WHERE id=?`, id).Scan(&status); err != nil {
		return err
	}
	switch action {
	case "resume":
		if status != "paused" {
			return ErrArtistImageBackfillState
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_image_backfill_runs WHERE status IN ('running','queued') AND id<>?`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrArtistImageBackfillState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_items SET status='pending',rate_limit_count=0,claim_token=claim_token+1 WHERE run_id=? AND status<>'completed'`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET status='running',pause_reason='',budget_baseline_ms=wait_total_ms WHERE id=?`, id)
	case "auto_resume":
		// 自动恢复重新校验持久化状态：先落地的人工暂停/停止优先。
		if status != "paused" {
			return ErrArtistImageBackfillState
		}
		var reason0, waiting string
		var autoCount int
		if err = tx.QueryRowContext(ctx, `SELECT pause_reason,COALESCE(waiting_until,''),auto_resume_count FROM artist_image_backfill_runs WHERE id=?`, id).Scan(&reason0, &waiting, &autoCount); err != nil {
			return err
		}
		eligible := ArtistImageBackfillRun{Status: status, PauseReason: reason0, WaitingUntil: waiting, AutoResumeCount: autoCount}
		if !ArtistImageBackfillAutoResumeEligible(eligible) {
			return ErrArtistImageBackfillState
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_image_backfill_runs WHERE status IN ('running','queued') AND id<>?`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrArtistImageBackfillState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_items SET status='pending',rate_limit_count=0,claim_token=claim_token+1 WHERE run_id=? AND status<>'completed'`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET status='running',pause_reason='',budget_baseline_ms=wait_total_ms,auto_resume_count=auto_resume_count+1 WHERE id=?`, id)
	case "pause", "cancel", "fail":
		if status != "running" && !(action == "cancel" && status == "paused") {
			return ErrArtistImageBackfillState
		}
		next := "paused"
		if action == "fail" {
			next = "failed"
		}
		if action == "cancel" {
			next = "cancelled"
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET status=?,pause_reason=?,finished_at=CASE WHEN ? IN ('cancelled','failed') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE NULL END WHERE id=?`, next, reason, next, id)
	case "complete":
		if status != "running" {
			return ErrArtistImageBackfillState
		}
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_image_backfill_items WHERE run_id=? AND status<>'completed'`, id).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return ErrArtistImageBackfillState
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET status='completed',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),wait_source='',waiting_until=NULL WHERE id=?`, id)
	default:
		return ErrArtistImageBackfillState
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverArtistImageBackfillRuns 启动恢复：中断的运行/排队任务转为暂停，
// 保留限流截止时间与累计等待；自动恢复轮次已用尽的任务标记为终态原因，
// 页面不得再承诺后端永远不会执行的自动继续。
func (s *Store) RecoverArtistImageBackfillRuns(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = artistImageBackfillWriteLock(ctx, tx); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET status='paused',pause_reason=CASE WHEN pause_reason='' AND waiting_until IS NOT NULL AND waiting_until<>'' AND auto_resume_count>=? THEN 'rate_limit_exhausted' WHEN pause_reason='' THEN 'server_restart' ELSE pause_reason END WHERE status IN ('running','queued')`, MaxArtistImageBackfillAutoResumeRounds)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func (s *Store) DurableArtistImageBackfillRun(ctx context.Context, id int64) (ArtistImageBackfillRun, error) {
	var r ArtistImageBackfillRun
	err := s.db.QueryRowContext(ctx, `SELECT id,status,total_items,processed_items,cached_items,no_url_items,skipped_items,failed_items,COALESCE(current_artist,''),COALESCE(error_message,''),pause_reason,wait_source,COALESCE(waiting_until,''),wait_total_ms,budget_baseline_ms,auto_resume_count FROM artist_image_backfill_runs WHERE id=?`, id).Scan(&r.ID, &r.Status, &r.Total, &r.Processed, &r.Cached, &r.NoURL, &r.Skipped, &r.Failed, &r.Current, &r.ErrorMessage, &r.PauseReason, &r.WaitSource, &r.WaitingUntil, &r.WaitTotalMS, &r.BudgetBaselineMS, &r.AutoResumeCount)
	return r, err
}

func (s *Store) UnfinishedArtistImageBackfillRun(ctx context.Context) (ArtistImageBackfillRun, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM artist_image_backfill_runs WHERE status IN ('running','paused','queued') ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return ArtistImageBackfillRun{}, err
	}
	return s.DurableArtistImageBackfillRun(ctx, id)
}

// LatestArtistImageBackfillRun 返回最近一条补全任务（任何状态），供页面在
// 没有活动任务时展示上一轮结果；从没有补全过时返回 sql.ErrNoRows。
func (s *Store) LatestArtistImageBackfillRun(ctx context.Context) (ArtistImageBackfillRun, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM artist_image_backfill_runs ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return ArtistImageBackfillRun{}, err
	}
	return s.DurableArtistImageBackfillRun(ctx, id)
}

// RecordArtistImageBackfillWait 记录一次限流（response=true 计连续次数），
// 共享冷却较长者取胜；连续 3 次或 30 分钟窗口预算耗尽即持久化暂停并返回
// ErrArtistImageBackfillBudget，自动恢复轮次用尽则标记 rate_limit_exhausted。
func (s *Store) RecordArtistImageBackfillWait(ctx context.Context, c ArtistImageBackfillCheckpoint, source string, until time.Time, elapsed time.Duration, response bool) error {
	if elapsed < 0 {
		return ErrArtistImageBackfillState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistImageBackfillWriteLock(ctx, tx); err != nil {
		return err
	}
	var artistID int64
	if err = tx.QueryRowContext(ctx, `SELECT artist_id FROM artist_image_backfill_items WHERE id=? AND run_id=?`, c.ItemID, c.RunID).Scan(&artistID); err != nil {
		return err
	}
	var runStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_image_backfill_runs WHERE id=?`, c.RunID).Scan(&runStatus); err != nil {
		return err
	}
	if err = requireArtistImageBackfillCheckpoint(ctx, tx, &c, artistID); err != nil {
		if response || (runStatus != "paused" && runStatus != "cancelled") {
			return err
		}
		var token int64
		var itemStatus string
		if e := tx.QueryRowContext(ctx, `SELECT claim_token,status FROM artist_image_backfill_items WHERE id=?`, c.ItemID).Scan(&token, &itemStatus); e != nil {
			return e
		}
		if token != c.ClaimToken || itemStatus != "in_progress" {
			return ErrArtistImageBackfillState
		}
	}
	increment := 0
	if response {
		increment = 1
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_items SET rate_limit_count=rate_limit_count+? WHERE id=?`, increment, c.ItemID); err != nil {
		return err
	}
	var current, currentSource string
	var total, baseline int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(waiting_until,''),wait_source,wait_total_ms,budget_baseline_ms FROM artist_image_backfill_runs WHERE id=?`, c.RunID).Scan(&current, &currentSource, &total, &baseline); err != nil {
		return err
	}
	if current != "" {
		old, e := time.Parse(time.RFC3339Nano, current)
		if e != nil {
			return e
		}
		if old.After(until) {
			until = old
			source = currentSource
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET wait_source=?,waiting_until=?,wait_total_ms=wait_total_ms+? WHERE id=?`, source, until.UTC().Format(time.RFC3339Nano), elapsed.Milliseconds(), c.RunID); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT rate_limit_count FROM artist_image_backfill_items WHERE id=?`, c.ItemID).Scan(&count); err != nil {
		return err
	}
	reason := ""
	if count >= 3 {
		reason = "rate_limit_count"
	}
	if total+elapsed.Milliseconds()-baseline >= int64((30*time.Minute)/time.Millisecond) {
		reason = "rate_limit_wait_budget"
	}
	if reason != "" && runStatus == "running" {
		var autoCount int
		if err = tx.QueryRowContext(ctx, `SELECT auto_resume_count FROM artist_image_backfill_runs WHERE id=?`, c.RunID).Scan(&autoCount); err != nil {
			return err
		}
		if autoCount >= MaxArtistImageBackfillAutoResumeRounds {
			// 自动恢复轮次用尽：标记为需人工处理，绝不无限自动重试。
			reason = "rate_limit_exhausted"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_image_backfill_runs SET status='paused',pause_reason=? WHERE id=?`, reason, c.RunID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if reason != "" {
		return ErrArtistImageBackfillBudget
	}
	return nil
}

// ArtistImageBackfillRunsAwaitingAutoResume 列出需要自动恢复定时器的限流
// 暂停任务（含重启后恢复出来的）。
func (s *Store) ArtistImageBackfillRunsAwaitingAutoResume(ctx context.Context) ([]ArtistImageBackfillRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM artist_image_backfill_runs WHERE status='paused' AND waiting_until IS NOT NULL AND waiting_until<>'' AND pause_reason IN ('rate_limit_count','rate_limit_wait_budget','server_restart') AND auto_resume_count<? ORDER BY id`, MaxArtistImageBackfillAutoResumeRounds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	var runs []ArtistImageBackfillRun
	for _, id := range ids {
		run, e := s.DurableArtistImageBackfillRun(ctx, id)
		if e != nil {
			return nil, e
		}
		runs = append(runs, run)
	}
	return runs, nil
}

// ArtistImageBackfillCacheRow 是批量扫描的一行：D50 候选及其有效缓存路径
// （本人行优先，合并继承按 fetched_at 最新，与 ArtistImagePath 的缓存分支
// 同口径；无缓存行为空串）。磁盘文件存在性由调用方 os.Stat 判定——“缓存
// 行在、文件丢”视作缺失。
type ArtistImageBackfillCacheRow struct {
	Candidate ArtistImageBackfillCandidate
	CachePath string
}

// ArtistImageBackfillCacheScan 一次查询返回全部 D50 候选与其有效缓存路径，
// 供统计与启动枚举避免逐艺术家的 N+1 查询。
func (s *Store) ArtistImageBackfillCacheScan(ctx context.Context) ([]ArtistImageBackfillCacheRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,COALESCE(a.user_display_name,a.display_name),COALESCE((SELECT c.cache_path FROM artist_image_cache c WHERE c.artist_id=a.id OR c.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=a.id) ORDER BY (c.artist_id=a.id) DESC,c.fetched_at DESC LIMIT 1),'') FROM artists a WHERE a.merged_into_artist_id IS NULL AND EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=a.id) AND NOT EXISTS(SELECT 1 FROM artist_custom_images ci JOIN artists ma ON ma.id=ci.artist_id WHERE ma.merged_into_artist_id=a.id) ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ArtistImageBackfillCacheRow
	for rows.Next() {
		var v ArtistImageBackfillCacheRow
		if err = rows.Scan(&v.Candidate.ID, &v.Candidate.Name, &v.CachePath); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// RefreshArtistProfileGuarded 在单事务内刷新既有已确认身份的资料（补全空
// URL 时查源后的落库）：checkpoint 有效（run running + item in_progress +
// claim_token 匹配）且身份仍是 expectedExternalID 时才 UPDATE 既有行；
// 绝不 INSERT（身份被并发解除后不得复活），绝不改写 external_id（并发
// 重新确认为其他身份后不得覆盖回来）。身份变化/解除、claim 过期或响应
// 身份漂移一律返回 ErrArtistImageBackfillState，调用方按 skipped 或受控
// 退出处理。
func (s *Store) RefreshArtistProfileGuarded(ctx context.Context, artistID int64, p ExternalArtistProfile, expectedExternalID string, c *ArtistImageBackfillCheckpoint) error {
	if expectedExternalID == "" || p.ExternalID != expectedExternalID {
		return ErrArtistImageBackfillState
	}
	aliases, _ := json.Marshal(p.Aliases)
	tags, _ := json.Marshal(p.Tags)
	if p.FetchedAt == "" {
		p.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if len(p.Raw) == 0 {
		p.Raw = json.RawMessage(`{}`)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_external_profiles SET id=id WHERE 0`); err != nil {
		return err
	}
	if err = requireArtistImageBackfillCheckpoint(ctx, tx, c, artistID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE artist_external_profiles SET display_name=?,sort_name=?,page_url=?,remote_image_url=?,image_checked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),biography=?,country=?,artist_type=?,disambiguation=?,aliases_json=?,tags_json=?,raw_json=?,fetched_at=? WHERE artist_id=? AND source=? AND external_id=?`, p.DisplayName, p.SortName, p.PageURL, p.RemoteImageURL, p.Biography, p.Country, p.ArtistType, p.Disambiguation, string(aliases), string(tags), string(p.Raw), p.FetchedAt, artistID, p.Source, expectedExternalID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		// 行不存在（身份已被并发解除）或 external_id 已变化（并发重新确认
		// 为其他身份）：绝不复活/覆盖。
		return ErrArtistImageBackfillState
	}
	return tx.Commit()
}

// SaveArtistImageGuarded 在写入头像缓存行之前于同一事务内再次验证：
// 艺术家仍未合并、本人与合并来源都没有（并发新增的）自定义头像、已确认
// 身份的来源资料仍是快照时的 external_id 与 remote_image_url；携带
// checkpoint 时还要求该补全项仍由本 worker 持有（纪元围栏）。任何一项不
// 成立都拒绝写入并返回 ErrArtistImageBackfillState——绝不覆盖并发设置的
// 自定义头像，也绝不跟随已变化的身份/URL。
func (s *Store) SaveArtistImageGuarded(ctx context.Context, image ArtistImageInput, expectSource, expectExternalID, expectURL string, c *ArtistImageBackfillCheckpoint) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_image_cache SET artist_id=artist_id WHERE 0`); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=? AND merged_into_artist_id IS NULL)`, image.ArtistID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrArtistImageBackfillState
	}
	var custom bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_custom_images ci WHERE ci.artist_id=? OR ci.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=?))`, image.ArtistID, image.ArtistID).Scan(&custom); err != nil {
		return err
	}
	if custom {
		return ErrArtistImageBackfillState
	}
	var currentID, currentURL string
	err = tx.QueryRowContext(ctx, `SELECT external_id,COALESCE(remote_image_url,'') FROM artist_external_profiles WHERE artist_id=? AND source=?`, image.ArtistID, expectSource).Scan(&currentID, &currentURL)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrArtistImageBackfillState
	}
	if err != nil {
		return err
	}
	if currentID != expectExternalID || currentURL != expectURL {
		return ErrArtistImageBackfillState
	}
	if err = requireArtistImageBackfillCheckpoint(ctx, tx, c, image.ArtistID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO artist_image_cache(artist_id,source,remote_url,content_hash,mime_type,cache_path,byte_size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(artist_id) DO UPDATE SET source=excluded.source,remote_url=excluded.remote_url,content_hash=excluded.content_hash,mime_type=excluded.mime_type,cache_path=excluded.cache_path,byte_size=excluded.byte_size,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, image.ArtistID, image.Source, image.RemoteURL, image.Hash, image.MIMEType, image.CachePath, image.ByteSize); err != nil {
		return err
	}
	return tx.Commit()
}
