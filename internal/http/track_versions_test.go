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
		path     string
		title    string
		album    string
		year     int
		artist   string
		composer string
	}{
		{"a/01.flac", "Crimson Stain", "Crimson Stain", 2016, "96猫", "Masaki Iehara"},
		{"b/01.flac", "crimson stain", "O2O", 2017, "96猫", "Masaki Iehara"},
		{"d/01.flac", "Crimson Stain", "Unrelated Album", 2019, "Other Artist", "Other Composer"},
		{"c/01.flac", "Unique Song", "Single", 2020, "96猫", "Masaki Iehara"},
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
				Artists:        []string{tc.artist},
				AlbumArtists:   []string{tc.artist},
				Composer:       tc.composer,
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
	if len(list.Items) != 4 {
		t.Fatalf("expected 4 tracks, got %d", len(list.Items))
	}
	var firstID, unrelatedID int64
	for _, tr := range list.Items {
		if tr.Title == "Unique Song" && tr.VersionCount != 0 {
			t.Fatalf("expected Unique Song versionCount=0, got %d", tr.VersionCount)
		}
		if tr.Title == "Crimson Stain" && tr.Composer == "Other Composer" {
			unrelatedID = tr.ID
			if tr.VersionCount != 0 {
				t.Fatalf("expected unrelated Crimson Stain versionCount=0, got %d", tr.VersionCount)
			}
		} else if tr.Title == "Crimson Stain" {
			firstID = tr.ID
			if tr.VersionCount != 2 {
				t.Fatalf("expected Crimson Stain versionCount=2, got %d", tr.VersionCount)
			}
		}
	}
	if firstID == 0 || unrelatedID == 0 {
		t.Fatal("Crimson Stain tracks not found")
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

	unrelRes := get(fmt.Sprintf("/api/v1/tracks/%d/versions", unrelatedID))
	if unrelRes.Code != http.StatusOK {
		t.Fatalf("unrelated versions status=%d body=%s", unrelRes.Code, unrelRes.Body.String())
	}
	var unrelVer struct {
		Track storage.Track   `json:"track"`
		Items []storage.Track `json:"items"`
	}
	if err := json.Unmarshal(unrelRes.Body.Bytes(), &unrelVer); err != nil {
		t.Fatalf("decode unrelated versions: %v", err)
	}
	if unrelVer.Track.ID != unrelatedID || len(unrelVer.Items) != 1 {
		t.Fatalf("expected only unrelated track itself, got %+v", unrelVer)
	}

	notFound := get("/api/v1/tracks/999999/versions")
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing track, got %d", notFound.Code)
	}
}
