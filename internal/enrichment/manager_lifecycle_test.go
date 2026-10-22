package enrichment

import (
	"context"
	"log/slog"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// 所有测试管理器都先取消并等待后台任务，再让数据目录进入 TempDir cleanup。
// 保留父上下文以便关机/恢复用例继续显式控制取消时机。
func newManagedTestManager(t *testing.T, ctx context.Context, store *storage.Store, logger *slog.Logger, dataDirectory string) *Manager {
	t.Helper()
	base, cancel := context.WithCancel(ctx)
	manager := New(base, store, logger, dataDirectory)
	t.Cleanup(func() { cancel(); manager.Wait() })
	return manager
}
