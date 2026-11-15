package httpapi

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/artistaliases"
)

func TestArtistAliasAdminAuthSettingsAndSecrets(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	svc, e := artistaliases.New(app.store, app.logger, artistaliases.Options{})
	if e != nil {
		t.Fatal(e)
	}
	app.SetArtistAliases(svc)
	handler := app.Handler()
	req := httptest.NewRequest("GET", artistAliasPath, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	rec = getWithCookie(handler, artistAliasPath, cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "开始存量匹配") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	form := url.Values{"enabled": {"on"}, "deepseek_key": {"sensitive-deepseek-secret"}, "search_limit": {"2"}, "fetch_limit": {"3"}, "model_limit": {"2"}, "rpm": {"6"}}
	post := func(csrfValue string) *httptest.ResponseRecorder {
		f := url.Values{}
		for k, v := range form {
			f[k] = v
		}
		f.Set("csrfToken", csrfValue)
		r := httptest.NewRequest("POST", artistAliasPath+"/settings", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if rec = post(""); rec.Code != 403 {
		t.Fatal(rec.Code)
	}
	if rec = post(csrf); rec.Code != 303 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	st, e := app.store.ArtistAliasSettings(context.Background())
	if e != nil || st.DeepSeekKey != "sensitive-deepseek-secret" || !st.Enabled {
		t.Fatal(st.Enabled, e)
	}
	for _, path := range []string{artistAliasPath, artistAliasPath + "/status", "/api/v1/artist-alias-completion"} {
		rec = getWithCookie(handler, path, cookie)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "sensitive-deepseek-secret") {
			t.Fatal(path, rec.Code)
		}
	}
	form.Set("deepseek_key", "")
	post(csrf)
	st, _ = app.store.ArtistAliasSettings(context.Background())
	if st.DeepSeekKey != "sensitive-deepseek-secret" {
		t.Fatal("blank key overwrote secret")
	}
	form.Set("search_limit", "0")
	post(csrf)
	st, _ = app.store.ArtistAliasSettings(context.Background())
	if st.SearchLimit != 2 {
		t.Fatal(st.SearchLimit)
	}
}
