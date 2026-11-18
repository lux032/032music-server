CREATE TABLE audio_file_identity_checks (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('primary', 'composer', 'lyricist', 'arranger', 'producer')),
    status TEXT NOT NULL CHECK (status IN ('verified', 'blocked')),
    PRIMARY KEY (audio_file_id, role)
) STRICT;

-- Preserve pending re-verification markers per (audio_file_id, artist_id) for
-- pre-075 evidence rows that cannot be strictly proven during migration, so
-- cross-file identity conflicts continue to block auto-bind until rescan.
CREATE TABLE audio_file_artist_reverify (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT '' CHECK (role IN ('', 'primary', 'composer', 'lyricist', 'arranger', 'producer')),
    PRIMARY KEY (audio_file_id, artist_id)
) STRICT;

CREATE INDEX idx_file_artist_reverify_artist ON audio_file_artist_reverify(artist_id);

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

-- Mark strictly provable 1-to-1 single-artist-per-role rows as verified.
INSERT OR REPLACE INTO audio_file_identity_checks(audio_file_id, role, status)
    SELECT DISTINCT id.audio_file_id, id.role, 'verified'
    FROM audio_file_artist_identities id
    JOIN audio_files af ON af.id = id.audio_file_id
    JOIN track_artists ta ON ta.track_id = af.track_id AND ta.artist_id = id.artist_id AND ta.role = id.role
    WHERE id.status = 'valid'
      AND trim(id.mbid) <> ''
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
            AND lower(trim(tag.value)) = lower(trim(id.mbid))
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
            AND lower(trim(tag2.value)) <> lower(trim(id.mbid))
            AND (
                (ta.role = 'primary' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_ARTISTID', 'MUSICBRAINZ ARTIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARTISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARTIST ID'))
                OR (ta.role = 'composer' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_COMPOSERID', 'MUSICBRAINZ COMPOSER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_COMPOSERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ COMPOSER ID'))
                OR (ta.role = 'lyricist' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_LYRICISTID', 'MUSICBRAINZ LYRICIST ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_LYRICISTID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ LYRICIST ID'))
                OR (ta.role = 'arranger' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_ARRANGERID', 'MUSICBRAINZ ARRANGER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_ARRANGERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ ARRANGER ID'))
                OR (ta.role = 'producer' AND UPPER(tag2.field_name) IN ('MUSICBRAINZ_PRODUCERID', 'MUSICBRAINZ PRODUCER ID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ_PRODUCERID', '----:ORG.MUSICBRAINZ.ITUNES:MUSICBRAINZ PRODUCER ID'))
            )
      );

-- Record pending re-verification for unprovable rows unless another role on the
-- same (file, artist) already proved the exact same MBID cleanly without
-- conflicting non-empty MBIDs.
INSERT OR IGNORE INTO audio_file_artist_reverify(audio_file_id, artist_id, role)
    SELECT cur.audio_file_id,
           cur.artist_id,
           CASE
               WHEN COUNT(DISTINCT CASE WHEN cur.status = 'valid' THEN cur.role END) = 1
               THEN MAX(CASE WHEN cur.status = 'valid' THEN cur.role END)
               WHEN COUNT(DISTINCT CASE WHEN cur.status = 'valid' THEN cur.role END) = 0
                AND COUNT(DISTINCT cur.role) = 1
               THEN MAX(cur.role)
               ELSE ''
           END
    FROM audio_file_artist_identities cur
    WHERE NOT (
        cur.status = 'valid'
        AND trim(cur.mbid) <> ''
        AND EXISTS (
            SELECT 1 FROM audio_file_identity_checks chk
            WHERE chk.audio_file_id = cur.audio_file_id
              AND chk.role = cur.role
              AND chk.status = 'verified'
        )
        AND EXISTS (
            SELECT 1 FROM audio_files af
            JOIN track_artists ta ON ta.track_id = af.track_id
            WHERE af.id = cur.audio_file_id
              AND ta.artist_id = cur.artist_id
              AND ta.role = cur.role
        )
    )
    GROUP BY cur.audio_file_id, cur.artist_id
    HAVING NOT EXISTS (
        SELECT 1
        FROM audio_file_artist_identities surv
        JOIN audio_file_identity_checks chk ON chk.audio_file_id = surv.audio_file_id AND chk.role = surv.role
        JOIN audio_files af ON af.id = surv.audio_file_id
        JOIN track_artists ta ON ta.track_id = af.track_id AND ta.artist_id = surv.artist_id AND ta.role = surv.role
        WHERE surv.audio_file_id = cur.audio_file_id
          AND surv.artist_id = cur.artist_id
          AND surv.status = 'valid'
          AND trim(surv.mbid) <> ''
          AND chk.status = 'verified'
          AND NOT EXISTS (
              SELECT 1 FROM audio_file_artist_identities diff
              WHERE diff.audio_file_id = cur.audio_file_id
                AND diff.artist_id = cur.artist_id
                AND diff.status = 'valid'
                AND trim(diff.mbid) <> ''
                AND lower(trim(diff.mbid)) <> lower(trim(surv.mbid))
          )
    );

-- Remove unprovable identity rows so only strictly proven rows remain in
-- audio_file_artist_identities.
DELETE FROM audio_file_artist_identities
WHERE NOT (
    status = 'valid'
    AND trim(mbid) <> ''
    AND EXISTS (
        SELECT 1 FROM audio_file_identity_checks chk
        WHERE chk.audio_file_id = audio_file_artist_identities.audio_file_id
          AND chk.role = audio_file_artist_identities.role
          AND chk.status = 'verified'
    )
    AND EXISTS (
        SELECT 1 FROM audio_files af
        JOIN track_artists ta ON ta.track_id = af.track_id
        WHERE af.id = audio_file_artist_identities.audio_file_id
          AND ta.artist_id = audio_file_artist_identities.artist_id
          AND ta.role = audio_file_artist_identities.role
    )
);

UPDATE artist_match_candidates
SET requires_manual_review = 1
WHERE instr(evidence_json, '标签身份与歌手名称不一致') > 0
   OR instr(evidence_json, '需要人工核对') > 0;
