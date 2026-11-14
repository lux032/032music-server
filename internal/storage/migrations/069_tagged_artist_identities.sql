CREATE TABLE audio_file_artist_identities (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    mbid TEXT NOT NULL,
    PRIMARY KEY(audio_file_id,artist_id)
) STRICT;
CREATE INDEX idx_file_artist_identity_artist ON audio_file_artist_identities(artist_id);
