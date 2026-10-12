package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// setupTestApp's API token.
const testAppAPIToken = "valid-test-token-at-least-24-chars"

// apiGet issues a GET with either a Bearer token, an admin cookie, or no
// credentials at all.
func apiGet(t *testing.T, app *App, path, bearer string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

// assertAPIAuth checks the shared /api/v1 read auth rules on path: no
// credentials and a wrong Bearer token are 401, a valid Bearer token and an
// admin session both pass.
func assertAPIAuth(t *testing.T, app *App, path string) {
	t.Helper()
	if rec := apiGet(t, app, path, "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("%s without credentials: %d", path, rec.Code)
	}
	if rec := apiGet(t, app, path, "wrong-token-wrong-token-wrong", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("%s with a wrong Bearer token: %d", path, rec.Code)
	}
	if rec := apiGet(t, app, path, testAppAPIToken, nil); rec.Code != http.StatusOK {
		t.Fatalf("%s with Bearer: %d %s", path, rec.Code, rec.Body.String())
	}
	if rec := apiGet(t, app, path, "", loginCookie(t, app)); rec.Code != http.StatusOK {
		t.Fatalf("%s with admin session: %d %s", path, rec.Code, rec.Body.String())
	}
}

func TestAPISeriesTracks(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	_, tracks := seedHomeLibrary(t, app, store)
	var workIDs []int64
	for i, links := range [][]int64{{tracks[0]}, {tracks[0], tracks[1]}} {
		work, err := store.CreateWork(ctx, storage.WorkInput{Title: []string{"First", "Second"}[i], Type: "anime", Year: 2020 + i})
		if err != nil {
			t.Fatal(err)
		}
		for _, trackID := range links {
			if err := store.AddWorkTrack(ctx, work.ID, storage.WorkTrackInput{TrackID: trackID, Role: "op"}); err != nil {
				t.Fatal(err)
			}
		}
		workIDs = append(workIDs, work.ID)
	}
	seriesID, err := store.CreateWorkSeries(ctx, "Saga", workIDs)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/series/" + itoa(seriesID) + "/tracks"
	assertAPIAuth(t, app, path)

	rec := apiGet(t, app, path, testAppAPIToken, nil)
	var body struct {
		Items []struct {
			ID        int64  `json:"id"`
			Title     string `json:"title"`
			Role      string `json:"role"`
			StreamURL string `json:"streamUrl"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].ID != tracks[0] || body.Items[1].ID != tracks[1] {
		t.Fatalf("series tracks must follow member order and keep a shared song once: %+v", body.Items)
	}
	if body.Items[0].Role != "op" || body.Items[0].StreamURL == "" {
		t.Fatalf("items must reuse the WorkTrack shape: %+v", body.Items[0])
	}

	if rec := apiGet(t, app, "/api/v1/series/999999/tracks", testAppAPIToken, nil); rec.Code != http.StatusNotFound || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("unknown series: %d %s", rec.Code, rec.Body.String())
	}
}
