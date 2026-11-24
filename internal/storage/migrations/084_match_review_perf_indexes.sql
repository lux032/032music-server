-- Optimize /admin/matches review queries, composite credit lookup, and durable run progress counters.
CREATE INDEX IF NOT EXISTS idx_artist_match_candidates_status_artist
    ON artist_match_candidates(status, artist_id, source);
CREATE INDEX IF NOT EXISTS idx_artist_source_match_state_source_outcome
    ON artist_source_match_state(source, outcome, artist_id);
CREATE INDEX IF NOT EXISTS idx_artist_match_run_items_run_outcome
    ON artist_match_run_items(run_id, outcome);
CREATE INDEX IF NOT EXISTS idx_artist_image_backfill_items_run_outcome
    ON artist_image_backfill_items(run_id, outcome);
CREATE INDEX IF NOT EXISTS idx_artist_biography_backfill_items_run_outcome
    ON artist_biography_backfill_items(run_id, outcome);
