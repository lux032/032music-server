ALTER TABLE scan_jobs ADD COLUMN rule_skipped_files INTEGER NOT NULL DEFAULT 0 CHECK (rule_skipped_files >= 0);
