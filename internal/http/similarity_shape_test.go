package httpapi

import (
	"context"
	"encoding/json"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"net/http/httptest"
	"testing"
)

func TestSimilarityHTTPResponseShapes(t *testing.T) {
	app, store, token := setupTestApp(t)
	ctx := context.Background()
	if e := store.EnsureLibrary(ctx, "sim", "/sim"); e != nil {
		t.Fatal(e)
	}
	lib, e := store.LibraryByRoot(ctx, "/sim")
	if e != nil {
		t.Fatal(e)
	}
	var ids []int64
	for i := 1; i <= 2; i++ {
		input := storage.ImportInput{LibraryID: lib.ID, RelativePath: jsonNumber(int64(i)) + ".flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: jsonNumber(int64(i)), Album: "sim", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i}}
		if e = store.ImportTrack(ctx, input); e != nil {
			t.Fatal(e)
		}
		tracks, e := store.ListTracks(ctx, storage.Filters{Limit: 10})
		if e != nil {
			t.Fatal(e)
		}
		ids = nil
		for _, track := range tracks {
			ids = append(ids, track.ID)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("tracks=%v", ids)
	}
	get := func(path string) map[string]json.RawMessage {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var body map[string]json.RawMessage
		if e := json.Unmarshal(rec.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		return body
	}
	base := "/api/v1/tracks/" + jsonNumber(ids[0])
	similar := get(base + "/similar")
	var entries []map[string]json.RawMessage
	if e := json.Unmarshal(similar["items"], &entries); e != nil || len(entries) != 1 {
		t.Fatalf("similar=%s err=%v", similar["items"], e)
	}
	for _, key := range []string{"track", "score", "distance", "reasons"} {
		if len(entries[0][key]) == 0 {
			t.Errorf("missing %s", key)
		}
	}
	from, to := jsonNumber(ids[0]), jsonNumber(ids[1])
	for _, tc := range []struct {
		path  string
		count int
	}{{"/api/v1/tracks/path?from=" + from + "&to=" + to, 2}, {"/api/v1/tracks/path?from=" + from + "&to=" + from, 1}} {
		body := get(tc.path)
		var tracks []struct {
			ID int64 `json:"id"`
		}
		if e := json.Unmarshal(body["items"], &tracks); e != nil || len(tracks) != tc.count || tracks[0].ID != ids[0] {
			t.Fatalf("path=%s err=%v", body["items"], e)
		}
		if tracks[len(tracks)-1].ID != ids[tc.count-1] || string(body["complete"]) != "true" {
			t.Fatalf("path=%s complete=%s", body["items"], body["complete"])
		}
	}
}
