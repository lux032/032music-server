package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// 升级路径：在 040 库上已有歌手/专辑关系/身份/简介数据，应用 041 后补全
// 表可用且候选查询读到旧数据（含歌手判定、人工简介与 found 行排除）。
func TestMigration041ArtistBiographyBackfillUpgrade(t *testing.T) {
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
		if entry.Name() >= "041_" {
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
	// 040 库上的旧数据：一位有专辑关系的缺简介歌手、一位有人工简介的歌手、
	// 一位已有 found 版本行的歌手、一位纯幕后人员。
	if _, err = s.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Music','/music')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Alpha','Alpha','alpha'),('Beta','Beta','beta'),('Gamma','Gamma','gamma'),('Delta','Delta','delta')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(1,'Album','Album','album')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title) VALUES(1,'Song','Song')`); err != nil {
		t.Fatal(err)
	}
	// Alpha/Beta/Gamma 是专辑歌手；Delta 只是作曲（纯幕后）。
	if _, err = s.db.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position) VALUES(1,1,0),(1,2,1),(1,3,2)`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(1,4,0,'composer')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artists SET user_biography='人工简介' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO artist_biographies(artist_id,source,language,biography,status) VALUES(3,'wikipedia','zh','已有简介','found')`); err != nil {
		t.Fatal(err)
	}

	// 应用 041（走 Migrate 的未应用分支）。
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('artist_biography_backfill_runs','artist_biography_backfill_items')`).Scan(&tables); err != nil || tables != 2 {
		t.Fatalf("backfill tables=%d err=%v", tables, err)
	}
	candidates, err := s.ArtistBiographyBackfillCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ID != 1 || candidates[0].Name != "Alpha" {
		t.Fatalf("candidates=%+v", candidates)
	}
	run, err := s.CreateArtistBiographyBackfillRun(ctx, candidates)
	if err != nil {
		t.Fatal(err)
	}
	item, err := s.ClaimArtistBiographyBackfillItem(ctx, run)
	if err != nil || item.ArtistName != "Alpha" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	// 重复应用幂等：再次 Migrate 不报错。
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}
