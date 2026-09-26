package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
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
	loginLimiter    *loginLimiter
	scanner         *scanner.Manager
	enrichment      *enrichment.Manager
	startEnrichment func(context.Context, enrichmentRunRequest) (storage.EnrichmentRun, error)
	assets          *assetRegistry
	transcoder      *transcodeManager
	thumbnails      *thumbnailManager
	similaritySlots chan struct{}
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

// Chrome carries the shared page-frame fields into every admin template:
// the logged-in user, the session CSRF token and the active navigation key.
// Page data structs embed it so templates keep using .Username/.CSRFToken
// while the shared layout partial can highlight navigation via .Nav.
type Chrome struct {
	Username  string
	CSRFToken string
	Nav       string
}

func chromeFor(session adminSession, nav string) Chrome {
	return Chrome{Username: session.Username, CSRFToken: session.CSRFToken, Nav: nav}
}

// indexLetters is the shared letter index used by the library index bar and
// the template helper.
var indexLetters = []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z", "あ", "か", "さ", "た", "な", "は", "ま", "や", "ら", "わ", "#"}

type dashboardPageData struct {
	Chrome
	Version        string
	Uptime         string
	DatabaseStatus string
	LibraryName    string
	MusicDirectory string
	Statistics     storage.Statistics
	Scan           storage.ScanJob
	Notice         string
}

func NewApp(cfg config.Config, store *storage.Store, scannerManager *scanner.Manager, enrichmentManager *enrichment.Manager, logger *slog.Logger, version string) (*App, error) {
	assets, err := newAssetRegistry(webFiles, "assets")
	if err != nil {
		return nil, err
	}

	templates, err := template.New("admin").Funcs(template.FuncMap{
		"formatDurationMillis":  formatDurationMillis,
		"firstGenre":            firstGenre,
		"formatAdminTime":       formatAdminTime,
		"formatTime":            formatTime,
		"albumTypeLabel":        albumTypeLabel,
		"workTypeLabel":         workTypeLabel,
		"playbackStateLabel":    playbackStateLabel,
		"enrichmentRunProgress": enrichmentRunProgress,
		"enrichmentTargetLabel": enrichmentTargetLabel,
		"scanStatusLabel":       scanStatusLabel,
		"indexValues": func() []string {
			return indexLetters
		},
		// asset* emit content-addressed asset URLs so browsers can cache them
		// forever; appBuild exposes the build hash as a meta tag used by the
		// PJAX layer to detect deployments. No-arg variants are used instead
		// of {{asset "name"}} because html/template's context escaper breaks
		// when several attribute actions carry string literals.
		"assetTokensCSS":    func() string { return assets.assetURL("tokens.css") },
		"assetBaseCSS":      func() string { return assets.assetURL("base.css") },
		"assetShellCSS":     func() string { return assets.assetURL("shell.css") },
		"assetPagesCSS":     func() string { return assets.assetURL("pages.css") },
		"assetIconsSVG":     func() string { return assets.assetURL("icons.svg") },
		"assetNavJS":        func() string { return assets.assetURL("navigation.js") },
		"assetPlayerMainJS": func() string { return assets.assetURL("player-main.js") },
		"assetAdminJS":      func() string { return assets.assetURL("admin.js") },
		"assetBrowseJS":     func() string { return assets.assetURL("browse.js") },
		"appBuild":          func() string { return assets.hash },
		// thumb appends a thumbnail size parameter to an artwork or artist
		// image URL. Empty URLs stay empty so {{if}} guards keep working.
		"thumb": thumbURL,
	}).ParseFS(webFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse admin templates: %w", err)
	}

	return &App{
		similaritySlots: make(chan struct{}, 4),
		config:          cfg,
		store:           store,
		logger:          logger,
		version:         version,
		startedAt:       time.Now(),
		templates:       templates,
		sessions:        newSessionManager(cfg.CookieSecure, store),
		loginLimiter:    newLoginLimiter(),
		scanner:         scannerManager,
		enrichment:      enrichmentManager,
		transcoder:      newTranscodeManager(cfg, logger),
		thumbnails:      newThumbnailManager(cfg),
		assets:          assets,
	}, nil
}

