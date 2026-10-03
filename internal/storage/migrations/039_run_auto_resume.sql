ALTER TABLE artist_match_runs ADD COLUMN auto_resume_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE enrichment_runs ADD COLUMN auto_resume_count INTEGER NOT NULL DEFAULT 0;
