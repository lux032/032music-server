package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func postTimeline(t *testing.T, handler http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/timeline", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestPlaybackTimelineSkipFields(t *testing.T) {
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	handler := app.Handler()

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
	id := strconv.FormatInt(tracks.Items[0].ID, 10)

	t.Run("unknown field rejected", func(t *testing.T) {
		rec := postTimeline(t, handler, token, `{"trackId":`+id+`,"state":"playing","positionMillis":1,"durationMillis":2,"mystery":true}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("explicit skip requires stopped state", func(t *testing.T) {
		rec := postTimeline(t, handler, token, `{"trackId":`+id+`,"state":"playing","positionMillis":1,"durationMillis":2,"skipped":true}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("oversized client id rejected", func(t *testing.T) {
		body := `{"trackId":` + id + `,"state":"playing","positionMillis":1,"durationMillis":2,"clientId":"` + strings.Repeat("c", 129) + `"}`
		rec := postTimeline(t, handler, token, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("explicit skip accepted and visible in history", func(t *testing.T) {
		// History only lists tracks with a play record, so play once first.
		rec := postTimeline(t, handler, token, `{"trackId":`+id+`,"state":"playing","positionMillis":1000,"durationMillis":240000}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("playing status = %d, body = %s", rec.Code, rec.Body.String())
		}
		rec = postTimeline(t, handler, token, `{"trackId":`+id+`,"state":"stopped","positionMillis":0,"durationMillis":240000,"continuing":true,"skipped":true,"clientId":"test-device"}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/playback/history?limit=10", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		historyRec := httptest.NewRecorder()
		handler.ServeHTTP(historyRec, req)
		if historyRec.Code != http.StatusOK {
			t.Fatalf("history status = %d", historyRec.Code)
		}
		var page struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.NewDecoder(historyRec.Body).Decode(&page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("history items = %v", page.Items)
		}
		if page.Items[0]["skipCount"] != float64(1) {
			t.Fatalf("history record = %v", page.Items[0])
		}
		if _, ok := page.Items[0]["lastSkippedAt"]; !ok {
			t.Fatalf("lastSkippedAt missing: %v", page.Items[0])
		}
	})

	t.Run("inferred skip end to end", func(t *testing.T) {
		rec := postTimeline(t, handler, token, `{"trackId":`+id+`,"state":"playing","positionMillis":5000,"durationMillis":240000}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("playing status = %d, body = %s", rec.Code, rec.Body.String())
		}
		rec = postTimeline(t, handler, token, `{"trackId":`+id+`,"state":"stopped","positionMillis":0,"durationMillis":240000,"continuing":true}`)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("stopped status = %d, body = %s", rec.Code, rec.Body.String())
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tracks/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		trackRec := httptest.NewRecorder()
		handler.ServeHTTP(trackRec, req)
		if trackRec.Code != http.StatusOK {
			t.Fatalf("track status = %d", trackRec.Code)
		}
		var fields map[string]any
		if err := json.NewDecoder(trackRec.Body).Decode(&fields); err != nil {
			t.Fatal(err)
		}
		if fields["skipCount"] != float64(2) {
			t.Fatalf("track skipCount = %v (body %s)", fields["skipCount"], trackRec.Body.String())
		}
	})
}

func TestCapabilitiesSkipInference(t *testing.T) {
	app, _, token := setupTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Features map[string]bool `json:"features"`
		Playback struct {
			SkipInference struct {
				ThresholdMillis   int     `json:"thresholdMillis"`
				ThresholdFraction float64 `json:"thresholdFraction"`
				WindowMinutes     int     `json:"windowMinutes"`
				ExplicitField     string  `json:"explicitField"`
			} `json:"skipInference"`
		} `json:"playback"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Features["skipInference"] {
		t.Fatalf("features: %v", body.Features)
	}
	skip := body.Playback.SkipInference
	if skip.ThresholdMillis != 30000 || skip.ThresholdFraction != 0.5 || skip.WindowMinutes != 30 || skip.ExplicitField != "skipped" {
		t.Fatalf("skip inference capabilities: %+v", skip)
	}
}
