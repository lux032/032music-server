package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigration025OriginBackfillFrom024(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "old.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	entries, e := migrationFiles.ReadDir("migrations")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))) STRICT`); e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.Name() >= "025_" {
			break
		}
		script, e := migrationFiles.ReadFile("migrations/" + entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		conn, e := s.db.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); e != nil {
			t.Fatal(e)
		}
		var version int
		for _, ch := range entry.Name()[:3] {
			version = version*10 + int(ch-'0')
		}
		e = s.applyMigration(ctx, conn, version, entry.Name(), string(script))
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		conn.Close()
		if e != nil {
			t.Fatalf("old migration %d: %v", version, e)
		}
	}
	if e = s.EnsureLibrary(ctx, "Test", "/music"); e != nil {
		t.Fatal(e)
	}
	var lib, album, track int64
	if e = s.db.QueryRowContext(ctx, `SELECT id FROM libraries LIMIT 1`).Scan(&lib); e != nil {
		t.Fatal(e)
	}
	r, e := s.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,'A','A','a')`, lib)
	if e != nil {
		t.Fatal(e)
	}
	album, _ = r.LastInsertId()
	r, e = s.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title) VALUES(?,'Song','Song')`, album)
	if e != nil {
		t.Fatal(e)
	}
	track, _ = r.LastInsertId()
	for _, tc := range []struct{ title, source string }{{"Automatic", "auto"}, {"Human", "manual"}, {"Enriched", "auto"}, {"Mixed", "auto"}, {"Dated", "auto"}, {"Rejected", "auto"}} {
		key := tc.title
		r, e = s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type) VALUES(?,?,'other')`, tc.title, key)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		if _, e = s.db.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,source) VALUES(?, ?, ?)`, id, track, tc.source); e != nil {
			t.Fatal(e)
		}
		if tc.title == "Dated" {
			if _, e = s.db.ExecContext(ctx, `UPDATE works SET year=2023 WHERE id=?`, id); e != nil {
				t.Fatal(e)
			}
		}
		if tc.title == "Rejected" {
			if _, e = s.db.ExecContext(ctx, `INSERT INTO work_match_candidates(work_id,source,external_id,title,score,status) VALUES(?,'bangumi','999','Rejected',60,'rejected')`, id); e != nil {
				t.Fatal(e)
			}
		}
		if tc.title == "Enriched" {
			if _, e = s.db.ExecContext(ctx, `UPDATE works SET poster_url='poster' WHERE id=?`, id); e != nil {
				t.Fatal(e)
			}
		}
		if tc.title == "Mixed" {
			if _, e = s.db.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,source) VALUES(? ,?,'op','manual')`, id, track); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = s.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ title, origin string }{{"Automatic", "auto"}, {"Human", "manual"}, {"Enriched", "manual"}, {"Mixed", "manual"}, {"Dated", "manual"}, {"Rejected", "manual"}} {
		var origin string
		if e = s.db.QueryRowContext(ctx, `SELECT origin FROM works WHERE title=?`, tc.title).Scan(&origin); e != nil || origin != tc.origin {
			t.Fatalf("%s origin %s err %v", tc.title, origin, e)
		}
	}
	var aliases int
	if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM work_aliases`).Scan(&aliases); e != nil || aliases != 6 {
		t.Fatalf("aliases=%d %v", aliases, e)
	}
	var check string
	if e = s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); e != nil || check != "ok" {
		t.Fatalf("integrity %s %v", check, e)
	}
	rows, e := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign keys invalid")
	}
	if e = rows.Err(); e != nil && e != sql.ErrNoRows {
		t.Fatal(e)
	}
}
