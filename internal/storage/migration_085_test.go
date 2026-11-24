package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func explainQueryPlan(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", query, err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func TestOpenRuntimePragmasAndMigrateOptimize(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "pragmas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	var cacheSize, tempStore int
	if err := s.db.QueryRowContext(ctx, `PRAGMA cache_size`).Scan(&cacheSize); err != nil {
		t.Fatal(err)
	}
	if cacheSize > -64000 {
		t.Fatalf("cache_size = %d, want <= -64000", cacheSize)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA temp_store`).Scan(&tempStore); err != nil {
		t.Fatal(err)
	}
	if tempStore != 2 {
		t.Fatalf("temp_store = %d, want 2 (MEMORY)", tempStore)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	var integrity string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q err=%v", integrity, err)
	}
}

func TestMigration085UpgradeIndexesAndPlans(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "m085.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.MigrateUpTo(ctx, 84); err != nil {
		t.Fatalf("migrate up to 84: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("upgrade to 85: %v", err)
	}

	var integrity string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q err=%v", integrity, err)
	}
	fkRows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if fkRows.Next() {
		t.Fatal("foreign_key_check returned violations")
	}
	fkRows.Close()

	requiredIndexes := []string{
		"idx_tracks_norm_title",
		"idx_albums_sort_title_nocase",
		"idx_albums_added_at",
		"idx_work_aliases_work",
		"idx_track_audio_features_version_analyzed",
		"idx_playback_sessions_counted",
		"idx_track_tags_source_track",
		"idx_work_match_candidates_status_work",
		"idx_artist_relation_candidates_status_artist",
		"idx_album_subject_candidates_status_ext",
		"idx_enrichment_run_items_run_outcome",
		"idx_acquisition_searches_rgid",
		"idx_acquisition_tasks_rgid",
		"idx_artist_merge_relation_moves_lookup",
		"idx_artist_merge_relation_moves_op",
		"idx_playback_sessions_track",
		"idx_artworks_track",
		"idx_daily_mix_play_tracks_track",
		"idx_track_artist_sources_artist",
		"idx_artist_alias_jobs_artist",
		"idx_artist_merge_operations_source",
		"idx_artist_merge_operations_target",
		"idx_artist_alias_candidates_alias",
	}
	for _, idx := range requiredIndexes {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name=?`, idx).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing index %s", idx)
		}
	}

	versionPlan := explainQueryPlan(t, s,
		`SELECT LOWER(TRIM(COALESCE(t.user_title,t.title))) FROM tracks t WHERE `+trackVisibleSQL("t.id")+` AND LOWER(TRIM(COALESCE(t.user_title,t.title))) IN (?,?) GROUP BY LOWER(TRIM(COALESCE(t.user_title,t.title))) HAVING COUNT(*) > 1`,
		"song a", "song b",
	)
	if !strings.Contains(versionPlan, "idx_tracks_norm_title") {
		t.Fatalf("version count query did not use idx_tracks_norm_title:\n%s", versionPlan)
	}

	deleteTrackPlan := explainQueryPlan(t, s, `DELETE FROM tracks WHERE id=?`, 1)
	for _, table := range []string{"playback_sessions", "artworks", "daily_mix_play_tracks"} {
		if strings.Contains(deleteTrackPlan, "SCAN "+table) {
			t.Fatalf("DELETE FROM tracks still scans %s:\n%s", table, deleteTrackPlan)
		}
	}

	deleteArtistPlan := explainQueryPlan(t, s, `DELETE FROM artists WHERE id=?`, 1)
	for _, table := range []string{"track_artist_sources", "artist_alias_jobs", "artist_merge_operations"} {
		if strings.Contains(deleteArtistPlan, "SCAN "+table) {
			t.Fatalf("DELETE FROM artists still scans %s:\n%s", table, deleteArtistPlan)
		}
	}

	deleteWorkPlan := explainQueryPlan(t, s, `DELETE FROM works WHERE id=?`, 1)
	if strings.Contains(deleteWorkPlan, "SCAN work_aliases") {
		t.Fatalf("DELETE FROM works still scans work_aliases:\n%s", deleteWorkPlan)
	}
}
