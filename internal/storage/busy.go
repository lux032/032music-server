package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"modernc.org/sqlite"
)

// retryDB wraps *sql.DB and retries write operations that fail with
// SQLITE_BUSY / SQLITE_LOCKED (M14). WAL allows a single writer; when the
// scanner writes in bulk while clients report playback, a loser can get an
// immediate BUSY (deadlock avoidance) that busy_timeout does not cover.
// Reads are not retried: in WAL mode readers never block. Statements inside
// an explicit transaction are deliberately not retried either — after a
// deadlock error the transaction state is uncertain.
type retryDB struct {
	*sql.DB
}

const (
	sqliteBusy   = 5
	sqliteLocked = 6
)

func isBusyError(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	// Extended result codes carry the extended bits in the high bytes.
	switch sqliteErr.Code() & 0xFF {
	case sqliteBusy, sqliteLocked:
		return true
	default:
		return false
	}
}

func withBusyRetry(ctx context.Context, operation func() error) error {
	backoff := 20 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := operation()
		if err == nil || !isBusyError(err) || attempt >= 10 {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 500*time.Millisecond {
			backoff = 500 * time.Millisecond
		}
	}
}

func (r retryDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var result sql.Result
	err := withBusyRetry(ctx, func() error {
		var execErr error
		result, execErr = r.DB.ExecContext(ctx, query, args...)
		return execErr
	})
	return result, err
}

func (r retryDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	var tx *sql.Tx
	err := withBusyRetry(ctx, func() error {
		var beginErr error
		tx, beginErr = r.DB.BeginTx(ctx, opts)
		return beginErr
	})
	return tx, err
}
