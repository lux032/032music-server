package httpapi

import (
	"bytes"
	"context"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestAlbumPageWorksAndAPI(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := context.Background()
	if e := app.store.EnsureLibrary(ctx, "Test", "/music"); e != nil {
		t.Fatal(e)
	}
	lib, _ := app.store.LibraryByRoot(ctx, "/music")
	for n, album := range []string{"First album", "Empty album"} {
		if e := app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: album + "/01.flac", FileSize: 1, ModifiedAtNS: int64(n + 1), Metadata: metadata.AudioMetadata{Title: "Track", Album: album, Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); e != nil {
			t.Fatal(e)
		}
	}
	albums, e := app.store.ListAlbums(ctx, storage.Filters{Limit: 20})
	if e != nil || len(albums) != 2 {
		t.Fatalf("albums %+v %v", albums, e)
	}
	var first, empty int64
	for _, album := range albums {
		if album.Title == "First album" {
			first = album.ID
		} else {
			empty = album.ID
		}
	}
	work, e := app.store.CreateWork(ctx, storage.WorkInput{Title: "作品测试", Type: "anime"})
	if e != nil {
		t.Fatal(e)
	}
	if e = app.store.AddWorkAlbum(ctx, work.ID, first, "ost"); e != nil {
		t.Fatal(e)
	}
	handler := app.Handler()
	for _, tc := range []struct {
		id   int64
		need string
	}{{first, "作品测试"}, {empty, "暂无关联作品"}} {
		req := httptest.NewRequest(http.MethodGet, "/admin/albums/"+strconv.FormatInt(tc.id, 10), nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.need) {
			t.Fatalf("page=%d %s", rec.Code, rec.Body.String())
		}
		if tc.id == first && !strings.Contains(rec.Body.String(), "/admin/works/"+strconv.FormatInt(work.ID, 10)) {
			t.Fatal("work link missing")
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/albums/"+strconv.FormatInt(first, 10)+"/works", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"title":"作品测试"`)) {
		t.Fatalf("api=%d %s", rec.Code, rec.Body.String())
	}
}
