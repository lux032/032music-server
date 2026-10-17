package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestAPITrackWorks(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	albumID, tracks := seedHomeLibrary(t, app, store)
	direct, err := store.CreateWork(ctx, storage.WorkInput{Title: "Alpha Show", Type: "anime", Year: 2020})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AddWorkTrack(ctx, direct.ID, storage.WorkTrackInput{TrackID: tracks[0], Role: "op"}); err != nil {
		t.Fatal(err)
	}
	// A second direct link of the same work must not duplicate it.
	if err = store.AddWorkTrack(ctx, direct.ID, storage.WorkTrackInput{TrackID: tracks[0], Role: "ed", Season: 2}); err != nil {
		t.Fatal(err)
	}
	byAlbum, err := store.CreateWork(ctx, storage.WorkInput{Title: "Beta Movie", Type: "movie", Year: 2021})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AddWorkAlbum(ctx, byAlbum.ID, albumID, "ost"); err != nil {
		t.Fatal(err)
	}
	seriesID, err := store.CreateWorkSeries(ctx, "Saga", []int64{direct.ID})
	if err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/tracks/" + itoa(tracks[0]) + "/works"
	assertAPIAuth(t, app, path)

	type item struct {
		ID       int64  `json:"id"`
		Title    string `json:"title"`
		Type     string `json:"type"`
		Role     string `json:"role"`
		Season   int    `json:"season"`
		SeriesID int64  `json:"seriesId"`
	}
	read := func(trackID int64) []item {
		t.Helper()
		rec := apiGet(t, app, "/api/v1/tracks/"+itoa(trackID)+"/works", testAppAPIToken, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("track %d works: %d %s", trackID, rec.Code, rec.Body.String())
		}
		var body struct {
			Items []item `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Items
	}

	got := read(tracks[0])
	want := []item{
		{ID: direct.ID, Title: "Alpha Show", Type: "anime", Role: "ed", Season: 2, SeriesID: seriesID},
		{ID: byAlbum.ID, Title: "Beta Movie", Type: "movie", Role: "ost"},
	}
	// Both direct links are manual: the tie-break is role, then season ("ed" < "op").
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("track works = %+v, want %+v", got, want)
	}
	if got := read(tracks[2]); len(got) != 1 || got[0].ID != byAlbum.ID || got[0].SeriesID != 0 {
		t.Fatalf("album-only track works = %+v", got)
	}

	// Inverse of TracksForWork: every listed work lists the track.
	for _, trackID := range tracks {
		for _, work := range read(trackID) {
			members, err := store.TracksForWork(ctx, work.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, member := range members {
				found = found || member.ID == trackID
			}
			if !found {
				t.Fatalf("work %d lists no track %d", work.ID, trackID)
			}
		}
	}

	if rec := apiGet(t, app, "/api/v1/tracks/999999/works", testAppAPIToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown track: %d %s", rec.Code, rec.Body.String())
	}
}