func (a *App) CancelTranscodes() { a.transcoder.CancelAll() }
func (a *App) WaitTranscodes()   { a.transcoder.Wait() }

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", a.handleHealth)
	mux.Handle("GET /api/v1/status", a.requireAPIOrAdmin(http.HandlerFunc(a.handleStatus)))
	mux.Handle("GET /api/v1/capabilities", a.requireAPIOrAdmin(http.HandlerFunc(a.handleCapabilities)))
	mux.Handle("GET /api/v1/artists", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIArtists)))
	mux.Handle("GET /api/v1/artists/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIArtist)))
	mux.Handle("PUT /api/v1/artists/{id}/favorite", a.requireAPIOrAdmin(http.HandlerFunc(a.handleSetArtistFavorite)))
	mux.Handle("DELETE /api/v1/artists/{id}/favorite", a.requireAPIOrAdmin(http.HandlerFunc(a.handleUnsetArtistFavorite)))
	mux.Handle("GET /api/v1/favorites/artists", a.requireAPIOrAdmin(http.HandlerFunc(a.handleFavoriteArtists)))
	mux.Handle("GET /api/v1/works", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIWorks)))
	mux.Handle("POST /api/v1/works", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPICreateWork)))
	mux.Handle("GET /api/v1/works/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIWork)))
	mux.Handle("PATCH /api/v1/works/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIUpdateWork)))
	mux.Handle("DELETE /api/v1/works/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIDeleteWork)))
	mux.Handle("GET /api/v1/works/{id}/tracks", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIWorkTracks)))
	mux.Handle("POST /api/v1/works/{id}/tracks", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAddWorkTrack)))
	mux.Handle("DELETE /api/v1/works/{id}/tracks/{trackId}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIRemoveWorkTrack)))
	mux.Handle("GET /api/v1/albums", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAlbums)))
	mux.Handle("GET /api/v1/albums/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAlbum)))
	mux.Handle("GET /api/v1/tracks", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPITracks)))
	mux.Handle("GET /api/v1/tracks/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPITrack)))
	mux.Handle("GET /api/v1/tracks/{id}/similar", a.requireAPIOrAdmin(a.withSimilaritySlot(http.HandlerFunc(a.handleSimilarTracks))))
	mux.Handle("GET /api/v1/tracks/path", a.requireAPIOrAdmin(a.withSimilaritySlot(http.HandlerFunc(a.handleTrackPath))))
	mux.Handle("GET /api/v1/sync/albums", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPISyncAlbums)))
	mux.Handle("GET /api/v1/sync/tracks", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPISyncTracks)))
	mux.Handle("GET /api/v1/tracks/sync", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPISyncTracks)))
	mux.Handle("PATCH /api/v1/artists/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIUpdateArtist)))
	mux.Handle("PATCH /api/v1/albums/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIUpdateAlbum)))
	mux.Handle("PATCH /api/v1/tracks/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIUpdateTrack)))
	mux.Handle("PUT /api/v1/albums/{id}/favorite", a.requireAPIOrAdmin(http.HandlerFunc(a.handleSetAlbumFavorite)))
	mux.Handle("DELETE /api/v1/albums/{id}/favorite", a.requireAPIOrAdmin(http.HandlerFunc(a.handleUnsetAlbumFavorite)))
	mux.Handle("PUT /api/v1/tracks/{id}/favorite", a.requireAPIOrAdmin(http.HandlerFunc(a.handleSetTrackFavorite)))
	mux.Handle("DELETE /api/v1/tracks/{id}/favorite", a.requireAPIOrAdmin(http.HandlerFunc(a.handleUnsetTrackFavorite)))
	mux.Handle("GET /api/v1/favorites/albums", a.requireAPIOrAdmin(http.HandlerFunc(a.handleFavoriteAlbums)))
	mux.Handle("GET /api/v1/favorites/tracks", a.requireAPIOrAdmin(http.HandlerFunc(a.handleFavoriteTracks)))
	mux.Handle("GET /api/v1/playlists", a.requireAPIOrAdmin(http.HandlerFunc(a.handlePlaylists)))
	mux.Handle("POST /api/v1/playlists", a.requireAPIOrAdmin(http.HandlerFunc(a.handleCreatePlaylist)))
	mux.Handle("GET /api/v1/playlists/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handlePlaylist)))
	mux.Handle("PATCH /api/v1/playlists/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleUpdatePlaylist)))
	mux.Handle("DELETE /api/v1/playlists/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleDeletePlaylist)))
	mux.Handle("PUT /api/v1/playlists/{id}/items", a.requireAPIOrAdmin(http.HandlerFunc(a.handleReplacePlaylistItems)))
	mux.Handle("POST /api/v1/playback/timeline", a.requireAPIOrAdmin(http.HandlerFunc(a.handlePlaybackTimeline)))
	mux.Handle("POST /api/v1/playback/scrobble", a.requireAPIOrAdmin(http.HandlerFunc(a.handlePlaybackScrobble)))
	mux.Handle("GET /api/v1/playback/history", a.requireAPIOrAdmin(http.HandlerFunc(a.handlePlaybackHistory)))
	mux.Handle("DELETE /api/v1/playback/history", a.requireAPIOrAdmin(http.HandlerFunc(a.handleClearPlaybackHistory)))
	mux.Handle("GET /api/v1/tracks/{id}/lyrics.lrc", a.requireMediaAccess(http.HandlerFunc(a.handleAPITrackLyricsText)))
	mux.Handle("GET /api/v1/tracks/{id}/lyrics", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPITrackLyrics)))
	for _, format := range []string{"mp3", "ogg", "flac"} {
		mux.Handle("GET /api/v1/tracks/{id}/transcode."+format, a.requireMediaAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleTranscode(w, r, format) })))
	}
	mux.Handle("GET /api/v1/tracks/{id}/stream", a.requireMediaAccess(http.HandlerFunc(a.handleStream)))
	mux.Handle("GET /api/v1/artwork/{id}", a.requireMediaAccess(http.HandlerFunc(a.handleArtwork)))
	mux.Handle("GET /api/v1/artists/{id}/image", a.requireMediaAccess(http.HandlerFunc(a.handleArtistImage)))
	mux.Handle("POST /api/v1/enrichment/run", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIStartEnrichment)))
	mux.Handle("GET /api/v1/enrichment/jobs", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIEnrichmentRuns)))
	mux.Handle("GET /api/v1/enrichment/jobs/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIEnrichmentRun)))
	mux.Handle("GET /api/v1/enrichment/works/{workId}/candidates", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIWorkEnrichmentCandidates)))
	mux.Handle("POST /api/v1/enrichment/works/{workId}/candidates/{candidateId}/accept", a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIWorkCandidateDecision(w, r, "confirmed") })))
	mux.Handle("POST /api/v1/enrichment/works/{workId}/candidates/{candidateId}/reject", a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIWorkCandidateDecision(w, r, "rejected") })))
	mux.Handle("GET /api/v1/enrichment/artists/{artistId}/relations", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIArtistRelationCandidates)))
	mux.Handle("POST /api/v1/enrichment/artists/{artistId}/relations/{candidateId}/accept", a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIArtistRelationDecision(w, r, "confirmed") })))
	mux.Handle("POST /api/v1/enrichment/artists/{artistId}/relations/{candidateId}/reject", a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIArtistRelationDecision(w, r, "rejected") })))

	mux.Handle("GET /admin/assets/", http.HandlerFunc(a.assets.serveAssets))
	mux.HandleFunc("GET /admin/login", a.handleLoginPage)
	mux.HandleFunc("POST /admin/login", a.handleLogin)
	mux.Handle("POST /admin/logout", a.requireAdmin(http.HandlerFunc(a.handleLogout)))
	mux.Handle("GET /admin", a.requireAdmin(http.HandlerFunc(a.handleDashboard)))
	mux.Handle("GET /admin/status", a.requireAdmin(http.HandlerFunc(a.handleAdminStatus)))
	mux.Handle("GET /admin/options/artists", a.requireAdminJSON(http.HandlerFunc(a.handleAdminArtistOptions)))
	mux.Handle("GET /admin/options/albums", a.requireAdminJSON(http.HandlerFunc(a.handleAdminAlbumOptions)))
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

	// The /api/v1 subtree goes through apiFallback so OPTIONS preflights
	// reach the CORS logic (ServeMux method patterns would otherwise answer
	// a bare 405 before any middleware runs). The fallback forwards every
	// non-OPTIONS request that matches a registered route to the main mux,
	// so all routing and auth behavior for known endpoints is unchanged.
	root := http.NewServeMux()
	root.Handle("/api/v1/", a.apiFallback(mux))
	// A bare /api/v1 (no trailing slash) keeps the pre-P1 behavior: a plain
	// 404, not the subtree redirect ServeMux would otherwise synthesize.
	root.Handle("/api/v1", http.HandlerFunc(http.NotFound))
	root.Handle("/", mux)

	return a.recoverPanic(a.securityHeaders(a.logRequests(a.gzipResponse(root))))
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

	username := r.FormValue("username")
	key := loginKey(r, username)
	if locked, remaining := a.loginLimiter.locked(key); locked {
		a.logger.Warn("login attempt while locked out", "username", username, "remoteAddr", r.RemoteAddr)
		a.render(w, http.StatusTooManyRequests, "login.html", loginPageData{Error: fmt.Sprintf("失败次数过多,请 %d 分钟后再试。", int(remaining.Minutes())+1)})
		return
	}

	usernameOK := secureEqual(username, a.config.AdminUsername)
	passwordOK := secureEqual(r.FormValue("password"), a.config.AdminPassword)
	if !usernameOK || !passwordOK {
		a.loginLimiter.recordFailure(key)
		time.Sleep(250 * time.Millisecond)
		a.render(w, http.StatusUnauthorized, "login.html", loginPageData{Error: "用户名或密码不正确。"})
		return
	}
	a.loginLimiter.recordSuccess(key)

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
		Chrome:         chromeFor(session, "console"),
		Version:        a.version,
		Uptime:         time.Since(a.startedAt).Round(time.Second).String(),
		DatabaseStatus: "正常",
		LibraryName:    a.config.LibraryName,
		MusicDirectory: a.config.MusicDirectory,
		Statistics:     stats,
		Scan:           latestScan(a.store, r),
		Notice:         r.URL.Query().Get("notice"),
	})
}

