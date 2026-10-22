package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

const backupsPath = "/admin/backups"

type backupsPageData struct {
	Chrome
	Settings   storage.BackupSettings
	Files      []storage.BackupFile
	Status     storage.BackupStatus
	Result     storage.RestoreState
	Notice     string
	Stage      string
	Validation *storage.RestoreValidation
	Restarting bool
}

func (a *App) SetBackups(b *storage.Backups) { a.backups = b }
func (a *App) backupService() *storage.Backups {
	// NewApp 初始化后不再修改；生产环境 main 注入同一个实例。
	return a.backups
}
func (a *App) BackupSettings(ctx context.Context) (storage.BackupSettings, error) {
	v := storage.BackupSettings{Enabled: a.config.BackupEnabled, Time: a.config.BackupTime, KeepDaily: a.config.BackupKeepDaily, KeepPreupgrade: a.config.BackupKeepPreupgrade}
	if v.Time == "" {
		v.Time = "02:30"
	}
	if v.KeepDaily == 0 {
		v.KeepDaily = 3
	}
	if v.KeepPreupgrade == 0 {
		v.KeepPreupgrade = 2
	}
	return a.store.BackupSettings(ctx, v)
}
func (a *App) registerBackupRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/backups", a.requireAdmin(http.HandlerFunc(a.handleBackups)))
	for path, handler := range map[string]http.HandlerFunc{"settings": a.handleBackupSettings, "create": a.handleBackupCreate, "delete": a.handleBackupDelete, "download": a.handleBackupDownload, "select": a.handleBackupSelect, "upload": a.handleBackupUpload, "confirm": a.handleBackupConfirm, "cancel": a.handleBackupCancel} {
		if path == "upload" {
			mux.Handle("POST /admin/backups/"+path, a.requireAdminJSON(handler))
		} else {
			mux.Handle("POST /admin/backups/"+path, a.requireAdmin(handler))
		}
	}
}
func (a *App) backupPage(w http.ResponseWriter, r *http.Request, stage string, validation *storage.RestoreValidation, restarting bool, notice string) {
	setNoStore(w)
	b := a.backupService()
	if b == nil {
		http.Error(w, "备份服务未初始化", 503)
		return
	}
	v, e := a.BackupSettings(r.Context())
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	list, e := b.List()
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	session, _ := a.sessions.get(r)
	a.render(w, 200, "backups.html", backupsPageData{Chrome: a.chromeFor(r.Context(), session, "backups"), Settings: v, Files: list, Status: b.Status(), Result: b.Result(), Stage: stage, Validation: validation, Restarting: restarting, Notice: backupNotice(notice)})
}
func (a *App) handleBackups(w http.ResponseWriter, r *http.Request) {
	stage := r.URL.Query().Get("stage")
	if stage != "" {
		v, e := a.backups.ValidateStage(r.Context(), stage)
		if e != nil {
			a.backupError(w, r, e)
			return
		}
		a.backupPage(w, r, stage, &v, false, "")
		return
	}
	a.backupPage(w, r, "", nil, false, r.URL.Query().Get("notice"))
}
func (a *App) backupCSRF(w http.ResponseWriter, r *http.Request) bool {
	if a.backupService() == nil {
		http.Error(w, "备份服务未初始化", 503)
		return false
	}
	setNoStore(w)
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", 403)
		return false
	}
	return true
}
func (a *App) backupError(w http.ResponseWriter, r *http.Request, e error) {
	a.backupPage(w, r, "", nil, false, e.Error())
}
func (a *App) handleBackupSettings(w http.ResponseWriter, r *http.Request) {
	if !a.backupCSRF(w, r) {
		return
	}
	var e error
	if r.PostFormValue("reset") == "1" {
		e = a.store.ResetBackupSettings(r.Context())
	} else {
		daily, _ := strconv.Atoi(r.PostFormValue("keep_daily"))
		pre, _ := strconv.Atoi(r.PostFormValue("keep_preupgrade"))
		e = a.store.SaveBackupSettings(r.Context(), storage.BackupSettings{Enabled: r.PostFormValue("enabled") == "1", Time: r.PostFormValue("time"), KeepDaily: daily, KeepPreupgrade: pre})
	}
	if e != nil {
		a.backupError(w, r, e)
		return
	}
	redirectWithNotice(w, r, backupsPath, "设置已保存")
}
func (a *App) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	if !a.backupCSRF(w, r) {
		return
	}
	if _, e := a.backups.Create(r.Context(), "manual", 0); e != nil {
		a.backupError(w, r, e)
		return
	}
	redirectWithNotice(w, r, backupsPath, "备份已完成")
}
func (a *App) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	if !a.backupCSRF(w, r) {
		return
	}
	if e := a.backups.Delete(r.PostFormValue("name")); e != nil {
		a.backupError(w, r, e)
		return
	}
	a.GCCustomImages(r.Context())
	redirectWithNotice(w, r, backupsPath, "快照已删除")
}
func (a *App) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	if a.backupService() == nil {
		http.Error(w, "备份服务未初始化", 503)
		return
	}
	if _, ok := a.beginSensitiveOperationAt(w, r, "backup download", backupsPath); !ok {
		return
	}
	name := r.PostFormValue("name")
	if !storage.BackupFilename.MatchString(name) {
		http.Error(w, "invalid backup filename", 400)
		return
	}
	f, e := os.Open(filepath.Join(a.backups.Directory, name))
	if e != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
