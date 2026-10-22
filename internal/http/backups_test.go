package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func setupBackupHTTP(t *testing.T) (*App, http.Handler, *http.Cookie) {
	t.Helper()
	app, s, _ := setupTestApp(t)
	app.config.BackupUploadMaxMB = 1
	b := storage.NewBackups(httpTestDBPath[s], app.config.DataDirectory, "/music")
	if e := b.Initialize(); e != nil {
		t.Fatal(e)
	}
	app.SetBackups(b)
	return app, app.Handler(), adminCookie(t, app)
}
func backupUploadRequest(t *testing.T, cookie *http.Cookie, data []byte) (*http.Request, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, e := writer.CreateFormFile("snapshot", "snapshot.db")
	if e != nil {
		t.Fatal(e)
	}
	part.Write(data)
	writer.Close()
	r := httptest.NewRequest("POST", "/admin/backups/upload", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r, writer.FormDataContentType()
}
func backupCSRFHeader(t *testing.T, app *App, r *http.Request) {
	t.Helper()
	session, ok := app.sessions.get(r)
	if !ok {
		t.Fatal("no session")
	}
	r.Header.Set("X-CSRF-Token", session.CSRFToken)
}
func TestBackupUploadAuthLimitsAndCleanup(t *testing.T) {
	app, h, cookie := setupBackupHTTP(t)
	for _, kind := range []string{"anonymous", "csrf", "over-limit", "interrupted", "invalid"} {
		t.Run(kind, func(t *testing.T) {
			data := []byte("invalid sqlite")
			if kind == "over-limit" {
				data = bytes.Repeat([]byte("x"), 2<<20)
			}
			r, typ := backupUploadRequest(t, cookie, data)
			if kind == "anonymous" {
				r.Header.Del("Cookie")
			} else if kind != "csrf" {
				backupCSRFHeader(t, app, r)
			}
			if kind == "interrupted" {
				r.Body = io.NopCloser(&failedUploadReader{data: []byte("--" + strings.TrimPrefix(typ, "multipart/form-data; boundary=") + "\r\nContent-Disposition: form-data; name=\"snapshot\"; filename=\"x.db\"\r\n\r\npartial")})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code == 200 {
				t.Fatalf("accepted %s", kind)
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(app.backups.Database), "restore-upload-*"))
			if len(files) != 0 {
				t.Fatalf("upload remnants %v", files)
			}
		})
	}
}

type failedUploadReader struct{ data []byte }

func (r *failedUploadReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, errors.New("interrupted upload")
}
func TestBackupUploadStagesWithoutArmingAndCancel(t *testing.T) {
	app, h, cookie := setupBackupHTTP(t)
	name, e := app.backups.Create(context.Background(), "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(app.backups.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	app.config.BackupUploadMaxMB = 20
	r, _ := backupUploadRequest(t, cookie, data)
	backupCSRFHeader(t, app, r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "location") {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	dir := filepath.Dir(app.backups.Database)
	if _, e = os.Stat(filepath.Join(dir, "restore-state.json")); !os.IsNotExist(e) {
		t.Fatal("upload armed restore")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "restore-staging-*.db"))
	if len(files) != 1 {
		t.Fatalf("stage files %v", files)
	}
	rec := postSecurity(t, app, h, cookie, "/admin/backups/cancel", url.Values{"stage": {filepath.Base(files[0])}})
	if rec.Code != 303 {
		t.Fatal(rec.Code)
	}
	if _, e = os.Stat(files[0]); !os.IsNotExist(e) {
		t.Fatal("cancel kept staging")
	}
}
func TestBackupDownloadTraversalAndPassword(t *testing.T) {
	app, h, cookie := setupBackupHTTP(t)
	// 直接调用只验证白名单阶段：用正确密码的完整鉴权另见 security 页现有测试。
	app2, _, handler, loginCookie := missingFilesTestApp(t, "never")
	app2.SetBackups(app.backups)
	bad := postSecurity(t, app2, handler, loginCookie, "/admin/backups/download", url.Values{"current_password": {testAdminPassword}, "name": {"../music.db"}})
	if bad.Code != 400 {
		t.Fatalf("traversal status %d %s", bad.Code, bad.Body.String())
	}
	rec := postSecurity(t, app, h, cookie, "/admin/backups/download", url.Values{"name": {"music-manual-20250101-000000-v56.db"}})
	if rec.Code == 200 {
		t.Fatal("download without password")
	}
	page := getWithCookie(h, "/admin/backups", cookie)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "UTC+8") {
		t.Fatalf("page %d %s", page.Code, page.Body.String())
	}
}
func TestBackupGCProtectsSnapshotAndImmediateRemoval(t *testing.T) {
	app, s, _ := setupTestApp(t)
	ctx := context.Background()
	b := storage.NewBackups(httpTestDBPath[s], app.config.DataDirectory, "/music")
	if e := b.Initialize(); e != nil {
		t.Fatal(e)
	}
	app.SetBackups(b)

	importTestTrack(t, app)
	artistID := firstArtistID(t, app)
	dir := app.customImagesDir()
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	name := "protected.png"
	path := filepath.Join(dir, name)
	os.WriteFile(path, []byte("image"), 0600)
	if _, e := s.SaveCustomArtistImage(ctx, artistID, storage.CustomImageInput{Hash: "hash", MIMEType: "image/png", FileName: name, ByteSize: 5}); e != nil {
		t.Fatal(e)
	}
	snapshot, e := b.Create(ctx, "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ResetCustomArtistImage(ctx, artistID); e != nil {
		t.Fatal(e)
	}
	os.Chtimes(path, time.Now().Add(-2*time.Hour), time.Now().Add(-2*time.Hour))
	app.removeUnreferenced(ctx, []string{name})
	app.gcCustomImages(ctx)
	if _, e = os.Stat(path); e != nil {
		t.Fatal("snapshot reference was deleted", e)
	}
	if e = b.Delete(snapshot); e != nil {
		t.Fatal(e)
	}
	app.gcCustomImages(ctx)
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("rotated snapshot reference not reclaimed")
	}
}

