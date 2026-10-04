package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// 歌手简介补全的持久化任务层：完整镜像 040 头像补全的 durable run 语义
// （claim token、限流等待预算、自动恢复轮次、启动恢复），与身份匹配 run、
// 头像补全 run 互为独立任务。补全只消费已确认的 MusicBrainz 身份（含合并
// 来源继承），绝不写身份，绝不触碰 artists.user_biography。

var ErrArtistBiographyBackfillState = errors.New("artist biography backfill run state conflict")
var ErrArtistBiographyBackfillBudget = errors.New("artist biography backfill rate limit budget exhausted")

// ArtistBiographyBackfillCheckpoint 是单个补全项的纪元围栏：claim_token 在
// claim/resume 时递增，旧 worker 的写入会因 token 失配被拒绝。
type ArtistBiographyBackfillCheckpoint struct {
	RunID, ItemID int64
	ClaimToken    int64
}

// ArtistBiographyBackfillCandidate 是 DB 级候选（H2/M4 口径）：未合并、
// 具有实际演唱/专辑关系（歌手；歌手兼幕后算歌手，纯幕后不纳入）、本行无
// 人工简介、本人与合并来源均无 status='found' 的简介版本行。是否有已确认
// MBID 不在 SQL 内判断——无 MBID（含合并继承）者逐项计 skipped_no_mbid。
type ArtistBiographyBackfillCandidate struct {
	ID   int64
	Name string
}

type ArtistBiographyBackfillItem struct {
	ID, RunID, ArtistID int64
	ClaimToken          int64
	RateLimitCount      int
	ArtistName          string
	Status, Outcome     string
}

type ArtistBiographyBackfillRun struct {
	ID                                    int64
	Status                                string
	Total, Processed                      int
	Filled, Missing, Skipped, Failed      int
	Current, ErrorMessage                 string
	PauseReason, WaitSource, WaitingUntil string
	WaitTotalMS, BudgetBaselineMS         int64
	AutoResumeCount                       int
}

// MaxArtistBiographyBackfillAutoResumeRounds 与身份匹配/头像补全 run 一致：
// 限流自动恢复每任务最多 3 轮，用尽后停在终态等人工继续。
const MaxArtistBiographyBackfillAutoResumeRounds = 3

// 歌手（performer）判定谓词与 ListArtistOptions(role="performer") 同口径：
// 专辑歌手或曲目 primary 演唱关系；artists.artist_type 是外部元数据字段，
// 绝不用作歌手判定。
const artistBiographyPerformerPredicate = `(EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=a.id) OR a.id IN (SELECT artist_id FROM track_artists WHERE role='primary'))`

// 候选缺口谓词（M4）：本行无人工简介，且本人与合并来源（同
// ArtistBiographies 的一跳继承口径）都没有可用的 found 版本行。
const artistBiographyGapPredicate = `COALESCE(a.user_biography,'')='' AND NOT EXISTS(SELECT 1 FROM artist_biographies b WHERE (b.artist_id=a.id OR b.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=a.id)) AND b.status='found' AND b.biography<>'')`

// ArtistBiographyBackfillAutoResumeEligible 报告暂停中的补全任务是否可由
// 限流自动恢复倒计时继续：必须带有限流截止时间且暂停原因不是人工/存储决定。
func ArtistBiographyBackfillAutoResumeEligible(r ArtistBiographyBackfillRun) bool {
	if r.Status != "paused" || r.WaitingUntil == "" || r.AutoResumeCount >= MaxArtistBiographyBackfillAutoResumeRounds {
		return false
	}
	switch r.PauseReason {
	case "rate_limit_count", "rate_limit_wait_budget", "server_restart":
		return true
	}
	return false
}

func artistBiographyBackfillWriteLock(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET status=status WHERE 0`)
	return err
}

func requireArtistBiographyBackfillCheckpoint(ctx context.Context, tx *sql.Tx, c *ArtistBiographyBackfillCheckpoint, artistID int64) error {
	if c == nil {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_biography_backfill_items i JOIN artist_biography_backfill_runs r ON r.id=i.run_id WHERE i.id=? AND r.id=? AND i.artist_id=? AND i.status='in_progress' AND r.status='running' AND i.claim_token=?`, c.ItemID, c.RunID, artistID, c.ClaimToken).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrArtistBiographyBackfillState
	}
	return nil
}

