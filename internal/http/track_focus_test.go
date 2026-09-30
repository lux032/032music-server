package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestParseTrackFocus(t *testing.T) {
	q, _ := url.ParseQuery("decade=2010,2020&decade=2010&trackType=regular&excludeTypes=off_vocal&hideInstrumental=true&arrangerArtist=123&credit=arranger:123")
	f, bad := parseTrackFocus(q)
	if len(bad) > 0 || len(f.Decades) != 2 || len(f.Credits) != 1 || len(f.ExcludeTypes) != 2 {
		t.Fatalf("%+v %v", f, bad)
	}
	for _, raw := range []string{"quality=wav", "quality=lossless&quality=hires", "format=wav", "credit=primary:1", "credit=composer:x", "trackType=regular&excludeTypes=regular", "trackType=instrumental&hideInstrumental=true", "tieupRole=no", "workType=no", "decade=2011", "yearFrom=2020&yearTo=2010"} {
		q, _ = url.ParseQuery(raw)
		_, bad = parseTrackFocus(q)
		if len(bad) == 0 {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestTracksFocusLinksRetainIndividualValues(t *testing.T) {
	req := httptest.NewRequest("GET", "/admin/tracks?decade=2010,2020&credit=composer:1&credit=arranger:2&format=flac&format=alac&page=4&index=A", nil)
	base := filterQuery(req)
	if len(base["decade"]) != 2 || len(base["credit"]) != 2 {
		t.Fatal(base)
	}
	if !strings.Contains(indexLinks(req, "/admin/tracks")[0].URL, "credit=arranger%3A2") {
		t.Fatal("index lost credit")
	}
	fields := searchFields(req)
	count := 0
	for _, f := range fields {
		if f.Name == "credit" {
			count++
		}
	}
	if count != 2 {
		t.Fatal(fields)
	}
	for _, tag := range filterTags(req, "/admin/tracks") {
		if tag.Name == "format" && tag.Value == "flac" {
			u, _ := url.Parse(tag.RemoveURL)
			q := u.Query()
			if q.Get("format") != "alac" || len(q["credit"]) != 2 || len(q["decade"]) != 2 {
				t.Fatal(q)
			}
		}
	}
	data := libraryPageData{}
	setPagination(req, &data, storage.Filters{Limit: 2}) // links retain whole original query
	req = httptest.NewRequest("GET", "/admin/albums?credit=composer:1&format=flac", nil)
	if len(filterQuery(req)) != 0 {
		t.Fatal("focus leaked onto albums")
	}
}
func TestTracksFocusAPIAndArtistCredits(t *testing.T) {
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	if e := store.EnsureLibrary(ctx, "Focus", "/focus"); e != nil {
		t.Fatal(e)
	}
	lib, _ := store.LibraryByRoot(ctx, "/focus")
	for i := 0; i < 3; i++ {
		kind := "regular"
		if i == 2 {
			kind = "instrumental"
		}
		if e := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("%d.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: fmt.Sprintf("Song%d", i), Album: "Album", Year: 2020, Artists: []string{"Singer"}, Composer: "Composer", Arranger: "Arranger", TrackType: kind, DiscNumber: 1, TrackNumber: i + 1}, AudioProps: metadata.AudioProps{Codec: "flac", SampleRate: 44100, BitDepth: 16}}); e != nil {
			t.Fatal(e)
		}
	}
	options, e := store.ListArtistOptions(ctx, "arranger", "", 20)
	if e != nil || len(options) != 1 {
		t.Fatalf("options %v %v", options, e)
	}
	id := options[0].ID
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/api/v1/tracks?format=wav", "/api/v1/tracks?trackType=instrumental&hideInstrumental=true", fmt.Sprintf("/api/v1/artists/%d/credits?role=primary", id)} {
		rec := get(path)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "invalid_filter") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := get(fmt.Sprintf("/api/v1/tracks?year=2020&trackType=regular&arrangerArtist=%d&format=flac", id))
	var page struct {
		Items         []storage.Track
		Total         int64
		Roles         []storage.CreditRoleCount
		Limit, Offset int
	}
	if e = json.Unmarshal(rec.Body.Bytes(), &page); e != nil || rec.Code != 200 || page.Total != 2 || len(page.Items) != 2 || len(page.Items[0].Credits) != 2 {
		t.Fatalf("intersection %d %s %v", rec.Code, rec.Body.String(), e)
	}
	rec = get("/api/v1/tracks?excludeTypes=instrumental,off_vocal")
	json.Unmarshal(rec.Body.Bytes(), &page)
	if page.Total != 2 {
		t.Fatal(rec.Body.String())
	}
	rec = get(fmt.Sprintf("/api/v1/artists/%d/credits?role=arranger&limit=1&offset=1", id))
	page.Items = nil
	json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != 200 || len(page.Items) != 1 || page.Total != 3 || page.Limit != 1 || page.Offset != 1 || len(page.Roles) != 1 || page.Roles[0].Count != 3 {
		t.Fatalf("credits %d %s", rec.Code, rec.Body.String())
	}
	rec = get(fmt.Sprintf("/api/v1/artists/%d/credits", id))
	json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != 200 || page.Total != 3 {
		t.Fatal(rec.Body.String())
	}
	if rec = get("/api/v1/artists/999999/credits"); rec.Code != 404 {
		t.Fatalf("404: %d %s", rec.Code, rec.Body.String())
	}
	login := httptest.NewRecorder()
	app.sessions.create(login, "admin")
	tracks, _ := store.ListTracks(ctx, storage.Filters{})
	req := httptest.NewRequest("GET", fmt.Sprintf("/admin/albums/%d", tracks[0].AlbumID), nil)
	req.AddCookie(login.Result().Cookies()[0])
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	composerOptions, _ := store.ListArtistOptions(ctx, "composer", "", 20)
	composerID := composerOptions[0].ID
	if rec.Code != 200 || !strings.Contains(body, fmt.Sprintf("/admin/artists/%d?credit=composer#credits", composerID)) {
		t.Fatalf("album credits %d %s", rec.Code, body)
	}
	if strings.Contains(body, fmt.Sprintf("href=\"/admin/artists/%d\"", composerID)) {
		t.Fatal("composer rendered as singer")
	}
	req = httptest.NewRequest("GET", "/admin/tracks?format=invalid", nil)
	req.AddCookie(login.Result().Cookies()[0])
	rec = httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "已忽略无效筛选") {
		t.Fatalf("web invalid %d %s", rec.Code, rec.Body.String())
	}
}

func TestLegacyFilterCommasAndSearchFieldOrder(t *testing.T) {
	for _, section := range []string{"albums", "artists", "tracks"} {
		t.Run(section, func(t *testing.T) {
			path := "/admin/" + section
			req := httptest.NewRequest("GET", path+"?q=Hello%2C+World&genre=Pop%2C+Rock&artist=12&index=A&format=flac&format=alac", nil)
			base := filterQuery(req)
			if len(base["q"]) != 1 || base.Get("q") != "Hello, World" {
				t.Fatalf("query altered: %v", base)
			}
			for _, link := range indexLinks(req, path) {
				u, _ := url.Parse(link.URL)
				if u.Query().Get("q") != "Hello, World" || len(u.Query()["q"]) != 1 {
					t.Fatalf("index query altered: %s", link.URL)
				}
			}
			for _, tag := range filterTags(req, path) {
				if tag.Name == "q" {
					continue
				}
				u, _ := url.Parse(tag.RemoveURL)
				if u.Query().Get("q") != "Hello, World" || len(u.Query()["q"]) != 1 {
					t.Fatalf("tag query altered: %+v", tag)
				}
			}
			fields := searchFields(req)
			expected := []queryField{{Name: "artist", Value: "12"}, {Name: "genre", Value: "Pop, Rock"}, {Name: "index", Value: "A"}}
			if section == "tracks" {
				expected = append(expected, queryField{Name: "format", Value: "flac"}, queryField{Name: "format", Value: "alac"})
			}
			for i := 0; i < 20; i++ {
				fields = searchFields(req)
				if len(fields) != len(expected) {
					t.Fatalf("fields %+v", fields)
				}
				for j := range expected {
					if fields[j] != expected[j] {
						t.Fatalf("fields %+v want %+v", fields, expected)
					}
				}
			}
			// q belongs to the visible input; only other filters are hidden.
			for _, field := range fields {
				if field.Name == "q" {
					t.Fatal("q duplicated in hidden fields")
				}
			}
		})
	}
}

func TestConflictTagsMatchEffectiveTrackTypes(t *testing.T) {
	for _, raw := range []string{"trackType=regular&excludeTypes=regular", "trackType=regular,instrumental&hideInstrumental=true"} {
		req := httptest.NewRequest("GET", "/admin/tracks?"+raw, nil)
		for _, tag := range filterTags(req, "/admin/tracks") {
			if tag.Name == "trackType" {
				t.Fatalf("discarded condition displayed: %+v", tag)
			}
			u, _ := url.Parse(tag.RemoveURL)
			if u.Query().Has("trackType") {
				t.Fatalf("discarded condition retained in removal URL: %s", tag.RemoveURL)
			}
		}
	}
}

func TestMergedArtistRedirectPreservesOnlyRequestedCredit(t *testing.T) {
	ctx := context.Background()
	app, store, _ := setupTestApp(t)
	if err := store.EnsureLibrary(ctx, "Redirect", "/redirect"); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Redirect Source"}, AlbumArtists: []string{"Redirect Target"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	sourceOptions, err := store.ListArtistOptions(ctx, "all", "Redirect Source", 20)
	if err != nil || len(sourceOptions) != 1 {
		t.Fatalf("source %v %v", sourceOptions, err)
	}
	targetOptions, err := store.ListArtistOptions(ctx, "all", "Redirect Target", 20)
	if err != nil || len(targetOptions) != 1 {
		t.Fatalf("target %v %v", targetOptions, err)
	}
	source, target := sourceOptions[0].ID, targetOptions[0].ID
	if _, err := store.MergeArtists(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	if _, err := app.sessions.create(login, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "?credit=arranger", "?credit="} {
		req := httptest.NewRequest("GET", fmt.Sprintf("/admin/artists/%d%s", source, suffix), nil)
		req.AddCookie(login.Result().Cookies()[0])
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		if rec.Code != 303 {
			t.Fatalf("status %d %s", rec.Code, rec.Body.String())
		}
		u, err := url.Parse(rec.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		if u.Path != fmt.Sprintf("/admin/artists/%d", target) || u.Query().Get("notice") == "" {
			t.Fatalf("location %s", u)
		}
		if suffix == "" {
			if u.Query().Has("credit") || u.Fragment != "" {
				t.Fatalf("unrequested credit redirect %s", u)
			}
		} else {
			if !u.Query().Has("credit") || u.Fragment != "credits" || u.Query().Get("credit") != req.URL.Query().Get("credit") {
				t.Fatalf("lost credit %s", u)
			}
		}
	}
}
