package httpapi

import (
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"
)

// loginProtectionView is the "登录保护" part of the security page.
type loginProtectionView struct {
	// ClientIP is the address this request is attributed to (after
	// MUSIC_SERVER_TRUSTED_PROXIES), so a misconfigured proxy is visible.
	ClientIP          string
	ClientAllowlisted bool
	Bans              []loginBanView
	EnvAllow          []string
	PageAllow         []loginAllowView
	Schedule          string
	MaxFailures       int
	MaxIPFailures     int
	MaxAllow          int
	MaxNote           int
	DecayDays         int
}

type loginBanView struct {
	IP        string
	Strikes   int
	Until     string // RFC 3339 UTC, localized in the browser
	Remaining string
	Current   bool
}

type loginAllowView struct {
	Prefix    string
	Note      string
	CreatedAt string
}

func (a *App) loginProtectionFor(r *http.Request) loginProtectionView {
	limiter := a.loginLimiter
	now := time.Now()
	clientIP := limiter.clientIP(r)
	clientKey := banKeyFor(clientIP)
	view := loginProtectionView{
		ClientIP:          clientIP,
		ClientAllowlisted: limiter.allowlisted(clientIP),
		Schedule:          loginBanScheduleText(),
		MaxFailures:       loginMaxFailures,
		MaxIPFailures:     loginIPMaxFailures,
		MaxAllow:          maxLoginAllowlist,
		MaxNote:           maxLoginAllowNote,
		DecayDays:         int(loginStrikeDecay / (24 * time.Hour)),
	}
	for _, ban := range limiter.activeBans(now) {
		view.Bans = append(view.Bans, loginBanView{
			IP:        ban.IP,
			Strikes:   ban.Strikes,
			Until:     ban.BannedUntil.UTC().Format(time.RFC3339),
			Remaining: humanDuration(ban.BannedUntil.Sub(now)),
			Current:   ban.IP == clientKey,
		})
	}
	env, page := limiter.allowlists()
	for _, prefix := range env {
		view.EnvAllow = append(view.EnvAllow, prefix.String())
	}
	for _, entry := range page {
		view.PageAllow = append(view.PageAllow, loginAllowView{Prefix: entry.Prefix, Note: entry.Note, CreatedAt: entry.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return view
}

// handleSecurityUnban lifts a ban and forgives its strikes. Only CSRF is
// required: lifting a ban grants nothing beyond the ability to try again.
func (a *App) handleSecurityUnban(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	key := strings.TrimSpace(r.PostFormValue("ip"))
	existed, err := a.loginLimiter.forgive(key)
	if err != nil {
		a.logger.Error("lift login ban", "ip", key, "error", err)
		securityRedirect(w, r, "save_failed")
		return
	}
	if !existed {
		securityRedirect(w, r, "ban_not_found")
		return
	}
	a.logger.Info("login ban lifted by admin", "ip", key, "session", sessionLogID(r))
	securityRedirect(w, r, "ban_removed")
}

// handleSecurityAllowAdd adds an allowlist entry. It weakens protection,
// so the current password is required like other credential changes.
func (a *App) handleSecurityAllowAdd(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.beginSensitiveOperation(w, r, "login_allowlist_add"); !ok {
		return
	}
	prefix, problem := parseAllowPrefix(r.PostFormValue("prefix"))
	if problem != "" {
		securityRedirect(w, r, problem)
		return
	}
	note := strings.TrimSpace(r.PostFormValue("note"))
	if utf8.RuneCountInString(note) > maxLoginAllowNote || hasControlCharacter(note) {
		securityRedirect(w, r, "allow_note_invalid")
		return
	}
	code, err := a.loginLimiter.addAllow(r.Context(), prefix, note)
	if err != nil {
		a.logger.Error("add login allowlist entry", "prefix", prefix.String(), "error", err)
		securityRedirect(w, r, "save_failed")
		return
	}
	if code != "" {
		securityRedirect(w, r, code)
		return
	}
	a.logger.Info("login allowlist entry added", "prefix", prefix.String(), "session", sessionLogID(r))
	securityRedirect(w, r, "allow_added")
}

func (a *App) handleSecurityAllowRemove(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(r.PostFormValue("prefix")))
	if err != nil {
		securityRedirect(w, r, "allow_not_found")
		return
	}
	removed, err := a.loginLimiter.removeAllow(r.Context(), prefix.Masked())
	if err != nil {
		a.logger.Error("remove login allowlist entry", "prefix", prefix.String(), "error", err)
		securityRedirect(w, r, "save_failed")
		return
	}
	if !removed {
		securityRedirect(w, r, "allow_not_found")
		return
	}
	a.logger.Info("login allowlist entry removed", "prefix", prefix.String(), "session", sessionLogID(r))
	securityRedirect(w, r, "allow_removed")
}
