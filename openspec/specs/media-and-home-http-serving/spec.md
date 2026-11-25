# media-and-home-http-serving Specification

## Purpose

Defines HTTP caching semantics for resized media fallbacks, local work poster lookup caching, and consolidated data aggregation for series and home HTTP endpoints.

## Requirements

### Requirement: Intentional small-image thumbnail fallback preserves HTTP caching headers
When a client requests an artwork, artist portrait, playlist cover, or work poster with a `?size=` parameter and the source image is intentionally served unmodified (such as when its dimensions are already within the requested size bucket or the format is intentionally bypassed), the server SHALL preserve the endpoint's normal `Cache-Control` policy and emit an `ETag`. The server SHALL only downgrade to `Cache-Control: no-cache` when thumbnail serving fails due to a transient error.

#### Scenario: Small album cover requested with size parameter remains cacheable
- **WHEN** a client requests `/api/v1/artwork/{id}?size=360` for a 500x500 album cover (within the 512 bucket)
- **THEN** the server serves the original image with `Cache-Control: public, max-age=31536000, immutable` and a stable `ETag` rather than `Cache-Control: no-cache`

#### Scenario: Small artist portrait requested with size parameter remains cacheable
- **WHEN** a client requests `/api/v1/artists/{id}/image?size=240` for a portrait smaller than or equal to the target bucket
- **THEN** the server serves the original image with `Cache-Control: public, max-age=86400` and a stable `ETag`

#### Scenario: Transient thumbnail generation failure still sends no-cache
- **WHEN** thumbnail generation fails due to a transient read/write error on a large image
- **THEN** the server falls back to serving the original image with `Cache-Control: no-cache` so clients retry later

### Requirement: Work poster cache lookups avoid repeated per-extension filesystem stat calls
Local work poster presence lookup SHALL be backed by an in-memory index synchronized with the poster cache directory and poster writes, avoiding repeated multi-extension `os.Stat` probing on every lookup or admin stats page render.

#### Scenario: Cached poster lookup hits memory after initial directory load
- **WHEN** pages or admin stats check local poster availability for many works
- **THEN** lookups resolve cached file path and MIME type from the in-memory index, newly downloaded posters are immediately visible in the index, and a file missing on open invalidates its stale entry cleanly

### Requirement: Series detail and home endpoints avoid redundant queries and N+1 hydration
- The series detail API SHALL compute distinct album and track counts via aggregated database queries rather than fetching and discarding full album and track lists per member work.
- Home page and home JSON API assembly SHALL reuse already-fetched recent albums, unfinished playback records, and year facet counts instead of executing duplicate queries, and SHALL batch album artist reference attachment across home sections.

#### Scenario: Series detail API returns exact counts without per-member full hydration
- **WHEN** a client requests `GET /api/v1/series/{id}` for a multi-work series
- **THEN** `stats.albumCount` and `stats.trackCount` match the distinct visible album and track counts across member works without invoking per-member `AlbumsForWork` and `TracksForWork` loops

#### Scenario: Home API produces identical payload with deduplicated queries
- **WHEN** a client requests `/admin/home` or `GET /api/v1/home`
- **THEN** the response fields for hero spotlight, recent albums, unfinished playback, chronicle totals, and album artist references match existing behavior while reusing shared query results and batching artist-ref hydration
