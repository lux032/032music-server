package storage

import (
	"context"
	"fmt"
	"time"
)

// LoginBan is a persisted ban of one client address (IPv4) or IPv6 /64.
type LoginBan struct {
	IP           string
	Strikes      int
	BannedUntil  time.Time
	LastStrikeAt time.Time
}

// LoginAllowEntry is an admin-managed address range that is never banned.
type LoginAllowEntry struct {
	Prefix    string
	Note      string
	CreatedAt time.Time
}

func parseLoginBanTime(value string) (time.Time, error) {
	return time.Parse(sessionTimeFormat, value)
}

// LoginBans returns every stored ban, including expired ones whose strikes
// have not decayed yet.
func (s *Store) LoginBans(ctx context.Context) ([]LoginBan, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ip, strikes, banned_until, last_strike_at FROM login_bans ORDER BY ip`)
	if err != nil {
		return nil, fmt.Errorf("query login bans: %w", err)
	}
	defer rows.Close()
	var bans []LoginBan
	for rows.Next() {
		var ban LoginBan
		var until, last string
		if err := rows.Scan(&ban.IP, &ban.Strikes, &until, &last); err != nil {
			return nil, fmt.Errorf("scan login ban: %w", err)
		}
		if ban.BannedUntil, err = parseLoginBanTime(until); err != nil {
			return nil, fmt.Errorf("parse login ban %s: %w", ban.IP, err)
		}
		if ban.LastStrikeAt, err = parseLoginBanTime(last); err != nil {
			return nil, fmt.Errorf("parse login ban %s: %w", ban.IP, err)
		}
		bans = append(bans, ban)
	}
	return bans, rows.Err()
}

// SaveLoginBan upserts a ban and, in the same transaction, deletes rows
// that ended and last struck before forgetBefore.
func (s *Store) SaveLoginBan(ctx context.Context, ban LoginBan, forgetBefore time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin login ban: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO login_bans(ip, strikes, banned_until, last_strike_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(ip) DO UPDATE SET strikes = excluded.strikes, banned_until = excluded.banned_until, last_strike_at = excluded.last_strike_at`,
		ban.IP, ban.Strikes, ban.BannedUntil.UTC().Format(sessionTimeFormat), ban.LastStrikeAt.UTC().Format(sessionTimeFormat)); err != nil {
		return fmt.Errorf("save login ban: %w", err)
	}
	// Timestamps share one fixed-zone RFC 3339 format; comparing them as
	// text would break on variable fractional digits, so compare in Go.
	rows, err := tx.QueryContext(ctx, `SELECT ip, banned_until, last_strike_at FROM login_bans`)
	if err != nil {
		return fmt.Errorf("query stale login bans: %w", err)
	}
	var stale []string
	for rows.Next() {
		var ip, until, last string
		if err := rows.Scan(&ip, &until, &last); err != nil {
			rows.Close()
			return fmt.Errorf("scan stale login ban: %w", err)
		}
		untilTime, err1 := parseLoginBanTime(until)
		lastTime, err2 := parseLoginBanTime(last)
		if err1 != nil || err2 != nil || (untilTime.Before(forgetBefore) && lastTime.Before(forgetBefore)) {
			stale = append(stale, ip)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate stale login bans: %w", err)
	}
	for _, ip := range stale {
		if ip == ban.IP {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_bans WHERE ip = ?`, ip); err != nil {
			return fmt.Errorf("prune login ban: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit login ban: %w", err)
	}
	return nil
}

// DeleteLoginBan removes a ban (and its strikes).
func (s *Store) DeleteLoginBan(ctx context.Context, ip string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM login_bans WHERE ip = ?`, ip); err != nil {
		return fmt.Errorf("delete login ban: %w", err)
	}
	return nil
}

// LoginAllowlist returns the admin-managed allowlist, oldest first.
func (s *Store) LoginAllowlist(ctx context.Context) ([]LoginAllowEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT prefix, note, created_at FROM login_allowlist ORDER BY created_at, prefix`)
	if err != nil {
		return nil, fmt.Errorf("query login allowlist: %w", err)
	}
	defer rows.Close()
	var entries []LoginAllowEntry
	for rows.Next() {
		var entry LoginAllowEntry
		var created string
		if err := rows.Scan(&entry.Prefix, &entry.Note, &created); err != nil {
			return nil, fmt.Errorf("scan login allowlist: %w", err)
		}
		if entry.CreatedAt, err = parseLoginBanTime(created); err != nil {
			return nil, fmt.Errorf("parse login allowlist %s: %w", entry.Prefix, err)
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

// AddLoginAllowlist inserts an entry; an existing prefix is an error.
func (s *Store) AddLoginAllowlist(ctx context.Context, entry LoginAllowEntry) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO login_allowlist(prefix, note, created_at) VALUES(?, ?, ?)`,
		entry.Prefix, entry.Note, entry.CreatedAt.UTC().Format(sessionTimeFormat)); err != nil {
		return fmt.Errorf("add login allowlist: %w", err)
	}
	return nil
}

// DeleteLoginAllowlist removes an entry.
func (s *Store) DeleteLoginAllowlist(ctx context.Context, prefix string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM login_allowlist WHERE prefix = ?`, prefix); err != nil {
		return fmt.Errorf("delete login allowlist: %w", err)
	}
	return nil
}