func TestBackupConfirmPasswordAndRestart(t *testing.T) {
	app, store, h, cookie := missingFilesTestApp(t, "never")
	b := storage.NewBackups(httpTestDBPath[store], app.config.DataDirectory, "/music")
	if e := b.Initialize(); e != nil {
		t.Fatal(e)
	}
	app.SetBackups(b)
	name, e := b.Create(context.Background(), "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	stage, _, e := b.Stage(context.Background(), filepath.Join(b.Directory, name))
	if e != nil {
		t.Fatal(e)
	}
	restart := make(chan struct{}, 1)
	app.RequestRestart = func() { restart <- struct{}{} }
	for _, password := range []string{"", "wrong"} {
		rec := postSecurity(t, app, h, cookie, "/admin/backups/confirm", url.Values{"stage": {stage}, "current_password": {password}})
		if rec.Code != 303 || !strings.HasPrefix(rec.Header().Get("Location"), backupsPath) {
			t.Fatalf("password failure %d %s", rec.Code, rec.Header().Get("Location"))
		}
		if _, e = os.Stat(filepath.Join(filepath.Dir(b.Database), "restore-state.json")); !os.IsNotExist(e) {
			t.Fatal("wrong password armed restore")
		}
	}
	rec := postSecurity(t, app, h, cookie, "/admin/backups/confirm", url.Values{"stage": {stage}, "current_password": {testAdminPassword}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "正在重启") {
		t.Fatalf("confirm %d %s", rec.Code, rec.Body.String())
	}
	if _, e = os.Stat(filepath.Join(filepath.Dir(b.Database), "restore-state.json")); e != nil {
		t.Fatal(e)
	}
	select {
	case <-restart:
	case <-time.After(2 * time.Second):
		t.Fatal("restart not requested")
	}
}
func TestBackupUploadValidSnapshotLimitDoesNotParseMultipart(t *testing.T) {
	app, h, cookie := setupBackupHTTP(t)
	// 大于 1MiB 的合法已迁移 SQLite 数据库，不依赖无效格式导致的拒绝。
	httpExec(t, app.store, `INSERT INTO credential_overrides(key,value,updated_at) VALUES('admin_username',hex(randomblob(800000)),'now')`)
	name, e := app.backups.Create(context.Background(), "manual", 0)
	if e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(app.backups.Directory, name)
	if _, e = app.backups.Validate(context.Background(), source); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(source)
	if e != nil {
		t.Fatal(e)
	}
	if len(data) <= 1<<20 {
		t.Fatal("fixture not over limit")
	}
	r, _ := backupUploadRequest(t, cookie, data)
	backupCSRFHeader(t, app, r)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "request body too large") {
		t.Fatalf("limit not enforced: %d %s", w.Code, w.Body.String())
	}
	// MultipartReader 自身设置一个空的内部 sentinel；ParseMultipartForm 才填充 Value/File。
	if r.MultipartForm != nil && (len(r.MultipartForm.File) > 0 || len(r.MultipartForm.Value) > 0) {
		t.Fatal("ParseMultipartForm populated form")
	}
	if _, err := r.MultipartReader(); err == nil || !strings.Contains(err.Error(), "MultipartReader called twice") {
		t.Fatalf("streaming multipart sentinel missing: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(app.backups.Database), "restore-upload-*"))
	if len(files) != 0 {
		t.Fatalf("upload remnants %v", files)
	}
}

func TestBackupNoticeChinese(t *testing.T) {
	for _, code := range []string{"wrong_password", "locked", "busy", "verify_failed"} {
		if got := backupNotice(code); got == code || got == "" {
			t.Fatalf("notice not translated: %s", code)
		}
	}
}

func TestBackupPageTranslatesNotice(t *testing.T) {
	app, h, cookie := setupBackupHTTP(t)
	_ = app
	rec := getWithCookie(h, "/admin/backups?notice=wrong_password", cookie)
	if !strings.Contains(rec.Body.String(), "当前管理员密码错误") {
		t.Fatal("page displays raw notice")
	}
}
