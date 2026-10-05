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
	"github.com/lux032/032music-server/internal/lastfm"
	"github.com/lux032/032music-server/internal/scanner"
	"github.com/lux032/032music-server/internal/storage"
)

type App struct {
	// config keeps the raw environment values; effective (possibly
	// admin-overridden) credentials are read from credentials.
	config      config.Config
	credentials *credentialStore
	// afterLoginCredentialsRead is a test hook run by handleLogin right
	// after it read the credentials snapshot; nil in production.
	afterLoginCredentialsRead func()
	store                     *storage.Store
	logger                    *slog.Logger
	version                   string
	startedAt                 time.Time
	templates                 *template.Template
	sessions                  *sessionManager
	loginLimiter              *loginLimiter
	scanner                   *scanner.Manager
	enrichment                *enrichment.Manager
	startEnrichment           func(context.Context, enrichmentRunRequest) (storage.EnrichmentRun, error)
	assets                    *assetRegistry
	transcoder                *transcodeManager
	thumbnails                *thumbnailManager
	// customImageMu serializes custom-image store file -> DB commit ->
	// cleanup sequences (M4) so GC never races an in-flight upload.
	customImageMu   sync.Mutex
	similaritySlots chan struct{}
	// lastfm is the optional Last.fm scrobbler; nil disables submission
	// (plays are still counted locally).
	lastfm *lastfm.Service
}

// SetLastFM attaches the Last.fm scrobbling service.
func (a *App) SetLastFM(service *lastfm.Service) { a.lastfm = service }

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
	// PendingReviewCount 是“作品关联审核”导航角标，每个请求在 chromeFor
	// 里用请求 context 查询一次（不在模板函数里用 context.Background() 查）。
	PendingReviewCount int
}

func (a *App) chromeFor(ctx context.Context, session adminSession, nav string) Chrome {
	chrome := Chrome{Username: session.Username, CSRFToken: session.CSRFToken, Nav: nav}
	total, err := a.store.PendingWorkReviewTotal(ctx)
	if err != nil {
		a.logger.Warn("pending work review count", "error", err)
	}
	chrome.PendingReviewCount = total
	return chrome
}

