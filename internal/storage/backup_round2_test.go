package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRejectsReservedNameCredentialTriggers(t *testing.T) {
	for _, name := range []string{"sqlite_autoindex_evil", "sqlite_sequence", "sqlite_stat9", "SQLITE_stat9"} {
		t.Run(name, func(t *testing.T) {
			s, b := backupFixture(t, 0)
			ctx := context.Background()
			mustExecBackup(t, s, `INSERT INTO playlists(id,name) VALUES(1,'NEW');INSERT INTO credential_overrides VALUES('admin_username','current','now')`)
			filename, e := b.Create(ctx, "manual", 0)
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(b.Directory, filename)
			db, e := independentDB(path, false)
			if e != nil {
				t.Fatal(e)
			}
			// sqlite_sequence 已由 AUTOINCREMENT 创建，移除其 schema 行后插入保留名触发器。
			if _, e = db.Exec(`PRAGMA writable_schema=ON;DELETE FROM sqlite_schema WHERE name=` + quoteSQL(name)); e != nil {
				t.Fatal(e)
			}
			query := fmt.Sprintf(`CREATE TRIGGER %s AFTER UPDATE ON playlists BEGIN INSERT OR REPLACE INTO credential_overrides VALUES('admin_username','attacker','now'); END`, name)
			if _, e = db.Exec(`INSERT INTO sqlite_schema(type,name,tbl_name,rootpage,sql) VALUES('trigger',?,'playlists',0,?)`, name, query); e != nil {
				t.Fatal(e)
			}
			db.Close()
			if _, e = b.Validate(ctx, path); e == nil {
				t.Fatal("accepted reserved-name trigger")
			}
			if _, _, e = b.Stage(ctx, path); e == nil {
				t.Fatal("staged reserved-name trigger")
			}
			mustExecBackup(t, s, `UPDATE playlists SET revision=revision+1`)
			var got string
			if e = s.db.QueryRow(`SELECT value FROM credential_overrides WHERE key='admin_username'`).Scan(&got); e != nil || got != "current" {
				t.Fatalf("credential changed: %s %v", got, e)
			}
			if _, e = os.Stat(b.markerPath()); !os.IsNotExist(e) {
				t.Fatal("trigger armed restore")
			}
		})
	}
}
func TestBackupEquivalentHistoricalSchema(t *testing.T) {
	s, b := backupFixture(t, 0)
	ctx := context.Background()
	name, e := b.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(b.Directory, name)
	db, e := independentDB(path, false)
	if e != nil {
		t.Fatal(e)
	}
	// 模拟旧版 SQLite RENAME 对表名引号及 ADD COLUMN 空白的不同持久化形式。
	var ddl string
	if e = db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='artworks'`).Scan(&ddl); e != nil {
		t.Fatal(e)
	}
	ddl = strings.Replace(ddl, `"artworks"`, `artworks`, 1)
	ddl = strings.Replace(ddl, "CREATE TABLE", "create  table", 1)
	if _, e = db.Exec(`PRAGMA writable_schema=ON;UPDATE sqlite_schema SET sql=? WHERE name='artworks'`, ddl); e != nil {
		t.Fatal(e)
	}
	db.Close()
	if _, e = b.Validate(ctx, path); e != nil {
		t.Fatal("equivalent historical schema rejected", e)
	}
	_ = s
}
func TestBackupRejectsStructuralChanges(t *testing.T) {
	for _, kind := range []string{"extra-column", "missing-column", "index", "check"} {
		t.Run(kind, func(t *testing.T) {
			s, b := backupFixture(t, 0)
			ctx := context.Background()
			switch kind {
			case "extra-column":
				mustExecBackup(t, s, `ALTER TABLE playlists ADD COLUMN injected TEXT`)
			case "missing-column":
				mustExecBackup(t, s, `ALTER TABLE playlists DROP COLUMN description`)
			case "index":
				mustExecBackup(t, s, `CREATE INDEX injected_index ON playlists(name)`)
			case "check":
				mustExecBackup(t, s, `PRAGMA writable_schema=ON;UPDATE sqlite_schema SET sql=replace(sql,'CHECK (id=1)','CHECK (id>0)') WHERE name='backup_settings'`)
			}
			name, e := b.Create(ctx, "manual", 0)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = b.Validate(ctx, filepath.Join(b.Directory, name)); e == nil {
				t.Fatalf("accepted %s", kind)
			}
		})
	}
}
func TestBackupPrepareFailuresRejectOrIsolate(t *testing.T) {
	for _, kind := range []string{"bad-prerestore", "bad-pending", "pending-is-directory", "missing-pending", "unknown-state", "invalid-marker", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s, b := backupFixture(t, 0)
			mustExecBackup(t, s, `INSERT INTO playlists(id,name) VALUES(1,'current')`)
			name, e := b.Create(ctx, "manual", 0)
			if e != nil {
				t.Fatal(e)
			}
			copyBackupFile(t, filepath.Join(b.Directory, name), b.pendingPath())
			hash, _ := fileHash(b.pendingPath())
			state := RestoreState{State: "prerestore_done", SHA256: hash, Prerestore: name}
			switch kind {
			case "bad-prerestore":
				os.WriteFile(filepath.Join(b.Directory, name), []byte("corrupt"), 0600)
			case "bad-pending":
				os.WriteFile(b.pendingPath(), []byte("corrupt"), 0600)
			case "pending-is-directory":
				os.Remove(b.pendingPath())
				os.Mkdir(b.pendingPath(), 0700)
			case "missing-pending":
				os.Remove(b.pendingPath())
				mustExecBackup(t, s, `UPDATE playlists SET name='current after snapshot'`)
			case "unknown-state":
				state.State = "invalid"
			case "canceled":
				state.State = "armed"
			}
			if e = atomicJSON(b.markerPath(), state); e != nil {
				t.Fatal(e)
			}
			if kind == "invalid-marker" {
				os.WriteFile(b.markerPath(), []byte("{broken"), 0600)
			}
			if kind == "canceled" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				if _, e = b.PrepareRestore(cancelCtx); e == nil {
					t.Fatal("missing canceled error")
				}
				var retained RestoreState
				if e = readJSON(b.markerPath(), &retained); e != nil || retained.State != "armed" {
					t.Fatal("canceled restore rejected")
				}
				return
			}
			s.Close()
			restored, e := b.PrepareRestore(ctx)
			if e != nil || restored {
				t.Fatalf("prepare: %v %v", restored, e)
			}
			if _, e = os.Stat(b.markerPath()); !os.IsNotExist(e) {
				t.Fatal("restart loop marker retained")
			}
			isolated := kind == "missing-pending" || kind == "unknown-state" || kind == "invalid-marker"
			if _, e = b.ManualRecovery(); isolated != (e == nil) {
				t.Fatalf("isolation mismatch %s: %v", kind, e)
			}
			if !isolated {
				if b.Result().Success || b.Result().Error == "" {
					t.Fatal("missing rejection result")
				}
				db, e := independentDB(b.Database, true)
				if e != nil {
					t.Fatal(e)
				}
				defer db.Close()
				var got string
				if e = db.QueryRow(`SELECT name FROM playlists`).Scan(&got); e != nil || got != "current" {
					t.Fatalf("original changed %s %v", got, e)
				}
			}
		})
	}
}
