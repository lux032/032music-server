package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/acquisition"
	"github.com/lux032/032music-server/internal/acquisition/catalog"
	"github.com/lux032/032music-server/internal/acquisition/jackett"
	"github.com/lux032/032music-server/internal/acquisition/qbit"
	"github.com/lux032/032music-server/internal/acquisition/sanitize"
	"github.com/lux032/032music-server/internal/acquisition/torrent"
	"github.com/lux032/032music-server/internal/discovery"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
)

func TestAcquisitionRoutesSettingsAndDiscovery(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	ctx := context.Background()
	app.SetAcquisition(acquisition.New(app.store, app.logger, acquisition.Options{}))
	app.SetDiscovery(discovery.New(app.store, app.logger, discovery.Options{DataDir: t.TempDir()}))
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
	handler := app.Handler()
	request := func(method, path, body, token string, login, form bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if login {
			r.AddCookie(cookie)
		}
		if form {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			r.Header.Set("X-CSRF-Token", token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("%d expected %d: %s", w.Code, status, w.Body.String())
		}
	}
	const rg = "11111111-1111-4111-8111-111111111111"
	routes := []string{"/admin/discover/rg/" + rg + "/search", "/admin/discover/rg/" + rg + "/acquire", "/admin/acquisitions/x/cancel", "/admin/acquisitions/x/retry", "/admin/acquisitions/x/link", "/admin/settings/acquisition/test-jackett", "/admin/settings/acquisition/test-qb"}
	for _, path := range routes {
		check(request("POST", path, "{}", "", false, false), 401)
		check(request("POST", path, "{}", "", true, false), 403)
		check(request("POST", path, "{}", "wrong", true, false), 403)
	}
	check(request("GET", "/admin/discover/acquisitions.json", "", "", false, false), 401)
	check(request("GET", "/admin/settings/acquisition", "", "", false, false), 303)
	check(request("POST", "/admin/settings/acquisition", "", "", false, true), 303)
	check(request("POST", "/admin/settings/acquisition", "", "", true, true), 403)
	save := func(values url.Values) *httptest.ResponseRecorder {
		values.Set("csrfToken", csrf)
		return request("POST", "/admin/settings/acquisition", values.Encode(), "", true, true)
	}
	values := url.Values{"jackett_url": {"http://localhost:9117"}, "qb_url": {"http://localhost:8080"}, "jackett_api_key": {"KEY-PRIVATE"}, "qb_password": {"PASSWORD-PRIVATE"}, "qb_username": {"user"}}
	check(save(values), 303)
	delete(values, "jackett_api_key")
	delete(values, "qb_password")
	check(save(values), 303)
	creds, err := app.store.AcquisitionCredentials(ctx)
	if err != nil || creds.JackettAPIKey != "KEY-PRIVATE" || creds.QBPassword != "PASSWORD-PRIVATE" {
		t.Fatal("empty values did not retain secrets", err)
	}
	page := request("GET", "/admin/settings/acquisition", "", "", true, false)
	check(page, 200)
	for _, secret := range []string{"KEY-PRIVATE", "PASSWORD-PRIVATE", "<script>", "onclick="} {
		if strings.Contains(page.Body.String(), secret) {
			t.Fatal("settings disclosure", secret)
		}
	}
	if !strings.Contains(page.Body.String(), "acquisition-settings.js") || !strings.Contains(page.Body.String(), "test-qb") {
		t.Fatal("missing external script/test controls")
	}
	values.Set("clear_key", "on")
	values.Set("clear_password", "on")
	check(save(values), 303)
	creds, err = app.store.AcquisitionCredentials(ctx)
	if err != nil || creds.JackettAPIKey != "" || creds.QBPassword != "" {
		t.Fatal("clear failed", err)
	}
	bad := url.Values{"jackett_url": {"http://user:secret@localhost/?apikey=secret"}}
	check(save(bad), 303)
	settings, _ := app.store.AcquisitionSettings(ctx)
	if settings.JackettURL != "http://localhost:9117" {
		t.Fatal("invalid URL saved")
	}
	w := request("POST", routes[0], `{"q":"album"}`, csrf, true, false)
	check(w, 409)
	if !strings.Contains(w.Body.String(), "disabled") {
		t.Fatal(w.Body.String())
	}
	album, _ := seedHomeLibrary(t, app, app.store)
	var artist int64
	if err := db.QueryRow(`SELECT id FROM artists LIMIT 1`).Scan(&artist); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,artist_credit,fetched_at) VALUES(?,'album','Album','Album','artist','now')`, rg)
	exec(`INSERT INTO discovery_candidates VALUES(?,?,0,10,'exploit',1,'[]')`, rg, artist)
	task, err := app.store.CreateAcquisitionTask(ctx, storage.AcquisitionTask{RGID: rg, Title: "task snapshot", Artist: "artist", IdempotencyKey: "one", DownloadRef: "/dl/jpopsuki/?path=DOWNLOAD-PRIVATE"})
	if err != nil {
		t.Fatal(err)
	}
	own, err := app.store.DiscoveryOwnership(ctx, []string{rg})
	if err != nil || !own[rg].Acquiring {
		t.Fatal("missing acquiring ownership", err)
	}
	w = request("GET", "/admin/discover/items.json", "", "", true, false)
	check(w, 200)
	if !strings.Contains(w.Body.String(), `"recommended":0`) || !strings.Contains(w.Body.String(), `"acquiring":1`) {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/admin/discover/acquisitions.json", "/admin/discover/items.json?tab=acquiring"} {
		w = request("GET", path, "", "", true, false)
		check(w, 200)
		if !strings.Contains(w.Body.String(), task.PublicID) || !strings.Contains(w.Body.String(), `"watchDisabled":true`) {
			t.Fatal(w.Body.String())
		}
		for _, secret := range []string{"DOWNLOAD-PRIVATE", "download_ref", "downloadRef", "KEY-PRIVATE", "PASSWORD-PRIVATE"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("task disclosure", secret)
			}
		}
	}
	check(request("POST", "/admin/discover/rg/"+rg+"/ignore", "", csrf, true, false), 409)
	if err := app.store.DiscoveryOwned(ctx, rg, album, false); !errors.Is(err, storage.ErrAcquisitionRGActive) {
		t.Fatalf("owned must detect active task inside transaction: %v", err)
	}
	exec(`INSERT INTO release_groups(rgid) VALUES('22222222-2222-4222-8222-222222222222')`)
	if err := app.store.AssociateAlbumReleaseGroup(ctx, album, "22222222-2222-4222-8222-222222222222"); err != nil {
		t.Fatal(err)
	}
	body := `{"albumId":` + strconvInt(album) + `}`
	w = request("POST", "/admin/discover/rg/"+rg+"/owned", body, csrf, true, false)
	check(w, 409)
	if !strings.Contains(w.Body.String(), "replaces_rg") {
		t.Fatal(w.Body.String())
	}
	body = `{"albumId":` + strconvInt(album) + `,"confirmReplace":true}`
	check(request("POST", "/admin/discover/rg/"+rg+"/owned", body, csrf, true, false), 200)
	task, err = app.store.AcquisitionTask(ctx, task.ID)
	if err != nil || task.State != "IN_LIBRARY" {
		t.Fatal(task.State, err)
	}
	evidence, err := app.store.AcquisitionEvidence(ctx, task.ID)
	if err != nil || evidence.Source != "owned" {
		t.Fatal(evidence.Source, err)
	}
	w = request("GET", "/admin/discover/acquisitions.json", "", "", true, false)
	check(w, 200)
	if !strings.Contains(w.Body.String(), `"albumId":`+strconvInt(album)) || !strings.Contains(w.Body.String(), `"albumTitle":"`) || !strings.Contains(w.Body.String(), `"verifiedAt":"20`) {
		t.Fatal(w.Body.String())
	}
	exec(`UPDATE acquisition_tasks SET finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-8 days')`)
	w = request("GET", "/admin/discover/acquisitions.json", "", "", true, false)
	check(w, 200)
	if strings.Contains(w.Body.String(), task.PublicID) {
		t.Fatal("old IN_LIBRARY visible")
	}
}

