package storage

import (
	"context"
	"database/sql"
	"errors"
)

// ArtistIdentitySettings controls canonical display-name normalization behavior
// for artists with confirmed MusicBrainz identities.
type ArtistIdentitySettings struct {
	CanonicalNativeNameEnabled bool   `json:"canonicalNativeNameEnabled"`
	UpdatedAt                  string `json:"updatedAt"`
}

func DefaultArtistIdentitySettings() ArtistIdentitySettings {
	return ArtistIdentitySettings{
		CanonicalNativeNameEnabled: true,
	}
}

func (s *Store) ArtistIdentitySettings(ctx context.Context) (ArtistIdentitySettings, error) {
	v := DefaultArtistIdentitySettings()
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT canonical_native_name_enabled,updated_at FROM artist_identity_settings WHERE id=1`).
		Scan(&enabled, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.CanonicalNativeNameEnabled = enabled != 0
	return v, nil
}

func (s *Store) SaveArtistIdentitySettings(ctx context.Context, v ArtistIdentitySettings) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO artist_identity_settings(id,canonical_native_name_enabled,updated_at)
		VALUES(1,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
		ON CONFLICT(id) DO UPDATE SET canonical_native_name_enabled=excluded.canonical_native_name_enabled,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, boolInt(v.CanonicalNativeNameEnabled))
	return err
}

func canonicalNativeNameEnabledTx(ctx context.Context, q queryRower) (bool, error) {
	var enabled int
	err := q.QueryRowContext(ctx, `SELECT canonical_native_name_enabled FROM artist_identity_settings WHERE id=1`).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return enabled != 0, nil
}
