# Playlist operations (API v1)

`apiRevision` remains 3. Capability features `playlistItemOps`,
`playlistArtwork`, and `playlistRevision` advertise these additive endpoints.
Writes require a Bearer API token or an administrator session with
`X-CSRF-Token`. JSON bodies are limited to 1 MiB.

- `POST /api/v1/playlists/{id}/items/append`: exactly one of
  `{ "trackIds": [1, 2, 1] }` or `{ "albumId": 3 }`. Album order uses effective
  disc/track numbers, including manual overrides.
- `POST .../items/remove`: `{ "trackIds": [1], "expectedRevision": 4 }`.
- `PUT .../order`: `{ "trackIds": [2, 1], "expectedRevision": 4 }` must contain
  every current track exactly once.
- `POST .../items/insert`: `{ "trackId": 1, "index": 0 }` is additive undo.
  The index is clamped to the current length; duplicate tracks are no-ops and
  deleted IDs are skipped. Revision changes do not overwrite other edits.

Append/insert return `added`, `removed`, `skippedDuplicate`, `skippedInvalid`,
`total`, and `revision`. First occurrence order is retained. Existing tracks
whose files are missing remain valid; playlist detail marks them `missing`.
Final unique count exceeding 5000 rejects the entire batch with
`playlist_capacity` and `remainingCapacity`. Stale remove/order revision returns
409 `playlist_conflict` without mutation. SQLite contention returns 503
`database_busy` with Retry-After; unrelated errors are not classified as busy.

Legacy `PUT .../items` stays strict full replacement and last-writer-wins when
no revision is supplied; `expectedRevision` is optional. Legacy create stays
strict and rolls back invalid IDs; its raw 5000-input limit remains unchanged.
Opt-in `POST /api/v1/playlists` with `invalidTracks: "skip"` and `trackIds`
creates/adds in one transaction and returns `{playlist, stats}`. Default create
continues returning the playlist directly.

## Artwork

`PUT .../artwork` takes multipart field `image` (JPEG/PNG/WebP by content,
10 MiB maximum, bounded dimensions). Upload and `DELETE .../artwork` reset
return JSON `{artworkUrl, revision, hasCustomArtwork}`; errors are JSON,
including 413. Own artwork wins, reset uses the existing automatic album
artwork rule. `GET .../artwork?v=<hash-prefix>&size=256` requires media access.
Only a matching version receives immutable caching; stale versions return 404.
Files are shared content-addressed names under custom-images, never music
files; lifecycle cleanup checks album/artist/playlist references.

Web undo is available about eight seconds and invalidated on navigation. Bulk
undo inserts in ascending original index order using additive operations, not
an atomic historical snapshot restore; concurrent additions are preserved.
Playlists remain single-server data without ownership or sharing. Sync is
unchanged; album-wide additions intentionally affect co-playlist similarity.