func TestAcquisitionErrorMappingNeverDiscloses(t *testing.T) {
	app, _, _ := csrfSessionApp(t)
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&logs, nil))
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{storage.ErrIdentityNotReady, 409, "identity_not_ready"}, {storage.ErrAcquisitionRGActive, 409, "acquisition_active"}, {storage.ErrAcquisitionHashBound, 409, "hash_bound"}, {storage.ErrAcquisitionStale, 409, "stale"},
		{&storage.ErrReplacesReleaseGroup{RGID: "old"}, 409, "replaces_rg"}, {qbit.ErrAuth, 502, "qb_auth"}, {sanitize.Error("qBittorrent request", &url.Error{Op: "Get", URL: "http://private/?apikey=KEY-PRIVATE", Err: errors.New("connection refused")}), 500, "acquisition_failed"}, {sql.ErrNoRows, 404, "not_found"},
		{&sourcegate.CooldownError{Source: "jackett", RetryAfter: time.Minute}, 429, "jackett_cooldown"},
		{&jackett.APIError{Code: 500}, 429, "jackett_cooldown"}, {&jackett.APIError{Code: 501}, 429, "jackett_cooldown"}, {&jackett.APIError{Code: 100}, 502, "jackett_auth"}, {&jackett.APIError{Code: 900}, 502, "jackett_failed"},
		{&jackett.HTTPError{Status: 429}, 429, "jackett_cooldown"}, {&jackett.HTTPError{Status: 401}, 502, "jackett_auth"},
		{errors.New("apikey=KEY-PRIVATE password=PASSWORD-PRIVATE download_ref=/dl/private"), 500, "acquisition_failed"},
	}
	for _, code := range []string{"disabled", "already_owned", "ignored", "not_retryable", "idempotency_conflict", "invalid_result", "invalid_settings", "category_missing"} {
		status := 409
		if strings.HasPrefix(code, "invalid_") || code == "category_missing" {
			status = 400
		}
		cases = append(cases, struct {
			err    error
			status int
			code   string
		}{&acquisition.Error{Code: code}, status, code})
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		app.acquisitionError(w, tc.err)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
			t.Fatalf("%s: %d %s", tc.code, w.Code, w.Body.String())
		}
		for _, secret := range []string{"KEY-PRIVATE", "PASSWORD-PRIVATE", "download_ref"} {
			if strings.Contains(w.Body.String()+logs.String(), secret) {
				t.Fatal("disclosed", secret)
			}
		}
	}
}

