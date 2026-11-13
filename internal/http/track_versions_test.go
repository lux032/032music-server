package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestTrackVersionsAndCounts(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "music", "/tmp/music"); err != nil {
		t.Fatalf("EnsureLibrary: %v", err)
	}
	lib, err := store.LibraryByRoot(ctx, "/tmp/music")
	if err != nil {
		t.Fatalf("LibraryByRoot: %v", err)
	}
	for i, tc := range []struct {
		path  string
		title string
		album string
		year  int
	}{
		{"a/01.flac", "Crimson Stain", "Crimson Stain", 2016},
		{"b/01.flac", "crimson stain", "O2O", 2017},
		{"c/01.flac", "Unique Song", "Single", 2020},
	} {
		if err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    lib.ID,
			RelativePath: tc.path,
			FileSize:     100,
			ModifiedAtNS: int64(i + 1),
			Metadata: metadata.AudioMetadata{
				Title:          tc.title,
				Album:          tc.album,
				Year:           tc.year,
				Artists:        []string{"96猫"},
				AlbumArtists:   []string{"96猫"},
				Composer:       "Masaki Iehara",
				DiscNumber:     1,
				TrackNumber:    1,
				DurationMillis: 192000,
			},
		}); err != nil {
			t.Fatalf("ImportTrack %s: %v", tc.path, err)
		}
	}

	h := app.Handler()
	get := func(url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	listRes := get("/api/v1/tracks?sort=title")
	if listRes.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRes.Code, listRes.Body.String())
	}
	var list struct {
		Items []storage.Track `json:"items"`
	}
	if err := json.Unmarshal(listRes.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 tracks, got %d", len(list.Items))
	}
	var firstID int64
	for _, tr := range list.Items {
		if tr.Title == "Unique Song" && tr.VersionCount != 0 {
			t.Fatalf("expected Unique Song versionCount=0, got %d", tr.VersionCount)
		}
		if tr.Title == "Crimson Stain" {
			firstID = tr.ID
			if tr.VersionCount != 2 {
				t.Fatalf("expected Crimson Stain versionCount=2, got %d", tr.VersionCount)
			}
		}
	}
	if firstID == 0 {
		t.Fatal("Crimson Stain not found")
	}

	verRes := get(fmt.Sprintf("/api/v1/tracks/%d/versions", firstID))
	if verRes.Code != http.StatusOK {
		t.Fatalf("versions status=%d body=%s", verRes.Code, verRes.Body.String())
	}
	var ver struct {
		Track storage.Track   `json:"track"`
		Items []storage.Track `json:"items"`
	}
	if err := json.Unmarshal(verRes.Body.Bytes(), &ver); err != nil {
		t.Fatalf("decode versions: %v", err)
	}
	if ver.Track.ID != firstID || len(ver.Items) != 2 {
		t.Fatalf("unexpected versions payload: %+v", ver)
	}
	if ver.Items[0].Year != 2016 || ver.Items[1].Year != 2017 {
		t.Fatalf("expected years [2016, 2017], got [%d, %d]", ver.Items[0].Year, ver.Items[1].Year)
	}

	notFound := get("/api/v1/tracks/999999/versions")
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing track, got %d", notFound.Code)
	}
}
