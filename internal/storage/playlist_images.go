package storage

import (
	"context"
	"database/sql"
)

func (s *Store) PlaylistImage(ctx context.Context, id int64) (CustomImageInput, error) {
	var img CustomImageInput
	err := s.db.QueryRowContext(ctx, `SELECT content_hash,mime_type,file_path,width,height,byte_size FROM playlist_custom_images WHERE playlist_id=?`, id).Scan(&img.Hash, &img.MIMEType, &img.FileName, &img.Width, &img.Height, &img.ByteSize)
	return img, err
}

// Capturing old names and changing references occur under the same writer lock.
// Callers serialize disk writes and post-commit reference-checked cleanup.
func (s *Store) changePlaylistImage(ctx context.Context, id int64, img *CustomImageInput, deletePlaylist bool) ([]string, error) {
	tx, err := playlistWriteTx(ctx, s)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM playlists WHERE id=?)`, id).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, sql.ErrNoRows
	}
	var oldName, oldHash string
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT file_path FROM playlist_custom_images WHERE playlist_id=?),''),COALESCE((SELECT content_hash FROM playlist_custom_images WHERE playlist_id=?),'')`, id, id).Scan(&oldName, &oldHash); err != nil {
		return nil, err
	}
	names := []string{}
	if oldName != "" {
		names = append(names, oldName)
	}
	if deletePlaylist {
		_, err = tx.ExecContext(ctx, `DELETE FROM playlists WHERE id=?`, id)
	} else if img == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM playlist_custom_images WHERE playlist_id=?`, id)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO playlist_custom_images(playlist_id,content_hash,mime_type,file_path,width,height,byte_size) VALUES(?,?,?,?,?,?,?) ON CONFLICT(playlist_id) DO UPDATE SET content_hash=excluded.content_hash,mime_type=excluded.mime_type,file_path=excluded.file_path,width=excluded.width,height=excluded.height,byte_size=excluded.byte_size,created_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, id, img.Hash, img.MIMEType, img.FileName, img.Width, img.Height, img.ByteSize)
	}
	if err != nil {
		return nil, err
	}
	if !deletePlaylist && ((img == nil && oldName != "") || (img != nil && img.Hash != oldHash)) {
		if err = bumpPlaylist(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	return names, tx.Commit()
}
func (s *Store) SaveCustomPlaylistImage(ctx context.Context, id int64, img CustomImageInput) ([]string, error) {
	return s.changePlaylistImage(ctx, id, &img, false)
}
func (s *Store) ResetCustomPlaylistImage(ctx context.Context, id int64) ([]string, error) {
	return s.changePlaylistImage(ctx, id, nil, false)
}
func (s *Store) DeletePlaylistWithImages(ctx context.Context, id int64) ([]string, error) {
	return s.changePlaylistImage(ctx, id, nil, true)
}
