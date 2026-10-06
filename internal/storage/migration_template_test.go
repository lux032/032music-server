package storage

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMain(m *testing.M) {
	cleanup := EnableMigrationTemplate()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// The template path must produce exactly what replaying the migrations does.
func TestMigrationTemplateMatchesRealMigration(t *testing.T) {
	ctx := context.Background()
	open := func(name string, skipTemplate bool) *Store {
		t.Helper()
		s, err := Open(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		s.skipMigrationTemplate = skipTemplate
		if err = s.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	}
	snapshot := func(s *Store) map[string][]string {
		t.Helper()
		out := map[string][]string{}
		for key, query := range map[string]string{
			"schema":     `SELECT type||' '||name||' '||tbl_name||' '||COALESCE(sql,'') FROM sqlite_schema ORDER BY type,name`,
			"migrations": `SELECT version||' '||name FROM schema_migrations ORDER BY version`,
			"pragmas":    `SELECT (SELECT user_version FROM pragma_user_version)||' '||(SELECT journal_mode FROM pragma_journal_mode)||' '||(SELECT page_size FROM pragma_page_size)`,
		} {
			rows, err := s.db.QueryContext(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var line string
				if err = rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				out[key] = append(out[key], line)
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
		return out
	}
	real := snapshot(open("real.db", true))
	restored := open("restored.db", false)
	if got := snapshot(restored); !reflect.DeepEqual(got, real) {
		t.Fatalf("template database differs from real migration:\nreal=%v\ngot=%v", real, got)
	}
	if len(real["migrations"]) == 0 {
		t.Fatal("no migrations recorded")
	}
	// The restored database is a normal, writable store.
	if err := restored.EnsureLibrary(ctx, "Music", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := restored.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}
