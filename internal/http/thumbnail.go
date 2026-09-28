package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/config"
)

const (
	thumbnailVersion       = "box-white-v2"
	thumbnailMaxPixels     = 24_000_000
	thumbnailMaxBytes      = 256 << 20
	thumbnailNegativeLimit = 4096
)

type thumbnailJob struct {
	done     chan struct{}
	err      error
	original bool
}
type thumbnailFile struct {
	size int64
	at   time.Time
}
type thumbnailManager struct {
	dir          string
	limit, total int64
	slots        chan struct{}
	mu           sync.Mutex
	jobs         map[string]*thumbnailJob
	files        map[string]thumbnailFile
	negative     map[string]time.Time
	decodeConfig func(*os.File) (image.Config, string, error)
	decode       func(*os.File) (image.Image, string, error)
	// openCache allows tests to simulate removal between cache lookup and open.
	openCache   func(string) (*os.File, error)
	removeCache func(string) error
}

func newThumbnailManager(cfg config.Config) *thumbnailManager {
	m := &thumbnailManager{dir: filepath.Join(cfg.DataDirectory, "thumbs"), limit: int64(positiveLimit(cfg.ThumbCacheMB, 512)) * 1024 * 1024, slots: make(chan struct{}, 2), jobs: make(map[string]*thumbnailJob), files: make(map[string]thumbnailFile), negative: make(map[string]time.Time), decodeConfig: func(f *os.File) (image.Config, string, error) { return image.DecodeConfig(f) }, decode: func(f *os.File) (image.Image, string, error) { return image.Decode(f) }, openCache: os.Open, removeCache: os.Remove}
	if err := os.MkdirAll(m.dir, 0750); err == nil {
		entries, _ := os.ReadDir(m.dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".part") {
				_ = os.Remove(filepath.Join(m.dir, e.Name()))
			} else if strings.HasSuffix(e.Name(), ".jpg") {
				if info, err := e.Info(); err == nil {
					m.files[e.Name()] = thumbnailFile{info.Size(), info.ModTime()}
					m.total += info.Size()
				}
			}
		}
		m.mu.Lock()
		m.evictLocked("")
		m.mu.Unlock()
	}
	return m
}
func thumbnailSize(r *http.Request) (int, error) {
	values, ok := r.URL.Query()["size"]
	if !ok {
		return 0, nil
	}
	if len(values) != 1 || values[0] == "" {
		return 0, fmt.Errorf("invalid size")
	}
	for _, c := range values[0] {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid size")
		}
	}
	n, err := strconv.ParseUint(values[0], 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("invalid size")
	}
	for _, b := range []int{256, 512, 768, 1024, 1536} {
		if n <= uint64(b) {
			return b, nil
		}
	}
	return 1536, nil
}
func (m *thumbnailManager) key(source string, id int64, path string, info os.FileInfo, bucket int) (string, string) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s|%d|%d|%d|%s", source, id, absolute, info.Size(), info.ModTime().UnixNano(), bucket, thumbnailVersion)))
	key := hex.EncodeToString(sum[:])
	return key, filepath.Join(m.dir, key+"-"+strconv.Itoa(bucket)+".jpg")
}

