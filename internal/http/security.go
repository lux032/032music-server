package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

// securityPagePath is the admin "账号与安全" page. Every POST below answers
// with a 303 back to it and a non-sensitive ?notice= message (CSRF failures
// excepted, which stay 403 like the rest of the admin UI). Secrets never
// travel in URLs: freshly generated or revealed tokens are handed over via a
// one-shot session flash that the next GET of this page consumes.
const securityPagePath = "/admin/settings/security"

// securityPageData is the template contract for security.html.
type securityPageData struct {
	Chrome
	Notice  string
	Sources CredentialSources
	// ResetMode mirrors MUSIC_SERVER_RESET_CREDENTIALS ("" when off); the
	// page should show a banner while it is set.
	ResetMode string
	DevMode   bool
	// MediaTokenEphemeral is true when the effective media token is the
	// per-boot random fallback (env unset, no override).
	MediaTokenEphemeral bool
	Flash               *securityFlash
}

// securityResetKeys maps the /reset/{key} path value to the override key
// and a user-facing label.
var securityResetKeys = map[string]struct {
	override string
	label    string
}{
	"username":    {storage.CredentialAdminUsername, "用户名"},
	"password":    {storage.CredentialAdminPasswordHash, "密码"},
	"api-token":   {storage.CredentialAPITokenHash, "API Token"},
	"media-token": {storage.CredentialMediaToken, "媒体 Token"},
}

func setNoStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

// sessionLogID identifies the acting session in logs without exposing the
// cookie: a short prefix of the SHA-256 of the cookie token.
func sessionLogID(r *http.Request) string {
	cookie, err := r.Cookie(adminSessionCookie)
	if err != nil {
		return ""
	}
	return sessionTokenHash(cookie.Value)[:12]
}

func (a *App) handleSecurityPage(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	session, _ := a.sessions.get(r)
	creds := a.currentCredentials()
	data := securityPageData{
		Chrome:              chromeFor(session, "security"),
		Notice:              r.URL.Query().Get("notice"),
		Sources:             creds.sources,
		ResetMode:           a.config.ResetCredentials,
		DevMode:             a.config.DevMode,
		MediaTokenEphemeral: a.config.MediaTokenGenerated && creds.sources.MediaToken == CredentialSourceEnv,
	}
	if flash, ok := a.sessions.takeFlash(r); ok {
		data.Flash = &flash
	}
	a.render(w, http.StatusOK, "security.html", data)
}

// Notices shared by several security handlers.
const (
	noticePasswordBusy       = "系统繁忙，请稍后重试。"
	noticeCredentialsChanged = "凭据已被其他操作修改，请刷新页面后重试。"
)

// beginSensitiveOperation enforces CSRF and re-verifies the current admin
// password. Failures count against the login limiter (same IP+username and
// IP-only keys as the login form) and are delayed like failed logins. It
// returns the credential snapshot the password was verified against; the
// caller must save with it as "expected" so a concurrent change made in
// between is refused. When it returns false the response has been written.
func (a *App) beginSensitiveOperation(w http.ResponseWriter, r *http.Request, operation string) (*credentials, bool) {
	setNoStore(w)
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return nil, false
	}
	creds := a.currentCredentials()
	if locked, remaining := a.loginLimiter.lockedFor(r, creds.username); locked {
		a.logger.Warn("sensitive admin operation while locked out", "operation", operation, "session", sessionLogID(r), "remoteAddr", r.RemoteAddr)
		redirectWithNotice(w, r, securityPagePath, fmt.Sprintf("失败次数过多,请 %d 分钟后再试。", int(remaining.Minutes())+1))
		return nil, false
	}
	ok, err := a.checkAdminPassword(r, creds, r.PostFormValue("current_password"))
	if errors.Is(err, errPasswordBusy) {
		redirectWithNotice(w, r, securityPagePath, noticePasswordBusy)
		return nil, false
	}
	if err != nil {
		a.logger.Error("verify admin password", "operation", operation, "error", err)
		redirectWithNotice(w, r, securityPagePath, "暂时无法验证密码,请稍后再试。")
		return nil, false
	}
	if !ok {
		a.loginLimiter.recordFailureFor(r, creds.username)
		time.Sleep(loginFailureDelay)
		a.logger.Warn("sensitive admin operation rejected: wrong current password", "operation", operation, "session", sessionLogID(r), "remoteAddr", r.RemoteAddr)
		redirectWithNotice(w, r, securityPagePath, "当前密码不正确。")
		return nil, false
	}
	a.loginLimiter.recordSuccessFor(r, creds.username)
	return creds, true
}

