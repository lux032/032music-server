package storage

import (
	"context"
	"strings"
)

// albumDiscCountSQL counts distinct visible disc numbers on the album instead of
// trusting external enrichment or DISCTOTAL tags that may exceed local files.
var albumDiscCountSQL = `COALESCE(NULLIF((SELECT COUNT(DISTINCT COALESCE(NULLIF(t.user_disc_number,0),NULLIF(t.disc_number,0),1)) FROM tracks t WHERE t.album_id=a.id AND ` + trackVisibleSQL("t.id") + `),0),a.disc_count,1)`

// asciiLowerTrim matches SQLite's LOWER(TRIM(...)), which folds only ASCII A-Z.
func asciiLowerTrim(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			b.WriteByte(c + ('a' - 'A'))
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func (s *Store) hydrateTrackVersionCounts(ctx context.Context, items []Track) error {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	keys := make([]string, 0, len(items))
	for _, item := range items {
		k := asciiLowerTrim(item.Title)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	counts := make(map[string]int, len(keys))
	for start := 0; start < len(keys); start += 200 {
		end := min(start+200, len(keys))
		chunk := keys[start:end]
		placeholders := strings.Repeat("?,", len(chunk))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]any, len(chunk))
		for i, k := range chunk {
			args[i] = k
		}
		rows, err := s.db.QueryContext(ctx, `SELECT LOWER(TRIM(COALESCE(t.user_title,t.title))), COUNT(*) FROM tracks t WHERE `+trackVisibleSQL("t.id")+` AND LOWER(TRIM(COALESCE(t.user_title,t.title))) IN (`+placeholders+`) GROUP BY LOWER(TRIM(COALESCE(t.user_title,t.title))) HAVING COUNT(*) > 1`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var title string
			var count int
			if err = rows.Scan(&title, &count); err != nil {
				rows.Close()
				return err
			}
			counts[title] = count
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	for i := range items {
		if count := counts[asciiLowerTrim(items[i].Title)]; count > 1 {
			items[i].VersionCount = count
		}
	}
	return nil
}

// TrackVersions returns the seed track and all visible tracks sharing the same
// normalized title across the library, ordered by release year and album.
func (s *Store) TrackVersions(ctx context.Context, trackID int64) (Track, []Track, error) {
	seed, err := s.TrackByID(ctx, trackID)
	if err != nil {
		return Track{}, nil, err
	}
	key := asciiLowerTrim(seed.Title)
	if key == "" {
		return seed, []Track{seed}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id FROM tracks t JOIN albums a ON a.id=t.album_id WHERE `+trackVisibleSQL("t.id")+` AND LOWER(TRIM(COALESCE(t.user_title,t.title)))=? ORDER BY COALESCE(a.user_release_year,a.release_year,0) ASC, a.id ASC, COALESCE(t.user_disc_number,t.disc_number) ASC, COALESCE(t.user_track_number,t.track_number) ASC, t.id ASC LIMIT 200`, key)
	if err != nil {
		return Track{}, nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return Track{}, nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return Track{}, nil, err
	}
	rows.Close()
	if len(ids) == 0 {
		return seed, []Track{seed}, nil
	}
	items, err := s.tracksByIDs(ctx, ids)
	if err != nil {
		return Track{}, nil, err
	}
	if err := s.AttachTrackAlbumArtistRefs(ctx, items); err != nil {
		return Track{}, nil, err
	}
	return seed, items, nil
}
