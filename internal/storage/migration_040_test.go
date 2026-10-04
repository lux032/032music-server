package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// 升级路径：在 039 库上已有艺术家/身份/自定义头像数据，应用 040 后补全
// 表可用且候选查询读到旧数据。
func TestMigration040ArtistImageBackfillUpgrade(t *testing.T) {
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
		if entry.Name() >= "040_" {
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
			conn.Close()
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
			t.Fatalf("apply %s: %v", entry.Name(), e)
		}
	}
	// 039 库上的旧数据：一位有身份的艺术家、一位有自定义头像的艺术家。
	if _, err = s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Alpha','Alpha','alpha'),('Beta','Beta','beta')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO artist_external_profiles(artist_id,source,external_id,display_name,raw_json,fetched_at) VALUES(1,'musicbrainz','mbid-1','Alpha','{}','2024-01-01T00:00:00Z'),(2,'musicbrainz','mbid-2','Beta','{}','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO artist_custom_images(artist_id,content_hash,mime_type,file_path,byte_size) VALUES(2,'h','image/png','b.png',10)`); err != nil {
		t.Fatal(err)
	}

	// 应用 040（走 Migrate 的未应用分支）。
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('artist_image_backfill_runs','artist_image_backfill_items')`).Scan(&tables); err != nil || tables != 2 {
		t.Fatalf("backfill tables=%d err=%v", tables, err)
	}
	candidates, err := s.ArtistImageBackfillCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ID != 1 || candidates[0].Name != "Alpha" {
		t.Fatalf("candidates=%+v", candidates)
	}
	run, err := s.CreateArtistImageBackfillRun(ctx, candidates)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.ClaimArtistImageBackfillItem(ctx, run)
	if err != nil || item.ArtistName != "Alpha" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	// 重复应用幂等（M12）：再次 Migrate 不报错。
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}
