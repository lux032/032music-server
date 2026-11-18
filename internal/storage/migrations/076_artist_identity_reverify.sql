CREATE TABLE audio_file_artist_reverify (
    audio_file_id INTEGER NOT NULL REFERENCES audio_files(id) ON DELETE CASCADE,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    role TEXT NOT NULL DEFAULT '' CHECK (role IN ('', 'primary', 'composer', 'lyricist', 'arranger', 'producer')),
    PRIMARY KEY (audio_file_id, artist_id)
) STRICT;

CREATE INDEX idx_file_artist_reverify_artist ON audio_file_artist_reverify(artist_id);

-- Backfill pending re-verification markers for any (file, artist, role) whose
-- identity check is 'blocked' alongside a non-empty role MBID tag. Other valid
-- roles on the same file never exempt a blocked role without slot-level proof
-- from a trusted rescan.
INSERT OR IGNORE INTO audio_file_artist_reverify(audio_file_id, artist_id, role)
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
