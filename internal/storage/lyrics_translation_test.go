package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// migrateBefore applies the real migrations whose file name sorts before prefix.
func migrateBefore(t *testing.T, ctx context.Context, s *Store, prefix string) {
	t.Helper()
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))) STRICT`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= prefix {
			break
		}
		script, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
		version := 0
		for _, ch := range entry.Name()[:3] {
			version = version*10 + int(ch-'0')
		}
		err = s.applyMigration(ctx, conn, version, entry.Name(), string(script))
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		conn.Close()
		if err != nil {
			t.Fatalf("apply %s: %v", entry.Name(), err)
		}
	}
}

// Upgrade path: a 051 database keeps its AI settings and usage rows, and the
// usage table accepts the new "translate" purpose.
func TestMigration052KeepsAISettingsAndUsage(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	migrateBefore(t, ctx, s, "052_")
	// Seed the historical schema directly: current setters require newer columns.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ai_settings(id,base_url,api_key,model,tagging_enabled,requests_per_minute,concurrency) VALUES(1,'https://x.test/v1','sk','lite',1,60,4)`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := s.AddAIUsage(ctx, now, "lite", "mix", AIUsage{Requests: 3, PromptTokens: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddAIUsage(ctx, now, "lite", "translate", AIUsage{Requests: 1}); err == nil {
		t.Fatal("051 schema accepted the translate purpose")
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	st, err := s.AISettings(ctx)
	if err != nil || st.Model != "lite" || st.APIKey != "sk" || !st.TaggingEnabled {
		t.Fatalf("settings = %+v %v", st, err)
	}
	if tr, err := s.LyricsTranslationSettings(ctx); err != nil || tr.Enabled || tr.Model != "" {
		t.Fatalf("translation defaults = %+v %v", tr, err)
	}
	if err := s.AddAIUsage(ctx, now, "lite", "translate", AIUsage{Requests: 1, PromptTokens: 7}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.AIUsageTotals(ctx, "")
	if err != nil || len(rows) != 2 || rows[0].Purpose != "mix" || rows[0].Usage.Requests != 3 || rows[1].Purpose != "translate" {
		t.Fatalf("usage = %+v %v", rows, err)
	}
}

func TestLyricsTranslationSettingsDoNotTouchTagging(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	// Saving translation settings before any AI settings creates the row with defaults.
	if err := s.SaveLyricsTranslationSettings(ctx, LyricsTranslationSettings{Enabled: true, Model: " flash "}); err != nil {
		t.Fatal(err)
	}
	tr, _ := s.LyricsTranslationSettings(ctx)
	ai, _ := s.AISettings(ctx)
	if !tr.Enabled || tr.Model != "flash" || ai.TaggingEnabled || ai.RequestsPerMinute != 60 {
		t.Fatalf("tr = %+v ai = %+v", tr, ai)
	}
	if TranslationConfigured(ai, tr) {
		t.Fatal("no base URL / key yet")
	}
	// Saving the tagging settings keeps the translation columns.
	ai.BaseURL, ai.APIKey, ai.Model = "https://x.test/v1", "sk", "lite"
	if err := s.SaveAISettings(ctx, ai); err != nil {
		t.Fatal(err)
	}
	if tr, _ = s.LyricsTranslationSettings(ctx); !tr.Enabled || tr.Model != "flash" {
		t.Fatalf("tagging save reset translation settings: %+v", tr)
	}
	if !TranslationConfigured(ai, tr) || EffectiveTranslationModel(ai, tr) != "flash" {
		t.Fatal("translation model should win")
	}
	if EffectiveTranslationModel(ai, LyricsTranslationSettings{}) != "lite" {
		t.Fatal("empty translation model falls back to the tagging model")
	}
}

func TestLyricsTranslationCache(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	trackID := seedSessionTrack(t, ctx, s, 200000)
	src := []string{"君の名は", "", "夏"}
	hash := LyricsSourceHash(src)
	if hash == LyricsSourceHash([]string{"君の名は", "夏"}) {
		t.Fatal("blank lines must change the hash")
	}
	if _, err := s.CachedLyricsTranslation(ctx, trackID, "en", hash); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty cache: %v", err)
	}
	v := LyricsTranslation{TrackID: trackID, TargetLanguage: "en", SourceHash: hash, SourceLines: src, TranslatedLines: []string{"Your name", "", "Summer"}, Model: "lite", PromptTokens: 10}
	id, err := s.SaveLyricsTranslation(ctx, v)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.CachedLyricsTranslation(ctx, trackID, "en", hash)
	if err != nil || got.ID != id || got.TranslatedLines[2] != "Summer" || got.Title == "" || got.Model != "lite" {
		t.Fatalf("cached = %+v %v", got, err)
	}
	// Same key overwrites in place (retranslate).
	v.TranslatedLines, v.Model = []string{"Kimi no Na wa", "", "Natsu"}, "flash"
	if id2, err := s.SaveLyricsTranslation(ctx, v); err != nil || id2 != id {
		t.Fatalf("overwrite id = %d %v", id2, err)
	}
	if got, _ = s.LyricsTranslationByID(ctx, id); got.Model != "flash" || got.TranslatedLines[0] != "Kimi no Na wa" {
		t.Fatalf("overwrite = %+v", got)
	}
	// Another language is a separate entry.
	v.TargetLanguage = "zh-Hans"
	if _, err := s.SaveLyricsTranslation(ctx, v); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.ListLyricsTranslations(ctx, LyricsTranslationFilter{Language: "zh-Hans"})
	if err != nil || total != 1 || len(items) != 1 || items[0].TargetLanguage != "zh-Hans" {
		t.Fatalf("filter = %+v %d %v", items, total, err)
	}
	if _, total, _ = s.ListLyricsTranslations(ctx, LyricsTranslationFilter{Query: "no such song"}); total != 0 {
		t.Fatal("query filter")
	}
	if _, total, _ = s.ListLyricsTranslations(ctx, LyricsTranslationFilter{Query: got.Title}); total != 2 {
		t.Fatalf("title query total = %d", total)
	}
	if langs, _ := s.LyricsTranslationLanguages(ctx); len(langs) != 2 {
		t.Fatalf("languages = %v", langs)
	}
	// Mismatched lengths and unknown tracks are rejected.
	if _, err := s.SaveLyricsTranslation(ctx, LyricsTranslation{TrackID: trackID, TargetLanguage: "en", SourceHash: "x", SourceLines: []string{"a"}, TranslatedLines: nil}); err == nil {
		t.Fatal("length mismatch accepted")
	}
	if _, err := s.SaveLyricsTranslation(ctx, LyricsTranslation{TrackID: trackID + 999, TargetLanguage: "en", SourceHash: "x", SourceLines: []string{}, TranslatedLines: []string{}}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown track: %v", err)
	}
	if n, err := s.DeleteLyricsTranslations(ctx, []int64{id}); err != nil || n != 1 {
		t.Fatalf("delete = %d %v", n, err)
	}
	if _, total, _ = s.ListLyricsTranslations(ctx, LyricsTranslationFilter{}); total != 1 {
		t.Fatalf("after delete total = %d", total)
	}
}
