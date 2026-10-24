package storage

import (
	"context"
	"database/sql"
	"errors"
)

// DiagnoseAcquisition is read-only and heuristic, never evidence of ownership.
func (s *Store) DiagnoseAcquisition(ctx context.Context, t AcquisitionTask) (string, error) {
	if e := s.requireIdentityReady(ctx); e != nil {
		return "", e
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	stored, err := scanAcquisitionTask(tx.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE id=? AND version=?`, t.ID, t.Version))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAcquisitionStale
	}
	if err != nil {
		return "", err
	}
	t = stored
	checks := []struct {
		reason, query string
		args          []any
	}{
		{"rg_conflict", `SELECT EXISTS(SELECT 1 FROM audio_file_release_groups g JOIN audio_files af ON af.id=g.audio_file_id JOIN tracks tr ON tr.id=af.track_id WHERE g.rgid=? AND af.status='available' AND ((SELECT count(*) FROM audio_file_release_groups x WHERE x.audio_file_id=af.id)>1 OR EXISTS(SELECT 1 FROM album_release_groups a WHERE a.album_id=tr.album_id AND a.status='conflict')))`, []any{t.RGID}},
		{"preexisting_only", `SELECT EXISTS(SELECT 1 FROM audio_file_release_groups g JOIN audio_files af ON af.id=g.audio_file_id WHERE g.rgid=? AND af.status='available' AND af.created_at<=?) AND NOT EXISTS(SELECT 1 FROM audio_file_release_groups g JOIN audio_files af ON af.id=g.audio_file_id WHERE g.rgid=? AND af.status='available' AND af.created_at>?)`, []any{t.RGID, t.CreatedAt, t.RGID, t.CreatedAt}},
		{"no_rg_files", `SELECT EXISTS(SELECT 1 FROM audio_files af JOIN tracks tr ON tr.id=af.track_id JOIN albums al ON al.id=tr.album_id WHERE af.status='available' AND af.created_at>? AND af.created_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now') AND ?<>'' AND (instr(lower(al.title),lower(?))>0 OR instr(lower(af.relative_path),lower(?))>0) AND NOT EXISTS(SELECT 1 FROM audio_file_release_groups g WHERE g.audio_file_id=af.id))`, []any{t.CreatedAt, t.Title, t.Title, t.Title}},
		{"rule_suppressed", `SELECT EXISTS(SELECT 1 FROM album_key_rules WHERE action='delete' AND trim(source_title)<>'' AND ?<>'' AND (instr(lower(source_title),lower(?))>0 OR instr(lower(?),lower(source_title))>0))`, []any{t.Title, t.Title, t.Title}},
	}
	for _, check := range checks {
		var found bool
		if e := tx.QueryRowContext(ctx, check.query, check.args...).Scan(&found); e != nil {
			return "", e
		}
		if found {
			return check.reason, nil
		}
	}
	return "library_timeout", nil
}