// ArtistBiographyBackfillCandidates 返回 DB 级候选：未合并的歌手（H2
// performer 口径）、本行无人工简介、本人与合并来源均无 found 版本行。
func (s *Store) ArtistBiographyBackfillCandidates(ctx context.Context) ([]ArtistBiographyBackfillCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,COALESCE(a.user_display_name,a.display_name) FROM artists a WHERE a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+` AND `+artistBiographyGapPredicate+` ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []ArtistBiographyBackfillCandidate
	for rows.Next() {
		var v ArtistBiographyBackfillCandidate
		if err = rows.Scan(&v.ID, &v.Name); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// ArtistIsPerformer 报告艺术家当前是否是未合并歌手（H2 performer 口径），
// 供自动匹配后的 best-effort 简介补全筛选：纯幕后人员绝不触发简介请求。
func (s *Store) ArtistIsPerformer(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists a WHERE a.id=? AND a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+`)`, id).Scan(&ok)
	return ok, err
}

// ArtistBiographyBackfillSubject 是单项的 DB 级实时资格审查（与候选同口径），
// 供 worker 在任务提交前再次验证当前状态；不合格返回 sql.ErrNoRows。
func (s *Store) ArtistBiographyBackfillSubject(ctx context.Context, artistID int64) (ArtistBiographyBackfillCandidate, error) {
	var v ArtistBiographyBackfillCandidate
	err := s.db.QueryRowContext(ctx, `SELECT a.id,COALESCE(a.user_display_name,a.display_name) FROM artists a WHERE a.id=? AND a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+` AND `+artistBiographyGapPredicate, artistID).Scan(&v.ID, &v.Name)
	return v, err
}

// ArtistBiographyBackfillRetrySubject 是限流重试/断点重放的放宽复查（P2-1）：
// 只要求仍是未合并歌手且本行无人工简介——不要求“无 found 行”。补全项在
// 部分语言已落 found 后遭遇 429 时，重试必须能继续补剩余语言并以 filled
// 结算，而不是被误判为 skipped_state。不合格返回 sql.ErrNoRows。
func (s *Store) ArtistBiographyBackfillRetrySubject(ctx context.Context, artistID int64) (ArtistBiographyBackfillCandidate, error) {
	var v ArtistBiographyBackfillCandidate
	err := s.db.QueryRowContext(ctx, `SELECT a.id,COALESCE(a.user_display_name,a.display_name) FROM artists a WHERE a.id=? AND a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+` AND COALESCE(a.user_biography,'')=''`, artistID).Scan(&v.ID, &v.Name)
	return v, err
}

// ArtistBiographyPerformerCount 统计未合并歌手总数（H2 口径，供页面统计行）。
func (s *Store) ArtistBiographyPerformerCount(ctx context.Context) (int, error) {
	var total int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artists a WHERE a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate).Scan(&total)
	return total, err
}

// ArtistBiographyIdentity 是简介抓取的身份快照（分叉 C1）：沿合并链（含
// 多跳）找到的第一个已确认 MusicBrainz 身份。Owner 是身份资料所在的
// artists 行（本行优先，其次按合并来源 id 最小继承）；简介一律写规范
// （未合并）歌手行。链上没有已确认 MBID 时返回 sql.ErrNoRows。
type ArtistBiographyIdentity struct {
	OwnerID int64
	MBID    string
}

func (s *Store) ArtistBiographyIdentity(ctx context.Context, artistID int64) (ArtistBiographyIdentity, error) {
	var v ArtistBiographyIdentity
	err := s.db.QueryRowContext(ctx, `WITH RECURSIVE src(id) AS (SELECT a.id FROM artists a WHERE a.merged_into_artist_id=? UNION SELECT a.id FROM artists a JOIN src s ON a.merged_into_artist_id=s.id) SELECT p.artist_id,p.external_id FROM artist_external_profiles p WHERE p.source='musicbrainz' AND (p.artist_id=? OR p.artist_id IN (SELECT id FROM src)) ORDER BY (p.artist_id=?) DESC,p.artist_id LIMIT 1`, artistID, artistID, artistID).Scan(&v.OwnerID, &v.MBID)
	return v, err
}

// ArtistBiographyFoundExists 报告规范歌手（含合并来源继承）是否已有可用
// 的 found 简介版本行，供补全项区分 filled/missing 终态。
func (s *Store) ArtistBiographyFoundExists(ctx context.Context, artistID int64) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_biographies b WHERE (b.artist_id=? OR b.artist_id IN (SELECT ma.id FROM artists ma WHERE ma.merged_into_artist_id=?)) AND b.status='found' AND b.biography<>'')`, artistID, artistID).Scan(&found)
	return found, err
}

