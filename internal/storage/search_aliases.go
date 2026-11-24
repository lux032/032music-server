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
	Source   string `json:"source"`
}

type searchQueryVariant struct {
	value    string
	prefix   string
	contains string
}

func buildSearchQueryVariants(query string) []searchQueryVariant {
	raw := SearchVariants(query)
	if len(raw) == 0 {
		raw = []string{""}
	}
	variants := make([]searchQueryVariant, 0, len(raw))
	for _, v := range raw {
		escaped := literalSearchPattern(v)
		variants = append(variants, searchQueryVariant{
			value:    v,
			prefix:   escaped + "%",
			contains: "%" + escaped + "%",
		})
	}
	return variants
}

func variantsJSONPrefilterSafe(variants []searchQueryVariant) bool {
	for _, v := range variants {
		for _, r := range v.value {
			if r < 0x20 || r == '\\' || r == '"' || r == '<' || r == '>' || r == '&' {
				return false
			}
		}
	}
	return true
}

func searchLikeCondition(variants []searchQueryVariant, fields ...string) (string, []any) {
	parts := make([]string, 0, len(fields)*len(variants))
	args := make([]any, 0, len(fields)*len(variants))
	for _, field := range fields {
		for _, v := range variants {
			parts = append(parts, field+` LIKE ? ESCAPE '\'`)
			args = append(args, v.contains)
		}
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func searchScoreExpr(variants []searchQueryVariant, fields ...string) (string, []any) {
	exactParts := make([]string, 0, len(fields)*len(variants))
	exactArgs := make([]any, 0, len(fields)*len(variants))
	prefixParts := make([]string, 0, len(fields)*len(variants))
	prefixArgs := make([]any, 0, len(fields)*len(variants))
	containsParts := make([]string, 0, len(fields)*len(variants))
	containsArgs := make([]any, 0, len(fields)*len(variants))
	for _, field := range fields {
		for _, v := range variants {
			exactParts = append(exactParts, field+`=? COLLATE NOCASE`)
			exactArgs = append(exactArgs, v.value)
			prefixParts = append(prefixParts, field+` LIKE ? ESCAPE '\'`)
			prefixArgs = append(prefixArgs, v.prefix)
			containsParts = append(containsParts, field+` LIKE ? ESCAPE '\'`)
			containsArgs = append(containsArgs, v.contains)
		}
	}
	args := make([]any, 0, len(exactArgs)+len(prefixArgs)+len(containsArgs))
	args = append(args, exactArgs...)
	args = append(args, prefixArgs...)
	args = append(args, containsArgs...)
	expr := `CASE WHEN ` + strings.Join(exactParts, " OR ") + ` THEN 0 WHEN ` + strings.Join(prefixParts, " OR ") + ` THEN 1 WHEN ` + strings.Join(containsParts, " OR ") + ` THEN 2 END`
	return expr, args
}

// Search aliases stay on their original owner so merging and rolling back a
// merge inherit and restore them without copying or losing user edits.
func (s *Store) ArtistSearchAliases(ctx context.Context, artistID int64) ([]ArtistSearchAlias, error) {
	rows, err := s.db.QueryContext(ctx, `WITH RECURSIVE members(id) AS (SELECT ? UNION SELECT a.id FROM artists a JOIN members m ON a.merged_into_artist_id=m.id) SELECT x.id,x.artist_id,x.value,x.source FROM artist_search_aliases x JOIN members m ON m.id=x.artist_id ORDER BY x.value COLLATE NOCASE,x.id`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ArtistSearchAlias{}
	for rows.Next() {
		var item ArtistSearchAlias
		if err := rows.Scan(&item.ID, &item.ArtistID, &item.Value, &item.Source); err != nil {
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

// matchedArtistRootsCTE builds a CTE (`search_matches(root, score)`) that scans
// each artist name/alias/profile table once, pre-filters aliases_json before
// invoking json_each when safe, and walks merged_into_artist_id upward only for
// matched rows instead of expanding the full library merge chain up front.
func matchedArtistRootsCTE(variants []searchQueryVariant) (string, []any) {
	var args []any

	aCond, aCondArgs := searchLikeCondition(variants, "a.display_name", "COALESCE(a.user_display_name,'')", "COALESCE(a.reading_name,'')")
	aScore, aScoreArgs := searchScoreExpr(variants, "a.display_name", "COALESCE(a.user_display_name,'')", "COALESCE(a.reading_name,'')")

	nCond, nCondArgs := searchLikeCondition(variants, "n.value")
	nScore, nScoreArgs := searchScoreExpr(variants, "n.value")

	saCond, saCondArgs := searchLikeCondition(variants, "sa.value")
	saScore, saScoreArgs := searchScoreExpr(variants, "sa.value")

	pCond, pCondArgs := searchLikeCondition(variants, "p.display_name", "COALESCE(p.sort_name,'')")
	pScore, pScoreArgs := searchScoreExpr(variants, "p.display_name", "COALESCE(p.sort_name,'')")

	jCond, jCondArgs := searchLikeCondition(variants, "j.value")
	jScore, jScoreArgs := searchScoreExpr(variants, "j.value")

	jsonPrefilter := ""
	var jsonPrefilterArgs []any
	if variantsJSONPrefilterSafe(variants) {
		rawCond, rawArgs := searchLikeCondition(variants, "p.aliases_json")
		jsonPrefilter = ` AND (instr(p.aliases_json,'\')>0 OR ` + rawCond + `)`
		jsonPrefilterArgs = rawArgs
	}

	args = append(args, aScoreArgs...)
	args = append(args, aCondArgs...)
	args = append(args, nScoreArgs...)
	args = append(args, nCondArgs...)
	args = append(args, saScoreArgs...)
	args = append(args, saCondArgs...)
	args = append(args, pScoreArgs...)
	args = append(args, pCondArgs...)
	args = append(args, jScoreArgs...)
	args = append(args, jsonPrefilterArgs...)
	args = append(args, jCondArgs...)

	cte := `WITH RECURSIVE raw_artist_hits(artist_id,score) AS (` +
		`SELECT a.id,` + aScore + ` FROM artists a WHERE ` + aCond +
		` UNION ALL SELECT n.artist_id,` + nScore + ` FROM artist_names n WHERE n.value<>'' AND ` + nCond +
		` UNION ALL SELECT sa.artist_id,` + saScore + ` FROM artist_search_aliases sa WHERE sa.value<>'' AND ` + saCond +
		` UNION ALL SELECT p.artist_id,` + pScore + ` FROM artist_external_profiles p WHERE ` + pCond +
		` UNION ALL SELECT p.artist_id,` + jScore + ` FROM artist_external_profiles p JOIN json_each(CASE WHEN json_valid(p.aliases_json) THEN p.aliases_json ELSE '[]' END) j ON 1=1 WHERE p.aliases_json IS NOT NULL AND p.aliases_json<>'' AND p.aliases_json<>'[]'` + jsonPrefilter + ` AND j.type='text' AND j.value<>'' AND ` + jCond +
		`), raw_artist_best(artist_id,score) AS MATERIALIZED (` +
		`SELECT artist_id,MIN(score) FROM raw_artist_hits WHERE score IS NOT NULL GROUP BY artist_id` +
		`), hit_to_root(id,next_id,score) AS (` +
		`SELECT a.id,a.merged_into_artist_id,h.score FROM raw_artist_best h JOIN artists a ON a.id=h.artist_id` +
		` UNION ALL SELECT a.id,a.merged_into_artist_id,t.score FROM hit_to_root t JOIN artists a ON a.id=t.next_id` +
		`), search_matches(root,score) AS MATERIALIZED (` +
		`SELECT id,MIN(score) FROM hit_to_root WHERE next_id IS NULL GROUP BY id` +
		`) `
	return cte, args
}

// searchArtistCTE folds merged identities into their canonical root, retaining
// original names and bound profiles. Candidate profiles are deliberately absent.
// Language variants are literal substrings, never SQL LIKE wildcards.
func searchArtistCTE(query string) (string, []any) {
	return matchedArtistRootsCTE(buildSearchQueryVariants(query))
}

func artistSearchCondition(idExpr, query string) (string, []any) {
	cte, args := searchArtistCTE(query)
	return idExpr + ` IN (` + cte + `SELECT root FROM search_matches)`, args
}
