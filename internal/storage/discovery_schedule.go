package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// DiscoverySchedule configures the automatic refresh. RunHour is in UTC.
type DiscoverySchedule struct {
	Enabled      bool
	IntervalDays int
	RunHour      int
	CacheDays    int
	LastAutoAt   string
}

var ErrInvalidDiscoverySchedule = errors.New("invalid discovery schedule")

func (s *Store) DiscoverySchedule(c context.Context) (DiscoverySchedule, error) {
	var v DiscoverySchedule
	var last sql.NullString
	e := s.db.QueryRowContext(c, `SELECT enabled,interval_days,run_hour,cache_days,last_auto_at FROM discovery_schedule WHERE id=1`).Scan(&v.Enabled, &v.IntervalDays, &v.RunHour, &v.CacheDays, &last)
	v.LastAutoAt = last.String
	return v, e
}

// SaveDiscoverySchedule stores user settings; LastAutoAt is never changed here.
func (s *Store) SaveDiscoverySchedule(c context.Context, v DiscoverySchedule) error {
	if v.IntervalDays < 1 || v.IntervalDays > 30 || v.RunHour < 0 || v.RunHour > 23 || v.CacheDays < 1 || v.CacheDays > 30 {
		return ErrInvalidDiscoverySchedule
	}
	_, e := s.db.ExecContext(c, `UPDATE discovery_schedule SET enabled=?,interval_days=?,run_hour=?,cache_days=? WHERE id=1`, v.Enabled, v.IntervalDays, v.RunHour, v.CacheDays)
	return e
}

// MarkDiscoveryAutoRun records when the scheduler last fired (or was armed).
func (s *Store) MarkDiscoveryAutoRun(c context.Context, at time.Time) error {
	_, e := s.db.ExecContext(c, `UPDATE discovery_schedule SET last_auto_at=? WHERE id=1`, at.UTC().Format(time.RFC3339))
	return e
}

// NextRun returns when the schedule is next due, or zero when disabled or not
// yet armed.
func (v DiscoverySchedule) NextRun() time.Time {
	if !v.Enabled || v.LastAutoAt == "" {
		return time.Time{}
	}
	last, e := time.Parse(time.RFC3339, v.LastAutoAt)
	if e != nil {
		return time.Time{}
	}
	day := time.Date(last.UTC().Year(), last.UTC().Month(), last.UTC().Day(), v.RunHour, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, v.IntervalDays)
}

// discoveryCacheCutoff is the fetched_at bound below which an incremental
// refresh fetches a seed again.
func discoveryCacheCutoff(c context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, now time.Time) string {
	days := 7
	_ = q.QueryRowContext(c, `SELECT cache_days FROM discovery_schedule WHERE id=1`).Scan(&days)
	return now.Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
}
