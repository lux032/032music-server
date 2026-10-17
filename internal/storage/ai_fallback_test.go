package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigration054FallbackSettings(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migrateBefore(t, ctx, s, "054_")
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ai_settings(id,model,api_key,translation_model,translation_enabled) VALUES(1,'primary','secret','translate',1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ai, err := s.AISettings(ctx)
	if err != nil || ai.Model != "primary" || ai.APIKey != "secret" || ai.FallbackModel != "" {
		t.Fatalf("ai=%+v err=%v", ai, err)
	}
	tr, err := s.LyricsTranslationSettings(ctx)
	if err != nil || tr.Model != "translate" || !tr.Enabled || tr.FallbackModel != "" {
		t.Fatalf("tr=%+v err=%v", tr, err)
	}
	ai.FallbackModel = " tag-backup "
	if err := s.SaveAISettings(ctx, ai); err != nil {
		t.Fatal(err)
	}
	tr.FallbackModel = " translation-backup "
	if err := s.SaveLyricsTranslationSettings(ctx, tr); err != nil {
		t.Fatal(err)
	}
	ai, _ = s.AISettings(ctx)
	tr, _ = s.LyricsTranslationSettings(ctx)
	if ai.FallbackModel != "tag-backup" || tr.FallbackModel != "translation-backup" {
		t.Fatalf("ai=%+v tr=%+v", ai, tr)
	}
	// Saving tagging again must preserve translation settings.
	ai.FallbackModel = ""
	if err := s.SaveAISettings(ctx, ai); err != nil {
		t.Fatal(err)
	}
	tr, _ = s.LyricsTranslationSettings(ctx)
	if tr.FallbackModel != "translation-backup" || !tr.Enabled || tr.Model != "translate" {
		t.Fatalf("translation overwritten: %+v", tr)
	}
}
