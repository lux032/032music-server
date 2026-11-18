-- Add 'rejected' outcome to artist_source_match_state, backfill existing
-- all-rejected artists, and record scan_mode on artist_match_runs so
-- incremental vs full rescan runs remain distinguishable across restarts.

CREATE TABLE artist_source_match_state_new (
 artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
 source TEXT NOT NULL,
 input_key TEXT NOT NULL,
 config_key TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome IN ('review','no_result','matched','skipped_composite','rejected','error')),
 checked_at TEXT NOT NULL,
 next_check_at TEXT NOT NULL,
 snapshot_json TEXT NOT NULL DEFAULT '{}',
 PRIMARY KEY(artist_id,source)
) STRICT;

INSERT INTO artist_source_match_state_new(artist_id,source,input_key,config_key,outcome,checked_at,next_check_at,snapshot_json)
 SELECT artist_id,source,input_key,config_key,outcome,checked_at,next_check_at,snapshot_json FROM artist_source_match_state;

DROP TRIGGER IF EXISTS artist_source_state_setting_change;
DROP TRIGGER IF EXISTS artist_source_state_merge_change;
DROP TABLE artist_source_match_state;
ALTER TABLE artist_source_match_state_new RENAME TO artist_source_match_state;

CREATE TRIGGER artist_source_state_setting_change AFTER UPDATE OF enabled,auto_match,language ON metadata_source_settings
WHEN OLD.enabled<>NEW.enabled OR OLD.auto_match<>NEW.auto_match OR OLD.language<>NEW.language
BEGIN DELETE FROM artist_source_match_state WHERE source=NEW.source; END;

CREATE TRIGGER artist_source_state_merge_change AFTER UPDATE OF merged_into_artist_id ON artists
WHEN OLD.merged_into_artist_id IS NOT NEW.merged_into_artist_id
BEGIN DELETE FROM artist_source_match_state WHERE artist_id=NEW.id; END;

UPDATE artist_source_match_state
SET outcome='rejected'
WHERE outcome='review'
  AND NOT EXISTS(
    SELECT 1 FROM artist_match_candidates c
    WHERE c.artist_id=artist_source_match_state.artist_id
      AND c.source=artist_source_match_state.source
      AND c.status='candidate'
  )
  AND EXISTS(
    SELECT 1 FROM artist_match_candidates c
    WHERE c.artist_id=artist_source_match_state.artist_id
      AND c.source=artist_source_match_state.source
      AND c.status='rejected'
  );

CREATE TABLE artist_match_run_modes (
 run_id INTEGER PRIMARY KEY REFERENCES artist_match_runs(id) ON DELETE CASCADE,
 scan_mode TEXT NOT NULL DEFAULT 'incremental' CHECK(scan_mode IN ('incremental','full'))
) STRICT;
