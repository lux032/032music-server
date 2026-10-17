-- Accept clientKind 'desktop' (032musicplayer). SQLite cannot alter a CHECK
-- constraint, so the table is rebuilt with the same columns, defaults and
-- indexes as after migrations 042/043/050. The migration runner disables
-- foreign keys around this script.
CREATE TABLE playback_sessions_migrated (
    session_id TEXT PRIMARY KEY,
    client_id TEXT NOT NULL,
    client_kind TEXT NOT NULL CHECK (client_kind IN ('web', 'android', 'desktop')),
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
    resumed_from TEXT,
    chain_id TEXT NOT NULL DEFAULT '',
    context_type TEXT CHECK (context_type IS NULL OR context_type IN ('dailyMix')),
    context_id INTEGER,
    context_position INTEGER,
    context_processed INTEGER NOT NULL DEFAULT 0 CHECK (context_processed IN (0, 1))
) STRICT;

INSERT INTO playback_sessions_migrated(session_id,client_id,client_kind,track_id,state,seq,position_ms,duration_ms,initial_position_ms,started_at,last_heartbeat_at,ended_at,end_reason,counted_at,resumed_from,chain_id,context_type,context_id,context_position,context_processed)
SELECT session_id,client_id,client_kind,track_id,state,seq,position_ms,duration_ms,initial_position_ms,started_at,last_heartbeat_at,ended_at,end_reason,counted_at,resumed_from,chain_id,context_type,context_id,context_position,context_processed FROM playback_sessions;

DROP TABLE playback_sessions;
ALTER TABLE playback_sessions_migrated RENAME TO playback_sessions;

CREATE INDEX idx_playback_sessions_open ON playback_sessions(last_heartbeat_at) WHERE ended_at IS NULL;
CREATE INDEX idx_playback_sessions_ended ON playback_sessions(ended_at) WHERE ended_at IS NOT NULL;
CREATE INDEX idx_playback_sessions_chain ON playback_sessions(chain_id);
CREATE UNIQUE INDEX idx_playback_sessions_chain_counted ON playback_sessions(chain_id) WHERE counted_at IS NOT NULL;
CREATE INDEX idx_playback_sessions_open_track ON playback_sessions(track_id) WHERE ended_at IS NULL;
CREATE INDEX idx_playback_sessions_ended_track ON playback_sessions(track_id, ended_at DESC) WHERE ended_at IS NOT NULL;
CREATE INDEX idx_playback_sessions_context_pending ON playback_sessions(ended_at)
    WHERE context_type IS NOT NULL AND context_processed = 0;
