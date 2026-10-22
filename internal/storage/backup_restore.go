package storage

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type RestoreState struct {
	RollingBack bool
	State       string
	SHA256      string
	Prerestore  string
	Finalized   bool
	Success     bool
	Error       string
	Time        time.Time
}

func (b *Backups) markerPath() string {
	return filepath.Join(filepath.Dir(b.Database), "restore-state.json")
}
func (b *Backups) resultPath() string {
	return filepath.Join(filepath.Dir(b.Database), "restore-result.json")
}
func (b *Backups) pendingPath() string {
	return filepath.Join(filepath.Dir(b.Database), "restore-pending.db")
}
func (b *Backups) Result() RestoreState {
	var s RestoreState
	_ = readJSON(b.resultPath(), &s)
	return s
}
func (b *Backups) Arm(ctx context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !validStage(name) {
		return fmt.Errorf("invalid staging filename")
	}
	if _, e := os.Stat(b.markerPath()); e == nil {
		return fmt.Errorf("已有待恢复任务")
	} else if !os.IsNotExist(e) {
		return e
	}
	source := filepath.Join(filepath.Dir(b.Database), name)
	if _, e := b.Validate(ctx, source); e != nil {
		return e
	}
	hash, e := fileHash(source)
	if e != nil {
		return e
	}
	if e = os.Rename(source, b.pendingPath()); e != nil {
		return e
	}
	if e = syncDirectory(filepath.Dir(b.Database)); e != nil {
		return e
	}
	return atomicJSON(b.markerPath(), RestoreState{State: "armed", SHA256: hash, Time: time.Now()})
}
func (b *Backups) finishResult(s RestoreState) error {
	if e := atomicJSON(b.resultPath(), s); e != nil {
		return e
	}
	if e := os.Remove(b.markerPath()); e != nil && !os.IsNotExist(e) {
		return e
	}
	return syncDirectory(filepath.Dir(b.Database))
}
func (b *Backups) reject(s RestoreState, cause error) error {
	s.State = "done"
	s.Success = false
	s.Error = cause.Error()
	s.Time = time.Now()
	_ = os.Remove(b.pendingPath())
	return b.finishResult(s)
}

// PrepareRestore 在 storage.Open 之前运行；B1 的原库快照完成前绝不触碰 WAL。
func (b *Backups) PrepareRestore(ctx context.Context) (restored bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var s RestoreState
	// 未确认换库前失败可以安全 reject；发生/无法判定换库后只能隔离。
	uncertain := false
	rollbackHandled := false
	defer func() {
		if err == nil || ctx.Err() != nil || rollbackHandled {
			return
		}
		if uncertain {
			err = b.RequireManualRecovery("prepare", err)
		} else {
			if rejectionErr := b.reject(s, err); rejectionErr != nil {
				err = b.RequireManualRecovery("reject", rejectionErr)
			} else {
				err = nil
			}
		}
	}()
	if e := ctx.Err(); e != nil {
		return false, e
	}
	if e := readJSON(b.markerPath(), &s); os.IsNotExist(e) {
		return false, nil
	} else if e != nil {
		uncertain = true
		return false, e
	}
	if s.RollingBack {
		uncertain = true
		rollbackHandled = true
		if e := b.RollbackRestore(context.Background(), fmt.Errorf("%s", s.Error)); e != nil {
			return false, e
		}
		return false, nil
	}
	if s.State == "done" {
		uncertain = true
		return s.Success, b.finishResult(s)
	}
	if s.State == "armed" {
		hash, e := fileHash(b.pendingPath())
		if e != nil || hash != s.SHA256 {
			return false, fmt.Errorf("待恢复文件校验和不匹配")
		}
		if _, e = checkDatabase(ctx, b.pendingPath(), "quick_check"); e != nil {
			return false, e
		}
		name, e := b.create(ctx, "prerestore", 2)
		if e != nil {
			return false, fmt.Errorf("恢复前快照失败（原数据库未变更）: %w", e)
		}
		s.Prerestore = name
		s.State = "prerestore_done"
		if e = atomicJSON(b.markerPath(), s); e != nil {
			return false, e
		}
	}
	if s.State == "prerestore_done" {
		if _, e := os.Stat(b.pendingPath()); os.IsNotExist(e) {
			uncertain = true
			hash, e := fileHash(b.Database)
			if e != nil || hash != s.SHA256 {
				return false, fmt.Errorf("恢复换库中断，当前数据库校验和不匹配")
			}
		} else if e != nil {
			return false, e
		} else {
			hash, e := fileHash(b.pendingPath())
			if e != nil || hash != s.SHA256 {
				return false, fmt.Errorf("待恢复文件校验和不匹配")
			}
			if e = b.replaceDatabase(ctx, b.pendingPath(), s.Prerestore); e != nil {
				// rename 成功但目录同步失败：pending 已消失，不得继续按原库完好处理。
				if _, statErr := os.Stat(b.pendingPath()); os.IsNotExist(statErr) {
					uncertain = true
				}
				return false, e
			}
			uncertain = true
		}
		s.State = "swapped"
		if e := atomicJSON(b.markerPath(), s); e != nil {
			return false, e
		}
	}
	if s.State != "swapped" {
		uncertain = true
		return false, fmt.Errorf("未知恢复状态 %q", s.State)
	}
	return true, nil
}

