CREATE TABLE artist_source_match_state (
 artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
 source TEXT NOT NULL,
 input_key TEXT NOT NULL,
 config_key TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome IN ('review','no_result','matched','skipped_composite','error')),
 checked_at TEXT NOT NULL,
 next_check_at TEXT NOT NULL,
 snapshot_json TEXT NOT NULL DEFAULT '{}',
 PRIMARY KEY(artist_id,source)
) STRICT;
ALTER TABLE artist_match_runs ADD COLUMN skipped_artists INTEGER NOT NULL DEFAULT 0;
ALTER TABLE artist_match_runs ADD COLUMN no_result_artists INTEGER NOT NULL DEFAULT 0;
CREATE TRIGGER artist_source_state_setting_change AFTER UPDATE OF enabled,auto_match,language ON metadata_source_settings
WHEN OLD.enabled<>NEW.enabled OR OLD.auto_match<>NEW.auto_match OR OLD.language<>NEW.language
BEGIN DELETE FROM artist_source_match_state WHERE source=NEW.source; END;
CREATE TRIGGER artist_source_state_merge_change AFTER UPDATE OF merged_into_artist_id ON artists
WHEN OLD.merged_into_artist_id IS NOT NEW.merged_into_artist_id
BEGIN DELETE FROM artist_source_match_state WHERE artist_id=NEW.id; END;
