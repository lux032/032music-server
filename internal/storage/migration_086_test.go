package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigration086UpgradeIndexesAndBackupValidation(t *testing.T) {
	ctx := context.Background()

	checkIndexes := func(t *testing.T, s *Store) {
		t.Helper()
		var integrity string
		if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity_check = %q err=%v", integrity, err)
		}
		fkRows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		if fkRows.Next() {
			fkRows.Close()
			t.Fatal("foreign_key_check returned violations")
		}
		fkRows.Close()

		for _, idx := range []string{
			"idx_tracks_added_id",
			"idx_artist_image_candidates_artist_status",
		} {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name=?`, idx).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("missing index %s", idx)
			}
		}
	}

	t.Run("fresh migrate", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "fresh086.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("fresh migrate failed: %v", err)
		}
		checkIndexes(t, s)
	})

	t.Run("upgrade 085 to 086", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "upgrade086.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.MigrateUpTo(ctx, 85); err != nil {
			t.Fatalf("migrate up to 85 failed: %v", err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("upgrade to 86 failed: %v", err)
		}
		checkIndexes(t, s)
	})

	t.Run("backup schema validation", func(t *testing.T) {
		_, b := backupFixture(t, 0)
		name, err := b.Create(ctx, "manual", 0)
		if err != nil {
			t.Fatalf("backup create failed: %v", err)
		}
		res, err := b.Validate(ctx, filepath.Join(b.Directory, name))
		if err != nil {
			t.Fatalf("backup validate failed: %v", err)
		}
		if res.Version < 86 {
			t.Fatalf("backup schema version = %d, want >= 86", res.Version)
		}
	})
}
