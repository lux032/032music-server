package loudness

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestParseEBUR128SummaryVersionsAndEdgeCases(t *testing.T) {
	// Standard ffmpeg 6/7 output with both Summary and progress lines.
	ffmpeg7 := `
[Parsed_ebur128_0 @ 0x55] t: 210.0  TARGET:-23 LUFS  M: -9.1 S: -8.8     I:  -8.4 LUFS       LRA:  6.8 LU  FTPK: -0.2 -0.3 dBFS  TPK: +0.2 +0.1 dBFS
[Parsed_ebur128_0 @ 0x55] Summary:

  Integrated loudness:
    I:          -8.4 LUFS
    Threshold: -18.5 LUFS

  Loudness range:
    LRA:         6.8 LU
    Threshold: -28.6 LUFS
    LRA low:   -12.5 LUFS
    LRA high:   -5.7 LUFS

  True peak:
    Peak:       +0.2 dBFS
`
	m, err := ParseEBUR128Summary(ffmpeg7)
	if err != nil {
		t.Fatalf("parse ffmpeg7: %v", err)
	}
	if m.IntegratedLUFS != -8.4 || m.TruePeakDB != 0.2 || m.LoudnessRange == nil || *m.LoudnessRange != 6.8 {
		t.Fatalf("unexpected measurement: %+v", m)
	}

	// Older ffmpeg formatting with negative zero / tight spacing / missing LRA.
	ffmpegOlder := `Summary:
  Integrated loudness:
    I: -14.25 LUFS
  True peak:
    Peak: 0.0 dBFS
`
	m2, err := ParseEBUR128Summary(ffmpegOlder)
	if err != nil {
		t.Fatalf("parse older: %v", err)
	}
	if m2.IntegratedLUFS != -14.25 || m2.TruePeakDB != 0.0 || m2.LoudnessRange != nil {
		t.Fatalf("unexpected older measurement: %+v", m2)
	}

	// Pure silence (-inf LUFS) must return ErrSilentTrack.
	silent := `Summary:
  Integrated loudness:
    I:         -70.0 LUFS
  True peak:
    Peak:      -inf dBFS
`
	if _, err := ParseEBUR128Summary(silent); !errors.Is(err, ErrSilentTrack) {
		t.Fatalf("expected ErrSilentTrack for silent audio, got %v", err)
	}

	silentInf := `Summary:
  Integrated loudness:
    I:         -inf LUFS
  True peak:
    Peak:      -inf dBFS
`
	if _, err := ParseEBUR128Summary(silentInf); !errors.Is(err, ErrSilentTrack) {
		t.Fatalf("expected ErrSilentTrack for -inf audio, got %v", err)
	}

	// Truncated/corrupt stderr must return ErrDecodeFailed.
	if _, err := ParseEBUR128Summary("Invalid data found when processing input"); !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("expected ErrDecodeFailed, got %v", err)
	}
}

func TestBuildFFmpegArgsPinsSingleThread(t *testing.T) {
	args := BuildFFmpegArgs("/music/song.flac")
	var hasThreads, hasFilterThreads, hasEbur bool
	for i := 0; i < len(args); i++ {
		if args[i] == "-threads" && i+1 < len(args) && args[i+1] == "1" {
			hasThreads = true
		}
		if args[i] == "-filter_threads" && i+1 < len(args) && args[i+1] == "1" {
			hasFilterThreads = true
		}
		if args[i] == "ebur128=peak=true" {
			hasEbur = true
		}
	}
	if !hasThreads || !hasFilterThreads || !hasEbur {
		t.Fatalf("missing expected ffmpeg constraints in %v", args)
	}
}

func TestDefaultAndClampWorkers(t *testing.T) {
	if got := DefaultWorkers(4); got != 2 {
		t.Fatalf("4-core NAS should default to 2 workers, got %d", got)
	}
	if got := DefaultWorkers(2); got != 1 {
		t.Fatalf("2-core CPU should default to 1 worker, got %d", got)
	}
	if got := DefaultWorkers(16); got != 2 {
		t.Fatalf("default workers should cap at 2, got %d", got)
	}
	if got := ClampWorkers(8); got != 4 {
		t.Fatalf("explicit workers should cap at 4, got %d", got)
	}
	if got := ClampWorkers(3); got != 3 {
		t.Fatalf("explicit workers=3 should stay 3, got %d", got)
	}
}

type fakeMeasurer struct {
	available bool
	mu        sync.Mutex
	fn        func(ctx context.Context, path string) (Measurement, error)
	calls     map[string]int
}

func (f *fakeMeasurer) Available() bool { return f.available }

func (f *fakeMeasurer) Measure(ctx context.Context, path string) (Measurement, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[path]++
	fn := f.fn
	f.mu.Unlock()
	return fn(ctx, path)
}