// UpsertArtistBiographyGuarded 在单事务内写入一条简介版本行（H1 围栏）：
// ① 携带 checkpoint 时要求 run running + item in_progress + claim_token
// 匹配（暂停/停止/恢复抢先后旧 worker 不得写入）；② 规范歌手仍未合并；
// ③ 身份属主行上的 (musicbrainz, expectedMBID) 资料仍存在且该属主仍沿
// 合并链归属于规范歌手（网络窗口内身份被解除/改认/合并关系变化时，迟到
// 的简介绝不落库）。任一条件不成立返回
// ErrArtistBiographyBackfillState，调用方按 skipped_state 处理。
func (s *Store) UpsertArtistBiographyGuarded(ctx context.Context, artistID, ownerID int64, expectedMBID string, value ArtistBiography, c *ArtistBiographyBackfillCheckpoint) error {
	if expectedMBID == "" || ownerID == 0 {
		return ErrArtistBiographyBackfillState
	}
	value.Source = strings.ToLower(strings.TrimSpace(value.Source))
	value.Language = normalizeLanguage(value.Language)
	value.Biography = strings.TrimSpace(value.Biography)
	value.PageURL = strings.TrimSpace(value.PageURL)
	if value.Status == "" {
		value.Status = "found"
	}
	if value.Biography == "" {
		value.Status = "missing"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE artist_biographies SET id=id WHERE 0`); err != nil {
		return err
	}
	if err = requireArtistBiographyBackfillCheckpoint(ctx, tx, c, artistID); err != nil {
		return err
	}
	var canonical bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=? AND merged_into_artist_id IS NULL)`, artistID).Scan(&canonical); err != nil {
		return err
	}
	if !canonical {
		return ErrArtistBiographyBackfillState
	}
	// P2-3：事务内以与 ArtistBiographyIdentity 相同的 CTE 重解当前生效身份，
	// 要求仍等于快照 (ownerID, expectedMBID)。网络窗口内规范歌手本行被确认
	// 了（优先级更高的）本行身份、属主身份被解除/改认、或合并链脱链时，
	// 迟到的旧身份简介一律拒绝，绝不覆盖新身份确认流程写入的内容。
	var currentOwner int64
	var currentMBID string
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE src(id) AS (SELECT a.id FROM artists a WHERE a.merged_into_artist_id=? UNION SELECT a.id FROM artists a JOIN src s ON a.merged_into_artist_id=s.id) SELECT p.artist_id,p.external_id FROM artist_external_profiles p WHERE p.source='musicbrainz' AND (p.artist_id=? OR p.artist_id IN (SELECT id FROM src)) ORDER BY (p.artist_id=?) DESC,p.artist_id LIMIT 1`, artistID, artistID, artistID).Scan(&currentOwner, &currentMBID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrArtistBiographyBackfillState
	}
	if err != nil {
		return err
	}
	if currentOwner != ownerID || currentMBID != expectedMBID {
		return ErrArtistBiographyBackfillState
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO artist_biographies(artist_id,source,language,biography,page_url,status) VALUES(?,?,?,?,?,?) ON CONFLICT(artist_id,source,language) DO UPDATE SET biography=excluded.biography,page_url=excluded.page_url,status=excluded.status,fetched_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, artistID, value.Source, value.Language, value.Biography, value.PageURL, value.Status); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateArtistBiographyBackfillRun(ctx context.Context, items []ArtistBiographyBackfillCandidate) (int64, error) {
	if len(items) == 0 {
		return 0, ErrArtistBiographyBackfillState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = artistBiographyBackfillWriteLock(ctx, tx); err != nil {
		return 0, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_biography_backfill_runs WHERE status IN ('running','queued','paused')`).Scan(&active); err != nil {
		return 0, err
	}
	if active > 0 {
		return 0, ErrArtistBiographyBackfillState
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO artist_biography_backfill_runs(status,total_items) VALUES('running',?)`, len(items))
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, item := range items {
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_biography_backfill_items(run_id,artist_id,artist_name) VALUES(?,?,?)`, id, item.ID, item.Name); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) ClaimArtistBiographyBackfillItem(ctx context.Context, runID int64) (ArtistBiographyBackfillItem, error) {
	var item ArtistBiographyBackfillItem
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if err = artistBiographyBackfillWriteLock(ctx, tx); err != nil {
		return item, err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_biography_backfill_runs WHERE id=?`, runID).Scan(&status); err != nil {
		return item, err
	}
	if status != "running" {
		return item, ErrArtistBiographyBackfillState
	}
	var rateLimitCount int64
	err = tx.QueryRowContext(ctx, `SELECT id,run_id,artist_id,artist_name,status,COALESCE(outcome,''),claim_token,rate_limit_count FROM artist_biography_backfill_items WHERE run_id=? AND status='pending' ORDER BY id LIMIT 1`, runID).Scan(&item.ID, &item.RunID, &item.ArtistID, &item.ArtistName, &item.Status, &item.Outcome, &item.ClaimToken, &rateLimitCount)
	if err != nil {
		return item, err
	}
	item.RateLimitCount = int(rateLimitCount)
	if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_items SET status='in_progress',claim_token=claim_token+1 WHERE id=?`, item.ID); err != nil {
		return item, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET current_artist=? WHERE id=?`, item.ArtistName, runID); err != nil {
		return item, err
	}
	item.Status = "in_progress"
	item.ClaimToken++
	return item, tx.Commit()
}

// CompleteArtistBiographyBackfillItem 幂等完成一个补全项并刷新 run 计数；
// 已完成的项目直接返回 nil（断点重放安全），checkpoint 失配拒绝旧 worker。
func (s *Store) CompleteArtistBiographyBackfillItem(ctx context.Context, c ArtistBiographyBackfillCheckpoint, outcome, errText string) error {
	switch outcome {
	case "filled", "missing", "skipped_no_mbid", "skipped_state", "skipped_fresh", "skipped_source_disabled", "failed":
	default:
		return ErrArtistBiographyBackfillState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistBiographyBackfillWriteLock(ctx, tx); err != nil {
		return err
	}
	var status string
	var artistID int64
	if err = tx.QueryRowContext(ctx, `SELECT status,artist_id FROM artist_biography_backfill_items WHERE id=? AND run_id=?`, c.ItemID, c.RunID).Scan(&status, &artistID); err != nil {
		return err
	}
	if status == "completed" {
		return nil
	}
	if err = requireArtistBiographyBackfillCheckpoint(ctx, tx, &c, artistID); err != nil {
		return err
	}
	if len(errText) > 500 {
		errText = errText[:500]
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_items SET status='completed',outcome=?,error=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, outcome, errText, c.ItemID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET filled_items=(SELECT count(*) FROM artist_biography_backfill_items WHERE run_id=? AND outcome='filled'),missing_items=(SELECT count(*) FROM artist_biography_backfill_items WHERE run_id=? AND outcome='missing'),skipped_items=(SELECT count(*) FROM artist_biography_backfill_items WHERE run_id=? AND outcome LIKE 'skipped%'),failed_items=(SELECT count(*) FROM artist_biography_backfill_items WHERE run_id=? AND outcome='failed') WHERE id=?`, c.RunID, c.RunID, c.RunID, c.RunID, c.RunID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET processed_items=(SELECT count(*) FROM artist_biography_backfill_items WHERE run_id=? AND status='completed'),wait_source='',waiting_until=NULL WHERE id=?`, c.RunID, c.RunID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TransitionArtistBiographyBackfillRun(ctx context.Context, id int64, action, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistBiographyBackfillWriteLock(ctx, tx); err != nil {
		return err
	}
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_biography_backfill_runs WHERE id=?`, id).Scan(&status); err != nil {
		return err
	}
	switch action {
	case "resume":
		if status != "paused" {
			return ErrArtistBiographyBackfillState
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_biography_backfill_runs WHERE status IN ('running','queued') AND id<>?`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrArtistBiographyBackfillState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_items SET status='pending',rate_limit_count=0,claim_token=claim_token+1 WHERE run_id=? AND status<>'completed'`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET status='running',pause_reason='',budget_baseline_ms=wait_total_ms WHERE id=?`, id)
	case "auto_resume":
		// 自动恢复重新校验持久化状态：先落地的人工暂停/停止优先。
		if status != "paused" {
			return ErrArtistBiographyBackfillState
		}
		var reason0, waiting string
		var autoCount int
		if err = tx.QueryRowContext(ctx, `SELECT pause_reason,COALESCE(waiting_until,''),auto_resume_count FROM artist_biography_backfill_runs WHERE id=?`, id).Scan(&reason0, &waiting, &autoCount); err != nil {
			return err
		}
		eligible := ArtistBiographyBackfillRun{Status: status, PauseReason: reason0, WaitingUntil: waiting, AutoResumeCount: autoCount}
		if !ArtistBiographyBackfillAutoResumeEligible(eligible) {
			return ErrArtistBiographyBackfillState
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_biography_backfill_runs WHERE status IN ('running','queued') AND id<>?`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return ErrArtistBiographyBackfillState
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_items SET status='pending',rate_limit_count=0,claim_token=claim_token+1 WHERE run_id=? AND status<>'completed'`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET status='running',pause_reason='',budget_baseline_ms=wait_total_ms,auto_resume_count=auto_resume_count+1 WHERE id=?`, id)
	case "pause", "cancel", "fail":
		if status != "running" && !(action == "cancel" && status == "paused") {
			return ErrArtistBiographyBackfillState
		}
		next := "paused"
		if action == "fail" {
			next = "failed"
		}
		if action == "cancel" {
			next = "cancelled"
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET status=?,pause_reason=?,finished_at=CASE WHEN ? IN ('cancelled','failed') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE NULL END WHERE id=?`, next, reason, next, id)
	case "complete":
		if status != "running" {
			return ErrArtistBiographyBackfillState
		}
		var pending int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM artist_biography_backfill_items WHERE run_id=? AND status<>'completed'`, id).Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return ErrArtistBiographyBackfillState
		}
		_, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET status='completed',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),wait_source='',waiting_until=NULL WHERE id=?`, id)
	default:
		return ErrArtistBiographyBackfillState
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// RecoverArtistBiographyBackfillRuns 启动恢复：中断的运行/排队任务转为
// 暂停，保留限流截止时间与累计等待；自动恢复轮次已用尽的任务标记为终态
// 原因，页面不得再承诺后端永远不会执行的自动继续。
func (s *Store) RecoverArtistBiographyBackfillRuns(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err = artistBiographyBackfillWriteLock(ctx, tx); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET status='paused',pause_reason=CASE WHEN pause_reason='' AND waiting_until IS NOT NULL AND waiting_until<>'' AND auto_resume_count>=? THEN 'rate_limit_exhausted' WHEN pause_reason='' THEN 'server_restart' ELSE pause_reason END WHERE status IN ('running','queued')`, MaxArtistBiographyBackfillAutoResumeRounds)
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

