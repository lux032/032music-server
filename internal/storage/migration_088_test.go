package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMigration088CompositeCreditSplitsAndBackupValidation(t *testing.T) {
	ctx := context.Background()

	checkSchema := func(t *testing.T, s *Store) {
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

		for _, tbl := range []string{"ai_review_settings", "artist_credit_splits", "artist_composite_exemptions"} {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, tbl).Scan(&count); err != nil || count != 1 {
				t.Fatalf("missing table %s (count=%d err=%v)", tbl, count, err)
			}
		}
		for _, idx := range []string{"idx_artist_credit_splits_status", "idx_artist_credit_splits_decision"} {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name=?`, idx).Scan(&count); err != nil || count != 1 {
				t.Fatalf("missing index %s (count=%d err=%v)", idx, count, err)
			}
		}
		// credit_mode='auto' must be accepted by CHECK constraint.
		if _, err := s.db.ExecContext(ctx, `UPDATE ai_review_settings SET credit_mode='auto' WHERE id=1`); err != nil {
			t.Fatalf("expected credit_mode='auto' to succeed: %v", err)
		}
	}

	t.Run("fresh migrate", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "fresh088.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("fresh migrate failed: %v", err)
		}
		checkSchema(t, s)
	})

	t.Run("upgrade 087 to 088 preserves ai_review_settings", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "upgrade088.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.MigrateUpTo(ctx, 87); err != nil {
			t.Fatalf("migrate up to 87: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE ai_review_settings SET enabled=1, model='gemini-2.5-flash', credit_mode='recommend', series_mode='auto' WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("upgrade to 088 failed: %v", err)
		}
		checkSchema(t, s)

		st, err := s.AIReviewSettings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !st.Enabled || st.Model != "gemini-2.5-flash" || st.SeriesMode != AIReviewModeAuto {
			t.Fatalf("ai_review_settings not preserved across migration 088: %+v", st)
		}
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
		if res.Version < 88 {
			t.Fatalf("backup schema version = %d, want >= 88", res.Version)
		}
	})
}

func TestCleanupOrphansProtectsActiveCompositeCreditSplits(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "orphans088.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// Create 4 unreferenced artists: composite, targetA, targetB, and trulyOrphan.
	insertArtist := func(name, key string) int64 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?)`, name, key, key)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	compID := insertArtist("ACE, 亀岡夏海", "ace, 亀岡夏海")
	targetA := insertArtist("ACE", "ace")
	targetB := insertArtist("亀岡夏海", "亀岡夏海")
	orphanID := insertArtist("Unused Artist", "unused artist")

	partIDsJSON, _ := json.Marshal([]int64{targetA, targetB})
	partNamesJSON, _ := json.Marshal([]string{"ACE", "亀岡夏海"})
	if _, err := s.db.ExecContext(ctx, `INSERT INTO artist_credit_splits(composite_artist_id,part_ids_json,part_names_json,status,source) VALUES(?,?,?,'active','manual')`,
		compID, string(partIDsJSON), string(partNamesJSON)); err != nil {
		t.Fatal(err)
	}

	if err := s.CleanupOrphans(ctx); err != nil {
		t.Fatalf("CleanupOrphans failed: %v", err)
	}

	exists := func(id int64) bool {
		var c int
		_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE id=?`, id).Scan(&c)
		return c == 1
	}
	if !exists(compID) || !exists(targetA) || !exists(targetB) {
		t.Fatalf("active credit split artists were deleted by CleanupOrphans: comp=%v a=%v b=%v", exists(compID), exists(targetA), exists(targetB))
	}
	if exists(orphanID) {
		t.Fatal("unrelated unreferenced artist should be deleted by CleanupOrphans")
	}

	// Once the split is reverted, unreferenced artists may be cleaned up normally.
	if _, err := s.db.ExecContext(ctx, `UPDATE artist_credit_splits SET status='reverted' WHERE composite_artist_id=?`, compID); err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupOrphans(ctx); err != nil {
		t.Fatalf("CleanupOrphans after revert failed: %v", err)
	}
	if exists(compID) || exists(targetA) || exists(targetB) {
		t.Fatalf("reverted unreferenced split artists should be cleaned up: comp=%v a=%v b=%v", exists(compID), exists(targetA), exists(targetB))
	}
}
