CREATE TABLE audio_file_artist_identities_new (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT 'primary' CHECK (role IN ('primary', 'composer', 'lyricist', 'arranger', 'producer')),
    mbid TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'valid' CHECK (status IN ('valid', 'conflict')),
    PRIMARY KEY (audio_file_id, artist_id, role)
) STRICT;

INSERT INTO audio_file_artist_identities_new(audio_file_id, artist_id, role, mbid, status)
    SELECT DISTINCT old.audio_file_id, old.artist_id, ta.role, lower(old.mbid), 'valid'
    FROM audio_file_artist_identities old
    JOIN audio_files af ON af.id = old.audio_file_id
    JOIN track_artists ta ON ta.track_id = af.track_id AND ta.artist_id = old.artist_id
    WHERE EXISTS (
        SELECT 1 FROM audio_file_tags tag
        WHERE tag.audio_file_id = old.audio_file_id
          AND instr(lower(tag.value), lower(old.mbid)) > 0
          AND (
              (ta.role = 'primary' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARTISTID', 'MUSICBRAINZ ARTIST ID'))
              OR (ta.role = 'composer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID'))
              OR (ta.role = 'lyricist' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID'))
              OR (ta.role = 'arranger' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID'))
              OR (ta.role = 'producer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID'))
          )
    );

INSERT OR IGNORE INTO audio_file_artist_identities_new(audio_file_id, artist_id, role, mbid, status)
    SELECT DISTINCT old.audio_file_id, old.artist_id, ta.role, '', 'conflict'
    FROM audio_file_artist_identities old
    JOIN audio_files af ON af.id = old.audio_file_id
    JOIN track_artists ta ON ta.track_id = af.track_id AND ta.artist_id = old.artist_id
    WHERE ta.role IN ('primary', 'composer', 'lyricist', 'arranger', 'producer');

DROP TABLE audio_file_artist_identities;
ALTER TABLE audio_file_artist_identities_new RENAME TO audio_file_artist_identities;

CREATE INDEX idx_file_artist_identity_artist ON audio_file_artist_identities(artist_id, role, status);
CREATE INDEX idx_file_artist_identity_file_role ON audio_file_artist_identities(audio_file_id, role);

ALTER TABLE artist_match_candidates ADD COLUMN requires_manual_review INTEGER NOT NULL DEFAULT 0 CHECK (requires_manual_review IN (0, 1));

UPDATE artist_match_candidates
SET requires_manual_review = 1
WHERE instr(evidence_json, '标签身份与歌手名称不一致') > 0
   OR instr(evidence_json, '需要人工核对') > 0;
