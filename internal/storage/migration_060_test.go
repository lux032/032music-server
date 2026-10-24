package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigration060PreservesDiscovery(t *testing.T) {
	c := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.MigrateUpTo(c, 59); e != nil {
		t.Fatal(e)
	}
	_, e = s.db.ExecContext(c, `INSERT INTO artists(id,display_name,sort_name,identity_key) VALUES(900,'Singer','Singer','singer');INSERT INTO libraries(id,name,root_path) VALUES(900,'test','/music')`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.db.ExecContext(c, `INSERT INTO albums(id,library_id,title,sort_title,grouping_key) VALUES(900,900,'Local','Local','local');INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,fetched_at) VALUES('11111111-1111-1111-1111-111111111111','Remote','Album','Album','2026-01-01');INSERT INTO discovery_candidates(rgid,seed_artist_id,generation,score,lane,rank,reasons_json) VALUES('11111111-1111-1111-1111-111111111111',900,4,12,'exploit',1,'[]');INSERT INTO discovery_possible_matches(rgid,album_id,reason) VALUES('11111111-1111-1111-1111-111111111111',900,'title_artist')`)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Migrate(c); e != nil {
		t.Fatal(e)
	}
	var kind, reason string
	var artist, g int64
	var score float64
	if e = s.db.QueryRow(`SELECT seed_kind,seed_artist_id,generation,score FROM discovery_candidates`).Scan(&kind, &artist, &g, &score); e != nil || kind != "artist" || artist != 900 || g != 4 || score != 12 {
		t.Fatal(kind, artist, g, score, e)
	}
	if e = s.db.QueryRow(`SELECT reason FROM discovery_possible_matches`).Scan(&reason); e != nil || reason != "title_artist" {
		t.Fatal(reason, e)
	}
	rows, e := s.db.Query(`PRAGMA foreign_key_check`)
	if e != nil {
		t.Fatal(e)
	}
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	var integrity string
	if e = s.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); e != nil || integrity != "ok" {
		t.Fatal(integrity, e)
	}
	for _, index := range []string{"idx_discovery_candidates_seed", "idx_discovery_candidates_work", "idx_discovery_matches_album", "idx_discovery_mappings_rg", "idx_discovery_mappings_confirmed"} {
		var n int
		if e = s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, index).Scan(&n); e != nil || n != 1 {
			t.Fatal(index, n, e)
		}
	}
}

func TestMigration060BackupValidateWithUserMappings(t *testing.T) {
	s, b := backupFixture(t, 0)
	mustExecBackup(t, s, `INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,fetched_at) VALUES('11111111-1111-1111-1111-111111111111','Remote','Album','Album','2026-01-01');INSERT INTO discovery_bangumi_mappings(subject_id,rgid,status) VALUES(1,'11111111-1111-1111-1111-111111111111','confirmed'),(2,'11111111-1111-1111-1111-111111111111','rejected')`)
	name, e := b.Create(context.Background(), "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(b.Directory, name)
	if _, e = b.Validate(context.Background(), path); e != nil {
		t.Fatal(e)
	}
	db, e := independentDB(path, true)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM discovery_bangumi_mappings WHERE status IN ('confirmed','rejected')`).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
}
