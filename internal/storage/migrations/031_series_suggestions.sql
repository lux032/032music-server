-- Phase 4.6 batch 5: cross-media / sequel series suggestions (D52~D60).
-- Suggestions are work-to-work pairs remembered by their Bangumi subject
-- pair; accepting or rejecting a pair is permanent (D55), so the same pair
-- is never suggested again.
CREATE TABLE work_series_suggestions (
    id INTEGER PRIMARY KEY,
    work_a INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    work_b INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    subject_a INTEGER NOT NULL,
    subject_b INTEGER NOT NULL,
    relation_ab TEXT NOT NULL,
    relation_ba TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('cross', 'sequel')),
    run_id INTEGER,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (work_a < work_b),
    UNIQUE (work_a, work_b)
) STRICT;

CREATE TABLE work_series_suggestion_decisions (
    subject_a INTEGER NOT NULL,
    subject_b INTEGER NOT NULL,
    decision TEXT NOT NULL CHECK (decision IN ('accepted', 'rejected')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    PRIMARY KEY (subject_a, subject_b),
    CHECK (subject_a < subject_b)
) STRICT;

CREATE INDEX idx_work_series_suggestions_work_b ON work_series_suggestions(work_b, work_a);
