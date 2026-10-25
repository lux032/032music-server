-- Covers are now fetched at 500px and fall back to Bangumi artwork for
-- work-sourced release groups; re-check those previously marked missing.
UPDATE discovery_release_groups SET cover_status='unknown', cover_checked_at=NULL WHERE cover_status IN ('missing','failed');
