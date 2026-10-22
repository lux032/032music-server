package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func backupFixture(t *testing.T, version int) (*Store, *Backups) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "music.db")
	s, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e = s.MigrateUpTo(context.Background(), version); e != nil {
		t.Fatal(e)
	}
	b := NewBackups(p, dir, "/music")
	if e = b.Initialize(); e != nil {
		t.Fatal(e)
	}
	return s, b
}
func mustExecBackup(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, e := s.db.ExecContext(context.Background(), q, args...); e != nil {
		t.Fatal(e)
	}
}
func TestBackupSnapshotConsistencyCleanup(t *testing.T) {
	ctx := context.Background()
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `CREATE TABLE snapshot_probe(id INTEGER PRIMARY KEY, a INTEGER,b INTEGER); INSERT INTO snapshot_probe VALUES(1,0,0)`)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = s.db.ExecContext(ctx, `UPDATE snapshot_probe SET a=a+1,b=b+1`)
			}
		}
	}()
	name, e := b.Create(ctx, "manual", 0)
	close(stop)
	wg.Wait()
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(b.Directory, name)
	if _, e = checkDatabase(ctx, p, "quick_check"); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	if raw[18] != 1 || raw[19] != 1 {
		t.Fatal("snapshot must remain DELETE mode")
	}
	db, e := independentDB(p, true)
	if e != nil {
		t.Fatal(e)
	}
	var a, c int
	e = db.QueryRow(`SELECT a,b FROM snapshot_probe`).Scan(&a, &c)
	db.Close()
	removeSidecars(p)
	if e != nil || a != c {
		t.Fatalf("inconsistent snapshot: %d %d %v", a, c, e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = b.Create(canceled, "daily", 3); e == nil {
		t.Fatal("canceled snapshot succeeded")
	}
	entries, _ := os.ReadDir(filepath.Join(b.Directory, ".staging"))
	if len(entries) != 0 {
		t.Fatalf("tmp remnants: %v", entries)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, e = os.Stat(p + suffix); !os.IsNotExist(e) {
			t.Fatalf("snapshot sidecar %s", suffix)
		}
	}
	tmp := filepath.Join(b.Directory, ".staging", "old.tmp")
	os.WriteFile(tmp, []byte("x"), 0600)
	upload := filepath.Join(filepath.Dir(b.Database), "restore-upload-interrupted.db")
	os.WriteFile(upload, []byte("x"), 0600)
	if e = b.Initialize(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(tmp); !os.IsNotExist(e) {
		t.Fatal("tmp not cleaned")
	}
	if _, e = os.Stat(upload); !os.IsNotExist(e) {
		t.Fatal("upload not cleaned")
	}
}
func TestBackupVacuumRemovesExistingTargetAndCanceledTarget(t *testing.T) {
	_, b := backupFixture(t, 0)
	p := filepath.Join(t.TempDir(), "out.db")
	os.WriteFile(p, []byte("partial"), 0600)
	if e := vacuumSnapshot(context.Background(), b.Database, p); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := vacuumSnapshot(ctx, b.Database, p); e == nil {
		t.Fatal("expected cancellation")
	}
	if _, e := os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("canceled target survived")
	}
}
func TestBackupRotationAndDailyCatchup(t *testing.T) {
	_, b := backupFixture(t, 0)
	for _, kind := range []string{"daily", "manual", "preupgrade", "prerestore"} {
		for n := 1; n <= 4; n++ {
			name := fmt.Sprintf("music-%s-2025010%d-023000-v56.db", kind, n)
			os.WriteFile(filepath.Join(b.Directory, name), []byte("x"), 0600)
		}
	}
	os.WriteFile(filepath.Join(b.Directory, "unrelated.db"), []byte("x"), 0600)
	for _, kind := range []string{"daily", "preupgrade", "prerestore"} {
		if e := b.rotate(kind, 2); e != nil {
			t.Fatal(e)
		}
	}
	list, e := b.List()
	if e != nil || len(list) != 10 {
		t.Fatalf("rotation: %d %v", len(list), e)
	}
	if _, e = os.Stat(filepath.Join(b.Directory, "unrelated.db")); e != nil {
		t.Fatal(e)
	}
	v := BackupSettings{Enabled: true, Time: "02:30", KeepDaily: 3, KeepPreupgrade: 2}
	if b.Due(time.Date(2025, 1, 4, 20, 0, 0, 0, time.UTC), v) != true {
		t.Fatal("UTC+8 next day catchup")
	}
	if b.Due(time.Date(2025, 1, 4, 1, 0, 0, 0, time.UTC), v) {
		t.Fatal("daily already exists")
	}
	if b.Due(time.Date(2025, 1, 5, 17, 0, 0, 0, time.UTC), v) {
		t.Fatal("before 02:30 UTC+8")
	}
}
func TestBackupValidateRejectsUnsafeSources(t *testing.T) {
	for _, kind := range []string{"trigger", "view", "virtual", "version", "core", "not-sqlite"} {
		t.Run(kind, func(t *testing.T) {
			s, b := backupFixture(t, 0)
			switch kind {
			case "trigger":
				mustExecBackup(t, s, `CREATE TRIGGER malicious AFTER UPDATE ON albums BEGIN DELETE FROM playlists; END`)
			case "view":
				mustExecBackup(t, s, `CREATE VIEW malicious AS SELECT * FROM albums`)
			case "virtual":
				mustExecBackup(t, s, `CREATE VIRTUAL TABLE malicious USING fts5(value)`)
			case "version":
				mustExecBackup(t, s, `INSERT INTO schema_migrations(version,name) VALUES(999,'future')`)
			case "core":
				mustExecBackup(t, s, `DROP TABLE playlist_items`)
			case "not-sqlite":
				p := filepath.Join(t.TempDir(), "bad.db")
				os.WriteFile(p, []byte("not sqlite"), 0600)
				if _, e := b.Validate(context.Background(), p); e == nil {
					t.Fatal("accepted non-SQLite")
				}
				return
			}
			name, e := b.Create(context.Background(), "manual", 0)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = b.Validate(context.Background(), filepath.Join(b.Directory, name)); e == nil {
				t.Fatal("accepted unsafe source")
			}
		})
	}
}
func TestBackupRestoreCrashReentryAndOldWAL(t *testing.T) {
	for _, crash := range []string{"armed", "prerestore_done", "renamed", "swapped", "finalized", "done"} {
		t.Run(crash, func(t *testing.T) {
			ctx := context.Background()
			s, b := backupFixture(t, 0)
			mustExecBackup(t, s, `INSERT INTO playlists(id,name,revision) VALUES(1,'NEW',2)`)
			name, e := b.Create(ctx, "manual", 0)
			if e != nil {
				t.Fatal(e)
			}
			// 源快照之后的写入只在旧 WAL 中，确保 B1 防回放。
			mustExecBackup(t, s, `PRAGMA wal_autocheckpoint=0; UPDATE playlists SET name='OLD',revision=99 WHERE id=1`)
			stage, _, e := b.Stage(ctx, filepath.Join(b.Directory, name))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = os.Stat(b.markerPath()); !os.IsNotExist(e) {
				t.Fatal("staging armed restore")
			}
			if e = b.Arm(ctx, stage); e != nil {
				t.Fatal(e)
			}
			var state RestoreState
			readJSON(b.markerPath(), &state)
			if crash != "armed" {
				pre, e := b.Create(ctx, "prerestore", 2)
				if e != nil {
					t.Fatal(e)
				}
				state.Prerestore = pre
				state.State = "prerestore_done"
				if e = atomicJSON(b.markerPath(), state); e != nil {
					t.Fatal(e)
				}
				if crash != "prerestore_done" {
					if e = b.replaceDatabase(ctx, b.pendingPath(), pre); e != nil {
						t.Fatal(e)
					}
					if crash != "renamed" {
						state.State = "swapped"
						atomicJSON(b.markerPath(), state)
					}
				}
			}
			// 原有连接在进程崩溃后不再可用；测试先关闭它再模拟启动。
			s.Close()
			restored, e := b.PrepareRestore(ctx)
			if e != nil || !restored {
				t.Fatalf("prepare %v %v", restored, e)
			}
			current, e := Open(b.Database)
			if e != nil {
				t.Fatal(e)
			}
			defer current.Close()
			if e = current.Migrate(ctx); e != nil {
				t.Fatal(e)
			}
			if e = b.CompleteRestore(ctx, current); e != nil {
				t.Fatal(e)
			}
			if crash == "finalized" || crash == "done" {
				result := b.Result()
				if crash == "finalized" {
					result.State = "swapped"
				} else {
					result.State = "done"
				}
				if e = atomicJSON(b.markerPath(), result); e != nil {
					t.Fatal(e)
				}
				if _, e = b.PrepareRestore(ctx); e != nil {
					t.Fatal(e)
				}
				if crash == "finalized" {
					if e = b.CompleteRestore(ctx, current); e != nil {
						t.Fatal(e)
					}
				}
			}
			var title string
			var revision int
			if e = current.db.QueryRow(`SELECT name,revision FROM playlists WHERE id=1`).Scan(&title, &revision); e != nil {
				t.Fatal(e)
			}
			if title != "NEW" || revision != 100 {
				t.Fatalf("WAL/revision regression: %s %d", title, revision)
			}
			if !b.Result().Success {
				t.Fatal("missing success result")
			}
			if _, e = os.Stat(b.markerPath()); !os.IsNotExist(e) {
				t.Fatal("unfinished marker")
			}
		})
	}
}
func TestBackupRestorePreservesCurrentCredentialsFromOldSnapshot(t *testing.T) {
	ctx := context.Background()
	old, source := backupFixture(t, 18)
	mustExecBackup(t, old, `INSERT INTO admin_sessions(token_hash,username,csrf_token,expires_at) VALUES('revoked','old','x','2099')`)
	name, e := source.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	current, b := backupFixture(t, 0)
	mustExecBackup(t, current, `INSERT INTO credential_overrides(key,value,updated_at) VALUES('admin_username','current','now'); INSERT INTO admin_sessions(token_hash,username,csrf_token,expires_at) VALUES('live','current','csrf','2099'); UPDATE lastfm_scrobble_settings SET api_secret='secret',session_key='session',username='current'; UPDATE metadata_source_settings SET api_key='key' WHERE source='lastfm'; INSERT OR IGNORE INTO ai_settings(id) VALUES(1); UPDATE ai_settings SET base_url='https://current',api_key='ai-key',proxy_url='http://proxy'`)
	stage, _, e := b.Stage(ctx, filepath.Join(source.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Arm(ctx, stage); e != nil {
		t.Fatal(e)
	}
	current.Close()
	if _, e = b.PrepareRestore(ctx); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(b.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if e = restored.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	mustExecBackup(t, restored, `INSERT INTO lastfm_scrobble_queue(track_id,track,artist,album,started_at,duration_seconds) VALUES(NULL,'t','a','b',1,100)`)
	if e = b.CompleteRestore(ctx, restored); e != nil {
		t.Fatal(e)
	}
	for query, want := range map[string]string{`SELECT value FROM credential_overrides WHERE key='admin_username'`: "current", `SELECT token_hash FROM admin_sessions`: "live", `SELECT api_secret||':'||session_key||':'||username FROM lastfm_scrobble_settings`: "secret:session:current", `SELECT api_key FROM metadata_source_settings WHERE source='lastfm'`: "key", `SELECT base_url||':'||api_key||':'||proxy_url FROM ai_settings`: "https://current:ai-key:http://proxy"} {
		var got string
		if e = restored.db.QueryRow(query).Scan(&got); e != nil || got != want {
			t.Fatalf("%s: %q != %q (%v)", query, got, want, e)
		}
	}
	var count int
	if e = restored.db.QueryRow(`SELECT count(*) FROM lastfm_scrobble_queue`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("queue not cleared: %d %v", count, e)
	}
}
func TestBackupRestoreRollback(t *testing.T) {
	ctx := context.Background()
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `INSERT INTO playlists(id,name) VALUES(1,'snapshot')`)
	name, e := b.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	mustExecBackup(t, s, `UPDATE playlists SET name='current'`)
	stage, _, e := b.Stage(ctx, filepath.Join(b.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	b.Arm(ctx, stage)
	s.Close()
	if _, e = b.PrepareRestore(ctx); e != nil {
		t.Fatal(e)
	}
	swapped, e := Open(b.Database)
	if e != nil {
		t.Fatal(e)
	}
	mustExecBackup(t, swapped, `DELETE FROM schema_migrations WHERE version=56; CREATE TABLE backup_settings_broken(x); DROP TABLE backup_settings; ALTER TABLE backup_settings_broken RENAME TO backup_settings`)
	// 模拟 migration/H2 错误由启动层进入自动回滚。
	swapped.Close()
	if e = b.RollbackRestore(ctx, fmt.Errorf("migration/H2 failure")); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(b.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	var title string
	reopened.db.QueryRow(`SELECT name FROM playlists WHERE id=1`).Scan(&title)
	if title != "current" {
		t.Fatal("rollback lost current database")
	}
	result := b.Result()
	if result.Success || !strings.Contains(result.Error, "migration/H2 failure") {
		t.Fatalf("result %+v", result)
	}
	if _, e = b.PrepareRestore(ctx); e != nil {
		t.Fatal("restore loop", e)
	}
}
func TestBackupImageReferencesAndWarnings(t *testing.T) {
	ctx := context.Background()
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `INSERT INTO artists(id,identity_key,display_name,sort_name) VALUES(1,'a','a','a'); INSERT INTO artist_custom_images(artist_id,file_path,mime_type,content_hash,byte_size) VALUES(1,'/old/path/keep.png','image/png','hash',1)`)
	name, e := b.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	mustExecBackup(t, s, `DELETE FROM artist_custom_images`)
	refs, e := b.ImageReferences(ctx)
	if e != nil || !refs["keep.png"] {
		t.Fatalf("unprotected image: %v %v", refs, e)
	}
	validation, e := b.Validate(ctx, filepath.Join(b.Directory, name))
	if e != nil || validation.MissingImages != 1 {
		t.Fatalf("warning: %+v %v", validation, e)
	}
	stage, _, e := b.Stage(ctx, filepath.Join(b.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Delete(name); e != nil {
		t.Fatal(e)
	}
	refs, e = b.ImageReferences(ctx)
	if e != nil || !refs["keep.png"] {
		t.Fatal("stage reference not protected", e)
	}
	if e = b.CancelStage(stage); e != nil {
		t.Fatal(e)
	}
	refs, e = b.ImageReferences(ctx)
	if e != nil || refs["keep.png"] {
		t.Fatal("deleted backup still protects image", e)
	}
}
func TestBackupPendingMigrations(t *testing.T) {
	s, _ := backupFixture(t, 55)
	n, e := s.PendingMigrations(context.Background())
	if e != nil || n != 1 {
		t.Fatalf("pending=%d %v", n, e)
	}
}

func TestBackupPrerestoreFailureNeverTouchesWAL(t *testing.T) {
	ctx := context.Background()
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `INSERT INTO playlists(id,name) VALUES(1,'snapshot')`)
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
	mustExecBackup(t, s, `PRAGMA wal_autocheckpoint=0;UPDATE playlists SET name='current'`)
	walBefore, e := os.ReadFile(b.Database + "-wal")
	if e != nil {
		t.Fatal(e)
	}
	staging := filepath.Join(b.Directory, ".staging")
	if e = os.RemoveAll(staging); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(staging, []byte("block snapshot creation"), 0600); e != nil {
		t.Fatal(e)
	}
	if restored, err := b.PrepareRestore(ctx); err != nil || restored {
		t.Fatalf("prerestore rejection: %v %v", restored, err)
	}
	if b.Result().Success || b.Result().Error == "" {
		t.Fatal("missing failed result")
	}
	if _, err := os.Stat(b.markerPath()); !os.IsNotExist(err) {
		t.Fatal("restore marker retained")
	}
	walAfter, e := os.ReadFile(b.Database + "-wal")
	if e != nil || string(walBefore) != string(walAfter) {
		t.Fatal("prerestore failure touched WAL", e)
	}
	var got string
	if e = s.db.QueryRow(`SELECT name FROM playlists`).Scan(&got); e != nil || got != "current" {
		t.Fatal("changed current database", got, e)
	}
}
func TestBackupRollbackCrashReentry(t *testing.T) {
	ctx := context.Background()
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `INSERT INTO playlists(id,name) VALUES(1,'snapshot')`)
	name, e := b.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	mustExecBackup(t, s, `UPDATE playlists SET name='current'`)
	stage, _, e := b.Stage(ctx, filepath.Join(b.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	if e = b.Arm(ctx, stage); e != nil {
		t.Fatal(e)
	}
	s.Close()
	if _, e = b.PrepareRestore(ctx); e != nil {
		t.Fatal(e)
	}
	var state RestoreState
	readJSON(b.markerPath(), &state)
	state.RollingBack = true
	state.Error = "H2 failed"
	atomicJSON(b.markerPath(), state)
	// 模拟回滚复制和换库均已完成但结果状态尚未提交时崩溃。
	src := filepath.Join(b.Directory, state.Prerestore)
	tmp := filepath.Join(filepath.Dir(b.Database), "rollback-copy.db")
	raw, _ := os.ReadFile(src)
	os.WriteFile(tmp, raw, 0600)
	if e = b.replaceDatabase(ctx, tmp, state.Prerestore); e != nil {
		t.Fatal(e)
	}
	if restored, e := b.PrepareRestore(ctx); e != nil || restored {
		t.Fatalf("rollback reentry: %v %v", restored, e)
	}
	if b.Result().Success || b.Result().Error != "H2 failed" {
		t.Fatalf("result %+v", b.Result())
	}
}
func TestBackupLibraryRootMismatch(t *testing.T) {
	s, b := backupFixture(t, 0)
	if e := s.EnsureLibrary(context.Background(), "old", "/old/music"); e != nil {
		t.Fatal(e)
	}
	name, e := b.Create(context.Background(), "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	v, e := b.Validate(context.Background(), filepath.Join(b.Directory, name))
	if e != nil || !v.RootMismatch {
		t.Fatalf("root warning %+v %v", v, e)
	}
}