// indexLetters is the shared letter index used by the library index bar and
// the template helper.
var indexLetters = []string{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z", "あ", "か", "さ", "た", "な", "は", "ま", "や", "ら", "わ", "#"}

// indexLettersWhere returns the indexLetters entries whose kana-ness matches
// kana, preserving order.
func indexLettersWhere(kana bool) []string {
	values := make([]string, 0, len(indexLetters))
	for _, letter := range indexLetters {
		if isKanaIndex(letter) == kana {
			values = append(values, letter)
		}
	}
	return values
}

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
		"albumCard":              albumCardContext,
		"albumSelectionBar":      albumSelectionBarContext,
		"albumKindLabel":         albumKindLabel,
		"formatDurationMillis":   formatDurationMillis,
		"firstGenre":             firstGenre,
		"creditLabel":            creditLabel,
		"creditPeople":           creditPeople,
		"focusLabel":             focusLabel,
		"formatAdminTime":        formatAdminTime,
		"formatTime":             formatAdminTime,
		"albumTypeLabel":         albumTypeLabel,
		"workTypeLabel":          workTypeLabel,
		"workRoleLabel":          workRoleLabel,
		"workRoleShortLabel":     workRoleShortLabel,
		"workRoleBadgeLabel":     workRoleBadgeLabel,
		"workAlbumRelationLabel": workAlbumRelationLabel,
		"workSourceLabel":        workSourceLabel,
		"matchKindLabel":         matchKindLabel,
		"matchKindClass":         matchKindClass,
		"playbackStateLabel":     playbackStateLabel,
		"enrichmentRunProgress":  enrichmentRunProgress,
		"enrichmentTargetLabel":  enrichmentTargetLabel,
		"enrichmentStageLine":    enrichmentStageLine,
		"runStatusLabel":         runStatusLabel,
		"pauseReasonLabel":       pauseReasonLabel,
		"enrichmentScopeLabel":   enrichmentScopeLabel,
		"percentDone":            percentDone,
		"clockOf":                clockOf,
		"waitTotalLabel":         waitTotalLabel,
		"runAutoResumes":         runAutoResumes,
		"add":                    func(a, b int) int { return a + b },
		"mul":                    func(a, b int) int { return a * b },
		"subtract":               func(a, b int) int { return a - b },
		"scanStatusLabel":        scanStatusLabel,
		// indexValues and kanaIndexValues split indexLetters the same way the
		// library index bar does: "#" stays with the always-visible Latin
		// letters, only kana go behind the かな toggle.
		"indexValues": func() []string {
			return indexLettersWhere(false)
		},
		"kanaIndexValues": func() []string {
			return indexLettersWhere(true)
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
		"assetThemeJS":      func() string { return assets.assetURL("theme.js") },
		"appBuild":          func() string { return assets.hash },
		// thumb appends a thumbnail size parameter to an artwork or artist
		// image URL. Empty URLs stay empty so {{if}} guards keep working.
		"thumb": thumbURL,
		// workPoster returns the local poster URL, or "" until it is cached.
		// size > 0 时附带缩略图尺寸参数（批次 8 C3），0 表示原图。
		"workPoster": func(work storage.Work, size int) string { return workPosterURL(enrichmentManager, work, size) },
	}).ParseFS(webFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse admin templates: %w", err)
	}

	credentialCtx, cancelCredentials := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelCredentials()
	credentials, err := newCredentialStore(credentialCtx, cfg, store, logger)
	if err != nil {
		return nil, fmt.Errorf("load credential overrides: %w", err)
	}
	trustedProxies, err := config.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("parse MUSIC_SERVER_TRUSTED_PROXIES: %w", err)
	}
	limiter := newLoginLimiter()
	limiter.trustedProxies = trustedProxies

	return &App{
		similaritySlots: make(chan struct{}, 4),
		config:          cfg,
		credentials:     credentials,
		store:           store,
		logger:          logger,
		version:         version,
		startedAt:       time.Now(),
		templates:       templates,
		sessions:        newSessionManager(cfg.CookieSecure, store),
		loginLimiter:    limiter,
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
	mux.Handle("GET /api/v1/works/{id}/albums", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIWorkAlbums)))
	mux.Handle("POST /api/v1/works/{id}/albums", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAddWorkAlbum)))
	mux.Handle("DELETE /api/v1/works/{id}/albums/{albumId}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIRemoveWorkAlbum)))
	mux.Handle("POST /api/v1/works/{id}/tracks", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAddWorkTrack)))
	mux.Handle("DELETE /api/v1/works/{id}/tracks/{trackId}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIRemoveWorkTrack)))
	mux.Handle("GET /api/v1/albums", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAlbums)))
	mux.Handle("GET /api/v1/albums/{id}", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAlbum)))
	mux.Handle("GET /api/v1/albums/{id}/works", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAlbumWorks)))
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
	mux.Handle("POST /api/v1/playlists/{id}/items/append", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAppendPlaylistItems)))
	mux.Handle("POST /api/v1/playlists/{id}/items/remove", a.requireAPIOrAdmin(http.HandlerFunc(a.handleRemovePlaylistItems)))
	mux.Handle("POST /api/v1/playlists/{id}/items/insert", a.requireAPIOrAdmin(http.HandlerFunc(a.handleInsertPlaylistItem)))
	mux.Handle("PUT /api/v1/playlists/{id}/order", a.requireAPIOrAdmin(http.HandlerFunc(a.handlePlaylistOrder)))
	mux.Handle("PUT /api/v1/playlists/{id}/artwork", a.requireAPIOrAdmin(http.HandlerFunc(a.handleUploadPlaylistArtwork)))
	mux.Handle("DELETE /api/v1/playlists/{id}/artwork", a.requireAPIOrAdmin(http.HandlerFunc(a.handleResetPlaylistArtwork)))
	mux.Handle("GET /api/v1/playlists/{id}/artwork", a.requireMediaAccess(http.HandlerFunc(a.handlePlaylistArtwork)))

	mux.Handle("POST /api/v1/playback/events", a.requirePlaybackReport(http.HandlerFunc(a.handlePlaybackEvents)))
	// Legacy playback endpoints were removed without a compatibility path
	// (apiRevision 3): they answer 410 so old clients fail loudly.
	mux.Handle("/api/v1/playback/timeline", http.HandlerFunc(handlePlaybackGone))
	mux.Handle("/api/v1/playback/scrobble", http.HandlerFunc(handlePlaybackGone))
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
	mux.Handle("POST /api/v1/enrichment/jobs/{id}/cancel", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPICancelEnrichment)))
	mux.Handle("GET /api/v1/enrichment/albums/{albumId}/subjects", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIAlbumSubjectCandidates)))
	mux.Handle("POST /api/v1/enrichment/albums/{albumId}/subjects/{candidateId}/accept", a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIAlbumSubjectDecision(w, r, true) })))
	mux.Handle("POST /api/v1/enrichment/albums/{albumId}/subjects/{candidateId}/reject", a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPIAlbumSubjectDecision(w, r, false) })))
	mux.Handle("GET /api/v1/enrichment/tracks/{trackId}/subjects", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPITrackSubjectCandidates)))
	mux.Handle("POST /api/v1/enrichment/tracks/{trackId}/subjects/{candidateId}/accept", a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPITrackSubjectDecision(w, r, true) })))
	mux.Handle("POST /api/v1/enrichment/tracks/{trackId}/subjects/{candidateId}/reject", a.requireAPIOrAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAPITrackSubjectDecision(w, r, false) })))
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
	mux.Handle("GET /admin/options/works", a.requireAdminJSON(http.HandlerFunc(a.handleAdminWorkOptions)))
	mux.Handle("GET /admin/options/series", a.requireAdminJSON(http.HandlerFunc(a.handleAdminSeriesOptions)))
	mux.Handle("POST /admin/scan", a.requireAdmin(http.HandlerFunc(a.handleStartScan)))
	mux.Handle("GET /admin/credits/{id}", a.requireAdmin(http.HandlerFunc(a.handleCreditArtistPage)))
	mux.Handle("GET /admin/credits", a.requireAdmin(http.HandlerFunc(a.handleCreditArtistsPage)))
	mux.Handle("GET /admin/artists", a.requireAdmin(http.HandlerFunc(a.handleArtistsPage)))
	mux.Handle("GET /admin/artists/album", a.requireAdmin(http.HandlerFunc(a.handleAlbumArtistsPage)))
	mux.Handle("GET /admin/artists/track", a.requireAdmin(http.HandlerFunc(a.handleTrackArtistsPage)))
	mux.Handle("GET /api/v1/artists/{id}/credits", a.requireAPIOrAdmin(http.HandlerFunc(a.handleAPIArtistCredits)))
	mux.Handle("GET /admin/artists/{id}", a.requireAdmin(http.HandlerFunc(a.handleArtistPage)))
	mux.Handle("POST /admin/artists/{id}", a.requireAdmin(http.HandlerFunc(a.handleUpdateArtist)))
	mux.Handle("POST /admin/artists/{id}/match", a.requireAdmin(http.HandlerFunc(a.handleMatchArtist)))
	mux.Handle("POST /admin/artists/{id}/biographies/refresh", a.requireAdmin(http.HandlerFunc(a.handleRefreshArtistBiographies)))
	mux.Handle("POST /admin/artists/{id}/biography", a.requireAdmin(http.HandlerFunc(a.handleSelectArtistBiography)))
	mux.Handle("POST /admin/artists/{id}/image", a.requireAdmin(http.HandlerFunc(a.handleUploadArtistImage)))
	mux.Handle("POST /admin/artists/{id}/image/reset", a.requireAdmin(http.HandlerFunc(a.handleResetArtistImage)))
	mux.Handle("POST /admin/artists/{id}/confirm/{candidate}", a.requireAdmin(http.HandlerFunc(a.handleConfirmArtistMatch)))
	mux.Handle("POST /admin/artists/{id}/identity-conflict/{candidate}/merge", a.requireAdmin(http.HandlerFunc(a.handleMergeIdentityConflict)))
	mux.Handle("POST /admin/artists/{id}/identity/reset", a.requireAdmin(http.HandlerFunc(a.handleResetArtistIdentity)))
	mux.Handle("POST /admin/artists/{id}/reject/{candidate}", a.requireAdmin(http.HandlerFunc(a.handleRejectArtistMatch)))
	mux.Handle("POST /admin/artists/{id}/merge", a.requireAdmin(http.HandlerFunc(a.handleMergeArtist)))
	mux.Handle("GET /admin/settings/metadata", a.requireAdmin(http.HandlerFunc(a.handleMetadataSettings)))
	mux.Handle("POST /admin/settings/metadata", a.requireAdmin(http.HandlerFunc(a.handleSaveMetadataSettings)))
	mux.Handle("POST /admin/settings/lastfm-scrobble", a.requireAdmin(http.HandlerFunc(a.handleSaveLastFMScrobble)))
	mux.Handle("POST /admin/settings/lastfm-scrobble/connect", a.requireAdmin(http.HandlerFunc(a.handleConnectLastFM)))
	mux.Handle("POST /admin/settings/lastfm-scrobble/complete", a.requireAdmin(http.HandlerFunc(a.handleCompleteLastFM)))
	mux.Handle("GET /admin/settings/lastfm-scrobble/callback", a.requireAdmin(http.HandlerFunc(a.handleLastFMCallback)))
	mux.Handle("POST /admin/settings/lastfm-scrobble/disconnect", a.requireAdmin(http.HandlerFunc(a.handleDisconnectLastFM)))
	mux.Handle("POST /admin/settings/lastfm-scrobble/retry", a.requireAdmin(http.HandlerFunc(a.handleRetryLastFM)))
	mux.Handle("GET /admin/settings/security", a.requireAdmin(http.HandlerFunc(a.handleSecurityPage)))
	mux.Handle("POST /admin/settings/security/username", a.requireAdmin(http.HandlerFunc(a.handleSecurityUsername)))
	mux.Handle("POST /admin/settings/security/password", a.requireAdmin(http.HandlerFunc(a.handleSecurityPassword)))
	mux.Handle("POST /admin/settings/security/api-token", a.requireAdmin(http.HandlerFunc(a.handleSecurityAPIToken)))
	mux.Handle("POST /admin/settings/security/media-token", a.requireAdmin(http.HandlerFunc(a.handleSecurityMediaToken)))
	mux.Handle("POST /admin/settings/security/media-token/reveal", a.requireAdmin(http.HandlerFunc(a.handleSecurityRevealMediaToken)))
	mux.Handle("POST /admin/settings/security/reset/{key}", a.requireAdmin(http.HandlerFunc(a.handleSecurityReset)))
	mux.Handle("POST /admin/matches/run", a.requireAdmin(http.HandlerFunc(a.handleRunArtistMatching)))
	mux.Handle("POST /admin/matches/runs/{id}/cancel", a.requireAdmin(http.HandlerFunc(a.handleCancelArtistMatching)))
	mux.Handle("POST /admin/matches/runs/{id}/pause", a.requireAdmin(http.HandlerFunc(a.handlePauseArtistRun)))
	mux.Handle("POST /admin/matches/runs/{id}/resume", a.requireAdmin(http.HandlerFunc(a.handleResumeArtistRun)))
	mux.Handle("GET /admin/matches/runs/active.json", a.requireAdmin(http.HandlerFunc(a.handleActiveArtistRun)))
	mux.Handle("GET /admin/matches/runs", a.requireAdmin(http.HandlerFunc(a.handleArtistRunList)))
	mux.Handle("GET /admin/matches/runs/{id}", a.requireAdmin(http.HandlerFunc(a.handleArtistRunDetail)))

	mux.Handle("POST /admin/matches/images/backfill", a.requireAdmin(http.HandlerFunc(a.handleStartArtistImageBackfill)))
	mux.Handle("POST /admin/matches/images/runs/{id}/pause", a.requireAdmin(http.HandlerFunc(a.handlePauseArtistImageBackfill)))
	mux.Handle("POST /admin/matches/images/runs/{id}/resume", a.requireAdmin(http.HandlerFunc(a.handleResumeArtistImageBackfill)))
	mux.Handle("POST /admin/matches/images/runs/{id}/cancel", a.requireAdmin(http.HandlerFunc(a.handleCancelArtistImageBackfill)))
	mux.Handle("GET /admin/matches/images/runs/active.json", a.requireAdmin(http.HandlerFunc(a.handleActiveArtistImageBackfill)))
	mux.Handle("POST /admin/matches/biographies/backfill", a.requireAdmin(http.HandlerFunc(a.handleStartArtistBiographyBackfill)))
	mux.Handle("POST /admin/matches/biographies/runs/{id}/pause", a.requireAdmin(http.HandlerFunc(a.handlePauseArtistBiographyBackfill)))
	mux.Handle("POST /admin/matches/biographies/runs/{id}/resume", a.requireAdmin(http.HandlerFunc(a.handleResumeArtistBiographyBackfill)))
	mux.Handle("POST /admin/matches/biographies/runs/{id}/cancel", a.requireAdmin(http.HandlerFunc(a.handleCancelArtistBiographyBackfill)))
	mux.Handle("GET /admin/matches/biographies/runs/active.json", a.requireAdmin(http.HandlerFunc(a.handleActiveArtistBiographyBackfill)))

	mux.Handle("GET /admin/matches", a.requireAdmin(http.HandlerFunc(a.handleMatchReview)))
	mux.Handle("GET /admin/work-review", a.requireAdmin(http.HandlerFunc(a.handleAdminWorkReview)))
	mux.Handle("GET /admin/enrichment", a.requireAdmin(http.HandlerFunc(a.handleAdminEnrichment)))
	mux.Handle("POST /admin/enrichment/run", a.requireAdmin(http.HandlerFunc(a.handleAdminStartEnrichment)))
	mux.Handle("POST /admin/enrichment/runs/{id}/cancel", a.requireAdmin(http.HandlerFunc(a.handleAdminCancelEnrichment)))
	mux.Handle("POST /admin/enrichment/runs/{id}/pause", a.requireAdmin(http.HandlerFunc(a.handlePauseEnrichmentRun)))
	mux.Handle("POST /admin/enrichment/runs/{id}/resume", a.requireAdmin(http.HandlerFunc(a.handleResumeEnrichmentRun)))
	mux.Handle("GET /admin/enrichment/runs/active.json", a.requireAdmin(http.HandlerFunc(a.handleActiveEnrichmentRun)))
	mux.Handle("GET /admin/enrichment/runs", a.requireAdmin(http.HandlerFunc(a.handleEnrichmentRunList)))
	mux.Handle("GET /admin/enrichment/runs/{id}", a.requireAdmin(http.HandlerFunc(a.handleEnrichmentRunDetail)))

	mux.Handle("POST /admin/enrichment/posters/backfill", a.requireAdmin(http.HandlerFunc(a.handleWorkPosterBackfill)))
	mux.Handle("POST /admin/enrichment/albums/{albumId}/subjects/{candidateId}/accept", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminAlbumSubjectDecision(w, r, true) })))
	mux.Handle("POST /admin/enrichment/albums/{albumId}/subjects/{candidateId}/reject", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminAlbumSubjectDecision(w, r, false) })))
	mux.Handle("POST /admin/enrichment/albums/{albumId}/subjects/search", a.requireAdmin(http.HandlerFunc(a.handleAdminAlbumSubjectSearch)))
	mux.Handle("POST /admin/enrichment/tracks/{trackId}/subjects/{candidateId}/accept", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminTrackSubjectDecision(w, r, true) })))
	mux.Handle("POST /admin/enrichment/tracks/{trackId}/subjects/{candidateId}/reject", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminTrackSubjectDecision(w, r, false) })))
	mux.Handle("POST /admin/enrichment/works/{workId}/candidates/{candidateId}/accept", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminWorkCandidateDecision(w, r, "confirmed") })))
	mux.Handle("POST /admin/enrichment/works/{workId}/candidates/reject-all", a.requireAdmin(http.HandlerFunc(a.handleAdminWorkCandidatesRejectAll)))
	mux.Handle("POST /admin/enrichment/works/{workId}/candidates/{candidateId}/reject", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminWorkCandidateDecision(w, r, "rejected") })))
	mux.Handle("POST /admin/enrichment/artists/{artistId}/relations/{candidateId}/accept", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminArtistRelationDecision(w, r, "confirmed") })))
	mux.Handle("POST /admin/enrichment/artists/{artistId}/relations/{candidateId}/reject", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminArtistRelationDecision(w, r, "rejected") })))
	mux.Handle("GET /admin/merges", a.requireAdmin(http.HandlerFunc(a.handleMergeHistory)))
	mux.Handle("POST /admin/merges/{id}/rollback", a.requireAdmin(http.HandlerFunc(a.handleRollbackMerge)))
	mux.Handle("GET /admin/works", a.requireAdmin(http.HandlerFunc(a.handleWorksPage)))
	mux.Handle("POST /admin/works", a.requireAdmin(http.HandlerFunc(a.handleCreateWork)))
	mux.Handle("GET /admin/works/{id}", a.requireAdmin(http.HandlerFunc(a.handleWorkPage)))
	mux.Handle("GET /admin/works/{id}/poster", a.requireAdmin(http.HandlerFunc(a.handleWorkPoster)))
	mux.Handle("POST /admin/works/{id}", a.requireAdmin(http.HandlerFunc(a.handleUpdateWork)))
	mux.Handle("POST /admin/works/{id}/bangumi", a.requireAdmin(http.HandlerFunc(a.handleManualWorkBangumi)))
	mux.Handle("POST /admin/works/{id}/delete", a.requireAdmin(http.HandlerFunc(a.handleDeleteWork)))
	mux.Handle("POST /admin/works/{id}/albums", a.requireAdmin(http.HandlerFunc(a.handleAddWorkAlbum)))
	mux.Handle("POST /admin/works/{id}/albums/{albumId}/remove", a.requireAdmin(http.HandlerFunc(a.handleRemoveWorkAlbum)))
	mux.Handle("POST /admin/works/refresh", a.requireAdmin(http.HandlerFunc(a.handleRefreshWorks)))
	mux.Handle("POST /admin/works/{id}/tracks", a.requireAdmin(http.HandlerFunc(a.handleAddWorkTrack)))
	mux.Handle("POST /admin/works/{id}/tracks/{trackId}/remove", a.requireAdmin(http.HandlerFunc(a.handleRemoveWorkTrack)))
	mux.Handle("POST /admin/works/{id}/series", a.requireAdmin(http.HandlerFunc(a.handleAddWorkSeries)))
	mux.Handle("POST /admin/works/{id}/series/detach", a.requireAdmin(http.HandlerFunc(a.handleDetachWorkSeries)))
	mux.Handle("POST /admin/series/{id}/rename", a.requireAdmin(http.HandlerFunc(a.handleRenameWorkSeries)))
	mux.Handle("POST /admin/series/{id}/dissolve", a.requireAdmin(http.HandlerFunc(a.handleDissolveWorkSeries)))
	mux.Handle("GET /admin/series", a.requireAdmin(http.HandlerFunc(a.handleAdminSeriesList)))
	mux.Handle("POST /admin/series", a.requireAdmin(http.HandlerFunc(a.handleAdminSeriesCreate)))
	mux.Handle("GET /admin/series/{id}", a.requireAdmin(http.HandlerFunc(a.handleAdminSeriesDetail)))
	mux.Handle("POST /admin/series/{id}/members", a.requireAdmin(http.HandlerFunc(a.handleAdminSeriesAddMember)))
	mux.Handle("POST /admin/series/{id}/members/{workId}/remove", a.requireAdmin(http.HandlerFunc(a.handleAdminSeriesRemoveMember)))
	mux.Handle("POST /admin/series/{id}/merge", a.requireAdmin(http.HandlerFunc(a.handleAdminSeriesMerge)))
	mux.Handle("POST /admin/work-review/series-suggestions/{id}/accept", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminSeriesSuggestionDecision(w, r, true) })))
	mux.Handle("POST /admin/work-review/series-suggestions/{id}/reject", a.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.handleAdminSeriesSuggestionDecision(w, r, false) })))
	mux.Handle("GET /admin/albums", a.requireAdmin(http.HandlerFunc(a.handleAlbumsPage)))
	mux.Handle("POST /admin/albums/merge", a.requireAdmin(http.HandlerFunc(a.handleMergeAlbums)))
	mux.Handle("POST /admin/albums/delete", a.requireAdmin(http.HandlerFunc(a.handleDeleteAlbums)))
	mux.Handle("GET /admin/albums/{id}", a.requireAdmin(http.HandlerFunc(a.handleAlbumPage)))
	mux.Handle("POST /admin/albums/{id}", a.requireAdmin(http.HandlerFunc(a.handleUpdateAlbum)))
	mux.Handle("POST /admin/albums/{id}/works", a.requireAdmin(http.HandlerFunc(a.handleAddAlbumWork)))
	mux.Handle("POST /admin/albums/{id}/artwork", a.requireAdmin(http.HandlerFunc(a.handleUploadAlbumArtwork)))
	mux.Handle("POST /admin/albums/{id}/artwork/reset", a.requireAdmin(http.HandlerFunc(a.handleResetAlbumArtwork)))
	mux.Handle("POST /admin/albums/{id}/works/{workId}/remove", a.requireAdmin(http.HandlerFunc(a.handleRemoveAlbumWork)))
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
	// Only fixed messages keyed by a short code are shown, so the query
	// string cannot inject arbitrary text into the login page.
	a.render(w, http.StatusOK, "login.html", loginPageData{Error: loginNotices[r.URL.Query().Get("notice")]})
}

