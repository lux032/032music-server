package storage

import (
	"context"
	"strings"
)

// Only unambiguous single-person-per-role credits or import-verified alignments
// provide identity evidence. Multi-person positions cannot be aligned safely
// after name deduplication and artist merges.
func (s *Store) artistTaggedMBIDs(ctx context.Context, artistID int64) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ta.artist_id,af.id,ta.role,tag.value FROM track_artists ta JOIN audio_files af ON af.track_id=ta.track_id JOIN audio_file_tags tag ON tag.audio_file_id=af.id WHERE (?=0 OR ta.artist_id=?) AND (
		(ta.role='primary' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID') AND NOT EXISTS(SELECT 1 FROM track_primary_artist_state st WHERE st.track_id=ta.track_id AND st.active=1))
		OR (ta.role='composer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_COMPOSERID','MUSICBRAINZ COMPOSER ID'))
		OR (ta.role='lyricist' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_LYRICISTID','MUSICBRAINZ LYRICIST ID'))
		OR (ta.role='arranger' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARRANGERID','MUSICBRAINZ ARRANGER ID'))
		OR (ta.role='producer' AND UPPER(tag.field_name) IN ('MUSICBRAINZ_PRODUCERID','MUSICBRAINZ PRODUCER ID'))
	) AND (SELECT COUNT(*) FROM track_artists p WHERE p.track_id=ta.track_id AND p.role=ta.role)=1 ORDER BY ta.artist_id,af.id,ta.role,tag.position`, artistID, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type fileRoleKey struct {
		artist, file int64
		role         string
	}
	type fileEvidence struct {
		artist int64
		values []string
	}
	files := map[fileRoleKey]*fileEvidence{}
	for rows.Next() {
		var artist, file int64
		var role, value string
		if err = rows.Scan(&artist, &file, &role, &value); err != nil {
			return nil, err
		}
		key := fileRoleKey{artist: artist, file: file, role: role}
		if files[key] == nil {
			files[key] = &fileEvidence{artist: artist}
		}
		files[key].values = append(files[key].values, strings.Split(value, ";")...)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	result := map[int64]string{}
	inconsistent := map[int64]bool{}
	for _, file := range files {
		if len(file.values) != 1 {
			continue
		}
		id := normalizedArtistMBID(file.values[0])
		if id == "" {
			continue
		}
		if previous := result[file.artist]; previous != "" && previous != id {
			inconsistent[file.artist] = true
		}
		result[file.artist] = id
	}
	// New scans preserve verified multi-person alignment separately from the
	// materialized relations. Do not infer identity from their current positions.
	evidence, err := s.db.QueryContext(ctx, `SELECT e.artist_id,e.mbid FROM audio_file_artist_identities e JOIN audio_files af ON af.id=e.audio_file_id WHERE (?=0 OR e.artist_id=?) AND EXISTS(SELECT 1 FROM audio_file_tags tag WHERE tag.audio_file_id=e.audio_file_id AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID','MUSICBRAINZ_COMPOSERID','MUSICBRAINZ COMPOSER ID','MUSICBRAINZ_LYRICISTID','MUSICBRAINZ LYRICIST ID','MUSICBRAINZ_ARRANGERID','MUSICBRAINZ ARRANGER ID','MUSICBRAINZ_PRODUCERID','MUSICBRAINZ PRODUCER ID') AND instr(lower(tag.value),e.mbid)>0) AND (NOT EXISTS(SELECT 1 FROM track_primary_artist_state st WHERE st.track_id=af.track_id AND st.active=1) OR EXISTS(SELECT 1 FROM audio_file_tags tag WHERE tag.audio_file_id=e.audio_file_id AND UPPER(tag.field_name) IN ('MUSICBRAINZ_COMPOSERID','MUSICBRAINZ COMPOSER ID','MUSICBRAINZ_LYRICISTID','MUSICBRAINZ LYRICIST ID','MUSICBRAINZ_ARRANGERID','MUSICBRAINZ ARRANGER ID','MUSICBRAINZ_PRODUCERID','MUSICBRAINZ PRODUCER ID') AND instr(lower(tag.value),e.mbid)>0))`, artistID, artistID)
	if err != nil {
		return nil, err
	}
	for evidence.Next() {
		var artist int64
		var mbid string
		if err = evidence.Scan(&artist, &mbid); err != nil {
			evidence.Close()
			return nil, err
		}
		if previous := result[artist]; previous != "" && previous != mbid {
			inconsistent[artist] = true
		}
		result[artist] = mbid
	}
	err = evidence.Err()
	evidence.Close()
	if err != nil {
		return nil, err
	}
	for artist := range inconsistent {
		delete(result, artist)
	}
	return result, nil
}
func normalizedArtistMBID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 {
		return ""
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return ""
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return ""
		}
	}
	if value == "00000000-0000-0000-0000-000000000000" {
		return ""
	}
	return value
}
