package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/upnp"
)

type fakeCastRenderer struct {
	mu      sync.Mutex
	dev     upnp.Device
	queue   []upnp.Item
	start   int
	posMs   int64
	calls   []string
	index   int
	state   string
	inserts []int
}

func (f *fakeCastRenderer) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}
func (f *fakeCastRenderer) Device() upnp.Device { return f.dev }
func (f *fakeCastRenderer) Replace(_ context.Context, items []upnp.Item, start int, pos int64, play bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue, f.start, f.posMs, f.index = items, start, pos, start
	f.state = upnp.StatePlaying
	f.calls = append(f.calls, fmt.Sprintf("replace:%d:%d:%v", start, pos, play))
	return nil
}
func (f *fakeCastRenderer) Insert(_ context.Context, items []upnp.Item, at int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if at < 0 || at > len(f.queue) {
		at = len(f.queue)
	}
	f.queue = append(f.queue[:at:at], append(items, f.queue[at:]...)...)
	f.inserts = append(f.inserts, at)
	return nil
}
func (f *fakeCastRenderer) Remove(context.Context, int) error { f.record("remove"); return nil }
func (f *fakeCastRenderer) Move(_ context.Context, from, to int) error {
	f.record(fmt.Sprintf("move:%d:%d", from, to))
	return nil
}
func (f *fakeCastRenderer) PlayIndex(_ context.Context, i int) error {
	f.mu.Lock()
	f.index = i
	f.mu.Unlock()
	f.record(fmt.Sprintf("playIndex:%d", i))
	return nil
}
func (f *fakeCastRenderer) Play(context.Context) error  { f.record("play"); return nil }
func (f *fakeCastRenderer) Pause(context.Context) error { f.record("pause"); return nil }
func (f *fakeCastRenderer) Stop(context.Context) error  { f.record("stop"); return nil }
func (f *fakeCastRenderer) Next(context.Context) error  { f.record("next"); return nil }
func (f *fakeCastRenderer) Previous(context.Context) error {
	f.record("previous")
	return nil
}
func (f *fakeCastRenderer) Seek(_ context.Context, ms int64) error {
	f.record(fmt.Sprintf("seek:%d", ms))
	return nil
}
func (f *fakeCastRenderer) SetPlayMode(_ context.Context, loop string, shuffle bool) error {
	f.record(fmt.Sprintf("mode:%s:%v", loop, shuffle))
	return nil
}
func (f *fakeCastRenderer) Status(context.Context) (upnp.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := upnp.Status{State: f.state, Index: f.index, QueueLength: len(f.queue), QueueActive: true, PositionMs: 1234}
	if f.index >= 0 && f.index < len(f.queue) {
		st.TrackURI = f.queue[f.index].URI
	}
	return st, nil
}
func (f *fakeCastRenderer) Queue(context.Context) ([]upnp.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]upnp.Item(nil), f.queue...)
	return append(out, upnp.Item{Title: "Radio", URI: "x-sonosapi-stream:s1234"}), nil
}
func (f *fakeCastRenderer) Volume(context.Context) (int, error) { return 30, nil }
func (f *fakeCastRenderer) SetVolume(_ context.Context, v int) error {
	f.record(fmt.Sprintf("volume:%d", v))
	return nil
}

type fakeCastManager struct{ r *fakeCastRenderer }

func (m fakeCastManager) Devices(context.Context, bool) []upnp.Device { return []upnp.Device{m.r.dev} }
func (m fakeCastManager) Renderer(_ context.Context, id string) (upnp.Renderer, error) {
	if id != m.r.dev.ID {
		return nil, upnp.ErrDeviceNotFound
	}
	return m.r, nil
}

