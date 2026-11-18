CREATE TABLE audio_file_artist_identities_new (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'primary' CHECK (role IN ('primary', 'composer', 'lyricist', 'arranger', 'producer')),
    mbid TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'valid' CHECK (status IN ('valid', 'conflict')),
    PRIMARY KEY (audio_file_id, artist_id, role)
) STRICT;

INSERT INTO audio_file_artist_identities_new(audio_file_id, artist_id, role, mbid, status)
    SELECT audio_file_id, artist_id, 'primary', mbid, 'valid' FROM audio_file_artist_identities;

DROP TABLE audio_file_artist_identities;
ALTER TABLE audio_file_artist_identities_new RENAME TO audio_file_artist_identities;

CREATE INDEX idx_file_artist_identity_artist ON audio_file_artist_identities(artist_id, role, status);
CREATE INDEX idx_file_artist_identity_file_role ON audio_file_artist_identities(audio_file_id, role);

ALTER TABLE artist_match_candidates ADD COLUMN requires_manual_review INTEGER NOT NULL DEFAULT 0 CHECK (requires_manual_review IN (0, 1));
