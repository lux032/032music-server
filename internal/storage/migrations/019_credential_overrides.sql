-- P5a: admin-page overrides for login and token credentials. A row takes
-- precedence over the matching environment variable; deleting it restores
-- the environment value. Keys: admin_username (plain), admin_password_hash
-- (pbkdf2-sha256 encoded), api_token_hash (SHA-256 hex), media_token (plain,
-- so an admin can reveal it again after re-entering the current password).

CREATE TABLE credential_overrides (
    key TEXT PRIMARY KEY CHECK (key IN ('admin_username', 'admin_password_hash', 'api_token_hash', 'media_token')),
    value TEXT NOT NULL CHECK (length(value) > 0),
    updated_at TEXT NOT NULL
) STRICT;
