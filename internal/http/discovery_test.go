package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/discovery"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestDiscoveryHandlers(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	db, e := sql.Open("sqlite", httpTestDBPath[app.store])
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(q, args...); e != nil {
			t.Fatal(e)
		}
	}
	dir := t.TempDir()
	app.SetDiscovery(discovery.New(app.store, app.logger, discovery.Options{DataDir: dir}))
	handler := app.Handler()
	request := func(method, path, body, token string, login bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if login {
			r.AddCookie(cookie)
		}
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("status %d want %d: %s", w.Code, code, w.Body.String())
		}
	}
	check(request("GET", "/admin/discover", "", "", false), 303)
	check(request("GET", "/admin/discover/items.json", "", "", false), 401)
	for _, endpoint := range []struct{ method, path string }{
		{"POST", "/admin/discover/refresh"}, {"POST", "/admin/discover/refresh/cancel"},
		{"POST", "/admin/discover/rg/bad/ignore"}, {"DELETE", "/admin/discover/rg/bad/ignore"},
		{"POST", "/admin/discover/rg/bad/owned"}, {"POST", "/admin/discover/rg/bad/not-same"},
	} {
		check(request(endpoint.method, endpoint.path, "", "", false), 401)
		for _, token := range []string{"", "wrong"} {
			check(request(endpoint.method, endpoint.path, "", token, true), 403)
		}
	}
	w := request("GET", "/admin/discover", "", "", true)
	check(w, 200)
	if !strings.Contains(w.Body.String(), "身份重建状态") || strings.Contains(w.Body.String(), "暂无推荐") {
		t.Fatal("missing identity block", w.Body.String())
	}
	for _, path := range []string{"items.json", "status.json", "refresh"} {
		method := "GET"
		if path == "refresh" {
			method = "POST"
		}
		w := request(method, "/admin/discover/"+path, "", csrf, true)
		check(w, 409)
		if !strings.Contains(w.Body.String(), "identity_not_ready") {
			t.Fatal(w.Body.String())
		}
	}
	exec(`UPDATE release_group_rebuild SET status='ready'`)
	check(request("POST", "/admin/discover/rg/bad/ignore", "", csrf, true), 400)
	const rg = "11111111-1111-4111-8111-111111111111"
	const old = "22222222-2222-4222-8222-222222222222"
	path := "/admin/discover/rg/" + rg
	check(request("POST", path+"/ignore", "", csrf, true), 404)
	albumID, _ := seedHomeLibrary(t, app, app.store)
	var artistID int64
	if e := db.QueryRow(`SELECT id FROM artists LIMIT 1`).Scan(&artistID); e != nil {
		t.Fatal(e)
	}
	exec(`INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,artist_credit,fetched_at) VALUES(?,'发现专辑','Album','Album','Singer','2026-01-01')`, rg)
	exec(`INSERT INTO discovery_candidates VALUES(?,?,0,10,'exploit',1,'[{"code":"favorite_artist","artist":"Singer"}]')`, rg, artistID)
	w = request("GET", "/admin/discover", "", "", true)
	check(w, 200)
	for _, text := range []string{"发现专辑", "你收藏了 Singer", "data-nav=\"discover\""} {
		if !strings.Contains(w.Body.String(), text) {
			t.Fatal("missing", text, w.Body.String())
		}
	}
	for _, text := range []string{"onclick=", "style=", "<script>"} {
		if strings.Contains(w.Body.String(), text) {
			t.Fatal("inline content", text)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("missing CSP")
	}
	check(request("POST", path+"/ignore", "", csrf, true), 200)
	w = request("GET", "/admin/discover/items.json?tab=ignored", "", "", true)
	check(w, 200)
	if !strings.Contains(w.Body.String(), `"ignored":1`) {
		t.Fatal(w.Body.String())
	}
	check(request("DELETE", path+"/ignore", "", csrf, true), 200)
	exec(`INSERT INTO discovery_possible_matches VALUES(?,?,'title_artist')`, rg, albumID)
	check(request("POST", path+"/not-same", fmt.Sprintf(`{"albumId":%d}`, albumID), csrf, true), 200)
	var n int
	if e := db.QueryRow(`SELECT count(*) FROM discovery_match_rejections`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	check(request("POST", path+"/owned", `{"albumId":999999}`, csrf, true), 404)
	if e := app.store.AssociateAlbumReleaseGroup(context.Background(), albumID, old); e != nil {
		t.Fatal(e)
	}
	body := fmt.Sprintf(`{"albumId":%d}`, albumID)
	w = request("POST", path+"/owned", body, csrf, true)
	check(w, 409)
	if !strings.Contains(w.Body.String(), "replaces_rg") || !strings.Contains(w.Body.String(), old) {
		t.Fatal(w.Body.String())
	}
	check(request("POST", path+"/owned", fmt.Sprintf(`{"albumId":%d,"confirmReplace":true}`, albumID), csrf, true), 200)
	check(request("GET", "/admin/discover/covers/"+rg, "", "", true), 404)
	if e := os.MkdirAll(filepath.Join(dir, "discovery-covers"), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "discovery-covers", rg+".jpg"), []byte("cached only"), 0644); e != nil {
		t.Fatal(e)
	}
	w = request("GET", "/admin/discover/covers/"+rg, "", "", true)
	check(w, 200)
	if w.Header().Get("Cache-Control") != "private, max-age=86400" || w.Body.String() != "cached only" {
		t.Fatal(w.Header(), w.Body.String())
	}
	check(request("GET", "/admin/discover/covers/bad", "", "", true), 400)
	exec(`UPDATE discovery_release_groups SET title=? WHERE rgid=?`, `<img src=x onerror=alert(1)>`, rg)
	// Remove ownership so the malicious title is included in the returned card HTML.
	exec(`DELETE FROM album_release_groups WHERE album_id=?`, albumID)
	w = request("GET", "/admin/discover/items.json", "", "", true)
	check(w, 200)
	var payload struct {
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload.HTML, "<img src=x") || !strings.Contains(payload.HTML, "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatal("unescaped title", payload.HTML)
	}
	app.SetDiscovery(nil)
	w = request("GET", "/admin/discover", "", "", true)
	check(w, 200)
	if strings.Contains(w.Body.String(), "身份重建状态") || strings.Contains(w.Body.String(), "前往控制台重建") {
		t.Fatal("nil service suggests rebuilding identity", w.Body.String())
	}
	check(request("GET", "/admin/discover/items.json", "", "", true), 503)
}

