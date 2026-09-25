ALTER TABLE playback_progress ADD COLUMN skip_count INTEGER NOT NULL DEFAULT 0 CHECK (skip_count >= 0);
ALTER TABLE playback_progress ADD COLUMN last_skipped_at TEXT;
