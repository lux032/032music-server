package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigration091ArtistImageAuditAndWebSearch(t *testing.T) {
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

		for _, tbl := range []string{
			"web_search_settings",
			"artist_image_backfill_runs",
			"artist_image_backfill_items",
			"artist_image_reviews",
			"artist_image_rejections",
			"artist_image_audit_actions",
			"ai_usage",
		} {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name=?`, tbl).Scan(&count); err != nil || count != 1 {
				t.Fatalf("missing table %s (count=%d err=%v)", tbl, count, err)
			}
		}
		// Verify new audit_all / audit_force modes and ai_usage purpose work.
		if _, err := s.db.ExecContext(ctx, `INSERT INTO ai_usage(day,model,purpose,requests) VALUES('2026-11-26','gpt-6-luna','artist_image_audit',1)`); err != nil {
			t.Fatalf("insert artist_image_audit ai_usage failed: %v", err)
		}
		imgSt, err := s.ArtistImageSettings(ctx)
		if err != nil {
			t.Fatalf("ArtistImageSettings: %v", err)
		}
		if imgSt.AuditAutoConfidence != 85 {
			t.Fatalf("AuditAutoConfidence = %d, want 85", imgSt.AuditAutoConfidence)
		}
	}

	t.Run("fresh migrate", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "fresh091.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("fresh migrate failed: %v", err)
		}
		checkSchema(t, s)
	})

	t.Run("upgrade 090 to 091 preserves runs items and deepseek key", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "upgrade091.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.MigrateUpTo(ctx, 90); err != nil {
			t.Fatalf("migrate up to 90: %v", err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE artist_alias_settings SET deepseek_key='legacy-ds-key' WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO artist_image_backfill_runs(status,mode,total_items,processed_items,cached_items) VALUES('completed','all',1,1,1)`)
		if err != nil {
			t.Fatal(err)
		}
		runID, _ := res.LastInsertId()
		if _, err := s.db.ExecContext(ctx, `INSERT INTO artist_image_backfill_items(run_id,artist_id,artist_name,status,outcome) VALUES(?,42,'Legacy Artist','completed','cached')`, runID); err != nil {
			t.Fatal(err)
		}

		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("upgrade to 091 failed: %v", err)
		}
		checkSchema(t, s)

		ws, err := s.WebSearchSettings(ctx)
		if err != nil {
			t.Fatalf("WebSearchSettings: %v", err)
		}
		if ws.DeepSeekKey != "legacy-ds-key" || ws.Provider != WebSearchProviderTavilyThenDeepSeek {
			t.Fatalf("migrated WebSearchSettings = %+v, want legacy-ds-key", ws)
		}
		run, err := s.DurableArtistImageBackfillRun(ctx, runID)
		if err != nil {
			t.Fatalf("DurableArtistImageBackfillRun: %v", err)
		}
		if run.Mode != "all" || run.Cached != 1 || run.Kept != 0 || run.Replaced != 0 || run.Cleared != 0 {
			t.Fatalf("migrated run = %+v", run)
		}
	})
}