// thumbnail coalesces work by key. A cancelled leader never poisons a live follower.
func (m *thumbnailManager) thumbnail(ctx context.Context, source string, id int64, file *os.File, info os.FileInfo, bucket int) (key, name string, ok bool, err error) {
	key, name = m.key(source, id, file.Name(), info, bucket)
	for attempt := 0; attempt < 2; attempt++ {
		m.mu.Lock()
		if _, exists := m.files[filepath.Base(name)]; exists {
			m.mu.Unlock()
			if _, e := os.Stat(name); e == nil {
				m.touch(filepath.Base(name))
				return key, name, true, nil
			}
			m.mu.Lock()
			m.removeFileLocked(filepath.Base(name))
			m.mu.Unlock()
		} else {
			m.mu.Unlock()
		}
		m.mu.Lock()
		if _, exists := m.negative[key]; exists {
			m.negative[key] = time.Now()
			m.mu.Unlock()
			return key, name, false, nil
		}
		job := m.jobs[key]
		if job == nil {
			job = &thumbnailJob{done: make(chan struct{})}
			m.jobs[key] = job
			m.mu.Unlock()
			m.lead(ctx, key, name, file, bucket, job)
		} else {
			m.mu.Unlock()
			select {
			case <-job.done:
			case <-ctx.Done():
				return key, name, false, ctx.Err()
			}
		}
		if (errors.Is(job.err, context.Canceled) || errors.Is(job.err, context.DeadlineExceeded)) && ctx.Err() == nil && attempt == 0 {
			continue
		}
		return key, name, !job.original && job.err == nil, job.err
	}
	return key, name, false, ctx.Err()
}
func (m *thumbnailManager) lead(ctx context.Context, key, name string, file *os.File, bucket int, job *thumbnailJob) {
	defer func() {
		if p := recover(); p != nil {
			job.original = true
			job.err = fmt.Errorf("thumbnail panic: %v", p)
		}
		m.mu.Lock()
		if job.original {
			if job.err == nil || (job.err != nil && !errors.Is(job.err, context.Canceled) && !errors.Is(job.err, context.DeadlineExceeded)) {
				m.negative[key] = time.Now()
				if len(m.negative) > thumbnailNegativeLimit {
					var oldest string
					var at time.Time
					for k, v := range m.negative {
						if oldest == "" || v.Before(at) {
							oldest, at = k, v
						}
					}
					delete(m.negative, oldest)
				}
			}
		} else if job.err == nil {
			if info, err := os.Stat(name); err == nil {
				m.addFileLocked(filepath.Base(name), info.Size())
				m.evictLocked(filepath.Base(name))
			}
		}
		delete(m.jobs, key)
		close(job.done)
		m.mu.Unlock()
	}()
	job.original, job.err = m.generate(ctx, file, name, bucket)
}
func (m *thumbnailManager) generate(ctx context.Context, file *os.File, name string, bucket int) (original bool, err error) {
	if _, err = file.Seek(0, 0); err != nil {
		return false, err
	}
	cfg, format, err := m.decodeConfig(file)
	if err != nil {
		return thumbnailFormatError(err), err
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	// WebP (x/image) decodes to YCbCr/NYCbCrA/NRGBA; sourceRGBA handles
	// YCbCr directly and everything else through the At() fallback.
	if format != "jpeg" && format != "png" && format != "gif" && format != "webp" {
		return true, nil
	}
	pixels := int64(cfg.Width) * int64(cfg.Height)
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 12000 || cfg.Height > 12000 || pixels > thumbnailMaxPixels || pixels*thumbnailBytesPerPixel(cfg.ColorModel)+thumbnailCanvasBytes(format, pixels) > thumbnailMaxBytes {
		return true, nil
	}
	if cfg.Width <= bucket && cfg.Height <= bucket {
		return true, nil
	}
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		return false, ctx.Err()
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if _, err = file.Seek(0, 0); err != nil {
		return false, err
	}
	img, _, err := m.decode(file)
	if err != nil {
		return true, err // Decode failures are logged once and negative-cached by source key.
	}
	if format == "gif" {
		b := img.Bounds()
		if b.Min.X < 0 || b.Min.Y < 0 || b.Max.X > cfg.Width || b.Max.Y > cfg.Height {
			return true, nil
		}
		canvas := image.NewRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
		draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		draw.Draw(canvas, b, img, b.Min, draw.Over)
		img = canvas
	} else if img.Bounds().Dx() != cfg.Width || img.Bounds().Dy() != cfg.Height {
		return true, nil
	}
	width, height := cfg.Width, cfg.Height
	if width >= height {
		height = int(math.Round(float64(height) * float64(bucket) / float64(width)))
		width = bucket
	} else {
		width = int(math.Round(float64(width) * float64(bucket) / float64(height)))
		height = bucket
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	dest := image.NewRGBA(image.Rect(0, 0, width, height))
	boxThumbnail(ctx, dest, img)
	if err = ctx.Err(); err != nil {
		return false, err
	}
	part := strings.TrimSuffix(name, ".jpg") + ".part"
	out, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0640)
	if err != nil {
		return false, err
	}
	defer os.Remove(part)
	defer out.Close() // Close before removing .part even when jpeg.Encode panics.
	err = jpeg.Encode(out, dest, &jpeg.Options{Quality: 85})
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return false, err
	}
	return false, os.Rename(part, name)
}
func thumbnailCanvasBytes(format string, pixels int64) int64 {
	if format == "gif" {
		return pixels * 4
	}
	return 0
}

// Only malformed/unsupported image data is stable for a source key; transient
// reader failures must be retried on the next request.
func thumbnailFormatError(err error) bool {
	if errors.Is(err, image.ErrFormat) {
		return true
	}
	var jpegFormat jpeg.FormatError
	var jpegUnsupported jpeg.UnsupportedError
	var pngFormat png.FormatError
	var pngUnsupported png.UnsupportedError
	if errors.As(err, &jpegFormat) || errors.As(err, &jpegUnsupported) || errors.As(err, &pngFormat) || errors.As(err, &pngUnsupported) {
		return true
	}
	// GIF reports malformed headers with plain errors; read failures can also
	// be wrapped in text, so leave those retryable rather than caching them.
	return strings.HasPrefix(err.Error(), "gif: invalid") || strings.HasPrefix(err.Error(), "gif: can't recognize") || strings.HasPrefix(err.Error(), "gif: unknown")
}

func thumbnailBytesPerPixel(model color.Model) int64 {
	if _, ok := model.(color.Palette); ok {
		return 1
	}
	switch model {
	case color.RGBA64Model, color.NRGBA64Model, color.Gray16Model:
		return 8
	case color.CMYKModel, color.RGBAModel, color.NRGBAModel:
		return 4
	case color.YCbCrModel:
		return 3
	case color.GrayModel:
		return 1
	default:
		return 4
	}
}

