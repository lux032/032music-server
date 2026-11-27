package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigration095UpgradePreservesDataAndConstraints(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "migration095.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	// Upgrade up to 094 first, seed rows across rebuilt tables, then apply 095.
	if err = s.MigrateUpTo(ctx, 94); err != nil {
		t.Fatalf("migrate up to 94: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO libraries(id,name,root_path) VALUES(1,'lib','/tmp/lib')`); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO albums(id,library_id,title,sort_title,grouping_key) VALUES(1,1,'Album','album','k1')`); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO tracks(id,album_id,title,sort_title) VALUES(1,1,'Track','track')`); err != nil {
		t.Fatalf("seed track: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO audio_files(id,library_id,track_id,relative_path,file_size,modified_at_ns) VALUES(1,1,1,'a.flac',100,1)`); err != nil {
		t.Fatalf("seed audio_file: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO artists(id,display_name,sort_name,identity_key) VALUES(1,'Yuki Kajiura','yuki kajiura','yuki kajiura'),(2,'梶浦由記','梶浦由記','梶浦由記')`); err != nil {
		t.Fatalf("seed artists: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO audio_file_artist_identities(audio_file_id,artist_id,role,mbid,status) VALUES(1,1,'primary','11111111-1111-1111-1111-111111111111','valid')`); err != nil {
		t.Fatalf("seed audio_file_artist_identities: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO audio_file_identity_checks(audio_file_id,role,status) VALUES(1,'primary','verified')`); err != nil {
		t.Fatalf("seed audio_file_identity_checks: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE ai_review_settings SET enabled=1, artist_mode='recommend', credit_mode='auto' WHERE id=1`); err != nil {
		t.Fatalf("seed ai_review_settings: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO artist_merge_operations(source_artist_id,target_artist_id,status,source_name,target_name) VALUES(1,2,'merged','Yuki Kajiura','梶浦由記')`); err != nil {
		t.Fatalf("seed artist_merge_operations: %v", err)
	}

	// Run full migration (applying 095) and verify idempotency on repeat call.
	if err = s.Migrate(ctx); err != nil {
		t.Fatalf("migrate to latest: %v", err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migrate: %v", err)
	}

	// Verify preserved audio_file_artist_identities row and new album_artist role acceptance.
	var preservedMBID string
	if err = s.db.QueryRowContext(ctx, `SELECT mbid FROM audio_file_artist_identities WHERE audio_file_id=1 AND artist_id=1 AND role='primary'`).Scan(&preservedMBID); err != nil {
		t.Fatalf("read preserved primary identity: %v", err)
	}
	if preservedMBID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("unexpected preserved mbid: %q", preservedMBID)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO audio_file_artist_identities(audio_file_id,artist_id,role,mbid,status) VALUES(1,2,'album_artist','11111111-1111-1111-1111-111111111111','valid')`); err != nil {
		t.Fatalf("insert album_artist identity failed: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO audio_file_identity_checks(audio_file_id,role,status) VALUES(1,'album_artist','verified')`); err != nil {
		t.Fatalf("insert album_artist check failed: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO audio_file_artist_identities(audio_file_id,artist_id,role,mbid,status) VALUES(1,2,'invalid_role','11111111-1111-1111-1111-111111111111','valid')`); err == nil {
		t.Fatalf("expected CHECK constraint failure for invalid_role")
	}

	// Verify recreated indexes on audio_file_artist_identities.
	for _, idx := range []string{"idx_file_artist_identity_artist", "idx_file_artist_identity_file_role"} {
		var name string
		if err = s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx).Scan(&name); err != nil {
			t.Fatalf("expected index %s to exist: %v", idx, err)
		}
	}

	// Verify preserved ai_review_settings fields and default artist_merge_mode='auto'.
	rev, err := s.AIReviewSettings(ctx)
	if err != nil {
		t.Fatalf("read ai review settings: %v", err)
	}
	if !rev.Enabled || rev.ArtistMode != AIReviewModeRecommend || rev.CreditMode != AIReviewModeAuto || rev.ArtistMergeMode != AIReviewModeAuto {
		t.Fatalf("unexpected migrated ai review settings: %+v", rev)
	}

	// Verify trigger_source defaulted to 'manual' and rejects invalid trigger values.
	var trigger string
	if err = s.db.QueryRowContext(ctx, `SELECT trigger_source FROM artist_merge_operations WHERE source_artist_id=1`).Scan(&trigger); err != nil {
		t.Fatalf("read trigger_source: %v", err)
	}
	if trigger != "manual" {
		t.Fatalf("expected trigger_source='manual', got %q", trigger)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE artist_merge_operations SET trigger_source='bogus' WHERE source_artist_id=1`); err == nil {
		t.Fatalf("expected CHECK constraint failure for bogus trigger_source")
	}

	// Verify artists.original_display_name exists and artist_identity_settings initialized enabled=1.
	var origCount int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE original_display_name IS NULL`).Scan(&origCount); err != nil || origCount != 2 {
		t.Fatalf("expected 2 artists with NULL original_display_name, got count=%d err=%v", origCount, err)
	}
	idSettings, err := s.ArtistIdentitySettings(ctx)
	if err != nil {
		t.Fatalf("read artist identity settings: %v", err)
	}
	if !idSettings.CanonicalNativeNameEnabled {
		t.Fatalf("expected CanonicalNativeNameEnabled=true by default")
	}
}

func TestArtistIdentitySettingsPersistence(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)

	got, err := s.ArtistIdentitySettings(ctx)
	if err != nil {
		t.Fatalf("read default settings: %v", err)
	}
	if !got.CanonicalNativeNameEnabled {
		t.Fatalf("expected default CanonicalNativeNameEnabled=true")
	}

	if err = s.SaveArtistIdentitySettings(ctx, ArtistIdentitySettings{CanonicalNativeNameEnabled: false}); err != nil {
		t.Fatalf("save false: %v", err)
	}
	got, err = s.ArtistIdentitySettings(ctx)
	if err != nil {
		t.Fatalf("read saved false: %v", err)
	}
	if got.CanonicalNativeNameEnabled {
		t.Fatalf("expected CanonicalNativeNameEnabled=false after save")
	}
}

func TestAIReviewSettingsArtistMergeModeValidation(t *testing.T) {
	s, ctx := openEnrichmentTestStore(t)

	got, err := s.AIReviewSettings(ctx)
	if err != nil {
		t.Fatalf("read default ai settings: %v", err)
	}
	if got.ArtistMergeMode != AIReviewModeAuto {
		t.Fatalf("expected default ArtistMergeMode=auto, got %q", got.ArtistMergeMode)
	}

	got.Enabled = true
	got.ArtistMergeMode = AIReviewModeRecommend
	if err = s.SaveAIReviewSettings(ctx, got); err != nil {
		t.Fatalf("save recommend mode: %v", err)
	}
	reloaded, err := s.AIReviewSettings(ctx)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if reloaded.ArtistMergeMode != AIReviewModeRecommend || reloaded.ModeForArtistMerge() != AIReviewModeRecommend {
		t.Fatalf("unexpected reloaded ArtistMergeMode: %+v", reloaded)
	}

	got.ArtistMergeMode = "invalid_mode"
	if err = s.SaveAIReviewSettings(ctx, got); err == nil {
		t.Fatalf("expected validation error for invalid ArtistMergeMode")
	}
}