// replaceDatabase 的调用者必须持有已持久化的 prerestore 快照。
func (b *Backups) replaceDatabase(ctx context.Context, pending, prerestore string) error {
	if !BackupFilename.MatchString(prerestore) {
		return fmt.Errorf("缺少恢复前快照")
	}
	if _, e := checkDatabase(ctx, filepath.Join(b.Directory, prerestore), "quick_check"); e != nil {
		return fmt.Errorf("恢复前快照不可用: %w", e)
	}
	db, e := independentDB(b.Database, false)
	if e != nil {
		return e
	}
	var busy, log, checkpointed int
	e = db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed)
	closeErr := db.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if busy != 0 {
		return fmt.Errorf("WAL checkpoint busy")
	}
	return b.installDatabase(pending)
}

// 回滚丢弃 swapped 库，不打开或 checkpoint 可能损坏的数据库。
func (b *Backups) installDatabase(pending string) error {
	var e error
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if e = os.Remove(b.Database + suffix); e != nil && !os.IsNotExist(e) {
			return e
		}
	}
	// 目录同步保证旧 sidecar 的删除先于新主文件的提交。
	if e = syncDirectory(filepath.Dir(b.Database)); e != nil {
		return e
	}
	if e = os.Rename(pending, b.Database); e != nil {
		return e
	}
	return syncDirectory(filepath.Dir(b.Database))
}

// CompleteRestore 必须在 Migrate 之后、NewApp 之前运行。所有后处理同一事务提交，
// 事务内 receipt 确保提交后、JSON 状态更新前断电时不会重复递增 revision。
func (b *Backups) CompleteRestore(ctx context.Context, s *Store) error {
	var state RestoreState
	if e := readJSON(b.markerPath(), &state); e != nil {
		if os.IsNotExist(e) && b.Result().Success {
			return nil
		}
		return e
	}
	if state.State != "swapped" {
		return fmt.Errorf("restore is not swapped")
	}
	conn, e := s.db.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	source := filepath.Join(b.Directory, state.Prerestore)
	// SQLite URI mode=ro；此处不能复用项目 WAL DSN。
	ro, e := readonlyURI(source)
	if e != nil {
		return e
	}
	if _, e = conn.ExecContext(ctx, "ATTACH DATABASE "+quoteSQL(ro)+" AS prerestore"); e != nil {
		return e
	}
	defer conn.ExecContext(context.Background(), "DETACH DATABASE prerestore")
	tx, e := conn.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS backup_restore_receipt (sha256 TEXT PRIMARY KEY) STRICT`); e != nil {
		return e
	}
	var done int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM backup_restore_receipt WHERE sha256=?`, state.SHA256).Scan(&done); e != nil {
		return e
	}
	if done == 0 {
		if e = preserveCredentials(ctx, tx); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE playlists SET revision=max(revision,coalesce((SELECT p.revision FROM prerestore.playlists p WHERE p.id=playlists.id),0))+1; DELETE FROM lastfm_scrobble_queue;`); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO backup_restore_receipt VALUES(?)`, state.SHA256); e != nil {
			return e
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	// 保护标记先于 done 持久化；后续启动会一直保护到一次成功扫描完成。
	if e = atomicJSON(filepath.Join(filepath.Dir(b.Database), "restore-scan.json"), true); e != nil {
		return e
	}
	state.Finalized = true
	state.State = "done"
	state.Success = true
	state.Time = time.Now()
	if e = atomicJSON(b.markerPath(), state); e != nil {
		return e
	}
	return b.finishResult(state)
}
func readonlyURI(path string) (string, error) {
	p, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	p = filepath.ToSlash(p)
	if runtime.GOOS == "windows" {
		p = "/" + p
	}
	q := url.Values{}
	q.Set("mode", "ro")
	return (&url.URL{Scheme: "file", Path: p, RawQuery: q.Encode()}).String(), nil
}
func tableColumns(ctx context.Context, tx *sql.Tx, schema, table string) (map[string]bool, error) {
	rows, e := tx.QueryContext(ctx, "PRAGMA "+schema+".table_info("+table+")")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if e = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); e != nil {
			return nil, e
		}
		out[name] = true
	}
	return out, rows.Err()
}
func preserveCredentials(ctx context.Context, tx *sql.Tx) error {
	for _, table := range []string{"credential_overrides", "admin_sessions"} {
		target, e := tableColumns(ctx, tx, "main", table)
		if e != nil {
			return e
		}
		source, e := tableColumns(ctx, tx, "prerestore", table)
		if e != nil {
			return e
		}
		if len(target) == 0 {
			continue
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM "+table); e != nil {
			return e
		}
		if len(source) == 0 {
			continue
		}
		cols := []string{}
		for c := range target {
			if source[c] {
				cols = append(cols, c)
			}
		}
		if len(cols) > 0 {
			list := strings.Join(cols, ",")
			if _, e = tx.ExecContext(ctx, "INSERT INTO "+table+" ("+list+") SELECT "+list+" FROM prerestore."+table); e != nil {
				return e
			}
		}
	}
	for _, item := range []struct {
		table, key string
		cols       []string
	}{{"lastfm_scrobble_settings", "id", []string{"api_secret", "session_key", "username"}}, {"metadata_source_settings", "source", []string{"api_key"}}, {"ai_settings", "id", []string{"base_url", "api_key", "proxy_url"}}} {
		source, e := tableColumns(ctx, tx, "prerestore", item.table)
		if e != nil {
			return e
		}
		target, e := tableColumns(ctx, tx, "main", item.table)
		if e != nil {
			return e
		}
		if len(target) == 0 {
			continue
		}
		// 不存在当前行/列意味着当前没有凭据，不能复活快照中的旧密钥。
		for _, c := range item.cols {
			if !target[c] {
				continue
			}
			query := "UPDATE " + item.table + " SET " + c + "=''"
			if source[c] {
				query = "UPDATE " + item.table + " SET " + c + "=" + preservedValueSQL(item.table, item.key, c)
			}
			if _, e = tx.ExecContext(ctx, query); e != nil {
				return e
			}
		}
		// 新版本单行设置可能尚未插入行。
		if len(source) > 0 {
			if _, e = tx.ExecContext(ctx, "INSERT OR IGNORE INTO "+item.table+" ("+item.key+") SELECT "+item.key+" FROM prerestore."+item.table); e != nil {
				return e
			}
			for _, c := range item.cols {
				if source[c] && target[c] {
					if _, e = tx.ExecContext(ctx, "UPDATE "+item.table+" SET "+c+"="+preservedValueSQL(item.table, item.key, c)); e != nil {
						return e
					}
				}
			}
		}
	}
	return nil
}

