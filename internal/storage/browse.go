package storage

import (
	"context"
	"database/sql"
	"slices"
	"strings"
)

// This file holds the shared browsing predicates and the two-stage paginated
// album listing. ListAlbums/CountAlbums and ListTracks/CountTracks deliberately
// share a single WHERE builder each so the admin grid, its result counter and
// the JSON API can never disagree about what a filter means.

// albumIndexExpression is the album sort key used by the letter index.
const albumIndexExpression = "COALESCE(NULLIF(a.reading_title,''),a.sort_title,COALESCE(a.user_title,a.title))"

// trackHasEffectiveGenreTX matches when track tx effectively carries genre gx:
// manual track overrides replace the raw file tags.
const trackHasEffectiveGenreTX = `((EXISTS(SELECT 1 FROM track_genre_overrides ox WHERE ox.track_id=tx.id) AND EXISTS(SELECT 1 FROM track_genre_overrides ox WHERE ox.track_id=tx.id AND ox.genre_id=gx.id)) OR (NOT EXISTS(SELECT 1 FROM track_genre_overrides ox WHERE ox.track_id=tx.id) AND EXISTS(SELECT 1 FROM track_genres rx WHERE rx.track_id=tx.id AND rx.genre_id=gx.id)))`

// trackHasEffectiveGenreT is the same predicate for track alias t.
const trackHasEffectiveGenreT = `((EXISTS(SELECT 1 FROM track_genre_overrides ox WHERE ox.track_id=t.id) AND EXISTS(SELECT 1 FROM track_genre_overrides ox WHERE ox.track_id=t.id AND ox.genre_id=gx.id)) OR (NOT EXISTS(SELECT 1 FROM track_genre_overrides ox WHERE ox.track_id=t.id) AND EXISTS(SELECT 1 FROM track_genres rx WHERE rx.track_id=t.id AND rx.genre_id=gx.id)))`

// albumHasEffectiveGenre matches when album a effectively carries genre gx:
// manual album overrides win; otherwise the union of the effective genres of
// the album's tracks applies. Same definition as the Genres value returned by
// ListAlbums and AlbumByID.
const albumHasEffectiveGenre = `(EXISTS(SELECT 1 FROM album_genre_overrides ago WHERE ago.album_id=a.id AND ago.genre_id=gx.id) OR (NOT EXISTS(SELECT 1 FROM album_genre_overrides ago WHERE ago.album_id=a.id) AND EXISTS(SELECT 1 FROM tracks tx WHERE tx.album_id=a.id AND ` + trackHasEffectiveGenreTX + `)))`

