package artistaliases

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeepSeekSearchFetchAndRedactedErrors(t *testing.T) {
	failAuth := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failAuth {
			w.WriteHeader(401)
			_, _ = w.Write([]byte("secret"))
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing bearer")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/v1/tools/search":
			if body["need_content"] != true {
				t.Error("search did not request full page content")
			}
			if body["count"] != float64(5) {
				t.Error(body)
			}
			_, _ = w.Write([]byte(`{"results":[{"url":"https://example.test/a","title":"artist","content":"actual page text","summary":"generated summary"}]}`))
		case "/v1/tools/fetch":
			_, _ = w.Write([]byte(`{"content":"name 中文名"}`))
		default:
			w.WriteHeader(401)
			_, _ = w.Write([]byte("secret"))
		}
	}))
	defer server.Close()
	c := &DeepSeekWeb{HTTP: server.Client(), Key: "secret", BaseURL: server.URL}
	results, e := c.Search(context.Background(), "Singer 中文名")
	if e != nil || len(results) != 1 || results[0].Content != "actual page text" {
		t.Fatal(results, e)
	}
	body, e := c.Fetch(context.Background(), results[0].URL)
	if e != nil || body != "name 中文名" {
		t.Fatal(body, e)
	}
	failAuth = true
	_, e = c.Search(context.Background(), "Singer 中文名")
	if e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
}