// RollbackRestore 先复制快照到同目录；绝不 rename 走可能跨文件系统的 backups 文件。
func (b *Backups) RollbackRestore(ctx context.Context, cause error) (err error) {
	defer func() {
		if err != nil {
			if e := b.RequireManualRecovery("rollback", err); e != nil {
				err = fmt.Errorf("%v; 持久化降级标志失败: %w", err, e)
			}
		}
	}()
	var state RestoreState
	if e := readJSON(b.markerPath(), &state); e != nil {
		return e
	}
	state.RollingBack = true
	state.Error = cause.Error()
	if e := atomicJSON(b.markerPath(), state); e != nil {
		return e
	}
	// swapped 库可能已经损坏；prerestore 是换库之前持久化的一致快照。
	if !BackupFilename.MatchString(state.Prerestore) {
		return fmt.Errorf("invalid prerestore snapshot")
	}
	f, e := os.CreateTemp(filepath.Dir(b.Database), "restore-rollback-*.db")
	if e != nil {
		return e
	}
	pending := f.Name()
	defer os.Remove(pending)
	src, e := os.Open(filepath.Join(b.Directory, state.Prerestore))
	if e != nil {
		f.Close()
		return e
	}
	_, e = io.Copy(f, src)
	src.Close()
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	if e != nil {
		return e
	}
	if _, e = checkDatabase(ctx, pending, "quick_check"); e != nil {
		return e
	}
	if e = b.installDatabase(pending); e != nil {
		return e
	}
	state.State = "done"
	state.Success = false
	state.RollingBack = false
	state.Error = cause.Error()
	state.Time = time.Now()
	return b.finishResult(state)
}
func (b *Backups) ScanProtectionPath() string {
	return filepath.Join(filepath.Dir(b.Database), "restore-scan.json")
}

func preservedValueSQL(table, key, column string) string {
	sub := "(SELECT p." + column + " FROM prerestore." + table + " p WHERE p." + key + "=" + table + "." + key + ")"
	if table == "metadata_source_settings" && column == "api_key" {
		return sub
	}
	return "coalesce(" + sub + ",'')"
}

type ManualRecovery struct {
	Time                     time.Time
	Stage, Error, Prerestore string
}

func (b *Backups) ManualRecoveryPath() string {
	return filepath.Join(filepath.Dir(b.Database), "restore-manual-recovery.json")
}
func (b *Backups) ManualRecovery() (ManualRecovery, error) {
	var v ManualRecovery
	e := readJSON(b.ManualRecoveryPath(), &v)
	return v, e
}
func (b *Backups) RequireManualRecovery(stage string, cause error) error {
	var s RestoreState
	_ = readJSON(b.markerPath(), &s)
	v := ManualRecovery{Time: time.Now(), Stage: stage, Error: ManualRecoverySummary(stage) + "，需要离线恢复；详见服务器日志", Prerestore: s.Prerestore}
	if e := atomicJSON(b.ManualRecoveryPath(), v); e != nil {
		return e
	}
	s.State = "done"
	s.Success = false
	s.RollingBack = false
	s.Error = cause.Error()
	s.Time = v.Time
	return b.finishResult(s)
}

func ManualRecoverySummary(stage string) string {
	if stage == "rollback" {
		return "自动回滚失败"
	}
	return "恢复状态无法判定或无法安全取消"
}
