# Spec Delta

## Purpose

Defines index-backed query execution, pagination, unified search, track version counting, playback state derivation, and random album sampling behavior across library read endpoints while preserving existing API semantics.

## ADDED Requirements

### Requirement: Track version counts resolve without full table scans of tracks
The library read path SHALL compute track version counts and retrieve track versions using the normalized track title expression index rather than scanning all tracks in the library.

#### Scenario: Listing or reading tracks on a large library uses index lookups for version counts
- **WHEN** a client requests a page of tracks or a single track detail on a library with duplicate and unique titles
- **THEN** the returned version counts match existing grouping semantics and the version-count SQL query plans use the normalized title index instead of a full scan of `tracks`

### Requirement: Filtered browsing queries omit inactive optional predicates
Library browsing queries for tracks, albums, artists, works, and credit artists SHALL dynamically omit inactive optional filter predicates instead of emitting always-evaluated `(?=0 OR ...)` or `(?='' OR ...)` guards that prevent index seeks.

#### Scenario: Filtering tracks by a single album seeks by album index
- **WHEN** a client lists or counts tracks filtered by a specific `albumId`
- **THEN** the query plan seeks directly to that album's tracks via index rather than scanning all albums or all tracks, while returning identical rows and totals

#### Scenario: Filtering albums by artist or works by type/year uses matching indexes
- **WHEN** a client lists or counts albums filtered by an artist or lists works filtered by type or year
- **THEN** the query plan drives from the selective filter index rather than full-scanning the base table

### Requirement: Unified search evaluates artist alias matching once per search request
Unified search SHALL compute matching canonical artists once per search call, short-circuit artist-derived album/track/composer matches when no artists match, filter candidates before materializing ranking sets, and drive artist-to-album/track joins from matched artists rather than scanning all credits.

#### Scenario: Unmatched unified search short-circuits quickly
- **WHEN** a client submits a unified search query that matches no artists, albums, or tracks on an 18,000-track library
- **THEN** the system returns zero totals across all categories without repeating artist alias JSON expansion per category and completes well within interactive latency bounds

#### Scenario: Matched unified search preserves ranking and result parity
- **WHEN** a client searches by exact artist name, katakana/hiragana variant, merged artist alias, album title, or track title
- **THEN** the returned best match, per-category totals, ordering, and pagination results remain identical to existing search semantics

### Requirement: Artist listing uses two-stage pagination and avoids correlated image scans
Artist browsing SHALL resolve the paginated artist IDs before hydrating per-artist album counts, track counts, and image URLs, and artist image URL resolution SHALL avoid non-indexed `OR artist_id IN (...)` table scans.

#### Scenario: Artist browsing returns consistent pages without pre-pagination full-table hydration
- **WHEN** a client requests any page of artists sorted by name, favorites, or random seed
- **THEN** display counts and image URLs are hydrated only for the selected page of artists (except when sorting explicitly by album count or track count), and results match existing visibility and sorting rules

### Requirement: Album recency sorting, playback state derivation, and random album sampling avoid quadratic or redundant scans
- Sorting albums by `recentlyPlayed` SHALL evaluate each album's latest playback timestamp at most once per album rather than twice.
- Deriving effective playback state for a batch of tracks SHALL resolve each track's latest ended session without quadratic per-session correlated sorting.
- Selecting a random visible album SHALL avoid sorting the entire albums table with `ORDER BY RANDOM()`.

#### Scenario: Effective playback state remains accurate for frequently played tracks
- **WHEN** effective playback states are requested for tracks that have hundreds of ended playback sessions
- **THEN** the derived state matches existing precedence rules (open session over latest ended session, tie-broken by latest `ended_at` then highest `rowid`) using a linear or indexed plan rather than a correlated temp-sort per session

#### Scenario: Random album selection excludes the specified album when alternatives exist
- **WHEN** a client requests a random visible album with an excluded album ID
- **THEN** the system returns a visible album different from the excluded ID whenever at least two visible albums exist, falls back to the excluded album if it is the only visible album, and avoids full-table `ORDER BY RANDOM()` sorting
