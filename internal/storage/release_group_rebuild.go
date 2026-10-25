package storage

import (
	"context"
	"fmt"
)

type ReleaseGroupRebuild struct {
	Status                string
	ScanJobID             int64
	Error                 string
	Processed, Discovered int64
}

func (s *Store) ReleaseGroupRebuildState(ctx context.Context) (ReleaseGroupRebuild, error) {
	var v ReleaseGroupRebuild
	err := s.db.QueryRowContext(ctx, `SELECT r.status,COALESCE(r.scan_job_id,0),r.error_message,COALESCE(j.processed_files,0),COALESCE(j.discovered_files,0) FROM release_group_rebuild r LEFT JOIN scan_jobs j ON j.id=r.scan_job_id WHERE r.id=1`).Scan(&v.Status, &v.ScanJobID, &v.Error, &v.Processed, &v.Discovered)
	return v, err
}
func (s *Store) BeginReleaseGroupRebuild(ctx context.Context, jobID int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE release_group_rebuild SET status='rebuilding',scan_job_id=?,error_message='',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND status<>'rebuilding'`, jobID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return fmt.Errorf("identity rebuild already running")
	}
	return err
}

// legacyReadFailureMessage is the error older builds stored when a rebuild
// scan had unreadable files; boot recovery finishes such rebuilds in place.
const legacyReadFailureMessage = "全量扫描有文件读取失败，请修复后重新重建"

// quarantinedAudioFilesSQL selects audio files that failed in the rebuild scan
// (?1) and have not been re-imported since. Their stored RG evidence predates
// the rebuild and may come from polluted tags, so it must not be trusted.
const quarantinedAudioFilesSQL = `SELECT af.id FROM audio_files af JOIN scan_jobs j ON j.id=?1 AND af.library_id=j.library_id JOIN scan_errors e ON e.scan_job_id=j.id AND e.relative_path=af.relative_path WHERE af.updated_at<=e.created_at`

// CompleteReleaseGroupRebuild is called only in the existing combined scan
// callback. Files that could not be read are quarantined (their old evidence
// is dropped) instead of blocking the whole library from becoming ready.
func (s *Store) CompleteReleaseGroupRebuild(ctx context.Context) error {
	state, err := s.ReleaseGroupRebuildState(ctx)
	if err != nil || state.Status != "rebuilding" {
		return err
	}
	var status string
	if err = s.db.QueryRowContext(ctx, `SELECT status FROM scan_jobs WHERE id=?`, state.ScanJobID).Scan(&status); err != nil {
		return err
	}
	if status != "completed" {
		return nil
	}
	if err = s.finishReleaseGroupRebuild(ctx, state.ScanJobID, "rebuilding"); err != nil {
		_ = s.failReleaseGroupRebuild(ctx, err.Error())
		return err
	}
	return nil
}

// finishReleaseGroupRebuild quarantines failed files, recomputes album
// identities and marks the rebuild ready in one transaction.
func (s *Store) finishReleaseGroupRebuild(ctx context.Context, jobID int64, from string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE release_group_rebuild SET status=status WHERE id=1 AND status=? AND scan_job_id=?`, from, jobID)
	if err != nil {
		return err
	}
	if n, e := res.RowsAffected(); e != nil || n == 0 {
		return e
	}
	var failed int64
	if err = tx.QueryRowContext(ctx, `SELECT failed_files FROM scan_jobs WHERE id=?`, jobID).Scan(&failed); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM audio_file_release_groups WHERE audio_file_id IN (`+quarantinedAudioFilesSQL+`)`, jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM audio_file_releases WHERE audio_file_id IN (`+quarantinedAudioFilesSQL+`)`, jobID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE album_release_groups SET status='observed' WHERE source='file'`); err != nil {
		return err
	}
	if err = refreshAllAlbumReleaseGroups(ctx, tx); err != nil {
		return err
	}
	message := ""
	if failed > 0 {
		message = fmt.Sprintf("%d 个文件读取失败，未纳入身份判定；修复文件后执行增量扫描即可纳入。", failed)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE release_group_rebuild SET status='ready',error_message=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1`, message); err != nil {
		return err
	}
	return tx.Commit()
}

// ScanFailure is one file that could not be read during a scan.
type ScanFailure struct {
	Path, Code, Message string
}

// ReleaseGroupRebuildFailures lists files that failed in the latest rebuild scan.
func (s *Store) ReleaseGroupRebuildFailures(ctx context.Context, limit int) ([]ScanFailure, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.relative_path,e.error_code,e.message FROM scan_errors e JOIN release_group_rebuild r ON r.id=1 AND e.scan_job_id=r.scan_job_id ORDER BY e.id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScanFailure
	for rows.Next() {
		var v ScanFailure
		if err = rows.Scan(&v.Path, &v.Code, &v.Message); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) failReleaseGroupRebuild(ctx context.Context, message string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE release_group_rebuild SET status='error',error_message=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND status='rebuilding'`, message)
	return err
}

// RecoverReleaseGroupRebuild runs before any new scans at boot. Interrupted
// queued/running jobs are errors, never proof of readiness; completed jobs can
// safely finish their idempotent aggregation after a callback interruption.
func (s *Store) RecoverReleaseGroupRebuild(ctx context.Context) error {
	state, err := s.ReleaseGroupRebuildState(ctx)
	if err != nil {
		return err
	}
	if state.Status == "error" && state.Error == legacyReadFailureMessage && state.ScanJobID > 0 {
		var status string
		if e := s.db.QueryRowContext(ctx, `SELECT status FROM scan_jobs WHERE id=?`, state.ScanJobID).Scan(&status); e == nil && status == "completed" {
			return s.finishReleaseGroupRebuild(ctx, state.ScanJobID, "error")
		}
		return nil
	}
	if state.Status != "rebuilding" {
		return nil
	}
	var status string
	if err = s.db.QueryRowContext(ctx, `SELECT status FROM scan_jobs WHERE id=?`, state.ScanJobID).Scan(&status); err != nil {
		return s.failReleaseGroupRebuild(ctx, "扫描任务不存在")
	}
	if status == "completed" {
		return s.CompleteReleaseGroupRebuild(ctx)
	}
	return s.failReleaseGroupRebuild(ctx, "身份重建扫描失败或因服务重启中断，请重新启动")
}
func (s *Store) CheckReleaseGroupRebuildFailure(ctx context.Context, jobID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE release_group_rebuild SET status='error',error_message=COALESCE((SELECT error_message FROM scan_jobs WHERE id=?),'扫描失败'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status='rebuilding' AND scan_job_id=? AND EXISTS(SELECT 1 FROM scan_jobs WHERE id=? AND status NOT IN ('queued','running','completed'))`, jobID, jobID, jobID)
	return err
}
