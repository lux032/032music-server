-- Phase 4.5.3 / 4.6 single-layer work series (D21~D25). Automatic grouping
-- follows only Bangumi sequel/prequel relations; manual members, renamed
-- titles and detached works are permanent user intent (R3).
CREATE TABLE work_series (
    id INTEGER PRIMARY KEY,
    title TEXT NOT NULL,
    title_source TEXT NOT NULL DEFAULT 'auto' CHECK (title_source IN ('auto', 'manual')),
    representative_work_id INTEGER REFERENCES works(id) ON DELETE SET NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

-- One work belongs to at most one series (D24).
CREATE TABLE work_series_members (
    work_id INTEGER PRIMARY KEY REFERENCES works(id) ON DELETE CASCADE,
    series_id INTEGER NOT NULL REFERENCES work_series(id) ON DELETE CASCADE,
    source TEXT NOT NULL DEFAULT 'auto' CHECK (source IN ('auto', 'manual')),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

-- A work the user detached from a series (or saw dissolved) is never grouped
-- automatically again (R3).
CREATE TABLE work_series_locks (
    work_id INTEGER PRIMARY KEY REFERENCES works(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

CREATE INDEX idx_work_series_members_series ON work_series_members(series_id, work_id);
CREATE INDEX idx_work_series_representative ON work_series(representative_work_id);
