-- Bangumi-authoritative work type correction and explicit user type locks.
ALTER TABLE works ADD COLUMN type_locked INTEGER NOT NULL DEFAULT 0 CHECK(type_locked IN (0,1));
UPDATE works SET type_locked=1 WHERE origin='manual';
CREATE INDEX idx_album_subject_candidates_status_id ON album_subject_candidates(status,id);
