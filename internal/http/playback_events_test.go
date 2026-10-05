package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func postEvent(t *testing.T, handler http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/events", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// setupPlaybackEventsApp returns an app with one imported 240 s track.
func setupPlaybackEventsApp(t *testing.T) (*App, *storage.Store, string, int64) {
	t.Helper()
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Artist/Album/01.flac", FileSize: 1024, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 240000}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil || len(tracks.Items) != 1 {
		t.Fatalf("sync tracks: %v %v", tracks, err)
	}
	return app, store, token, tracks.Items[0].ID
}

func eventBody(sessionID string, seq int64, eventType, state string, trackID, position, duration int64) string {
	body := `{"clientId":"device-1","clientKind":"android","sessionId":"` + sessionID + `","seq":` + strconv.FormatInt(seq, 10) +
		`,"type":"` + eventType + `","trackId":` + strconv.FormatInt(trackID, 10) +
		`,"positionMillis":` + strconv.FormatInt(position, 10) + `,"durationMillis":` + strconv.FormatInt(duration, 10)
	if state != "" {
		body += `,"state":"` + state + `"`
	}
	return body + `}`
}

func historyItems(t *testing.T, handler http.Handler, token string) []map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/playback/history?limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("history status = %d", rec.Code)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

func TestPlaybackEventsSkipAndHistory(t *testing.T) {
	app, _, token, trackID := setupPlaybackEventsApp(t)
	handler := app.Handler()

	// An explicit skipped end below MIN(30s, duration/2) counts one skip and
	// is visible in history (H4, replaces the old continuing inference).
	if rec := postEvent(t, handler, token, eventBody("session-http-skip", 1, "start", "playing", trackID, 0, 240000)); rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	end := eventBody("session-http-skip", 2, "end", "", trackID, 0, 240000)
	end = strings.TrimSuffix(end, `}`) + `,"endReason":"skipped"}`
	if rec := postEvent(t, handler, token, end); rec.Code != http.StatusOK {
		t.Fatalf("end: %d %s", rec.Code, rec.Body.String())
	}
	items := historyItems(t, handler, token)
	if len(items) != 1 {
		t.Fatalf("history items = %v", items)
	}
	if items[0]["skipCount"] != float64(1) {
		t.Fatalf("history record = %v", items[0])
	}
	if _, ok := items[0]["lastSkippedAt"]; !ok {
		t.Fatalf("lastSkippedAt missing: %v", items[0])
	}
	if items[0]["state"] != "skipped" {
		t.Fatalf("history state = %v", items[0]["state"])
	}
}

