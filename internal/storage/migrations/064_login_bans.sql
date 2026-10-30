-- Persistent login bans with escalating durations, and the admin-managed
-- allowlist of addresses that are never banned.
--
-- login_bans.ip is the ban key: an IPv4 address or an IPv6 /64 prefix.
-- strikes counts lockouts within the decay window; banned_until grows with
-- it. Rows are pruned once the ban ended and the strikes have decayed.

CREATE TABLE login_bans (
    ip TEXT PRIMARY KEY CHECK (length(ip) BETWEEN 1 AND 64),
    strikes INTEGER NOT NULL CHECK (strikes >= 1),
    banned_until TEXT NOT NULL,
    last_strike_at TEXT NOT NULL
) STRICT;

CREATE TABLE login_allowlist (
    prefix TEXT PRIMARY KEY CHECK (length(prefix) BETWEEN 1 AND 64),
    note TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    created_at TEXT NOT NULL
) STRICT;
