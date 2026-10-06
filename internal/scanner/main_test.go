package scanner

import (
	"os"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// Fresh test databases are copied from a once-migrated template instead of
// replaying every migration (see storage.EnableMigrationTemplate).
func TestMain(m *testing.M) {
	cleanup := storage.EnableMigrationTemplate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}
