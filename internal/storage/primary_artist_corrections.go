package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrArtistCorrectionConflict = errors.New("署名已变化，请刷新后重新预览")

// PrimaryArtistCorrection is a snapshot used for optimistic preview/commit.
type PrimaryArtistCorrection struct {
	TrackID         int64
	Revision        int64
	Active          bool
	Artists         []ArtistRef
	Base            []ArtistRef
	Token           string
	PreviousArtists []ArtistRef
}

// correctionRevisionGuard prevents merge rollback from overwriting any newer
// manual edit. Conservatively includes all tracks; unrelated edits require a
// fresh merge decision rather than silently corrupting a correction.
func correctionRevisionGuard(ctx context.Context, tx *sql.Tx) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT track_id,revision FROM track_primary_artist_state ORDER BY track_id`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var values []int64
	for rows.Next() {
		var id, revision int64
		if err = rows.Scan(&id, &revision); err != nil {
			return "", err
		}
		values = append(values, id, revision)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	raw, _ := json.Marshal(values)
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func reapplyPrimaryArtistCorrections(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT track_id FROM track_primary_artist_state WHERE active=1`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = applyPrimaryArtistCorrection(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

type correctionQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func correctionSnapshot(ctx context.Context, db correctionQuery, trackID int64) (PrimaryArtistCorrection, error) {
	v := PrimaryArtistCorrection{TrackID: trackID, Artists: []ArtistRef{}, Base: []ArtistRef{}}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT 1 FROM tracks WHERE id=?`, trackID).Scan(&exists); err != nil {
		return v, err
	}
	err := db.QueryRowContext(ctx, `SELECT revision,active FROM track_primary_artist_state WHERE track_id=?`, trackID).Scan(&v.Revision, &v.Active)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	for _, source := range []struct {
		query string
		dest  *[]ArtistRef
	}{
		{`SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name) FROM track_artists p JOIN artists ar ON ar.id=p.artist_id WHERE p.track_id=? AND p.role='primary' ORDER BY p.position,ar.id`, &v.Artists},
		{`SELECT ar.id,COALESCE(ar.user_display_name,ar.display_name) FROM track_primary_artist_base p JOIN artists ar ON ar.id=p.artist_id WHERE p.track_id=? ORDER BY p.position,ar.id`, &v.Base},
	} {
		rows, e := db.QueryContext(ctx, source.query, trackID)
		if e != nil {
			return v, e
		}
		for rows.Next() {
			var a ArtistRef
			if e = rows.Scan(&a.ID, &a.Name); e != nil {
				rows.Close()
				return v, e
			}
			*source.dest = append(*source.dest, a)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return v, e
		}
	}
	if v.Revision > 0 {
		var previousActive bool
		var previousRaw string
		if err := db.QueryRowContext(ctx, `SELECT before_active,before_json FROM track_primary_artist_operations WHERE track_id=? AND revision=?`, trackID, v.Revision).Scan(&previousActive, &previousRaw); err != nil {
			return v, err
		}
		if previousActive {
			var ids []int64
			if err := json.Unmarshal([]byte(previousRaw), &ids); err != nil {
				return v, err
			}
			seen := map[int64]bool{}
			for _, id := range ids {
				canonical, err := canonicalArtistID(ctx, db, id)
				if err != nil {
					return v, err
				}
				if seen[canonical] {
					continue
				}
				seen[canonical] = true
				var name string
				if err := db.QueryRowContext(ctx, `SELECT COALESCE(user_display_name,display_name) FROM artists WHERE id=?`, canonical).Scan(&name); err != nil {
					return v, err
				}
				v.PreviousArtists = append(v.PreviousArtists, ArtistRef{ID: canonical, Name: name})
			}
		} else {
			v.PreviousArtists = append([]ArtistRef{}, v.Base...)
		}
	}
	raw, _ := json.Marshal(v)
	v.Token = fmt.Sprintf("%x", sha256.Sum256(raw))
	return v, nil
}

func (s *Store) PrimaryArtistCorrection(ctx context.Context, trackID int64) (PrimaryArtistCorrection, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PrimaryArtistCorrection{}, err
	}
	defer tx.Rollback()
	v, err := correctionSnapshot(ctx, tx, trackID)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}

// capturePrimaryArtistBase is called only after a fresh scanner-owned rebuild.
func capturePrimaryArtistBase(ctx context.Context, tx *sql.Tx, trackID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM track_primary_artist_base WHERE track_id=?`, trackID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO track_primary_artist_base(track_id,artist_id,position,join_phrase) SELECT track_id,artist_id,MIN(position),join_phrase FROM track_artists WHERE track_id=? AND role='primary' GROUP BY track_id,artist_id`, trackID)
	return err
}

// applyPrimaryArtistCorrection replaces, never unions, primary identities.
func applyPrimaryArtistCorrection(ctx context.Context, tx *sql.Tx, trackID int64) error {
	var active bool
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT active,artists_json FROM track_primary_artist_state WHERE track_id=?`, trackID).Scan(&active, &raw)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !active {
		return nil
	}
	if err != nil {
		return err
	}
	var ids []int64
	if err = json.Unmarshal([]byte(raw), &ids); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM track_artists WHERE track_id=? AND role='primary'`, trackID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		canonical, e := canonicalArtistID(ctx, tx, id)
		if e != nil {
			return e
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		if _, err = tx.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,?,'primary')`, trackID, canonical, len(seen)-1); err != nil {
			return err
		}
	}
	return nil
}

