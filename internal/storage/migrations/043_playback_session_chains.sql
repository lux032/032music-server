-- Shared play-chain counting (review B-1) and bounded history aggregation
-- (review M-2). A resume chain shares one chain_id (the root session id);
-- at most one session per chain may be counted, enforced in the event
-- transaction and backstopped by a unique partial index.
ALTER TABLE playback_sessions ADD COLUMN chain_id TEXT NOT NULL DEFAULT '';

-- Pre-chain rows each form their own chain; their resume links are unknown.
UPDATE playback_sessions SET chain_id = session_id WHERE chain_id = '';

CREATE INDEX idx_playback_sessions_chain ON playback_sessions(chain_id);
-- Database backstop: one counted play per chain, no matter what races.
CREATE UNIQUE INDEX idx_playback_sessions_chain_counted ON playback_sessions(chain_id) WHERE counted_at IS NOT NULL;

-- Chain membership now derives the inherited counted flag at event time, so
-- the start-time snapshot column is obsolete.
ALTER TABLE playback_sessions DROP COLUMN counted_inherited;

-- Bounded per-page aggregation (M-2): open sessions per track (bounded by
-- active devices) and the single latest ended session per track (index
-- seek), replacing the unbounded whole-history scan.
DROP INDEX idx_playback_sessions_track;
CREATE INDEX idx_playback_sessions_open_track ON playback_sessions(track_id) WHERE ended_at IS NULL;
CREATE INDEX idx_playback_sessions_ended_track ON playback_sessions(track_id, ended_at DESC) WHERE ended_at IS NOT NULL;