// albumWhere builds the WHERE clause shared by ListAlbums and CountAlbums.
// Every condition stacks: letter index, keyword (album title, reading title or
// any album artist via EXISTS so multi-artist albums keep all names), artist
// (album credit or any track credit), release year and effective genre.
func albumWhere(f Filters) (string, []any) {
	clauses := make([]string, 0, 6)
	args := make([]any, 0, 16)
	// 已无可用文件的专辑不出现在任何浏览结果中（见 visibility.go）。
	clauses = append(clauses, albumVisibleSQL("a.id"))
	if condition, indexArgs := IndexCondition(albumIndexExpression, f.Index); condition != "" {
		clauses = append(clauses, condition)
		args = append(args, indexArgs...)
	}
	if variants := SearchVariants(f.Query); len(variants) > 0 {
		parts := make([]string, 0, len(variants)*3)
		for _, variant := range variants {
			parts = append(parts,
				"COALESCE(a.user_title,a.title) LIKE '%'||?||'%'",
				"COALESCE(a.reading_title,'') LIKE '%'||?||'%'",
				"EXISTS(SELECT 1 FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id AND COALESCE(ar.user_display_name,ar.display_name) LIKE '%'||?||'%')")
			args = append(args, variant, variant, variant)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	trackRole := ""
	if f.PerformerOnly {
		trackRole = " AND tax.role='primary'"
	}
	clauses = append(clauses,
		"(?=0 OR EXISTS(SELECT 1 FROM album_artists ax WHERE ax.album_id=a.id AND ax.artist_id=?) OR EXISTS(SELECT 1 FROM tracks tx JOIN track_artists tax ON tax.track_id=tx.id WHERE tx.album_id=a.id AND tax.artist_id=?"+trackRole+"))",
		"(?=0 OR COALESCE(a.user_release_year,a.release_year)=?)",
		"(?='' OR EXISTS(SELECT 1 FROM genres gx WHERE gx.name=? COLLATE NOCASE AND "+albumHasEffectiveGenre+"))")
	args = append(args, f.ArtistID, f.ArtistID, f.ArtistID, f.Year, f.Year, f.Genre, f.Genre)
	return strings.Join(clauses, " AND "), args
}

// albumReleaseDateSort is the effective release date of album alias a as a
// sortable string, newest first under DESC. A user-edited date wins; a
// user-edited year that contradicts the tagged date replaces it; otherwise
// the tagged date, then the tagged year. Tag separators vary (2004.05.12,
// 2004/05/12), so they are unified to '-'. A bare year sorts after the full
// dates of that year under DESC, undated albums sort last.
const albumReleaseDateSort = "REPLACE(REPLACE(COALESCE(NULLIF(a.user_release_date,''),CASE WHEN a.user_release_year IS NOT NULL AND substr(COALESCE(a.release_date,''),1,4)<>CAST(a.user_release_year AS TEXT) THEN CAST(a.user_release_year AS TEXT) END,NULLIF(a.release_date,''),CAST(a.release_year AS TEXT),''),'.','-'),'/','-')"

// albumOrder returns a stable ORDER BY for album browsing. An explicit sort
// wins; with an active letter index and no explicit sort the historical
// reading-title order applies. a.id always terminates the key so pagination
// is stable.
func albumOrder(f Filters) string {
	switch f.Sort {
	case "year":
		return "COALESCE(a.user_release_year,a.release_year,0) DESC,a.sort_title,a.id"
	case "date":
		return albumReleaseDateSort + " DESC,a.sort_title,a.id"
	case "added":
		return "a.added_at DESC,a.id"
	case "recentlyPlayed":
		return "COALESCE((SELECT MAX(pp.last_played_at) FROM tracks pt JOIN playback_progress pp ON pp.track_id=pt.id WHERE pt.album_id=a.id),'') DESC,a.sort_title,a.id"
	}
	if f.Index != "" {
		return "COALESCE(NULLIF(a.reading_title,''),a.sort_title) COLLATE NOCASE,a.id"
	}
	return "a.sort_title COLLATE NOCASE,a.id"
}

// trackWhere builds the WHERE clause shared by ListTracks and CountTracks.
// Artist predicates preserve API any-role behavior unless PerformerOnly is set, and respect
// user_display_name; the genre predicate uses effective (override-aware)
// track genres.
func trackWhere(f Filters) (string, []any) {
	clauses := make([]string, 0, 7)
	args := make([]any, 0, 16)
	// 已无可用文件的歌曲不出现在任何浏览结果中（见 visibility.go）。
	clauses = append(clauses, trackVisibleSQL("t.id"))
	if variants := SearchVariants(f.Query); len(variants) > 0 {
		parts := make([]string, 0, len(variants)*3)
		for _, variant := range variants {
			parts = append(parts,
				"COALESCE(t.user_title,t.title) LIKE '%'||?||'%'",
				"COALESCE(a.user_title,a.title) LIKE '%'||?||'%'",
				"EXISTS(SELECT 1 FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.track_id=t.id"+trackNameRole(f)+" AND COALESCE(ar.user_display_name,ar.display_name) LIKE '%'||?||'%')")
			args = append(args, variant, variant, variant)
			if f.PerformerOnly {
				parts = append(parts, "EXISTS(SELECT 1 FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=t.album_id AND COALESCE(ar.user_display_name,ar.display_name) LIKE '%'||?||'%')")
				args = append(args, variant)
			}
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if f.PerformerOnly {
		clauses = append(clauses, "(?=0 OR t.id IN (SELECT track_id FROM track_artists WHERE role='primary' AND artist_id=?) OR t.album_id IN (SELECT album_id FROM album_artists WHERE artist_id=?))")
		args = append(args, f.ArtistID, f.ArtistID, f.ArtistID)
	} else {
		clauses = append(clauses, "(?=0 OR EXISTS(SELECT 1 FROM track_artists ta WHERE ta.track_id=t.id AND ta.artist_id=?))")
		args = append(args, f.ArtistID, f.ArtistID)
	}
	clauses = append(clauses,
		"(?=0 OR a.id=?)",
		"(?=0 OR COALESCE(a.user_release_year,a.release_year)=?)",
		"(?='' OR EXISTS(SELECT 1 FROM genres gx WHERE gx.name=? COLLATE NOCASE AND "+trackHasEffectiveGenreT+"))",
		"(?=0 OR "+effectiveTrackTypeSQL+" NOT IN ('instrumental','off_vocal'))")
	args = append(args, f.AlbumID, f.AlbumID, f.Year, f.Year, f.Genre, f.Genre, boolInt(f.HideInstrumental))
	appendTrackFocus(&clauses, &args, f.Focus)
	return strings.Join(clauses, " AND "), args
}

// hydrateAlbums loads the display fields for one page of album ids using one
// batch query per aspect instead of per-row correlated subqueries. The result
// order follows ids.
func (s *Store) hydrateAlbums(ctx context.Context, ids []int64) ([]Album, error) {
	placeholders, args := inClause(ids)
	byID := make(map[int64]*Album, len(ids))

	base, err := s.db.QueryContext(ctx, `SELECT a.id,COALESCE(a.user_title,a.title),COALESCE(a.user_release_year,a.release_year,0),a.disc_count,COALESCE(a.user_performed_by,a.performed_by,''),COALESCE(a.user_album_type,a.album_type,'album'),COALESCE(a.user_version,a.version,''),a.added_at,a.updated_at,a.is_favorite,COALESCE(a.user_album_type,''),COALESCE(a.album_type,''),COALESCE(a.album_type_source,'') FROM albums a WHERE a.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	kindInputs := make(map[int64]*releaseKindInput, len(ids))
	for base.Next() {
		v := Album{Artist: "Unknown Artist"}
		var favorite int
		in := &releaseKindInput{}
		if err = base.Scan(&v.ID, &v.Title, &v.Year, &v.DiscCount, &v.PerformedBy, &v.AlbumType, &v.Version, &v.AddedAt, &v.UpdatedAt, &favorite, &in.UserType, &in.StoredType, &in.Source); err != nil {
			base.Close()
			return nil, err
		}
		v.IsFavorite = favorite != 0
		byID[v.ID] = &v
		kindInputs[v.ID] = in
	}
	if err = base.Err(); err != nil {
		base.Close()
		return nil, err
	}
	base.Close()

	// stringHydration runs a (album_id, text) batch query and applies each
	// value to its album.
	stringHydration := func(query string, queryArgs []any, set func(*Album, string)) error {
		rows, qerr := s.db.QueryContext(ctx, query, queryArgs...)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var albumID int64
			var value sql.NullString
			if err = rows.Scan(&albumID, &value); err != nil {
				return err
			}
			if album, ok := byID[albumID]; ok && value.Valid {
				set(album, value.String)
			}
		}
		return rows.Err()
	}
	numberHydration := func(query string, set func(*Album, int64)) error {
		rows, qerr := s.db.QueryContext(ctx, query, args...)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var albumID, value int64
			if err = rows.Scan(&albumID, &value); err != nil {
				return err
			}
			if album, ok := byID[albumID]; ok {
				set(album, value)
			}
		}
		return rows.Err()
	}

	hydrations := []func() error{
		// Album artists in credit order; all of them, regardless of filters.
		func() error {
			return stringHydration(`SELECT aa.album_id, GROUP_CONCAT(COALESCE(ar.user_display_name,ar.display_name), ', ' ORDER BY aa.position, ar.id) FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id IN (`+placeholders+`) GROUP BY aa.album_id`, args, func(a *Album, v string) { a.Artist = v })
		},
		// Effective genres, fallback layer: distinct union of the per-track
		// effective genres (override-aware) for albums without album-level
		// overrides. Genre-id order matches the historical genres-table scan.
		func() error {
			doubleArgs := append(append([]any(nil), args...), args...)
			return stringHydration(`SELECT album_id, GROUP_CONCAT(name, ',' ORDER BY gid) FROM (SELECT t.album_id AS album_id, g.id AS gid, g.name AS name FROM tracks t JOIN track_genre_overrides ox ON ox.track_id=t.id JOIN genres g ON g.id=ox.genre_id WHERE t.album_id IN (`+placeholders+`) AND NOT EXISTS(SELECT 1 FROM album_genre_overrides ago WHERE ago.album_id=t.album_id) UNION SELECT t.album_id, g.id, g.name FROM tracks t JOIN track_genres rx ON rx.track_id=t.id JOIN genres g ON g.id=rx.genre_id WHERE t.album_id IN (`+placeholders+`) AND NOT EXISTS(SELECT 1 FROM track_genre_overrides ox WHERE ox.track_id=t.id) AND NOT EXISTS(SELECT 1 FROM album_genre_overrides ago WHERE ago.album_id=t.album_id)) GROUP BY album_id`, doubleArgs, func(a *Album, v string) { a.Genres = v })
		},
		// Effective genres, override layer: album-level overrides win and keep
		// their manual ordering.
		func() error {
			return stringHydration(`SELECT ago.album_id, GROUP_CONCAT(g.name, ',' ORDER BY ago.position) FROM album_genre_overrides ago JOIN genres g ON g.id=ago.genre_id WHERE ago.album_id IN (`+placeholders+`) GROUP BY ago.album_id`, args, func(a *Album, v string) { a.Genres = v })
		},
		func() error {
			return stringHydration(`SELECT t.album_id, GROUP_CONCAT(DISTINCT UPPER(af.container)) FROM tracks t JOIN audio_files af ON af.track_id=t.id AND af.status='available' WHERE t.album_id IN (`+placeholders+`) GROUP BY t.album_id`, args, func(a *Album, v string) { a.Formats = v })
		},
		func() error {
			return numberHydration(`SELECT t.album_id, SUM(af.file_size) FROM tracks t JOIN audio_files af ON af.track_id=t.id AND af.status='available' WHERE t.album_id IN (`+placeholders+`) GROUP BY t.album_id`, func(a *Album, v int64) { a.TotalBytes = v })
		},
		func() error {
			return numberHydration(`SELECT t.album_id, COUNT(*) FROM tracks t WHERE t.album_id IN (`+placeholders+`) AND `+trackVisibleSQL("t.id")+` GROUP BY t.album_id`, func(a *Album, v int64) { a.TrackCount = v })
		},
		// 发行类型推断输入：核心曲目数与时长（不含伴奏/off vocal/TV size）。
		func() error {
			rows, qerr := s.db.QueryContext(ctx, `SELECT t.album_id, COUNT(*), COALESCE(SUM(t.duration_ms),0) FROM tracks t WHERE t.album_id IN (`+placeholders+`) AND COALESCE(t.user_track_type,t.track_type,'regular') NOT IN ('instrumental','off_vocal','tv_size') GROUP BY t.album_id`, args...)
			if qerr != nil {
				return qerr
			}
			defer rows.Close()
			for rows.Next() {
				var albumID, count, duration int64
				if err = rows.Scan(&albumID, &count, &duration); err != nil {
					return err
				}
				if in, ok := kindInputs[albumID]; ok {
					in.CoreTracks, in.DurationMillis = count, duration
				}
			}
			return rows.Err()
		},
		func() error {
			return stringHydration(`SELECT pt.album_id, COALESCE(MAX(pp.last_played_at),'') FROM tracks pt JOIN playback_progress pp ON pp.track_id=pt.id WHERE pt.album_id IN (`+placeholders+`) GROUP BY pt.album_id`, args, func(a *Album, v string) { a.LastPlayedAt = v })
		},
		// Custom cover first, then primary, falling back to the earliest
		// artwork (same rule as the album detail view).
		func() error {
			return stringHydration(`SELECT album_id, '/api/v1/artwork/'||id FROM (SELECT aw.album_id AS album_id, aw.id AS id, ROW_NUMBER() OVER (PARTITION BY aw.album_id ORDER BY (aw.source_type='custom') DESC, aw.is_primary DESC, aw.id) AS rn FROM artworks aw WHERE aw.album_id IN (`+placeholders+`)) WHERE rn=1`, args, func(a *Album, v string) { a.ArtworkURL = v })
		},
	}
	for _, hydrate := range hydrations {
		if err := hydrate(); err != nil {
			return nil, err
		}
	}
	if err = s.hydrateAlbumArtistLinks(ctx, placeholders, args, byID); err != nil {
		return nil, err
	}
	for id, album := range byID {
		if in, ok := kindInputs[id]; ok {
			in.Title, in.DiscCount, in.TotalTracks = album.Title, album.DiscCount, album.TrackCount
			album.ReleaseKind = resolveReleaseKind(*in)
		}
	}

	result := make([]Album, 0, len(ids))
	for _, id := range ids {
		album, ok := byID[id]
		if !ok {
			continue
		}
		album.TotalSize = formatBytes(album.TotalBytes)
		result = append(result, *album)
	}
	return result, nil
}

// hydrateAlbumArtistLinks attaches the credited album artists (id + name,
// credit order) so browse cards can link each name to its artist page.
func (s *Store) hydrateAlbumArtistLinks(ctx context.Context, placeholders string, args []any, byID map[int64]*Album) error {
	rows, err := s.db.QueryContext(ctx, `SELECT aa.album_id,ar.id,COALESCE(ar.user_display_name,ar.display_name) FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id IN (`+placeholders+`) ORDER BY aa.album_id,aa.position,ar.id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var albumID int64
		var artist Artist
		if err = rows.Scan(&albumID, &artist.ID, &artist.Name); err != nil {
			return err
		}
		if album, ok := byID[albumID]; ok {
			if !slices.ContainsFunc(album.AlbumArtists, func(v Artist) bool { return v.ID == artist.ID }) {
				album.AlbumArtists = append(album.AlbumArtists, artist)
			}
		}
	}
	return rows.Err()
}

// ListArtistOptions returns id+name rows for filter dropdowns without the
// counting subqueries of ListArtists.
func (s *Store) ListArtistOptions(ctx context.Context, role, query string, limit int) ([]Artist, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	creditRole := IsCreditRole(role) || role == "credit"
	if !creditRole && role != "performer" {
		role = normalizeArtistRole(role)
	}
	clauses := []string{"ar.merged_into_artist_id IS NULL", "(?='all' OR (?='album' AND EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=ar.id)) OR (?='track' AND EXISTS(SELECT 1 FROM track_artists ta WHERE ta.artist_id=ar.id)))"}
	args := []any{role, role, role}
	if role == "performer" {
		clauses[1] = "(EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=ar.id) OR ar.id IN (SELECT artist_id FROM track_artists WHERE role='primary'))"
		args = nil
	}
	if creditRole {
		// Start from the role index once, then follow only credited artists' merge chains.
		clauses = []string{"ar.merged_into_artist_id IS NULL", `ar.id IN (WITH RECURSIVE credited(id,next) AS (SELECT a.id,a.merged_into_artist_id FROM artists a WHERE a.id IN (SELECT ta.artist_id FROM track_artists ta WHERE ta.role=? AND `+trackVisibleSQL("ta.track_id")+`) UNION SELECT a.id,a.merged_into_artist_id FROM artists a JOIN credited c ON a.id=c.next) SELECT id FROM credited WHERE next IS NULL)`}
		args = []any{role}
		if role == "credit" {
			clauses[1] = strings.Replace(clauses[1], "ta.role=?", "ta.role IN ('composer','lyricist','arranger','producer')", 1)
			args = nil
		}
	}
	if variants := SearchVariants(query); len(variants) > 0 {
		parts := make([]string, 0, len(variants)*2)
		for _, variant := range variants {
			parts = append(parts, "COALESCE(ar.user_display_name,ar.display_name) LIKE '%'||?||'%'", "COALESCE(ar.reading_name,'') LIKE '%'||?||'%'")
			args = append(args, variant, variant)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if !creditRole {
		// Credit options are already restricted to visible tracks inside the
		// merge-chain CTE (credits stay on the historical artist ids).
		clauses = append(clauses, artistVisibleSQL("ar.id"))
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name) FROM artists ar WHERE `+strings.Join(clauses, " AND ")+` ORDER BY COALESCE(ar.user_display_name,ar.display_name) COLLATE NOCASE, ar.id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]Artist, 0)
	for rows.Next() {
		var v Artist
		if err = rows.Scan(&v.ID, &v.Name); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// ListAlbumOptions returns id+title rows for filter dropdowns without the
// album detail hydration.
func (s *Store) ListAlbumOptions(ctx context.Context, query string, limit int) ([]Album, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}
	where := albumVisibleSQL("a.id")
	args := make([]any, 0, 4)
	if variants := SearchVariants(query); len(variants) > 0 {
		parts := make([]string, 0, len(variants)*2)
		for _, variant := range variants {
			parts = append(parts, "COALESCE(a.user_title,a.title) LIKE '%'||?||'%'", "COALESCE(a.reading_title,'') LIKE '%'||?||'%'")
			args = append(args, variant, variant)
		}
		where += " AND (" + strings.Join(parts, " OR ") + ")"
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,COALESCE(a.user_title,a.title) FROM albums a WHERE `+where+` ORDER BY a.sort_title COLLATE NOCASE, a.id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := make([]Album, 0)
	for rows.Next() {
		var v Album
		if err = rows.Scan(&v.ID, &v.Title); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	return list, rows.Err()
}

// ArtistNameByID resolves a single display name; used to label an active
// filter even when the row is outside the first page of dropdown options.
func (s *Store) ArtistNameByID(ctx context.Context, id int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=?`, id).Scan(&name)
	return name, err
}

// AlbumTitleByID resolves a single display title; same purpose as
// ArtistNameByID.
func (s *Store) AlbumTitleByID(ctx context.Context, id int64) (string, error) {
	var title string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(user_title,title) FROM albums WHERE id=?`, id).Scan(&title)
	return title, err
}

func trackNameRole(f Filters) string {
	if f.PerformerOnly {
		return " AND ta.role='primary'"
	}
	return ""
}
