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

// CompleteReleaseGroupRebuild is called only in the existing combined scan
// callback. A partial full scan cannot certify all old, possibly polluted tags.
func (s *Store) CompleteReleaseGroupRebuild(ctx context.Context) error {
	state, err := s.ReleaseGroupRebuildState(ctx)
	if err != nil || state.Status != "rebuilding" {
		return err
	}
	var status string
	var failed int64
	if err = s.db.QueryRowContext(ctx, `SELECT status,failed_files FROM scan_jobs WHERE id=?`, state.ScanJobID).Scan(&status, &failed); err != nil {
		return err
	}
	if status != "completed" {
		return nil
	}
	if failed > 0 {
		return s.failReleaseGroupRebuild(ctx, "全量扫描有文件读取失败，请修复后重新重建")
	}
	if err = s.RefreshReleaseGroupIdentities(ctx); err != nil {
		_ = s.failReleaseGroupRebuild(ctx, err.Error())
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE release_group_rebuild SET status='ready',error_message='',updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND status='rebuilding' AND scan_job_id=?`, state.ScanJobID)
	return err
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
	if err != nil || state.Status != "rebuilding" {
		return err
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
