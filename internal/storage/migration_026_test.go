package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestM1Migration026PreservesAlbumWorksAndIndex(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))) STRICT`); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "026_" {
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
		_, e = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`)
		if e != nil {
			t.Fatal(e)
		}
		version := 0
		for _, ch := range entry.Name()[:3] {
			version = version*10 + int(ch-'0')
		}
		e = s.applyMigration(ctx, conn, version, entry.Name(), string(script))
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		conn.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('test','/test')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(1,'Album','Album','a')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type) VALUES('Work','work','anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,source) VALUES(1,1,'manual')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number,track_type) VALUES(1,'Track','Track',1,1,'regular')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(1,1,'op','auto','work|anime|0')`); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE source='manual'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("preserved %d: %v", n, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE source='auto' AND inferred_key='work|anime|0'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("work_tracks preserved %d: %v", n, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('idx_album_works_work','idx_work_tracks_track','idx_work_tracks_work_role','idx_work_tracks_work_track')`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("indexes %d: %v", n, err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("FK violations %d: %v", n, err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE album_works SET source='bangumi' WHERE album_id=1 AND work_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE work_tracks SET source='bangumi' WHERE work_id=1 AND track_id=1`); err != nil {
		t.Fatal(err)
	}
}
