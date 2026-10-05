-- Multi-device playback sessions (plan C). Each play is a durable session
-- row; the active state is derived from last_heartbeat_at and a lease, never
-- from playback_progress.state. The migration only creates the table: no
-- sessions are fabricated for existing history rows, and playback_progress
-- keeps its counters (play_count/skip_count/position) untouched.
CREATE TABLE playback_sessions (
    session_id TEXT PRIMARY KEY,
    client_id TEXT NOT NULL,
    client_kind TEXT NOT NULL CHECK (client_kind IN ('web', 'android')),
    track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('playing', 'buffering', 'paused', 'ended')),
    seq INTEGER NOT NULL CHECK (seq >= 1),
    position_ms INTEGER NOT NULL DEFAULT 0 CHECK (position_ms >= 0),
    duration_ms INTEGER NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    initial_position_ms INTEGER NOT NULL DEFAULT 0 CHECK (initial_position_ms >= 0),
    started_at TEXT NOT NULL,
    last_heartbeat_at TEXT NOT NULL,
    ended_at TEXT,
    end_reason TEXT CHECK (end_reason IN ('completed', 'skipped', 'stopped', 'replaced', 'error', 'client_closed', 'expired')),
    counted_at TEXT,
    counted_inherited INTEGER NOT NULL DEFAULT 0 CHECK (counted_inherited IN (0, 1)),
    resumed_from TEXT
) STRICT;

-- Per-page history aggregation loads sessions for the visible tracks.
CREATE INDEX idx_playback_sessions_track ON playback_sessions(track_id, last_heartbeat_at DESC);
-- The sweeper finalizes sessions whose lease has run out.
CREATE INDEX idx_playback_sessions_open ON playback_sessions(last_heartbeat_at) WHERE ended_at IS NULL;
-- Ended sessions are purged after the retention window.
CREATE INDEX idx_playback_sessions_ended ON playback_sessions(ended_at) WHERE ended_at IS NOT NULL;
