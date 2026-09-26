package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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
