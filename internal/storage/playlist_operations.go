package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrPlaylistConflict = errors.New("playlist revision conflict")

type PlaylistCapacityError struct{ Remaining int }

func (e *PlaylistCapacityError) Error() string {
	return fmt.Sprintf("playlist cannot contain more than 5000 tracks; remaining capacity %d", e.Remaining)
}

// IsBusyError identifies SQLite contention, not unrelated database errors.
func IsBusyError(err error) bool { return isBusyError(err) }

type PlaylistItemResult struct {
	Added            int   `json:"added"`
	Removed          int   `json:"removed"`
	SkippedDuplicate int   `json:"skippedDuplicate"`
	SkippedInvalid   int   `json:"skippedInvalid"`
	Total            int   `json:"total"`
	Revision         int64 `json:"revision"`
}

func playlistWriteTx(ctx context.Context, s *Store) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE playlists SET id=id WHERE 0`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

const playlistSelect = `SELECT p.id,p.name,p.description,(SELECT COUNT(*) FROM playlist_items WHERE playlist_id=p.id),COALESCE((SELECT '/api/v1/playlists/'||p.id||'/artwork?v='||substr(content_hash,1,16) FROM playlist_custom_images WHERE playlist_id=p.id),(SELECT '/api/v1/artwork/'||aw.id FROM playlist_items first JOIN tracks t ON t.id=first.track_id JOIN artworks aw ON aw.album_id=t.album_id WHERE first.playlist_id=p.id ORDER BY first.position,(aw.source_type='custom') DESC,aw.is_primary DESC,aw.id LIMIT 1),''),p.created_at,p.updated_at,p.revision,EXISTS(SELECT 1 FROM playlist_custom_images WHERE playlist_id=p.id) FROM playlists p WHERE p.id=?`

func playlistByIDTx(ctx context.Context, tx *sql.Tx, id int64) (Playlist, error) {
	var p Playlist
	err := tx.QueryRowContext(ctx, playlistSelect, id).Scan(&p.ID, &p.Name, &p.Description, &p.ItemCount, &p.ArtworkURL, &p.CreatedAt, &p.UpdatedAt, &p.Revision, &p.HasCustomArtwork)
	return p, err
}
func playlistIDsTx(ctx context.Context, tx *sql.Tx, id int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT track_id FROM playlist_items WHERE playlist_id=? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var v int64
		if err = rows.Scan(&v); err != nil {
			return nil, err
		}
		ids = append(ids, v)
	}
	return ids, rows.Err()
}
func bumpPlaylist(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE playlists SET revision=revision+1,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, id)
	return err
}
func writePlaylistIDs(ctx context.Context, tx *sql.Tx, id int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM playlist_items WHERE playlist_id=?`, id); err != nil {
		return err
	}
	return insertPlaylistItems(ctx, tx, id, ids)
}