func openLoudnessTestStore(t *testing.T) (*storage.Store, string, int64) {
	t.Helper()
	dir := t.TempDir()
	musicDir := filepath.Join(dir, "music")
	if err := os.MkdirAll(musicDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureLibrary(ctx, "Default", musicDir); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, musicDir)
	if err != nil {
		t.Fatal(err)
	}
	return s, musicDir, lib.ID
}

func importTestTrack(t *testing.T, s *storage.Store, libID int64, rel, title, album string) {
	t.Helper()
	err := s.ImportTrack(context.Background(), storage.ImportInput{
		LibraryID:    libID,
		RelativePath: rel,
		FileSize:     1000,
		ModifiedAtNS: 42,
		Metadata: metadata.AudioMetadata{
			Title:          title,
			Album:          album,
			Artists:        []string{"Artist"},
			AlbumArtists:   []string{"Artist"},
			DiscNumber:     1,
			TrackNumber:    1,
			DurationMillis: 240_000,
		},
		AudioProps: metadata.AudioProps{Codec: "flac", SampleRate: 44100, BitDepth: 16},
	})
	if err != nil {
		t.Fatalf("import %s: %v", rel, err)
	}
}

func TestServiceSchedulingYieldPauseRetryAndConcurrencyDedup(t *testing.T) {
	s, musicDir, libID := openLoudnessTestStore(t)
	importTestTrack(t, s, libID, "a/01.flac", "One", "AlbA")
	importTestTrack(t, s, libID, "a/02.flac", "Two", "AlbA")
	importTestTrack(t, s, libID, "a/03.flac", "Silent", "AlbA")

	var busy atomic.Bool
	busy.Store(true)

	silentPath := filepath.Join(musicDir, "a", "03.flac")
	var failSilent atomic.Bool
	failSilent.Store(true)

	fm := &fakeMeasurer{
		available: true,
		fn: func(_ context.Context, path string) (Measurement, error) {
			if path == silentPath && failSilent.Load() {
				return Measurement{}, ErrSilentTrack
			}
			time.Sleep(15 * time.Millisecond)
			lra := 5.0
			return Measurement{IntegratedLUFS: -9.0, TruePeakDB: -0.3, LoudnessRange: &lra}, nil
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := New(s, logger, Options{
		Measurer: fm,
		Workers:  2,
		Busy: func() string {
			if busy.Load() {
				return "曲库扫描中"
			}
			return ""
		},
	})

	ctx := context.Background()
	// Yield while busy.
	if d := svc.Step(ctx); d != yieldDelay {
		t.Fatalf("expected yieldDelay while busy, got %v", d)
	}
	snap, err := svc.Snapshot(ctx)
	if err != nil || snap.State != StateYielding {
		t.Fatalf("expected yielding snapshot, got %+v err=%v", snap, err)
	}

	// Pause takes precedence over running.
	busy.Store(false)
	if err = svc.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	if d := svc.Step(ctx); d != pausedDelay {
		t.Fatalf("expected pausedDelay, got %v", d)
	}
	if err = svc.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}

	// Run 2 concurrent workers until all 3 tracks settle (2 analyzed, 1 permanent silent failure).
	runCtx, cancel := context.WithCancel(ctx)
	svc.Start(runCtx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap, err = svc.Snapshot(ctx)
		if err == nil && snap.Stats != nil && snap.Stats.Analyzed == 2 && snap.Stats.PermanentFailures == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	svc.Wait()

	if snap.Stats == nil || snap.Stats.Analyzed != 2 || snap.Stats.PermanentFailures != 1 {
		t.Fatalf("unexpected final stats: %+v", snap.Stats)
	}
	// Each file must have been measured exactly once despite 2 concurrent workers.
	fm.mu.Lock()
	for p, count := range fm.calls {
		if count != 1 {
			t.Fatalf("expected exactly 1 call for %s, got %d", p, count)
		}
	}
	fm.mu.Unlock()

	// RetryFailures clears permanent failure so track 3 succeeds when re-enabled.
	failSilent.Store(false)
	if n, err := svc.RetryFailures(ctx); err != nil || n != 1 {
		t.Fatalf("retry failures: n=%d err=%v", n, err)
	}
	if d := svc.Step(ctx); d != 0 {
		t.Fatalf("expected immediate processing after retry, got %v", d)
	}
	snap, err = svc.Snapshot(ctx)
	if err != nil || snap.Stats == nil || snap.Stats.Analyzed != 3 || snap.Stats.PermanentFailures != 0 {
		t.Fatalf("expected all 3 analyzed after retry, got %+v err=%v", snap, err)
	}
}
