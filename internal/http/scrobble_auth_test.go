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
	"time"

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

func TestPlaybackReportAcceptsMediaToken(t *testing.T) {
	app, store, id := setupScrobbleApp(t)
	idText := itoa64(id)
	now := time.Now()

	// App clients that stream with ?mediaToken= can report with the same
	// query parameter...
	response := postPlayback(app, "/api/v1/playback/timeline?mediaToken="+scrobbleTestMediaToken, "", `{"trackId":`+idText+`,"state":"playing","positionMillis":1000,"durationMillis":200000}`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("timeline with media query token: %d %s", response.Code, response.Body.String())
	}
	response = postPlayback(app, "/api/v1/playback/scrobble?mediaToken="+scrobbleTestMediaToken, "", `{"trackId":`+idText+`,"positionMillis":100000,"durationMillis":200000,"timestamp":"`+now.Add(-2*time.Hour).UTC().Format(time.RFC3339)+`"}`)
	if response.Code != http.StatusNoContent || trackPlayCount(t, store, id) != 1 {
		t.Fatalf("scrobble with media query token: %d %s", response.Code, response.Body.String())
	}
	// ...or as a Bearer header; UNIX-seconds timestamps are accepted too.
	response = postPlayback(app, "/api/v1/playback/scrobble", "Bearer "+scrobbleTestMediaToken, `{"trackId":`+idText+`,"positionMillis":150000,"timestamp":`+itoa64(now.Add(-time.Hour).Unix())+`}`)
	if response.Code != http.StatusNoContent || trackPlayCount(t, store, id) != 2 {
		t.Fatalf("scrobble with media bearer token: %d %s", response.Code, response.Body.String())
	}
	// API token keeps working.
	response = postPlayback(app, "/api/v1/playback/scrobble", "Bearer "+scrobbleTestAPIToken, `{"trackId":`+idText+`,"positionMillis":150000}`)
	if response.Code != http.StatusNoContent || trackPlayCount(t, store, id) != 3 {
		t.Fatalf("scrobble with API token: %d %s", response.Code, response.Body.String())
	}
}

func TestPlaybackReportThresholdAndDuplicateResponses(t *testing.T) {
	app, store, id := setupScrobbleApp(t)
	idText := itoa64(id)
	response := postPlayback(app, "/api/v1/playback/scrobble", "Bearer "+scrobbleTestMediaToken, `{"trackId":`+idText+`,"positionMillis":30000}`)
	var body struct {
		Recorded bool   `json:"recorded"`
		Reason   string `json:"reason"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Recorded || body.Reason != storage.ScrobbleReasonThreshold {
		t.Fatalf("below half: %d %s", response.Code, response.Body.String())
	}
	if response = postPlayback(app, "/api/v1/playback/scrobble", "Bearer "+scrobbleTestMediaToken, `{"trackId":`+idText+`,"positionMillis":100000}`); response.Code != http.StatusNoContent {
		t.Fatalf("half: %d %s", response.Code, response.Body.String())
	}
	response = postPlayback(app, "/api/v1/playback/scrobble", "Bearer "+scrobbleTestMediaToken, `{"trackId":`+idText+`,"positionMillis":100500}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), storage.ScrobbleReasonDuplicate) || trackPlayCount(t, store, id) != 1 {
		t.Fatalf("duplicate: %d %s", response.Code, response.Body.String())
	}
	if response = postPlayback(app, "/api/v1/playback/scrobble", "Bearer "+scrobbleTestMediaToken, `{"trackId":`+idText+`,"positionMillis":100000,"timestamp":"yesterday"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid timestamp: %d %s", response.Code, response.Body.String())
	}
}

func TestMediaTokenGrantsOnlyPlaybackReporting(t *testing.T) {
	app, _, id := setupScrobbleApp(t)
	idText := itoa64(id)
	for _, auth := range []string{"Bearer wrong-token-at-least-24-characters", ""} {
		if response := postPlayback(app, "/api/v1/playback/scrobble", auth, `{"trackId":`+idText+`,"positionMillis":100000}`); response.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q: status %d", auth, response.Code)
		}
	}
	if response := postPlayback(app, "/api/v1/playback/scrobble?mediaToken=wrong", "", `{"trackId":`+idText+`,"positionMillis":100000}`); response.Code != http.StatusUnauthorized {
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