func strconvInt(v int64) string { return fmt.Sprint(v) }

func TestAcquisitionConfirmHashBoundAndConnectionTests(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	ctx := context.Background()
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&logs, nil))
	const rg = "11111111-1111-4111-8111-111111111111"
	const other = "22222222-2222-4222-8222-222222222222"
	data := []byte("d4:infod4:name1:x6:pieces20:aaaaaaaaaaaaaaaaaaaaee")
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "auth/login"):
			io.WriteString(w, "Ok.")
		case strings.HasSuffix(r.URL.Path, "app/version"):
			io.WriteString(w, "v5.0")
		case strings.HasSuffix(r.URL.Path, "torrents/categories"):
			io.WriteString(w, `{"music":{"name":"music"}}`)
		case strings.Contains(r.URL.Path, "torznab"):
			io.WriteString(w, `<rss><channel><item><title>FLAC</title><link>/dl/jpopsuki/?path=DOWNLOAD-PRIVATE&amp;jackett_apikey=KEY-PRIVATE</link></item></channel></rss>`)
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fake.Close()
	db, err := sql.Open("sqlite", httpTestDBPath[app.store])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE release_group_rebuild SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{rg, other} {
		if _, err = db.Exec(`INSERT INTO discovery_release_groups(rgid,title,primary_type,kind,artist_credit,fetched_at) VALUES(?,'album','Album','Album','artist','now')`, id); err != nil {
			t.Fatal(err)
		}
	}
	settings := storage.AcquisitionSettingsUpdate{Enabled: true, JackettURL: fake.URL, QBURL: fake.URL, JackettAPIKey: "KEY-PRIVATE", QBPassword: "PASSWORD-PRIVATE", QBCategory: "missing"}
	if err = app.store.UpdateAcquisitionSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	service := acquisition.New(app.store, app.logger, acquisition.Options{})
	app.SetAcquisition(service)
	handler := app.Handler()
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("/admin/settings/acquisition/test-qb", "")
	if w.Code != 400 || !strings.Contains(w.Body.String(), "category_missing") {
		t.Fatal(w.Code, w.Body.String())
	}
	settings.QBCategory = "music"
	if err = app.store.UpdateAcquisitionSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	service.SettingsChanged()
	for _, path := range []string{"/admin/settings/acquisition/test-qb", "/admin/settings/acquisition/test-jackett"} {
		w = request(path, "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	search, err := app.store.SaveAcquisitionSearch(ctx, storage.AcquisitionSearch{RGID: other, Indexer: "jpopsuki", Query: "album", Results: []catalog.Result{{Title: "FLAC", DownloadRef: "/dl/jpopsuki/?path=DOWNLOAD-PRIVATE"}}})
	if err != nil {
		t.Fatal(err)
	}
	meta, err := torrent.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := app.store.CreateAcquisitionTask(ctx, storage.AcquisitionTask{RGID: rg, IdempotencyKey: "bound", InfoHash: meta.InfoHash, Title: "bound title", Artist: "bound artist"})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"searchId":"` + search.ID + `","resultIdx":0,"idempotencyKey":"new"}`
	w = request("/admin/discover/rg/"+other+"/acquire", body)
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"publicId":"`+bound.PublicID+`"`) || !strings.Contains(w.Body.String(), "hash_bound") {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, secret := range []string{meta.InfoHash, "KEY-PRIVATE", "PASSWORD-PRIVATE", "DOWNLOAD-PRIVATE", "download_ref"} {
		if strings.Contains(w.Body.String()+logs.String(), secret) {
			t.Fatal("disclosed", secret)
		}
	}
	if _, err = service.Cancel(ctx, bound.PublicID); err != nil {
		t.Fatal(err)
	}
	w = request("/admin/discover/rg/"+other+"/acquire", body)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"state":"QB_SUBMITTING"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	fake.Close()
	service.SettingsChanged()
	w = request("/admin/settings/acquisition/test-qb", "")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "qb_unreachable") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAcquisitionTabSurvivesCandidateRefreshAndCancel(t *testing.T) {
	app, _, _ := csrfSessionApp(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite", httpTestDBPath[app.store])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE release_group_rebuild SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	const rg = "11111111-1111-4111-8111-111111111111"
	task, err := app.store.CreateAcquisitionTask(ctx, storage.AcquisitionTask{RGID: rg, Title: "snapshot without candidate", Artist: "artist", Kind: "Album", IdempotencyKey: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	rows, counts, err := app.store.ListDiscovery(ctx, "acquiring", 60, 0)
	if err != nil || counts.Acquiring != 1 || len(rows) != 1 || rows[0].Title != task.Title {
		t.Fatal(rows, counts, err)
	}
	service := acquisition.New(app.store, app.logger, acquisition.Options{})
	if _, err = service.Cancel(ctx, task.PublicID); err != nil {
		t.Fatal(err)
	}
	rows, counts, err = app.store.ListDiscovery(ctx, "acquiring", 60, 0)
	if err != nil || counts.Acquiring != 0 || len(rows) != 0 {
		t.Fatal(rows, counts, err)
	}
}

func TestAcquisitionCooldownRetryAfterHeader(t *testing.T) {
	app, _, _ := csrfSessionApp(t)
	for _, err := range []error{&sourcegate.CooldownError{Source: "jackett", RetryAfter: 120 * time.Second}, &jackett.APIError{Code: 500, RetryAfter: 120 * time.Second}, &jackett.APIError{Code: 501, RetryAfter: 120 * time.Second}, &jackett.HTTPError{Status: 429, RetryAfter: 120 * time.Second}} {
		w := httptest.NewRecorder()
		app.acquisitionError(w, err)
		if w.Code != 429 || w.Header().Get("Retry-After") != "120" {
			t.Fatalf("%T: %d %v", err, w.Code, w.Header())
		}
	}
}

func TestAcquisitionTabPagination(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite", httpTestDBPath[app.store])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE release_group_rebuild SET status='ready'`); err != nil {
		t.Fatal(err)
	}
	app.SetDiscovery(discovery.New(app.store, app.logger, discovery.Options{DataDir: t.TempDir()}))
	app.SetAcquisition(acquisition.New(app.store, app.logger, acquisition.Options{}))
	tasks := []storage.AcquisitionTask{}
	for i := 1; i <= 3; i++ {
		task, err := app.store.CreateAcquisitionTask(ctx, storage.AcquisitionTask{RGID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", i), IdempotencyKey: fmt.Sprint(i), Title: fmt.Sprint("snapshot", i)})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}
	for _, tc := range []struct {
		offset, limit int
		want          []string
	}{{0, 1, []string{tasks[2].PublicID}}, {1, 1, []string{tasks[1].PublicID}}, {2, 2, []string{tasks[0].PublicID}}, {3, 1, []string{}}} {
		rows, counts, err := app.store.ListDiscovery(ctx, "acquiring", tc.limit, tc.offset)
		if err != nil || counts.Acquiring != 3 || len(rows) != len(tc.want) {
			t.Fatal(rows, counts, err)
		}
		path := fmt.Sprintf("/admin/discover/items.json?tab=acquiring&offset=%d&limit=%d", tc.offset, tc.limit)
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		var body struct {
			Items  []acquisitionTaskDTO `json:"items"`
			Counts discoveryCountsDTO   `json:"counts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Counts.Acquiring != 3 || len(body.Items) != len(tc.want) {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		for i, id := range tc.want {
			if body.Items[i].PublicID != id || rows[i].Title != body.Items[i].Title {
				t.Fatal(body.Items, rows)
			}
		}
	}
}
