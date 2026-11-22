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
		// Remember the wrong identity as a manual rejection before the binding is
		// removed; otherwise the next rematch re-discovers and auto-binds it.
		if _, err = tx.ExecContext(ctx, `INSERT INTO artist_match_candidates(artist_id,source,external_id,display_name,sort_name,disambiguation,country,artist_type,mbid,score,evidence_json,payload_json,status) SELECT artist_id,source,external_id,display_name,sort_name,disambiguation,country,artist_type,CASE WHEN source='musicbrainz' OR external_id GLOB '????????-????-????-????-????????????' THEN external_id ELSE '' END,0,'["已手动解除为错误身份"]',json_object('pageUrl',COALESCE(page_url,'')),'rejected' FROM artist_external_profiles WHERE artist_id=? AND source=? AND external_id=? ON CONFLICT(artist_id,source,external_id) DO UPDATE SET status='rejected'`, artistID, source, expectedID); err != nil {
			return err
		}
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
		// reviewed again. Cross-source bindings require their own explicit reset,
		// while fallback avatars/cooldowns/candidates anchored to the removed
		// identity are cleared unless another confirmed identity still owns them.
		if _, err = tx.ExecContext(ctx, `DELETE FROM artist_source_match_state WHERE artist_id=? AND source=?`, artistID, source); err != nil {
			return err
		}
		if source == "musicbrainz" {
			_, err = tx.ExecContext(ctx, `DELETE FROM artist_image_cache WHERE artist_id=? AND (source<>'lastfm' OR NOT EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=? AND p.source='lastfm'))`, artistID, artistID)
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM artist_image_cache WHERE artist_id=? AND (source='lastfm' OR NOT EXISTS(SELECT 1 FROM artist_external_profiles p WHERE p.artist_id=? AND p.source='musicbrainz'))`, artistID, artistID)
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_attempts WHERE artist_id=?`, artistID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM artist_image_candidates WHERE artist_id=? AND status='pending'`, artistID); err != nil {
			return err
		}
		// Other candidates of this source become reviewable again; the reset
		// identity stays rejected until the user explicitly picks it again.
		if _, err = tx.ExecContext(ctx, `UPDATE artist_match_candidates SET status='candidate' WHERE artist_id=? AND source=? AND external_id<>? AND status IN ('rejected','confirmed')`, artistID, source, expectedID); err != nil {
			return err
		}
		if source == "musicbrainz" {
			_, err = tx.ExecContext(ctx, `DELETE FROM artist_biographies WHERE artist_id=? AND source IN ('wikipedia','lastfm','ai_translated','ai_web')`, artistID)
		} else {
			_, err = tx.ExecContext(ctx, `DELETE FROM artist_biographies WHERE artist_id=? AND (source IN ('lastfm','ai_web') OR (source='ai_translated' AND origin_source='lastfm'))`, artistID)
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
