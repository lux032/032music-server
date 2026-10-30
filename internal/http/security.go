package httpapi

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/storage"
)

// securityPagePath is the admin "账号与安全" page. Every POST below answers
// with a 303 back to it and a fixed ?notice= code (CSRF failures excepted,
// which stay 403 like the rest of the admin UI). Secrets never travel in
// URLs: freshly generated or revealed tokens are handed over via a one-shot
// session flash that the next GET of this page consumes.
const securityPagePath = "/admin/settings/security"

// securityPageData is the template contract for security.html.
type securityPageData struct {
	Chrome
	// Notice is the fixed message for the ?notice= code, nil when the code
	// is absent or unknown.
	Notice *securityNotice
	// CurrentUsername is the effective login username.
	CurrentUsername string
	Sources         CredentialSources
	// ResetMode mirrors MUSIC_SERVER_RESET_CREDENTIALS ("" when off); the
	// page shows a banner while it is set. ResetItems names what it clears.
	ResetMode  string
	ResetItems string
	DevMode    bool
	// MinPasswordLength is 0 in DevMode (any non-empty password).
	MinPasswordLength int
	MinTokenLength    int
	MaxTokenLength    int
	MaxUsernameLength int
	// MediaTokenEphemeral is true when the effective media token is the
	// per-boot random fallback (env unset, no override).
	MediaTokenEphemeral bool
	// MediaTokenSharesAPIToken is the DevMode fallback: no media token was
	// configured, so the environment API token doubles as the media token.
	MediaTokenSharesAPIToken bool
	Flash                    *securityFlash
	// TOTPEnabled reports whether two-factor login is on; TOTPSetup is the
	// enrollment pending on this session, if any.
	TOTPEnabled bool
	TOTPSetup   *totpSetupView
	// Login is the ban / allowlist overview.
	Login loginProtectionView
}

// securityResetKeys maps the /reset/{key} path value to the override key
// and the notice code prefix.
var securityResetKeys = map[string]struct {
	override string
	code     string
}{
	"username":    {storage.CredentialAdminUsername, "username"},
	"password":    {storage.CredentialAdminPasswordHash, "password"},
	"api-token":   {storage.CredentialAPITokenHash, "api_token"},
	"media-token": {storage.CredentialMediaToken, "media_token"},
}

// resetModeItems describes what each MUSIC_SERVER_RESET_CREDENTIALS value
// clears, for the reset-mode banner.
var resetModeItems = map[string]string{
	config.ResetCredentialsPassword: "登录用户名、密码与两步验证",
	config.ResetCredentialsTokens:   "API Token 与媒体 Token",
	config.ResetCredentialsAll:      "登录用户名、密码、两步验证、API Token 与媒体 Token",
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
		Chrome:                   a.chromeFor(r.Context(), session, "security"),
		CurrentUsername:          creds.username,
		Sources:                  creds.sources,
		ResetMode:                a.config.ResetCredentials,
		ResetItems:               resetModeItems[a.config.ResetCredentials],
		DevMode:                  a.config.DevMode,
		MinTokenLength:           minCredentialTokenLen,
		MaxTokenLength:           maxCredentialTokenLen,
		MaxUsernameLength:        maxAdminUsernameLength,
		MediaTokenEphemeral:      a.config.MediaTokenGenerated && creds.sources.MediaToken == CredentialSourceEnv,
		MediaTokenSharesAPIToken: a.config.DevMode && creds.sources.MediaToken == CredentialSourceEnv && a.config.MediaToken == a.config.APIToken,
	}
	if !a.config.DevMode {
		data.MinPasswordLength = minAdminPasswordLength
	}
	if notice, ok := securityNotices[r.URL.Query().Get("notice")]; ok {
		data.Notice = &notice
	}
	if flash, ok := a.sessions.takeFlash(r); ok {
		data.Flash = &flash
	}
	data.Login = a.loginProtectionFor(r)
	data.TOTPEnabled = creds.totpEnabled()
	if setup, ok := a.sessions.totpSetupFor(r); ok && !data.TOTPEnabled {
		data.TOTPSetup = &totpSetupView{Secret: groupTOTPSecret(setup.secret), QRImage: template.URL(totpQRDataURI(totpURI(creds.username, setup.secret)))}
	}
	a.render(w, http.StatusOK, "security.html", data)
}

