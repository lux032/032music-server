package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// LibraryWatchSettings 是管理页保存的曲库监控覆盖设置。
type LibraryWatchSettings struct {
	Enabled  bool
	Interval time.Duration
}

// LibraryWatchSettings 返回管理页覆盖设置；found=false 表示未覆盖，应使用
// 环境变量默认值。
func (s *Store) LibraryWatchSettings(ctx context.Context) (LibraryWatchSettings, bool, error) {
	var enabled int
	var seconds int64
	err := s.db.QueryRowContext(ctx, `SELECT enabled, interval_seconds FROM library_watch_settings WHERE id=1`).Scan(&enabled, &seconds)
	if errors.Is(err, sql.ErrNoRows) {
		return LibraryWatchSettings{}, false, nil
	}
	if err != nil {
		return LibraryWatchSettings{}, false, err
	}
	return LibraryWatchSettings{Enabled: enabled != 0, Interval: time.Duration(seconds) * time.Second}, true, nil
}

// SaveLibraryWatchSettings 写入覆盖设置；Interval 按整秒保存。
func (s *Store) SaveLibraryWatchSettings(ctx context.Context, value LibraryWatchSettings) error {
	seconds := int64(value.Interval / time.Second)
	if seconds < 1 {
		return errors.New("library watch interval must be at least one second")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO library_watch_settings(id, enabled, interval_seconds) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled, interval_seconds=excluded.interval_seconds, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, boolInt(value.Enabled), seconds)
	return err
}

// ResetLibraryWatchSettings 删除覆盖设置，恢复环境变量默认值。
func (s *Store) ResetLibraryWatchSettings(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM library_watch_settings WHERE id=1`)
	return err
}
