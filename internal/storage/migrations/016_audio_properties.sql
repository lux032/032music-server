-- codec, bitrate (kbps), sample_rate, bit_depth and channels already exist in 001.
ALTER TABLE audio_files ADD COLUMN audio_probe_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE audio_files ADD COLUMN has_external_lrc INTEGER NOT NULL DEFAULT 0 CHECK (has_external_lrc IN (0,1));
ALTER TABLE tracks ADD COLUMN user_disc_number INTEGER CHECK (user_disc_number IS NULL OR user_disc_number > 0);
ALTER TABLE tracks ADD COLUMN user_track_number INTEGER CHECK (user_track_number IS NULL OR user_track_number >= 0);
