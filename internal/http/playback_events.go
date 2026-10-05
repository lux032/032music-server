package httpapi

import (
	"errors"
	"net/http"

	"github.com/lux032/032music-server/internal/storage"
)

// handlePlaybackEvents is the single playback reporting endpoint (plan C).
// Clients drive durable sessions with typed events; counting, skips, the
// breakpoint aggregate and the Last.fm outbox are derived server-side in
// the same transaction. The legacy /api/v1/playback/timeline and
// /api/v1/playback/scrobble endpoints were removed without a compatibility
// path: they answer 410 via handlePlaybackGone.
//
// Error protocol (clients must not guess):
//   - 400 invalid_request / invalid_json: malformed event (enums, bounds).
//   - 404 not_found: trackId does not exist.
//   - 404 session_not_found: event for an unknown/cleared session → start
//     a new session.
//   - 409 session_expired: the lease ran out; the server finalized the
//     session as ended/expired → start a new session with
//     resumedFromSessionId to inherit the counted flag.
//   - 409 session_owner_mismatch: the session belongs to another clientId.
//   - 409 session_conflict: trackId/clientKind differ from the session.
//   - 409 resume_invalid: resumedFromSessionId is unknown, belongs to
//     another client/track, or names a predecessor that ended completed,
//     skipped or replaced. (An active predecessor on the same
//     client+track is not an error: it is superseded as replaced in the
//     same transaction and the resume is accepted, M-3.)
func (a *App) handlePlaybackEvents(w http.ResponseWriter, r *http.Request) {
	var input storage.PlaybackEventInput
	if !decode(w, r, &input) {
		return
	}
	result, err := a.store.RecordPlaybackEvent(r.Context(), input)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrInvalidPlaybackEvent):
			writeAPIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		case errors.Is(err, storage.ErrTrackNotFound):
			writeAPIError(w, http.StatusNotFound, "not_found", "The requested track was not found.")
		case errors.Is(err, storage.ErrSessionNotFound):
			writeAPIError(w, http.StatusNotFound, "session_not_found", "The playback session is unknown; start a new session.")
		case errors.Is(err, storage.ErrSessionExpired):
			writeAPIError(w, http.StatusConflict, "session_expired", "The playback session expired; start a new session with resumedFromSessionId to keep the resume chain intact.")
		case errors.Is(err, storage.ErrSessionOwnerMismatch):
			writeAPIError(w, http.StatusConflict, "session_owner_mismatch", "The playback session belongs to a different clientId.")
		case errors.Is(err, storage.ErrSessionConflict):
			writeAPIError(w, http.StatusConflict, "session_conflict", "trackId and clientKind are immutable for the life of a session.")
		case errors.Is(err, storage.ErrResumeInvalid):
			writeAPIError(w, http.StatusConflict, "resume_invalid", "resumedFromSessionId must name a session of the same client and track that ended with a resumable reason (expired, client_closed, error, stopped); an active predecessor would have been superseded as replaced.")
		default:
			a.logger.Error("playback event failed", "error", err)
			writeAPIError(w, http.StatusInternalServerError, "playback_event_failed", "The request could not be completed.")
		}
		return
	}
	if result.QueuedForLastFM && a.lastfm != nil {
		a.lastfm.Wake()
	}
	if result.Applied && result.State == "playing" && a.lastfm != nil {
		a.lastfm.NowPlaying(input.TrackID)
	}
	writeJSON(w, http.StatusOK, result)
}

// handlePlaybackGone answers the removed legacy playback endpoints. There is
// no compatibility path: old clients must upgrade to the session protocol.
func handlePlaybackGone(w http.ResponseWriter, _ *http.Request) {
	writeAPIError(w, http.StatusGone, "gone", "This endpoint was removed; use POST /api/v1/playback/events with playback sessions (apiRevision 3).")
}
