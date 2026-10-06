package scanner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// 变化必须在连续两次轮询中保持一致才触发扫描；扫描正在运行时保留待处理
// 变化并在下次轮询重试；与基线一致时不触发。
func TestWatcherTriggersOnlyAfterChangeSettles(t *testing.T) {
	fp := func(n int) libraryFingerprint { return libraryFingerprint{files: n, hash: uint64(n)} }
	// 下标 0 是基线。
	sequence := []libraryFingerprint{fp(1), fp(1), fp(2), fp(3), fp(3), fp(3), fp(4), fp(4), fp(4), fp(4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	calls := 0
	var scanAt []int
	busyOnce := true
	w := &Watcher{logger: logger(), interval: time.Millisecond}
	w.fingerprint = func() (libraryFingerprint, error) {
		mu.Lock()
		defer mu.Unlock()
		if calls >= len(sequence) {
			cancel()
			return sequence[len(sequence)-1], nil
		}
		calls++
		return sequence[calls-1], nil
	}
	w.startScan = func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		scanAt = append(scanAt, calls-1)
		if calls-1 == 7 && busyOnce {
			busyOnce = false
			return ErrScanRunning
		}
		return nil
	}
	done := make(chan struct{})
	go func() { w.run(ctx, false); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not stop")
	}
	if want := []int{4, 7, 8}; !reflect.DeepEqual(scanAt, want) {
		t.Fatalf("scan attempts at polls %v, want %v", scanAt, want)
	}
}

// 暂停期间保留基线：运行期开启后能发现暂停期间发生的变化。
func TestWatcherResumesFromPauseWithBaseline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	current := libraryFingerprint{files: 1, hash: 1}
	scans := make(chan struct{}, 4)
	w := &Watcher{logger: logger(), wake: make(chan struct{}, 1)}
	w.fingerprint = func() (libraryFingerprint, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, nil
	}
	w.startScan = func(context.Context) error { scans <- struct{}{}; return nil }
	done := make(chan struct{})
	go func() { w.run(ctx, false); close(done) }()

	time.Sleep(30 * time.Millisecond) // 等基线记录（暂停状态）
	mu.Lock()
	current = libraryFingerprint{files: 2, hash: 2}
	mu.Unlock()
	select {
	case <-scans:
		t.Fatal("paused watcher must not scan")
	case <-time.After(50 * time.Millisecond):
	}
	w.SetInterval(5 * time.Millisecond)
	select {
	case <-scans:
	case <-time.After(5 * time.Second):
		t.Fatal("resumed watcher did not detect the change made while paused")
	}
	cancel()
	<-done
}

func TestWatcherFingerprintTracksRelevantFiles(t *testing.T) {
	root := t.TempDir()
	m := &Manager{library: storage.Library{RootPath: root}}
	w := NewWatcher(m, logger(), time.Minute)
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustFP := func() libraryFingerprint {
		t.Helper()
		f, err := w.computeFingerprint()
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	write("Album/01.flac", "a")
	base := mustFP()
	if base.files != 1 {
		t.Fatalf("files = %d, want 1", base.files)
	}
	// 无关文件与隐藏目录不影响指纹。
	write("Album/notes.txt", "x")
	write(".hidden/02.flac", "x")
	if got := mustFP(); got != base {
		t.Fatalf("irrelevant files changed fingerprint: %+v -> %+v", base, got)
	}
	// 新增歌词、新增音频、修改内容都会改变指纹。
	write("Album/01.lrc", "[00:00.00]x")
	withLRC := mustFP()
	if withLRC == base {
		t.Fatal("adding .lrc must change fingerprint")
	}
	write("Album/02.mp3", "b")
	withMP3 := mustFP()
	if withMP3 == withLRC || withMP3.files != 3 {
		t.Fatalf("adding audio must change fingerprint: %+v", withMP3)
	}
	write("Album/02.mp3", "bigger")
	if mustFP() == withMP3 {
		t.Fatal("modifying audio must change fingerprint")
	}
	// 根目录不可读时报错，而不是返回“空曲库”。
	m2 := &Manager{library: storage.Library{RootPath: filepath.Join(root, "does-not-exist")}}
	if _, err := NewWatcher(m2, logger(), time.Minute).computeFingerprint(); err == nil {
		t.Fatal("missing root must return an error")
	}
}

// 端到端：启动扫描后新放入曲库的文件会被自动入库。
func TestWatcherImportsNewFiles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, _ := testStore(t)
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(ctx, store, logger(), library, t.TempDir())
	manager.SetMetadataReader(func(path string) (metadata.AudioMetadata, error) {
		name := filepath.Base(path)
		return metadata.AudioMetadata{Title: name, Album: "Album", Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 1, Container: "flac"}, nil
	})
	manager.probeAudio = func(string, string) metadata.AudioProps { return metadata.AudioProps{} }
	defer manager.Wait()
	defer cancel()

	NewWatcher(manager, logger(), 20*time.Millisecond).Start(ctx, true)
	// 等启动扫描结束：此时基线已记录。
	waitFor(t, func() bool {
		job, err := store.LatestScanJob(ctx)
		return err == nil && job.Status == "completed"
	})
	if err := os.MkdirAll(filepath.Join(root, "Album"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Album", "01.flac"), []byte("fLaC"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		available, err := store.CountAvailableAudioFiles(ctx, library.ID)
		return err == nil && available == 1
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}