func (a *App) requireAPIOrAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, X-CSRF-Token")
		// No OPTIONS branch here: apiFallback intercepts every /api/v1
		// preflight before the route-specific middleware runs.
		// A valid Bearer API token is a self-contained, non-ambient
		// credential: it bypasses the session CSRF check entirely and must
		// be honoured even when a browser session cookie rides along.
		if scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
			if secureEqual(token, a.config.APIToken) {
				next.ServeHTTP(w, r)
				return
			}
			// An invalid Bearer attempt must not fall through to ambient
			// session auth; otherwise a cross-site page could attach a forged
			// Authorization header and still ride the victim's session.
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "A valid API token is required.")
			return
		}
		if session, ok := a.sessions.get(r); ok {
			// Browser session writes are cross-site forgeable, so state
			// changing requests must prove they came from the admin UI by
			// echoing the per-session token from <meta name="csrf-token">.
			// Bearer-token clients are unaffected: the token itself is the
			// credential and is never ambient.
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				if !secureEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
					writeAPIError(w, http.StatusForbidden, "csrf_invalid", "A valid X-CSRF-Token header is required for session-authenticated writes.")
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		a.requireAPIToken(next).ServeHTTP(w, r)
	})
}

func (a *App) requireMediaAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Range, Content-Type, Accept")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
		// No OPTIONS branch here either: apiFallback answers preflights.
		if _, ok := a.sessions.get(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		tokenQuery := r.URL.Query().Get("mediaToken")
		if tokenQuery == "" {
			tokenQuery = r.URL.Query().Get("token")
		}
		// Only the dedicated media token is accepted here. The full-access
		// API token must never be usable on media endpoints: media URLs are
		// routinely shared and leak into browser history, proxy logs and
		// Referer headers.
		if tokenQuery != "" && secureEqual(tokenQuery, a.config.MediaToken) {
			next.ServeHTTP(w, r)
			return
		}
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if ok && strings.EqualFold(scheme, "Bearer") && secureEqual(token, a.config.MediaToken) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "A valid media token is required.")
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