func (s *Store) DurableArtistBiographyBackfillRun(ctx context.Context, id int64) (ArtistBiographyBackfillRun, error) {
	var r ArtistBiographyBackfillRun
	err := s.db.QueryRowContext(ctx, `SELECT id,status,total_items,processed_items,filled_items,missing_items,skipped_items,failed_items,COALESCE(current_artist,''),COALESCE(error_message,''),pause_reason,wait_source,COALESCE(waiting_until,''),wait_total_ms,budget_baseline_ms,auto_resume_count FROM artist_biography_backfill_runs WHERE id=?`, id).Scan(&r.ID, &r.Status, &r.Total, &r.Processed, &r.Filled, &r.Missing, &r.Skipped, &r.Failed, &r.Current, &r.ErrorMessage, &r.PauseReason, &r.WaitSource, &r.WaitingUntil, &r.WaitTotalMS, &r.BudgetBaselineMS, &r.AutoResumeCount)
	return r, err
}

func (s *Store) UnfinishedArtistBiographyBackfillRun(ctx context.Context) (ArtistBiographyBackfillRun, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM artist_biography_backfill_runs WHERE status IN ('running','paused','queued') ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return ArtistBiographyBackfillRun{}, err
	}
	return s.DurableArtistBiographyBackfillRun(ctx, id)
}

