package storage

import (
	"context"
	"time"
)

// AdminSession is a persisted admin login session. TokenHash is the SHA-256
// hash (hex) of the cookie token; the raw token never touches the database.
type AdminSession struct {
	TokenHash string
	Username  string
	CSRFToken string
	ExpiresAt time.Time
}

const sessionTimeFormat = time.RFC3339Nano

func (s *Store) CreateAdminSession(ctx context.Context, session AdminSession) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO admin_sessions(token_hash,username,csrf_token,expires_at) VALUES(?,?,?,?)`, session.TokenHash, session.Username, session.CSRFToken, session.ExpiresAt.UTC().Format(sessionTimeFormat))
	return err
}

func (s *Store) AdminSessionByTokenHash(ctx context.Context, tokenHash string) (AdminSession, error) {
	var session AdminSession
	var expiresAt string
	err := s.db.QueryRowContext(ctx, `SELECT token_hash,username,csrf_token,expires_at FROM admin_sessions WHERE token_hash=?`, tokenHash).Scan(&session.TokenHash, &session.Username, &session.CSRFToken, &expiresAt)
	if err != nil {
		return AdminSession{}, err
	}
	session.ExpiresAt, err = time.Parse(sessionTimeFormat, expiresAt)
	if err != nil {
		return AdminSession{}, err
	}
	return session, nil
}

func (s *Store) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash=?`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredAdminSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE expires_at<=?`, now.UTC().Format(sessionTimeFormat))
	return err
}
