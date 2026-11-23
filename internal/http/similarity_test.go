package httpapi

import (
	"context"
	"encoding/json"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSimilarityHTTPValidationAndAuth(t *testing.T) {
	app, _, token := setupTestApp(t)
	h := app.Handler()
	for _, tc := range []struct {
		url    string
		auth   bool
		status int
	}{
		{"/api/v1/tracks/1/similar?limit=0", true, 400},
		{"/api/v1/tracks/1/similar?limit=101", true, 400},
		{"/api/v1/tracks/abc/similar", true, 400},
		{"/api/v1/tracks/1/similar?exclude=abc", true, 400},
		{"/api/v1/tracks/1/similar?diverse=maybe", true, 400},
		{"/api/v1/tracks/1/similar?maxPerArtist=0", true, 400},
		{"/api/v1/tracks/1/similar?maxPerArtist=11", true, 400},
		{"/api/v1/tracks/999999/similar", true, 404},
		{"/api/v1/tracks/path?from=1&to=2&limit=1", true, 400},
		{"/api/v1/tracks/path?from=no&to=2", true, 400},
		{"/api/v1/tracks/path?from=999999&to=1", true, 404},
		{"/api/v1/tracks/999999/similar?mediaToken=bad", false, 401},
		{"/api/v1/capabilities", true, 200},
	} {
		req := httptest.NewRequest("GET", tc.url, nil)
		if tc.auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Errorf("%s status=%d want=%d body=%s", tc.url, w.Code, tc.status, w.Body.String())
		}
		if tc.url == "/api/v1/capabilities" && !strings.Contains(w.Body.String(), `"method":"metadata"`) {
			t.Error("missing metadata capability")
		}
	}
	_ = http.StatusOK
}

func TestSimilarityHTTPExcludeDiverseAndHybridCapability(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	if e := store.EnsureLibrary(ctx, "sim2", "/sim2"); e != nil {
		t.Fatal(e)
	}
	lib, e := store.LibraryByRoot(ctx, "/sim2")
	if e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 3; i++ {
		if e := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID: lib.ID, RelativePath: jsonNumber(int64(i)) + ".flac", FileSize: 1, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: "Track " + jsonNumber(int64(i)), Album: "sim2", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i},
		}); e != nil {
			t.Fatal(e)
		}
	}
	tracks, e := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if e != nil || len(tracks) != 3 {
		t.Fatalf("tracks=%d err=%v", len(tracks), e)
	}
	req := httptest.NewRequest("GET", "/api/v1/tracks/"+jsonNumber(tracks[0].ID)+"/similar?limit=10&diverse=1&exclude="+jsonNumber(tracks[1].ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []struct {
			Track struct{ ID int64 `json:"id"` } `json:"track"`
		} `json:"items"`
	}
	if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if len(body.Items) != 1 || body.Items[0].Track.ID != tracks[2].ID {
		t.Fatalf("expected only track %d, got %+v", tracks[2].ID, body.Items)
	}
	// Adding an acoustic embedding switches capabilities.similarity.method to "hybrid".
	if e := store.SaveTrackAudioFeatures(ctx, storage.TrackAudioFeatures{
		TrackID: tracks[0].ID, Fingerprint: "fp1", AnalysisVersion: "v1", Mode: "fast",
		Genre: []byte{1}, MoodTheme: []byte{1}, Instrument: []byte{1}, HeadsJSON: "{}",
		Embedding: []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}); e != nil {
		t.Fatal(e)
	}
	capReq := httptest.NewRequest("GET", "/api/v1/capabilities", nil)
	capReq.Header.Set("Authorization", "Bearer "+token)
	capRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(capRec, capReq)
	if capRec.Code != 200 || !strings.Contains(capRec.Body.String(), `"method":"hybrid"`) {
		t.Fatalf("expected hybrid capability, got %d %s", capRec.Code, capRec.Body.String())
	}
}
