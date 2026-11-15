-- Existing artists are intentionally not marked new. Only later INSERTs may
-- enter automatic completion; restarts/profile refreshes never enqueue stock.
CREATE TABLE artist_alias_new_artists (
 artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE
) STRICT;
CREATE TRIGGER artist_alias_observe_new AFTER INSERT ON artists BEGIN
 INSERT OR IGNORE INTO artist_alias_new_artists(artist_id) VALUES(NEW.id);
END;
ALTER TABLE artist_search_aliases ADD COLUMN source TEXT NOT NULL DEFAULT 'manual';
CREATE TABLE artist_alias_settings (
 id INTEGER PRIMARY KEY CHECK(id=1), enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN(0,1)),
 paused INTEGER NOT NULL DEFAULT 0 CHECK(paused IN(0,1)), deepseek_key TEXT NOT NULL DEFAULT '',
 search_limit INTEGER NOT NULL DEFAULT 2 CHECK(search_limit BETWEEN 1 AND 10),
 fetch_limit INTEGER NOT NULL DEFAULT 3 CHECK(fetch_limit BETWEEN 1 AND 20),
 model_limit INTEGER NOT NULL DEFAULT 2 CHECK(model_limit BETWEEN 1 AND 5),
 rpm INTEGER NOT NULL DEFAULT 6 CHECK(rpm BETWEEN 1 AND 60)
) STRICT;
INSERT INTO artist_alias_settings(id) VALUES(1);
CREATE TABLE artist_alias_jobs (
 id INTEGER PRIMARY KEY, artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
 owner_id INTEGER NOT NULL, mbid TEXT NOT NULL, origin TEXT NOT NULL CHECK(origin IN('manual','auto')),
 status TEXT NOT NULL DEFAULT 'queued' CHECK(status IN('queued','running','filled','missing','review','failed','skipped')),
 search_calls INTEGER NOT NULL DEFAULT 0, fetch_calls INTEGER NOT NULL DEFAULT 0, model_calls INTEGER NOT NULL DEFAULT 0,
 epoch INTEGER NOT NULL DEFAULT 0, detail TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;
CREATE UNIQUE INDEX artist_alias_jobs_active ON artist_alias_jobs(artist_id) WHERE status IN('queued','running');
CREATE INDEX artist_alias_jobs_queue ON artist_alias_jobs(status,id);
CREATE TABLE artist_alias_candidates (
 id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL REFERENCES artist_alias_jobs(id) ON DELETE CASCADE,
 artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
 owner_id INTEGER NOT NULL, mbid TEXT NOT NULL,
 value TEXT NOT NULL CHECK(length(trim(value)) BETWEEN 1 AND 200), original_value TEXT NOT NULL,
 language TEXT NOT NULL, evidence_json TEXT NOT NULL CHECK(json_valid(evidence_json)),
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN('pending','accepted','rejected')),
 alias_id INTEGER REFERENCES artist_search_aliases(id) ON DELETE SET NULL,
 reason TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT(strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 UNIQUE(artist_id,value COLLATE NOCASE)
) STRICT;
CREATE TABLE artist_alias_rejections (
 artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE, value TEXT NOT NULL COLLATE NOCASE,
 PRIMARY KEY(artist_id,value)
) STRICT;
-- Deletion is an explicit user decision, including deletion through older APIs.
CREATE TRIGGER artist_alias_remember_deletion BEFORE DELETE ON artist_search_aliases BEGIN
 INSERT OR IGNORE INTO artist_alias_rejections(artist_id,value) VALUES(OLD.artist_id,OLD.value);
 UPDATE artist_alias_candidates SET status='rejected' WHERE alias_id=OLD.id;
END;
CREATE TABLE ai_usage_alias_new (
 day TEXT NOT NULL, model TEXT NOT NULL,
 purpose TEXT NOT NULL CHECK(purpose IN('tag','review','sample','test','mix','translate','playlist','artist_alias')),
 requests INTEGER NOT NULL DEFAULT 0, prompt_tokens INTEGER NOT NULL DEFAULT 0,
 completion_tokens INTEGER NOT NULL DEFAULT 0, cached_tokens INTEGER NOT NULL DEFAULT 0,
 reasoning_tokens INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(day,model,purpose)
) STRICT;
INSERT INTO ai_usage_alias_new SELECT * FROM ai_usage;
DROP TABLE ai_usage;
ALTER TABLE ai_usage_alias_new RENAME TO ai_usage;
