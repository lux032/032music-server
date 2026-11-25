package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/lux032/032music-server/internal/metadata"
)

// ExemptCompositeArtist marks an artist whose name contains composite separators
// (e.g. "Simon & Garfunkel") as a verified single unit, clearing any _composite
// skip state so it exits Tab 2 and becomes eligible for normal identity matching.
func (s *Store) ExemptCompositeArtist(ctx context.Context, artistID int64, reason string) error {
	if artistID <= 0 {
		return errors.New("无效的艺术家 ID")
	}
	return withBusyRetry(ctx, func() error {
		tx, err := s.beginWrite(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()

		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE id=? AND merged_into_artist_id IS NULL`, artistID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_composite_exemptions(artist_id,reason) VALUES(?,?) ON CONFLICT(artist_id) DO UPDATE SET reason=excluded.reason`, artistID, strings.TrimSpace(reason)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM artist_source_match_state WHERE artist_id=? AND source=?`, artistID, compositeStateSource); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// IsCompositeArtist reports whether an artist should be treated as a composite
// credit, taking persisted single-artist exemptions into account.
func (s *Store) IsCompositeArtist(ctx context.Context, artistID int64, name string) (bool, error) {
	return isCompositeArtistTx(ctx, s.db, artistID, name)
}

func isCompositeArtistTx(ctx context.Context, q artistIdentityQuerier, artistID int64, name string) (bool, error) {
	if !metadata.CompositeArtistCredit(name) {
		return false, nil
	}
	if artistID <= 0 {
		return true, nil
	}
	var exempt bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artist_composite_exemptions WHERE artist_id=?)`, artistID).Scan(&exempt); err != nil {
		if strings.Contains(err.Error(), "no such table: artist_composite_exemptions") {
			return true, nil
		}
		return false, err
	}
	return !exempt, nil
}

func compositeExemptArtistIDs(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) (map[int64]bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT artist_id FROM artist_composite_exemptions`)
	if err != nil {
		if strings.Contains(err.Error(), "no such table: artist_composite_exemptions") {
			return map[int64]bool{}, nil
		}
		return nil, err
	}
	defer rows.Close()
	exempt := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		exempt[id] = true
	}
	return exempt, rows.Err()
}

func isCompositeWithExemptions(exempt map[int64]bool, artistID int64, name string) bool {
	return metadata.CompositeArtistCredit(name) && !exempt[artistID]
}

// HighConfidenceCreditSplitCandidate represents an unapplied AI composite split
// recommendation meeting the confidence threshold and ready for batch application.
type HighConfidenceCreditSplitCandidate struct {
	ArtistID   int64
	ArtistName string
	DecisionID int64
	Confidence int
	Parts      []string
}

func (s *Store) HighConfidencePendingCreditSplits(ctx context.Context, minConfidence int) ([]HighConfidenceCreditSplitCandidate, error) {
	if minConfidence <= 0 {
		minConfidence = 92
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,ar.id,COALESCE(ar.user_display_name,ar.display_name),d.confidence,d.payload_json
		FROM ai_review_decisions d
		JOIN artists ar ON ar.id=d.target_id
		WHERE d.target_kind='credit_split'
		  AND d.applied=0
		  AND d.reverted=0
		  AND d.action='recommend_accept'
		  AND d.confidence>=?
		  AND ar.merged_into_artist_id IS NULL
		  AND NOT EXISTS(SELECT 1 FROM artist_composite_exemptions ex WHERE ex.artist_id=ar.id)
		  AND NOT EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=ar.id)
		  AND (EXISTS(SELECT 1 FROM track_artists ta WHERE ta.artist_id=ar.id) OR EXISTS(SELECT 1 FROM album_artists aa WHERE aa.artist_id=ar.id))
		ORDER BY d.confidence DESC,ar.id ASC`, minConfidence)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HighConfidenceCreditSplitCandidate
	for rows.Next() {
		var c HighConfidenceCreditSplitCandidate
		var payloadRaw string
		if err = rows.Scan(&c.DecisionID, &c.ArtistID, &c.ArtistName, &c.Confidence, &payloadRaw); err != nil {
			return nil, err
		}
		if !metadata.CompositeArtistCredit(c.ArtistName) {
			continue
		}
		var payload struct {
			IsComposite bool     `json:"isComposite"`
			Parts       []string `json:"parts"`
		}
		if json.Unmarshal([]byte(payloadRaw), &payload) != nil || !payload.IsComposite || len(payload.Parts) < 2 {
			continue
		}
		c.Parts = payload.Parts
		out = append(out, c)
	}
	return out, rows.Err()
}

// ArtistRoleSummary summarizes how many tracks/albums reference an artist under a role.
type ArtistRoleSummary struct {
	Role  string
	Count int
}

func (s *Store) ArtistRoleBreakdownForIDs(ctx context.Context, artistIDs []int64) (map[int64][]ArtistRoleSummary, error) {
	out := make(map[int64][]ArtistRoleSummary, len(artistIDs))
	if len(artistIDs) == 0 {
		return out, nil
	}
	encoded, err := json.Marshal(artistIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT artist_id, 'album_artist' AS role, COUNT(DISTINCT album_id) AS cnt
		FROM album_artists
		WHERE artist_id IN (SELECT value FROM json_each(?))
		GROUP BY artist_id
		UNION ALL
		SELECT artist_id, role, COUNT(DISTINCT track_id) AS cnt
		FROM track_artists
		WHERE artist_id IN (SELECT value FROM json_each(?))
		GROUP BY artist_id, role`, string(encoded), string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var aid int64
		var role string
		var cnt int
		if err = rows.Scan(&aid, &role, &cnt); err != nil {
			return nil, err
		}
		if cnt > 0 {
			out[aid] = append(out[aid], ArtistRoleSummary{Role: role, Count: cnt})
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	roleRank := func(role string) int {
		switch role {
		case "album_artist":
			return 0
		case "primary":
			return 1
		case "composer":
			return 2
		case "lyricist":
			return 3
		case "arranger":
			return 4
		case "producer":
			return 5
		default:
			return 6
		}
	}
	for aid := range out {
		sort.Slice(out[aid], func(i, j int) bool {
			ri, rj := roleRank(out[aid][i].Role), roleRank(out[aid][j].Role)
			if ri != rj {
				return ri < rj
			}
			return out[aid][i].Role < out[aid][j].Role
		})
	}
	return out, nil
}
