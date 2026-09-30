package storage

import (
	"context"
	"strings"
)

// Only unambiguous primary credits provide identity evidence. Multi-person
// positions cannot be aligned safely after name deduplication and artist merges.
func (s *Store) artistTaggedMBIDs(ctx context.Context, artistID int64) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ta.artist_id,af.id,tag.value FROM track_artists ta JOIN audio_files af ON af.track_id=ta.track_id JOIN audio_file_tags tag ON tag.audio_file_id=af.id WHERE ta.role='primary' AND (?=0 OR ta.artist_id=?) AND (SELECT COUNT(*) FROM track_artists p WHERE p.track_id=ta.track_id AND p.role='primary')=1 AND UPPER(tag.field_name) IN ('MUSICBRAINZ_ARTISTID','MUSICBRAINZ ARTIST ID') ORDER BY ta.artist_id,af.id,tag.position`, artistID, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type fileEvidence struct {
		artist int64
		values []string
	}
	files := map[int64]*fileEvidence{}
	for rows.Next() {
		var artist, file int64
		var value string
		if err = rows.Scan(&artist, &file, &value); err != nil {
			return nil, err
		}
		if files[file] == nil {
			files[file] = &fileEvidence{artist: artist}
		}
		files[file].values = append(files[file].values, strings.Split(value, ";")...)
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