// securityNotice is a fixed message shown on the security page.
type securityNotice struct {
	Text  string
	Error bool
}

// securityNotices maps the ?notice= codes used by the security handlers to
// fixed messages. Only codes travel in URLs; unknown codes show nothing,
// so the query string cannot inject text into the page.
var securityNotices = map[string]securityNotice{
	"locked":         {"失败次数过多，当前地址已被临时封禁，请稍后再试。", true},
	"busy":           {"系统繁忙，请稍后重试。", true},
	"verify_failed":  {"暂时无法验证密码，请稍后再试。", true},
	"wrong_password": {"当前密码不正确。", true},
	"tokens_equal":   {"API Token 与媒体 Token 不能相同。", true},
	"changed":        {"凭据已被其他操作修改，请刷新页面后重试。", true},
	"save_failed":    {"保存失败，请稍后再试。", true},
	"unknown_item":   {"未知的凭据项。", true},

	"username_empty":     {"用户名不能为空。", true},
	"username_too_long":  {fmt.Sprintf("用户名不能超过 %d 个字符。", maxAdminUsernameLength), true},
	"username_invalid":   {"用户名不能包含控制字符。", true},
	"username_same":      {"新用户名与当前用户名相同。", true},
	"password_empty":     {"新密码不能为空。", true},
	"password_too_short": {fmt.Sprintf("新密码至少 %d 个字符。", minAdminPasswordLength), true},
	"password_mismatch":  {"两次输入的新密码不一致。", true},
	"password_same":      {"新密码不能与当前密码相同。", true},
	"token_too_short":    {fmt.Sprintf("Token 至少 %d 位。", minCredentialTokenLen), true},
	"token_too_long":     {fmt.Sprintf("Token 不能超过 %d 位。", maxCredentialTokenLen), true},
	"token_invalid":      {"Token 只能包含字母、数字和 - _ . ~。", true},
	"flash_failed":       {"新 Token 已生效但无法显示，请重新生成。", true},
	"reveal_failed":      {"无法显示媒体 Token，请重试。", true},

	"username_changed":      {"用户名已修改，其他登录会话已退出。", false},
	"password_changed":      {"密码已修改，其他登录会话已退出。", false},
	"api_token_updated":     {"API Token 已更新，旧 Token 已失效。", false},
	"api_token_generated":   {"API Token 已重新生成，旧 Token 已失效。", false},
	"media_token_updated":   {"媒体 Token 已更新，旧 Token 已失效。", false},
	"media_token_generated": {"媒体 Token 已重新生成，旧 Token 已失效。", false},
	"media_token_revealed":  {"当前媒体 Token 已显示，刷新页面后隐藏。", false},

	"username_reset":    {"用户名已恢复为环境变量。其他登录会话已退出。", false},
	"password_reset":    {"密码已恢复为环境变量。其他登录会话已退出。", false},
	"api_token_reset":   {"API Token 已恢复为环境变量。", false},
	"media_token_reset": {"媒体 Token 已恢复为环境变量。", false},
	"username_env":      {"用户名已在使用环境变量。", true},
	"password_env":      {"密码已在使用环境变量。", true},
	"api_token_env":     {"API Token 已在使用环境变量。", true},
	"media_token_env":   {"媒体 Token 已在使用环境变量。", true},

	"totp_setup_started":   {"请用身份验证器扫描二维码（或手动输入密钥），然后输入 6 位验证码完成启用。", false},
	"totp_setup_expired":   {"两步验证设置已过期，请重新开始。", true},
	"totp_setup_cancelled": {"已取消启用两步验证。", false},
	"totp_code_wrong":      {"验证码不正确或已使用，请等待下一个验证码后重试。", true},
	"totp_enabled":         {"两步验证已启用，其他登录会话已退出。", false},
	"totp_disabled":        {"两步验证已停用，其他登录会话已退出。", false},
	"totp_already":         {"两步验证已经启用。", true},
	"totp_not_enabled":     {"两步验证尚未启用。", true},

	"ban_removed":        {"已解除封禁，该地址的失败记录已清零。", false},
	"ban_not_found":      {"该地址未被封禁。", true},
	"allow_added":        {"已加入白名单。", false},
	"allow_removed":      {"已从白名单移除。", false},
	"allow_invalid":      {"请输入有效的 IP 地址或 CIDR（如 192.168.1.0/24）。", true},
	"allow_too_broad":    {"范围过大：IPv4 至少 /8，IPv6 至少 /16。", true},
	"allow_exists":       {"该范围已在白名单中。", true},
	"allow_full":         {fmt.Sprintf("白名单最多 %d 条。", maxLoginAllowlist), true},
	"allow_note_invalid": {fmt.Sprintf("备注不能超过 %d 个字符，且不能包含控制字符。", maxLoginAllowNote), true},
	"allow_not_found":    {"白名单中没有该条目。", true},
}

