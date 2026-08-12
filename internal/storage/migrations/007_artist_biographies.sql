ALTER TABLE artists ADD COLUMN preferred_biography_source TEXT;
ALTER TABLE artists ADD COLUMN preferred_biography_language TEXT;

CREATE TABLE artist_biography_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    preferred_languages TEXT NOT NULL DEFAULT 'zh,ja,en',
    source_priority TEXT NOT NULL DEFAULT 'wikipedia,lastfm',
    wikipedia_enabled INTEGER NOT NULL DEFAULT 1 CHECK (wikipedia_enabled IN (0, 1)),
    english_fallback INTEGER NOT NULL DEFAULT 1 CHECK (english_fallback IN (0, 1)),
    cache_days INTEGER NOT NULL DEFAULT 30 CHECK (cache_days > 0),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
) STRICT;

INSERT INTO artist_biography_settings(id) VALUES (1);

CREATE TABLE artist_biographies (
    id INTEGER PRIMARY KEY,
    artist_id INTEGER NOT NULL REFERENCES artists(id) ON DELETE CASCADE,
    source TEXT NOT NULL CHECK (source IN ('wikipedia', 'lastfm')),
    language TEXT NOT NULL,
    biography TEXT NOT NULL DEFAULT '',
    page_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'found' CHECK (status IN ('found', 'missing')),
    fetched_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE(artist_id, source, language)
) STRICT;

CREATE INDEX idx_artist_biographies_artist ON artist_biographies(artist_id, status, language, source);

INSERT OR IGNORE INTO artist_biographies(artist_id, source, language, biography, page_url, status, fetched_at)
SELECT p.artist_id, 'lastfm', COALESCE(NULLIF(s.language, ''), 'en'), p.biography,
       COALESCE(p.page_url, ''), 'found', p.fetched_at
FROM artist_external_profiles p
JOIN metadata_source_settings s ON s.source = 'lastfm'
WHERE p.source = 'lastfm' AND COALESCE(p.biography, '') <> '';
