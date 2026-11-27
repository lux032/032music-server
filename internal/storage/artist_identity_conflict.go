package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type ArtistExternalIDConflictError struct {
	Source, ExternalID, OwnerName string
	OwnerArtistID                 int64
}

func (e *ArtistExternalIDConflictError) Error() string {
	return fmt.Sprintf("该 %s 身份已绑定到歌手 %s", e.Source, e.OwnerName)
}

var ErrIdentityConflictStale = errors.New("身份归属或候选已变化，未执行合并")
var ErrCompositeArtistIdentity = errors.New("疑似合作署名，不能绑定单个艺术家的身份；请先核对 ARTISTS 标签并完整重扫，或手动修正署名")

type artistIdentityQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func artistIdentityOwnerCheck(ctx context.Context, db artistIdentityQuerier, artistID int64, source, externalID string) error {
	var e ArtistExternalIDConflictError
	e.Source, e.ExternalID = source, externalID
	err := db.QueryRowContext(ctx, `SELECT p.artist_id,COALESCE(a.user_display_name,a.display_name) FROM artist_external_profiles p JOIN artists a ON a.id=p.artist_id WHERE p.source=? AND p.external_id=? AND p.artist_id<>?`, source, externalID, artistID).Scan(&e.OwnerArtistID, &e.OwnerName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &e
}

type ArtistIdentityConflict struct {
	CandidateID, OwnerArtistID, TargetArtistID int64
	Source, ExternalID, OwnerName, TargetName  string
	CanMerge                                   bool
}

func artistIdentityConflict(ctx context.Context, db artistIdentityQuerier, artistID, candidateID int64) (ArtistIdentityConflict, error) {
	var v ArtistIdentityConflict
	v.CandidateID = candidateID
	var manualOnly int
	err := db.QueryRowContext(ctx, `SELECT c.source,c.external_id,c.requires_manual_review,p.artist_id,COALESCE(a.user_display_name,a.display_name) FROM artist_match_candidates c JOIN artist_external_profiles p ON p.source=c.source AND p.external_id=c.external_id JOIN artists a ON a.id=p.artist_id WHERE c.id=? AND c.artist_id=? AND c.status='candidate' AND p.artist_id<>?`, candidateID, artistID, artistID).Scan(&v.Source, &v.ExternalID, &manualOnly, &v.OwnerArtistID, &v.OwnerName)
	if err != nil {
		return v, err
	}
	v.TargetArtistID, err = canonicalArtistID(ctx, db, v.OwnerArtistID)
	if err != nil {
		return v, err
	}
	var sourceMerged sql.NullInt64
	if err = db.QueryRowContext(ctx, `SELECT merged_into_artist_id FROM artists WHERE id=?`, artistID).Scan(&sourceMerged); err != nil {
		return v, err
	}
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=?`, v.TargetArtistID).Scan(&v.TargetName); err != nil {
		return v, err
	}
	var sourceName string
	if err = db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=?`, artistID).Scan(&sourceName); err != nil {
		return v, err
	}
	// A formatted collaboration or unsafe tagged identity is not auto-mergeable.
	targetComposite, err := isCompositeArtistTx(ctx, db, v.TargetArtistID, v.TargetName)
	if err != nil {
		return v, err
	}
	sourceComposite, err := isCompositeArtistTx(ctx, db, artistID, sourceName)
	if err != nil {
		return v, err
	}
	v.CanMerge = manualOnly == 0 && !sourceMerged.Valid && v.TargetArtistID != artistID && !targetComposite && !sourceComposite
	return v, nil
}
func hasRolledBackArtistMergePairTx(ctx context.Context, db artistIdentityQuerier, artistA, artistB int64) (bool, error) {
	if artistA <= 0 || artistB <= 0 || artistA == artistB {
		return false, nil
	}
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM artist_merge_operations
		WHERE status='rolled_back'
		  AND ((source_artist_id=? AND target_artist_id=?) OR (source_artist_id=? AND target_artist_id=?))
	)`, artistA, artistB, artistB, artistA).Scan(&exists)
	return exists, err
}

// HasRolledBackArtistMergePair reports whether an admin previously rolled back
// any merge between artistA and artistB in either direction.
func (s *Store) HasRolledBackArtistMergePair(ctx context.Context, artistA, artistB int64) (bool, error) {
	return hasRolledBackArtistMergePairTx(ctx, s.db, artistA, artistB)
}

// ArtistProfileNameMatches checks whether a local artist name matches the
// display name, sort name, flat aliases, or structured aliases of an external
// artist profile after whitespace/case normalization.
func ArtistProfileNameMatches(name string, profile ExternalArtistProfile) bool {
	local := normalizeArtistName(name)
	if local == "" {
		return false
	}
	for _, value := range append([]string{profile.DisplayName, profile.SortName}, profile.Aliases...) {
		if normalizeArtistName(value) == local {
			return true
		}
	}
	structured := profile.StructuredAliases
	if len(structured) == 0 {
		structured = parseStructuredAliasesFromRaw(profile.Raw)
	}
	for _, alias := range structured {
		if normalizeArtistName(alias.Name) == local {
			return true
		}
	}
	return false
}

func (s *Store) ArtistIdentityConflict(ctx context.Context, artistID, candidateID int64) (ArtistIdentityConflict, error) {
	return artistIdentityConflict(ctx, s.db, artistID, candidateID)
}
func (s *Store) MergeArtistsForIdentityConflict(ctx context.Context, sourceID, candidateID, expectedTargetID int64) (int64, error) {
	var operation int64
	err := withBusyRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		conflict, err := artistIdentityConflict(ctx, tx, sourceID, candidateID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrIdentityConflictStale
		}
		if err != nil {
			return err
		}
		if !conflict.CanMerge || expectedTargetID <= 0 || conflict.TargetArtistID != expectedTargetID {
			return ErrIdentityConflictStale
		}
		operation, err = mergeArtistsTx(ctx, tx, sourceID, expectedTargetID)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return 0, err
	}
	return operation, nil
}

// ArtistConflictConvergenceResult summarizes a deterministic backfill merge run
// triggered by an administrator to converge pre-existing same-MBID duplicates.
type ArtistConflictConvergenceResult struct {
	Merged  int
	Skipped int
	Failed  int
}

type deterministicArtistConflict struct {
	SourceArtistID int64
	TargetArtistID int64
	CandidateID    int64
	Source         string
	ExternalID     string
}

func (s *Store) findDeterministicArtistConflicts(ctx context.Context) ([]deterministicArtistConflict, error) {
	seenSource := map[int64]bool{}
	var result []deterministicArtistConflict

	// 1. Pending high-confidence MusicBrainz candidates (>=92, requires_manual_review=0)
	// whose external_id is already owned by another canonical artist.
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.artist_id, c.source, c.external_id, p.artist_id, COALESCE(src.user_display_name, src.display_name)
		FROM artist_match_candidates c
		JOIN artists src ON src.id = c.artist_id
		JOIN artist_external_profiles p ON p.source = c.source AND p.external_id = c.external_id
		WHERE c.source = 'musicbrainz'
		  AND c.status = 'candidate'
		  AND c.score >= 92
		  AND c.requires_manual_review = 0
		  AND src.merged_into_artist_id IS NULL
		  AND p.artist_id <> c.artist_id
		  AND NOT EXISTS(SELECT 1 FROM artist_external_profiles own WHERE own.artist_id = c.artist_id AND own.source = 'musicbrainz')
		ORDER BY c.score DESC, c.id ASC`)
	if err != nil {
		return nil, err
	}
	type candRow struct {
		candID, srcID, ownerID   int64
		source, extID, srcName   string
	}
	var candRows []candRow
	for rows.Next() {
		var r candRow
		if err = rows.Scan(&r.candID, &r.srcID, &r.source, &r.extID, &r.ownerID, &r.srcName); err != nil {
			rows.Close()
			return nil, err
		}
		candRows = append(candRows, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	for _, r := range candRows {
		if seenSource[r.srcID] {
			continue
		}
		targetID, err := canonicalArtistID(ctx, s.db, r.ownerID)
		if err != nil || targetID == r.srcID {
			continue
		}
		var targetName string
		if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name, display_name) FROM artists WHERE id=?`, targetID).Scan(&targetName); err != nil {
			continue
		}
		if srcComp, err := isCompositeArtistTx(ctx, s.db, r.srcID, r.srcName); err != nil || srcComp {
			continue
		}
		if tgtComp, err := isCompositeArtistTx(ctx, s.db, targetID, targetName); err != nil || tgtComp {
			continue
		}
		if rolledBack, err := hasRolledBackArtistMergePairTx(ctx, s.db, r.srcID, targetID); err != nil || rolledBack {
			continue
		}
		seenSource[r.srcID] = true
		result = append(result, deterministicArtistConflict{
			SourceArtistID: r.srcID,
			TargetArtistID: targetID,
			CandidateID:    r.candID,
			Source:         r.source,
			ExternalID:     r.extID,
		})
	}

	// 2. Unbound unmerged artists with verified TaggedMBID matching another
	// canonical artist's MusicBrainz profile AND passing ArtistProfileNameMatches.
	taggedMBIDs, err := s.artistTaggedMBIDs(ctx, 0)
	if err != nil {
		return nil, err
	}
	// Query unbound unmerged artists deterministically ordered by ID.
	aRows, err := s.db.QueryContext(ctx, `SELECT a.id, COALESCE(a.user_display_name, a.display_name)
		FROM artists a
		WHERE a.merged_into_artist_id IS NULL
		  AND NOT EXISTS(SELECT 1 FROM artist_external_profiles own WHERE own.artist_id = a.id AND own.source = 'musicbrainz')
		ORDER BY a.id ASC`)
	if err != nil {
		return nil, err
	}
	type unboundRow struct {
		id   int64
		name string
	}
	var unbound []unboundRow
	for aRows.Next() {
		var u unboundRow
		if err = aRows.Scan(&u.id, &u.name); err != nil {
			aRows.Close()
			return nil, err
		}
		unbound = append(unbound, u)
	}
	if err = aRows.Err(); err != nil {
		aRows.Close()
		return nil, err
	}
	aRows.Close()

	for _, u := range unbound {
		if seenSource[u.id] {
			continue
		}
		mbid := taggedMBIDs[u.id]
		if mbid == "" {
			continue
		}
		profile, ownerID, pErr := s.ExternalArtistProfileByID(ctx, "musicbrainz", mbid)
		if pErr != nil {
			continue
		}
		targetID, err := canonicalArtistID(ctx, s.db, ownerID)
		if err != nil || targetID == u.id {
			continue
		}
		if !ArtistProfileNameMatches(u.name, profile) {
			continue
		}
		var targetName string
		if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name, display_name) FROM artists WHERE id=?`, targetID).Scan(&targetName); err != nil {
			continue
		}
		if srcComp, err := isCompositeArtistTx(ctx, s.db, u.id, u.name); err != nil || srcComp {
			continue
		}
		if tgtComp, err := isCompositeArtistTx(ctx, s.db, targetID, targetName); err != nil || tgtComp {
			continue
		}
		if rolledBack, err := hasRolledBackArtistMergePairTx(ctx, s.db, u.id, targetID); err != nil || rolledBack {
			continue
		}
		seenSource[u.id] = true
		result = append(result, deterministicArtistConflict{
			SourceArtistID: u.id,
			TargetArtistID: targetID,
			Source:         "musicbrainz",
			ExternalID:     mbid,
		})
	}

	return result, nil
}

