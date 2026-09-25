package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestTranscodeRoutesAndHead(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	app, store, _ := setupTestApp(t)
	app.transcoder.path = ffmpeg
	app.transcoder.dir = filepath.Join(t.TempDir(), "transcode-cache")
	if err := os.MkdirAll(app.transcoder.dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"mp3", "ogg", "flac"} {
		app.transcoder.available[format] = true
	}
	root := t.TempDir()
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "song.flac")
	command := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=96000:duration=2", "-c:a", "flac", "-sample_fmt", "s32", "-bits_per_raw_sample", "24", "-y", path)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v %s", err, out)
	}
	info, _ := os.Stat(path)
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: info.Size(), ModifiedAtNS: info.ModTime().UnixNano(), Metadata: metadata.AudioMetadata{Title: "Test", Album: "Album", Artists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 2000}, AudioProps: metadata.AudioProps{Codec: "flac", SampleRate: 96000, BitDepth: 24}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := tracks.Items[0].ID
	var calls atomic.Int32
	app.transcoder.run = func(ctx context.Context, args []string, dest string) error {
		calls.Add(1)
		return app.transcoder.convert(ctx, args, dest)
	}
	handler := app.Handler() // The literal suffix patterns must not panic ServeMux.
	request := func(method, format, query, rng string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, fmt.Sprintf("/api/v1/tracks/%d/transcode.%s?mediaToken=%s%s", id, format, app.config.MediaToken, query), nil)
		r.Header.Set("Range", rng)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, format := range []string{"mp3", "ogg", "flac"} {
		w := request("HEAD", format, "", "")
		if w.Code != 200 {
			t.Errorf("HEAD %s: %d %s", format, w.Code, w.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatal("HEAD started ffmpeg")
	}
	if w := request("GET", "mp3", "", "bytes=1-"); w.Code != 416 {
		t.Fatalf("range: %d", w.Code)
	}
	if w := request("GET", "mp3", "&bitrate=3", ""); w.Code != 400 {
		t.Fatalf("bitrate: %d", w.Code)
	}
	if w := request("GET", "ogg", "&offsetMs=3000", ""); w.Code != 400 {
		t.Fatalf("offset: %d", w.Code)
	}
	if w := request("GET", "flac", "&offsetMs=0", ""); w.Code != 400 {
		t.Fatalf("flac offset: %d", w.Code)
	}
	for _, format := range []string{"mp3", "ogg"} {
		w := request("GET", format, "&offsetMs=500", "")
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Errorf("live %s: %d %s", format, w.Code, w.Body.String())
		}
		probe, probeErr := exec.LookPath("ffprobe")
		if probeErr == nil && w.Code == 200 {
			cmd := exec.Command(probe, "-v", "error", "-show_entries", "stream=codec_type", "-show_entries", "format=duration", "-of", "default=nw=1", "-i", "pipe:0")
			cmd.Stdin = bytes.NewReader(w.Body.Bytes())
			out, e := cmd.CombinedOutput()
			if e != nil || strings.Count(string(out), "codec_type=audio") != 1 || strings.Contains(string(out), "codec_type=video") {
				t.Errorf("ffprobe %s: %s (%v)", format, out, e)
			}
		}
	}
	w := request("GET", "flac", "&maxSampleRate=48000", "")
	if w.Code != 200 || w.Header().Get("Content-Length") == "" {
		t.Fatalf("flac: %d %s", w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("cache transcodes: %d", calls.Load())
	}
	w = request("GET", "flac", "&maxSampleRate=48000", "bytes=0-9")
	if w.Code != 206 || w.Body.Len() != 10 {
		t.Fatalf("flac range: %d %d", w.Code, w.Body.Len())
	}
	w = request("HEAD", "flac", "&maxSampleRate=48000", "")
	if w.Code != 200 || w.Header().Get("Content-Length") == "" {
		t.Fatalf("cached HEAD: %d", w.Code)
	}
	if calls.Load() != 1 {
		t.Fatal("cache hit started ffmpeg")
	}
	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := request("GET", "flac", "", "")
			if response.Code != 200 {
				t.Errorf("concurrent cache response: %d", response.Code)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("singleflight jobs: %d", calls.Load())
	}
	changed := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	if response := request("GET", "flac", "", ""); response.Code != 200 || calls.Load() != 3 {
		t.Fatalf("mtime invalidation: %d, %d", response.Code, calls.Load())
	}
	app.CancelTranscodes()
	// No request should remain attached to a canceled background job.
	select {
	case <-app.transcoder.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("manager did not cancel")
	}
}