// sourceRGBA returns premultiplied 16-bit components, avoiding interface boxing on common formats.
func sourceRGBA(img image.Image, x, y int) (uint32, uint32, uint32, uint32) {
	switch im := img.(type) {
	case *image.RGBA:
		i := im.PixOffset(x, y)
		p := im.Pix
		return uint32(p[i]) * 257, uint32(p[i+1]) * 257, uint32(p[i+2]) * 257, uint32(p[i+3]) * 257
	case *image.NRGBA:
		i := im.PixOffset(x, y)
		p := im.Pix
		a := uint32(p[i+3]) * 257
		return uint32(p[i]) * a / 255, uint32(p[i+1]) * a / 255, uint32(p[i+2]) * a / 255, a
	case *image.YCbCr:
		y8 := im.Y[im.YOffset(x, y)]
		ci := im.COffset(x, y)
		return (color.YCbCr{Y: y8, Cb: im.Cb[ci], Cr: im.Cr[ci]}).RGBA()
	default:
		return img.At(x, y).RGBA()
	}
}
func boxThumbnail(ctx context.Context, dest *image.RGBA, img image.Image) {
	bounds := img.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	dw, dh := dest.Bounds().Dx(), dest.Bounds().Dy()
	for y := 0; y < dh; y++ {
		if ctx.Err() != nil {
			return
		}
		top := float64(y) * float64(sh) / float64(dh)
		bottom := float64(y+1) * float64(sh) / float64(dh)
		for x := 0; x < dw; x++ {
			left := float64(x) * float64(sw) / float64(dw)
			right := float64(x+1) * float64(sw) / float64(dw)
			var sums [3]float64
			for sy := int(top); sy < int(math.Ceil(bottom)); sy++ {
				wy := math.Min(float64(sy+1), bottom) - math.Max(float64(sy), top)
				for sx := int(left); sx < int(math.Ceil(right)); sx++ {
					wx := math.Min(float64(sx+1), right) - math.Max(float64(sx), left)
					r, g, b, a := sourceRGBA(img, bounds.Min.X+sx, bounds.Min.Y+sy)
					w := wx * wy
					sums[0] += w * float64(r+65535-a)
					sums[1] += w * float64(g+65535-a)
					sums[2] += w * float64(b+65535-a)
				}
			}
			i := dest.PixOffset(x, y)
			area := (right - left) * (bottom - top)
			for c := 0; c < 3; c++ {
				dest.Pix[i+c] = uint8(math.Round(sums[c] / area / 257))
			}
			dest.Pix[i+3] = 255
		}
	}
}
func (a *App) serveThumbnail(w http.ResponseWriter, r *http.Request, file *os.File, info os.FileInfo, source string, id int64, bucket int, cacheControl string) bool {
	key, name := a.thumbnails.key(source, id, file.Name(), info, bucket)
	for attempt := 0; attempt < 3; attempt++ {
		cached, err := a.thumbnails.openCache(name)
		if err == nil {
			defer cached.Close()
			a.thumbnails.touch(filepath.Base(name))
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("Cache-Control", cacheControl)
			w.Header().Set("ETag", "\""+key+"\"")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			http.ServeContent(w, r, "thumbnail.jpg", time.Time{}, cached)
			return true
		}
		if r.Method == http.MethodHead {
			return false
		}
		if !errors.Is(err, os.ErrNotExist) {
			a.logger.Warn("thumbnail cache open failed; serving original", "error", err)
			return false
		}
		if attempt == 2 {
			break
		}
		// An open failure may have raced with eviction. Forget the stale index before regeneration.
		a.thumbnails.mu.Lock()
		a.thumbnails.removeFileLocked(filepath.Base(name))
		a.thumbnails.mu.Unlock()
		_, _, ok, e := a.thumbnails.thumbnail(r.Context(), source, id, file, info, bucket)
		if e != nil {
			if r.Context().Err() == nil {
				a.logger.Warn("thumbnail generation failed; serving original", "path", file.Name(), "error", e)
			}
			return false
		}
		if !ok {
			return false
		}
	}
	return false
}
func (m *thumbnailManager) touch(name string) {
	m.mu.Lock()
	if v, ok := m.files[name]; ok {
		v.at = time.Now()
		m.files[name] = v
	}
	m.mu.Unlock()
}
func (m *thumbnailManager) removeFileLocked(name string) {
	if v, ok := m.files[name]; ok {
		m.total -= v.size
		delete(m.files, name)
	}
}
func (m *thumbnailManager) addFileLocked(name string, size int64) {
	m.removeFileLocked(name)
	m.files[name] = thumbnailFile{size, time.Now()}
	m.total += size
}
func (m *thumbnailManager) evictLocked(keep string) {
	for m.total > m.limit {
		old := ""
		var at time.Time
		for name, v := range m.files {
			if name != keep && (old == "" || v.at.Before(at)) {
				old, at = name, v.at
			}
		}
		if old == "" {
			break
		}
		if err := m.removeCache(filepath.Join(m.dir, old)); err != nil {
			// Retain the indexed bytes; refresh access and stop rather than delete newer files.
			v := m.files[old]
			v.at = time.Now()
			m.files[old] = v
			break
		}
		m.removeFileLocked(old)
	}
}
