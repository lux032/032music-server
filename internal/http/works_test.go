package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestWorksAPI(t *testing.T) {
	app, store, token := setupTestApp(t)
	handler := app.Handler()
	body := []byte(`{"title":"葬送のフリーレン","readingTitle":"sousou no frieren","translatedTitle":"Frieren","type":"anime","year":2023}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/works", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil || created.ID == 0 {
		t.Fatalf("created=%#v err=%v", created, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/works?q=sousou&index=S", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("葬送のフリーレン")) {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/works/"+jsonNumber(created.ID)+"/albums", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("[]")) {
		t.Fatalf("albums status=%d body=%s", rec.Code, rec.Body.String())
	}

	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Works", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	albums, err := store.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums=%+v %v", albums, err)
	}
	request := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	endpoint := "/api/v1/works/" + jsonNumber(created.ID) + "/albums"
	if res := request(http.MethodPost, endpoint, []byte(`{"albumId":`+jsonNumber(albums[0].ID)+`,"role":"ost"}`)); res.Code != http.StatusNoContent {
		t.Fatalf("add status=%d body=%s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/works/"+jsonNumber(created.ID)+"/albums", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"albumId"`)) {
		t.Fatalf("linked albums status=%d body=%s", rec.Code, rec.Body.String())
	}
	if res := request(http.MethodPost, endpoint, []byte(`{"albumId":999999,"role":""}`)); res.Code != http.StatusNotFound {
		t.Fatalf("missing album status=%d body=%s", res.Code, res.Body.String())
	}
	if res := request(http.MethodPost, "/api/v1/works/999999/albums", []byte(`{"albumId":`+jsonNumber(albums[0].ID)+`}`)); res.Code != http.StatusNotFound {
		t.Fatalf("missing work status=%d body=%s", res.Code, res.Body.String())
	}
	if res := request(http.MethodDelete, endpoint+"/"+jsonNumber(albums[0].ID), nil); res.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", res.Code, res.Body.String())
	}
	if err := store.DeleteWork(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/works/"+jsonNumber(created.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func jsonNumber(value int64) string {
	data, _ := json.Marshal(value)
	return string(data)
}

// TestWorksPageKanaIndex: the works index bar splits letters like the library
// bar — "#" stays visible next to A-Z, only kana sit behind the かな toggle,
// and a kana index opens the toggle on load.
func TestWorksPageKanaIndex(t *testing.T) {
	_, _, handler := credentialTestApp(t)
	cookie := mustLogin(t, handler, "admin", testAdminPassword)
	body := getWithCookie(handler, "/admin/works", cookie).Body.String()
	kanaStart := strings.Index(body, `<span class="kana-links"`)
	if kanaStart < 0 {
		t.Fatal("works page must render the kana links")
	}
	kanaEnd := kanaStart + strings.Index(body[kanaStart:], `</span>`)
	kana := body[kanaStart:kanaEnd]
	if !strings.Contains(body, `<button type="button" class="kana-toggle" aria-expanded="false">`) || !strings.Contains(kana, ` hidden>`) {
		t.Fatal("kana links must start collapsed without a kana index")
	}
	if strings.Contains(kana, "index=%23") || !strings.Contains(body[:kanaStart], `href="/admin/works?index=%23"`) {
		t.Fatal(`"#" must render outside the kana group`)
	}
	if !strings.Contains(kana, ">あ</a>") || !strings.Contains(kana, ">わ</a>") {
		t.Fatal("kana group must list the kana rows")
	}

	body = getWithCookie(handler, "/admin/works?index="+url.QueryEscape("か"), cookie).Body.String()
	if !strings.Contains(body, `<button type="button" class="kana-toggle" aria-expanded="true">`) || strings.Contains(body, `<span class="kana-links" hidden>`) {
		t.Fatal("a kana index must expand the kana links")
	}
}
