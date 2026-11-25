// Package loudness measures per-track EBU R128 integrated loudness, true peak,
// and loudness range in the background using ffmpeg's built-in ebur128 filter.
package loudness

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	measureTimeout   = 5 * time.Minute
	stderrTailLimit  = 64 << 10
	minValidLUFS     = -70.0
	maxValidLUFS     = 10.0
	minValidTruePeak = -120.0
	maxValidTruePeak = 24.0
)

var (
	// ErrSilentTrack indicates silence or non-finite integrated loudness (permanent).
	ErrSilentTrack = errors.New("silent or unmeasurable audio loudness")
	// ErrDecodeFailed indicates ffmpeg could not decode or measure the file (permanent).
	ErrDecodeFailed = errors.New("ffmpeg loudness decode failed")
)

// Measurement is the parsed EBU R128 summary for one audio file.
type Measurement struct {
	IntegratedLUFS float64
	TruePeakDB     float64
	LoudnessRange  *float64
}

// Measurer measures EBU R128 loudness for one audio file path.
type Measurer interface {
	Available() bool
	Measure(ctx context.Context, path string) (Measurement, error)
}

var ebur128ProbeCache sync.Map // executable path -> bool

func probeFFmpegEbur128(path string) bool {
	if cached, ok := ebur128ProbeCache.Load(path); ok {
		return cached.(bool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-hide_banner", "-filters").Output()
	supported := false
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[1] == "ebur128" {
				supported = true
				break
			}
		}
	}
	actual, _ := ebur128ProbeCache.LoadOrStore(path, supported)
	return actual.(bool)
}

// FFmpegMeasurer runs `ffmpeg -threads 1 -filter_threads 1 ... -filter:a ebur128=peak=true`.
type FFmpegMeasurer struct {
	path      string
	available bool
}

// NewFFmpegMeasurer probes `ffmpegPath` (default "ffmpeg") for the ebur128 filter.
func NewFFmpegMeasurer(ffmpegPath string) *FFmpegMeasurer {
	if strings.TrimSpace(ffmpegPath) == "" {
		ffmpegPath = "ffmpeg"
	}
	return &FFmpegMeasurer{
		path:      ffmpegPath,
		available: probeFFmpegEbur128(ffmpegPath),
	}
}

func (m *FFmpegMeasurer) Available() bool { return m != nil && m.available }

// tailWriter retains only the last `limit` bytes written to it so long tracks
// with frequent ebur128 progress lines cannot grow memory unboundedly.
type tailWriter struct {
	limit int
	buf   []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	if len(p) >= w.limit {
		w.buf = append(w.buf[:0], p[len(p)-w.limit:]...)
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	if excess := len(w.buf) - w.limit; excess > 0 {
		copy(w.buf, w.buf[excess:])
		w.buf = w.buf[:w.limit]
	}
	return len(p), nil
}

// BuildFFmpegArgs returns the exact CLI arguments used to measure one file.
func BuildFFmpegArgs(path string) []string {
	return []string{
		"-nostdin",
		"-hide_banner",
		"-threads", "1",
		"-filter_threads", "1",
		"-i", path,
		"-map", "0:a:0",
		"-filter:a", "ebur128=peak=true",
		"-f", "null",
		"-",
	}
}

func (m *FFmpegMeasurer) Measure(ctx context.Context, path string) (Measurement, error) {
	if !m.Available() {
		return Measurement{}, errors.New("ffmpeg ebur128 filter is unavailable")
	}
	if _, err := os.Stat(path); err != nil {
		return Measurement{}, err
	}
	tctx, cancel := context.WithTimeout(ctx, measureTimeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, m.path, BuildFFmpegArgs(path)...)
	stderr := &tailWriter{limit: stderrTailLimit}
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return Measurement{}, ctx.Err()
	}
	if tctx.Err() != nil {
		return Measurement{}, fmt.Errorf("loudness measurement timed out: %w", tctx.Err())
	}
	if err != nil {
		tail := strings.TrimSpace(string(stderr.buf))
		if len(tail) > 240 {
			tail = tail[len(tail)-240:]
		}
		return Measurement{}, fmt.Errorf("%w: %v (%s)", ErrDecodeFailed, err, tail)
	}
	return ParseEBUR128Summary(string(stderr.buf))
}

var (
	reIntegrated = regexp.MustCompile(`(?i)\bI:\s*([+-]?(?:\d+(?:\.\d+)?|inf)|nan)\s*LUFS\b`)
	reLRA        = regexp.MustCompile(`(?i)\bLRA:\s*([+-]?(?:\d+(?:\.\d+)?|inf)|nan)\s*LU\b`)
	rePeak       = regexp.MustCompile(`(?i)\bPeak:\s*([+-]?(?:\d+(?:\.\d+)?|inf)|nan)\s*dBFS\b`)
)

func parseFloatToken(token string) (float64, bool) {
	lower := strings.ToLower(strings.TrimSpace(token))
	if lower == "nan" || lower == "inf" || lower == "+inf" || lower == "-inf" {
		return 0, false
	}
	v, err := strconv.ParseFloat(lower, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// ParseEBUR128Summary extracts Integrated Loudness (LUFS), True Peak (dBFS/dBTP),
// and optional Loudness Range (LU) from ffmpeg's ebur128 stderr output.
func ParseEBUR128Summary(stderr string) (Measurement, error) {
	section := stderr
	if idx := strings.LastIndex(strings.ToLower(stderr), "summary:"); idx >= 0 {
		section = stderr[idx:]
	}
	mI := reIntegrated.FindStringSubmatch(section)
	if len(mI) < 2 {
		return Measurement{}, fmt.Errorf("%w: missing integrated loudness summary", ErrDecodeFailed)
	}
	lufs, ok := parseFloatToken(mI[1])
	if !ok || lufs <= minValidLUFS || lufs > maxValidLUFS {
		return Measurement{}, fmt.Errorf("%w: integrated=%s", ErrSilentTrack, mI[1])
	}

	// Prefer the Peak value in the "True peak:" subsection when present.
	peakSection := section
	if tpIdx := strings.Index(strings.ToLower(section), "true peak:"); tpIdx >= 0 {
		peakSection = section[tpIdx:]
	}
	mPeak := rePeak.FindStringSubmatch(peakSection)
	if len(mPeak) < 2 {
		mPeak = rePeak.FindStringSubmatch(section)
	}
	if len(mPeak) < 2 {
		return Measurement{}, fmt.Errorf("%w: missing true peak summary", ErrDecodeFailed)
	}
	peak, ok := parseFloatToken(mPeak[1])
	if !ok || peak < minValidTruePeak || peak > maxValidTruePeak {
		return Measurement{}, fmt.Errorf("%w: peak=%s", ErrSilentTrack, mPeak[1])
	}

	res := Measurement{
		IntegratedLUFS: math.Round(lufs*100) / 100,
		TruePeakDB:     math.Round(peak*100) / 100,
	}
	if mLRA := reLRA.FindStringSubmatch(section); len(mLRA) >= 2 {
		if lra, ok := parseFloatToken(mLRA[1]); ok && lra >= 0 && lra <= 100 {
			rounded := math.Round(lra*100) / 100
			res.LoudnessRange = &rounded
		}
	}
	return res, nil
}