// credentialSaveFailed maps a save error to a non-sensitive notice.
func (a *App) credentialSaveFailed(w http.ResponseWriter, r *http.Request, item string, err error) {
	switch {
	case errors.Is(err, errCredentialTokensEqual):
		redirectWithNotice(w, r, securityPagePath, "API Token 与媒体 Token 不能相同。")
	case errors.Is(err, errCredentialsChanged):
		redirectWithNotice(w, r, securityPagePath, noticeCredentialsChanged)
	default:
		a.logger.Error("save admin credential", "item", item, "error", err)
		redirectWithNotice(w, r, securityPagePath, "保存失败,请稍后再试。")
	}
}

// saveTokenCredentials persists a token override change (sessions are not
// affected). It returns false after writing a redirect.
func (a *App) saveTokenCredentials(w http.ResponseWriter, r *http.Request, item string, expected *credentials, set map[string]string, remove []string) bool {
	if _, err := a.credentials.update(r.Context(), expected, set, remove); err != nil {
		a.credentialSaveFailed(w, r, item, err)
		return false
	}
	a.logger.Info("admin credential changed", "item", item, "session", sessionLogID(r))
	return true
}

// saveLoginCredentials persists a username/password change and, in the
// same database transaction, replaces every admin session with a rotated
// current session (new cookie, new CSRF token, new username). Nothing
// changes if any part fails.
func (a *App) saveLoginCredentials(w http.ResponseWriter, r *http.Request, item string, expected *credentials, set map[string]string, remove []string, notice string) {
	actor := sessionLogID(r)
	if _, err := a.credentials.updateLogin(r.Context(), a.sessions, w, expected, set, remove); err != nil {
		a.credentialSaveFailed(w, r, item, err)
		return
	}
	a.logger.Info("admin credential changed; other admin sessions revoked", "item", item, "session", actor)
	redirectWithNotice(w, r, securityPagePath, notice)
}

func (a *App) handleSecurityUsername(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperation(w, r, "username")
	if !ok {
		return
	}
	username, problem := normalizeAdminUsername(r.PostFormValue("new_username"))
	if problem == "" && username == creds.username {
		problem = "新用户名与当前用户名相同。"
	}
	if problem != "" {
		redirectWithNotice(w, r, securityPagePath, problem)
		return
	}
	a.saveLoginCredentials(w, r, "username", creds, map[string]string{storage.CredentialAdminUsername: username}, nil, "用户名已修改,其他登录会话已退出。")
}

func (a *App) handleSecurityPassword(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperation(w, r, "password")
	if !ok {
		return
	}
	password := r.PostFormValue("new_password")
	if problem := validateNewAdminPassword(password, a.config.DevMode); problem != "" {
		redirectWithNotice(w, r, securityPagePath, problem)
		return
	}
	if password != r.PostFormValue("confirm_password") {
		redirectWithNotice(w, r, securityPagePath, "两次输入的新密码不一致。")
		return
	}
	same, err := a.checkAdminPassword(r, creds, password)
	if errors.Is(err, errPasswordBusy) {
		redirectWithNotice(w, r, securityPagePath, noticePasswordBusy)
		return
	}
	if err != nil {
		a.logger.Error("compare new admin password", "error", err)
		redirectWithNotice(w, r, securityPagePath, "暂时无法验证密码,请稍后再试。")
		return
	}
	if same {
		redirectWithNotice(w, r, securityPagePath, "新密码不能与当前密码相同。")
		return
	}
	encoded, err := hashPassword(r.Context(), password)
	if errors.Is(err, errPasswordBusy) {
		redirectWithNotice(w, r, securityPagePath, noticePasswordBusy)
		return
	}
	if err != nil {
		a.logger.Error("hash admin password", "error", err)
		redirectWithNotice(w, r, securityPagePath, "保存失败,请稍后再试。")
		return
	}
	a.saveLoginCredentials(w, r, "password", creds, map[string]string{storage.CredentialAdminPasswordHash: encoded}, nil, "密码已修改,其他登录会话已退出。")
}

