-- First-run setup wizard durable state and automatic upgrade detection.

CREATE TABLE setup_wizard_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'skipped', 'completed')),
    current_step INTEGER NOT NULL DEFAULT 1 CHECK (current_step BETWEEN 1 AND 5),
    step_states_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(step_states_json)),
    banner_dismissed INTEGER NOT NULL DEFAULT 0 CHECK (banner_dismissed IN (0, 1)),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO setup_wizard_state(id, status, current_step)
SELECT 1,
    CASE
        WHEN EXISTS (SELECT 1 FROM audio_files LIMIT 1)
          OR EXISTS (SELECT 1 FROM scan_jobs LIMIT 1)
          OR EXISTS (SELECT 1 FROM metadata_source_settings WHERE enabled = 1 OR COALESCE(api_key, '') <> '' LIMIT 1)
          OR EXISTS (SELECT 1 FROM ai_settings WHERE COALESCE(api_key, '') <> '' OR COALESCE(model, '') <> '' LIMIT 1)
          OR EXISTS (SELECT 1 FROM web_search_settings WHERE (tavily_keys_json <> '[]' AND tavily_keys_json <> '') OR deepseek_key <> '' LIMIT 1)
          OR EXISTS (SELECT 1 FROM acquisition_settings WHERE enabled = 1 OR jackett_api_key <> '' LIMIT 1)
          OR EXISTS (SELECT 1 FROM lastfm_scrobble_settings WHERE enabled = 1 OR session_key <> '' LIMIT 1)
          OR EXISTS (SELECT 1 FROM listenbrainz_settings WHERE enabled = 1 OR user_token <> '' LIMIT 1)
        THEN 'completed'
        ELSE 'pending'
    END,
    1;
