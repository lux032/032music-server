package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lux032/032music-server/internal/config"
)

type ffmpegProbeResult struct {
	version   string
	available map[string]bool
}

var ffmpegProbeCache sync.Map // executable path -> ffmpegProbeResult; avoids repeated startup probes in tests.

func probeFFmpeg(path string) ffmpegProbeResult {
	if value, ok := ffmpegProbeCache.Load(path); ok {
		return value.(ffmpegProbeResult)
	}
	result := ffmpegProbeResult{available: map[string]bool{}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	version, err := exec.CommandContext(ctx, path, "-version").Output()
	if err == nil {
		result.version = strings.SplitN(string(version), "\n", 2)[0]
		enc, err := exec.CommandContext(ctx, path, "-hide_banner", "-encoders").Output()
		if err == nil {
			for format, codec := range map[string]string{"mp3": "libmp3lame", "ogg": "libopus", "flac": "flac"} {
				for _, line := range strings.Split(string(enc), "\n") {
					fields := strings.Fields(line)
					if len(fields) > 1 && fields[1] == codec {
						result.available[format] = true
						break
					}
				}
			}
		}
	}
	actual, _ := ffmpegProbeCache.LoadOrStore(path, result)
	return actual.(ffmpegProbeResult)
}

type transcodeJob struct {
	done chan struct{}
	err  error
}
type transcodeManager struct {
	path, version, dir string
	available          map[string]bool
	live, cache        chan struct{}
	limit              int64
	ctx                context.Context
	cancel             context.CancelFunc
	mu                 sync.Mutex
	wg                 sync.WaitGroup
	active             atomic.Int32
	jobs               map[string]*transcodeJob
	access             map[string]time.Time
	openCache          func(string) (*os.File, error)
	removeCache        func(string) error
	logger             *slog.Logger
	// run is replaceable by tests; all spawned commands must be context-bound.
	run func(context.Context, []string, string) error
}

func positiveLimit(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
func newTranscodeManager(cfg config.Config, logger *slog.Logger) *transcodeManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &transcodeManager{ctx: ctx, cancel: cancel, dir: filepath.Join(cfg.DataDirectory, "transcode-cache"), available: map[string]bool{}, live: make(chan struct{}, positiveLimit(cfg.TranscodeLiveMax, 4)), cache: make(chan struct{}, positiveLimit(cfg.TranscodeCacheJobs, 2)), limit: int64(positiveLimit(cfg.TranscodeCacheMB, 4096)) * 1024 * 1024, jobs: map[string]*transcodeJob{}, access: map[string]time.Time{}, openCache: os.Open, removeCache: os.Remove, logger: logger}
	m.path = cfg.FFmpegPath
	if m.path == "" {
		m.path = "ffmpeg"
	}
	probe := probeFFmpeg(m.path)
	m.version = probe.version
	for format, available := range probe.available {
		m.available[format] = available
	}
	if err := os.MkdirAll(m.dir, 0750); err != nil {
		logger.Error("create transcode cache", "error", err)
		m.available["flac"] = false
	} else {
		entries, _ := os.ReadDir(m.dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".part") {
				_ = os.Remove(filepath.Join(m.dir, e.Name()))
			} else if strings.HasSuffix(e.Name(), ".flac") {
				if info, err := e.Info(); err == nil {
					m.access[e.Name()] = info.ModTime()
				}
			}
		}
		m.evict("")
	}
	m.run = m.convert
	return m
}
func (m *transcodeManager) CancelAll() { m.cancel() }
func (m *transcodeManager) Wait()      { m.wg.Wait() }
func (m *transcodeManager) args(path, format string, bitrate int, offset int64, rate int, depth int, lossless bool) []string {
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if offset > 0 {
		args = append(args, "-ss", fmt.Sprintf("%d.%03d", offset/1000, offset%1000))
	}
	args = append(args, "-protocol_whitelist", "file", "-i", "file:"+path, "-map", "0:a:0", "-vn", "-sn", "-dn", "-map_metadata", "-1")
	switch format {
	case "mp3":
		args = append(args, "-c:a", "libmp3lame", "-b:a", fmt.Sprintf("%dk", bitrate), "-f", "mp3", "-y", "pipe:1")
	case "ogg":
		args = append(args, "-c:a", "libopus", "-b:a", fmt.Sprintf("%dk", bitrate), "-f", "ogg", "-y", "pipe:1")
	case "flac":
		args = append(args, "-c:a", "flac")
		if !lossless || (depth > 0 && depth <= 16) {
			args = append(args, "-sample_fmt", "s16")
		} else if depth > 16 {
			args = append(args, "-sample_fmt", "s32", "-bits_per_raw_sample", "24")
		}
		if rate > 0 {
			args = append(args, "-ar", strconv.Itoa(rate))
		} else if rate < 0 {
			args = append(args, "-af", "aformat=sample_rates=44100|48000")
		}
		args = append(args, "-f", "flac", "-y")
	}
	return args
}

