package storage

import "context"

// AddArtistCandidates upserts manually searched candidates without pruning
// existing ones. Existing manual decisions (confirmed/rejected) are preserved.
func (s *Store) AddArtistCandidates(ctx context.Context, artistID int64, candidates []ArtistCandidate) error {
	return withBusyRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = replaceArtistCandidatesTx(ctx, tx, artistID, candidates, nil); err != nil {
			return err
		}
		return tx.Commit()
	})
}

// ArtistCandidateID returns the candidate row for one external identity.
func (s *Store) ArtistCandidateID(ctx context.Context, artistID int64, source, externalID string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM artist_match_candidates WHERE artist_id=? AND source=? AND external_id=?`, artistID, source, externalID).Scan(&id)
	return id, err
}
