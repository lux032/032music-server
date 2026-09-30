package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebPerformerSeparationKeepsAPI(t *testing.T) {
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	if e := store.EnsureLibrary(ctx, "Web", "/web"); e != nil {
		t.Fatal(e)
	}
	lib, _ := store.LibraryByRoot(ctx, "/web")
	if e := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Vocal Song", Album: "Vocal Album", Artists: []string{"Vocal Singer"}, AlbumArtists: []string{"Album Singer"}, Composer: "Only Composer", DiscNumber: 1, TrackNumber: 1}}); e != nil {
		t.Fatal(e)
	}
	people, _ := store.ListArtists(ctx, storage.Filters{})
	var composer, singer int64
	for _, p := range people {
		if p.Name == "Only Composer" {
			composer = p.ID
		}
		if p.Name == "Vocal Singer" {
			singer = p.ID
		}
	}
	login := httptest.NewRecorder()
	if _, e := app.sessions.create(login, "admin"); e != nil {
		t.Fatal(e)
	}
	cookie := login.Result().Cookies()[0]
	get := func(path string, api bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if api {
			r.Header.Set("Authorization", "Bearer "+token)
		} else {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w
	}
	for _, section := range []string{"tracks", "albums"} {
		t.Run("missing artist "+section, func(t *testing.T) {
			body := get("/admin/"+section+"?artist=999999999", false).Body.String()
			if !strings.Contains(body, "共 0 ") || strings.Contains(body, `data-track-title="Vocal Song"`) || strings.Contains(body, `data-album-title="Vocal Album"`) {
				t.Fatalf("expected empty %s list: %s", section, body)
			}
		})
	}
	for _, path := range []string{fmt.Sprintf("/api/v1/tracks?artist=%d", composer), "/api/v1/tracks?q=Only+Composer", "/api/v1/artists?role=track"} {
		body := get(path, true).Body.String()
		want := "Vocal Song"
		if strings.Contains(path, "/artists?") {
			want = "Only Composer"
		}
		if !strings.Contains(body, want) {
			t.Fatalf("API changed: %s", body)
		}
	}
	detail := get(fmt.Sprintf("/api/v1/artists/%d", composer), true)
	var d map[string]any
	if e := json.Unmarshal(detail.Body.Bytes(), &d); e != nil {
		t.Fatal(e)
	}
	d = d["artist"].(map[string]any)
	if d["trackCount"] != float64(1) {
		t.Fatal(d)
	}
	for _, key := range []string{"PerformedTrackCount", "CreditTrackCount", "performedTrackCount", "creditTrackCount"} {
		if _, ok := d[key]; ok {
			t.Fatalf("API leaked %s", key)
		}
	}
	body := get("/admin/artists/track", false).Body.String()
	if strings.Contains(body, "Only Composer") {
		t.Fatal("pure composer on singers page")
	}
	body = get("/admin/tracks?q=Only+Composer", false).Body.String()
	if strings.Contains(body, `data-track-title="Vocal Song"`) {
		t.Fatal("web query matched composer")
	}
	body = get(fmt.Sprintf("/admin/tracks?artist=%d", composer), false).Body.String()
	if !strings.Contains(body, "查看幕后作品") || !strings.Contains(body, fmt.Sprintf("/admin/artists/%d#credits", composer)) {
		t.Fatal("missing backstage hint")
	}
	for _, condition := range append([]string{"q=missing", "album=999999", "year=1900", "genre=missing", "index=A"}, func() []string {
		conditions := []string{}
		for _, name := range trackFocusNames {
			conditions = append(conditions, name+"=invalid")
		}
		return conditions
	}()...) {
		t.Run("no backstage hint with "+condition, func(t *testing.T) {
			body := get(fmt.Sprintf("/admin/tracks?artist=%d&%s", composer, condition), false).Body.String()
			if !strings.Contains(body, "共 0 ") || strings.Contains(body, "查看幕后作品") || strings.Contains(body, "该人员没有演唱曲目") {
				t.Fatalf("filtered empty result should not show backstage hint: %s", body)
			}
		})
	}
	body = get(fmt.Sprintf("/admin/artists/%d", composer), false).Body.String()
	if !strings.Contains(body, "幕后 1 首") || strings.Contains(body, `class="artist-tracks-section"`) || strings.Contains(body, `class="artist-discography"`) {
		t.Fatalf("pure credit detail %s", body)
	}
	body = get(fmt.Sprintf("/admin/artists/%d", singer), false).Body.String()
	if !strings.Contains(body, "演唱 1 首") || !strings.Contains(body, "查看全部 1 首") {
		t.Fatal("performed counts missing")
	}
	rec := get("/admin/credits?credit=composer&sort=tracks", false)
	body = rec.Body.String()
	if !strings.Contains(body, "Only Composer") || !strings.Contains(body, "幕后 1 首") || !strings.Contains(body, "credit=composer&amp;index=") {
		t.Fatal("credit directory or index missing")
	}
	nav := `href="/admin/credits" data-nav="credits" class="active" aria-current="page" title="幕后人员" aria-label="幕后人员"`
	if strings.Count(body, nav) != 2 {
		t.Fatal("credits navigation lacks consistent active accessibility attributes")
	}
	found := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "032_sort_credits" && c.Value == "tracks" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing credits sort cookie")
	}
	req := httptest.NewRequest("GET", "/admin/credits?credit=composer&index=C&sort=tracks", nil)
	if filterQuery(req).Get("credit") != "composer" {
		t.Fatal("lost credit")
	}
	data := libraryPageData{Total: 121}
	setPagination(req, &data, storage.Filters{Limit: 60})
	if !strings.Contains(data.NextURL, "credit=composer") {
		t.Fatal(data.NextURL)
	}
}