// stderrTail limits untrusted ffmpeg diagnostics and never contains request tokens.
type stderrTail struct{ b []byte }

func (t *stderrTail) Write(p []byte) (int, error) {
	n := len(p)
	t.b = append(t.b, p...)
	if len(t.b) > 8192 {
		t.b = append([]byte(nil), t.b[len(t.b)-8192:]...)
	}
	return n, nil
}
func (m *transcodeManager) convert(ctx context.Context, args []string, dest string) error {
	cmd := exec.CommandContext(ctx, m.path, append(args, dest)...)
	m.active.Add(1)
	defer m.active.Add(-1)
	cmd.WaitDelay = 300 * time.Millisecond
	var stderr stderrTail
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		m.logger.Error("ffmpeg transcode failed", "error", err, "stderr", string(stderr.b))
		return err
	}
	return nil
}
func (a *App) handleTranscode(w http.ResponseWriter, r *http.Request, format string) {
	m := a.transcoder
	mime := map[string]string{"mp3": "audio/mpeg", "ogg": "audio/ogg", "flac": "audio/flac"}[format]
	if m.ctx.Err() != nil || !m.available[format] {
		writeAPIError(w, 503, "transcode_unavailable", "This transcoding format is unavailable.")
		return
	}
	q := r.URL.Query()
	bitrate := 0
	allowed := map[string][]int{"mp3": {128, 192, 256, 320}, "ogg": {64, 96, 128, 160, 192, 256}}
	if format != "flac" {
		bitrate = map[string]int{"mp3": 320, "ogg": 128}[format]
	}
	if values, ok := q["bitrate"]; ok {
		if format == "flac" || len(values) != 1 {
			writeAPIError(w, 400, "invalid_request", "Invalid bitrate.")
			return
		}
		n, err := strconv.Atoi(values[0])
		valid := false
		for _, v := range allowed[format] {
			if n == v {
				valid = true
			}
		}
		if err != nil || !valid {
			writeAPIError(w, 400, "invalid_request", "Invalid bitrate.")
			return
		}
		bitrate = n
	}
	maxRate := 0
	if values, ok := q["maxSampleRate"]; ok {
		if format != "flac" || len(values) != 1 || values[0] != "48000" {
			writeAPIError(w, 400, "invalid_request", "Invalid maxSampleRate.")
			return
		}
		maxRate = 48000
	}
	offset := int64(0)
	if values, ok := q["offsetMs"]; ok {
		if format == "flac" || len(values) != 1 {
			writeAPIError(w, 400, "invalid_request", "Invalid offsetMs.")
			return
		}
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || n < 0 {
			writeAPIError(w, 400, "invalid_request", "Invalid offsetMs.")
			return
		}
		offset = n
	}
	trackID := parseInt64(r.PathValue("id"))
	path, err := a.store.AudioFilePath(r.Context(), trackID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAPIError(w, 404, "not_found", "Track not found.")
		} else {
			writeAPIError(w, 500, "query_failed", "Track lookup failed.")
		}
		return
	}
	path, err = filepath.Abs(path)
	if err != nil {
		writeAPIError(w, 500, "query_failed", "Invalid source path.")
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeAPIError(w, 404, "not_found", "Source file not found.")
		return
	}
	if offset > 0 {
		duration, _, _, _, e := a.store.TranscodeProperties(r.Context(), trackID)
		if e != nil {
			writeAPIError(w, 500, "query_failed", "Track lookup failed.")
			return
		}
		if duration > 0 && offset > duration {
			writeAPIError(w, 400, "invalid_request", "offsetMs exceeds track duration.")
			return
		}
	}
	w.Header().Set("Content-Type", mime)
	if format != "flac" {
		w.Header().Set("Accept-Ranges", "none")
		w.Header().Set("Cache-Control", "no-store")
		if h := r.Header.Get("Range"); h != "" && h != "bytes=0-" {
			w.Header().Set("Content-Range", "bytes */*")
			writeAPIError(w, 416, "range_not_satisfiable", "Use offsetMs to seek.")
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(200)
			return
		}
		select {
		case m.live <- struct{}{}:
			defer func() { <-m.live }()
			if m.ctx.Err() != nil {
				writeAPIError(w, 503, "transcode_unavailable", "Transcoder is shutting down.")
				return
			}
		default:
			busyTranscode(w)
			return
		}
		args := m.args(path, format, bitrate, offset, 0, 0, false)
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(m.ctx, cancel)
		defer stop()
		defer cancel()
		cmd := exec.CommandContext(ctx, m.path, args...)
		cmd.WaitDelay = 300 * time.Millisecond
		var stderr stderrTail
		cmd.Stderr = &stderr
		stdout, e := cmd.StdoutPipe()
		if e != nil {
			writeAPIError(w, 502, "transcode_failed", "Unable to start transcoding.")
			return
		}
		if e = cmd.Start(); e != nil {
			writeAPIError(w, 502, "transcode_failed", "Unable to start transcoding.")
			return
		}
		m.active.Add(1)
		defer m.active.Add(-1)
		type firstResult struct {
			b   []byte
			err error
		}
		first := make(chan firstResult, 1)
		go func() { b := make([]byte, 4096); n, e := io.ReadFull(stdout, b); first <- firstResult{b[:n], e} }()
		var chunk firstResult
		select {
		case chunk = <-first:
		case <-time.After(10 * time.Second):
			cancel()
			chunk = <-first
			_ = cmd.Wait()
			writeAPIError(w, 502, "transcode_failed", "Transcoder timed out.")
			return
		case <-ctx.Done():
			cancel()
			chunk = <-first
			_ = cmd.Wait()
			return
		}
		if len(chunk.b) == 0 {
			e = cmd.Wait()
			m.logger.Error("ffmpeg live output failed", "error", e, "stderr", string(stderr.b))
			writeAPIError(w, 502, "transcode_failed", "Transcoder produced no audio.")
			return
		}
		w.WriteHeader(200)
		_, copyErr := w.Write(chunk.b)
		if copyErr == nil {
			_, copyErr = io.Copy(w, stdout)
		}
		if copyErr != nil {
			cancel()
		}
		e = cmd.Wait()
		if e != nil && r.Context().Err() == nil {
			m.logger.Error("ffmpeg live stream failed", "error", e, "stderr", string(stderr.b))
			panic(http.ErrAbortHandler)
		}
		return
	}
	// Cache HEAD never starts an encoder; a preexisting file can still advertise its length.
	duration, rate, depth, lossless, err := a.store.TranscodeProperties(r.Context(), trackID)
	_ = duration
	if err != nil {
		writeAPIError(w, 500, "query_failed", "Track lookup failed.")
		return
	}
	targetRate := 0
	if maxRate > 0 && rate > 48000 {
		targetRate = 48000
		if rate%44100 == 0 {
			targetRate = 44100
		}
	}
	if maxRate > 0 && rate == 0 {
		targetRate = -1
	}
	args := m.args(path, "flac", 0, 0, targetRate, depth, lossless)
	// Include every conversion argument and the algorithm revision, not merely the query.
	keyBytes := sha256.Sum256([]byte(fmt.Sprintf("p2-v2|%d|%s|%d|%d|%s|%q", trackID, path, info.Size(), info.ModTime().UnixNano(), m.version, args)))
	key := hex.EncodeToString(keyBytes[:])
	name := filepath.Join(m.dir, key+".flac")
	w.Header().Set("ETag", "\""+key+"\"")
	if r.Method == http.MethodHead {
		if cached, e := os.Stat(name); e == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(cached.Size(), 10))
			w.Header().Set("Accept-Ranges", "bytes")
		}
		w.WriteHeader(200)
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, e := os.Stat(name); e != nil {
			m.mu.Lock()
			if _, cached := os.Stat(name); cached == nil {
				m.mu.Unlock()
			} else {
				job := m.jobs[key]
				if job == nil {
					if m.ctx.Err() != nil {
						m.mu.Unlock()
						writeAPIError(w, 503, "transcode_unavailable", "Transcoder is shutting down.")
						return
					}
					select {
					case m.cache <- struct{}{}:
						job = &transcodeJob{done: make(chan struct{})}
						m.jobs[key] = job
						m.wg.Add(1)
						go func() {
							defer m.wg.Done()
							ctx, cancel := context.WithTimeout(m.ctx, 10*time.Minute)
							defer cancel()
							part := filepath.Join(m.dir, key+".part")
							job.err = m.run(ctx, args, part)
							if job.err == nil {
								var f *os.File
								f, job.err = os.OpenFile(part, os.O_RDWR, 0)
								if job.err == nil {
									job.err = f.Sync()
									if e := f.Close(); job.err == nil {
										job.err = e
									}
								}
								if job.err == nil {
									job.err = os.Rename(part, name)
								}
							}
							if job.err != nil {
								_ = os.Remove(part)
							}
							m.mu.Lock()
							if job.err == nil {
								m.evictLocked(key + ".flac")
							}
							delete(m.jobs, key)
							close(job.done)
							m.mu.Unlock()
							<-m.cache
						}()
					default:
						m.mu.Unlock()
						busyTranscode(w)
						return
					}
				}
				m.mu.Unlock()
				select {
				case <-job.done:
					if job.err != nil {
						writeAPIError(w, 502, "transcode_failed", "Transcoding failed.")
						return
					}
				case <-r.Context().Done():
					return
				}
			}
		}
		file, e := m.openCache(name)
		if errors.Is(e, os.ErrNotExist) && attempt == 0 {
			continue
		}
		if e != nil {
			writeAPIError(w, 502, "transcode_failed", "Cached audio unavailable.")
			return
		}
		defer file.Close()
		_, e = file.Stat()
		if e != nil {
			writeAPIError(w, 502, "transcode_failed", "Cached audio unavailable.")
			return
		}
		m.mu.Lock()
		m.access[key+".flac"] = time.Now()
		m.mu.Unlock()
		w.Header().Set("Content-Type", mime)
		http.ServeContent(w, r, "audio.flac", time.Time{}, file)
		return
	}
	writeAPIError(w, 502, "transcode_failed", "Cached audio unavailable.")
}
func busyTranscode(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	writeAPIError(w, 503, "transcode_busy", "Transcoding capacity reached.")
}
func (m *transcodeManager) evict(keep string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evictLocked(keep)
}
func (m *transcodeManager) evictLocked(keep string) {
	entries, _ := os.ReadDir(m.dir)
	type item struct {
		name string
		size int64
		at   time.Time
	}
	var files []item
	var total int64
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".flac") {
			continue
		}
		info, err := e.Info()
		if err == nil {
			at, ok := m.access[e.Name()]
			if !ok {
				at = info.ModTime()
				m.access[e.Name()] = at
			}
			files = append(files, item{e.Name(), info.Size(), at})
			total += info.Size()
		}
	}
	for total > m.limit {
		old := -1
		for i := range files {
			if files[i].name != keep && (old < 0 || files[i].at.Before(files[old].at)) {
				old = i
			}
		}
		if old < 0 {
			break
		}
		f := files[old]
		files = append(files[:old], files[old+1:]...)
		if m.removeCache(filepath.Join(m.dir, f.name)) == nil {
			delete(m.access, f.name)
			total -= f.size
		} else {
			// Preserve the file and its accounted bytes; retry on a later eviction.
			m.access[f.name] = time.Now()
			break
		} // Do not delete newer entries to compensate for a locked file.
	}
}