// securityRedirect answers a security form with 303 and a notice code.
func securityRedirect(w http.ResponseWriter, r *http.Request, code string) {
	redirectWithNotice(w, r, securityPagePath, code)
}

// beginSensitiveOperation enforces CSRF and re-verifies the current admin
// password. Failures count against the login limiter (same IP+username and
// IP-only keys as the login form) and are delayed like failed logins. It
// returns the credential snapshot the password was verified against; the
// caller must save with it as "expected" so a concurrent change made in
// between is refused. When it returns false the response has been written.
func (a *App) beginSensitiveOperation(w http.ResponseWriter, r *http.Request, operation string) (*credentials, bool) {
	return a.beginSensitiveOperationAt(w, r, operation, securityPagePath)
}
func (a *App) beginSensitiveOperationAt(w http.ResponseWriter, r *http.Request, operation, target string) (*credentials, bool) {
	return a.beginSensitiveOperationOpts(w, r, operation, target, true)
}

// beginSensitiveOperationOpts: with clearLimiter false a correct password
// leaves earlier failures counted; the caller clears them once its own
// additional check (e.g. a two-factor code) passed as well.
func (a *App) beginSensitiveOperationOpts(w http.ResponseWriter, r *http.Request, operation, target string, clearLimiter bool) (*credentials, bool) {
	setNoStore(w)
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return nil, false
	}
	creds := a.currentCredentials()
	if locked, _ := a.loginLimiter.lockedFor(r, creds.username); locked {
		a.logger.Warn("sensitive admin operation while locked out", "operation", operation, "session", sessionLogID(r), "remoteAddr", r.RemoteAddr)
		redirectWithNotice(w, r, target, "locked")
		return nil, false
	}
	ok, err := a.checkAdminPassword(r, creds, r.PostFormValue("current_password"))
	if errors.Is(err, errPasswordBusy) {
		redirectWithNotice(w, r, target, "busy")
		return nil, false
	}
	if err != nil {
		a.logger.Error("verify admin password", "operation", operation, "error", err)
		redirectWithNotice(w, r, target, "verify_failed")
		return nil, false
	}
	if !ok {
		a.loginLimiter.recordFailureFor(r, creds.username)
		time.Sleep(loginFailureDelay)
		a.logger.Warn("sensitive admin operation rejected: wrong current password", "operation", operation, "session", sessionLogID(r), "remoteAddr", r.RemoteAddr)
		redirectWithNotice(w, r, target, "wrong_password")
		return nil, false
	}
	if clearLimiter {
		a.loginLimiter.recordSuccessFor(r, creds.username)
	}
	return creds, true
}

