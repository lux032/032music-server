package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidSearch = errors.New("invalid search: use at most 200 characters and category artists, albums, tracks or composers")

type SearchOptions struct {
	Query         string
	Category      string
	Limit, Offset int
}

type SearchPage[T any] struct {
	Items  []T   `json:"items"`
	Total  int64 `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

type SearchMatch struct {
	Type     string `json:"type"`
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	ImageURL string `json:"imageUrl"`
	AlbumID  int64  `json:"albumId,omitempty"`
}

type SearchResults struct {
	Query     string             `json:"query"`
	Category  string             `json:"category"`
	BestMatch *SearchMatch       `json:"bestMatch"`
	Artists   SearchPage[Artist] `json:"artists"`
	Albums    SearchPage[Album]  `json:"albums"`
	Tracks    SearchPage[Track]  `json:"tracks"`
	Composers SearchPage[Artist] `json:"composers"`
}

func ValidSearchCategory(category string) bool {
	return category == "" || category == "artists" || category == "albums" || category == "tracks" || category == "composers"
}

type resolvedSearchArtists struct {
	hasMatches  bool
	rootsJSON   string
	membersJSON string
}

// resolveSearchArtists evaluates artist name/alias/external-profile matching
// once per Search call and expands only matched canonical roots to their merged
// member IDs.
func (s *Store) resolveSearchArtists(ctx context.Context, variants []searchQueryVariant) (resolvedSearchArtists, error) {
	cte, args := matchedArtistRootsCTE(variants)
	rows, err := s.db.QueryContext(ctx, cte+`, matched_members(id,root) AS (SELECT root,root FROM search_matches UNION SELECT a.id,m.root FROM artists a JOIN matched_members m ON a.merged_into_artist_id=m.id) SELECT m.id,m.root,sm.score FROM matched_members m JOIN search_matches sm ON sm.root=m.root`, args...)
	if err != nil {
		return resolvedSearchArtists{}, err
	}
	defer rows.Close()

	rootsSeen := map[int64]bool{}
	var roots [][2]int64
	var members [][2]int64
	for rows.Next() {
		var memberID, rootID, score int64
		if err = rows.Scan(&memberID, &rootID, &score); err != nil {
			return resolvedSearchArtists{}, err
		}
		if !rootsSeen[rootID] {
			rootsSeen[rootID] = true
			roots = append(roots, [2]int64{rootID, score})
		}
		members = append(members, [2]int64{memberID, rootID})
	}
	if err = rows.Err(); err != nil {
		return resolvedSearchArtists{}, err
	}
	if len(roots) == 0 {
		return resolvedSearchArtists{}, nil
	}
	rootsRaw, err := json.Marshal(roots)
	if err != nil {
		return resolvedSearchArtists{}, err
	}
	membersRaw, err := json.Marshal(members)
	if err != nil {
		return resolvedSearchArtists{}, err
	}
	return resolvedSearchArtists{
		hasMatches:  true,
		rootsJSON:   string(rootsRaw),
		membersJSON: string(membersRaw),
	}, nil
}

// Search ranks and pages IDs in SQLite before hydrating the selected page.
// Artist previews never limit the set used to find related albums and tracks.
func (s *Store) Search(ctx context.Context, options SearchOptions) (SearchResults, error) {
	q := strings.TrimSpace(options.Query)
	result := SearchResults{Query: q, Category: options.Category,
		Artists:   SearchPage[Artist]{Items: []Artist{}, Limit: 5},
		Albums:    SearchPage[Album]{Items: []Album{}, Limit: 6},
		Tracks:    SearchPage[Track]{Items: []Track{}, Limit: 10},
		Composers: SearchPage[Artist]{Items: []Artist{}, Limit: 5},
	}
	if !ValidSearchCategory(options.Category) || utf8.RuneCountInString(q) > 200 {
		return result, ErrInvalidSearch
	}
	if q == "" {
		return result, nil
	}
	variants := buildSearchQueryVariants(q)
	resolved, err := s.resolveSearchArtists(ctx, variants)
	if err != nil {
		return result, err
	}
	bestScore := 4
	ambiguousArtist := false
	for _, category := range []string{"artists", "tracks", "albums", "composers"} {
		if options.Category != "" && category != options.Category {
			continue
		}
		limit := map[string]int{"artists": 5, "albums": 6, "tracks": 10, "composers": 5}[category]
		offset := 0
		if options.Category != "" {
			limit, offset = page(Filters{Limit: options.Limit, Offset: options.Offset})
		}
		var ids []int64
		var scores []int
		var total int64
		if (category == "artists" || category == "composers") && !resolved.hasMatches {
			ids = []int64{}
		} else {
			pageSQL, countSQL, baseArgs := searchCategorySQL(category, variants, resolved)
			queryArgs := append(append([]any{}, baseArgs...), limit, offset)
			rows, qErr := s.db.QueryContext(ctx, pageSQL, queryArgs...)
			if qErr != nil {
				return result, qErr
			}
			for rows.Next() {
				var id int64
				var score int
				if err = rows.Scan(&id, &score, &total); err != nil {
					rows.Close()
					return result, err
				}
				ids = append(ids, id)
				scores = append(scores, score)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return result, err
			}
			// A page beyond the end has no row to carry the window count.
			if len(ids) == 0 && offset > 0 {
				if err := s.db.QueryRowContext(ctx, countSQL, baseArgs...).Scan(&total); err != nil {
					return result, err
				}
			}
		}
		var candidate *SearchMatch
		switch category {
		case "artists", "composers":
			items, err := s.searchArtistsByIDs(ctx, ids, category == "composers")
			if err != nil {
				return result, err
			}
			p := SearchPage[Artist]{Items: items, Total: total, Limit: limit, Offset: offset}
			if category == "artists" {
				result.Artists = p
			} else {
				result.Composers = p
			}
			if len(items) > 0 {
				candidate = &SearchMatch{Type: category, ID: items[0].ID, Title: items[0].Name, ImageURL: items[0].ImageURL}
			}
			if category == "artists" && len(scores) > 1 && scores[0] == scores[1] {
				ambiguousArtist = true
			}
		case "albums":
			items := []Album{}
			if len(ids) > 0 {
				items, err = s.hydrateAlbums(ctx, ids)
			}
			if err != nil {
				return result, err
			}
			result.Albums = SearchPage[Album]{Items: items, Total: total, Limit: limit, Offset: offset}
			if len(items) > 0 {
				v := items[0]
				candidate = &SearchMatch{Type: category, ID: v.ID, Title: v.Title, Subtitle: v.Artist, ImageURL: v.ArtworkURL}
			}
		case "tracks":
			items, err := s.tracksByIDs(ctx, ids)
			if err != nil {
				return result, err
			}
			if items == nil {
				items = []Track{}
			}
			result.Tracks = SearchPage[Track]{Items: items, Total: total, Limit: limit, Offset: offset}
			if len(items) > 0 {
				v := items[0]
				candidate = &SearchMatch{Type: category, ID: v.ID, Title: v.Title, Subtitle: v.Artist, ImageURL: v.ArtworkURL, AlbumID: v.AlbumID}
			}
		}
		// No speculative best match for a tie between equally matching artists.
		// On equal scores a performer wins, then a track, album and composer.
		if options.Category == "" && candidate != nil && scores[0] < bestScore && scores[0] < 3 {
			result.BestMatch = candidate
			bestScore = scores[0]
		}
	}
	if result.BestMatch != nil && result.BestMatch.Type == "artists" && ambiguousArtist {
		result.BestMatch = nil
	}
	return result, nil
}

// searchCategorySQL builds the paginated and count SQL for one category using
// pre-resolved artist matches, pushing match filters before materializing and
// pinning join order from matched_members into album_artists/track_artists.
func searchCategorySQL(category string, variants []searchQueryVariant, resolved resolvedSearchArtists) (pageSQL, countSQL string, args []any) {
	switch category {
	case "artists", "composers":
		var visibleSQL string
		if category == "artists" {
			visibleSQL = `SELECT DISTINCT mm.root FROM matched_members mm CROSS JOIN album_artists aa ON aa.artist_id=mm.id WHERE ` + albumVisibleSQL("aa.album_id") +
				` UNION SELECT DISTINCT mm.root FROM matched_members mm CROSS JOIN track_artists ta ON ta.artist_id=mm.id WHERE ta.role='primary' AND ` + trackVisibleSQL("ta.track_id")
		} else {
			visibleSQL = `SELECT DISTINCT mm.root FROM matched_members mm CROSS JOIN track_artists ta ON ta.artist_id=mm.id WHERE ta.role='composer' AND ` + trackVisibleSQL("ta.track_id")
		}
		cte := `WITH matched_roots(root,score) AS MATERIALIZED (SELECT CAST(json_extract(value,'$[0]') AS INTEGER),CAST(json_extract(value,'$[1]') AS INTEGER) FROM json_each(?)),` +
			` matched_members(id,root) AS MATERIALIZED (SELECT CAST(json_extract(value,'$[0]') AS INTEGER),CAST(json_extract(value,'$[1]') AS INTEGER) FROM json_each(?)),` +
			` visible_roots(root) AS MATERIALIZED (` + visibleSQL + `) `
		args = []any{resolved.rootsJSON, resolved.membersJSON}
		pageSQL = cte + `SELECT ar.id,m.score,COUNT(*) OVER() FROM visible_roots v JOIN matched_roots m ON m.root=v.root CROSS JOIN artists ar ON ar.id=v.root ORDER BY m.score,COALESCE(ar.user_display_name,ar.display_name) COLLATE NOCASE,ar.id LIMIT ? OFFSET ?`
		countSQL = cte + `SELECT COUNT(*) FROM visible_roots`
		return pageSQL, countSQL, args

	case "albums":
		titleCond, titleArgs := searchLikeCondition(variants, "COALESCE(a.user_title,a.title)", "COALESCE(a.reading_title,'')")
		scoreExpr, scoreArgs := searchScoreExpr(variants, "title", "COALESCE(reading_title,'')")
		if !resolved.hasMatches {
			cte := `WITH matched AS (SELECT a.id,COALESCE(a.user_title,a.title) AS title,a.reading_title FROM albums a WHERE ` + titleCond + ` AND ` + albumVisibleSQL("a.id") + `),` +
				` ranked AS (SELECT id,COALESCE(` + scoreExpr + `,3) AS score,2 AS relation,title FROM matched) `
			args = append(append([]any(nil), titleArgs...), scoreArgs...)
			return cte + `SELECT id,score,COUNT(*) OVER() FROM ranked ORDER BY score,relation,title COLLATE NOCASE,id LIMIT ? OFFSET ?`,
				cte + `SELECT COUNT(*) FROM ranked`, args
		}
		cte := `WITH matched_members(id,root) AS MATERIALIZED (SELECT CAST(json_extract(value,'$[0]') AS INTEGER),CAST(json_extract(value,'$[1]') AS INTEGER) FROM json_each(?)),` +
			` album_artist_hits(album_id) AS MATERIALIZED (SELECT DISTINCT aa.album_id FROM matched_members mm CROSS JOIN album_artists aa ON aa.artist_id=mm.id),` +
			` track_artist_hits(album_id) AS MATERIALIZED (SELECT DISTINCT t.album_id FROM matched_members mm CROSS JOIN track_artists ta ON ta.artist_id=mm.id CROSS JOIN tracks t ON t.id=ta.track_id WHERE ta.role='primary' AND ` + trackVisibleSQL("t.id") + `),` +
			` matched AS (SELECT a.id,COALESCE(a.user_title,a.title) AS title,a.reading_title,a.user_is_compilation,a.is_compilation,a.id IN (SELECT album_id FROM album_artist_hits) AS album_match FROM albums a WHERE (a.id IN (SELECT album_id FROM album_artist_hits) OR a.id IN (SELECT album_id FROM track_artist_hits) OR ` + titleCond + `) AND ` + albumVisibleSQL("a.id") + `),` +
			` ranked AS (SELECT id,COALESCE(` + scoreExpr + `,3) AS score,CASE WHEN album_match THEN CASE WHEN COALESCE(user_is_compilation,is_compilation,0)=0 AND (SELECT COUNT(*) FROM album_artists aa WHERE aa.album_id=matched.id)=1 THEN 0 ELSE 1 END ELSE 2 END AS relation,title FROM matched) `
		args = append([]any{resolved.membersJSON}, titleArgs...)
		args = append(args, scoreArgs...)
		return cte + `SELECT id,score,COUNT(*) OVER() FROM ranked ORDER BY score,relation,title COLLATE NOCASE,id LIMIT ? OFFSET ?`,
			cte + `SELECT COUNT(*) FROM ranked`, args

	case "tracks":
		titleCond, titleArgs := searchLikeCondition(variants, "COALESCE(t.user_title,t.title)")
		scoreExpr, scoreArgs := searchScoreExpr(variants, "title")
		if !resolved.hasMatches {
			cte := `WITH matched AS (SELECT t.id,COALESCE(t.user_title,t.title) AS title FROM tracks t WHERE ` + titleCond + ` AND ` + trackVisibleSQL("t.id") + `),` +
				` ranked AS (SELECT id,COALESCE(` + scoreExpr + `,3) AS score,0 AS relation,title FROM matched) `
			args = append(append([]any(nil), titleArgs...), scoreArgs...)
			return cte + `SELECT id,score,COUNT(*) OVER() FROM ranked ORDER BY score,relation,title COLLATE NOCASE,id LIMIT ? OFFSET ?`,
				cte + `SELECT COUNT(*) FROM ranked`, args
		}
		cte := `WITH matched_members(id,root) AS MATERIALIZED (SELECT CAST(json_extract(value,'$[0]') AS INTEGER),CAST(json_extract(value,'$[1]') AS INTEGER) FROM json_each(?)),` +
			` direct_track_hits(track_id) AS MATERIALIZED (SELECT DISTINCT ta.track_id FROM matched_members mm CROSS JOIN track_artists ta ON ta.artist_id=mm.id WHERE ta.role='primary'),` +
			` album_fallback_hits(album_id) AS MATERIALIZED (SELECT DISTINCT aa.album_id FROM matched_members mm CROSS JOIN album_artists aa ON aa.artist_id=mm.id),` +
			` matched AS (SELECT t.id,COALESCE(t.user_title,t.title) AS title FROM tracks t WHERE (t.id IN (SELECT track_id FROM direct_track_hits) OR (t.album_id IN (SELECT album_id FROM album_fallback_hits) AND NOT EXISTS(SELECT 1 FROM track_artists ta WHERE ta.track_id=t.id AND ta.role='primary')) OR ` + titleCond + `) AND ` + trackVisibleSQL("t.id") + `),` +
			` ranked AS (SELECT id,COALESCE(` + scoreExpr + `,3) AS score,0 AS relation,title FROM matched) `
		args = append([]any{resolved.membersJSON}, titleArgs...)
		args = append(args, scoreArgs...)
		return cte + `SELECT id,score,COUNT(*) OVER() FROM ranked ORDER BY score,relation,title COLLATE NOCASE,id LIMIT ? OFFSET ?`,
			cte + `SELECT COUNT(*) FROM ranked`, args
	}
	panic("unvalidated search category")
}

func (s *Store) searchArtistsByIDs(ctx context.Context, ids []int64, composer bool) ([]Artist, error) {
	result := []Artist{}
	if len(ids) == 0 {
		return result, nil
	}
	placeholders, idArgs := inClause(ids)
	role := "'primary'"
	if composer {
		role = "'composer'"
	}
	args := make([]any, 0, len(idArgs)*2)
	args = append(args, idArgs...)
	args = append(args, idArgs...)
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE chain(id,root) AS (SELECT id,id FROM artists WHERE id IN (`+placeholders+`) UNION SELECT a.id,c.root FROM artists a JOIN chain c ON a.merged_into_artist_id=c.id) SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name),`+artistImageURLSQL("ar")+`,ar.is_favorite,
 (SELECT COUNT(DISTINCT aa.album_id) FROM chain c CROSS JOIN album_artists aa ON aa.artist_id=c.id WHERE c.root=ar.id AND `+albumVisibleSQL("aa.album_id")+`),
 (SELECT COUNT(DISTINCT ta.track_id) FROM chain c CROSS JOIN track_artists ta ON ta.artist_id=c.id WHERE c.root=ar.id AND ta.role=`+role+` AND `+trackVisibleSQL("ta.track_id")+`) FROM artists ar WHERE ar.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]Artist{}
	for rows.Next() {
		var item Artist
		var favorite int
		if err = rows.Scan(&item.ID, &item.Name, &item.ImageURL, &favorite, &item.AlbumCount, &item.TrackCount); err != nil {
			return nil, err
		}
		item.IsFavorite = favorite != 0
		byID[item.ID] = item
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result, nil
}
