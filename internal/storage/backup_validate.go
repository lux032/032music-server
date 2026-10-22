package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type RestoreValidation struct {
	Version       int
	MissingImages int
	LibraryRoots  []string
	RootMismatch  bool
}

var schemaComments = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*(?:\n|$)`)

func (b *Backups) Validate(ctx context.Context, path string) (RestoreValidation, error) {
	var result RestoreValidation
	v, e := checkDatabase(ctx, path, "integrity_check")
	if e != nil {
		return result, e
	}
	result.Version = v
	if v < 1 || v > MaxMigrationVersion() {
		return result, fmt.Errorf("快照版本 %d 不受支持（程序最高版本 %d）", v, MaxMigrationVersion())
	}
	db, e := independentDB(path, true)
	if e != nil {
		return result, e
	}
	defer func() { db.Close(); removeSidecars(path) }()
	for _, table := range []string{"tracks", "albums", "playlists", "playlist_items", "audio_files", "libraries"} {
		var n int
		if e = db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&n); e != nil {
			return result, e
		}
		if n != 1 {
			return result, fmt.Errorf("缺少核心表 %s", table)
		}
	}
	// 使用真实迁移构建版本对应的可信 schema；不对上传库执行任何写操作。
	reference, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		return result, e
	}
	reference.SetMaxOpenConns(1)
	defer reference.Close()
	s := &Store{db: retryDB{reference}, skipMigrationTemplate: true}
	if e = s.MigrateUpTo(ctx, v); e != nil {
		return result, fmt.Errorf("构建可信 schema: %w", e)
	}
	identifiers, e := referenceSchemaIdentifiers(ctx, reference)
	if e != nil {
		return result, e
	}
	expected, e := schemaObjects(ctx, reference, nil, identifiers)
	if e != nil {
		return result, e
	}
	actual, e := schemaObjects(ctx, db, expected, identifiers)
	if e != nil {
		return result, e
	}
	if len(expected) != len(actual) {
		return result, fmt.Errorf("快照包含不可信的触发器、视图或虚拟表")
	}
	for name, query := range actual {
		if expected[name] != query {
			return result, fmt.Errorf("不可信 schema 对象: %s", name)
		}
	}
	rows, e := db.QueryContext(ctx, `SELECT root_path FROM libraries`)
	if e != nil {
		return result, e
	}
	for rows.Next() {
		var root string
		if e = rows.Scan(&root); e != nil {
			rows.Close()
			return result, e
		}
		result.LibraryRoots = append(result.LibraryRoots, root)
		if filepath.Clean(root) != filepath.Clean(b.MusicDirectory) {
			result.RootMismatch = true
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return result, e
	}
	refs, e := snapshotImageRefs(ctx, db)
	if e != nil {
		return result, e
	}
	for name := range refs {
		if _, e = os.Stat(filepath.Join(b.ImagesDirectory, name)); os.IsNotExist(e) {
			result.MissingImages++
		} else if e != nil {
			return result, e
		}
	}
	return result, nil
}

// Stage 只生成暂存文件，绝不写 armed 标记；普通重启不会触发恢复。
func (b *Backups) Stage(ctx context.Context, source string) (string, RestoreValidation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result, e := b.Validate(ctx, source)
	if e != nil {
		return "", result, e
	}
	f, e := os.CreateTemp(filepath.Dir(b.Database), "restore-staging-*.db")
	if e != nil {
		return "", result, e
	}
	path := f.Name()
	f.Close()
	if e = vacuumSnapshot(ctx, source, path); e != nil {
		os.Remove(path)
		return "", result, e
	}
	if _, e = checkDatabase(ctx, path, "quick_check"); e != nil {
		os.Remove(path)
		return "", result, e
	}
	if e = syncDirectory(filepath.Dir(path)); e != nil {
		os.Remove(path)
		return "", result, e
	}
	return filepath.Base(path), result, nil
}
func validStage(name string) bool {
	return filepath.Base(name) == name && strings.HasPrefix(name, "restore-staging-") && strings.HasSuffix(name, ".db") && !strings.ContainsAny(name, "/\\")
}
func (b *Backups) CancelStage(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !validStage(name) {
		return fmt.Errorf("invalid staging filename")
	}
	p := filepath.Join(filepath.Dir(b.Database), name)
	removeSidecars(p)
	return os.Remove(p)
}
func snapshotImageRefs(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	out := map[string]bool{}
	for _, item := range []struct{ table, query string }{{"artworks", `SELECT source_path FROM artworks WHERE source_type='custom' AND source_path IS NOT NULL`}, {"artist_custom_images", `SELECT file_path FROM artist_custom_images`}, {"playlist_custom_images", `SELECT file_path FROM playlist_custom_images`}} {
		var exists int
		if e := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, item.table).Scan(&exists); e != nil {
			return nil, e
		}
		if exists == 0 {
			continue
		}
		rows, e := db.QueryContext(ctx, item.query)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var p string
			if e = rows.Scan(&p); e != nil {
				rows.Close()
				return nil, e
			}
			if p != "" {
				out[filepath.Base(p)] = true
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

// ImageReferences 覆盖保留快照和所有暂存/待恢复库；读取出错则调用方必须停止 GC。
func (b *Backups) ImageReferences(ctx context.Context) (map[string]bool, error) {
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()
	list, e := b.List()
	if e != nil {
		return nil, e
	}
	paths := []string{}
	for _, v := range list {
		paths = append(paths, filepath.Join(b.Directory, v.Name))
	}
	for _, pattern := range []string{"restore-staging-*.db", "restore-pending.db"} {
		p, _ := filepath.Glob(filepath.Join(filepath.Dir(b.Database), pattern))
		paths = append(paths, p...)
	}
	out := map[string]bool{}
	active := map[string]bool{}
	for _, p := range paths {
		active[p] = true
		info, e := os.Stat(p)
		if e != nil {
			return nil, e
		}
		c, ok := b.cache[p]
		if !ok || !c.modified.Equal(info.ModTime()) || c.size != info.Size() {
			db, e := independentDB(p, true)
			if e != nil {
				return nil, e
			}
			refs, e := snapshotImageRefs(ctx, db)
			db.Close()
			removeSidecars(p)
			if e != nil {
				return nil, e
			}
			c = imageRefCache{info.ModTime(), info.Size(), refs}
			b.cache[p] = c
		}
		for name := range c.refs {
			out[filepath.Base(name)] = true
		}
	}
	for p := range b.cache {
		if !active[p] {
			delete(b.cache, p)
		}
	}
	return out, nil
}

func (b *Backups) ValidateStage(ctx context.Context, name string) (RestoreValidation, error) {
	if !validStage(name) {
		return RestoreValidation{}, fmt.Errorf("invalid staging filename")
	}
	return b.Validate(ctx, filepath.Join(filepath.Dir(b.Database), name))
}
