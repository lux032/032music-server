package storage

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func playbackSessionColumns(t *testing.T, ctx context.Context, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info('playback_sessions') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func playbackSessionIndexes(t *testing.T, ctx context.Context, s *Store) []string {
	t.Helper()
	rows, err := s.db.QueryContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='index' AND tbl_name='playback_sessions' AND sql IS NOT NULL ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var list []string
	for rows.Next() {
		var sql string
		if err = rows.Scan(&sql); err != nil {
			t.Fatal(err)
		}
		list = append(list, strings.Join(strings.Fields(sql), " "))
	}
	return list
}

// Upgrade path: a 050 database with existing sessions keeps every column,
// index and row after 051, and accepts the new desktop client kind.
func TestMigration051AcceptsDesktopAndPreservesSessions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.db.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))) STRICT`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "051_" {
			break
		}
		script, e := migrationFiles.ReadFile("migrations/" + entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		conn, e := s.db.Conn(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); e != nil {
			conn.Close()
			t.Fatal(e)
		}
		version := 0
		for _, ch := range entry.Name()[:3] {
			version = version*10 + int(ch-'0')
		}
		e = s.applyMigration(ctx, conn, version, entry.Name(), string(script))
		_, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		conn.Close()
		if e != nil {
			t.Fatalf("apply %s: %v", entry.Name(), e)
		}
	}
	trackID := seedSessionTrack(t, ctx, s, 240000)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO playback_sessions(session_id,client_id,client_kind,track_id,state,seq,position_ms,duration_ms,initial_position_ms,started_at,last_heartbeat_at,ended_at,end_reason,counted_at,resumed_from,chain_id,context_type,context_id,context_position,context_processed) VALUES('session-old-1','device-1','android',?,'ended',3,120000,240000,0,'2026-01-01T00:00:00Z','2026-01-01T00:02:00Z','2026-01-01T00:02:00Z','completed','2026-01-01T00:02:00Z',NULL,'session-old-1','dailyMix',7,2,1)`, trackID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO playback_sessions(session_id,client_id,client_kind,track_id,state,seq,started_at,last_heartbeat_at,chain_id) VALUES('session-desktop-pre','device-2','desktop',?,'playing',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','x')`, trackID); err == nil {
		t.Fatal("050 schema accepted desktop clientKind")
	}
	columnsBefore := playbackSessionColumns(t, ctx, s)
	indexesBefore := playbackSessionIndexes(t, ctx, s)

	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	if got := playbackSessionColumns(t, ctx, s); !reflect.DeepEqual(got, columnsBefore) {
		t.Fatalf("columns changed:\nbefore %v\nafter  %v", columnsBefore, got)
	}
	if got := playbackSessionIndexes(t, ctx, s); !reflect.DeepEqual(got, indexesBefore) {
		t.Fatalf("indexes changed:\nbefore %v\nafter  %v", indexesBefore, got)
	}
	var kind, chain, ctxType string
	var ctxID, ctxPosition, processed, seq int64
	if err = s.db.QueryRowContext(ctx, `SELECT client_kind,chain_id,context_type,context_id,context_position,context_processed,seq FROM playback_sessions WHERE session_id='session-old-1'`).Scan(&kind, &chain, &ctxType, &ctxID, &ctxPosition, &processed, &seq); err != nil {
		t.Fatal(err)
	}
	if kind != "android" || chain != "session-old-1" || ctxType != "dailyMix" || ctxID != 7 || ctxPosition != 2 || processed != 1 || seq != 3 {
		t.Fatalf("row not preserved: %s %s %s %d %d %d %d", kind, chain, ctxType, ctxID, ctxPosition, processed, seq)
	}

	start := sessionEvent(trackID, 1, "start", "playing", 0, 240000)
	start.ClientKind = "desktop"
	start.SessionID = "session-desktop-1"
	mustEvent(t, ctx, s, start)
	if err = s.db.QueryRowContext(ctx, `SELECT client_kind FROM playback_sessions WHERE session_id='session-desktop-1'`).Scan(&kind); err != nil || kind != "desktop" {
		t.Fatalf("desktop session: %q %v", kind, err)
	}
	var fkViolations int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check('playback_sessions')`).Scan(&fkViolations); err != nil || fkViolations != 0 {
		t.Fatalf("foreign key check: %d %v", fkViolations, err)
	}
}
