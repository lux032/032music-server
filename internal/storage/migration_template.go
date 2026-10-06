package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"modernc.org/sqlite"
)

// Test-suite acceleration. Almost every test opens a fresh database and calls
// Migrate, which replays all migrations (~90% of a typical test's runtime,
// far more under -race). With the template enabled, the first Migrate of a
// pristine database in the process builds a template by replaying the real
// migrations once; later pristine databases get a page-for-page copy of it
// through SQLite's online backup API, which is identical to having run the
// migrations. Databases that already contain anything (e.g. migration tests
// that apply a prefix of the migrations first) always take the real path.
//
// Production code never enables this.
var migrationTemplate struct {
	mu      sync.Mutex
	enabled bool
	dir     string
	path    string // built template database; "" until first use
}

// EnableMigrationTemplate turns on the migration template for this process.
// Intended to be called from a test binary's TestMain; the returned cleanup
// disables it and removes the template files.
func EnableMigrationTemplate() (cleanup func()) {
	t := &migrationTemplate
	t.mu.Lock()
	t.enabled = true
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.enabled = false
		if t.dir != "" {
			_ = os.RemoveAll(t.dir)
		}
		t.dir, t.path = "", ""
	}
}

func (s *Store) restoreMigrationTemplate(ctx context.Context) error {
	if s.skipMigrationTemplate {
		return nil
	}
	t := &migrationTemplate
	t.mu.Lock()
	enabled := t.enabled
	t.mu.Unlock()
	if !enabled {
		return nil
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for migration template: %w", err)
	}
	defer conn.Close()
	var objects int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema`).Scan(&objects); err != nil {
		return fmt.Errorf("inspect database for migration template: %w", err)
	}
	if objects != 0 {
		return nil
	}

	templatePath, err := buildMigrationTemplate()
	if err != nil {
		return err
	}
	dsn, err := sqliteDSN(templatePath)
	if err != nil {
		return err
	}
	return conn.Raw(func(driverConn any) error {
		restorer, ok := driverConn.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("sqlite driver connection %T does not support restore", driverConn)
		}
		backup, err := restorer.NewRestore(dsn)
		if err != nil {
			return fmt.Errorf("start migration template restore: %w", err)
		}
		_, stepErr := backup.Step(-1)
		finishErr := backup.Finish()
		if err = errors.Join(stepErr, finishErr); err != nil {
			return fmt.Errorf("restore migration template: %w", err)
		}
		return nil
	})
}

// buildMigrationTemplate returns the template database path, building it by
// replaying the real migrations the first time.
func buildMigrationTemplate() (string, error) {
	t := &migrationTemplate
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.path != "" {
		return t.path, nil
	}
	dir, err := os.MkdirTemp("", "032music-migration-template-")
	if err != nil {
		return "", fmt.Errorf("create migration template directory: %w", err)
	}
	path := filepath.Join(dir, "template.db")
	if err = func() error {
		template, err := Open(path)
		if err != nil {
			return err
		}
		defer template.Close()
		template.skipMigrationTemplate = true
		ctx := context.Background()
		if err = template.Migrate(ctx); err != nil {
			return err
		}
		// Fold the WAL into the main file so the template is self-contained.
		_, err = template.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		return err
	}(); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("build migration template: %w", err)
	}
	t.dir, t.path = dir, path
	return path, nil
}
