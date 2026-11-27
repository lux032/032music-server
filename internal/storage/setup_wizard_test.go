package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSetupWizardFreshSeedAndTransitions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var integrity string
	if err := store.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q, err = %v", integrity, err)
	}
	rows, err := store.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check returned violations")
	}

	st, err := store.SetupWizardState(ctx)
	if err != nil {
		t.Fatalf("SetupWizardState: %v", err)
	}
	if !st.IsPending() || st.CurrentStep != 1 || st.BannerDismissed || st.ShowResumeBanner() {
		t.Fatalf("unexpected fresh state: %+v", st)
	}
	if st.CompletedConfigSteps() != 0 {
		t.Fatalf("CompletedConfigSteps = %d, want 0", st.CompletedConfigSteps())
	}

	// Step 1 done -> advances to step 2.
	st, err = store.SaveSetupWizardStep(ctx, 1, SetupStepDone, 2)
	if err != nil {
		t.Fatalf("SaveSetupWizardStep(1): %v", err)
	}
	if st.Status != SetupStatusPending || st.CurrentStep != 2 || st.StepStates[1] != SetupStepDone {
		t.Fatalf("after step 1: %+v", st)
	}
	if st.CompletedConfigSteps() != 1 {
		t.Fatalf("CompletedConfigSteps = %d, want 1", st.CompletedConfigSteps())
	}

	// Step 2 skipped -> advances to step 3.
	st, err = store.SaveSetupWizardStep(ctx, 2, SetupStepSkipped, 3)
	if err != nil {
		t.Fatalf("SaveSetupWizardStep(2): %v", err)
	}
	if st.StepStates[2] != SetupStepSkipped || st.CurrentStep != 3 {
		t.Fatalf("after step 2 skip: %+v", st)
	}

	// Skip wizard -> status=skipped and banner visible.
	st, err = store.SkipSetupWizard(ctx)
	if err != nil {
		t.Fatalf("SkipSetupWizard: %v", err)
	}
	if st.Status != SetupStatusSkipped || !st.ShowResumeBanner() {
		t.Fatalf("after skip: %+v", st)
	}

	// Dismiss banner -> persists across reads.
	if err := store.DismissSetupWizardBanner(ctx); err != nil {
		t.Fatalf("DismissSetupWizardBanner: %v", err)
	}
	st, err = store.SetupWizardState(ctx)
	if err != nil {
		t.Fatalf("SetupWizardState after dismiss: %v", err)
	}
	if !st.BannerDismissed || st.ShowResumeBanner() || st.Status != SetupStatusSkipped {
		t.Fatalf("after dismiss: %+v", st)
	}

	// Complete wizard -> status=completed; subsequent SkipSetupWizard must keep completed.
	st, err = store.CompleteSetupWizard(ctx)
	if err != nil {
		t.Fatalf("CompleteSetupWizard: %v", err)
	}
	if st.Status != SetupStatusCompleted || st.StepStates[5] != SetupStepDone || st.CurrentStep != 5 || st.ShowResumeBanner() {
		t.Fatalf("after complete: %+v", st)
	}

	st, err = store.SkipSetupWizard(ctx)
	if err != nil {
		t.Fatalf("SkipSetupWizard after complete: %v", err)
	}
	if st.Status != SetupStatusCompleted {
		t.Fatalf("completed status downgraded on skip: %+v", st)
	}
}

func TestSetupWizardUpgradeDetectionSignals(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		seed func(t *testing.T, s *Store)
	}{
		{
			name: "existing_scan_job",
			seed: func(t *testing.T, s *Store) {
				if err := s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
					t.Fatalf("EnsureLibrary: %v", err)
				}
				lib, err := s.LibraryByRoot(ctx, "/music")
				if err != nil {
					t.Fatalf("LibraryByRoot: %v", err)
				}
				if _, err := s.db.ExecContext(ctx, `INSERT INTO scan_jobs(library_id, scan_type, status) VALUES(?, 'incremental', 'completed')`, lib.ID); err != nil {
					t.Fatalf("insert scan_jobs: %v", err)
				}
			},
		},
		{
			name: "enabled_metadata_source",
			seed: func(t *testing.T, s *Store) {
				if _, err := s.db.ExecContext(ctx, `UPDATE metadata_source_settings SET enabled = 1 WHERE source = 'bangumi'`); err != nil {
					t.Fatalf("update metadata_source_settings: %v", err)
				}
			},
		},
		{
			name: "configured_ai_settings",
			seed: func(t *testing.T, s *Store) {
				if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO ai_settings(id, base_url, api_key, model) VALUES(1, 'https://api.openai.com/v1', 'sk-test', 'gpt-4o-mini')`); err != nil {
					t.Fatalf("insert ai_settings: %v", err)
				}
			},
		},
		{
			name: "configured_web_search",
			seed: func(t *testing.T, s *Store) {
				if _, err := s.db.ExecContext(ctx, `UPDATE web_search_settings SET tavily_keys_json = '["tvly-123"]' WHERE id = 1`); err != nil {
					t.Fatalf("update web_search_settings: %v", err)
				}
			},
		},
		{
			name: "configured_listenbrainz",
			seed: func(t *testing.T, s *Store) {
				if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO listenbrainz_settings(id, enabled, user_token) VALUES(1, 1, 'tok')`); err != nil {
					t.Fatalf("insert listenbrainz_settings: %v", err)
				}
			},
		},
		{
			name: "configured_acquisition",
			seed: func(t *testing.T, s *Store) {
				if _, err := s.db.ExecContext(ctx, `UPDATE acquisition_settings SET enabled = 1, jackett_api_key = 'jk-1' WHERE id = 1`); err != nil {
					t.Fatalf("update acquisition_settings: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "upgrade.db")
			s, err := Open(dbPath)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = s.Close() })
			if err := s.Migrate(ctx); err != nil {
				t.Fatalf("Migrate: %v", err)
			}
			// Rewind migration 094 so we can seed pre-094 state and re-run Migrate.
			if _, err := s.db.ExecContext(ctx, `DROP TABLE setup_wizard_state; DELETE FROM schema_migrations WHERE version = 94`); err != nil {
				t.Fatalf("rewind 094: %v", err)
			}
			tc.seed(t, s)
			if err := s.Migrate(ctx); err != nil {
				t.Fatalf("re-Migrate 094: %v", err)
			}
			st, err := s.SetupWizardState(ctx)
			if err != nil {
				t.Fatalf("SetupWizardState: %v", err)
			}
			if st.Status != SetupStatusCompleted {
				t.Fatalf("upgrade status = %q, want completed", st.Status)
			}
		})
	}
}
