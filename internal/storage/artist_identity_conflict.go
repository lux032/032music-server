package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
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
	err := db.QueryRowContext(ctx, `SELECT c.source,c.external_id,p.artist_id,COALESCE(a.user_display_name,a.display_name) FROM artist_match_candidates c JOIN artist_external_profiles p ON p.source=c.source AND p.external_id=c.external_id JOIN artists a ON a.id=p.artist_id WHERE c.id=? AND c.artist_id=? AND c.status='candidate' AND p.artist_id<>?`, candidateID, artistID, artistID).Scan(&v.Source, &v.ExternalID, &v.OwnerArtistID, &v.OwnerName)
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
	// A formatted collaboration is not an alias of one of its contributors.
	v.CanMerge = !sourceMerged.Valid && v.TargetArtistID != artistID && !metadata.CompositeArtistCredit(v.TargetName) && !metadata.CompositeArtistCredit(sourceName)
	return v, nil
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
