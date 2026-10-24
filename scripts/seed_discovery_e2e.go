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
		exec(`INSERT OR REPLACE INTO discovery_candidates(rgid,seed_kind,seed_artist_id,generation,score,lane,rank,reasons_json) SELECT ?,'artist',id,0,10,'exploit',?,'[{"code":"favorite_artist","artist":"E2E Artist"}]' FROM artists LIMIT 1`, id, i+1)
		exec(`INSERT OR IGNORE INTO discovery_artist_release_groups(artist_id,rgid) SELECT id,? FROM artists LIMIT 1`, id)
		if i >= 2 {
			exec(`INSERT OR IGNORE INTO discovery_possible_matches(rgid,album_id,reason) SELECT ?,id,'title_artist' FROM albums LIMIT 1`, id)
		}
	}

	exec(`INSERT OR IGNORE INTO works(id,title,normalized_title,type) VALUES(900001,'发现测试作品','发现测试作品','anime')`)
	exec(`INSERT OR REPLACE INTO work_external_profiles(work_id,source,external_id,title,raw_json,fetched_at) VALUES(900001,'bangumi','900001','发现测试作品','{}','2026-01-01')`)
	exec(`INSERT OR IGNORE INTO album_works(album_id,work_id,role,source) SELECT id,900001,'ost','manual' FROM albums LIMIT 1`)
	exec(`UPDATE metadata_source_settings SET enabled=1 WHERE source='bangumi'`)
	exec(`UPDATE discovery_refresh_state SET status='completed',generation=0`)
	exec(`INSERT OR REPLACE INTO discovery_work_seeds(work_id,subject_id,generation,status) VALUES(900001,900001,0,'done')`)
	for i, title := range []string{"Discovery Work OST", "A / B", "Discovery Auto"} {
		subject := 900010 + i
		rg := fmt.Sprintf("55555555-5555-4555-8555-%012d", i+1)
		status := "suggested"
		if i != 1 {
			status = "auto"
		}
		exec(`INSERT OR IGNORE INTO discovery_release_groups(rgid,title,primary_type,kind,first_release_date,artist_credit,fetched_at) VALUES(?,?,'Album','Album','2024-01-01','E2E Artist','2026-01-01')`, rg, title)
		exec(`INSERT OR REPLACE INTO discovery_bangumi_music(subject_id,name,name_cn,date,artists_json,multi_title,search_status,fetched_at,searched_at) VALUES(?,?,?,'2024-01-01','["E2E Artist"]',?,'hits',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, subject, title, "测试音乐条目", i == 1)
		exec(`INSERT OR REPLACE INTO discovery_work_music(work_id,subject_id,relation,ordinal) VALUES(900001,?,'原声集',?)`, subject, i)
		exec(`INSERT OR REPLACE INTO discovery_bangumi_mappings(subject_id,rgid,status,score,evidence_json) VALUES(?,?,?,100,'["title_strict","artist_overlap","date_day"]')`, subject, rg, status)
		if status == "auto" {
			exec(`INSERT OR REPLACE INTO discovery_candidates(rgid,seed_kind,seed_work_id,bangumi_subject_id,generation,score,lane,rank,reasons_json) VALUES(?,'work',900001,?,0,10,'exploit',?,'[{"code":"work_plays","work":"发现测试作品"}]')`, rg, subject, i+5)
		}
	}

	// A second suggested group verifies group-local rejection. A pending multi-title
	// subject is intentionally not mapped until the real refresh searches fake MB.
	exec(`INSERT OR REPLACE INTO discovery_bangumi_music(subject_id,name,name_cn,search_status,fetched_at,searched_at) VALUES(900013,'Fresh A / B','刷新生成条目','pending','2026-01-01',NULL)`)
	exec(`INSERT OR REPLACE INTO discovery_work_music(work_id,subject_id,relation,ordinal) VALUES(900001,900013,'片头曲',3)`)
	exec(`INSERT OR REPLACE INTO discovery_bangumi_music(subject_id,name,name_cn,search_status,fetched_at,searched_at) VALUES(900014,'Independent Suggested','另一组待确认','hits',strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`)
	exec(`INSERT OR REPLACE INTO discovery_work_music(work_id,subject_id,relation,ordinal) VALUES(900001,900014,'片头曲',4)`)
	for i, subject := range []int{900011, 900014} {
		rg := fmt.Sprintf("66666666-6666-4666-8666-%012d", i+1)
		exec(`INSERT OR IGNORE INTO discovery_release_groups(rgid,title,primary_type,kind,first_release_date,artist_credit,fetched_at) VALUES(?,?,'Single','Single','2024-01-01','E2E Artist','2026-01-01')`, rg, []string{"A / B alternate", "Independent Suggested"}[i])
		exec(`INSERT OR REPLACE INTO discovery_bangumi_mappings(subject_id,rgid,status,score,evidence_json) VALUES(?,?,'suggested',60,'["multi_title"]')`, subject, rg)
	}
	if len(os.Args) > 2 && os.Args[2] == "acquisition" {
		exec(`UPDATE acquisition_settings SET enabled=1,jackett_url='http://127.0.0.1:45447',jackett_api_key='e2e-key',jackett_indexer='jpopsuki',qb_url='http://127.0.0.1:45447',qb_username='e2e',qb_password='e2e-password',qb_category='music' WHERE id=1`)
		reasons := []string{"qb_unreachable", "qb_auth", "qb_rejected", "qb_missing", "torrent_unavailable", "library_timeout", "rg_conflict", "preexisting_only", "no_rg_files", "rule_suppressed"}
		for i, reason := range append(reasons, "", "", "", "") {
			state := "ATTENTION"
			if i >= len(reasons) {
				state = []string{"QB_SUBMITTING", "DOWNLOADING", "AWAITING_LIBRARY", "IN_LIBRARY"}[i-len(reasons)]
			}
			exec(`INSERT OR IGNORE INTO discovery_release_groups(rgid,title,primary_type,kind,artist_credit,fetched_at) VALUES(?,?,'Album','Album','E2E Artist','2026-01-01')`, fmt.Sprintf("44444444-4444-4444-8444-%012d", i+1), "Task "+reason+state)
			publicID := fmt.Sprintf("22222222-2222-4222-8222-%012d", i+1)
			exec(`INSERT INTO acquisition_tasks(public_id,idempotency_key,rgid,title,artist,kind,torrent_title,state,attention_reason,next_check_at,finished_at) VALUES(?,?,?,?,?,'Album','Fixture FLAC',?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now','+1 day'),CASE WHEN ?='IN_LIBRARY' THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') END)`, publicID, publicID, fmt.Sprintf("44444444-4444-4444-8444-%012d", i+1), "Task "+reason+state, "E2E Artist", state, reason, state)
			if state == "IN_LIBRARY" {
				exec(`INSERT INTO acquisition_evidence(task_id,source,album_id,detected_count) SELECT t.id,'manual',a.id,20 FROM acquisition_tasks t,albums a WHERE t.public_id=? LIMIT 1`, publicID)
			}
		}
	}
	if e = tx.Commit(); e != nil {
		panic(e)
	}
}
