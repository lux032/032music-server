package storage

import (
	"context"
	"errors"
)

// ResetArtistIdentity only removes the explicitly previewed binding. It never
// touches library relations, user biographies, favorites, or custom images.
func (s *Store) ResetArtistIdentity(ctx context.Context, artistID int64, source, expectedID string) error {
	if source != "musicbrainz" && source != "lastfm" {
		return errors.New("不支持的身份来源")
	}
	return withBusyRetry(ctx, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(ctx, `DELETE FROM artist_external_profiles WHERE artist_id=? AND source=? AND external_id=?`, artistID, source, expectedID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrIdentityConflictStale
		}
		// Reset only this source's prior decisions so a correct identity may be
		// reviewed again. Cross-source bindings require their own explicit reset.
		for _, statement := range []string{
			`DELETE FROM artist_match_candidates WHERE artist_id=? AND source=?`,
			`DELETE FROM artist_image_cache WHERE artist_id=? AND source=?`,
		} {
			if _, err = tx.ExecContext(ctx, statement, artistID, source); err != nil {
				return err
			}
		}
		if source == "musicbrainz" {
			_, err = tx.ExecContext(ctx, `DELETE FROM artist_biographies WHERE artist_id=? AND source IN ('wikipedia','lastfm')`, artistID)
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM artist_biographies WHERE artist_id=? AND source='lastfm'`, artistID)
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE artists SET biography=NULL WHERE id=?`, artistID)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
}