func TestDiscoveryRefreshHTTPWorkerLifetime(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	seedHomeLibrary(t, app, app.store)
	db, _ := sql.Open("sqlite", httpTestDBPath[app.store])
	defer db.Close()
	if _, e := db.Exec(`UPDATE release_group_rebuild SET status='ready'; INSERT INTO artist_external_profiles(artist_id,source,external_id,display_name,raw_json,fetched_at) SELECT id,'musicbrainz','33333333-3333-4333-8333-333333333333','Singer','{}','2026-01-01' FROM artists LIMIT 1`); e != nil {
		t.Fatal(e)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		fmt.Fprint(w, `{"release-groups":[],"release-group-count":0}`)
	}))
	defer fake.Close()
	service := discovery.New(app.store, app.logger, discovery.Options{MBBaseURL: fake.URL, DataDir: t.TempDir()})
	app.SetDiscovery(service)
	h := app.Handler()
	post := func(ctx context.Context, path string) int {
		r := httptest.NewRequest("POST", path, nil).WithContext(ctx)
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	ctx, cancel := context.WithCancel(context.Background())
	if code := post(ctx, "/admin/discover/refresh"); code != 202 {
		t.Fatal(code)
	}
	cancel()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("worker cancelled with request")
	}
	if code := post(context.Background(), "/admin/discover/refresh"); code != 409 {
		t.Fatal(code)
	}
	if code := post(context.Background(), "/admin/discover/refresh/cancel"); code != 200 {
		t.Fatal(code)
	}
	close(release)
	service.Wait()
	status, e := service.Status(context.Background())
	if e != nil || status.Status != "cancelled" {
		t.Fatal(status, e)
	}
}

func TestDiscoveryAlbumOptions(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := context.Background()
	firstID, _ := seedHomeLibrary(t, app, app.store)
	library, err := app.store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	err = app.store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Singer/Z Unknown/01.flac", FileSize: 1024, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "Z Unknown", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Year: 2025}})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", httpTestDBPath[app.store])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE release_group_rebuild SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	if err = app.store.AssociateAlbumReleaseGroup(ctx, firstID, "22222222-2222-4222-8222-222222222222"); err != nil {
		t.Fatal(err)
	}
	handler := app.Handler()
	get := func(path string) []map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var items []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
			t.Fatal(err)
		}
		return items
	}
	defaults := get("/admin/options/albums?q=")
	if len(defaults) != 2 || defaults[0]["id"] != float64(firstID) {
		t.Fatal(defaults)
	}
	for _, item := range defaults {
		if len(item) != 2 || item["id"] == nil || item["label"] == nil {
			t.Fatal("default options contract changed", item)
		}
	}
	items := get("/admin/options/albums?q=&discovery=1")
	if len(items) != 2 || items[0]["label"] != "Z Unknown" || items[0]["currentRg"] != "" || items[1]["id"] != float64(firstID) {
		t.Fatal("unknown RG must sort first", items)
	}
	if items[0]["year"] != float64(2025) || items[0]["trackCount"] != float64(1) || items[1]["trackCount"] != float64(3) {
		t.Fatal("missing comparison metadata", items)
	}
}
