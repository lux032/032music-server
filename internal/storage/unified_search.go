package storage

import (
	"context"
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
	cte, args := searchArtistCTE(q)
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
		candidates := searchCandidatesSQL(category)
		var total int64
		// Count and page in one pass rather than rebuilding all alias and
		// relationship sets for a separate COUNT query.
		queryArgs := append(append([]any{}, args...), limit, offset)
		rows, err := s.db.QueryContext(ctx, cte+candidates+` SELECT id,score,COUNT(*) OVER() FROM ranked ORDER BY score,relation,title COLLATE NOCASE,id LIMIT ? OFFSET ?`, queryArgs...)
		if err != nil {
			return result, err
		}
		ids := []int64{}
		scores := []int{}
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
			if err := s.db.QueryRowContext(ctx, cte+candidates+` SELECT COUNT(*) FROM ranked`, args...).Scan(&total); err != nil {
				return result, err
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

func searchTitleRankSQL(fields ...string) string {
	values := make([]string, 0, len(fields))
	for _, field := range fields {
		values = append(values, "SELECT "+field+" AS value")
	}
	return `(SELECT MIN(CASE WHEN v.value=q.value COLLATE NOCASE THEN 0 WHEN v.value LIKE q.prefix ESCAPE '\' THEN 1 ELSE 2 END) FROM (` + strings.Join(values, " UNION ALL ") + `) v CROSS JOIN search_query q WHERE v.value LIKE q.contains ESCAPE '\')`
}

// Membership subqueries are deliberately uncorrelated: SQLite builds each
// matching artist/album/track set once. A correlated EXISTS joining all primary
// credits here can rescan the entire role index for every album in the library.
func searchCandidatesSQL(category string) string {
	switch category {
	case "artists", "composers":
		role := "'primary'"
		albumCondition := `EXISTS(SELECT 1 FROM album_artists aa JOIN chain c ON c.id=aa.artist_id WHERE c.root=ar.id AND ` + albumVisibleSQL("aa.album_id") + `) OR `
		if category == "composers" {
			role = "'composer'"
			albumCondition = ""
		}
		return `,ranked AS (SELECT ar.id,m.score,0 AS relation,COALESCE(ar.user_display_name,ar.display_name) AS title FROM search_matches m JOIN artists ar ON ar.id=m.root WHERE (` + albumCondition + `EXISTS(SELECT 1 FROM track_artists ta JOIN chain c ON c.id=ta.artist_id WHERE c.root=ar.id AND ta.role=` + role + ` AND ` + trackVisibleSQL("ta.track_id") + `)))`
	case "albums":
		return `,album_candidates AS MATERIALIZED (SELECT a.id,COALESCE(a.user_title,a.title) title,` + searchTitleRankSQL("COALESCE(a.user_title,a.title)", "a.reading_title") + ` AS score,
   a.id IN (SELECT aa.album_id FROM album_artists aa JOIN chain c ON c.id=aa.artist_id JOIN search_matches m ON m.root=c.root) AS album_match,
   a.id IN (SELECT t.album_id FROM track_artists ta JOIN chain c ON c.id=ta.artist_id JOIN search_matches m ON m.root=c.root JOIN tracks t ON t.id=ta.track_id WHERE ta.role='primary' AND ` + trackVisibleSQL("t.id") + `) AS track_match,
   CASE WHEN COALESCE(a.user_is_compilation,a.is_compilation,0)=0 AND (SELECT COUNT(*) FROM album_artists aa WHERE aa.album_id=a.id)=1 THEN 0 ELSE 1 END AS collaboration
   FROM albums a WHERE ` + albumVisibleSQL("a.id") + `),ranked AS (SELECT id,COALESCE(score,3) score,CASE WHEN album_match THEN collaboration ELSE 2 END AS relation,title FROM album_candidates WHERE score IS NOT NULL OR album_match OR track_match)`
	case "tracks":
		return `,track_candidates AS MATERIALIZED (SELECT t.id,COALESCE(t.user_title,t.title) title,` + searchTitleRankSQL("COALESCE(t.user_title,t.title)") + ` AS score,
   (t.id IN (SELECT ta.track_id FROM track_artists ta JOIN chain c ON c.id=ta.artist_id JOIN search_matches m ON m.root=c.root WHERE ta.role='primary') OR
   (NOT EXISTS(SELECT 1 FROM track_artists ta WHERE ta.track_id=t.id AND ta.role='primary') AND t.album_id IN (SELECT aa.album_id FROM album_artists aa JOIN chain c ON c.id=aa.artist_id JOIN search_matches m ON m.root=c.root))) AS performer_match
   FROM tracks t WHERE ` + trackVisibleSQL("t.id") + `),ranked AS (SELECT id,COALESCE(score,3) score,0 AS relation,title FROM track_candidates WHERE score IS NOT NULL OR performer_match)`
	}
	panic("unvalidated search category")
}

func (s *Store) searchArtistsByIDs(ctx context.Context, ids []int64, composer bool) ([]Artist, error) {
	result := []Artist{}
	if len(ids) == 0 {
		return result, nil
	}
	placeholders, args := inClause(ids)
	role := "'primary'"
	if composer {
		role = "'composer'"
	}
	rows, err := s.db.QueryContext(ctx, artistMergeChainSQL+` SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name),`+artistImageURLSQL("ar")+`,ar.is_favorite,
 (SELECT COUNT(DISTINCT aa.album_id) FROM album_artists aa JOIN chain c ON c.id=aa.artist_id WHERE c.root=ar.id AND `+albumVisibleSQL("aa.album_id")+`),
 (SELECT COUNT(DISTINCT ta.track_id) FROM track_artists ta JOIN chain c ON c.id=ta.artist_id WHERE c.root=ar.id AND ta.role=`+role+` AND `+trackVisibleSQL("ta.track_id")+`) FROM artists ar WHERE ar.id IN (`+placeholders+`)`, args...)
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
