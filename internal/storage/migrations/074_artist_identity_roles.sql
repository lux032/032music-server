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
    WHERE trim(old.mbid) <> ''
      AND ta.role IN ('primary', 'composer', 'lyricist', 'arranger', 'producer')
      AND (SELECT COUNT(*) FROM track_artists ta2 WHERE ta2.track_id = af.track_id AND ta2.role = ta.role) = 1
      AND NOT EXISTS (
          SELECT 1 FROM track_artist_sources s
          WHERE s.track_id = af.track_id AND s.role = ta.role AND s.source <> 'file_tag'
      )
      AND NOT EXISTS (
          SELECT 1 FROM artist_merge_relation_moves m
          JOIN artist_merge_operations op ON op.id = m.operation_id
          WHERE op.status = 'merged' AND m.relation_type = 'track'
            AND m.owner_id = af.track_id AND m.role = ta.role
      )
      AND EXISTS (
          SELECT 1 FROM audio_file_tags tag
          WHERE tag.audio_file_id = old.audio_file_id
            AND lower(trim(tag.value)) = lower(trim(old.mbid))
            AND (
                (ta.role = 'primary' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARTISTID', 'MUSICBRAINZ ARTIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARTISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARTIST ID'))
                OR (ta.role = 'composer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID'))
                OR (ta.role = 'lyricist' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID'))
                OR (ta.role = 'arranger' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID'))
                OR (ta.role = 'producer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'))
            )
      )
      AND NOT EXISTS (
          SELECT 1 FROM audio_file_tags tag2
          WHERE tag2.audio_file_id = old.audio_file_id
            AND trim(tag2.value) <> ''
            AND lower(trim(tag2.value)) <> lower(trim(old.mbid))
            AND (
                (ta.role = 'primary' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_ARTISTID', 'MUSICBRAINZ ARTIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARTISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARTIST ID'))
                OR (ta.role = 'composer' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID'))
                OR (ta.role = 'lyricist' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID'))
                OR (ta.role = 'arranger' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID'))
                OR (ta.role = 'producer' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'))
            )
      );

INSERT OR IGNORE INTO audio_file_artist_identities_new(audio_file_id, artist_id, role, mbid, status)
    SELECT DISTINCT old.audio_file_id, old.artist_id,
           CASE
               WHEN EXISTS (
                   SELECT 1 FROM audio_files af
                   JOIN track_artists ta ON ta.track_id = af.track_id AND ta.artist_id = old.artist_id
                   WHERE af.id = old.audio_file_id AND ta.role = 'primary'
               ) AND NOT EXISTS (
                   SELECT 1 FROM audio_files af
                   JOIN track_artists ta ON ta.track_id = af.track_id AND ta.artist_id = old.artist_id
                   WHERE af.id = old.audio_file_id AND ta.role <> 'primary'
               ) THEN 'primary'
               WHEN NOT EXISTS (
                   SELECT 1 FROM audio_files af
                   JOIN track_artists ta ON ta.track_id = af.track_id AND ta.artist_id = old.artist_id
                   WHERE af.id = old.audio_file_id
               ) AND NOT EXISTS (
                   SELECT 1 FROM audio_file_tags tc
                   WHERE tc.audio_file_id = old.audio_file_id
                     AND UPPER(tc.field_name) IN (
                         'MUSICBRAINZ_COMPOSERID','MUSICBRAINZ COMPOSER ID','----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID','----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID',
                         'MUSICBRAINZ_LYRICISTID','MUSICBRAINZ LYRICIST ID','----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID','----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID',
                         'MUSICBRAINZ_ARRANGERID','MUSICBRAINZ ARRANGER ID','----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID','----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID',
                         'MUSICBRAINZ_PRODUCERID','MUSICBRAINZ PRODUCER ID','----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID','----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'
                     )
               ) THEN 'primary'
               ELSE 'composer'
           END,
           '', 'conflict'
    FROM audio_file_artist_identities old
    WHERE NOT EXISTS (
        SELECT 1 FROM audio_file_artist_identities_new n
        WHERE n.audio_file_id = old.audio_file_id AND n.artist_id = old.artist_id
    );

DROP TABLE audio_file_artist_identities;
ALTER TABLE audio_file_artist_identities_new RENAME TO audio_file_artist_identities;

CREATE INDEX idx_file_artist_identity_artist ON audio_file_artist_identities(artist_id, role, status);
CREATE INDEX idx_file_artist_identity_file_role ON audio_file_artist_identities(audio_file_id, role);

ALTER TABLE artist_match_candidates ADD COLUMN requires_manual_review INTEGER NOT NULL DEFAULT 0 CHECK (requires_manual_review IN (0, 1));

UPDATE artist_match_candidates
SET requires_manual_review = 1
WHERE instr(evidence_json, '标签身份与歌手名称不一致') > 0
   OR instr(evidence_json, '需要人工核对') > 0;