func setupCastApp(t *testing.T) (*App, *fakeCastRenderer, string, map[string]int64) {
	t.Helper()
	ctx := context.Background()
	app, store, token := setupTestApp(t)
	app.config.CastBaseURL = "http://192.168.1.10:4533"
	app.transcoder.available = map[string]bool{"mp3": true, "flac": true}
	if err := store.EnsureLibrary(ctx, "Cast", "/cast"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/cast")
	files := []struct {
		name, codec string
		rate        int
	}{{"cd", "flac", 44100}, {"hires", "flac", 96000}, {"opus", "opus", 48000}}
	for i, f := range files {
		if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: f.name + ".flac", FileSize: 1, ModifiedAtNS: 1,
			Metadata:   metadata.AudioMetadata{Title: "Song " + f.name, Album: "Album & Co", Artists: []string{"Singer"}, DiscNumber: 1, TrackNumber: i + 1, DurationMillis: 180000, Container: "flac", MIMEType: "audio/flac"},
			AudioProps: metadata.AudioProps{Codec: f.codec, SampleRate: f.rate, BitDepth: 16}}); err != nil {
			t.Fatal(err)
		}
	}
	tracks, err := store.ListTracks(ctx, storage.Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, tr := range tracks {
		ids[strings.TrimPrefix(tr.Title, "Song ")] = tr.ID
	}
	r := &fakeCastRenderer{dev: upnp.Device{ID: "RINCON_1", Kind: upnp.KindSonos, Host: "192.168.1.30", Queue: true}, index: -1, state: upnp.StateStopped}
	app.SetCastManager(fakeCastManager{r})
	return app, r, token, ids
}

