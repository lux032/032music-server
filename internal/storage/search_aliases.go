package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidSearchAlias = errors.New("search alias must contain 1 to 200 characters")

type ArtistSearchAlias struct {
	ID       int64  `json:"id"`
	ArtistID int64  `json:"artistId"`
	Value    string `json:"value"`
}

// Search aliases stay on their original owner so merging and rolling back a
// merge inherit and restore them without copying or losing user edits.
func (s *Store) ArtistSearchAliases(ctx context.Context, artistID int64) ([]ArtistSearchAlias, error) {
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE members(id) AS (SELECT ? UNION SELECT a.id FROM artists a JOIN members m ON a.merged_into_artist_id=m.id) SELECT x.id,x.artist_id,x.value FROM artist_search_aliases x JOIN members m ON m.id=x.artist_id ORDER BY x.value COLLATE NOCASE,x.id`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ArtistSearchAlias{}
	for rows.Next() {
		var item ArtistSearchAlias
		if err := rows.Scan(&item.ID, &item.ArtistID, &item.Value); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AddArtistSearchAlias(ctx context.Context, artistID int64, value string) error {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > 200 {
		return ErrInvalidSearchAlias
	}
	id, err := s.CanonicalArtistID(ctx, artistID)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO artist_search_aliases(artist_id,value) VALUES(?,?) ON CONFLICT DO NOTHING`, id, value)
	return err
}

func (s *Store) DeleteArtistSearchAlias(ctx context.Context, artistID, aliasID int64) error {
	// Only an alias in this artist's merge tree can be removed.
	result, err := s.db.ExecContext(ctx, `WITH RECURSIVE members(id) AS (SELECT ? UNION SELECT a.id FROM artists a JOIN members m ON a.merged_into_artist_id=m.id) DELETE FROM artist_search_aliases WHERE id=? AND artist_id IN (SELECT id FROM members)`, artistID, aliasID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}

func literalSearchPattern(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// searchArtistCTE folds merged identities into their canonical root, retaining
// original names and bound profiles. Candidate profiles are deliberately absent.
// Language variants are literal substrings, never SQL LIKE wildcards.
func searchArtistCTE(query string) (string, []any) {
	variants := SearchVariants(query)
	if len(variants) == 0 {
		variants = []string{""}
	}
	values := make([]string, 0, len(variants))
	args := make([]any, 0, len(variants)*3)
	for _, v := range variants {
		values = append(values, "(?,?,?)")
		escaped := literalSearchPattern(v)
		args = append(args, v, escaped+"%", "%"+escaped+"%")
	}
	return artistMergeChainSQL + `, search_query(value,prefix,contains) AS MATERIALIZED (VALUES ` + strings.Join(values, ",") + `), search_names(root,value) AS (
 SELECT c.root,a.display_name FROM chain c JOIN artists a ON a.id=c.id
 UNION ALL SELECT c.root,a.user_display_name FROM chain c JOIN artists a ON a.id=c.id WHERE a.user_display_name IS NOT NULL
 UNION ALL SELECT c.root,a.reading_name FROM chain c JOIN artists a ON a.id=c.id WHERE a.reading_name IS NOT NULL
 UNION ALL SELECT c.root,n.value FROM chain c JOIN artist_names n ON n.artist_id=c.id
 UNION ALL SELECT c.root,n.value FROM chain c JOIN artist_search_aliases n ON n.artist_id=c.id
 UNION ALL SELECT c.root,p.display_name FROM chain c JOIN artist_external_profiles p ON p.artist_id=c.id
 UNION ALL SELECT c.root,p.sort_name FROM chain c JOIN artist_external_profiles p ON p.artist_id=c.id WHERE p.sort_name IS NOT NULL
 UNION ALL SELECT c.root,j.value FROM chain c JOIN artist_external_profiles p ON p.artist_id=c.id JOIN json_each(CASE WHEN json_valid(p.aliases_json) THEN p.aliases_json ELSE '[]' END) j WHERE j.type='text'
 ), search_matches(root,score) AS MATERIALIZED (
 SELECT n.root,MIN(CASE WHEN n.value=q.value COLLATE NOCASE THEN 0 WHEN n.value LIKE q.prefix ESCAPE '\' THEN 1 ELSE 2 END)
 FROM search_names n CROSS JOIN search_query q WHERE n.value<>'' AND n.value LIKE q.contains ESCAPE '\' GROUP BY n.root
 ) `, args
}

func artistSearchCondition(idExpr, query string) (string, []any) {
	cte, args := searchArtistCTE(query)
	return idExpr + ` IN (` + cte + `SELECT root FROM search_matches)`, args
}
