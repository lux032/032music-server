-- Admin two-factor login: allow the admin_totp_secret override key (base32
-- TOTP secret). SQLite cannot alter a CHECK constraint, so the table is
-- rebuilt with its rows preserved.

CREATE TABLE credential_overrides_new (
    key TEXT PRIMARY KEY CHECK (key IN ('admin_username', 'admin_password_hash', 'admin_totp_secret', 'api_token_hash', 'media_token')),
    value TEXT NOT NULL CHECK (length(value) > 0),
    updated_at TEXT NOT NULL
) STRICT;

INSERT INTO credential_overrides_new(key, value, updated_at)
SELECT key, value, updated_at FROM credential_overrides;

DROP TABLE credential_overrides;
ALTER TABLE credential_overrides_new RENAME TO credential_overrides;
