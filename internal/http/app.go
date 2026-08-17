package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/enrichment"
	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

type App struct {
	config          config.Config
	store           *storage.Store
	logger          *slog.Logger
	version         string
	startedAt       time.Time
	templates       *template.Template
	sessions        *sessionManager
	scanner         *scanner.Manager
	enrichment      *enrichment.Manager
	startEnrichment func(context.Context, enrichmentRunRequest) (storage.EnrichmentRun, error)
	assets          http.Handler
}

type healthResponse struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	Time          string `json:"time"`
	UptimeSeconds int64  `json:"uptimeSeconds"`
	Database      string `json:"database"`
}

type statusResponse struct {
	healthResponse
	Statistics storage.Statistics `json:"statistics"`
}

type loginPageData struct {
	Error string
}

type dashboardPageData struct {
	Username       string
	CSRFToken      string
	Version        string
	Uptime         string
	DatabaseStatus string
	LibraryName    string
	MusicDirectory string
	Statistics     storage.Statistics
	Scan           storage.ScanJob
}

func NewApp(cfg config.Config, store *storage.Store, scannerManager *scanner.Manager, enrichmentManager *enrichment.Manager, logger *slog.Logger, version string) (*App, error) {
	templates, err := template.New("admin").Funcs(template.FuncMap{
		"formatDurationMillis":  formatDurationMillis,
		"formatAdminTime":       formatAdminTime,
		"playbackStateLabel":    playbackStateLabel,
		"enrichmentRunProgress": enrichmentRunProgress,
		"enrichmentTargetLabel": enrichmentTargetLabel,
		"indexValues": func() []string {
			return []string{"あ", "か", "さ", "た", "な", "は", "ま", "や", "ら", "わ", "A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z", "#"}
		},
	}).ParseFS(webFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse admin templates: %w", err)
	}

	assetFS, err := fs.Sub(webFiles, "assets")
	if err != nil {
		return nil, fmt.Errorf("load admin assets: %w", err)
	}

	return &App{
		config:     cfg,
		store:      store,
		logger:     logger,
		version:    version,
		startedAt:  time.Now(),
		templates:  templates,
		sessions:   newSessionManager(cfg.CookieSecure),
		scanner:    scannerManager,
		enrichment: enrichmentManager,
		assets:     http.StripPrefix("/admin/assets/", http.FileServer(http.FS(assetFS))),
	}, nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", a.handleHealth)
	mux.Handle("GET /api/v1/status", a.requireAPIToken(http.HandlerFunc(a.handleStatus)))
	mux.Handle("GET /api/v1/capabilities", a.requireAPIToken(http.HandlerFunc(a.handleCapabilities)))
	mux.Handle("GET /api/v1/artists", a.requireAPIToken(http.HandlerFunc(a.handleAPIArtists)))
	mux.Handle("GET /api/v1/works", a.requireAPIToken(http.HandlerFunc(a.handleAPIWorks)))
	mux.Handle("POST /api/v1/works", a.requireAPIToken(http.HandlerFunc(a.handleAPICreateWork)))
	mux.Handle("GET /api/v1/works/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPIWork)))
	mux.Handle("PATCH /api/v1/works/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPIUpdateWork)))
	mux.Handle("DELETE /api/v1/works/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPIDeleteWork)))
	mux.Handle("GET /api/v1/works/{id}/tracks", a.requireAPIToken(http.HandlerFunc(a.handleAPIWorkTracks)))
	mux.Handle("POST /api/v1/works/{id}/tracks", a.requireAPIToken(http.HandlerFunc(a.handleAPIAddWorkTrack)))
	mux.Handle("DELETE /api/v1/works/{id}/tracks/{trackId}", a.requireAPIToken(http.HandlerFunc(a.handleAPIRemoveWorkTrack)))
	mux.Handle("GET /api/v1/albums", a.requireAPIToken(http.HandlerFunc(a.handleAPIAlbums)))
	mux.Handle("GET /api/v1/albums/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPIAlbum)))
	mux.Handle("GET /api/v1/tracks", a.requireAPIToken(http.HandlerFunc(a.handleAPITracks)))
	mux.Handle("GET /api/v1/tracks/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPITrack)))
	mux.Handle("GET /api/v1/sync/albums", a.requireAPIToken(http.HandlerFunc(a.handleAPISyncAlbums)))
	mux.Handle("GET /api/v1/sync/tracks", a.requireAPIToken(http.HandlerFunc(a.handleAPISyncTracks)))
	mux.Handle("GET /api/v1/tracks/sync", a.requireAPIToken(http.HandlerFunc(a.handleAPISyncTracks)))
	mux.Handle("PATCH /api/v1/artists/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPIUpdateArtist)))
	mux.Handle("PATCH /api/v1/albums/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPIUpdateAlbum)))
	mux.Handle("PATCH /api/v1/tracks/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPIUpdateTrack)))
	mux.Handle("PUT /api/v1/albums/{id}/favorite", a.requireAPIToken(http.HandlerFunc(a.handleSetAlbumFavorite)))
	mux.Handle("DELETE /api/v1/albums/{id}/favorite", a.requireAPIToken(http.HandlerFunc(a.handleUnsetAlbumFavorite)))
	mux.Handle("PUT /api/v1/tracks/{id}/favorite", a.requireAPIToken(http.HandlerFunc(a.handleSetTrackFavorite)))
	mux.Handle("DELETE /api/v1/tracks/{id}/favorite", a.requireAPIToken(http.HandlerFunc(a.handleUnsetTrackFavorite)))
	mux.Handle("GET /api/v1/favorites/albums", a.requireAPIToken(http.HandlerFunc(a.handleFavoriteAlbums)))
	mux.Handle("GET /api/v1/favorites/tracks", a.requireAPIToken(http.HandlerFunc(a.handleFavoriteTracks)))
	mux.Handle("GET /api/v1/playlists", a.requireAPIToken(http.HandlerFunc(a.handlePlaylists)))
	mux.Handle("POST /api/v1/playlists", a.requireAPIToken(http.HandlerFunc(a.handleCreatePlaylist)))
	mux.Handle("GET /api/v1/playlists/{id}", a.requireAPIToken(http.HandlerFunc(a.handlePlaylist)))
	mux.Handle("PATCH /api/v1/playlists/{id}", a.requireAPIToken(http.HandlerFunc(a.handleUpdatePlaylist)))
	mux.Handle("DELETE /api/v1/playlists/{id}", a.requireAPIToken(http.HandlerFunc(a.handleDeletePlaylist)))
	mux.Handle("PUT /api/v1/playlists/{id}/items", a.requireAPIToken(http.HandlerFunc(a.handleReplacePlaylistItems)))
	mux.Handle("POST /api/v1/playback/timeline", a.requireAPIToken(http.HandlerFunc(a.handlePlaybackTimeline)))
	mux.Handle("POST /api/v1/playback/scrobble", a.requireAPIToken(http.HandlerFunc(a.handlePlaybackScrobble)))
	mux.Handle("GET /api/v1/playback/history", a.requireAPIToken(http.HandlerFunc(a.handlePlaybackHistory)))
	mux.Handle("DELETE /api/v1/playback/history", a.requireAPIToken(http.HandlerFunc(a.handleClearPlaybackHistory)))
	mux.Handle("GET /api/v1/tracks/{id}/lyrics", a.requireAPIToken(http.HandlerFunc(a.handleAPITrackLyrics)))
	mux.Handle("GET /api/v1/tracks/{id}/stream", a.requireMediaAccess(http.HandlerFunc(a.handleStream)))
	mux.Handle("GET /api/v1/artwork/{id}", a.requireMediaAccess(http.HandlerFunc(a.handleArtwork)))
	mux.Handle("GET /api/v1/artists/{id}/image", a.requireMediaAccess(http.HandlerFunc(a.handleArtistImage)))
	mux.Handle("POST /api/v1/enrichment/run", a.requireAPIToken(http.HandlerFunc(a.handleAPIStartEnrichment)))
	mux.Handle("GET /api/v1/enrichment/jobs", a.requireAPIToken(http.HandlerFunc(a.handleAPIEnrichmentRuns)))
	mux.Handle("GET /api/v1/enrichment/jobs/{id}", a.requireAPIToken(http.HandlerFunc(a.handleAPIEnrichmentRun)))
	mux.Handle("GET /api/v1/enrichment/works/{workId}/candidates", a.requireAPIToken(http.HandlerFunc(a.handleAPIWorkEnrichmentCandidates)))
	mux.Handle("POST /api/v1/enrichment/works/{workId}/candidates/{candidateId}/accept", a.requireAPIToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIWorkCandidateDecision(w, r, "confirmed") })))
	mux.Handle("POST /api/v1/enrichment/works/{workId}/candidates/{candidateId}/reject", a.requireAPIToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIWorkCandidateDecision(w, r, "rejected") })))
	mux.Handle("GET /api/v1/enrichment/artists/{artistId}/relations", a.requireAPIToken(http.HandlerFunc(a.handleAPIArtistRelationCandidates)))
	mux.Handle("POST /api/v1/enrichment/artists/{artistId}/relations/{candidateId}/accept", a.requireAPIToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIArtistRelationDecision(w, r, "confirmed") })))
	mux.Handle("POST /api/v1/enrichment/artists/{artistId}/relations/{candidateId}/reject", a.requireAPIToken(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIArtistRelationDecision(w, r, "rejected") })))

	mux.Handle("GET /admin/assets/", a.assets)
	mux.HandleFunc("GET /admin/login", a.handleLoginPage)
	mux.HandleFunc("POST /admin/login", a.handleLogin)
	mux.Handle("POST /admin/logout", a.requireAdmin(http.HandlerFunc(a.handleLogout)))
	mux.Handle("GET /admin", a.requireAdmin(http.HandlerFunc(a.handleDashboard)))
	mux.Handle("GET /admin/status", a.requireAdmin(http.HandlerFunc(a.handleAdminStatus)))
	mux.Handle("POST /admin/scan", a.requireAdmin(http.HandlerFunc(a.handleStartScan)))
	mux.Handle("GET /admin/artists", a.requireAdmin(http.HandlerFunc(a.handleArtistsPage)))
	mux.Handle("GET /admin/artists/album", a.requireAdmin(http.HandlerFunc(a.handleAlbumArtistsPage)))
	mux.Handle("GET /admin/artists/track", a.requireAdmin(http.HandlerFunc(a.handleTrackArtistsPage)))
	mux.Handle("GET /admin/artists/{id}", a.requireAdmin(http.HandlerFunc(a.handleArtistPage)))
	mux.Handle("POST /admin/artists/{id}", a.requireAdmin(http.HandlerFunc(a.handleUpdateArtist)))
	mux.Handle("POST /admin/artists/{id}/match", a.requireAdmin(http.HandlerFunc(a.handleMatchArtist)))
	mux.Handle("POST /admin/artists/{id}/biographies/refresh", a.requireAdmin(http.HandlerFunc(a.handleRefreshArtistBiographies)))
	mux.Handle("POST /admin/artists/{id}/biography", a.requireAdmin(http.HandlerFunc(a.handleSelectArtistBiography)))
	mux.Handle("POST /admin/artists/{id}/confirm/{candidate}", a.requireAdmin(http.HandlerFunc(a.handleConfirmArtistMatch)))
	mux.Handle("POST /admin/artists/{id}/reject/{candidate}", a.requireAdmin(http.HandlerFunc(a.handleRejectArtistMatch)))
	mux.Handle("POST /admin/artists/{id}/merge", a.requireAdmin(http.HandlerFunc(a.handleMergeArtist)))
	mux.Handle("GET /admin/settings/metadata", a.requireAdmin(http.HandlerFunc(a.handleMetadataSettings)))
	mux.Handle("POST /admin/settings/metadata", a.requireAdmin(http.HandlerFunc(a.handleSaveMetadataSettings)))
	mux.Handle("POST /admin/matches/run", a.requireAdmin(http.HandlerFunc(a.handleRunArtistMatching)))
	mux.Handle("GET /admin/matches", a.requireAdmin(http.HandlerFunc(a.handleMatchReview)))
	mux.Handle("GET /admin/enrichment", a.requireAdmin(http.HandlerFunc(a.handleAdminEnrichment)))
	mux.Handle("POST /admin/enrichment/run", a.requireAdmin(http.HandlerFunc(a.handleAdminStartEnrichment)))
	mux.Handle("POST /admin/enrichment/works/{workId}/candidates/{candidateId}/accept", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminWorkCandidateDecision(w, r, "confirmed") })))
	mux.Handle("POST /admin/enrichment/works/{workId}/candidates/{candidateId}/reject", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminWorkCandidateDecision(w, r, "rejected") })))
	mux.Handle("POST /admin/enrichment/artists/{artistId}/relations/{candidateId}/accept", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminArtistRelationDecision(w, r, "confirmed") })))
	mux.Handle("POST /admin/enrichment/artists/{artistId}/relations/{candidateId}/reject", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminArtistRelationDecision(w, r, "rejected") })))
	mux.Handle("GET /admin/merges", a.requireAdmin(http.HandlerFunc(a.handleMergeHistory)))
	mux.Handle("POST /admin/merges/{id}/rollback", a.requireAdmin(http.HandlerFunc(a.handleRollbackMerge)))
	mux.Handle("GET /admin/works", a.requireAdmin(http.HandlerFunc(a.handleWorksPage)))
	mux.Handle("POST /admin/works", a.requireAdmin(http.HandlerFunc(a.handleCreateWork)))
	mux.Handle("GET /admin/works/{id}", a.requireAdmin(http.HandlerFunc(a.handleWorkPage)))
	mux.Handle("POST /admin/works/{id}", a.requireAdmin(http.HandlerFunc(a.handleUpdateWork)))
	mux.Handle("POST /admin/works/{id}/delete", a.requireAdmin(http.HandlerFunc(a.handleDeleteWork)))
	mux.Handle("POST /admin/works/{id}/tracks", a.requireAdmin(http.HandlerFunc(a.handleAddWorkTrack)))
	mux.Handle("POST /admin/works/{id}/tracks/{trackId}/remove", a.requireAdmin(http.HandlerFunc(a.handleRemoveWorkTrack)))
	mux.Handle("GET /admin/albums", a.requireAdmin(http.HandlerFunc(a.handleAlbumsPage)))
	mux.Handle("GET /admin/albums/{id}", a.requireAdmin(http.HandlerFunc(a.handleAlbumPage)))
	mux.Handle("POST /admin/albums/{id}", a.requireAdmin(http.HandlerFunc(a.handleUpdateAlbum)))
	mux.Handle("GET /admin/tracks", a.requireAdmin(http.HandlerFunc(a.handleTracksPage)))
	mux.Handle("POST /admin/tracks/{id}", a.requireAdmin(http.HandlerFunc(a.handleUpdateTrack)))
	mux.Handle("GET /admin/favorites", a.requireAdmin(http.HandlerFunc(a.handleAdminFavorites)))
	mux.Handle("POST /admin/favorites/albums/{id}", a.requireAdmin(http.HandlerFunc(a.handleAdminAlbumFavorite)))
	mux.Handle("POST /admin/favorites/tracks/{id}", a.requireAdmin(http.HandlerFunc(a.handleAdminTrackFavorite)))
	mux.Handle("GET /admin/playlists", a.requireAdmin(http.HandlerFunc(a.handleAdminPlaylists)))
	mux.Handle("POST /admin/playlists", a.requireAdmin(http.HandlerFunc(a.handleAdminCreatePlaylist)))
	mux.Handle("GET /admin/playlists/{id}", a.requireAdmin(http.HandlerFunc(a.handleAdminPlaylist)))
	mux.Handle("POST /admin/playlists/{id}", a.requireAdmin(http.HandlerFunc(a.handleAdminUpdatePlaylist)))
	mux.Handle("POST /admin/playlists/{id}/delete", a.requireAdmin(http.HandlerFunc(a.handleAdminDeletePlaylist)))
	mux.Handle("POST /admin/playlists/{id}/tracks", a.requireAdmin(http.HandlerFunc(a.handleAdminAddPlaylistTrack)))
	mux.Handle("POST /admin/playlists/{id}/tracks/{track}/remove", a.requireAdmin(http.HandlerFunc(a.handleAdminRemovePlaylistTrack)))
	mux.Handle("POST /admin/playlists/{id}/tracks/{track}/move", a.requireAdmin(http.HandlerFunc(a.handleAdminMovePlaylistTrack)))
	mux.Handle("GET /admin/playback", a.requireAdmin(http.HandlerFunc(a.handleAdminPlayback)))
	mux.Handle("POST /admin/playback/clear", a.requireAdmin(http.HandlerFunc(a.handleAdminClearPlayback)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})

	return a.recoverPanic(a.securityHeaders(a.logRequests(mux)))
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	response := a.health(r)
	statusCode := http.StatusOK
	if response.Status != "ok" {
		statusCode = http.StatusServiceUnavailable
	}
	writeJSON(w, statusCode, response)
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.Statistics(r.Context())
	if err != nil {
		a.logger.Error("query status statistics", "error", err)
		writeAPIError(w, http.StatusInternalServerError, "statistics_unavailable", "Statistics are currently unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{healthResponse: a.health(r), Statistics: stats})
}

func (a *App) health(r *http.Request) healthResponse {
	databaseStatus := "ok"
	status := "ok"
	if err := a.store.Ping(r.Context()); err != nil {
		databaseStatus = "unavailable"
		status = "degraded"
	}
	return healthResponse{
		Status:        status,
		Version:       a.version,
		Time:          time.Now().UTC().Format(time.RFC3339),
		UptimeSeconds: int64(time.Since(a.startedAt).Seconds()),
		Database:      databaseStatus,
	}
}

func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.sessions.get(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	a.render(w, http.StatusOK, "login.html", loginPageData{})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.render(w, http.StatusBadRequest, "login.html", loginPageData{Error: "无法读取登录信息。"})
		return
	}

	usernameOK := secureEqual(r.FormValue("username"), a.config.AdminUsername)
	passwordOK := secureEqual(r.FormValue("password"), a.config.AdminPassword)
	if !usernameOK || !passwordOK {
		time.Sleep(250 * time.Millisecond)
		a.render(w, http.StatusUnauthorized, "login.html", loginPageData{Error: "用户名或密码不正确。"})
		return
	}

	if _, err := a.sessions.create(w, a.config.AdminUsername); err != nil {
		a.logger.Error("create admin session", "error", err)
		a.render(w, http.StatusInternalServerError, "login.html", loginPageData{Error: "暂时无法创建登录会话。"})
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	session, ok := a.sessions.get(r)
	if !ok || !secureEqual(r.FormValue("csrfToken"), session.CSRFToken) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	a.sessions.delete(w, r)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	stats, err := a.store.Statistics(r.Context())
	if err != nil {
		a.logger.Error("query dashboard statistics", "error", err)
		http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		return
	}

	a.render(w, http.StatusOK, "dashboard.html", dashboardPageData{
		Username:       session.Username,
		CSRFToken:      session.CSRFToken,
		Version:        a.version,
		Uptime:         time.Since(a.startedAt).Round(time.Second).String(),
		DatabaseStatus: "正常",
		LibraryName:    a.config.LibraryName,
		MusicDirectory: a.config.MusicDirectory,
		Statistics:     stats,
		Scan:           latestScan(a.store, r),
	})
}

func (a *App) requireAPIOrAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.sessions.get(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		a.requireAPIToken(next).ServeHTTP(w, r)
	})
}

func (a *App) requireMediaAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.sessions.get(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		if secureEqual(r.URL.Query().Get("mediaToken"), a.config.MediaToken) && r.URL.Query().Get("mediaToken") != "" {
			next.ServeHTTP(w, r)
			return
		}
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if ok && strings.EqualFold(scheme, "Bearer") && secureEqual(token, a.config.APIToken) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "A valid API token or media token is required.")
	})
}

func (a *App) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.sessions.get(r); !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) requireAPIToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || !secureEqual(token, a.config.APIToken) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "A valid API token is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
		a.logger.Error("render admin template", "template", name, "error", err)
	}
}

func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"durationMs", time.Since(started).Milliseconds(),
		)
	})
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("panic recovered", "panic", recovered, "stack", string(debug.Stack()))
				writeAPIError(w, http.StatusInternalServerError, "internal_error", "An internal error occurred.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func secureEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