// All item operations read only after the transaction has acquired the writer
// lock. Additive insert ignores stale revisions and clamps its index to the
// current list; it never replaces concurrent additions.
func (s *Store) mutatePlaylist(ctx context.Context, id int64, expected *int64, change func(*sql.Tx, []int64, *PlaylistItemResult) ([]int64, error)) (PlaylistItemResult, error) {
	var out PlaylistItemResult
	tx, err := playlistWriteTx(ctx, s)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var rev int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM playlists WHERE id=?`, id).Scan(&rev); err != nil {
		return out, err
	}
	if expected != nil && *expected != rev {
		return out, ErrPlaylistConflict
	}
	old, err := playlistIDsTx(ctx, tx, id)
	if err != nil {
		return out, err
	}
	next, err := change(tx, old, &out)
	if err != nil {
		return out, err
	}
	if !slices.Equal(old, next) {
		if err = writePlaylistIDs(ctx, tx, id, next); err != nil {
			return out, err
		}
		if err = bumpPlaylist(ctx, tx, id); err != nil {
			return out, err
		}
		rev++
	}
	out.Total = len(next)
	out.Revision = rev
	return out, tx.Commit()
}
func validPlaylistAdditions(ctx context.Context, tx *sql.Tx, old, ids []int64, out *PlaylistItemResult) ([]int64, error) {
	seen := map[int64]bool{}
	for _, id := range old {
		seen[id] = true
	}
	added := []int64{}
	for _, id := range ids {
		if seen[id] {
			out.SkippedDuplicate++
			continue
		}
		seen[id] = true
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracks WHERE id=?)`, id).Scan(&exists); err != nil {
			return nil, err
		}
		if id <= 0 || !exists {
			out.SkippedInvalid++
			continue
		}
		added = append(added, id)
	}
	if len(old)+len(added) > 5000 {
		return nil, &PlaylistCapacityError{Remaining: 5000 - len(old)}
	}
	out.Added = len(added)
	return added, nil
}
func (s *Store) AppendPlaylistItems(ctx context.Context, id int64, ids []int64, albumID *int64) (PlaylistItemResult, error) {
	return s.mutatePlaylist(ctx, id, nil, func(tx *sql.Tx, old []int64, out *PlaylistItemResult) ([]int64, error) {
		if albumID != nil {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM albums WHERE id=?)`, *albumID).Scan(&exists); err != nil {
				return nil, err
			}
			if !exists {
				return nil, sql.ErrNoRows
			}
			rows, err := tx.QueryContext(ctx, `SELECT id FROM tracks WHERE album_id=? ORDER BY COALESCE(user_disc_number,disc_number),COALESCE(user_track_number,track_number),id`, *albumID)
			if err != nil {
				return nil, err
			}
			ids = []int64{}
			for rows.Next() {
				var v int64
				if err = rows.Scan(&v); err != nil {
					rows.Close()
					return nil, err
				}
				ids = append(ids, v)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
		added, err := validPlaylistAdditions(ctx, tx, old, ids, out)
		return append(old, added...), err
	})
}
func (s *Store) RemovePlaylistItems(ctx context.Context, id int64, ids []int64, expected *int64) (PlaylistItemResult, error) {
	return s.mutatePlaylist(ctx, id, expected, func(_ *sql.Tx, old []int64, out *PlaylistItemResult) ([]int64, error) {
		next := []int64{}
		for _, v := range old {
			if slices.Contains(ids, v) {
				out.Removed++
			} else {
				next = append(next, v)
			}
		}
		return next, nil
	})
}
func (s *Store) InsertPlaylistItemAt(ctx context.Context, id, trackID int64, index int) (PlaylistItemResult, error) {
	return s.mutatePlaylist(ctx, id, nil, func(tx *sql.Tx, old []int64, out *PlaylistItemResult) ([]int64, error) {
		added, err := validPlaylistAdditions(ctx, tx, old, []int64{trackID}, out)
		if err != nil || len(added) == 0 {
			return old, err
		}
		index = max(0, min(index, len(old)))
		return slices.Insert(old, index, trackID), nil
	})
}
func (s *Store) ReorderPlaylistItems(ctx context.Context, id int64, ids []int64, expected *int64) (PlaylistItemResult, error) {
	return s.mutatePlaylist(ctx, id, expected, func(_ *sql.Tx, old []int64, _ *PlaylistItemResult) ([]int64, error) {
		a, b := slices.Clone(old), slices.Clone(ids)
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			return nil, errors.New("invalid order: must contain every current track exactly once")
		}
		return ids, nil
	})
}
func (s *Store) MovePlaylistItem(ctx context.Context, id, trackID int64, direction int) error {
	_, err := s.mutatePlaylist(ctx, id, nil, func(_ *sql.Tx, old []int64, _ *PlaylistItemResult) ([]int64, error) {
		next := slices.Clone(old)
		i := slices.Index(next, trackID)
		j := i + direction
		if i >= 0 && j >= 0 && j < len(next) {
			next[i], next[j] = next[j], next[i]
		}
		return next, nil
	})
	return err
}
func (s *Store) ReplacePlaylistItemsAtRevision(ctx context.Context, id int64, ids []int64, expected *int64) error {
	_, err := s.mutatePlaylist(ctx, id, expected, func(tx *sql.Tx, _ []int64, _ *PlaylistItemResult) ([]int64, error) {
		if len(ids) > 5000 {
			return nil, errors.New("playlist cannot contain more than 5000 tracks")
		}
		seen := map[int64]bool{}
		next := []int64{}
		for _, v := range ids {
			if v <= 0 {
				return nil, fmt.Errorf("invalid track id %d", v)
			}
			if seen[v] {
				continue
			}
			seen[v] = true
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tracks WHERE id=?)`, v).Scan(&exists); err != nil {
				return nil, err
			}
			if !exists {
				return nil, fmt.Errorf("invalid track id %d", v)
			}
			next = append(next, v)
		}
		return next, nil
	})
	return err
}
func (s *Store) CreatePlaylistSkippingInvalid(ctx context.Context, name, description string, ids []int64) (Playlist, PlaylistItemResult, error) {
	var out PlaylistItemResult
	name = strings.TrimSpace(name)
	if name == "" {
		return Playlist{}, out, errors.New("playlist name must not be empty")
	}
	tx, err := playlistWriteTx(ctx, s)
	if err != nil {
		return Playlist{}, out, err
	}
	defer tx.Rollback()
	added, err := validPlaylistAdditions(ctx, tx, nil, ids, &out)
	if err != nil {
		return Playlist{}, out, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO playlists(name,description) VALUES(?,?)`, name, strings.TrimSpace(description))
	if err != nil {
		return Playlist{}, out, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Playlist{}, out, err
	}
	if err = insertPlaylistItems(ctx, tx, id, added); err != nil {
		return Playlist{}, out, err
	}
	if len(added) > 0 {
		if err = bumpPlaylist(ctx, tx, id); err != nil {
			return Playlist{}, out, err
		}
	}
	p, err := playlistByIDTx(ctx, tx, id)
	if err != nil {
		return p, out, err
	}
	out.Total = len(added)
	out.Revision = p.Revision
	return p, out, tx.Commit()
}
