package httpapi

import "net/http"

func (a *App) handleIdentityRebuild(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if a.scanner == nil {
		http.Error(w, "scanner unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, err := a.scanner.StartIdentityRebuild(r.Context()); err != nil {
		redirectWithNotice(w, r, "/admin", "身份重建未启动：扫描或身份重建已在运行")
		return
	}
	redirectWithNotice(w, r, "/admin", "身份重建已启动，请刷新查看进度")
}
