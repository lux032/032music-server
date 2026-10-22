package main

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

func recoveryHandler(v storage.ManualRecovery) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.URL.Path == "/api/v1/health" {
			fmt.Fprint(w, "manual recovery required")
			return
		}
		snapshot := "backups/" + filepath.Base(v.Prerestore)
		if v.Prerestore == "" {
			snapshot = "请在 backups/ 中选择最新的 music-prerestore-*.db"
		}
		// 不公开数据库错误细节（可能包含敏感内容），只显示固定摘要及受控文件名。
		fmt.Fprintf(w, "数据库恢复进入安全隔离模式；%s。请停止服务，先删除数据库旁的 -wal、-shm、-journal，再复制 %s 为目标数据库；最后删除 restore-state.json、restore-pending.db、restore-manual-recovery.json 后重新启动。不要在服务运行时替换数据库。", storage.ManualRecoverySummary(v.Stage), html.EscapeString(snapshot))
	})
}
func runRecoveryMode(ctx context.Context, cfg config.Config, b *storage.Backups, logger *slog.Logger) error {
	v, e := b.ManualRecovery()
	if e != nil {
		return e
	}
	logger.Error("数据库安全隔离：停止服务，删除数据库旁 -wal/-shm/-journal，复制 prerestore 快照还原；删除 restore-state.json、restore-pending.db、restore-manual-recovery.json 后重启", "prerestore", filepath.Base(v.Prerestore), "stage", v.Stage)
	server := &http.Server{Addr: cfg.ListenAddress, Handler: recoveryHandler(v), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case e := <-done:
		return e
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
