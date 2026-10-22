package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupRejectsDisguisedSchemaAndCredentialTrigger(t *testing.T) {
	for _, kind := range []string{"disguised-trigger", "comment-virtual", "spaces-virtual"} {
		t.Run(kind, func(t *testing.T) {
			s, b := backupFixture(t, 0)
			ctx := context.Background()
			mustExecBackup(t, s, `INSERT INTO playlists(id,name) VALUES(1,'NEW'); INSERT INTO credential_overrides VALUES('admin_username','current','now')`)
			name, e := b.Create(ctx, "manual", 0)
			if e != nil {
				t.Fatal(e)
			}
			source := filepath.Join(b.Directory, name)
			db, e := independentDB(source, false)
			if e != nil {
				t.Fatal(e)
			}
			query := `CREATE TRIGGER evil AFTER UPDATE ON playlists BEGIN INSERT OR REPLACE INTO credential_overrides VALUES('admin_username','attacker','now'); END`
			if kind == "comment-virtual" {
				query = `CREATE /**/ VIRTUAL TABLE evil USING fts5(value)`
			}
			if kind == "spaces-virtual" {
				query = "CREATE \n  VIRTUAL   TABLE evil USING fts5(value)"
			}
			if _, e = db.Exec(`PRAGMA writable_schema=ON`); e != nil {
				t.Fatal(e)
			}
			if _, e = db.Exec(`INSERT INTO sqlite_schema(type,name,tbl_name,rootpage,sql) VALUES('table','evil',?,0,?)`, map[bool]string{true: "playlists", false: "evil"}[kind == "disguised-trigger"], query); e != nil {
				t.Fatal(e)
			}
			db.Close()
			// 用完整导入入口作为探针；Stage 必须在生成 pending/armed 之前拒绝攻击。
			if _, e = b.Validate(ctx, source); e == nil {
				t.Fatal("Validate accepted disguised schema")
			}
			if stage, _, e := b.Stage(ctx, source); e == nil {
				t.Fatalf("Stage accepted attacker: %s", stage)
			}
			if _, e = os.Stat(b.markerPath()); !os.IsNotExist(e) {
				t.Fatal("attack armed restore")
			}
			mustExecBackup(t, s, `UPDATE playlists SET revision=revision+1`)
			var username string
			if e = s.db.QueryRow(`SELECT value FROM credential_overrides WHERE key='admin_username'`).Scan(&username); e != nil || username != "current" {
				t.Fatalf("credential trigger took effect: %s %v", username, e)
			}
		})
	}
}
func TestBackupSchemaReceiptRoundTrip(t *testing.T) {
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `CREATE TABLE backup_restore_receipt (sha256 TEXT PRIMARY KEY) STRICT`)
	name, e := b.Create(context.Background(), "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Validate(context.Background(), filepath.Join(b.Directory, name)); e != nil {
		t.Fatal(e)
	}
}
func copyBackupFile(t *testing.T, source, dest string) {
	t.Helper()
	raw, e := os.ReadFile(source)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(dest, raw, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestBackupB1CrashWALRegression(t *testing.T) {
	ctx := context.Background()
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `INSERT INTO playlists(id,name) VALUES(1,'NEW')`)
	name, e := b.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	// 让当前库与 VACUUM 快照具有相同页布局，否则旧帧可能写到无关页，反向对照无效。
	s.Close()
	copyBackupFile(t, filepath.Join(b.Directory, name), b.Database)
	s, e = Open(b.Database)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	s.db.SetMaxOpenConns(1)
	mustExecBackup(t, s, `PRAGMA wal_checkpoint(TRUNCATE); PRAGMA wal_autocheckpoint=0; UPDATE playlists SET name='OLD'`)
	// 保持源连接打开，原样复制崩溃现场，绝不在副本上调用原连接 Close。
	for _, safe := range []bool{false, true} {
		t.Run(fmt.Sprint("safe=", safe), func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "music.db")
			for _, suffix := range []string{"", "-wal", "-shm"} {
				copyBackupFile(t, b.Database+suffix, p+suffix)
			}
			crash := NewBackups(p, dir, "/music")
			if e := crash.Initialize(); e != nil {
				t.Fatal(e)
			}
			pending := crash.pendingPath()
			copyBackupFile(t, filepath.Join(b.Directory, name), pending)
			if safe {
				hash, _ := fileHash(pending)
				if e := atomicJSON(crash.markerPath(), RestoreState{State: "armed", SHA256: hash}); e != nil {
					t.Fatal(e)
				}
				if restored, e := crash.PrepareRestore(ctx); e != nil || !restored {
					t.Fatalf("prepare %v %v", restored, e)
				}
			} else {
				if e := os.Rename(pending, p); e != nil {
					t.Fatal(e)
				}
			}
			dsn, _ := sqliteDSN(p)
			db, e := sql.Open("sqlite", dsn)
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			var got string
			if e = db.QueryRow(`SELECT name FROM playlists WHERE id=1`).Scan(&got); e != nil {
				t.Fatal(e)
			}
			want := "OLD"
			if safe {
				want = "NEW"
			}
			if got != want {
				t.Fatalf("B1: got %s want %s", got, want)
			}
		})
	}
}
func TestBackupRollbackCorruptSwappedAndManualIsolation(t *testing.T) {
	ctx := context.Background()
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `INSERT INTO playlists(id,name) VALUES(1,'safe')`)
	name, e := b.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	if e = atomicJSON(b.markerPath(), RestoreState{State: "swapped", Prerestore: name}); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(b.Database, []byte("corrupt swapped database"), 0600)
	os.WriteFile(b.Database+"-journal", []byte("stale journal"), 0600)
	if e = b.RollbackRestore(ctx, fmt.Errorf("migration failed")); e != nil {
		t.Fatal(e)
	}
	if _, e = checkDatabase(ctx, b.Database, "quick_check"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(b.Database + "-journal"); !os.IsNotExist(e) {
		t.Fatal("journal retained")
	}
	atomicJSON(b.markerPath(), RestoreState{State: "swapped", Prerestore: "music-prerestore-20000101-000000-v56.db"})
	if e = b.RollbackRestore(ctx, fmt.Errorf("H2 failed")); e == nil {
		t.Fatal("missing snapshot accepted")
	}
	v, e := b.ManualRecovery()
	if e != nil || v.Stage != "rollback" || !strings.HasPrefix(v.Prerestore, "music-prerestore") {
		t.Fatalf("manual marker %+v %v", v, e)
	}
	if _, e = os.Stat(b.markerPath()); !os.IsNotExist(e) {
		t.Fatal("automatic restore loop marker retained")
	}
}
func TestBackupPreserveNullableAPIKey(t *testing.T) {
	s, b := backupFixture(t, 0)
	ctx := context.Background()
	mustExecBackup(t, s, `UPDATE metadata_source_settings SET api_key=NULL WHERE source='lastfm'`)
	name, e := b.Create(ctx, "prerestore", 2)
	if e != nil {
		t.Fatal(e)
	}
	mustExecBackup(t, s, `UPDATE metadata_source_settings SET api_key='snapshot-old' WHERE source='lastfm'`)
	conn, e := s.db.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	uri, _ := readonlyURI(filepath.Join(b.Directory, name))
	if _, e = conn.ExecContext(ctx, "ATTACH "+quoteSQL(uri)+" AS prerestore"); e != nil {
		t.Fatal(e)
	}
	tx, e := conn.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = preserveCredentials(ctx, tx); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	var isnull bool
	if e = conn.QueryRowContext(ctx, `SELECT api_key IS NULL FROM metadata_source_settings WHERE source='lastfm'`).Scan(&isnull); e != nil || !isnull {
		t.Fatalf("NULL lost %v %v", isnull, e)
	}
}
func TestBackupInitializeRemovesOrphanPending(t *testing.T) {
	_, b := backupFixture(t, 0)
	os.WriteFile(b.pendingPath(), []byte("orphan"), 0600)
	if e := b.Initialize(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(b.pendingPath()); !os.IsNotExist(e) {
		t.Fatal("orphan retained")
	}
}

func TestBackupImageReferencesDoesNotWaitForSnapshotMutex(t *testing.T) {
	_, b := backupFixture(t, 0)
	b.mu.Lock()
	done := make(chan error, 1)
	go func() { _, e := b.ImageReferences(context.Background()); done <- e }()
	select {
	case e := <-done:
		b.mu.Unlock()
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		b.mu.Unlock()
		<-done
		t.Fatal("image reference reads blocked on snapshot mutex")
	}
}
