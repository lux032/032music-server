package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

func startupFixture(t *testing.T) (config.Config, *storage.Backups, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{DatabasePath: filepath.Join(dir, "music.db"), DataDirectory: dir, MusicDirectory: "/music", BackupTime: "02:30", BackupKeepDaily: 3, BackupKeepPreupgrade: 2}
	b := storage.NewBackups(cfg.DatabasePath, dir, "/music")
	if e := b.Initialize(); e != nil {
		t.Fatal(e)
	}
	s, e := storage.Open(cfg.DatabasePath)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(context.Background()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return cfg, b, s
}
func startupLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func TestStartupRestoreSkipsPreupgrade(t *testing.T) {
	cfg, b, s := startupFixture(t)
	ctx := context.Background()
	s.Close()
	raw, e := sql.Open("sqlite", cfg.DatabasePath)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`DELETE FROM schema_migrations WHERE version>=56;DROP INDEX idx_acq_available_file_created;DROP TABLE acquisition_evidence;DROP TABLE acquisition_tasks;DROP TABLE acquisition_search_results;DROP TABLE acquisition_searches;DROP TABLE acquisition_settings;DROP TABLE discovery_schedule;DROP TABLE discovery_bangumi_mappings;DROP TABLE discovery_work_music;DROP TABLE discovery_bangumi_music;DROP TABLE discovery_work_seeds;DROP TABLE discovery_match_rejections;DROP TABLE discovery_possible_matches;DROP TABLE discovery_candidates;DROP TABLE discovery_artist_release_groups;DROP TABLE discovery_seeds;DROP TABLE discovery_refresh_state;DROP TABLE discovery_release_groups;DROP TABLE audio_file_release_groups;DROP TABLE audio_file_releases;DROP TABLE album_release_groups;DROP TABLE release_group_decisions;DROP TABLE release_group_rebuild;DROP TABLE release_groups;DROP TABLE backup_settings;DROP TABLE login_bans;DROP TABLE login_allowlist;DROP TABLE credential_overrides;CREATE TABLE credential_overrides (
    key TEXT PRIMARY KEY CHECK (key IN ('admin_username', 'admin_password_hash', 'api_token_hash', 'media_token')),
    value TEXT NOT NULL CHECK (length(value) > 0),
    updated_at TEXT NOT NULL
) STRICT`); e != nil {
		t.Fatal(e)
	}
	raw.Close()
	name, e := b.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	stage, _, e := b.Stage(ctx, filepath.Join(b.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Arm(ctx, stage); e != nil {
		t.Fatal(e)
	}
	db, e := openStartupDatabase(ctx, cfg, b, startupLogger())
	if e != nil {
		t.Fatal(e)
	}
	if db == nil {
		t.Fatal("unexpected isolation")
	}
	defer db.Close()
	files, e := b.List()
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range files {
		if strings.Contains(f.Name, "preupgrade") {
			t.Fatal("restored startup generated preupgrade")
		}
	}
	if !b.Result().Success {
		t.Fatalf("result %+v", b.Result())
	}
}
func TestStartupMigrationFailureRollsBack(t *testing.T) {
	cfg, b, s := startupFixture(t)
	ctx := context.Background()
	name, e := b.Create(ctx, "prerestore", 2)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	raw, e := sql.Open("sqlite", cfg.DatabasePath)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = raw.Exec(`DELETE FROM schema_migrations WHERE version>=54;DROP TABLE ai_settings`); e != nil {
		t.Fatal(e)
	}
	raw.Close()
	state := storage.RestoreState{State: "swapped", Prerestore: name, SHA256: "migration-failure-test"}
	bytes, _ := json.Marshal(state)
	if e = os.WriteFile(filepath.Join(filepath.Dir(cfg.DatabasePath), "restore-state.json"), bytes, 0600); e != nil {
		t.Fatal(e)
	}
	db, e := openStartupDatabase(ctx, cfg, b, startupLogger())
	if e != nil {
		t.Fatal(e)
	}
	if db == nil {
		t.Fatal("rollback entered isolation")
	}
	defer db.Close()
	if b.Result().Success || b.Result().Error == "" {
		t.Fatalf("missing rollback result %+v", b.Result())
	}
	if e = db.Ping(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestStartupManualRecoveryNeverOpensDatabase(t *testing.T) {
	cfg, b, s := startupFixture(t)
	s.Close()
	os.Remove(cfg.DatabasePath)
	v := storage.ManualRecovery{Stage: "rollback", Error: "safe summary", Prerestore: "music-prerestore-20250101-000000-v56.db"}
	bytes, _ := json.Marshal(v)
	os.WriteFile(b.ManualRecoveryPath(), bytes, 0600)
	db, e := openStartupDatabase(context.Background(), cfg, b, startupLogger())
	if e != nil || db != nil {
		t.Fatalf("isolated startup %v %v", db, e)
	}
	if _, e = os.Stat(cfg.DatabasePath); !os.IsNotExist(e) {
		t.Fatal("opened database in isolation")
	}
	for _, path := range []string{"/api/v1/health", "/"} {
		w := httptest.NewRecorder()
		recoveryHandler(v).ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
		if path == "/" {
			for _, text := range []string{v.Prerestore, "restore-manual-recovery.json", "restore-state.json", "restore-pending.db", "-journal"} {
				if !strings.Contains(w.Body.String(), text) {
					t.Fatalf("missing guide %s", text)
				}
			}
		}
	}
}
func TestRestartExitCode(t *testing.T) {
	if exitCode(errRestart) != 75 || exitCode(nil) != 0 || exitCode(errors.New("failure")) != 1 {
		t.Fatal("exit code mapping")
	}
}

func TestRecoveryModeGracefulCancellation(t *testing.T) {
	cfg, b, s := startupFixture(t)
	s.Close()
	cfg.ListenAddress = "127.0.0.1:0"
	if e := b.RequireManualRecovery("rollback", errors.New("test failure")); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := runRecoveryMode(ctx, cfg, b, startupLogger()); e != nil {
		t.Fatal(e)
	}
}

func TestRecoveryPageWithoutPrerestore(t *testing.T) {
	w := httptest.NewRecorder()
	recoveryHandler(storage.ManualRecovery{}).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "请在 backups/ 中选择最新的 music-prerestore-*.db") {
		t.Fatal(w.Body.String())
	}
}

func TestRecoveryPageStageSummary(t *testing.T) {
	for _, stage := range []string{"rollback", "prepare", "reject"} {
		w := httptest.NewRecorder()
		recoveryHandler(storage.ManualRecovery{Stage: stage}).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		body := w.Body.String()
		want := "恢复状态无法判定或无法安全取消"
		if stage == "rollback" {
			want = "自动回滚失败"
		}
		if !strings.Contains(body, want) {
			t.Fatalf("stage %s summary: %s", stage, body)
		}
		if stage != "rollback" && strings.Contains(body, "自动回滚失败") {
			t.Fatal("prepare/reject incorrectly described as rollback failure")
		}
		for _, step := range []string{"-wal", "-shm", "-journal", "restore-state.json", "restore-pending.db", "restore-manual-recovery.json"} {
			if !strings.Contains(body, step) {
				t.Fatalf("missing recovery step %s", step)
			}
		}
	}
}
