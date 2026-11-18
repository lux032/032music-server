package storage

import (
	"context"
	"database/sql"
	"strings"
)

// Only unambiguous single-person-per-role credits (pre-074 legacy rows) or
// import-verified per-role alignments provide identity evidence.
func (s *Store) artistTaggedMBIDs(ctx context.Context, artistID int64) (map[int64]string, error) {
	result := map[int64]string{}
	inconsistent := map[int64]bool{}

	record := func(artist int64, mbid string) {
		if previous := result[artist]; previous != "" && previous != mbid {
			inconsistent[artist] = true
		}
		result[artist] = mbid
	}

	// 1. Import-verified per-(audio_file, artist, role) identities and conflicts.
	var evidence *sql.Rows
	var err error
	if artistID > 0 {
		evidence, err = s.db.QueryContext(ctx, `SELECT e.artist_id,e.role,e.mbid,e.status FROM audio_file_artist_identities e JOIN audio_files af ON af.id=e.audio_file_id WHERE e.artist_id=? AND (e.role<>'primary' OR NOT EXISTS(SELECT 1 FROM track_primary_artist_state st WHERE st.track_id=af.track_id AND st.active=1)) AND (e.status='conflict' OR EXISTS(SELECT 1 FROM audio_file_tags tag WHERE tag.audio_file_id=e.audio_file_id AND ((e.role='primary' AND tag.field_name IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID')) OR (e.role='composer' AND tag.field_name IN ('MUSICBRAINZ_COMPOSERID','MUSICBRAINZ COMPOSER ID')) OR (e.role='lyricist' AND tag.field_name IN ('MUSICBRAINZ_LYRICISTID','MUSICBRAINZ LYRICIST ID')) OR (e.role='arranger' AND tag.field_name IN ('MUSICBRAINZ_ARRANGERID','MUSICBRAINZ ARRANGER ID')) OR (e.role='producer' AND tag.field_name IN ('MUSICBRAINZ_PRODUCERID','MUSICBRAINZ PRODUCER ID'))) AND instr(lower(tag.value),e.mbid)>0))`, artistID)
	} else {
		evidence, err = s.db.QueryContext(ctx, `SELECT e.artist_id,e.role,e.mbid,e.status FROM audio_file_artist_identities e JOIN audio_files af ON af.id=e.audio_file_id WHERE (e.role<>'primary' OR NOT EXISTS(SELECT 1 FROM track_primary_artist_state st WHERE st.track_id=af.track_id AND st.active=1)) AND (e.status='conflict' OR EXISTS(SELECT 1 FROM audio_file_tags tag WHERE tag.audio_file_id=e.audio_file_id AND ((e.role='primary' AND tag.field_name IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID')) OR (e.role='composer' AND tag.field_name IN ('MUSICBRAINZ_COMPOSERID','MUSICBRAINZ COMPOSER ID')) OR (e.role='lyricist' AND tag.field_name IN ('MUSICBRAINZ_LYRICISTID','MUSICBRAINZ LYRICIST ID')) OR (e.role='arranger' AND tag.field_name IN ('MUSICBRAINZ_ARRANGERID','MUSICBRAINZ ARRANGER ID')) OR (e.role='producer' AND tag.field_name IN ('MUSICBRAINZ_PRODUCERID','MUSICBRAINZ PRODUCER ID'))) AND instr(lower(tag.value),e.mbid)>0))`)
	}
	if err != nil {
		return nil, err
	}
	for evidence.Next() {
		var artist int64
		var role, mbid, status string
		if err = evidence.Scan(&artist, &role, &mbid, &status); err != nil {
			evidence.Close()
			return nil, err
		}
		_ = role
		if status == "conflict" {
			inconsistent[artist] = true
			continue
		}
		if normalized := normalizedArtistMBID(mbid); normalized != "" {
			record(artist, normalized)
		}
	}
	if err = evidence.Err(); err != nil {
		evidence.Close()
		return nil, err
	}
	evidence.Close()

	// 2. Legacy fallback for pre-074 databases/rows where (audio_file, role) has
	// no recorded import evidence. Blocked if any conflict exists for the artist
	// on that file, or if track_artists was modified by remote/manual sources.
	var rows *sql.Rows
	const legacyTagPred = `((ta.role='primary' AND tag.field_name IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID') AND NOT EXISTS(SELECT 1 FROM track_primary_artist_state st WHERE st.track_id=ta.track_id AND st.active=1)) OR (ta.role='composer' AND tag.field_name IN ('MUSICBRAINZ_COMPOSERID','MUSICBRAINZ COMPOSER ID')) OR (ta.role='lyricist' AND tag.field_name IN ('MUSICBRAINZ_LYRICISTID','MUSICBRAINZ LYRICIST ID')) OR (ta.role='arranger' AND tag.field_name IN ('MUSICBRAINZ_ARRANGERID','MUSICBRAINZ ARRANGER ID')) OR (ta.role='producer' AND tag.field_name IN ('MUSICBRAINZ_PRODUCERID','MUSICBRAINZ PRODUCER ID')))`
	const legacyGuard = `NOT EXISTS(SELECT 1 FROM audio_file_artist_identities e WHERE e.audio_file_id=af.id AND e.role=ta.role) AND NOT EXISTS(SELECT 1 FROM audio_file_artist_identities c WHERE c.audio_file_id=af.id AND c.artist_id=ta.artist_id AND c.status='conflict') AND NOT EXISTS(SELECT 1 FROM track_artist_sources s WHERE s.track_id=ta.track_id AND s.role=ta.role AND s.source<>'file_tag') AND (SELECT COUNT(*) FROM track_artists p WHERE p.track_id=ta.track_id AND p.role=ta.role)=1`
	if artistID > 0 {
		rows, err = s.db.QueryContext(ctx, `SELECT ta.artist_id,af.id,ta.role,tag.field_name,tag.value FROM track_artists ta JOIN audio_files af ON af.track_id=ta.track_id JOIN audio_file_tags tag ON tag.audio_file_id=af.id WHERE ta.artist_id=? AND `+legacyTagPred+` AND `+legacyGuard, artistID)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT ta.artist_id,af.id,ta.role,tag.field_name,tag.value FROM audio_file_tags tag JOIN audio_files af ON af.id=tag.audio_file_id JOIN track_artists ta ON ta.track_id=af.track_id WHERE tag.field_name IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID','MUSICBRAINZ_COMPOSERID','MUSICBRAINZ COMPOSER ID','MUSICBRAINZ_LYRICISTID','MUSICBRAINZ LYRICIST ID','MUSICBRAINZ_ARRANGERID','MUSICBRAINZ ARRANGER ID','MUSICBRAINZ_PRODUCERID','MUSICBRAINZ PRODUCER ID') AND `+legacyTagPred+` AND `+legacyGuard)
	}
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
		byKey  map[string][]string
	}
	files := map[fileRoleKey]*fileEvidence{}
	for rows.Next() {
		var artist, file int64
		var role, fieldName, value string
		if err = rows.Scan(&artist, &file, &role, &fieldName, &value); err != nil {
			return nil, err
		}
		key := fileRoleKey{artist: artist, file: file, role: role}
		if files[key] == nil {
			files[key] = &fileEvidence{artist: artist, byKey: map[string][]string{}}
		}
		upperKey := strings.ToUpper(strings.TrimSpace(fieldName))
		files[key].byKey[upperKey] = append(files[key].byKey[upperKey], strings.Split(value, ";")...)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, file := range files {
		agreed := ""
		ambiguous := false
		for _, vals := range file.byKey {
			if len(vals) != 1 {
				ambiguous = true
				break
			}
			id := normalizedArtistMBID(vals[0])
			if id == "" {
				ambiguous = true
				break
			}
			if agreed != "" && agreed != id {
				ambiguous = true
				break
			}
			agreed = id
		}
		if ambiguous {
			inconsistent[file.artist] = true
			continue
		}
		if agreed != "" {
			record(file.artist, agreed)
		}
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
