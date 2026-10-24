CREATE TABLE release_groups (
 rgid TEXT PRIMARY KEY CHECK(length(rgid)=36 AND rgid=lower(rgid) AND substr(rgid,9,1)='-' AND substr(rgid,14,1)='-' AND substr(rgid,19,1)='-' AND substr(rgid,24,1)='-' AND length(replace(rgid,'-',''))=32 AND replace(rgid,'-','') NOT GLOB '*[^0-9a-f]*'),
 title TEXT,
 primary_type TEXT,
 first_release_date TEXT,
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;
CREATE TABLE audio_file_release_groups (
 audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
 rgid TEXT NOT NULL REFERENCES release_groups(rgid),
 PRIMARY KEY(audio_file_id,rgid)
) STRICT;
CREATE INDEX idx_audio_file_rg ON audio_file_release_groups(rgid,audio_file_id);
CREATE TABLE audio_file_releases (
 audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
 release_id TEXT NOT NULL CHECK(length(release_id)=36 AND release_id=lower(release_id) AND substr(release_id,9,1)='-' AND substr(release_id,14,1)='-' AND substr(release_id,19,1)='-' AND substr(release_id,24,1)='-' AND length(replace(release_id,'-',''))=32 AND replace(release_id,'-','') NOT GLOB '*[^0-9a-f]*'),
 PRIMARY KEY(audio_file_id,release_id)
) STRICT;
CREATE TABLE album_release_groups (
 album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
 rgid TEXT NOT NULL REFERENCES release_groups(rgid),
 source TEXT NOT NULL CHECK(source IN ('file','manual')),
 status TEXT NOT NULL CHECK(status IN ('resolved','conflict','observed')),
 PRIMARY KEY(album_id,rgid,source)
) STRICT;
CREATE UNIQUE INDEX idx_album_rg_resolved ON album_release_groups(album_id) WHERE status='resolved';
CREATE INDEX idx_album_rg ON album_release_groups(rgid,status);
CREATE TABLE release_group_decisions (
 rgid TEXT PRIMARY KEY CHECK(length(rgid)=36 AND rgid=lower(rgid) AND substr(rgid,9,1)='-' AND substr(rgid,14,1)='-' AND substr(rgid,19,1)='-' AND substr(rgid,24,1)='-' AND length(replace(rgid,'-',''))=32 AND replace(rgid,'-','') NOT GLOB '*[^0-9a-f]*'),
 decision TEXT NOT NULL CHECK(decision='ignore'),
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;
CREATE TABLE release_group_rebuild (
 id INTEGER PRIMARY KEY CHECK(id=1),
 status TEXT NOT NULL CHECK(status IN ('not_ready','rebuilding','ready','error')),
 scan_job_id INTEGER REFERENCES scan_jobs(id) ON DELETE SET NULL,
 error_message TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
) STRICT;
INSERT INTO release_group_rebuild(id,status) VALUES(1,'not_ready');
