-- M11: persist admin sessions so a restart does not log everyone out.
-- Only the SHA-256 hash of the session token is stored; a database leak
-- does not expose usable session credentials.

CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    csrf_token TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_admin_sessions_expires ON admin_sessions(expires_at);
