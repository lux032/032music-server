package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type BackupSettings struct {
	Enabled                   bool
	Time                      string
	KeepDaily, KeepPreupgrade int
}

func (v BackupSettings) Validate() error {
	if _, err := time.Parse("15:04", v.Time); err != nil || len(v.Time) != 5 {
		return fmt.Errorf("备份时间必须为 HH:MM")
	}
	if v.KeepDaily < 1 || v.KeepDaily > 365 || v.KeepPreupgrade < 1 || v.KeepPreupgrade > 365 {
		return fmt.Errorf("保留份数须为 1–365")
	}
	return nil
}
func (s *Store) BackupSettings(ctx context.Context, defaults BackupSettings) (BackupSettings, error) {
	v := defaults
	err := s.db.QueryRowContext(ctx, `SELECT enabled,time,keep_daily,keep_preupgrade FROM backup_settings WHERE id=1`).Scan(&v.Enabled, &v.Time, &v.KeepDaily, &v.KeepPreupgrade)
	if errors.Is(err, sql.ErrNoRows) || (err != nil && strings.Contains(err.Error(), "no such table: backup_settings")) {
		return defaults, nil
	}
	return v, err
}
func (s *Store) SaveBackupSettings(ctx context.Context, v BackupSettings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO backup_settings VALUES(1,?,?,?,?) ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled,time=excluded.time,keep_daily=excluded.keep_daily,keep_preupgrade=excluded.keep_preupgrade`, v.Enabled, v.Time, v.KeepDaily, v.KeepPreupgrade)
	return err
}
func (s *Store) ResetBackupSettings(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM backup_settings`)
	return err
}
func MaxMigrationVersion() int {
	entries, _ := migrationFiles.ReadDir("migrations")
	max := 0
	for _, e := range entries {
		n, _, _ := strings.Cut(e.Name(), "_")
		v, _ := strconv.Atoi(n)
		if v > max {
			max = v
		}
	}
	return max
}

// PendingMigrations 不将每次启动的 Go 修复计入待升级项；空库返回 0。
func (s *Store) PendingMigrations(ctx context.Context) (int, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name='schema_migrations'`).Scan(&exists); err != nil {
		return 0, err
	}
	if exists == 0 {
		return 0, nil
	}
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT coalesce(max(version),0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, err
	}
	if v == 0 {
		return 0, nil
	}
	return MaxMigrationVersion() - v, nil
}