func restorePrimaryArtistBase(ctx context.Context, tx *sql.Tx, trackID int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM track_artists WHERE track_id=? AND role='primary'`, trackID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT artist_id FROM track_primary_artist_base WHERE track_id=? ORDER BY position,artist_id`, trackID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		canonical, e := canonicalArtistID(ctx, tx, id)
		if e != nil {
			return e
		}
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		if _, err = tx.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,?,'primary')`, trackID, canonical, len(seen)-1); err != nil {
			return err
		}
	}
	return nil
}

// SetPrimaryArtistCorrection requires the exact preview token. Empty IDs reset
// to current tags. Undo restores the previous configuration, not stale tags.
func (s *Store) SetPrimaryArtistCorrection(ctx context.Context, trackID int64, token string, ids []int64, undo bool) error {
	if len(ids) > 32 {
		return errors.New("最多选择 32 位艺术家")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	snapshot, err := correctionSnapshot(ctx, tx, trackID)
	if err != nil {
		return err
	}
	if token == "" || snapshot.Token != token {
		return ErrArtistCorrectionConflict
	}
	beforeActive := snapshot.Active
	beforeRaw := "[]"
	err = tx.QueryRowContext(ctx, `SELECT artists_json FROM track_primary_artist_state WHERE track_id=?`, trackID).Scan(&beforeRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	active := len(ids) > 0
	if undo {
		err = tx.QueryRowContext(ctx, `SELECT before_active,before_json FROM track_primary_artist_operations WHERE track_id=? AND revision=?`, trackID, snapshot.Revision).Scan(&active, &beforeRaw)
		if err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(beforeRaw), &ids); err != nil {
			return err
		}
		// Keep the current configuration for the new operation's audit snapshot.
		beforeRaw = "[]"
		if err = tx.QueryRowContext(ctx, `SELECT artists_json FROM track_primary_artist_state WHERE track_id=?`, trackID).Scan(&beforeRaw); err != nil {
			return err
		}
	}
	normalized := []int64{}
	seen := map[int64]bool{}
	for _, id := range ids {
		canonical, e := canonicalArtistID(ctx, tx, id)
		if e != nil {
			return e
		}
		if seen[canonical] {
			return errors.New("艺术家重复，请重新选择")
		}
		seen[canonical] = true
		normalized = append(normalized, canonical)
	}
	raw, _ := json.Marshal(normalized)
	if _, err = tx.ExecContext(ctx, `INSERT INTO track_primary_artist_state(track_id,revision,active,artists_json) VALUES(?,?,?,?) ON CONFLICT(track_id) DO UPDATE SET revision=excluded.revision,active=excluded.active,artists_json=excluded.artists_json,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, trackID, snapshot.Revision+1, active, string(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO track_primary_artist_operations(track_id,revision,before_active,before_json,after_active,after_json) VALUES(?,?,?,?,?,?)`, trackID, snapshot.Revision+1, beforeActive, beforeRaw, active, string(raw)); err != nil {
		return err
	}
	if active {
		err = applyPrimaryArtistCorrection(ctx, tx, trackID)
	} else {
		err = restorePrimaryArtistBase(ctx, tx, trackID)
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tracks SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, trackID); err != nil {
		return err
	}
	// Album refreshes must notice changed track credits, even without a new ID.
	if _, err = tx.ExecContext(ctx, `UPDATE albums SET updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=(SELECT album_id FROM tracks WHERE id=?)`, trackID); err != nil {
		return err
	}
	return tx.Commit()
}
