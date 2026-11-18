CREATE TABLE audio_file_identity_checks (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('primary', 'composer', 'lyricist', 'arranger', 'producer')),
    status TEXT NOT NULL CHECK (status IN ('verified', 'blocked')),
    PRIMARY KEY (audio_file_id, role)
) STRICT;

-- Record blocked role checks for every file that already participated in
-- identity tracking before cleaning up unprovable 074 rows, so legacy
-- single-artist fallback cannot resurrect unverified identities.
INSERT OR IGNORE INTO audio_file_identity_checks(audio_file_id, role, status)
    SELECT DISTINCT id.audio_file_id, r.role, 'blocked'
    FROM audio_file_artist_identities id
    CROSS JOIN (
        SELECT 'primary' AS role UNION ALL
        SELECT 'composer' UNION ALL
        SELECT 'lyricist' UNION ALL
        SELECT 'arranger' UNION ALL
        SELECT 'producer'
    ) r;

-- Purge pre-075 identity rows that cannot be strictly proven as an unambiguous
-- 1-to-1 single-artist-per-role match, removing false promotions and spurious
-- cross-role conflicts introduced by earlier 074 migrations.
DELETE FROM audio_file_artist_identities
WHERE status <> 'valid'
   OR trim(mbid) = ''
   OR NOT EXISTS (
       SELECT 1 FROM audio_files af
       JOIN track_artists ta ON ta.track_id = af.track_id
       WHERE af.id = audio_file_artist_identities.audio_file_id
         AND ta.artist_id = audio_file_artist_identities.artist_id
         AND ta.role = audio_file_artist_identities.role
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
             WHERE tag.audio_file_id = af.id
               AND lower(trim(tag.value)) = lower(trim(audio_file_artist_identities.mbid))
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
             WHERE tag2.audio_file_id = af.id
               AND trim(tag2.value) <> ''
               AND lower(trim(tag2.value)) <> lower(trim(audio_file_artist_identities.mbid))
               AND (
                   (ta.role = 'primary' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_ARTISTID', 'MUSICBRAINZ ARTIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARTISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARTIST ID'))
                   OR (ta.role = 'composer' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID'))
                   OR (ta.role = 'lyricist' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID'))
                   OR (ta.role = 'arranger' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID'))
                   OR (ta.role = 'producer' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'))
               )
         )
   );

INSERT OR REPLACE INTO audio_file_identity_checks(audio_file_id, role, status)
    SELECT DISTINCT audio_file_id, role, 'verified'
    FROM audio_file_artist_identities
    WHERE status = 'valid';

UPDATE artist_match_candidates
SET requires_manual_review = 1
WHERE instr(evidence_json, '标签身份与歌手名称不一致') > 0
   OR instr(evidence_json, '需要人工核对') > 0;
