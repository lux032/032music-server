-- Promote single-artist creator roles on pre-074/075 files where 069/073
-- could only persist one primary identity row per (file, artist), provided the
-- creator role is single-artist, untouched by TIPL/remote sources or merges,
-- has a single unambiguous 36-char UUID MBID tag, no conflict row, and exact
-- single-name tag alignment with the linked artist's display name.
INSERT OR IGNORE INTO audio_file_artist_identities(audio_file_id, artist_id, role, mbid, status)
    SELECT chk.audio_file_id,
           ta.artist_id,
           chk.role,
           lower(trim(tag.value)),
           'valid'
    FROM audio_file_identity_checks chk
    JOIN audio_files af ON af.id = chk.audio_file_id
    JOIN track_artists ta ON ta.track_id = af.track_id AND ta.role = chk.role
    JOIN artists a ON a.id = ta.artist_id
    JOIN audio_file_tags tag ON tag.audio_file_id = chk.audio_file_id
    WHERE chk.status = 'blocked'
      AND chk.role IN ('composer', 'lyricist', 'arranger', 'producer')
      AND (SELECT COUNT(*) FROM track_artists ta2 WHERE ta2.track_id = af.track_id AND ta2.role = ta.role) = 1
      AND NOT EXISTS (
          SELECT 1 FROM track_artist_sources s
          WHERE s.track_id = af.track_id AND s.role = ta.role
      )
      AND NOT EXISTS (
          SELECT 1 FROM artist_merge_relation_moves m
          JOIN artist_merge_operations op ON op.id = m.operation_id
          WHERE op.status = 'merged' AND m.relation_type = 'track'
            AND m.owner_id = af.track_id AND m.role = ta.role
      )
      AND NOT EXISTS (
          SELECT 1 FROM audio_file_artist_identities c
          WHERE c.audio_file_id = chk.audio_file_id
            AND c.artist_id = ta.artist_id
            AND c.role = chk.role
      )
      AND trim(tag.value) LIKE '________-____-____-____-____________'
      AND (
          (ta.role = 'composer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID'))
          OR (ta.role = 'lyricist' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID'))
          OR (ta.role = 'arranger' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID'))
          OR (ta.role = 'producer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'))
      )
      AND NOT EXISTS (
          SELECT 1 FROM audio_file_tags tag2
          WHERE tag2.audio_file_id = chk.audio_file_id
            AND trim(tag2.value) <> ''
            AND lower(trim(tag2.value)) <> lower(trim(tag.value))
            AND (
                (ta.role = 'composer' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID'))
                OR (ta.role = 'lyricist' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID'))
                OR (ta.role = 'arranger' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID'))
                OR (ta.role = 'producer' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'))
            )
      )
      AND EXISTS (
          SELECT 1 FROM audio_file_tags nt
          WHERE nt.audio_file_id = chk.audio_file_id
            AND lower(trim(nt.value)) = lower(trim(a.display_name))
            AND (
                (ta.role = 'composer' AND UPPER(nt.field_name) IN ('COMPOSER', 'COMPOSERS', 'TCOM'))
                OR (ta.role = 'lyricist' AND UPPER(nt.field_name) IN ('LYRICIST', 'LYRICISTS', 'TEXT'))
                OR (ta.role = 'arranger' AND UPPER(nt.field_name) IN ('ARRANGER', 'ARRANGERS'))
                OR (ta.role = 'producer' AND UPPER(nt.field_name) IN ('PRODUCER', 'PRODUCERS'))
            )
      )
      AND NOT EXISTS (
          SELECT 1 FROM audio_file_tags nt2
          WHERE nt2.audio_file_id = chk.audio_file_id
            AND trim(nt2.value) <> ''
            AND lower(trim(nt2.value)) <> lower(trim(a.display_name))
            AND (
                (ta.role = 'composer' AND UPPER(nt2.field_name) IN ('COMPOSER', 'COMPOSERS', 'TCOM'))
                OR (ta.role = 'lyricist' AND UPPER(nt2.field_name) IN ('LYRICIST', 'LYRICISTS', 'TEXT'))
                OR (ta.role = 'arranger' AND UPPER(nt2.field_name) IN ('ARRANGER', 'ARRANGERS'))
                OR (ta.role = 'producer' AND UPPER(nt2.field_name) IN ('PRODUCER', 'PRODUCERS'))
            )
      );

UPDATE audio_file_identity_checks
SET status = 'verified'
WHERE status = 'blocked'
  AND role IN ('composer', 'lyricist', 'arranger', 'producer')
  AND EXISTS (
      SELECT 1 FROM audio_file_artist_identities id
      WHERE id.audio_file_id = audio_file_identity_checks.audio_file_id
        AND id.role = audio_file_identity_checks.role
        AND id.status = 'valid'
  );

-- Recompute reverify markers without the unsafe cross-role instr() exemption
-- from 0c80f9c's 076, covering both DBs that already executed 0c80f9c and
-- creator roles promoted above so primary override isolates only primary.
INSERT OR REPLACE INTO audio_file_artist_reverify(audio_file_id, artist_id, role)
    SELECT chk.audio_file_id,
           ta.artist_id,
           CASE
               WHEN COUNT(DISTINCT chk.role) = 1 THEN MAX(chk.role)
               ELSE ''
           END
    FROM audio_file_identity_checks chk
    JOIN audio_files af ON af.id = chk.audio_file_id
    JOIN track_artists ta ON ta.track_id = af.track_id AND ta.role = chk.role
    WHERE chk.status = 'blocked'
      AND EXISTS (
          SELECT 1 FROM audio_file_tags tag
          WHERE tag.audio_file_id = chk.audio_file_id
            AND trim(tag.value) <> ''
            AND (
                (chk.role = 'primary' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARTISTID', 'MUSICBRAINZ ARTIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARTISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARTIST ID'))
                OR (chk.role = 'composer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID'))
                OR (chk.role = 'lyricist' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID'))
                OR (chk.role = 'arranger' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID'))
                OR (chk.role = 'producer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'))
            )
      )
      AND NOT EXISTS (
          SELECT 1 FROM audio_file_artist_identities cur
          WHERE cur.audio_file_id = chk.audio_file_id
            AND cur.artist_id = ta.artist_id
            AND cur.role = chk.role
            AND cur.status = 'valid'
      )
    GROUP BY chk.audio_file_id, ta.artist_id;

DELETE FROM audio_file_artist_reverify
WHERE NOT EXISTS (
    SELECT 1
    FROM audio_file_identity_checks chk
    JOIN audio_files af ON af.id = chk.audio_file_id
    JOIN track_artists ta ON ta.track_id = af.track_id AND ta.role = chk.role
    WHERE chk.audio_file_id = audio_file_artist_reverify.audio_file_id
      AND ta.artist_id = audio_file_artist_reverify.artist_id
      AND chk.status = 'blocked'
      AND EXISTS (
          SELECT 1 FROM audio_file_tags tag
          WHERE tag.audio_file_id = chk.audio_file_id
            AND trim(tag.value) <> ''
            AND (
                (chk.role = 'primary' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARTISTID', 'MUSICBRAINZ ARTIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARTISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARTIST ID'))
                OR (chk.role = 'composer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID'))
                OR (chk.role = 'lyricist' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID'))
                OR (chk.role = 'arranger' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID'))
                OR (chk.role = 'producer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'))
            )
      )
      AND NOT EXISTS (
          SELECT 1 FROM audio_file_artist_identities cur
          WHERE cur.audio_file_id = chk.audio_file_id
            AND cur.artist_id = ta.artist_id
            AND cur.role = chk.role
            AND cur.status = 'valid'
      )
);
