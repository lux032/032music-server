package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/dailymixzone"
)

var BackupFilename = regexp.MustCompile(`^music-(daily|preupgrade|manual|prerestore)-[0-9]{8}-[0-9]{6}-v[0-9]+\.db$`)

type BackupStatus struct {
	Success bool
	Time    time.Time
	Error   string
}
type BackupFile struct {
	Name     string
	Size     int64
	Modified time.Time
}
type imageRefCache struct {
	modified time.Time
	size     int64
	refs     map[string]bool
}
type Backups struct {
	Database, Directory, MusicDirectory, ImagesDirectory string
	mu                                                   sync.Mutex
	cacheMu                                              sync.Mutex
	cache                                                map[string]imageRefCache
	wg                                                   sync.WaitGroup
}

func NewBackups(database, data, music string) *Backups {
	return &Backups{Database: database, Directory: filepath.Join(data, "backups"), MusicDirectory: music, ImagesDirectory: filepath.Join(data, "custom-images"), cache: map[string]imageRefCache{}}
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	err = f.Sync()
	if runtime.GOOS == "windows" {
		return nil
	}
	return err
}
func syncFile(path string) error {
	f, e := os.OpenFile(path, os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func atomicJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	tmp := path + ".tmp"
	defer os.Remove(tmp)
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	if e = syncFile(tmp); e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	return syncDirectory(filepath.Dir(path))
}
func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func quoteSQL(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func independentDB(path string, readonly bool) (*sql.DB, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	if runtime.GOOS == "windows" {
		p = "/" + filepath.ToSlash(p)
	}
	q := url.Values{}
	if readonly {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	}
	q.Add("_pragma", "trusted_schema(OFF)")
	q.Add("_pragma", "busy_timeout(1000)")
	db, e := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}).String())
	if e == nil {
		db.SetMaxOpenConns(1)
	}
	return db, e
}
func removeSidecars(path string) { _ = os.Remove(path + "-wal"); _ = os.Remove(path + "-shm") }
func fileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (b *Backups) Initialize() error {
	if e := os.MkdirAll(filepath.Join(b.Directory, ".staging"), 0700); e != nil {
		return e
	}
	// 只清理本功能拥有的临时文件；确认过的 pending 不动。
	for _, pattern := range []string{filepath.Join(b.Directory, ".staging", "*"), filepath.Join(filepath.Dir(b.Database), "restore-staging-*"), filepath.Join(filepath.Dir(b.Database), "restore-upload-*"), filepath.Join(filepath.Dir(b.Database), "restore-*.json.tmp"), filepath.Join(filepath.Dir(b.Database), "restore-rollback-*")} {
		paths, _ := filepath.Glob(pattern)
		for _, p := range paths {
			_ = os.Remove(p)
			removeSidecars(p)
		}
	}
	if _, e := os.Stat(b.markerPath()); os.IsNotExist(e) {
		_ = os.Remove(b.pendingPath())
		removeSidecars(b.pendingPath())
	}

	return nil
}

