package storage

import (
	"strings"
	"testing"
)

func TestArtistSourceStateKeysCooldownAndToggle(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, id := seedEnrichmentEntities(t, s, ctx)
	input, err := s.ArtistForMatching(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	setting, err := s.MetadataSourceSetting(ctx, "lastfm")
	if err != nil {
		t.Fatal(err)
	}
	input, err = s.ArtistMatchQueryContext(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	setting.APIKey = "secret-one"
	setting.CacheDays = 30
	if err = s.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArtistSourceCheck(ctx, input, setting, ArtistSourceSnapshot{}); err != nil {
		t.Fatal(err)
	}
	check, err := s.ArtistSourceCheckNeeded(ctx, input, setting)
	if err != nil || check.Eligible {
		t.Fatalf("%+v %v", check, err)
	}
	var ik, ck, raw string
	if err = s.db.QueryRowContext(ctx, `SELECT input_key,config_key,snapshot_json FROM artist_source_match_state WHERE artist_id=?`, id).Scan(&ik, &ck, &raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ik+ck+raw, "secret-one") {
		t.Fatal("secret persisted")
	}
	setting.APIKey = "secret-two"
	_, newCK := ArtistMatchKeys(input, setting)
	if newCK != ck {
		t.Fatal("key rotation changed config hash")
	}
	renamed := input
	renamed.Name += " renamed"
	check, err = s.ArtistSourceCheckNeeded(ctx, renamed, setting)
	if err != nil || !check.Eligible {
		t.Fatal(check, err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artist_source_match_state SET checked_at='2000-01-01T00:00:00Z' WHERE artist_id=?`, id); err != nil {
		t.Fatal(err)
	}
	check, err = s.ArtistSourceCheckNeeded(ctx, input, setting)
	if err != nil || !check.Eligible {
		t.Fatal(check, err)
	}
	snapshot := ArtistSourceSnapshot{Candidates: []ArtistCandidate{{Source: "lastfm", ExternalID: "review", DisplayName: "Review", Score: 82}}}
	if err = s.SaveArtistSourceCheck(ctx, input, setting, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artist_source_match_state SET checked_at='2000-01-01T00:00:00Z' WHERE artist_id=?`, id); err != nil {
		t.Fatal(err)
	}
	check, err = s.ArtistSourceCheckNeeded(ctx, input, setting)
	if err != nil || check.Eligible || check.Snapshot != nil {
		t.Fatalf("expired review should stay stable but provide no evidence: %+v %v", check, err)
	}
	setting.Enabled = false
	if err = s.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	if err = s.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	check, err = s.ArtistSourceCheckNeeded(ctx, input, setting)
	if err != nil || !check.Eligible {
		t.Fatal(check, err)
	}
}

func TestArtistSourceStateAtomicFailureAndForeignKey(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, id := seedEnrichmentEntities(t, s, ctx)
	input, _ := s.ArtistForMatching(ctx, id)
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_artist_state BEFORE INSERT ON artist_source_match_state BEGIN SELECT RAISE(ABORT,'injected state write'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveArtistSourceCheck(ctx, input, setting, ArtistSourceSnapshot{Candidates: []ArtistCandidate{{Source: "musicbrainz", ExternalID: "x", DisplayName: "X"}}}); err == nil {
		t.Fatal("missing error")
	}
	candidates, _ := s.ArtistCandidates(ctx, id)
	if len(candidates) != 0 {
		t.Fatal("candidate escaped failed checkpoint", candidates)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER fail_artist_state`); err != nil {
		t.Fatal(err)
	}
	absent := input
	absent.ID = 999999
	if err := s.SaveArtistSourceCheck(ctx, absent, setting, ArtistSourceSnapshot{}); err == nil {
		t.Fatal("missing FK")
	}
}

func TestArtistRunOutcomeColumnsPreserveAndConserve(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	id, err := s.CreateArtistMatchRun(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateArtistMatchRunOutcomes(ctx, id, 1, 1, 1, 1, 1, "Artist"); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListArtistMatchRuns(ctx, 1)
	if err != nil || len(runs) != 1 {
		t.Fatal(err)
	}
	r := runs[0]
	if r.ID != id || r.Processed != r.Matched+r.Review+r.NoResult+r.Skipped+r.Failed || r.Processed != 5 {
		t.Fatal(r)
	}
}

func TestMigration036PreservesExistingRunRows(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER artist_source_state_setting_change; DROP TRIGGER artist_source_state_merge_change; DROP TABLE artist_source_match_state; ALTER TABLE artist_match_runs DROP COLUMN skipped_artists; ALTER TABLE artist_match_runs DROP COLUMN no_result_artists; INSERT INTO artist_match_runs(status,total_artists,processed_artists,matched_artists) VALUES('completed',3,2,2)`); err != nil {
		t.Fatal(err)
	}
	script, err := migrationFiles.ReadFile("migrations/036_artist_source_match_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, string(script)); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ListArtistMatchRuns(ctx, 1)
	if err != nil || len(runs) != 1 || runs[0].Matched != 2 || runs[0].Processed != 2 || runs[0].Skipped != 0 || runs[0].NoResult != 0 {
		t.Fatal(runs, err)
	}
}

func TestArtistSourceStateResetAndDelete(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, id := seedEnrichmentEntities(t, s, ctx)
	input, _ := s.ArtistForMatching(ctx, id)
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	if err := s.SaveArtistSourceCheck(ctx, input, setting, ArtistSourceSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertExternalArtistProfile(ctx, id, ExternalArtistProfile{Source: "musicbrainz", ExternalID: "reset", DisplayName: "Artist"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetArtistIdentity(ctx, id, "musicbrainz", "reset"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_source_match_state WHERE artist_id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Temporary','temporary','state-temp')`)
	if err != nil {
		t.Fatal(err)
	}
	temp, _ := r.LastInsertId()
	input.ID = temp
	if err := s.SaveArtistSourceCheck(ctx, input, setting, ArtistSourceSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM artists WHERE id=?`, temp); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_source_match_state WHERE artist_id=?`, temp).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestArtistSourceDynamicCacheDays(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)
	_, _, _, id := seedEnrichmentEntities(t, s, ctx)
	input, _ := s.ArtistForMatching(ctx, id)
	setting, _ := s.MetadataSourceSetting(ctx, "musicbrainz")
	setting.Enabled = true
	setting.AutoMatch = true
	setting.CacheDays = 30
	if err := s.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveArtistSourceCheck(ctx, input, setting, ArtistSourceSnapshot{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE artist_source_match_state SET checked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-2 days') WHERE artist_id=?`, id); err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{1, 30} {
		setting.CacheDays = days
		if err := s.SaveMetadataSourceSetting(ctx, setting); err != nil {
			t.Fatal(err)
		}
		check, err := s.ArtistSourceCheckNeeded(ctx, input, setting)
		if err != nil || check.Eligible != (days == 1) {
			t.Fatalf("days=%d %+v %v", days, check, err)
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artist_source_match_state WHERE artist_id=?`, id).Scan(&count); err != nil || count != 1 {
			t.Fatal(count, err)
		}
	}
}
