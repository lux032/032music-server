//go:build ignore

// Isolated discovery fixture; never run against the shared e2e database.
package main

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
)

func main() {
	db, e := sql.Open("sqlite", os.Args[1]+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if e != nil {
		panic(e)
	}
	defer db.Close()
	var n int
	if e = db.QueryRow(`SELECT count(*) FROM tracks`).Scan(&n); e != nil || n < 20 {
		os.Exit(2)
	}
	tx, e := db.Begin()
	if e != nil {
		panic(e)
	}
	defer tx.Rollback()
	exec := func(q string, args ...any) {
		if _, e := tx.Exec(q, args...); e != nil {
			panic(e)
		}
	}
	exec(`UPDATE release_group_rebuild SET status='ready'`)
	exec(`INSERT OR IGNORE INTO artist_external_profiles(artist_id,source,external_id,display_name,raw_json,fetched_at) SELECT id,'musicbrainz','33333333-3333-4333-8333-333333333333',display_name,'{}','2026-01-01' FROM artists LIMIT 1`)
	for i, title := range []string{"Discovery Missing", "Discovery Replace", "Discovery Compare Yes", "Discovery Compare No"} {
		id := fmt.Sprintf("11111111-1111-4111-8111-%012d", i+1)
		exec(`INSERT OR IGNORE INTO discovery_release_groups(rgid,title,primary_type,kind,first_release_date,artist_credit,fetched_at) VALUES(?,?,'Album','Album','2024','E2E Artist','2026-01-01')`, id, title)
		exec(`INSERT OR REPLACE INTO discovery_candidates SELECT ?,id,0,10,'exploit',?,'[{"code":"favorite_artist","artist":"E2E Artist"}]' FROM artists LIMIT 1`, id, i+1)
		exec(`INSERT OR IGNORE INTO discovery_artist_release_groups SELECT id,? FROM artists LIMIT 1`, id)
		if i >= 2 {
			exec(`INSERT OR IGNORE INTO discovery_possible_matches SELECT ?,id,'title_artist' FROM albums LIMIT 1`, id)
		}
	}
	if e = tx.Commit(); e != nil {
		panic(e)
	}
}
