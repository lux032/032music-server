package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

type transcodeFixture struct {
	t                   *testing.T
	app                 *App
	store               *storage.Store
	root, ffmpeg, probe string
	handler             http.Handler
	ids                 []int64
}

func newTranscodeFixture(t *testing.T, requireProbe bool) *transcodeFixture {
	t.Helper()
	ff, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("ffmpeg unavailable")
	}
	probe, e := exec.LookPath("ffprobe")
	if requireProbe && e != nil {
		t.Skip("ffprobe unavailable")
	}
	app, s, _ := setupTestApp(t)
	for _, codec := range []string{"mp3", "ogg", "flac"} {
		if !app.transcoder.available[codec] {
			t.Skipf("ffmpeg encoder for %s unavailable", codec)
		}
	}
	app.transcoder.path = ff
	root := t.TempDir()
	return &transcodeFixture{t: t, app: app, store: s, root: root, ffmpeg: ff, probe: probe, handler: app.Handler()}
}
func (f *transcodeFixture) source(name string, rate, depth int, codec string, cover bool) int64 {
	f.t.Helper()
	path := filepath.Join(f.root, name)
	duration := 2
	if strings.Contains(name, "disconnect") {
		duration = 60
	}
	args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:sample_rate=%d:duration=%d", rate, duration)}
	if cover {
		args = append(args, "-f", "lavfi", "-i", "color=c=red:s=32x32:d=1", "-map", "0:a:0", "-map", "1:v:0", "-c:v", "mjpeg", "-disposition:v:0", "attached_pic")
	}
	if codec == "opus" {
		args = append(args, "-c:a", "libopus")
	} else {
		args = append(args, "-c:a", "flac")
		if depth == 24 {
			args = append(args, "-sample_fmt", "s32", "-bits_per_raw_sample", "24")
		}
	}
	args = append(args, "-y", path)
	if out, e := exec.Command(f.ffmpeg, args...).CombinedOutput(); e != nil {
		f.t.Fatalf("generate %s: %v %s", name, e, out)
	}
	info, e := os.Stat(path)
	if e != nil {
		f.t.Fatal(e)
	}
	ctx := context.Background()
	if e = f.store.EnsureLibrary(ctx, "Test", f.root); e != nil {
		f.t.Fatal(e)
	}
	lib, e := f.store.LibraryByRoot(ctx, f.root)
	if e != nil {
		f.t.Fatal(e)
	}
	if e = f.store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: name, FileSize: info.Size(), ModifiedAtNS: info.ModTime().UnixNano(), Metadata: metadata.AudioMetadata{Title: name, Album: "Album", Artists: []string{"Singer"}, DiscNumber: 1, TrackNumber: len(f.ids) + 1, DurationMillis: 2000}, AudioProps: metadata.AudioProps{Codec: codec, SampleRate: rate, BitDepth: depth}}); e != nil {
		f.t.Fatal(e)
	}
	tracks, e := f.store.SyncTracks(ctx, storage.SyncTracksParams{})
	if e != nil {
		f.t.Fatal(e)
	}
	for _, item := range tracks.Items {
		if item.Title == name {
			f.ids = append(f.ids, item.ID)
			return item.ID
		}
	}
	f.t.Fatalf("missing imported track %s", name)
	return 0
}
func (f *transcodeFixture) request(id int64, method, format, query, rng string, authorized bool) *httptest.ResponseRecorder {
	f.t.Helper()
	url := fmt.Sprintf("/api/v1/tracks/%d/transcode.%s", id, format)
	if authorized {
		url += "?mediaToken=" + f.app.config.MediaToken
		if query != "" {
			url += "&" + query
		}
	} else if query != "" {
		url += "?" + query
	}
	r := httptest.NewRequest(method, url, nil)
	if rng != "" {
		r.Header.Set("Range", rng)
	}
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}
func (f *transcodeFixture) inspect(b []byte) struct {
	Streams []struct {
		CodecType  string `json:"codec_type"`
		SampleRate string `json:"sample_rate"`
		Bits       string `json:"bits_per_raw_sample"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
} {
	f.t.Helper()
	var v struct {
		Streams []struct {
			CodecType  string `json:"codec_type"`
			SampleRate string `json:"sample_rate"`
			Bits       string `json:"bits_per_raw_sample"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	cmd := exec.Command(f.probe, "-v", "error", "-show_entries", "stream=codec_type,sample_rate,bits_per_raw_sample", "-show_entries", "format=duration", "-of", "json", "pipe:0")
	cmd.Stdin = bytes.NewReader(b)
	out, e := cmd.CombinedOutput()
	if e != nil {
		f.t.Fatalf("ffprobe: %v %s", e, out)
	}
	if e = json.Unmarshal(out, &v); e != nil {
		f.t.Fatal(e)
	}
	return v
}
func (f *transcodeFixture) check(status int, w *httptest.ResponseRecorder) {
	f.t.Helper()
	if w.Code != status {
		f.t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
	}
}
func TestTranscodeEmbeddedCoverLive(t *testing.T) {
	f := newTranscodeFixture(t, true)
	id := f.source("cover.flac", 44100, 16, "flac", true)
	for format, mime := range map[string]string{"mp3": "audio/mpeg", "ogg": "audio/ogg"} {
		w := f.request(id, "GET", format, "", "", true)
		f.check(200, w)
		if w.Header().Get("Content-Type") != mime || w.Header().Get("Accept-Ranges") != "none" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s headers: %v", format, w.Header())
		}
		v := f.inspect(w.Body.Bytes())
		if len(v.Streams) != 1 || v.Streams[0].CodecType != "audio" {
			t.Fatalf("%s streams %+v", format, v.Streams)
		}
	}
}
func TestTranscodeOpusSource(t *testing.T) {
	f := newTranscodeFixture(t, true)
	id := f.source("opus.ogg", 48000, 0, "opus", false)
	for _, format := range []string{"mp3", "ogg", "flac"} {
		w := f.request(id, "GET", format, "", "", true)
		f.check(200, w)
		v := f.inspect(w.Body.Bytes())
		if len(v.Streams) != 1 || v.Streams[0].CodecType != "audio" {
			t.Fatalf("%s: %+v", format, v.Streams)
		}
		if format == "flac" && v.Streams[0].Bits != "16" {
			t.Fatalf("opus -> flac bits: %s", v.Streams[0].Bits)
		}
	}
}
func TestTranscodeOffsetDurationAndValidation(t *testing.T) {
	f := newTranscodeFixture(t, true)
	id := f.source("offset.flac", 44100, 16, "flac", false)
	for _, format := range []string{"mp3", "ogg"} {
		w := f.request(id, "GET", format, "offsetMs=700", "", true)
		f.check(200, w)
		_ = f.inspect(w.Body.Bytes())
		// Pipe-based MP3/Ogg has no container duration; count decoded samples.
		decode := exec.Command(f.ffmpeg, "-v", "error", "-i", "pipe:0", "-map", "0:a:0", "-f", "s16le", "-ac", "1", "-ar", "48000", "pipe:1")
		decode.Stdin = bytes.NewReader(w.Body.Bytes())
		pcm, e := decode.Output()
		if e != nil {
			t.Fatalf("decode %s: %v", format, e)
		}
		duration := float64(len(pcm)) / (2 * 48000)
		if duration < 1.0 || duration > 1.6 {
			t.Fatalf("%s duration %.3fs", format, duration)
		}
		f.check(400, f.request(id, "GET", format, "offsetMs=2001", "", true))
	}
	f.check(400, f.request(id, "GET", "flac", "offsetMs=0", "", true))
}
func TestTranscodeMaxSampleRate(t *testing.T) {
	f := newTranscodeFixture(t, true)
	for _, tc := range []struct{ rate, want int }{{96000, 48000}, {88200, 44100}, {44100, 44100}} {
		id := f.source(fmt.Sprintf("rate%d.flac", tc.rate), tc.rate, 24, "flac", false)
		w := f.request(id, "GET", "flac", "maxSampleRate=48000", "", true)
		f.check(200, w)
		v := f.inspect(w.Body.Bytes())
		if len(v.Streams) != 1 || v.Streams[0].SampleRate != strconv.Itoa(tc.want) || v.Streams[0].Bits != "24" {
			t.Fatalf("input %d: %+v", tc.rate, v.Streams)
		}
	}
}
func TestTranscodeMediaAuthAndParameters(t *testing.T) {
	f := newTranscodeFixture(t, false)
	id := f.source("auth.flac", 44100, 16, "flac", false)
	f.check(401, f.request(id, "HEAD", "mp3", "", "", false))
	f.check(200, f.request(id, "HEAD", "mp3", "", "", true))
	for _, tc := range []struct{ format, query string }{{"mp3", "bitrate=42"}, {"ogg", "bitrate=320"}, {"flac", "bitrate=128"}, {"mp3", "maxSampleRate=48000"}, {"flac", "maxSampleRate=44100"}, {"flac", "offsetMs=0"}} {
		f.check(400, f.request(id, "GET", tc.format, tc.query, "", true))
	}
}
func TestTranscodeCorruptAndUnavailable(t *testing.T) {
	f := newTranscodeFixture(t, false)
	path := filepath.Join(f.root, "broken.flac")
	if e := os.WriteFile(path, []byte("not an audio file"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if e := f.store.EnsureLibrary(ctx, "Test", f.root); e != nil {
		t.Fatal(e)
	}
	lib, e := f.store.LibraryByRoot(ctx, f.root)
	if e != nil {
		t.Fatal(e)
	}
	e = f.store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "broken.flac", FileSize: 17, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "bad", Album: "Album", DiscNumber: 1, TrackNumber: 1}})
	if e != nil {
		t.Fatal(e)
	}
	items, e := f.store.SyncTracks(ctx, storage.SyncTracksParams{})
	if e != nil {
		t.Fatal(e)
	}
	id := items.Items[0].ID
	for _, format := range []string{"mp3", "flac"} {
		w := f.request(id, "GET", format, "", "", true)
		f.check(502, w)
		if !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("non-JSON error")
		}
	}
	entries, _ := os.ReadDir(f.app.transcoder.dir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") {
			t.Fatal("leftover .part")
		}
	}
	f.app.transcoder.available["ogg"] = false
	f.check(503, f.request(id, "GET", "ogg", "", "", true))
	f.check(502, f.request(id, "GET", "mp3", "", "", true))
	missing := newTranscodeManager(config.Config{DataDirectory: t.TempDir(), FFmpegPath: filepath.Join(t.TempDir(), "nonexistent")}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.app.transcoder = missing
	for _, format := range []string{"mp3", "ogg", "flac"} {
		w := f.request(id, "GET", format, "", "", true)
		f.check(503, w)
		if !strings.Contains(w.Body.String(), "transcode_unavailable") {
			t.Fatal(w.Body.String())
		}
	}
	r := httptest.NewRequest("GET", "/api/v1/capabilities", nil)
	r.Header.Set("Authorization", "Bearer "+f.app.config.APIToken)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	f.check(200, w)
	if !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), `"transcode":false`) {
		t.Fatal(w.Body.String())
	}
}
func TestTranscodeCacheSingleflightRangeInvalidation(t *testing.T) {
	f := newTranscodeFixture(t, false)
	id := f.source("cache.flac", 44100, 16, "flac", false)
	var calls atomic.Int32
	release := make(chan struct{})
	f.app.transcoder.run = func(ctx context.Context, args []string, dest string) error {
		calls.Add(1)
		select {
		case <-release:
			return f.app.transcoder.convert(ctx, args, dest)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 5)
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.request(id, "GET", "flac", "", "", true) }()
	}
	deadline := time.After(2 * time.Second)
	for calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("job never started")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(release)
	wg.Wait()
	close(results)
	for w := range results {
		f.check(200, w)
		if n, _ := strconv.Atoi(w.Header().Get("Content-Length")); n != w.Body.Len() {
			t.Fatalf("length %d / %d", n, w.Body.Len())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("singleflight calls %d", calls.Load())
	}
	w := f.request(id, "GET", "flac", "", "bytes=0-9", true)
	f.check(206, w)
	if w.Body.Len() != 10 {
		t.Fatal("range length")
	}
	first := f.request(id, "GET", "flac", "", "", true)
	second := f.request(id, "GET", "flac", "", "", true)
	etag := first.Header().Get("ETag")
	if etag == "" || etag != second.Header().Get("ETag") || first.Header().Get("Last-Modified") != "" {
		t.Fatalf("unstable validator: %q %q", etag, second.Header().Get("ETag"))
	}
	request := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/tracks/%d/transcode.flac?mediaToken=%s", id, f.app.config.MediaToken), nil)
	request.Header.Set("Range", "bytes=10-19")
	request.Header.Set("If-Range", etag)
	resume := httptest.NewRecorder()
	f.handler.ServeHTTP(resume, request)
	f.check(206, resume)
	path := filepath.Join(f.root, "cache.flac")
	info, _ := os.Stat(path)
	stamp := info.ModTime().Add(time.Second)
	if e := os.Chtimes(path, stamp, stamp); e != nil {
		t.Fatal(e)
	}
	f.check(200, f.request(id, "GET", "flac", "", "", true))
	if calls.Load() != 2 {
		t.Fatalf("mtime calls %d", calls.Load())
	}
}
func TestTranscodeUnknownPropertiesRateAndDepth(t *testing.T) {
	f := newTranscodeFixture(t, true)
	id := f.source("unknown.flac", 96000, 24, "flac", false)
	// Force the scanner's unavailable-property fallback without modifying the actual input.
	if e := f.store.UpdateAudioProbe(context.Background(), func() int64 {
		lib, e := f.store.LibraryByRoot(context.Background(), f.root)
		if e != nil {
			t.Fatal(e)
		}
		return lib.ID
	}(), "unknown.flac", metadata.AudioProps{Codec: "flac"}, false); e != nil {
		t.Fatal(e)
	}
	w := f.request(id, "GET", "flac", "maxSampleRate=48000", "", true)
	f.check(200, w)
	v := f.inspect(w.Body.Bytes())
	if len(v.Streams) != 1 || v.Streams[0].SampleRate != "48000" || v.Streams[0].Bits != "24" {
		t.Fatalf("unknown property fallback: %+v", v.Streams)
	}
}
func TestTranscodeOversizedCacheAndMissingOpenRetry(t *testing.T) {
	f := newTranscodeFixture(t, false)
	id := f.source("oversize.flac", 44100, 16, "flac", false)
	m := f.app.transcoder
	m.limit = 1
	var calls atomic.Int32
	m.run = func(ctx context.Context, args []string, dest string) error {
		calls.Add(1)
		return m.convert(ctx, args, dest)
	}
	f.check(200, f.request(id, "GET", "flac", "", "", true))
	entries, e := os.ReadDir(m.dir)
	if e != nil || len(entries) != 1 {
		t.Fatalf("oversized result removed: %v %d", e, len(entries))
	}
	f.check(200, f.request(id, "GET", "flac", "", "", true))
	if calls.Load() != 1 {
		t.Fatal("oversized cache re-transcoded")
	}
	original := m.openCache
	var once sync.Once
	m.openCache = func(path string) (*os.File, error) {
		removed := false
		once.Do(func() { removed = true; _ = os.Remove(path) })
		if removed {
			return nil, os.ErrNotExist
		}
		return original(path)
	}
	f.check(200, f.request(id, "GET", "flac", "", "", true))
	if calls.Load() != 2 {
		t.Fatalf("missing-open retry calls %d", calls.Load())
	}
}
func TestTranscodePartCleanupAndLRU(t *testing.T) {
	f := newTranscodeFixture(t, false)
	part := filepath.Join(f.app.transcoder.dir, "orphan.part")
	if e := os.WriteFile(part, []byte("orphan"), 0600); e != nil {
		t.Fatal(e)
	}
	m := newTranscodeManager(config.Config{DataDirectory: filepath.Dir(f.app.transcoder.dir), FFmpegPath: f.ffmpeg}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, e := os.Stat(part); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("part not removed: %v", e)
	}
	m.limit = 1024 * 1024
	old := filepath.Join(m.dir, "old.flac")
	recent := filepath.Join(m.dir, "new.flac")
	payload := make([]byte, 600*1024)
	for _, path := range []string{old, recent} {
		if e := os.WriteFile(path, payload, 0600); e != nil {
			t.Fatal(e)
		}
	}
	stamp := time.Now().Add(-time.Hour)
	if e := os.Chtimes(old, stamp, stamp); e != nil {
		t.Fatal(e)
	}
	m.evict("")
	if _, e := os.Stat(old); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("old not evicted: %v", e)
	}
	if _, e := os.Stat(recent); e != nil {
		t.Fatalf("new evicted: %v", e)
	}
}
func TestTranscodeLiveMidstreamFailureAbortsConnection(t *testing.T) {
	f := newTranscodeFixture(t, false)
	id := f.source("failure.flac", 44100, 16, "flac", false)
	// Exiting immediately after a valid first chunk exercises the post-header Wait error.
	script := filepath.Join(t.TempDir(), "ffmpeg.sh")
	if runtime.GOOS == "windows" {
		t.Skip("shell fake ffmpeg unavailable on Windows; integration abort exercised on Unix")
	}
	body := "#!/bin/sh\nhead -c 4096 /dev/zero\nexit 1\n"
	if e := os.WriteFile(script, []byte(body), 0700); e != nil {
		t.Fatal(e)
	}
	f.app.transcoder.path = script
	server := httptest.NewServer(f.handler)
	defer server.Close()
	response, e := server.Client().Get(fmt.Sprintf("%s/api/v1/tracks/%d/transcode.mp3?mediaToken=%s", server.URL, id, f.app.config.MediaToken))
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("status %d", response.StatusCode)
	}
	_, e = io.ReadAll(response.Body)
	if e == nil {
		t.Fatal("truncated stream ended in clean EOF")
	}
}
func TestTranscodeClientDisconnectReapsFFmpeg(t *testing.T) {
	f := newTranscodeFixture(t, false)
	id := f.source("disconnect.flac", 44100, 16, "flac", false)
	server := httptest.NewServer(f.handler)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, e := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/v1/tracks/%d/transcode.ogg?mediaToken=%s", server.URL, id, f.app.config.MediaToken), nil)
	if e != nil {
		t.Fatal(e)
	}
	response, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	if response.StatusCode != 200 {
		t.Fatalf("status %d", response.StatusCode)
	}
	buf := make([]byte, 512)
	if _, e = response.Body.Read(buf); e != nil {
		t.Fatal(e)
	}
	if f.app.transcoder.active.Load() != 1 {
		t.Fatalf("expected active encoder, got %d", f.app.transcoder.active.Load())
	}
	cancel()
	_ = response.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for f.app.transcoder.active.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("ffmpeg still active: %d", f.app.transcoder.active.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func TestTranscodeCancelAllReapsJobs(t *testing.T) {
	f := newTranscodeFixture(t, false)
	id := f.source("shutdown.flac", 44100, 16, "flac", false)
	started := make(chan struct{})
	f.app.transcoder.run = func(ctx context.Context, args []string, dest string) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- f.request(id, "GET", "flac", "", "", true) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("job not started")
	}
	f.app.CancelTranscodes()
	f.app.WaitTranscodes()
	if f.app.transcoder.active.Load() != 0 {
		t.Fatal("active process after shutdown")
	}
	select {
	case response := <-result:
		f.check(502, response)
	case <-time.After(2 * time.Second):
		t.Fatal("request did not finish")
	}
	f.check(503, f.request(id, "GET", "flac", "", "", true))
}
func TestTranscodeCapacityAndCancel(t *testing.T) {
	f := newTranscodeFixture(t, false)
	id := f.source("capacity.flac", 44100, 16, "flac", false)
	m := f.app.transcoder
	m.live = make(chan struct{}, 1)
	m.cache = make(chan struct{}, 1)
	m.live <- struct{}{}
	w := f.request(id, "GET", "mp3", "", "", true)
	f.check(503, w)
	if w.Header().Get("Retry-After") != "2" {
		t.Fatal("missing retry-after")
	}
	<-m.live
	m.cache <- struct{}{}
	w = f.request(id, "GET", "flac", "", "", true)
	f.check(503, w)
	if w.Header().Get("Retry-After") != "2" {
		t.Fatal("missing retry-after")
	}
	<-m.cache
	f.check(200, f.request(id, "GET", "flac", "", "", true))
	m.cache <- struct{}{}
	f.check(200, f.request(id, "GET", "flac", "", "", true))
	<-m.cache
	f.check(416, f.request(id, "GET", "mp3", "", "bytes=1-", true))
	f.app.CancelTranscodes()
	f.check(503, f.request(id, "GET", "mp3", "", "", true))
	f.app.WaitTranscodes()
}
