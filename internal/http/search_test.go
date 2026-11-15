package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestSearchAPIAndPage(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	importTestTrack(t, app)
	ctx := context.Background()
	artists, err := app.store.ListArtists(ctx, storage.Filters{Query: "Artist"})
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists=%+v %v", artists, err)
	}
	id := artists[0].ID
	if err := app.store.UpsertExternalArtistProfile(ctx, id, storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: "suisei", DisplayName: "Artist", Aliases: []string{"星街彗星"}}); err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	request := func(path string, authorized bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if authorized {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	apiPath := "/api/v1/search?q=" + url.QueryEscape("星街彗星")
	if rec := request(apiPath, false); rec.Code != 401 {
		t.Fatalf("unauthenticated API: %d", rec.Code)
	}
	if rec := request("/admin/search", false); rec.Code != 303 {
		t.Fatalf("unauthenticated page: %d", rec.Code)
	}
	rec := request(apiPath, true)
	if rec.Code != 200 {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}
	var result storage.SearchResults
	if err = json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Artists.Total != 1 || result.Albums.Total != 1 || result.Tracks.Total != 1 || result.BestMatch == nil || result.BestMatch.ID != id {
		t.Fatalf("result=%+v", result)
	}
	for _, path := range []string{apiPath + "&category=tracks&limit=1&offset=0", apiPath + "&category=composers", "/api/v1/search?q=absent", "/api/v1/search"} {
		rec := request(path, true)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"items":null`) {
			t.Fatalf("null items: %s", rec.Body.String())
		}
	}
	for _, path := range []string{apiPath + "&category=unknown", apiPath + "&offset=-1", apiPath + "&limit=oops", "/api/v1/search?q=" + strings.Repeat("a", 201), "/admin/search?page=-1"} {
		if rec := request(path, true); rec.Code != 400 {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
	body := adminGetBody(t, handler, cookie, "/admin/search?q="+url.QueryEscape("星街彗星"))
	for _, want := range []string{"最佳匹配", "search-artists-heading", "search-albums-heading", "search-tracks-heading", "播放 Track", fmt.Sprintf("/admin/artists/%d", id)} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if strings.Contains(body, "search-composers-heading") {
		t.Fatal("empty composers section rendered")
	}
	body = adminGetBody(t, handler, cookie, "/admin/search?q=absent")
	if !strings.Contains(body, "没有找到匹配结果") {
		t.Fatal("missing empty state")
	}
	body = adminGetBody(t, handler, cookie, "/admin/search")
	if !strings.Contains(body, "从一个名字开始") || strings.Contains(body, "没有找到匹配结果") {
		t.Fatal("empty query browsed the library")
	}
	body = adminGetBody(t, handler, cookie, "/admin/home")
	if !strings.Contains(body, `action="/admin/search"`) {
		t.Fatal("home search not updated")
	}
}

func TestSearchAliasManagementAuthAndPersistence(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	importTestTrack(t, app)
	ctx := context.Background()
	artists, _ := app.store.ListArtists(ctx, storage.Filters{})
	id := artists[0].ID
	handler := app.Handler()
	base := fmt.Sprintf("/api/v1/artists/%d/search-aliases", id)
	post := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", base, strings.NewReader(`{"value":"星街彗星"}`))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("X-CSRF-Token", token)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(""); rec.Code != 403 {
		t.Fatalf("missing CSRF: %d", rec.Code)
	}
	if rec := post(csrf); rec.Code != 204 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(csrf); rec.Code != 204 {
		t.Fatalf("idempotent add: %d", rec.Code)
	}
	aliases, err := app.store.ArtistSearchAliases(ctx, id)
	if err != nil || len(aliases) != 1 {
		t.Fatalf("aliases=%+v %v", aliases, err)
	}
	profile := adminGetBody(t, handler, cookie, fmt.Sprintf("/admin/artists/%d?view=profile", id))
	for _, want := range []string{"搜索别名", "星街彗星", "添加人工别名"} {
		if !strings.Contains(profile, want) {
			t.Fatalf("profile missing %s", want)
		}
	}
	adminPath := fmt.Sprintf("/admin/artists/%d/search-aliases", id)
	if rec := postAdminForm(t, handler, cookie, "wrong", adminPath, url.Values{"value": {"Other Alias"}}); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	rec := postAdminForm(t, handler, cookie, csrf, adminPath, url.Values{"value": {" "}})
	if rec.Code != 303 || !strings.Contains(notice303Of(t, rec), "200") {
		t.Fatalf("invalid alias status=%d", rec.Code)
	}
	deletePath := fmt.Sprintf("/admin/artists/%d/search-aliases/%d/delete", id, aliases[0].ID)
	rec = postAdminForm(t, handler, cookie, csrf, deletePath, nil)
	if rec.Code != 303 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	location, _ := url.Parse(rec.Header().Get("Location"))
	if location.Query().Get("view") != "profile" || location.Fragment != "search-aliases" {
		t.Fatalf("redirect=%s", location)
	}
	result, err := app.store.Search(ctx, storage.SearchOptions{Query: "星街彗星"})
	if err != nil || result.Artists.Total != 0 {
		t.Fatalf("deleted alias still searchable %+v %v", result, err)
	}
}
