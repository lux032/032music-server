CREATE TABLE work_enrichment_retries (
    work_id INTEGER NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    requested_at TEXT NOT NULL,
    PRIMARY KEY(work_id,source)
) STRICT;
