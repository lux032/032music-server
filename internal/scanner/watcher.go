package scanner

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

// Watcher 以轮询指纹的方式监控曲库目录，发现变化且稳定后自动触发增量扫描。
//
// 不使用 inotify/fsnotify：Docker Desktop 绑定挂载的 Windows 目录、NAS 的
// SMB/NFS 挂载都不会向容器投递文件系统事件，超大曲库还会撞上 inotify watch
// 上限。轮询只做 stat（不读文件内容），对所有文件系统都可靠。
//
// 轮询间隔可在运行期通过 SetInterval 调整；间隔为 0 表示暂停监控。暂停期间
// 保留基线指纹，恢复后第一次轮询即可发现暂停期间的变化。
type Watcher struct {
	manager *Manager
	logger  *slog.Logger
	root    string

	mu       sync.Mutex
	interval time.Duration
	wake     chan struct{}

	// 测试替换点。
	fingerprint func() (libraryFingerprint, error)
	startScan   func(context.Context) error
}

// libraryFingerprint 概括增量扫描关心的全部文件状态：受支持音频与同名 .lrc
// 的相对路径、大小与修改时间。
type libraryFingerprint struct {
	files int
	hash  uint64
}

// NewWatcher 创建曲库监控器；interval 为轮询间隔，0 表示暂停。
func NewWatcher(manager *Manager, logger *slog.Logger, interval time.Duration) *Watcher {
	w := &Watcher{manager: manager, logger: logger, root: manager.library.RootPath, interval: interval, wake: make(chan struct{}, 1)}
	w.fingerprint = w.computeFingerprint
	w.startScan = func(ctx context.Context) error {
		_, err := manager.Start(ctx, "incremental")
		return err
	}
	return w
}

// Interval 返回当前轮询间隔；0 表示监控已暂停。
func (w *Watcher) Interval() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.interval
}

// SetInterval 在运行期调整轮询间隔，立即生效；0 暂停监控。
func (w *Watcher) SetInterval(interval time.Duration) {
	if interval < 0 {
		interval = 0
	}
	w.mu.Lock()
	changed := w.interval != interval
	w.interval = interval
	w.mu.Unlock()
	if !changed {
		return
	}
	if interval > 0 {
		w.logger.Info("library watcher interval updated", "interval", interval.String())
	} else {
		w.logger.Info("library watcher paused")
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start 在后台运行监控循环，ctx 取消后退出；Manager.Wait 会等待它结束。
// startupScan 为 true 时，在记录基线指纹后立即发起一次增量扫描：先记基线
// 再扫描，保证扫描期间新增的文件也会被下一轮轮询发现。
func (w *Watcher) Start(ctx context.Context, startupScan bool) {
	w.manager.wg.Add(1)
	go func() {
		defer w.manager.wg.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				w.logger.Error("panic in library watcher", "panic", recovered, "stack", string(debug.Stack()))
			}
		}()
		w.run(ctx, startupScan)
	}()
}

func (w *Watcher) run(ctx context.Context, startupScan bool) {
	baseline, err := w.fingerprint()
	haveBaseline := err == nil
	if err != nil {
		w.logger.Warn("library watcher could not read library", "root", w.root, "error", err)
	}
	if startupScan && ctx.Err() == nil {
		if err := w.startScan(ctx); err != nil {
			w.logger.Warn("automatic startup scan was not started", "error", err)
		}
	}
	if interval := w.Interval(); interval > 0 {
		w.logger.Info("library watcher started", "root", w.root, "interval", interval.String())
	} else {
		w.logger.Info("library watcher is paused", "root", w.root)
	}

	var pending *libraryFingerprint
	for {
		var tick <-chan time.Time
		var timer *time.Timer
		if interval := w.Interval(); interval > 0 {
			timer = time.NewTimer(interval)
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-w.wake:
			// 配置变更：按新间隔重新计时。
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-tick:
		}
		current, err := w.fingerprint()
		if err != nil {
			w.logger.Debug("library watcher poll failed", "root", w.root, "error", err)
			continue
		}
		if haveBaseline && current == baseline {
			pending = nil
			continue
		}
		// 变化需在连续两次轮询中保持一致才触发，避免扫描到仍在复制中的文件。
		if pending == nil || *pending != current {
			pending = &current
			w.logger.Debug("library change detected; waiting for it to settle", "files", current.files)
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if err := w.startScan(ctx); err != nil {
			if errors.Is(err, ErrScanRunning) {
				// 正在扫描（手动或上一轮），保留 pending，下次轮询重试。
				continue
			}
			w.logger.Warn("library watcher could not start scan", "error", err)
			continue
		}
		w.logger.Info("library change detected; incremental scan started", "files", current.files)
		baseline, haveBaseline, pending = current, true, nil
	}
}

// computeFingerprint 遍历曲库，跳过规则与 discover 保持一致（隐藏目录、符号
// 链接、不受支持的扩展名）。根目录不可读时返回错误，其余不可读条目跳过。
func (w *Watcher) computeFingerprint() (libraryFingerprint, error) {
	h := fnv.New64a()
	var fp libraryFingerprint
	var buf [16]byte
	err := filepath.WalkDir(w.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if path == w.root {
				return err
			}
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != w.root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !metadata.IsSupported(path) && !strings.EqualFold(filepath.Ext(path), ".lrc") {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(w.root, path)
		if relErr != nil {
			return nil
		}
		fp.files++
		_, _ = h.Write([]byte(filepath.ToSlash(rel)))
		binary.LittleEndian.PutUint64(buf[:8], uint64(info.Size()))
		binary.LittleEndian.PutUint64(buf[8:], uint64(info.ModTime().UnixNano()))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(buf[:])
		return nil
	})
	if err != nil {
		return libraryFingerprint{}, err
	}
	fp.hash = h.Sum64()
	return fp, nil
}
