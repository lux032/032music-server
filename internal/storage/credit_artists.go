package storage

import (
	"context"
	"strings"
)

// CreditArtistFilters belongs to the web credit directory, not public API filters.
type CreditArtistFilters struct {
	Role, Query, Index, Sort string
	Limit, Offset            int
}

// Only credits on tracks with an available file count (see visibility.go).
const artistMergeChainSQL = `WITH RECURSIVE chain(id,root) AS (SELECT id,id FROM artists WHERE merged_into_artist_id IS NULL UNION SELECT a.id,c.root FROM artists a JOIN chain c ON a.merged_into_artist_id=c.id)`

var creditArtistChain = artistMergeChainSQL + `, credits AS (SELECT c.root,COUNT(DISTINCT ta.track_id) track_count FROM track_artists ta JOIN chain c ON c.id=ta.artist_id WHERE ta.role IN ('composer','lyricist','arranger','producer') AND (?='all' OR ta.role=?) AND ` + trackVisibleSQL("ta.track_id") + ` GROUP BY c.root) `

func creditArtistChainForRole(role string) (string, []any) {
	if IsCreditRole(role) {
		return artistMergeChainSQL + `, credits AS (SELECT c.root,COUNT(DISTINCT ta.track_id) track_count FROM track_artists ta JOIN chain c ON c.id=ta.artist_id WHERE ta.role=? AND ` + trackVisibleSQL("ta.track_id") + ` GROUP BY c.root) `, []any{role}
	}
	return artistMergeChainSQL + `, credits AS (SELECT c.root,COUNT(DISTINCT ta.track_id) track_count FROM track_artists ta JOIN chain c ON c.id=ta.artist_id WHERE ta.role IN ('composer','lyricist','arranger','producer') AND ` + trackVisibleSQL("ta.track_id") + ` GROUP BY c.root) `, nil
}

func creditArtistWhere(f CreditArtistFilters) (string, []any) {
	clauses := []string{"ar.merged_into_artist_id IS NULL"}
	var args []any
	if strings.TrimSpace(f.Query) != "" {
		condition, queryArgs := artistSearchCondition("ar.id", f.Query)
		clauses = append(clauses, condition)
		args = append(args, queryArgs...)
	}
	if c, a := IndexCondition("COALESCE(NULLIF(ar.reading_name,''),ar.sort_name,COALESCE(ar.user_display_name,ar.display_name))", f.Index); c != "" {
		clauses = append(clauses, c)
		args = append(args, a...)
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Store) ListCreditArtists(ctx context.Context, f CreditArtistFilters) ([]Artist, error) {
	chainSQL, chainArgs := creditArtistChainForRole(f.Role)
	where, whereArgs := creditArtistWhere(f)
	order := "name COLLATE NOCASE,ar.id"
	if f.Sort == "tracks" {
		order = "credits.track_count DESC," + order
	}
	limit, offset := page(Filters{Limit: f.Limit, Offset: f.Offset})
	args := make([]any, 0, len(chainArgs)+len(whereArgs)+2)
	args = append(args, chainArgs...)
	args = append(args, whereArgs...)
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, chainSQL+`SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name) name,`+artistImageURLSQL("ar")+`,credits.track_count,ar.is_favorite FROM credits JOIN artists ar ON ar.id=credits.root WHERE `+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []Artist{}
	for rows.Next() {
		var a Artist
		var favorite int
		if err = rows.Scan(&a.ID, &a.Name, &a.ImageURL, &a.TrackCount, &favorite); err != nil {
			return nil, err
		}
		a.IsFavorite = favorite != 0
		list = append(list, a)
	}
	return list, rows.Err()
}

func (s *Store) CountCreditArtists(ctx context.Context, f CreditArtistFilters) (int64, error) {
	chainSQL, chainArgs := creditArtistChainForRole(f.Role)
	where, whereArgs := creditArtistWhere(f)
	args := make([]any, 0, len(chainArgs)+len(whereArgs))
	args = append(args, chainArgs...)
	args = append(args, whereArgs...)
	var n int64
	err := s.db.QueryRowContext(ctx, chainSQL+`SELECT COUNT(*) FROM credits JOIN artists ar ON ar.id=credits.root WHERE `+where, args...).Scan(&n)
	return n, err
}
