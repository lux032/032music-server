package storage

import (
	"context"
	"fmt"
	"time"
)

// Credential override keys stored in credential_overrides. Values never
// contain a plaintext password or API token: the password is stored as a
// self-describing PBKDF2 hash and the API token as its SHA-256 hex digest.
// The media token is stored in plaintext so it can be revealed again.
const (
	CredentialAdminUsername     = "admin_username"
	CredentialAdminPasswordHash = "admin_password_hash"
	CredentialAPITokenHash      = "api_token_hash"
	CredentialMediaToken        = "media_token"
)

// CredentialOverrides returns every stored override keyed by credential key.
func (s *Store) CredentialOverrides(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM credential_overrides`)
	if err != nil {
		return nil, fmt.Errorf("query credential overrides: %w", err)
	}
	defer rows.Close()
	overrides := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("scan credential override: %w", err)
		}
		overrides[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credential overrides: %w", err)
	}
	return overrides, nil
}

// UpdateCredentialOverrides upserts set and deletes remove in a single
// transaction, so a failed write leaves the previous overrides intact. When
// replaceSessions is non-nil the same transaction also deletes every admin
// session and inserts *replaceSessions: a username/password change and the
// revocation of all other sessions then commit (or fail) together.
func (s *Store) UpdateCredentialOverrides(ctx context.Context, set map[string]string, remove []string, replaceSessions *AdminSession) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential override update: %w", err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(sessionTimeFormat)
	for key, value := range set {
		if _, err := tx.ExecContext(ctx, `INSERT INTO credential_overrides(key, value, updated_at) VALUES(?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now); err != nil {
			return fmt.Errorf("save credential override %s: %w", key, err)
		}
	}
	for _, key := range remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM credential_overrides WHERE key = ?`, key); err != nil {
			return fmt.Errorf("delete credential override %s: %w", key, err)
		}
	}
	if keep := replaceSessions; keep != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions`); err != nil {
			return fmt.Errorf("delete admin sessions: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,username,csrf_token,expires_at) VALUES(?,?,?,?)`, keep.TokenHash, keep.Username, keep.CSRFToken, keep.ExpiresAt.UTC().Format(sessionTimeFormat)); err != nil {
			return fmt.Errorf("insert rotated admin session: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credential override update: %w", err)
	}
	return nil
}

// ResetCredentialOverrides deletes the given overrides and every admin
// session in one transaction. It backs MUSIC_SERVER_RESET_CREDENTIALS.
func (s *Store) ResetCredentialOverrides(ctx context.Context, keys []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential reset: %w", err)
	}
	defer tx.Rollback()
	for _, key := range keys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM credential_overrides WHERE key = ?`, key); err != nil {
			return fmt.Errorf("reset credential override %s: %w", key, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM admin_sessions`); err != nil {
		return fmt.Errorf("reset admin sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credential reset: %w", err)
	}
	return nil
}