// loginNotices are the 303 redirect notices of handleLogin.
var loginNotices = map[string]string{
	"busy":  "系统繁忙，请稍后重试。",
	"retry": "登录状态已变更，请重试。",
}

// loginUsernameLogID identifies a submitted username in logs without
// recording the (possibly mistyped password-like) raw value.
func loginUsernameLogID(username string) string {
	return sessionTokenHash(username)[:12]
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		a.render(w, http.StatusBadRequest, "login.html", loginPageData{Error: "无法读取登录信息。"})
		return
	}

	username := r.FormValue("username")
	if locked, remaining := a.loginLimiter.lockedFor(r, username); locked {
		a.logger.Warn("login attempt while locked out", "usernameHash", loginUsernameLogID(username), "remoteAddr", r.RemoteAddr)
		a.render(w, http.StatusTooManyRequests, "login.html", loginPageData{Error: fmt.Sprintf("失败次数过多,请 %d 分钟后再试。", int(remaining.Minutes())+1)})
		return
	}

	// Order matters: read the session generation BEFORE the credentials.
	// A username/password change publishes the new credentials before it
	// bumps the generation, so a login that authenticates against the old
	// credentials always holds the old generation and createAt refuses it.
	generation := a.sessions.currentGeneration()
	creds := a.currentCredentials()
	if a.afterLoginCredentialsRead != nil {
		a.afterLoginCredentialsRead()
	}
	usernameOK := secureEqual(username, creds.username)
	// The password is always checked, even for a wrong username, so the
	// response time does not reveal whether the username exists.
	passwordOK, err := a.checkAdminPassword(r, creds, r.FormValue("password"))
	if errors.Is(err, errPasswordBusy) {
		http.Redirect(w, r, "/admin/login?notice=busy", http.StatusSeeOther)
		return
	}
	if err != nil {
		a.logger.Error("verify admin password", "error", err)
		a.render(w, http.StatusServiceUnavailable, "login.html", loginPageData{Error: "暂时无法验证登录信息,请稍后再试。"})
		return
	}
	if !usernameOK || !passwordOK {
		a.loginLimiter.recordFailureFor(r, username)
		time.Sleep(loginFailureDelay)
		a.render(w, http.StatusUnauthorized, "login.html", loginPageData{Error: "用户名或密码不正确。"})
		return
	}
	a.loginLimiter.recordSuccessFor(r, username)

	if _, err := a.sessions.createAt(w, creds.username, generation); err != nil {
		if errors.Is(err, errSessionsRevoked) {
			http.Redirect(w, r, "/admin/login?notice=retry", http.StatusSeeOther)
			return
		}
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
	if err := a.sessions.delete(w, r); err != nil {
		a.logger.Error("delete admin session", "error", err)
		http.Error(w, "logout unavailable", http.StatusInternalServerError)
		return
	}
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
		Chrome:         a.chromeFor(r.Context(), session, "console"),
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
			if a.currentCredentials().apiTokenMatches(token) {
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

// requirePlaybackReport guards the playback reporting endpoints (timeline and
// scrobble). Besides the API token and the admin session it accepts the media
// token — as a Bearer header or a mediaToken/token query parameter — so
// players that only hold the media token (the one embedded in their stream
// URLs) can still record plays. The media token grants nothing else here:
// these endpoints only update play statistics and the Last.fm outbox.
func (a *App) requirePlaybackReport(next http.Handler) http.Handler {
	apiOrAdmin := a.requireAPIOrAdmin(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		creds := a.currentCredentials()
		mediaOK := false
		if scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
			mediaOK = token != "" && !creds.apiTokenMatches(token) && creds.mediaTokenMatches(token)
		} else if r.Header.Get("Authorization") == "" {
			tokenQuery := r.URL.Query().Get("mediaToken")
			if tokenQuery == "" {
				tokenQuery = r.URL.Query().Get("token")
			}
			mediaOK = tokenQuery != "" && creds.mediaTokenMatches(tokenQuery)
		}
		if mediaOK {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, X-CSRF-Token")
			next.ServeHTTP(w, r)
			return
		}
		apiOrAdmin.ServeHTTP(w, r)
	})
}