func (a *App) handleBackupSelect(w http.ResponseWriter, r *http.Request) {
	if !a.backupCSRF(w, r) {
		return
	}
	name := r.PostFormValue("name")
	if !storage.BackupFilename.MatchString(name) {
		http.Error(w, "invalid backup filename", 400)
		return
	}
	stage, result, e := a.backups.Stage(r.Context(), filepath.Join(a.backups.Directory, name))
	if e != nil {
		a.backupError(w, r, e)
		return
	}
	a.backupPage(w, r, stage, &result, false, "")
}

// 上传不能使用 validCSRF/FormValue：否则 net/http 会预解析 multipart 到 /tmp。
func (a *App) handleBackupUpload(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	session, ok := a.sessions.get(r)
	if !ok || !secureEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
		http.Error(w, "invalid CSRF header", 403)
		return
	}
	b := a.backupService()
	if b == nil {
		http.Error(w, "备份服务未初始化", 503)
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Minute))
	maxMB := a.config.BackupUploadMaxMB
	if maxMB < 1 {
		maxMB = 2048
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxMB)*1024*1024)
	reader, e := r.MultipartReader()
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	f, e := os.CreateTemp(filepath.Dir(b.Database), "restore-upload-*.db")
	if e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	path := f.Name()
	defer func() { f.Close(); os.Remove(path); os.Remove(path + "-wal"); os.Remove(path + "-shm") }()
	found := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if part.FormName() != "snapshot" || found {
			part.Close()
			http.Error(w, "只允许一个 snapshot 文件", 400)
			return
		}
		found = true
		if _, err = io.Copy(f, part); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		part.Close()
	}
	if !found {
		http.Error(w, "未上传快照", 400)
		return
	}
	if e = f.Sync(); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	if e = f.Close(); e != nil {
		http.Error(w, e.Error(), 500)
		return
	}
	stage, _, e := b.Stage(r.Context(), path)
	if e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"location":%q}`, backupsPath+"?stage="+stage)
}
func (a *App) handleBackupConfirm(w http.ResponseWriter, r *http.Request) {
	if a.backupService() == nil {
		http.Error(w, "备份服务未初始化", 503)
		return
	}
	if _, ok := a.beginSensitiveOperationAt(w, r, "restore database", backupsPath); !ok {
		return
	}
	if e := a.backups.Arm(r.Context(), r.PostFormValue("stage")); e != nil {
		a.backupError(w, r, e)
		return
	}
	a.backupPage(w, r, "", nil, true, "")
	if a.RequestRestart != nil {
		go func() { time.Sleep(500 * time.Millisecond); a.RequestRestart() }()
	}
}
func (a *App) handleBackupCancel(w http.ResponseWriter, r *http.Request) {
	if !a.backupCSRF(w, r) {
		return
	}
	if e := a.backups.CancelStage(r.PostFormValue("stage")); e != nil {
		a.backupError(w, r, e)
		return
	}
	redirectWithNotice(w, r, backupsPath, "已取消恢复")
}
func (a *App) customImageReferences(ctx context.Context) (map[string]bool, error) {
	refs, e := a.store.CustomImageFiles(ctx)
	if e != nil {
		return nil, e
	}
	out := map[string]bool{}
	for p := range refs {
		out[filepath.Base(p)] = true
	}
	if a.backups != nil {
		saved, e := a.backups.ImageReferences(ctx)
		if e != nil {
			return nil, fmt.Errorf("备份图片引用检查: %w", e)
		}
		for p := range saved {
			out[filepath.Base(p)] = true
		}
	}
	return out, nil
}

func backupNotice(code string) string {
	switch code {
	case "wrong_password":
		return "当前管理员密码错误，请重新输入。"
	case "locked":
		return "尝试次数过多，请稍后重试。"
	case "busy":
		return "密码校验繁忙，请稍后重试。"
	case "verify_failed":
		return "密码校验失败，请稍后重试或检查日志。"
	}
	return code
}