func castRequest(t *testing.T, app *App, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func TestCastReplaceQueueBuildsSonosItems(t *testing.T) {
	app, r, token, ids := setupCastApp(t)
	body := fmt.Sprintf(`{"mode":"replace","trackIds":[%d,%d,%d],"startIndex":0,"positionMs":5000}`, ids["cd"], ids["hires"], ids["opus"])
	rec := castRequest(t, app, token, http.MethodPost, "/api/v1/cast/devices/RINCON_1/queue", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var st castStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil || st.TrackID != ids["cd"] || st.Index != 0 || st.QueueLength != 3 {
		t.Fatalf("status = %+v %v", st, err)
	}
	if len(r.calls) != 1 || r.calls[0] != "replace:0:5000:true" {
		t.Fatalf("calls = %v", r.calls)
	}
	token = app.currentCredentials().mediaToken
	base := "http://192.168.1.10:4533/api/v1/tracks/"
	want := []struct{ uri, mime string }{
		{fmt.Sprintf("%s%d/stream?mediaToken=%s", base, ids["cd"], token), "audio/flac"},
		{fmt.Sprintf("%s%d/transcode.flac?mediaToken=%s&maxSampleRate=48000", base, ids["hires"], token), "audio/flac"},
		{fmt.Sprintf("%s%d/transcode.mp3?mediaToken=%s&bitrate=320", base, ids["opus"], token), "audio/mpeg"},
	}
	for i, w := range want {
		if r.queue[i].URI != w.uri || r.queue[i].MIME != w.mime {
			t.Errorf("item %d = %s (%s), want %s (%s)", i, r.queue[i].URI, r.queue[i].MIME, w.uri, w.mime)
		}
	}
	first := r.queue[0]
	if first.Title != "Song cd" || first.Artist != "Singer" || first.Album != "Album & Co" || first.DurationMs != 180000 {
		t.Errorf("metadata = %+v", first)
	}
	if !strings.Contains(first.DIDL(), "Album &amp; Co") {
		t.Errorf("DIDL = %s", first.DIDL())
	}
}

func TestCastInsertControlStatusAndQueue(t *testing.T) {
	app, r, token, ids := setupCastApp(t)
	castRequest(t, app, token, http.MethodPost, "/api/v1/cast/devices/RINCON_1/queue", fmt.Sprintf(`{"trackIds":[%d,%d]}`, ids["cd"], ids["cd"]))
	rec := castRequest(t, app, token, http.MethodPost, "/api/v1/cast/devices/RINCON_1/queue", fmt.Sprintf(`{"mode":"insert","insertAt":1,"trackIds":[%d]}`, ids["opus"]))
	if rec.Code != http.StatusOK || len(r.queue) != 3 || r.inserts[0] != 1 || castTrackID(r.queue[1].URI) != ids["opus"] {
		t.Fatalf("insert %d %s queue=%v", rec.Code, rec.Body.String(), r.queue)
	}
	for _, c := range []struct{ body, want string }{
		{`{"action":"pause"}`, "pause"},
		{`{"action":"seek","positionMs":61000}`, "seek:61000"},
		{`{"action":"playIndex","index":2}`, "playIndex:2"},
		{`{"action":"playMode","loop":"all","shuffle":true}`, "mode:all:true"},
		{`{"action":"volume","volume":40}`, "volume:40"},
	} {
		rec := castRequest(t, app, token, http.MethodPost, "/api/v1/cast/devices/RINCON_1/control", c.body)
		if rec.Code != http.StatusOK || r.calls[len(r.calls)-1] != c.want {
			t.Fatalf("%s -> %d %s calls=%v", c.body, rec.Code, rec.Body.String(), r.calls)
		}
	}
	rec = castRequest(t, app, token, http.MethodGet, "/api/v1/cast/devices/RINCON_1/status?volume=1", "")
	var st castStatusResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Index != 2 || st.TrackID != ids["cd"] || st.Volume == nil || *st.Volume != 30 {
		t.Fatalf("status = %s", rec.Body.String())
	}
	rec = castRequest(t, app, token, http.MethodGet, "/api/v1/cast/devices/RINCON_1/queue", "")
	var q struct {
		Items []castQueueTrack `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &q)
	if len(q.Items) != 4 || q.Items[1].TrackID != ids["opus"] || q.Items[1].Title != "Song opus" || q.Items[1].DurationMs != 180000 || q.Items[3].TrackID != 0 || q.Items[3].Title != "Radio" {
		t.Fatalf("queue = %s", rec.Body.String())
	}
	if rec := castRequest(t, app, token, http.MethodPost, "/api/v1/cast/devices/RINCON_1/queue/move", `{"from":0,"to":2}`); rec.Code != 200 || r.calls[len(r.calls)-1] != "move:0:2" {
		t.Fatalf("move %d %v", rec.Code, r.calls)
	}
}

func TestCastRejectsBadRequests(t *testing.T) {
	app, _, token, ids := setupCastApp(t)
	for _, c := range []struct {
		method, path, body string
		code               int
	}{
		{http.MethodPost, "/api/v1/cast/devices/RINCON_1/control", `{"action":"explode"}`, 400},
		{http.MethodPost, "/api/v1/cast/devices/NOPE/control", `{"action":"play"}`, 404},
		{http.MethodPost, "/api/v1/cast/devices/RINCON_1/queue", `{"mode":"append","trackIds":[]}`, 400},
		{http.MethodPost, "/api/v1/cast/devices/RINCON_1/queue", `{"trackIds":[999999]}`, 404},
		{http.MethodPost, "/api/v1/cast/devices/RINCON_1/queue", `{"mode":"shuffle","trackIds":[1]}`, 400},
	} {
		if rec := castRequest(t, app, token, c.method, c.path, c.body); rec.Code != c.code {
			t.Errorf("%s %s -> %d %s, want %d", c.path, c.body, rec.Code, rec.Body.String(), c.code)
		}
	}
	if rec := castRequest(t, app, "", http.MethodGet, "/api/v1/cast/devices", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", rec.Code)
	}
	if rec := castRequest(t, app, token, http.MethodGet, "/api/v1/cast/devices", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"RINCON_1"`) {
		t.Fatalf("devices = %d %s", rec.Code, rec.Body.String())
	}
	_ = ids
}

func TestCastTrackIDAndBaseURL(t *testing.T) {
	for uri, want := range map[string]int64{
		"http://10.0.0.2:4533/api/v1/tracks/42/stream?mediaToken=x":                   42,
		"x-rincon-mp3radio://10.0.0.2:4533/api/v1/tracks/7/transcode.mp3?bitrate=320": 7,
		"x-sonosapi-stream:s1234": 0,
	} {
		if got := castTrackID(uri); got != want {
			t.Errorf("castTrackID(%s) = %d", uri, got)
		}
	}
	app := &App{}
	app.config.ListenAddress = ":4533"
	dev := upnp.Device{Host: "192.168.1.30"}
	req := httptest.NewRequest(http.MethodGet, "http://192.168.1.10:8080/x", nil)
	if got := app.castBaseURL(req, dev); got != "http://192.168.1.10:8080" {
		t.Errorf("same-LAN browser host = %s", got)
	}
	app.config.CastBaseURL = "http://music.lan:4533"
	if got := app.castBaseURL(req, dev); got != "http://music.lan:4533" {
		t.Errorf("configured = %s", got)
	}
	app.config.CastBaseURL = ""
	req = httptest.NewRequest(http.MethodGet, "http://localhost:4533/x", nil)
	if got := app.castBaseURL(req, upnp.Device{Host: "127.0.0.1"}); got != "http://127.0.0.1:4533" {
		t.Errorf("outbound = %s", got)
	}
}
