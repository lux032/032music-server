-- Search-only names never participate in identity matching or metadata enrichment.
CREATE TABLE artist_search_aliases (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    value TEXT NOT NULL CHECK (length(trim(value)) BETWEEN 1 AND 200),
    UNIQUE (artist_id, value COLLATE NOCASE)
) STRICT;
