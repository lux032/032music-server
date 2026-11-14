-- Only explicit user-requested conversion creates these rows. A matching
-- source fingerprint keeps ordinary rescans from restoring the joined name.
CREATE TABLE legacy_creator_conversions (
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK(role IN ('composer','lyricist','arranger','producer')),
    source_hash TEXT NOT NULL,
    names_json TEXT NOT NULL,
    converted_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY(track_id,role)
) STRICT;
