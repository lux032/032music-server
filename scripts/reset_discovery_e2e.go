//go:build ignore

package main

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"os"
)

func main() {
	db, e := sql.Open("sqlite", os.Args[1]+"?_pragma=busy_timeout(5000)")
	if e != nil {
		panic(e)
	}
	defer db.Close()
	_, e = db.Exec(`DELETE FROM discovery_bangumi_mappings; DELETE FROM discovery_work_music; DELETE FROM discovery_bangumi_music; DELETE FROM discovery_work_seeds; DELETE FROM acquisition_evidence; DELETE FROM acquisition_tasks; DELETE FROM acquisition_search_results; DELETE FROM acquisition_searches; DELETE FROM release_group_decisions; DELETE FROM album_release_groups; DELETE FROM discovery_match_rejections; DELETE FROM discovery_possible_matches; DELETE FROM discovery_candidates; DELETE FROM discovery_artist_release_groups; DELETE FROM discovery_release_groups; UPDATE discovery_refresh_state SET status='idle',generation=0; DELETE FROM discovery_seeds; DELETE FROM http_response_cache WHERE source='bangumi'; UPDATE discovery_schedule SET enabled=1,interval_days=7,run_hour=20,cache_days=7,last_auto_at=NULL;`)
	if e != nil {
		panic(e)
	}
}
