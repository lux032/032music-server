package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

const (
	scrobbleTestAPIToken   = "api-token-at-least-24-characters"
	scrobbleTestMediaToken = "media-token-at-least-24-characters"
)

func setupScrobbleApp(t *testing.T) (*App, *storage.Store, int64) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "scrobble.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{APIToken: scrobbleTestAPIToken, MediaToken: scrobbleTestMediaToken, DataDirectory: t.TempDir()}
	app, err := NewApp(cfg, store, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLibrary(ctx, "Default", "/music"); err != nil {
		t.Fatal(err)
	}
	library, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "a.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, DurationMillis: 200000, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 1})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks: %v %v", tracks, err)
	}
	return app, store, tracks[0].ID
}

func postPlayback(app *App, path, auth, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func trackPlayCount(t *testing.T, store *storage.Store, id int64) int64 {
	t.Helper()
	track, err := store.TrackByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return track.PlayCount
}

func TestPlaybackEventsAcceptMediaToken(t *testing.T) {
	app, store, id := setupScrobbleApp(t)
	idText := itoa64(id)

	// App clients that stream with ?mediaToken= can report with the same
	// query parameter...
	response := postPlayback(app, "/api/v1/playback/events?mediaToken="+scrobbleTestMediaToken, "", `{"clientId":"device-1","clientKind":"android","sessionId":"session-media-01","seq":1,"type":"start","trackId":`+idText+`,"state":"playing","positionMillis":1000,"durationMillis":200000}`)
	if response.Code != http.StatusOK {
		t.Fatalf("start with media query token: %d %s", response.Code, response.Body.String())
	}
	// ...or as a Bearer header.
	response = postPlayback(app, "/api/v1/playback/events", "Bearer "+scrobbleTestMediaToken, `{"clientId":"device-1","clientKind":"android","sessionId":"session-media-01","seq":2,"type":"heartbeat","trackId":`+idText+`,"state":"playing","positionMillis":150000,"durationMillis":200000}`)
	if response.Code != http.StatusOK || trackPlayCount(t, store, id) != 1 {
		t.Fatalf("heartbeat with media bearer token: %d %s", response.Code, response.Body.String())
	}
	// API token keeps working on a separate session.
	response = postPlayback(app, "/api/v1/playback/events", "Bearer "+scrobbleTestAPIToken, `{"clientId":"device-2","clientKind":"web","sessionId":"session-media-02","seq":1,"type":"start","trackId":`+idText+`,"state":"playing","positionMillis":150000,"durationMillis":200000}`)
	if response.Code != http.StatusOK || trackPlayCount(t, store, id) != 2 {
		t.Fatalf("start with API token: %d %s", response.Code, response.Body.String())
	}
}

func TestPlaybackEventsCountedAndIdempotentResponses(t *testing.T) {
	app, _, id := setupScrobbleApp(t)
	idText := itoa64(id)

	decodeBody := func(response *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", response.Body.String(), err)
		}
		return body
	}

	// Below the 50% threshold the session is accepted but not yet counted.
	response := postPlayback(app, "/api/v1/playback/events", "Bearer "+scrobbleTestMediaToken, `{"clientId":"device-1","clientKind":"web","sessionId":"session-count-01","seq":1,"type":"start","trackId":`+idText+`,"state":"playing","positionMillis":30000,"durationMillis":200000}`)
	body := decodeBody(response)
	if response.Code != http.StatusOK || body["applied"] != true || body["counted"] != false {
		t.Fatalf("below half: %d %s", response.Code, response.Body.String())
	}
	// Crossing half counts once.
	response = postPlayback(app, "/api/v1/playback/events", "Bearer "+scrobbleTestMediaToken, `{"clientId":"device-1","clientKind":"web","sessionId":"session-count-01","seq":2,"type":"heartbeat","trackId":`+idText+`,"state":"playing","positionMillis":100000,"durationMillis":200000}`)
	if body = decodeBody(response); response.Code != http.StatusOK || body["counted"] != true {
		t.Fatalf("half: %d %s", response.Code, response.Body.String())
	}
	// A retried (stale-seq) event is an idempotent applied=false no-op.
	response = postPlayback(app, "/api/v1/playback/events", "Bearer "+scrobbleTestMediaToken, `{"clientId":"device-1","clientKind":"web","sessionId":"session-count-01","seq":2,"type":"heartbeat","trackId":`+idText+`,"state":"playing","positionMillis":100500,"durationMillis":200000}`)
	if body = decodeBody(response); response.Code != http.StatusOK || body["applied"] != false {
		t.Fatalf("retry: %d %s", response.Code, response.Body.String())
	}
	// Unknown fields are rejected.
	response = postPlayback(app, "/api/v1/playback/events", "Bearer "+scrobbleTestMediaToken, `{"clientId":"device-1","clientKind":"web","sessionId":"session-count-02","seq":1,"type":"start","trackId":`+idText+`,"state":"playing","positionMillis":1,"durationMillis":2,"mystery":true}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d %s", response.Code, response.Body.String())
	}
	// Invalid values are rejected with invalid_request.
	response = postPlayback(app, "/api/v1/playback/events", "Bearer "+scrobbleTestMediaToken, `{"clientId":"device-1","clientKind":"web","sessionId":"session-count-03","seq":0,"type":"start","trackId":`+idText+`,"state":"playing","positionMillis":1,"durationMillis":2}`)
	body = decodeBody(response)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_request") {
		t.Fatalf("seq 0: %d %s", response.Code, response.Body.String())
	}
	_ = body
}

func TestMediaTokenGrantsOnlyPlaybackReporting(t *testing.T) {
	app, _, id := setupScrobbleApp(t)
	idText := itoa64(id)
	valid := `{"clientId":"device-1","clientKind":"web","sessionId":"session-authz-01","seq":1,"type":"start","trackId":` + idText + `,"state":"playing","positionMillis":1000,"durationMillis":200000}`
	for _, auth := range []string{"Bearer wrong-token-at-least-24-characters", ""} {
		if response := postPlayback(app, "/api/v1/playback/events", auth, valid); response.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q: status %d", auth, response.Code)
		}
	}
	if response := postPlayback(app, "/api/v1/playback/events?mediaToken=wrong", "", valid); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong query token: status %d", response.Code)
	}
	// The media token stays read-only everywhere else.
	request := httptest.NewRequest(http.MethodPut, "/api/v1/tracks/"+idText+"/favorite", nil)
	request.Header.Set("Authorization", "Bearer "+scrobbleTestMediaToken)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("media token on favorite: status %d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/playback/history?mediaToken="+scrobbleTestMediaToken, nil)
	response = httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("media token on history: status %d", response.Code)
	}
}

func itoa64(value int64) string {
	data, _ := json.Marshal(value)
	return string(data)
}