// vacuumSnapshot 自有忙重试，每次重试从空目标开始，不经 retryDB。
func vacuumSnapshot(ctx context.Context, source, target string) (err error) {
	defer func() {
		if err != nil {
			_ = os.Remove(target)
		}
	}()
	defer func() { removeSidecars(target) }()
	db, e := independentDB(source, true)
	if e != nil {
		return e
	}
	defer db.Close()
	if _, e = db.ExecContext(ctx, "PRAGMA query_only=OFF"); e != nil {
		return e
	}
	for attempt := 0; attempt < 5; attempt++ {
		_ = os.Remove(target)
		_, e = db.ExecContext(ctx, "VACUUM INTO "+quoteSQL(target))
		if e == nil {
			if e = os.Chmod(target, 0600); e == nil {
				e = syncFile(target)
			}
			return e
		}
		_ = os.Remove(target)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !strings.Contains(strings.ToLower(e.Error()), "busy") && !strings.Contains(strings.ToLower(e.Error()), "locked") {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 100 * time.Millisecond):
		}
	}
	return e
}
func checkDatabase(ctx context.Context, path, pragma string) (int, error) {
	f, e := os.Open(path)
	if e != nil {
		return 0, e
	}
	header := make([]byte, 16)
	_, e = io.ReadFull(f, header)
	f.Close()
	if e != nil || string(header) != "SQLite format 3\x00" {
		return 0, fmt.Errorf("不是 SQLite 数据库")
	}
	db, e := independentDB(path, true)
	if e != nil {
		return 0, e
	}
	defer func() { db.Close(); removeSidecars(path) }()
	var check string
	if e = db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&check); e != nil {
		return 0, e
	}
	if check != "ok" {
		return 0, fmt.Errorf("数据库完整性检查失败: %s", check)
	}
	var v int
	e = db.QueryRowContext(ctx, `SELECT coalesce(max(version),0) FROM schema_migrations`).Scan(&v)
	return v, e
}
func (b *Backups) Create(ctx context.Context, kind string, keep int) (name string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.create(ctx, kind, keep)
}
func (b *Backups) create(ctx context.Context, kind string, keep int) (name string, err error) {
	defer func() {
		s := BackupStatus{Success: err == nil, Time: time.Now()}
		if err != nil {
			s.Error = err.Error()
		}
		if e := atomicJSON(filepath.Join(b.Directory, "status.json"), s); err == nil && e != nil {
			err = e
		}
	}()
	if !regexp.MustCompile(`^(daily|manual|preupgrade|prerestore)$`).MatchString(kind) {
		return "", fmt.Errorf("invalid backup kind")
	}
	if err = os.MkdirAll(filepath.Join(b.Directory, ".staging"), 0700); err != nil {
		return
	}
	info, e := os.Stat(b.Database)
	if e != nil {
		return "", e
	}
	free, e := backupFreeSpace(b.Directory)
	if e != nil {
		return "", e
	}
	if free < uint64(info.Size())*12/10 {
		return "", fmt.Errorf("备份磁盘剩余空间不足（需要数据库大小的 1.2 倍）")
	}
	tmp, e := os.CreateTemp(filepath.Join(b.Directory, ".staging"), "snapshot-*.tmp")
	if e != nil {
		return "", e
	}
	path := tmp.Name()
	tmp.Close()
	defer os.Remove(path)
	if err = vacuumSnapshot(ctx, b.Database, path); err != nil {
		return
	}
	v, e := checkDatabase(ctx, path, "quick_check")
	if e != nil {
		return "", e
	}
	name = fmt.Sprintf("music-%s-%s-v%d.db", kind, time.Now().In(dailymixzone.Zone).Format("20060102-150405"), v)
	dest := filepath.Join(b.Directory, name)
	if _, e = os.Stat(dest); e == nil {
		return "", fmt.Errorf("同一秒已有此类快照，请稍后重试")
	}
	if err = os.Rename(path, dest); err != nil {
		return
	}
	if err = syncDirectory(b.Directory); err != nil {
		return
	}
	if kind != "manual" {
		err = b.rotate(kind, keep)
	}
	return
}
func (b *Backups) List() ([]BackupFile, error) {
	entries, e := os.ReadDir(b.Directory)
	if e != nil {
		return nil, e
	}
	out := []BackupFile{}
	for _, v := range entries {
		if !v.IsDir() && BackupFilename.MatchString(v.Name()) {
			i, e := v.Info()
			if e != nil {
				return nil, e
			}
			out = append(out, BackupFile{v.Name(), i.Size(), i.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}
func (b *Backups) rotate(kind string, keep int) error {
	list, e := b.List()
	if e != nil {
		return e
	}
	n := 0
	for _, v := range list {
		if strings.HasPrefix(v.Name, "music-"+kind+"-") {
			n++
			if n > keep {
				if e = os.Remove(filepath.Join(b.Directory, v.Name)); e != nil {
					return e
				}
			}
		}
	}
	return syncDirectory(b.Directory)
}
func (b *Backups) Delete(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !BackupFilename.MatchString(name) {
		return fmt.Errorf("invalid backup filename")
	}
	e := os.Remove(filepath.Join(b.Directory, name))
	if e != nil {
		return e
	}
	return syncDirectory(b.Directory)
}
func (b *Backups) Status() BackupStatus {
	var s BackupStatus
	_ = readJSON(filepath.Join(b.Directory, "status.json"), &s)
	return s
}
func (b *Backups) Due(now time.Time, v BackupSettings) bool {
	if !v.Enabled {
		return false
	}
	local := now.In(dailymixzone.Zone)
	if local.Format("15:04") < v.Time {
		return false
	}
	list, e := b.List()
	if e != nil {
		return false
	}
	prefix := "music-daily-" + local.Format("20060102") + "-"
	for _, f := range list {
		if strings.HasPrefix(f.Name, prefix) {
			return false
		}
	}
	return true
}
func (b *Backups) Start(ctx context.Context, settings func(context.Context) (BackupSettings, error)) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		var next time.Time
		delay := time.Minute
		for {
			v, e := settings(ctx)
			if e == nil && !time.Now().Before(next) && b.Due(time.Now(), v) {
				if _, err := b.Create(ctx, "daily", v.KeepDaily); err != nil {
					delay *= 2
					if delay > time.Hour {
						delay = time.Hour
					}
					next = time.Now().Add(delay)
				} else {
					delay = time.Minute
					next = time.Time{}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
}
func (b *Backups) Wait() { b.wg.Wait() }
