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
		{"POST", "/admin/discover/mapping/1/bad/confirm"}, {"POST", "/admin/discover/mapping/1/bad/reject"}, {"DELETE", "/admin/discover/mapping/1/bad/reject"},
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
	for _, query := range []string{"source=nope", "tab=nope"} {
		check(request("GET", "/admin/discover/items.json?"+query, "", "", true), 400)
	}
	for _, subject := range []string{"0", "-1", "abc", "9223372036854775808"} {
		check(request("POST", "/admin/discover/mapping/"+subject+"/11111111-1111-4111-8111-111111111111/confirm", "", csrf, true), 400)
	}
	check(request("POST", "/admin/discover/mapping/1/bad/confirm", "", csrf, true), 400)

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
	exec(`INSERT INTO discovery_candidates(rgid,seed_kind,seed_artist_id,generation,score,lane,rank,reasons_json) VALUES(?,'artist',?,0,10,'exploit',1,'[{"code":"favorite_artist","artist":"Singer"}]')`, rg, artistID)
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
	exec(`INSERT INTO discovery_possible_matches(rgid,album_id,reason) VALUES(?,?,'title_artist')`, rg, albumID)
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

func TestDiscoveryWorkReasonAndMappingError(t *testing.T) {
	for code, want := range map[string]string{"work_plays": "来自你常听的作品《作品》", "work_series": "同系列作品加成", "work_relation": "《作品》的片头曲", "explore_work": "探索：来自作品《作品》", "mapping_auto": "Bangumi 自动对应"} {
		if got := discoveryReason(storage.DiscoveryReason{Code: code, Work: "作品", Relation: "片头曲"}); got != want {
			t.Fatalf("%s: %q", code, got)
		}
	}
	app, _, _ := csrfSessionApp(t)
	w := httptest.NewRecorder()
	app.discoveryError(w, storage.ErrMappingSubjectConfirmed)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "subject_confirmed") {
		t.Fatal(w.Code, w.Body.String())
	}

}