// CountConvergeableArtistConflicts returns the number of pre-existing duplicate
// artists eligible for safe deterministic same-MBID convergence.
func (s *Store) CountConvergeableArtistConflicts(ctx context.Context) (int, error) {
	items, err := s.findDeterministicArtistConflicts(ctx)
	if err != nil {
		return 0, err
	}
	return len(items), nil
}

// ConvergeDeterministicArtistConflicts batch-merges pre-existing same-MBID
// duplicate artists that satisfy all deterministic safety guards, writing
// auditable backfill merge records into artist_merge_operations.
func (s *Store) ConvergeDeterministicArtistConflicts(ctx context.Context) (ArtistConflictConvergenceResult, error) {
	items, err := s.findDeterministicArtistConflicts(ctx)
	if err != nil {
		return ArtistConflictConvergenceResult{}, err
	}
	var out ArtistConflictConvergenceResult
	for _, item := range items {
		var merged bool
		txErr := withBusyRetry(ctx, func() error {
			merged = false
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()

			var srcName string
			var srcMerged sql.NullInt64
			if err = tx.QueryRowContext(ctx, `SELECT COALESCE(user_display_name, display_name), merged_into_artist_id FROM artists WHERE id=?`, item.SourceArtistID).Scan(&srcName, &srcMerged); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil
				}
				return err
			}
			if srcMerged.Valid {
				return nil
			}
			var ownMB int
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artist_external_profiles WHERE artist_id=? AND source='musicbrainz'`, item.SourceArtistID).Scan(&ownMB); err != nil {
				return err
			}
			if ownMB > 0 {
				return nil
			}
			targetID, err := canonicalArtistID(ctx, tx, item.TargetArtistID)
			if err != nil || targetID == item.SourceArtistID {
				return err
			}
			var targetName string
			if err = tx.QueryRowContext(ctx, `SELECT COALESCE(user_display_name, display_name) FROM artists WHERE id=?`, targetID).Scan(&targetName); err != nil {
				return err
			}
			if srcComp, cErr := isCompositeArtistTx(ctx, tx, item.SourceArtistID, srcName); cErr != nil || srcComp {
				return cErr
			}
			if tgtComp, cErr := isCompositeArtistTx(ctx, tx, targetID, targetName); cErr != nil || tgtComp {
				return cErr
			}
			if rolledBack, rbErr := hasRolledBackArtistMergePairTx(ctx, tx, item.SourceArtistID, targetID); rbErr != nil || rolledBack {
				return rbErr
			}
			if item.CandidateID > 0 {
				var status string
				var manualOnly, score int
				err = tx.QueryRowContext(ctx, `SELECT status, requires_manual_review, score FROM artist_match_candidates WHERE id=? AND artist_id=?`, item.CandidateID, item.SourceArtistID).Scan(&status, &manualOnly, &score)
				if errors.Is(err, sql.ErrNoRows) || status != "candidate" || manualOnly != 0 || score < 92 {
					return nil
				}
				if err != nil {
					return err
				}
			}
			if _, err = mergeArtistsWithTriggerTx(ctx, tx, item.SourceArtistID, targetID, MergeTriggerBackfill); err != nil {
				return err
			}
			if item.CandidateID > 0 {
				if _, err = tx.ExecContext(ctx, `UPDATE artist_match_candidates SET status=CASE WHEN id=? THEN 'confirmed' ELSE 'rejected' END WHERE artist_id=? AND source=? AND status='candidate'`, item.CandidateID, item.SourceArtistID, item.Source); err != nil {
					return err
				}
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			merged = true
			return nil
		})
		if txErr != nil {
			out.Failed++
		} else if merged {
			out.Merged++
		} else {
			out.Skipped++
		}
	}
	return out, nil
}