func (a *App) requireMediaAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Range, Content-Type, Accept")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
		// No OPTIONS branch here either: apiFallback answers preflights.
		// The media token is checked once, when the request starts: rotating
		// it from the admin page rejects new requests immediately but does
		// not cut off a stream that is already being served.
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
		creds := a.currentCredentials()
		if tokenQuery != "" && creds.mediaTokenMatches(tokenQuery) {
			next.ServeHTTP(w, r)
			return
		}
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if ok && strings.EqualFold(scheme, "Bearer") && token != "" && creds.mediaTokenMatches(token) {
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
		if !ok || !strings.EqualFold(scheme, "Bearer") || !a.currentCredentials().apiTokenMatches(token) {
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
	// Admin pages carry per-session data (CSRF token, one-shot security
	// flashes): never let the browser or a proxy keep a copy. no-store also
	// keeps these pages out of the back/forward cache.
	w.Header().Set("Cache-Control", "no-store")
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
		if quietAccessLogPath(r.URL.Path) || r.Method == http.MethodGet && r.URL.Path == "/admin/login" {
			a.logger.Debug("http request", fields...)
			return
		}
		a.logger.Info("http request", fields...)
	})
}

// quietAccessLogPath keeps high-frequency asset, artwork and polling
// requests out of the Info log so real events stay visible.
func quietAccessLogPath(path string) bool {
	if strings.HasPrefix(path, "/admin/assets/") || strings.HasPrefix(path, "/api/v1/artwork/") || path == "/admin/status" || path == "/api/v1/health" || path == "/api/v1/playback/events" || path == "/admin/matches/runs/active.json" || path == "/admin/matches/images/runs/active.json" || path == "/admin/matches/biographies/runs/active.json" || path == "/admin/enrichment/runs/active.json" {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/playlists/") && strings.HasSuffix(path, "/artwork") {
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