// handleTokenChange implements the API and media token forms: an empty
// custom_token generates a new token (flashed once), otherwise the custom
// token is validated and saved (never echoed back).
func (a *App) handleTokenChange(w http.ResponseWriter, r *http.Request, item, overrideKey, label string, stored func(token string) string) {
	creds, ok := a.beginSensitiveOperation(w, r, item)
	if !ok {
		return
	}
	token, generated, problem := chooseCredentialToken(r.PostFormValue("custom_token"))
	if problem != "" {
		redirectWithNotice(w, r, securityPagePath, problem)
		return
	}
	if !a.saveTokenCredentials(w, r, item, creds, map[string]string{overrideKey: stored(token)}, nil) {
		return
	}
	if !generated {
		redirectWithNotice(w, r, securityPagePath, label+" 已更新,旧 Token 已失效。")
		return
	}
	if !a.sessions.setFlash(r, securityFlash{Label: "新的 " + label, Value: token}) {
		redirectWithNotice(w, r, securityPagePath, "新 Token 已生效但无法显示，请重新生成。")
		return
	}
	redirectWithNotice(w, r, securityPagePath, label+" 已重新生成,旧 Token 已失效。新 Token 只显示这一次,请立即复制。")
}

func (a *App) handleSecurityAPIToken(w http.ResponseWriter, r *http.Request) {
	a.handleTokenChange(w, r, "api_token", storage.CredentialAPITokenHash, "API Token", hashAPIToken)
}

func (a *App) handleSecurityMediaToken(w http.ResponseWriter, r *http.Request) {
	a.handleTokenChange(w, r, "media_token", storage.CredentialMediaToken, "媒体 Token", func(token string) string { return token })
}

func (a *App) handleSecurityRevealMediaToken(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperation(w, r, "media_token_reveal")
	if !ok {
		return
	}
	if a.currentCredentials() != creds {
		redirectWithNotice(w, r, securityPagePath, noticeCredentialsChanged)
		return
	}
	if !a.sessions.setFlash(r, securityFlash{Label: "当前媒体 Token", Value: creds.mediaToken}) {
		redirectWithNotice(w, r, securityPagePath, "无法显示媒体 Token，请重试。")
		return
	}
	a.logger.Info("admin credential revealed", "item", "media_token", "session", sessionLogID(r))
	redirectWithNotice(w, r, securityPagePath, "当前媒体 Token 已显示,刷新页面后隐藏。")
}

func (a *App) handleSecurityReset(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperation(w, r, "reset")
	if !ok {
		return
	}
	target, known := securityResetKeys[r.PathValue("key")]
	if !known {
		redirectWithNotice(w, r, securityPagePath, "未知的凭据项。")
		return
	}
	sources := creds.sources
	current := map[string]string{
		storage.CredentialAdminUsername:     sources.Username,
		storage.CredentialAdminPasswordHash: sources.Password,
		storage.CredentialAPITokenHash:      sources.APIToken,
		storage.CredentialMediaToken:        sources.MediaToken,
	}[target.override]
	if current == CredentialSourceEnv {
		redirectWithNotice(w, r, securityPagePath, target.label+"已在使用环境变量。")
		return
	}
	item := "reset_" + target.override
	notice := target.label + "已恢复为环境变量。"
	remove := []string{target.override}
	if target.override == storage.CredentialAdminUsername || target.override == storage.CredentialAdminPasswordHash {
		a.saveLoginCredentials(w, r, item, creds, nil, remove, notice+"其他登录会话已退出。")
		return
	}
	if a.saveTokenCredentials(w, r, item, creds, nil, remove) {
		redirectWithNotice(w, r, securityPagePath, notice)
	}
}
