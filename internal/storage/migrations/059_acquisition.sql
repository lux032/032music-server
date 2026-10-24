CREATE TABLE acquisition_settings (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
 jackett_url TEXT NOT NULL DEFAULT '', jackett_api_key TEXT NOT NULL DEFAULT '',
 jackett_indexer TEXT NOT NULL DEFAULT 'jpopsuki',
 qb_url TEXT NOT NULL DEFAULT '', qb_username TEXT NOT NULL DEFAULT '', qb_password TEXT NOT NULL DEFAULT '',
 qb_category TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;
INSERT INTO acquisition_settings(id) VALUES(1);

CREATE TABLE acquisition_searches (
 id TEXT PRIMARY KEY,
 rgid TEXT NOT NULL, indexer TEXT NOT NULL, query TEXT NOT NULL, query_key TEXT NOT NULL,
 result_count INTEGER NOT NULL,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;
CREATE INDEX idx_acq_search_key ON acquisition_searches(indexer, query_key, created_at);
CREATE TABLE acquisition_search_results (
 search_id TEXT NOT NULL REFERENCES acquisition_searches(id) ON DELETE CASCADE,
 idx INTEGER NOT NULL, title TEXT NOT NULL, category TEXT NOT NULL DEFAULT '',
 size INTEGER NOT NULL DEFAULT 0, seeders INTEGER, leechers INTEGER, grabs INTEGER,
 download_factor REAL, published_at TEXT NOT NULL DEFAULT '',
 download_ref TEXT NOT NULL DEFAULT '',
 magnet_only INTEGER NOT NULL DEFAULT 0 CHECK(magnet_only IN (0,1)),
 PRIMARY KEY(search_id, idx)) STRICT;

CREATE TABLE acquisition_tasks (
 id INTEGER PRIMARY KEY,
 public_id TEXT NOT NULL UNIQUE CHECK(length(public_id)=36 AND public_id=lower(public_id) AND substr(public_id,9,1)='-' AND substr(public_id,14,1)='-' AND substr(public_id,19,1)='-' AND substr(public_id,24,1)='-' AND length(replace(public_id,'-',''))=32 AND replace(public_id,'-','') NOT GLOB '*[^0-9a-f]*'),
 idempotency_key TEXT NOT NULL UNIQUE,
 rgid TEXT NOT NULL CHECK(length(rgid)=36 AND rgid=lower(rgid) AND substr(rgid,9,1)='-' AND substr(rgid,14,1)='-' AND substr(rgid,19,1)='-' AND substr(rgid,24,1)='-' AND length(replace(rgid,'-',''))=32 AND replace(rgid,'-','') NOT GLOB '*[^0-9a-f]*'),
 title TEXT NOT NULL, artist TEXT NOT NULL, kind TEXT NOT NULL,
 torrent_title TEXT NOT NULL, torrent_size INTEGER NOT NULL DEFAULT 0,
 info_hash TEXT CHECK(info_hash IS NULL OR (length(info_hash)=40 AND info_hash NOT GLOB '*[^0-9a-f]*')),
 download_ref TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN ('QB_SUBMITTING','DOWNLOADING','AWAITING_LIBRARY','IN_LIBRARY','ATTENTION','CANCELLED')),
 attention_reason TEXT NOT NULL DEFAULT '' CHECK(attention_reason IN ('','qb_unreachable','qb_auth','qb_rejected','qb_missing','torrent_unavailable','library_timeout','rg_conflict','preexisting_only','no_rg_files','rule_suppressed')),
 attention_detail TEXT NOT NULL DEFAULT '',
 submit_attempts INTEGER NOT NULL DEFAULT 0,
 add_outcome TEXT NOT NULL DEFAULT '' CHECK(add_outcome IN ('','ok','rejected','unknown')),
 next_check_at TEXT, qb_seen_at TEXT, qb_missing_since TEXT, download_completed_at TEXT,
 add_unknown_since TEXT, qb_unreachable_since TEXT,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 finished_at TEXT,
 version INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE UNIQUE INDEX idx_acq_active_rg ON acquisition_tasks(rgid)
 WHERE state IN ('QB_SUBMITTING','DOWNLOADING','AWAITING_LIBRARY','ATTENTION');
CREATE UNIQUE INDEX idx_acq_active_hash ON acquisition_tasks(info_hash)
 WHERE info_hash IS NOT NULL AND state IN ('QB_SUBMITTING','DOWNLOADING','AWAITING_LIBRARY','ATTENTION');
CREATE INDEX idx_acq_state ON acquisition_tasks(state, next_check_at);

CREATE TABLE acquisition_evidence (
 task_id INTEGER PRIMARY KEY REFERENCES acquisition_tasks(id) ON DELETE CASCADE,
 source TEXT NOT NULL CHECK(source IN ('file','manual','owned')),
 album_id INTEGER REFERENCES albums(id) ON DELETE SET NULL,
 evidence_audio_file_id INTEGER REFERENCES audio_files(id) ON DELETE SET NULL,
 detected_count INTEGER NOT NULL DEFAULT 0,
 verified_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;

CREATE INDEX idx_acq_available_file_created ON audio_files(created_at) WHERE status='available';