func TestDiscoveryMappingHTTP(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	db, err := sql.Open("sqlite", httpTestDBPath[app.store])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE release_group_rebuild SET status='ready'`)
	exec(`INSERT INTO works(id,title,normalized_title,type) VALUES(900001,'测试作品','测试作品','anime')`)
	exec(`INSERT INTO discovery_bangumi_music(subject_id,name,name_cn,fetched_at) VALUES(900010,'音乐条目','音乐中文名','2026-01-01')`)
	exec(`INSERT INTO discovery_work_music(work_id,subject_id,relation,ordinal) VALUES(900001,900010,'片头曲',0)`)
	ids := []string{"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}
	for _, id := range ids {
		exec(`INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,fetched_at) VALUES(?,'测试选项','Album','Album','2026-01-01')`, strings.ToLower(id))
		exec(`INSERT INTO discovery_bangumi_mappings(subject_id,rgid,status,score,evidence_json) VALUES(900010,?,'suggested',70,'["title_normalized","multi_title"]')`, strings.ToLower(id))
	}
	app.SetDiscovery(discovery.New(app.store, app.logger, discovery.Options{DataDir: t.TempDir()}))
	handler := app.Handler()
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/admin/discover/items.json?tab=mapping&source=work")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{"raw_json", "artists_json", "decided_at", "api_key", "evidence_json"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("DTO leaked", secret)
		}
	}
	var list struct {
		Items []discoveryItemDTO `json:"items"`
		HTML  string             `json:"html"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Source != "work" || list.Items[0].Work != "测试作品" || list.Items[0].Mapping.SubjectNameCN != "音乐中文名" || len(list.Items[0].Mapping.Options) != 2 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(list.HTML, `data-action="acquire"`) || !strings.Contains(list.HTML, `rel="noopener noreferrer"`) || !strings.Contains(list.HTML, "标题规范化后一致") {
		t.Fatal(list.HTML)
	}
	if strings.Contains(list.HTML, " · 0 · ") {
		t.Fatal("zero year rendered", list.HTML)
	}
	for _, step := range []struct {
		method, path string
		code         int
		contains     string
	}{
		{"DELETE", "/admin/discover/mapping/900010/" + ids[0] + "/reject", 404, "对应关系不存在或状态已变化"},
		{"POST", "/admin/discover/mapping/900010/" + ids[0] + "/reject", 200, `"reranked":false`},
		{"GET", "/admin/discover/items.json?tab=ignored", 200, "已拒绝的对应"},
		{"DELETE", "/admin/discover/mapping/900010/" + ids[0] + "/reject", 200, `"ok":true`},
		{"POST", "/admin/discover/mapping/900010/" + ids[0] + "/confirm", 200, `"ok":true`},
		{"POST", "/admin/discover/mapping/900010/" + ids[1] + "/confirm", 409, "subject_confirmed"},
	} {
		w := request(step.method, step.path)
		if w.Code != step.code || !strings.Contains(w.Body.String(), step.contains) {
			t.Fatalf("%s %s: %d %s", step.method, step.path, w.Code, w.Body.String())
		}
	}
	// An API bearer token is not an administrator session.
	for _, step := range []struct{ method, action string }{{"POST", "confirm"}, {"POST", "reject"}, {"DELETE", "reject"}} {
		r := httptest.NewRequest(step.method, "/admin/discover/mapping/900010/"+ids[0]+"/"+step.action, nil)
		r.Header.Set("Authorization", "Bearer "+app.config.APIToken)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestDiscoveryMappingGroupPaginationAndGlobalCounts(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
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
	exec(`UPDATE release_group_rebuild SET status='ready'`)
	exec(`INSERT INTO works(id,title,normalized_title,type) VALUES(900001,'作品','作品','anime')`)
	for subject := 1; subject <= 61; subject++ {
		exec(`INSERT INTO discovery_bangumi_music(subject_id,name,fetched_at) VALUES(?,'条目','2026-01-01')`, subject)
		exec(`INSERT INTO discovery_work_music(work_id,subject_id,relation,ordinal) VALUES(900001,?,'片头曲',?)`, subject, subject)
		for option := 1; option <= 3; option++ {
			rg := fmt.Sprintf("77777777-7777-4777-8777-%012d", subject*10+option)
			exec(`INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,fetched_at) VALUES(?,'选项','Album','Album','2026-01-01')`, rg)
			exec(`INSERT INTO discovery_bangumi_mappings(subject_id,rgid,status,score) VALUES(?,?,'suggested',?)`, subject, rg, option)
		}
	}
	albumID, _ := seedHomeLibrary(t, app, app.store)
	var artistID int64
	if e := db.QueryRow(`SELECT id FROM artists LIMIT 1`).Scan(&artistID); e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 2; i++ {
		rg := fmt.Sprintf("88888888-8888-4888-8888-%012d", i)
		exec(`INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,fetched_at) VALUES(?,'艺人发行','Album','Album','2026-01-01')`, rg)
		exec(`INSERT INTO discovery_candidates(rgid,seed_kind,seed_artist_id,generation,score,lane,rank,reasons_json) VALUES(?,'artist',?,0,10,'exploit',?,'[]')`, rg, artistID, i)
		if i == 1 {
			exec(`INSERT INTO discovery_possible_matches(rgid,album_id,reason) VALUES(?,?,'title_artist')`, rg, albumID)
		} else {
			exec(`INSERT INTO release_group_decisions(rgid,decision) VALUES(?,'ignore')`, rg)
		}
	}
	app.SetDiscovery(discovery.New(app.store, app.logger, discovery.Options{DataDir: t.TempDir()}))
	handler := app.Handler()
	fetch := func(query string) ([]discoveryItemDTO, discoveryCountsDTO) {
		t.Helper()
		r := httptest.NewRequest("GET", "/admin/discover/items.json?"+query, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var v struct {
			Items  []discoveryItemDTO
			Counts discoveryCountsDTO
		}
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v.Items, v.Counts
	}
	first, c := fetch("tab=mapping&limit=60")
	second, _ := fetch("tab=mapping&limit=60&offset=60")
	if len(first) != 60 || len(second) != 1 || c.Mapping != 61 || c.Possible != 1 || c.Ignored != 1 {
		t.Fatal(len(first), len(second), c)
	}
	seen := map[int64]bool{}
	for _, item := range append(first, second...) {
		if seen[item.Mapping.SubjectID] || len(item.Mapping.Options) != 3 {
			t.Fatal(item.Mapping)
		}
		seen[item.Mapping.SubjectID] = true
	}
	for _, source := range []string{"all", "artist", "work"} {
		items, counts := fetch("tab=mapping&source=" + source)
		if counts != c {
			t.Fatal(source, counts, c)
		}
		if source == "artist" && len(items) != 0 {
			t.Fatal(items)
		}
	}
	exec(`UPDATE discovery_bangumi_mappings SET status='rejected'`)
	first, c = fetch("tab=ignored&limit=60")
	second, _ = fetch("tab=ignored&limit=60&offset=60")
	if len(first) != 60 || len(second) != 2 || c.Ignored != 62 || len(first[1].Mapping.Options) != 3 {
		t.Fatal(len(first), len(second), c)
	}
	for _, source := range []string{"artist", "work"} {
		_, counts := fetch("tab=ignored&source=" + source)
		if counts != c {
			t.Fatal(source, counts, c)
		}
	}
	// A missing metadata row during a refresh is skipped instead of failing the list.
	exec(`PRAGMA foreign_keys=OFF; DELETE FROM discovery_bangumi_music WHERE subject_id=1`)
	items, remaining := fetch("tab=ignored&limit=60")
	if remaining.Ignored != 61 {
		t.Fatal("missing metadata count", remaining)
	}
	for _, item := range items {
		if item.Mapping != nil && item.Mapping.SubjectID == 1 {
			t.Fatal("missing metadata was not skipped")
		}
	}
}
