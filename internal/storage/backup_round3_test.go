package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupRejectsDDLTokenSemanticChanges(t *testing.T) {
	for _, kind := range []string{"quoted-null", "quoted-true", "quoted-false", "quoted-current_time", "quoted-current_date", "quoted-current_timestamp", "quoted-noncolumn", "quoted-realcolumn", "collation-a0", "collation-85", "where-a0", "where-85"} {
		t.Run(kind, func(t *testing.T) {
			_, b := backupFixture(t, 0)
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
			object := "idx_playback_sessions_chain_counted"
			if strings.HasPrefix(kind, "quoted-") && kind != "quoted-null" {
				object = "artist_image_backfill_items"
			}
			if strings.HasPrefix(kind, "collation-") {
				object = "genres"
			}
			var ddl string
			if e = db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, object).Scan(&ddl); e != nil {
				db.Close()
				t.Fatal(e)
			}
			changed := ddl
			switch {
			case kind == "quoted-null":
				changed = strings.Replace(ddl, "counted_at IS NOT NULL", `counted_at IS NOT "null"`, 1)
			case strings.HasPrefix(kind, "quoted-"):
				word := strings.TrimPrefix(kind, "quoted-")
				switch word {
				case "noncolumn":
					word = "not_a_real_column"
				case "realcolumn":
					// 字符串字面量 'completed' 改为同名真实列的双引号标识符：
					// 语义从字符串比较变为列比较，必须拒绝。
					word = "status"
				}
				changed = strings.Replace(ddl, "status='completed'", `status="`+word+`"`, 1)
			case strings.HasPrefix(kind, "collation-"):
				c := byte(0xa0)
				if strings.HasSuffix(kind, "85") {
					c = 0x85
				}
				changed = strings.Replace(ddl, "COLLATE NOCASE", "COLLATE NOCASE"+string([]byte{c}), 1)
			default:
				c := byte(0xa0)
				if strings.HasSuffix(kind, "85") {
					c = 0x85
				}
				changed = strings.Replace(ddl, "IS NOT NULL", "IS NOT NULL"+string([]byte{c}), 1)
			}
			if changed == ddl {
				db.Close()
				t.Fatal("fixture rewrite did not match")
			}
			if _, e = db.Exec(`PRAGMA writable_schema=ON;UPDATE sqlite_schema SET sql=? WHERE name=?`, changed, object); e != nil {
				db.Close()
				t.Fatal(e)
			}
			db.Close()
			if _, e = b.Validate(ctx, path); e == nil {
				t.Fatal("semantic DDL change accepted")
			}
			if _, _, e = b.Stage(ctx, path); e == nil {
				t.Fatal("semantic DDL change staged")
			}
		})
	}
}

func TestBackupSchemaTokensSQLiteByteRules(t *testing.T) {
	identifiers := map[string]bool{"artworks": true, "counted_at": true, "null": true, "status": true}
	normalized := func(s string) string {
		t.Helper()
		v, e := schemaTokens(s, identifiers)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	if normalized(`CREATE TABLE "artworks"`) != normalized("create\t table artworks") {
		t.Fatal("historical identifier quoting must remain compatible")
	}
	for _, quote := range []string{`"`, "`", "["} {
		end := quote
		if quote == "[" {
			end = "]"
		}
		for _, word := range []string{"null", "true", "false", "current_time", "current_date", "current_timestamp", "noncolumn"} {
			if normalized("IS NOT "+quote+word+end) == normalized("IS NOT "+word) {
				t.Fatalf("quoted DQS word collapsed: %s", word)
			}
		}
	}
	for _, c := range []byte{0x85, 0xa0, 0xff} {
		if normalized("NOCASE"+string([]byte{c})) == normalized("NOCASE") {
			t.Fatalf("high byte swallowed %x", c)
		}
	}
	if normalized("word"+string([]byte{0x85})) == normalized("word"+string([]byte{0xa0})) {
		t.Fatal("invalid UTF-8 bytes collapsed")
	}
	if normalized("A$B") != normalized("a$b") {
		t.Fatal("ASCII identifier folding")
	}
}

func TestBackupRollingBackFailurePreservesSnapshotIdentity(t *testing.T) {
	s, b := backupFixture(t, 0)
	s.Close()
	state := RestoreState{State: "swapped", RollingBack: true, SHA256: "retained-sha", Prerestore: "music-prerestore-20250101-000000-v56.db", Error: "interrupted rollback"}
	if e := atomicJSON(b.markerPath(), state); e != nil {
		t.Fatal(e)
	}
	// 缺失的 prerestore 模拟断电后再次回滚失败。
	if _, e := b.PrepareRestore(context.Background()); e == nil {
		t.Fatal("expected rollback failure")
	}
	manual, e := b.ManualRecovery()
	if e != nil {
		t.Fatal(e)
	}
	if manual.Stage != "rollback" || manual.Prerestore != state.Prerestore {
		t.Fatalf("isolation identity overwritten: %+v", manual)
	}
	result := b.Result()
	if result.SHA256 != state.SHA256 || result.Prerestore != state.Prerestore {
		t.Fatalf("result identity lost: %+v", result)
	}
	if _, e = os.Stat(b.markerPath()); !os.IsNotExist(e) {
		t.Fatal("execution marker retained")
	}
	if !strings.Contains(manual.Error, "自动回滚失败") {
		t.Fatal(fmt.Sprintf("wrong summary: %+v", manual))
	}
}