// requireAdminJSON is requireAdmin for fetch()-consumed admin endpoints: an
// unauthenticated request gets a 401 JSON error instead of a login redirect
// (which fetch would otherwise follow into an HTML page).
func (a *App) requireAdminJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.sessions.get(r); !ok {
			writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Admin session required.")
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

// apiFallback owns the /api/v1 subtree's edge cases:
//   - OPTIONS preflights get the union of the CORS allow-lists the API and
//     media middlewares use (they carry no credentials and must succeed so
//     browsers will send the real request).
//   - A request whose path and method both match a registered route is
//     forwarded to the main mux unchanged.
//   - A path that exists under other methods gets a JSON 405 with an Allow
//     header, mirroring what ServeMux itself would have produced.
//   - Anything else is an unknown path: it passes through the standard auth
//     middleware and ends at a JSON 404.
func (a *App) apiFallback(apiMux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, Range, X-CSRF-Token")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if _, pattern := apiMux.Handler(r); pattern != "" {
			apiMux.ServeHTTP(w, r)
			return
		}
		allowed := []string{}
		hasGet := false
		for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
			probe := r.Clone(r.Context())
			probe.Method = method
			if _, pattern := apiMux.Handler(probe); pattern != "" {
				allowed = append(allowed, method)
				if method == "GET" {
					hasGet = true
				}
			}
		}
		if len(allowed) > 0 {
			if hasGet {
				allowed = append(allowed, "HEAD")
			}
			allowed = append(allowed, "OPTIONS")
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			w.Header().Set("Access-Control-Allow-Origin", "*")
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The requested method is not allowed for this resource.")
			return
		}
		a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, http.StatusNotFound, "not_found", "The requested API resource does not exist.")
		})).ServeHTTP(w, r)
	})
}

var renderBufferPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// render executes the template into a pooled buffer first. A template error
// then produces a clean 500 instead of a half-written page after the status
// line has already been committed.
func (a *App) render(w http.ResponseWriter, status int, name string, data any) {
	buffer := renderBufferPool.Get().(*bytes.Buffer)
	buffer.Reset()
	defer putRenderBuffer(buffer)
	if err := a.templates.ExecuteTemplate(buffer, name, data); err != nil {
		a.logger.Error("render admin template", "template", name, "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buffer.WriteTo(w)
}

// putRenderBuffer returns a buffer to the pool unless it grew beyond 256KB;
// retaining oversized buffers would pin memory after a one-off large page.
func putRenderBuffer(buffer *bytes.Buffer) {
	if buffer.Cap() > 256*1024 {
		return
	}
	renderBufferPool.Put(buffer)
}

func (a *App) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		fields := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"durationMs", time.Since(started).Milliseconds(),
		}
		if quietAccessLogPath(r.URL.Path) {
			a.logger.Debug("http request", fields...)
			return
		}
		a.logger.Info("http request", fields...)
	})
}

// quietAccessLogPath keeps high-frequency asset, artwork and polling
// requests out of the Info log so real events stay visible.
func quietAccessLogPath(path string) bool {
	if strings.HasPrefix(path, "/admin/assets/") || strings.HasPrefix(path, "/api/v1/artwork/") || path == "/admin/status" {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/artists/") && strings.HasSuffix(path, "/image") {
		return true
	}
	return false
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' data: blob:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func panicAsError(value any) error { err, _ := value.(error); return err }
func (a *App) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if errors.Is(panicAsError(recovered), http.ErrAbortHandler) {
					panic(http.ErrAbortHandler)
				}
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

// thumbURL appends a thumbnail size parameter to an artwork or artist
// image URL. Empty URLs stay empty so template {{if}} guards keep working.
func thumbURL(rawURL string, size int) string {
	if rawURL == "" {
		return ""
	}
	separator := "?"
	if strings.Contains(rawURL, "?") {
		separator = "&"
	}
	return rawURL + separator + "size=" + strconv.Itoa(size)
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