// LatestArtistBiographyBackfillRun 返回最近一条补全任务（任何状态），供
// 页面在没有活动任务时展示上一轮结果；从没有补全过时返回 sql.ErrNoRows。
func (s *Store) LatestArtistBiographyBackfillRun(ctx context.Context) (ArtistBiographyBackfillRun, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM artist_biography_backfill_runs ORDER BY id DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return ArtistBiographyBackfillRun{}, err
	}
	return s.DurableArtistBiographyBackfillRun(ctx, id)
}

// RecordArtistBiographyBackfillWait 记录一次限流（response=true 计连续
// 次数），共享冷却较长者取胜；连续 3 次或 30 分钟窗口预算耗尽即持久化
// 暂停并返回 ErrArtistBiographyBackfillBudget，自动恢复轮次用尽则标记
// rate_limit_exhausted。
func (s *Store) RecordArtistBiographyBackfillWait(ctx context.Context, c ArtistBiographyBackfillCheckpoint, source string, until time.Time, elapsed time.Duration, response bool) error {
	if elapsed < 0 {
		return ErrArtistBiographyBackfillState
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = artistBiographyBackfillWriteLock(ctx, tx); err != nil {
		return err
	}
	var artistID int64
	if err = tx.QueryRowContext(ctx, `SELECT artist_id FROM artist_biography_backfill_items WHERE id=? AND run_id=?`, c.ItemID, c.RunID).Scan(&artistID); err != nil {
		return err
	}
	var runStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM artist_biography_backfill_runs WHERE id=?`, c.RunID).Scan(&runStatus); err != nil {
		return err
	}
	if err = requireArtistBiographyBackfillCheckpoint(ctx, tx, &c, artistID); err != nil {
		if response || (runStatus != "paused" && runStatus != "cancelled") {
			return err
		}
		var token int64
		var itemStatus string
		if e := tx.QueryRowContext(ctx, `SELECT claim_token,status FROM artist_biography_backfill_items WHERE id=?`, c.ItemID).Scan(&token, &itemStatus); e != nil {
			return e
		}
		if token != c.ClaimToken || itemStatus != "in_progress" {
			return ErrArtistBiographyBackfillState
		}
	}
	increment := 0
	if response {
		increment = 1
	}
	if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_items SET rate_limit_count=rate_limit_count+? WHERE id=?`, increment, c.ItemID); err != nil {
		return err
	}
	var current, currentSource string
	var total, baseline int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(waiting_until,''),wait_source,wait_total_ms,budget_baseline_ms FROM artist_biography_backfill_runs WHERE id=?`, c.RunID).Scan(&current, &currentSource, &total, &baseline); err != nil {
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
	if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET wait_source=?,waiting_until=?,wait_total_ms=wait_total_ms+? WHERE id=?`, source, until.UTC().Format(time.RFC3339Nano), elapsed.Milliseconds(), c.RunID); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT rate_limit_count FROM artist_biography_backfill_items WHERE id=?`, c.ItemID).Scan(&count); err != nil {
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
		if err = tx.QueryRowContext(ctx, `SELECT auto_resume_count FROM artist_biography_backfill_runs WHERE id=?`, c.RunID).Scan(&autoCount); err != nil {
			return err
		}
		if autoCount >= MaxArtistBiographyBackfillAutoResumeRounds {
			// 自动恢复轮次用尽：标记为需人工处理，绝不无限自动重试。
			reason = "rate_limit_exhausted"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE artist_biography_backfill_runs SET status='paused',pause_reason=? WHERE id=?`, reason, c.RunID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if reason != "" {
		return ErrArtistBiographyBackfillBudget
	}
	return nil
}

// ArtistBiographyBackfillRunsAwaitingAutoResume 列出需要自动恢复定时器的
// 限流暂停任务（含重启后恢复出来的）。
func (s *Store) ArtistBiographyBackfillRunsAwaitingAutoResume(ctx context.Context) ([]ArtistBiographyBackfillRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM artist_biography_backfill_runs WHERE status='paused' AND waiting_until IS NOT NULL AND waiting_until<>'' AND pause_reason IN ('rate_limit_count','rate_limit_wait_budget','server_restart') AND auto_resume_count<? ORDER BY id`, MaxArtistBiographyBackfillAutoResumeRounds)
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
	var runs []ArtistBiographyBackfillRun
	for _, id := range ids {
		run, e := s.DurableArtistBiographyBackfillRun(ctx, id)
		if e != nil {
			return nil, e
		}
		runs = append(runs, run)
	}
	return runs, nil
}
