CREATE TABLE discovery_release_groups (
 rgid TEXT PRIMARY KEY CHECK(length(rgid)=36 AND rgid=lower(rgid) AND substr(rgid,9,1)='-' AND substr(rgid,14,1)='-' AND substr(rgid,19,1)='-' AND substr(rgid,24,1)='-' AND length(replace(rgid,'-',''))=32 AND replace(rgid,'-','') NOT GLOB '*[^0-9a-f]*'),
 title TEXT NOT NULL, primary_type TEXT NOT NULL, secondary_types_json TEXT NOT NULL DEFAULT '[]',
 kind TEXT NOT NULL CHECK(kind IN ('Album','EP','Single','OST')),
 first_release_date TEXT NOT NULL DEFAULT '', disambiguation TEXT NOT NULL DEFAULT '', artist_credit TEXT NOT NULL DEFAULT '',
 cover_status TEXT NOT NULL DEFAULT 'unknown' CHECK(cover_status IN ('unknown','cached','missing','failed')),
 cover_ext TEXT NOT NULL DEFAULT '', cover_checked_at TEXT, fetched_at TEXT NOT NULL) STRICT;
CREATE TABLE discovery_seeds (
 artist_id INTEGER PRIMARY KEY REFERENCES artists(id) ON DELETE CASCADE, mbid TEXT NOT NULL, generation INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','done','failed','gone')), rg_count INTEGER NOT NULL DEFAULT 0, error_message TEXT NOT NULL DEFAULT '', fetched_at TEXT) STRICT;
CREATE TABLE discovery_artist_release_groups (artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE, rgid TEXT NOT NULL REFERENCES discovery_release_groups(rgid) ON DELETE CASCADE, PRIMARY KEY(artist_id,rgid)) STRICT;
CREATE INDEX idx_discovery_arg_rg ON discovery_artist_release_groups(rgid);
CREATE TABLE discovery_candidates (rgid TEXT PRIMARY KEY REFERENCES discovery_release_groups(rgid) ON DELETE CASCADE, seed_artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE, generation INTEGER NOT NULL, score REAL NOT NULL, lane TEXT NOT NULL CHECK(lane IN ('exploit','explore')), rank INTEGER, reasons_json TEXT NOT NULL) STRICT;
CREATE TABLE discovery_possible_matches (rgid TEXT NOT NULL REFERENCES discovery_release_groups(rgid) ON DELETE CASCADE, album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE, reason TEXT NOT NULL CHECK(reason IN ('title_artist','other_rg')), PRIMARY KEY(rgid,album_id)) STRICT;
CREATE TABLE discovery_match_rejections (rgid TEXT NOT NULL REFERENCES discovery_release_groups(rgid) ON DELETE CASCADE, album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE, PRIMARY KEY(rgid,album_id)) STRICT;
CREATE TABLE discovery_refresh_state (id INTEGER PRIMARY KEY CHECK(id=1), generation INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL CHECK(status IN ('idle','running','cancelled','completed','failed')), total INTEGER NOT NULL DEFAULT 0, processed INTEGER NOT NULL DEFAULT 0, error_message TEXT NOT NULL DEFAULT '', started_at TEXT, finished_at TEXT) STRICT;
INSERT INTO discovery_refresh_state(id,status) VALUES(1,'idle');

CREATE INDEX idx_discovery_matches_album ON discovery_possible_matches(album_id);
CREATE INDEX idx_discovery_rejections_album ON discovery_match_rejections(album_id);
CREATE INDEX idx_discovery_candidates_seed ON discovery_candidates(seed_artist_id);
