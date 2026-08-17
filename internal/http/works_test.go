package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