func TestPlaybackEventsExpiredAndResume(t *testing.T) {
	app, store, token, trackID := setupPlaybackEventsApp(t)
	handler := app.Handler()
	ctx := context.Background()

	// Play past the threshold so the session is counted.
	if rec := postEvent(t, handler, token, eventBody("session-http-exp", 1, "start", "playing", trackID, 130000, 240000)); rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	// Age the heartbeat past the 90 s lease: the next event gets a precise
	// 409 session_expired and the server finalizes the session (H2).
	old := time.Now().UTC().Add(-2 * time.Minute).Format("2006-01-02T15:04:05.000Z")
	if err := store.SetPlaybackSessionHeartbeatForTest(ctx, "session-http-exp", "playing", old); err != nil {
		t.Fatal(err)
	}
	rec := postEvent(t, handler, token, eventBody("session-http-exp", 2, "heartbeat", "playing", trackID, 140000, 240000))
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "session_expired" {
		t.Fatalf("expired: %d %s", rec.Code, rec.Body.String())
	}
	if got := historyItems(t, handler, token)[0]["state"]; got != "interrupted" {
		t.Fatalf("history state = %v, want interrupted", got)
	}

	// The client opens a new session with resumedFromSessionId and inherits
	// the counted flag: no double count (H2/H3).
	resume := eventBody("session-http-exp2", 1, "start", "playing", trackID, 140000, 240000)
	resume = strings.TrimSuffix(resume, `}`) + `,"resumedFromSessionId":"session-http-exp"}`
	rec = postEvent(t, handler, token, resume)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || body["applied"] != true || body["counted"] != true {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
	track, err := store.TrackByID(ctx, trackID)
	if err != nil || track.PlayCount != 1 {
		t.Fatalf("resume double-counted: %d", track.PlayCount)
	}

	// A fork (wrong client) is rejected with resume_invalid.
	fork := eventBody("session-http-exp3", 1, "start", "playing", trackID, 140000, 240000)
	fork = strings.Replace(fork, `"device-1"`, `"device-2"`, 1)
	fork = strings.TrimSuffix(fork, `}`) + `,"resumedFromSessionId":"session-http-exp"}`
	rec = postEvent(t, handler, token, fork)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "resume_invalid" {
		t.Fatalf("fork: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPlaybackEventsErrorProtocol(t *testing.T) {
	app, _, token, trackID := setupPlaybackEventsApp(t)
	handler := app.Handler()

	// Unknown session → 404 session_not_found (open a new session).
	rec := postEvent(t, handler, token, eventBody("session-http-404", 2, "heartbeat", "playing", trackID, 1000, 240000))
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "session_not_found" {
		t.Fatalf("missing session: %d %s", rec.Code, rec.Body.String())
	}
	// Unknown track → 404 not_found.
	rec = postEvent(t, handler, token, eventBody("session-http-404b", 1, "start", "playing", 999999, 1000, 240000))
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "not_found" {
		t.Fatalf("missing track: %d %s", rec.Code, rec.Body.String())
	}
	// Owner mismatch → 409 session_owner_mismatch.
	postEvent(t, handler, token, eventBody("session-http-own", 1, "start", "playing", trackID, 1000, 240000))
	stranger := strings.Replace(eventBody("session-http-own", 2, "heartbeat", "playing", trackID, 2000, 240000), `"device-1"`, `"device-9"`, 1)
	rec = postEvent(t, handler, token, stranger)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "session_owner_mismatch" {
		t.Fatalf("owner: %d %s", rec.Code, rec.Body.String())
	}
	// Track conflict → 409 session_conflict.
	otherTrack := eventBody("session-http-own", 2, "heartbeat", "playing", trackID+100, 2000, 240000)
	rec = postEvent(t, handler, token, otherTrack)
	if rec.Code != http.StatusConflict || errorCode(t, rec) != "session_conflict" {
		t.Fatalf("track conflict: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPlaybackLegacyEndpointsGone(t *testing.T) {
	app, _, token, _ := setupPlaybackEventsApp(t)
	handler := app.Handler()
	for _, path := range []string{"/api/v1/playback/timeline", "/api/v1/playback/scrobble"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusGone || errorCode(t, rec) != "gone" {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestPlaybackEventsClearThenRebuild(t *testing.T) {
	app, _, token, trackID := setupPlaybackEventsApp(t)
	handler := app.Handler()

	postEvent(t, handler, token, eventBody("session-http-clear", 1, "start", "playing", trackID, 1000, 240000))
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/playback/history", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear: %d", rec.Code)
	}
	// The still-playing client gets session_not_found and starts over.
	rec = postEvent(t, handler, token, eventBody("session-http-clear", 2, "heartbeat", "playing", trackID, 2000, 240000))
	if rec.Code != http.StatusNotFound || errorCode(t, rec) != "session_not_found" {
		t.Fatalf("post-clear heartbeat: %d %s", rec.Code, rec.Body.String())
	}
	rec = postEvent(t, handler, token, eventBody("session-http-clear2", 1, "start", "playing", trackID, 2000, 240000))
	if rec.Code != http.StatusOK {
		t.Fatalf("post-clear start: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCapabilitiesPlaybackSessions(t *testing.T) {
	app, _, token := setupTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		APIRevision int             `json:"apiRevision"`
		Features    map[string]bool `json:"features"`
		Playback    struct {
			Endpoint   string         `json:"endpoint"`
			Removed    []string       `json:"removedEndpoints"`
			Heartbeat  map[string]int `json:"heartbeatMillis"`
			Lease      map[string]int `json:"leaseMillis"`
			EndReasons []string       `json:"endReasons"`
			Scrobble   struct {
				Threshold float64 `json:"thresholdFraction"`
			} `json:"scrobble"`
			Skip struct {
				EndReason string `json:"endReason"`
			} `json:"skip"`
		} `json:"playback"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.APIRevision != 3 {
		t.Fatalf("apiRevision = %d", body.APIRevision)
	}
	if !body.Features["playbackEvents"] {
		t.Fatalf("features: %v", body.Features)
	}
	if _, ok := body.Features["skipInference"]; ok {
		t.Fatalf("legacy skipInference flag kept: %v", body.Features)
	}
	if _, ok := body.Features["scrobble"]; ok {
		t.Fatalf("legacy scrobble flag kept: %v", body.Features)
	}
	if body.Playback.Endpoint != "/api/v1/playback/events" || len(body.Playback.Removed) != 2 {
		t.Fatalf("playback capabilities: %+v", body.Playback)
	}
	if body.Playback.Heartbeat["active"] != 15000 || body.Playback.Heartbeat["paused"] != 60000 ||
		body.Playback.Lease["active"] != 90000 || body.Playback.Lease["paused"] != 600000 {
		t.Fatalf("lease capabilities: %+v", body.Playback)
	}
	if body.Playback.Scrobble.Threshold != 0.5 || body.Playback.Skip.EndReason != "skipped" {
		t.Fatalf("counting capabilities: %+v", body.Playback)
	}
}