// credentialSaveFailed maps a save error to a notice code.
func (a *App) credentialSaveFailed(w http.ResponseWriter, r *http.Request, item string, err error) {
	switch {
	case errors.Is(err, errCredentialTokensEqual):
		securityRedirect(w, r, "tokens_equal")
	case errors.Is(err, errCredentialsChanged):
		securityRedirect(w, r, "changed")
	default:
		a.logger.Error("save admin credential", "item", item, "error", err)
		securityRedirect(w, r, "save_failed")
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
func (a *App) saveLoginCredentials(w http.ResponseWriter, r *http.Request, item string, expected *credentials, set map[string]string, remove []string, code string) {
	actor := sessionLogID(r)
	if _, err := a.credentials.updateLogin(r.Context(), a.sessions, w, expected, set, remove); err != nil {
		a.credentialSaveFailed(w, r, item, err)
		return
	}
	a.logger.Info("admin credential changed; other admin sessions revoked", "item", item, "session", actor)
	securityRedirect(w, r, code)
}

func (a *App) handleSecurityUsername(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperation(w, r, "username")
	if !ok {
		return
	}
	username, problem := normalizeAdminUsername(r.PostFormValue("new_username"))
	if problem == "" && username == creds.username {
		problem = "username_same"
	}
	if problem != "" {
		securityRedirect(w, r, problem)
		return
	}
	a.saveLoginCredentials(w, r, "username", creds, map[string]string{storage.CredentialAdminUsername: username}, nil, "username_changed")
}

func (a *App) handleSecurityPassword(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperation(w, r, "password")
	if !ok {
		return
	}
	password := r.PostFormValue("new_password")
	if problem := validateNewAdminPassword(password, a.config.DevMode); problem != "" {
		securityRedirect(w, r, problem)
		return
	}
	if password != r.PostFormValue("confirm_password") {
		securityRedirect(w, r, "password_mismatch")
		return
	}
	same, err := a.checkAdminPassword(r, creds, password)
	if errors.Is(err, errPasswordBusy) {
		securityRedirect(w, r, "busy")
		return
	}
	if err != nil {
		a.logger.Error("compare new admin password", "error", err)
		securityRedirect(w, r, "verify_failed")
		return
	}
	if same {
		securityRedirect(w, r, "password_same")
		return
	}
	encoded, err := hashPassword(r.Context(), password)
	if errors.Is(err, errPasswordBusy) {
		securityRedirect(w, r, "busy")
		return
	}
	if err != nil {
		a.logger.Error("hash admin password", "error", err)
		securityRedirect(w, r, "save_failed")
		return
	}
	a.saveLoginCredentials(w, r, "password", creds, map[string]string{storage.CredentialAdminPasswordHash: encoded}, nil, "password_changed")
}

// handleTokenChange implements the API and media token forms: an empty
// custom_token generates a new token (flashed once), otherwise the custom
// token is validated and saved (never echoed back). item doubles as the
// notice code prefix.
func (a *App) handleTokenChange(w http.ResponseWriter, r *http.Request, item, overrideKey, label string, stored func(token string) string) {
	creds, ok := a.beginSensitiveOperation(w, r, item)
	if !ok {
		return
	}
	token, generated, problem := chooseCredentialToken(r.PostFormValue("custom_token"))
	if problem != "" {
		securityRedirect(w, r, problem)
		return
	}
	if !a.saveTokenCredentials(w, r, item, creds, map[string]string{overrideKey: stored(token)}, nil) {
		return
	}
	if !generated {
		securityRedirect(w, r, item+"_updated")
		return
	}
	if !a.sessions.setFlash(r, securityFlash{Label: "新的 " + label, Value: token}) {
		securityRedirect(w, r, "flash_failed")
		return
	}
	securityRedirect(w, r, item+"_generated")
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
		securityRedirect(w, r, "changed")
		return
	}
	if !a.sessions.setFlash(r, securityFlash{Label: "当前媒体 Token", Value: creds.mediaToken}) {
		securityRedirect(w, r, "reveal_failed")
		return
	}
	a.logger.Info("admin credential revealed", "item", "media_token", "session", sessionLogID(r))
	securityRedirect(w, r, "media_token_revealed")
}

func (a *App) handleSecurityReset(w http.ResponseWriter, r *http.Request) {
	creds, ok := a.beginSensitiveOperation(w, r, "reset")
	if !ok {
		return
	}
	target, known := securityResetKeys[r.PathValue("key")]
	if !known {
		securityRedirect(w, r, "unknown_item")
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
		securityRedirect(w, r, target.code+"_env")
		return
	}
	item := "reset_" + target.override
	remove := []string{target.override}
	if target.override == storage.CredentialAdminUsername || target.override == storage.CredentialAdminPasswordHash {
		a.saveLoginCredentials(w, r, item, creds, nil, remove, target.code+"_reset")
		return
	}
	if a.saveTokenCredentials(w, r, item, creds, nil, remove) {
		securityRedirect(w, r, target.code+"_reset")
	}
}
