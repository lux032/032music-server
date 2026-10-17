package storage

import (
	"testing"
	"time"
)

// A deferred transaction that reads, then writes after another connection
// committed, fails with SQLITE_BUSY_SNAPSHOT (517); beginWrite takes the lock first
// so the other writer waits instead.
func TestBeginWriteAvoidsBusySnapshot(t *testing.T) {
	s, ctx, _ := audioAnalysisFixture(t, 1)
	if err := s.SaveDailyMixSettings(ctx, DefaultDailyMixSettings()); err != nil {
		t.Fatal(err)
	}
	// Old pattern: reproduce 517.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT mix_count FROM daily_mix_settings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE daily_mix_settings SET mix_count = 4`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE daily_mix_settings SET track_count = 30`)
	tx.Rollback()
	if err == nil || !isBusyError(err) {
		t.Fatalf("expected a busy snapshot error, got %v", err)
	}

	// beginWrite: the concurrent writer waits for the commit.
	tx, err = s.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT mix_count FROM daily_mix_settings`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.db.ExecContext(ctx, `UPDATE daily_mix_settings SET mix_count = 6`)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err = tx.ExecContext(ctx, `UPDATE daily_mix_settings SET track_count = 30`); err != nil {
		t.Fatalf("write after read: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatalf("concurrent writer: %v", err)
	}
	set, _ := s.DailyMixSettings(ctx)
	if set.MixCount != 6 || set.TrackCount != 30 {
		t.Fatalf("settings = %+v", set)
	}
}
