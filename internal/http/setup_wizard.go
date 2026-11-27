package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/lux032/032music-server/internal/acquisition/jackett"
	"github.com/lux032/032music-server/internal/acquisition/lanhttp"
	"github.com/lux032/032music-server/internal/acquisition/qbit"
	"github.com/lux032/032music-server/internal/aiclient"
	"github.com/lux032/032music-server/internal/config"
	"github.com/lux032/032music-server/internal/lastfm"
	"github.com/lux032/032music-server/internal/listenbrainz"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

const setupWizardPath = "/admin/setup"

type dashboardSetupView struct {
	Status           string
	ShowResumeBanner bool
	CompletedSteps   int
	CurrentStep      int
	MissingLabels    string
}

type setupPageData struct {
	CSRFToken     string
	BootstrapJSON template.JS
}

type setupLibraryPreflight struct {
	Directory       string `json:"directory"`
	OK              bool   `json:"ok"`
	Status          string `json:"status"` // ok | empty | missing | unreadable
	SubdirCount     int    `json:"subdirCount"`
	AudioFileSample int    `json:"audioFileSample"`
	Truncated       bool   `json:"truncated"`
	Message         string `json:"message"`
}

type setupBootstrapPayload struct {
	State      storage.SetupWizardState `json:"state"`
	Deployment struct {
		MusicDirectory      string `json:"musicDirectory"`
		DataDirectory       string `json:"dataDirectory"`
		LibraryName         string `json:"libraryName"`
		BackupTime          string `json:"backupTime"`
		BackupKeepDaily     int    `json:"backupKeepDaily"`
		AnalyzerConfigured  bool   `json:"analyzerConfigured"`
		ProxyPeer           string `json:"proxyPeer"`
		MediaTokenEphemeral bool   `json:"mediaTokenEphemeral"`
	} `json:"deployment"`
	Preflight setupLibraryPreflight `json:"preflight"`
	Step1     struct {
		MusicBrainzEnabled bool   `json:"musicbrainzEnabled"`
		MusicBrainzContact string `json:"musicbrainzContact"`
		MusicBrainzAuto    bool   `json:"musicbrainzAuto"`
		BangumiEnabled     bool   `json:"bangumiEnabled"`
		BangumiAuto        bool   `json:"bangumiAuto"`
		LastFMEnabled      bool   `json:"lastfmEnabled"`
		LastFMKeySet       bool   `json:"lastfmKeySet"`
		BioTargetLanguage  string `json:"bioTargetLanguage"`
		WikipediaEnabled   bool   `json:"wikipediaEnabled"`
		FanartEnabled      bool   `json:"fanartEnabled"`
		FanartKeySet       bool   `json:"fanartKeySet"`
		CacheDays          int    `json:"cacheDays"`
		ImgBangumi         bool   `json:"imgBangumi"`
		ImgDeezer          bool   `json:"imgDeezer"`
		ImgWikipedia       bool   `json:"imgWikipedia"`
	} `json:"step1"`
	Step2 struct {
		BaseURL           string `json:"baseUrl"`
		KeySet            bool   `json:"keySet"`
		Model             string `json:"model"`
		ReviewModel       string `json:"reviewModel"`
		FallbackModel     string `json:"fallbackModel"`
		ProxySet          bool   `json:"proxySet"`
		RPM               int    `json:"rpm"`
		Concurrency       int    `json:"concurrency"`
		ReasoningEffort   string `json:"reasoningEffort"`
		VisionModel       string `json:"visionModel"`
		WebSearchProvider string `json:"webSearchProvider"`
		TavilyKeyCount    int    `json:"tavilyKeyCount"`
		DeepSeekKeySet    bool   `json:"deepSeekKeySet"`
		WebSearchReady    bool   `json:"webSearchReady"`
		WebSearchSummary  string `json:"webSearchSummary"`
		LLMConfigured     bool   `json:"llmConfigured"`
	} `json:"step2"`
	Step3 struct {
		AIReviewEnabled    bool   `json:"aiReviewEnabled"`
		AIReviewMode       string `json:"aiReviewMode"`
		AIReviewWeb        bool   `json:"aiReviewWeb"`
		AliasEnabled       bool   `json:"aliasEnabled"`
		AvatarWebAIEnabled bool   `json:"avatarWebAiEnabled"`
		BioAIEnabled       bool   `json:"bioAiEnabled"`
		LyricsTransEnabled bool   `json:"lyricsTransEnabled"`
		DailyMixEnabled    bool   `json:"dailyMixEnabled"`
		DailyMixLLMTitles  bool   `json:"dailyMixLlmTitles"`
		AITaggingEnabled   bool   `json:"aiTaggingEnabled"`
		AcousticEnabled    bool   `json:"acousticEnabled"`
	} `json:"step3"`
	Step4 struct {
		LBEnabled           bool   `json:"lbEnabled"`
		LBServerURL         string `json:"lbServerUrl"`
		LBTokenSet          bool   `json:"lbTokenSet"`
		LBConnected         bool   `json:"lbConnected"`
		LBUsername          string `json:"lbUsername"`
		LBNowPlaying        bool   `json:"lbNowPlaying"`
		LBLoveSync          bool   `json:"lbLoveSync"`
		LFMScrobbleEnabled  bool   `json:"lfmScrobbleEnabled"`
		LFMSecretSet        bool   `json:"lfmSecretSet"`
		LFMConnected        bool   `json:"lfmConnected"`
		LFMUsername         string `json:"lfmUsername"`
		LFMNowPlaying       bool   `json:"lfmNowPlaying"`
		LFMLoveSync         bool   `json:"lfmLoveSync"`
		LFMPendingAuthURL   string `json:"lfmPendingAuthUrl"`
		AcqEnabled          bool   `json:"acqEnabled"`
		JackettURL          string `json:"jackettUrl"`
		JackettIndexer      string `json:"jackettIndexer"`
		JackettKeySet       bool   `json:"jackettKeySet"`
		QBURL               string `json:"qbUrl"`
		QBUsername          string `json:"qbUsername"`
		QBPasswordSet       bool   `json:"qbPasswordSet"`
		QBCategory          string `json:"qbCategory"`
	} `json:"step4"`
	Step5 struct {
		WatchEnabled         bool `json:"watchEnabled"`
		WatchIntervalSeconds int  `json:"watchIntervalSeconds"`
	} `json:"step5"`
}

func (a *App) registerSetupRoutes(mux *http.ServeMux) {
	mux.Handle("GET "+setupWizardPath, a.requireAdmin(http.HandlerFunc(a.handleSetupPage)))
	mux.Handle("GET "+setupWizardPath+"/preflight/library", a.requireAdminJSON(http.HandlerFunc(a.handleSetupPreflightLibrary)))
	mux.Handle("POST "+setupWizardPath+"/step/1", a.requireAdminJSON(http.HandlerFunc(a.handleSetupStep1)))
	mux.Handle("POST "+setupWizardPath+"/step/2", a.requireAdminJSON(http.HandlerFunc(a.handleSetupStep2)))
	mux.Handle("POST "+setupWizardPath+"/step/3", a.requireAdminJSON(http.HandlerFunc(a.handleSetupStep3)))
	mux.Handle("POST "+setupWizardPath+"/step/4", a.requireAdminJSON(http.HandlerFunc(a.handleSetupStep4)))
	mux.Handle("POST "+setupWizardPath+"/complete", a.requireAdminJSON(http.HandlerFunc(a.handleSetupComplete)))
	mux.Handle("POST "+setupWizardPath+"/skip", a.requireAdminJSON(http.HandlerFunc(a.handleSetupSkip)))
	mux.Handle("POST "+setupWizardPath+"/banner/dismiss", a.requireAdminJSON(http.HandlerFunc(a.handleSetupDismissBanner)))
	mux.Handle("POST "+setupWizardPath+"/test/llm", a.requireAdminJSON(http.HandlerFunc(a.handleSetupTestLLM)))
	mux.Handle("POST "+setupWizardPath+"/test/web-search", a.requireAdminJSON(http.HandlerFunc(a.handleSetupTestWebSearch)))
	mux.Handle("POST "+setupWizardPath+"/test/listenbrainz", a.requireAdminJSON(http.HandlerFunc(a.handleSetupTestListenBrainz)))
	mux.Handle("POST "+setupWizardPath+"/test/lastfm/connect", a.requireAdminJSON(http.HandlerFunc(a.handleSetupLastFMConnect)))
	mux.Handle("POST "+setupWizardPath+"/test/lastfm/complete", a.requireAdminJSON(http.HandlerFunc(a.handleSetupLastFMComplete)))
	mux.Handle("POST "+setupWizardPath+"/test/jackett", a.requireAdminJSON(http.HandlerFunc(a.handleSetupTestJackett)))
	mux.Handle("POST "+setupWizardPath+"/test/qb", a.requireAdminJSON(http.HandlerFunc(a.handleSetupTestQB)))
}

func (a *App) shouldRedirectToSetup(ctx context.Context) bool {
	st, err := a.store.SetupWizardState(ctx)
	if err != nil {
		a.logger.Warn("load setup wizard state", "error", err)
		return false
	}
	return st.IsPending()
}

func (a *App) dashboardSetupView(ctx context.Context) dashboardSetupView {
	st, err := a.store.SetupWizardState(ctx)
	if err != nil {
		return dashboardSetupView{Status: storage.SetupStatusCompleted}
	}
	view := dashboardSetupView{
		Status:           st.Status,
		ShowResumeBanner: st.ShowResumeBanner(),
		CompletedSteps:   st.CompletedConfigSteps(),
		CurrentStep:      st.CurrentStep,
	}
	if view.ShowResumeBanner {
		var missing []string
		labels := []struct {
			step  int
			label string
		}{
			{1, "元数据来源"},
			{2, "大模型与联网搜索"},
			{3, "自动化能力"},
			{4, "同步与资源获取"},
		}
		for _, item := range labels {
			if st.StepStates[item.step] != storage.SetupStepDone {
				missing = append(missing, item.label)
			}
		}
		view.MissingLabels = strings.Join(missing, "、")
	}
	return view
}

func (a *App) triggerFirstRunScanRelease(startScan, autoEnrich bool) {
	if a.releaseFirstRunScan != nil {
		a.releaseFirstRunScan(startScan, autoEnrich)
	}
}

func (a *App) buildSetupBootstrap(ctx context.Context, r *http.Request) (setupBootstrapPayload, error) {
	var p setupBootstrapPayload
	st, err := a.store.SetupWizardState(ctx)
	if err != nil {
		return p, err
	}
	p.State = st

	// Deployment & contextual security warnings.
	p.Deployment.MusicDirectory = a.config.MusicDirectory
	p.Deployment.DataDirectory = a.config.DataDirectory
	p.Deployment.LibraryName = a.config.LibraryName
	backupSt := storage.BackupSettings{
		Enabled:        a.config.BackupEnabled,
		Time:           a.config.BackupTime,
		KeepDaily:      a.config.BackupKeepDaily,
		KeepPreupgrade: a.config.BackupKeepPreupgrade,
	}
	if loaded, bErr := a.store.BackupSettings(ctx, backupSt); bErr == nil {
		backupSt = loaded
	}
	p.Deployment.BackupTime = backupSt.Time
	p.Deployment.BackupKeepDaily = backupSt.KeepDaily
	p.Deployment.AnalyzerConfigured = a.audioAnalysis != nil && a.audioAnalysis.Configured()
	if r != nil && a.loginLimiter != nil {
		if peer, ok := a.loginLimiter.untrustedForwarder(r); ok {
			p.Deployment.ProxyPeer = peer
		}
	}
	creds := a.currentCredentials()
	p.Deployment.MediaTokenEphemeral = a.config.MediaTokenGenerated && creds.sources.MediaToken == CredentialSourceEnv

	p.Preflight = probeMusicDirectory(ctx, a.config.MusicDirectory)

	// Step 1: Metadata & Artwork
	mb, _ := a.store.MetadataSourceSetting(ctx, "musicbrainz")
	bgm, _ := a.store.MetadataSourceSetting(ctx, "bangumi")
	lfmMeta, _ := a.store.MetadataSourceSetting(ctx, "lastfm")
	bio, _ := a.store.BiographySettings(ctx)
	imgSt, _ := a.store.ArtistImageSettings(ctx)
	// On a completely fresh pending wizard (step 1 untouched), suggest MusicBrainz
	// and Bangumi checked as recommended defaults in the UI without mutating DB yet.
	freshStep1 := st.IsPending() && st.StepStates[1] == storage.SetupStepPending
	p.Step1.MusicBrainzEnabled = mb.Enabled || (freshStep1 && !mb.Enabled && !bgm.Enabled)
	p.Step1.MusicBrainzContact = mb.Contact
	p.Step1.MusicBrainzAuto = mb.AutoMatch || freshStep1
	p.Step1.BangumiEnabled = bgm.Enabled || (freshStep1 && !mb.Enabled && !bgm.Enabled)
	p.Step1.BangumiAuto = bgm.AutoMatch || freshStep1
	p.Step1.LastFMEnabled = lfmMeta.Enabled
	p.Step1.LastFMKeySet = lfmMeta.HasAPIKey
	if bio.TargetLanguage == "" {
		p.Step1.BioTargetLanguage = "zh-Hans"
	} else {
		p.Step1.BioTargetLanguage = bio.TargetLanguage
	}
	p.Step1.WikipediaEnabled = bio.WikipediaEnabled
	p.Step1.FanartEnabled = imgSt.FanartEnabled
	p.Step1.FanartKeySet = strings.TrimSpace(imgSt.FanartAPIKey) != ""
	if mb.CacheDays > 0 {
		p.Step1.CacheDays = mb.CacheDays
	} else {
		p.Step1.CacheDays = 30
	}
	p.Step1.ImgBangumi = imgSt.BangumiEnabled
	p.Step1.ImgDeezer = imgSt.DeezerEnabled
	p.Step1.ImgWikipedia = imgSt.WikipediaEnabled

	// Step 2: LLM & Web Search
	aiSt, _ := a.store.AISettings(ctx)
	wsReady, wsSummary, ws, _, dsSet := a.webSearchStatusSummary(ctx)
	p.Step2.BaseURL = aiSt.BaseURL
	p.Step2.KeySet = aiSt.APIKey != ""
	p.Step2.Model = aiSt.Model
	p.Step2.ReviewModel = aiSt.ReviewModel
	p.Step2.FallbackModel = aiSt.FallbackModel
	p.Step2.ProxySet = aiSt.ProxyURL != ""
	if aiSt.RequestsPerMinute > 0 {
		p.Step2.RPM = aiSt.RequestsPerMinute
	} else {
		p.Step2.RPM = 60
	}
	if aiSt.Concurrency > 0 {
		p.Step2.Concurrency = aiSt.Concurrency
	} else {
		p.Step2.Concurrency = 4
	}
	p.Step2.ReasoningEffort = aiSt.ReasoningEffort
	p.Step2.VisionModel = imgSt.VisionModel
	p.Step2.WebSearchProvider = ws.Provider
	p.Step2.TavilyKeyCount = len(ws.TavilyKeys)
	p.Step2.DeepSeekKeySet = dsSet
	p.Step2.WebSearchReady = wsReady
	p.Step2.WebSearchSummary = wsSummary
	p.Step2.LLMConfigured = aiSt.Configured()

	// Step 3: Automation Capabilities
	rev, _ := a.store.AIReviewSettings(ctx)
	aliasSt, _ := a.store.ArtistAliasSettings(ctx)
	trSt, _ := a.store.LyricsTranslationSettings(ctx)
	dmSt, _ := a.store.DailyMixSettings(ctx)
	aaSt, _ := a.store.AudioAnalysisSettings(ctx)
	p.Step3.AIReviewEnabled = rev.Enabled
	if rev.AlbumMode == "auto" || rev.ArtistMode == "auto" {
		p.Step3.AIReviewMode = "auto"
	} else {
		p.Step3.AIReviewMode = "recommend"
	}
	p.Step3.AIReviewWeb = rev.WebSearchEnabled
	p.Step3.AliasEnabled = aliasSt.Enabled
	p.Step3.AvatarWebAIEnabled = imgSt.WebFallbackEnabled
	p.Step3.BioAIEnabled = bio.AITranslateEnabled
	p.Step3.LyricsTransEnabled = trSt.Enabled
	p.Step3.DailyMixEnabled = dmSt.Enabled
	p.Step3.DailyMixLLMTitles = dmSt.LLMTitles
	p.Step3.AITaggingEnabled = aiSt.TaggingEnabled
	p.Step3.AcousticEnabled = aaSt.Enabled

	// Step 4: Sync & Acquisition
	lbSt, _ := a.store.ListenBrainzSettings(ctx)
	lfmSt, _ := a.store.LastFMScrobbleSettings(ctx)
	acqSt, _ := a.store.AcquisitionSettings(ctx)
	p.Step4.LBEnabled = lbSt.Enabled
	if lbSt.ServerURL != "" {
		p.Step4.LBServerURL = lbSt.ServerURL
	} else {
		p.Step4.LBServerURL = "https://api.listenbrainz.org"
	}
	p.Step4.LBTokenSet = lbSt.UserToken != ""
	p.Step4.LBConnected = lbSt.Connected()
	p.Step4.LBUsername = lbSt.Username
	p.Step4.LBNowPlaying = lbSt.NowPlaying
	p.Step4.LBLoveSync = lbSt.SyncLoves
	p.Step4.LFMScrobbleEnabled = lfmSt.Enabled
	p.Step4.LFMSecretSet = lfmSt.HasAPISecret()
	p.Step4.LFMConnected = lfmSt.Connected()
	p.Step4.LFMUsername = lfmSt.Username
	p.Step4.LFMNowPlaying = lfmSt.NowPlaying
	p.Step4.LFMLoveSync = lfmSt.SyncLoves
	if a.lastfm != nil && lfmSt.HasAPISecret() {
		if authURL, authErr := a.lastfm.PendingAuthorizationURL(ctx); authErr == nil {
			p.Step4.LFMPendingAuthURL = authURL
		}
	}
	p.Step4.AcqEnabled = acqSt.Enabled
	p.Step4.JackettURL = acqSt.JackettURL
	if acqSt.JackettIndexer != "" {
		p.Step4.JackettIndexer = acqSt.JackettIndexer
	} else {
		p.Step4.JackettIndexer = "jpopsuki"
	}
	p.Step4.JackettKeySet = acqSt.KeySet
	p.Step4.QBURL = acqSt.QBURL
	p.Step4.QBUsername = acqSt.QBUsername
	p.Step4.QBPasswordSet = acqSt.HasPassword
	p.Step4.QBCategory = acqSt.QBCategory

	// Step 5: Library Watch
	watchSt, _, _ := a.effectiveLibraryWatch(ctx)
	p.Step5.WatchEnabled = watchSt.Enabled
	secs := int(watchSt.Interval / time.Second)
	if secs <= 0 {
		secs = int(config.DefaultWatchInterval / time.Second)
	}
	p.Step5.WatchIntervalSeconds = secs

	return p, nil
}

func (a *App) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	session, _ := a.sessions.get(r)
	payload, err := a.buildSetupBootstrap(r.Context(), r)
	if err != nil {
		a.logger.Error("build setup wizard bootstrap", "error", err)
		http.Error(w, "setup unavailable", http.StatusInternalServerError)
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "setup unavailable", http.StatusInternalServerError)
		return
	}
	// Escape '</' so JSON embedded inside <script type="application/json"> cannot close the tag.
	safeJSON := strings.ReplaceAll(string(raw), "</", `<\/`)
	a.render(w, http.StatusOK, "setup.html", setupPageData{
		CSRFToken:     session.CSRFToken,
		BootstrapJSON: template.JS(safeJSON),
	})
}

func (a *App) handleSetupPreflightLibrary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, probeMusicDirectory(r.Context(), a.config.MusicDirectory))
}

// probeMusicDirectory performs a fast, bounded read-only preflight check on the
// configured music root without reading audio tags or touching SQLite.
func probeMusicDirectory(ctx context.Context, root string) setupLibraryPreflight {
	res := setupLibraryPreflight{Directory: root}
	root = strings.TrimSpace(root)
	if root == "" {
		res.Status = "missing"
		res.Message = "未配置音乐目录（MUSIC_SERVER_MUSIC_DIR 为空）。"
		return res
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			res.Status = "missing"
			res.Message = fmt.Sprintf("目录 %s 不存在，请检查 Docker 卷挂载路径 MUSIC_SERVER_MUSIC_PATH。", root)
		} else {
			res.Status = "unreadable"
			res.Message = fmt.Sprintf("无法访问目录 %s（请检查容器非 root 用户的只读权限）。", root)
		}
		return res
	}
	if !info.IsDir() {
		res.Status = "unreadable"
		res.Message = fmt.Sprintf("路径 %s 不是目录，请检查挂载配置。", root)
		return res
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		res.Status = "unreadable"
		res.Message = fmt.Sprintf("无法读取目录 %s 的内容（请检查容器读权限与目录执行位 +x）。", root)
		return res
	}

	const maxScannedEntries = 500
	scanned := 0
	for _, entry := range entries {
		if probeCtx.Err() != nil || scanned >= maxScannedEntries {
			res.Truncated = true
			break
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		scanned++
		if entry.IsDir() {
			res.SubdirCount++
			// Peek one level deeper (bounded) to see sample audio files.
			subEntries, subErr := os.ReadDir(filepath.Join(root, name))
			if subErr == nil {
				for _, sub := range subEntries {
					if probeCtx.Err() != nil || scanned >= maxScannedEntries {
						res.Truncated = true
						break
					}
					if strings.HasPrefix(sub.Name(), ".") {
						continue
					}
					scanned++
					if !sub.IsDir() && metadata.IsSupported(sub.Name()) {
						res.AudioFileSample++
					}
				}
			}
		} else if metadata.IsSupported(name) {
			res.AudioFileSample++
		}
	}

	if res.SubdirCount == 0 && res.AudioFileSample == 0 {
		res.Status = "empty"
		res.Message = fmt.Sprintf("目录 %s 当前为空或未检测到可读音频文件，请确认宿主机挂载路径及读取权限。", root)
		return res
	}
	res.OK = true
	res.Status = "ok"
	if res.SubdirCount > 0 {
		suffix := ""
		if res.Truncated {
			suffix = "+"
		}
		res.Message = fmt.Sprintf("目录已挂载且可读，快速预检发现 %d%s 个子目录（尚未导入数据库）。启动首次扫描只读取标签，不改写源文件。", res.SubdirCount, suffix)
	} else {
		res.Message = fmt.Sprintf("目录已挂载且可读，快速预检发现 %d 个音频文件（尚未导入数据库）。启动首次扫描只读取标签，不改写源文件。", res.AudioFileSample)
	}
	return res
}

func decodeSetupJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if !aValidContentBody(w, r, dst) {
		return false
	}
	return true
}

func aValidContentBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	if err := dec.Decode(dst); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_body", "请求体格式无效")
		return false
	}
	return true
}

func (a *App) handleSetupStep1(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		Skip               bool   `json:"skip"`
		MusicBrainzEnabled bool   `json:"musicbrainzEnabled"`
		MusicBrainzContact string `json:"musicbrainzContact"`
		MusicBrainzAuto    bool   `json:"musicbrainzAuto"`
		BangumiEnabled     bool   `json:"bangumiEnabled"`
		BangumiAuto        bool   `json:"bangumiAuto"`
		LastFMEnabled      bool   `json:"lastfmEnabled"`
		LastFMAPIKey       string `json:"lastfmApiKey"`
		BioTargetLanguage  string `json:"bioTargetLanguage"`
		WikipediaEnabled   bool   `json:"wikipediaEnabled"`
		FanartAPIKey       string `json:"fanartApiKey"`
		ClearFanartKey     bool   `json:"clearFanartKey"`
		CacheDays          int    `json:"cacheDays"`
		ImgBangumi         bool   `json:"imgBangumi"`
		ImgDeezer          bool   `json:"imgDeezer"`
		ImgWikipedia       bool   `json:"imgWikipedia"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.Skip {
		st, err := a.store.SaveSetupWizardStep(ctx, 1, storage.SetupStepSkipped, 2)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st})
		return
	}

	contact := strings.TrimSpace(body.MusicBrainzContact)
	if body.MusicBrainzEnabled && contact == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "validation_error",
				"field":   "musicbrainzContact",
				"message": "启用 MusicBrainz 时必须填写联系邮箱或项目地址",
			},
		})
		return
	}
	if strings.IndexFunc(contact, unicode.IsControl) >= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "validation_error",
				"field":   "musicbrainzContact",
				"message": "联系方式不能包含换行等控制字符",
			},
		})
		return
	}

	existingMB, _ := a.store.MetadataSourceSetting(ctx, "musicbrainz")
	existingBGM, _ := a.store.MetadataSourceSetting(ctx, "bangumi")
	existingLFM, _ := a.store.MetadataSourceSetting(ctx, "lastfm")

	lastfmKey := strings.TrimSpace(body.LastFMAPIKey)
	if body.LastFMEnabled && lastfmKey == "" && !existingLFM.HasAPIKey {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{
				"code":    "validation_error",
				"field":   "lastfmApiKey",
				"message": "首次启用 Last.fm 时必须填写 API Key",
			},
		})
		return
	}

	cacheDays := body.CacheDays
	if cacheDays < 1 || cacheDays > 3650 {
		cacheDays = 30
	}

	existingMB.Source = "musicbrainz"
	existingMB.Enabled = body.MusicBrainzEnabled
	existingMB.Contact = contact
	existingMB.AutoMatch = body.MusicBrainzAuto
	existingMB.CacheDays = cacheDays
	if err := a.store.SaveMetadataSourceSetting(ctx, existingMB); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 MusicBrainz 设置失败："+err.Error())
		return
	}

	existingBGM.Source = "bangumi"
	existingBGM.Enabled = body.BangumiEnabled
	existingBGM.AutoMatch = body.BangumiAuto
	existingBGM.CacheDays = cacheDays
	if err := a.store.SaveMetadataSourceSetting(ctx, existingBGM); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 Bangumi 设置失败："+err.Error())
		return
	}

	existingLFM.Source = "lastfm"
	existingLFM.Enabled = body.LastFMEnabled
	existingLFM.APIKey = lastfmKey // empty preserves stored key inside SaveMetadataSourceSetting
	existingLFM.CacheDays = cacheDays
	if err := a.store.SaveMetadataSourceSetting(ctx, existingLFM); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 Last.fm 设置失败："+err.Error())
		return
	}

	bio, err := a.store.BiographySettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if lang := strings.TrimSpace(body.BioTargetLanguage); lang != "" {
		bio.TargetLanguage = lang
	}
	bio.WikipediaEnabled = body.WikipediaEnabled
	bio.CacheDays = cacheDays
	if err := a.store.SaveBiographySettings(ctx, bio); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存简介策略失败："+err.Error())
		return
	}

	imgSt, err := a.store.ArtistImageSettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	imgSt.BangumiEnabled = body.ImgBangumi
	imgSt.DeezerEnabled = body.ImgDeezer
	imgSt.WikipediaEnabled = body.ImgWikipedia
	if body.ClearFanartKey {
		imgSt.FanartAPIKey = ""
		imgSt.FanartEnabled = false
	} else if fk := strings.TrimSpace(body.FanartAPIKey); fk != "" {
		imgSt.FanartAPIKey = fk
		imgSt.FanartEnabled = true
	}
	if err := a.store.SaveArtistImageSettings(ctx, imgSt); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存图源设置失败："+err.Error())
		return
	}

	st, err := a.store.SaveSetupWizardStep(ctx, 1, storage.SetupStepDone, 2)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	bootstrap, _ := a.buildSetupBootstrap(ctx, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st, "bootstrap": bootstrap})
}

func (a *App) handleSetupStep2(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		Skip              bool   `json:"skip"`
		BaseURL           string `json:"baseUrl"`
		APIKey            string `json:"apiKey"`
		ClearAPIKey       bool   `json:"clearApiKey"`
		Model             string `json:"model"`
		ReviewModel       string `json:"reviewModel"`
		FallbackModel     string `json:"fallbackModel"`
		ProxyURL          string `json:"proxyUrl"`
		ClearProxy        bool   `json:"clearProxy"`
		RPM               int    `json:"rpm"`
		Concurrency       int    `json:"concurrency"`
		ReasoningEffort   string `json:"reasoningEffort"`
		VisionModel       string `json:"visionModel"`
		WebSearchProvider string `json:"webSearchProvider"`
		TavilyKeys        string `json:"tavilyKeys"`
		TavilyKeysMode    string `json:"tavilyKeysMode"`
		ClearTavilyKeys   bool   `json:"clearTavilyKeys"`
		DeepSeekKey       string `json:"deepSeekKey"`
		ClearDeepSeekKey  bool   `json:"clearDeepSeekKey"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.Skip {
		st, err := a.store.SaveSetupWizardStep(ctx, 2, storage.SetupStepSkipped, 3)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st})
		return
	}

	aiCur, err := a.store.AISettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	next := aiCur
	next.BaseURL = strings.TrimRight(strings.TrimSpace(body.BaseURL), "/")
	next.Model = strings.TrimSpace(body.Model)
	next.ReviewModel = strings.TrimSpace(body.ReviewModel)
	next.FallbackModel = strings.TrimSpace(body.FallbackModel)
	next.ReasoningEffort = strings.TrimSpace(body.ReasoningEffort)
	if key := strings.TrimSpace(body.APIKey); key != "" {
		next.APIKey = key
	} else if body.ClearAPIKey {
		next.APIKey = ""
	}
	if proxy := strings.TrimSpace(body.ProxyURL); proxy != "" {
		if !validProxyURL(proxy) {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "代理地址无效：支持 http://、https://、socks5:// 开头的地址")
			return
		}
		next.ProxyURL = proxy
	} else if body.ClearProxy {
		next.ProxyURL = ""
	}
	if body.RPM >= 1 && body.RPM <= 10000 {
		next.RequestsPerMinute = body.RPM
	}
	if body.Concurrency >= 1 && body.Concurrency <= 16 {
		next.Concurrency = body.Concurrency
	}
	if next.BaseURL != "" {
		if err := tagging.ValidateBaseURL(next.BaseURL); err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	if err := a.store.SaveAISettings(ctx, next); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存模型设置失败："+err.Error())
		return
	}
	if a.sharedAILimiter != nil && next.RequestsPerMinute > 0 {
		a.sharedAILimiter.SetRate(next.RequestsPerMinute)
	}

	// Update vision model on artist_image_settings cleanly.
	imgSt, err := a.store.ArtistImageSettings(ctx)
	if err == nil {
		vision := strings.TrimSpace(body.VisionModel)
		if vision != imgSt.VisionModel {
			imgSt.VisionModel = vision
			imgSt.VisionEnabled = vision != ""
			_ = a.store.SaveArtistImageSettings(ctx, imgSt)
		}
	}

	ws, err := a.store.WebSearchSettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if prov := strings.TrimSpace(body.WebSearchProvider); prov != "" {
		ws.Provider = storage.NormalizeWebSearchProvider(prov)
	}
	if body.ClearTavilyKeys {
		ws.TavilyKeys = nil
	} else if raw := strings.TrimSpace(body.TavilyKeys); raw != "" {
		parsed := storage.ParseAPIKeyList(raw)
		if strings.EqualFold(strings.TrimSpace(body.TavilyKeysMode), "replace") {
			ws.TavilyKeys = parsed
		} else {
			ws.TavilyKeys = storage.DedupeAPIKeys(append(ws.TavilyKeys, parsed...))
		}
	}
	if body.ClearDeepSeekKey {
		ws.DeepSeekKey = ""
	} else if ds := strings.TrimSpace(body.DeepSeekKey); ds != "" {
		ws.DeepSeekKey = ds
	}
	if err := a.store.SaveWebSearchSettings(ctx, ws); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存联网搜索设置失败："+err.Error())
		return
	}

	st, err := a.store.SaveSetupWizardStep(ctx, 2, storage.SetupStepDone, 3)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	bootstrap, _ := a.buildSetupBootstrap(ctx, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st, "bootstrap": bootstrap})
}

func (a *App) handleSetupStep3(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		Skip               bool   `json:"skip"`
		AIReviewEnabled    bool   `json:"aiReviewEnabled"`
		AIReviewMode       string `json:"aiReviewMode"`
		AIReviewWeb        bool   `json:"aiReviewWeb"`
		AliasEnabled       bool   `json:"aliasEnabled"`
		AvatarWebAIEnabled bool   `json:"avatarWebAiEnabled"`
		BioAIEnabled       bool   `json:"bioAiEnabled"`
		LyricsTransEnabled bool   `json:"lyricsTransEnabled"`
		DailyMixEnabled    bool   `json:"dailyMixEnabled"`
		DailyMixLLMTitles  bool   `json:"dailyMixLlmTitles"`
		AITaggingEnabled   bool   `json:"aiTaggingEnabled"`
		AcousticEnabled    bool   `json:"acousticEnabled"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.Skip {
		st, err := a.store.SaveSetupWizardStep(ctx, 3, storage.SetupStepSkipped, 4)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st})
		return
	}

	wizardCur, _ := a.store.SetupWizardState(ctx)
	aiSt, err := a.store.AISettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	wsReady, _, _, _, _ := a.webSearchStatusSummary(ctx)
	llmReady := aiSt.Configured()
	analyzerReady := a.audioAnalysis != nil && a.audioAnalysis.Configured()

	// Enforce prerequisite gates server-side as well as client-side.
	aiReviewOn := body.AIReviewEnabled && llmReady
	aliasOn := body.AliasEnabled && llmReady && wsReady
	avatarAIOn := body.AvatarWebAIEnabled && llmReady && wsReady
	bioAIOn := body.BioAIEnabled && llmReady
	lyricsOn := body.LyricsTransEnabled && llmReady
	taggingOn := body.AITaggingEnabled && llmReady
	acousticOn := body.AcousticEnabled && analyzerReady

	// 1. AI Review settings
	rev, err := a.store.AIReviewSettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	rev.Enabled = aiReviewOn
	rev.InlineEnabled = aiReviewOn
	rev.WebSearchEnabled = aiReviewOn && body.AIReviewWeb && wsReady
	mode := "off"
	if aiReviewOn {
		if body.AIReviewMode == "auto" {
			mode = "auto"
		} else {
			mode = "recommend"
		}
	}
	rev.AlbumMode = mode
	rev.TrackMode = mode
	rev.WorkMode = mode
	rev.ArtistMode = mode
	rev.ArtistMergeMode = mode
	rev.CreditMode = mode
	rev.SeriesMode = mode
	if err := a.store.SaveAIReviewSettings(ctx, rev); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 AI 初审设置失败："+err.Error())
		return
	}

	// 2. Artist alias completion
	aliasSt, err := a.store.ArtistAliasSettings(ctx)
	if err == nil {
		aliasSt.Enabled = aliasOn
		if err := a.store.SaveArtistAliasSettings(ctx, aliasSt); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存中文别名设置失败："+err.Error())
			return
		}
	}

	// 3. Artist avatar web AI fallback & auto ingest AI gate
	imgSt, err := a.store.ArtistImageSettings(ctx)
	if err == nil {
		imgSt.WebFallbackEnabled = avatarAIOn
		imgSt.AutoIngestAIEnabled = avatarAIOn
		if err := a.store.SaveArtistImageSettings(ctx, imgSt); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存头像 AI 策略失败："+err.Error())
			return
		}
	}

	// 4. Biography AI translate & web fallback
	bio, err := a.store.BiographySettings(ctx)
	if err == nil {
		bio.AITranslateEnabled = bioAIOn
		bio.AIWebFallbackEnabled = bioAIOn && wsReady
		if err := a.store.SaveBiographySettings(ctx, bio); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存简介 AI 设置失败："+err.Error())
			return
		}
	}

	// 5. Lyrics translation
	trSt, err := a.store.LyricsTranslationSettings(ctx)
	if err == nil {
		trSt.Enabled = lyricsOn
		if err := a.store.SaveLyricsTranslationSettings(ctx, trSt); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存歌词翻译设置失败："+err.Error())
			return
		}
	}

	// 6. Daily mixes
	dmSt, err := a.store.DailyMixSettings(ctx)
	if err == nil {
		dmSt.Enabled = body.DailyMixEnabled
		dmSt.LLMTitles = body.DailyMixLLMTitles && llmReady
		if err := a.store.SaveDailyMixSettings(ctx, dmSt); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存每日推荐设置失败："+err.Error())
			return
		}
	}

	// 7. AI tagging toggle
	aiSt.TaggingEnabled = taggingOn
	if err := a.store.SaveAISettings(ctx, aiSt); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 AI 打标开关失败："+err.Error())
		return
	}

	// 8. Acoustic analysis toggle
	if analyzerReady {
		aaSt, err := a.store.AudioAnalysisSettings(ctx)
		if err == nil {
			aaSt.Enabled = acousticOn
			if err := a.store.SaveAudioAnalysisSettings(ctx, aaSt); err != nil {
				writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存声学分析设置失败："+err.Error())
				return
			}
		}
	}

	// While setup is still pending, do NOT wake background workers prematurely;
	// they wake naturally once Step 5 releases the first scan. When re-editing
	// after completion/skip, wake updated services right away.
	if !wizardCur.IsPending() {
		if a.aiTagging != nil && taggingOn {
			a.aiTagging.Wake()
		}
		if a.artistAliases != nil && aliasOn {
			a.artistAliases.Wake()
		}
		if a.audioAnalysis != nil && acousticOn {
			a.audioAnalysis.Wake()
		}
	}

	st, err := a.store.SaveSetupWizardStep(ctx, 3, storage.SetupStepDone, 4)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	bootstrap, _ := a.buildSetupBootstrap(ctx, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st, "bootstrap": bootstrap})
}

func (a *App) handleSetupStep4(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		Skip               bool   `json:"skip"`
		LBEnabled          bool   `json:"lbEnabled"`
		LBServerURL        string `json:"lbServerUrl"`
		LBUserToken        string `json:"lbUserToken"`
		LBNowPlaying       bool   `json:"lbNowPlaying"`
		LBLoveSync         bool   `json:"lbLoveSync"`
		LFMScrobbleEnabled bool   `json:"lfmScrobbleEnabled"`
		LFMAPIKey          string `json:"lfmApiKey"`
		LFMSecret          string `json:"lfmSecret"`
		LFMNowPlaying      bool   `json:"lfmNowPlaying"`
		LFMLoveSync        bool   `json:"lfmLoveSync"`
		AcqEnabled         bool   `json:"acqEnabled"`
		JackettURL         string `json:"jackettUrl"`
		JackettIndexer     string `json:"jackettIndexer"`
		JackettAPIKey      string `json:"jackettApiKey"`
		QBURL              string `json:"qbUrl"`
		QBUsername         string `json:"qbUsername"`
		QBPassword         string `json:"qbPassword"`
		QBCategory         string `json:"qbCategory"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.Skip {
		st, err := a.store.SaveSetupWizardStep(ctx, 4, storage.SetupStepSkipped, 5)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st})
		return
	}

	// ListenBrainz
	if body.LBEnabled || strings.TrimSpace(body.LBUserToken) != "" {
		normURL, err := listenbrainz.NormalizeServerURL(body.LBServerURL)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "ListenBrainz 地址无效："+err.Error())
			return
		}
		if tok := strings.TrimSpace(body.LBUserToken); tok != "" {
			existingLB, _ := a.store.ListenBrainzSettings(ctx)
			username := existingLB.Username
			if err := a.store.SetListenBrainzConnection(ctx, normURL, tok, username); err != nil {
				writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 ListenBrainz 凭据失败："+err.Error())
				return
			}
		}
		if err := a.store.SaveListenBrainzPreferences(ctx, body.LBEnabled, body.LBNowPlaying, normURL); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 ListenBrainz 设置失败："+err.Error())
			return
		}
		_ = a.store.SetListenBrainzLoveSync(ctx, body.LBEnabled && body.LBLoveSync)
		if a.listenbrainz != nil {
			a.listenbrainz.Wake()
		}
	} else {
		existingLB, _ := a.store.ListenBrainzSettings(ctx)
		if existingLB.Enabled {
			_ = a.store.SaveListenBrainzPreferences(ctx, false, existingLB.NowPlaying, existingLB.ServerURL)
		}
	}

	// Last.fm Scrobble (sync API key back to Step 1 metadata_source_settings if entered here)
	if key := strings.TrimSpace(body.LFMAPIKey); key != "" {
		lfmMeta, _ := a.store.MetadataSourceSetting(ctx, "lastfm")
		lfmMeta.Source = "lastfm"
		lfmMeta.APIKey = key
		if err := a.store.SaveMetadataSourceSetting(ctx, lfmMeta); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 Last.fm API Key 失败："+err.Error())
			return
		}
	}
	if err := a.store.SaveLastFMScrobblePreferences(ctx, body.LFMScrobbleEnabled, body.LFMNowPlaying, strings.TrimSpace(body.LFMSecret)); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存 Last.fm 播放记录设置失败："+err.Error())
		return
	}
	_ = a.store.SetLastFMLoveSync(ctx, body.LFMScrobbleEnabled && body.LFMLoveSync)
	if a.lastfm != nil && body.LFMScrobbleEnabled {
		a.lastfm.Wake()
	}

	// Acquisition (Jackett + qBittorrent)
	jURL := strings.TrimRight(strings.TrimSpace(body.JackettURL), "/")
	qbURL := strings.TrimRight(strings.TrimSpace(body.QBURL), "/")
	for _, base := range []string{jURL, qbURL} {
		if base != "" {
			if _, err := lanhttp.New(base, 0); err != nil {
				writeAPIError(w, http.StatusBadRequest, "validation_error", "Jackett 或 qBittorrent 地址无效：请使用不含凭据或查询参数的 HTTP(S) 地址")
				return
			}
		}
	}
	if body.AcqEnabled && (jURL == "" || qbURL == "") {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "启用资源获取前请填写 Jackett 和 qBittorrent 地址")
		return
	}
	if err := a.store.UpdateAcquisitionSettings(ctx, storage.AcquisitionSettingsUpdate{
		Enabled:        body.AcqEnabled,
		JackettURL:     jURL,
		JackettIndexer: strings.TrimSpace(body.JackettIndexer),
		JackettAPIKey:  strings.TrimSpace(body.JackettAPIKey),
		QBURL:          qbURL,
		QBUsername:     strings.TrimSpace(body.QBUsername),
		QBPassword:     body.QBPassword,
		QBCategory:     strings.TrimSpace(body.QBCategory),
	}); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存资源获取设置失败："+err.Error())
		return
	}
	if a.acquisition != nil {
		a.acquisition.SettingsChanged()
	}

	st, err := a.store.SaveSetupWizardStep(ctx, 4, storage.SetupStepDone, 5)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	bootstrap, _ := a.buildSetupBootstrap(ctx, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": st, "bootstrap": bootstrap})
}

func (a *App) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		WatchEnabled         bool `json:"watchEnabled"`
		WatchIntervalSeconds int  `json:"watchIntervalSeconds"`
		StartScan            bool `json:"startScan"`
		AutoEnrich           bool `json:"autoEnrich"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()

	interval := time.Duration(body.WatchIntervalSeconds) * time.Second
	if interval < config.MinWatchInterval || interval > maxWatchInterval {
		interval = config.DefaultWatchInterval
	}
	if err := a.store.SaveLibraryWatchSettings(ctx, storage.LibraryWatchSettings{
		Enabled:  body.WatchEnabled,
		Interval: interval,
	}); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", "保存目录监控设置失败："+err.Error())
		return
	}
	a.applyLibraryWatchSettings(ctx)

	prevState, _ := a.store.SetupWizardState(ctx)
	st, err := a.store.CompleteSetupWizard(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}

	// Release first-run scan hold if we were pending; if reopening after completion
	// and user explicitly requested a scan now, trigger incremental scan directly.
	if prevState.IsPending() {
		a.triggerFirstRunScanRelease(body.StartScan, body.AutoEnrich)
	} else if body.StartScan && a.scanner != nil {
		_, _ = a.scanner.Start(context.Background(), "incremental")
	}

	redirectTo := "/admin/home"
	if body.StartScan {
		redirectTo = "/admin"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"state":      st,
		"redirectTo": redirectTo,
	})
}

func (a *App) handleSetupSkip(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	ctx := r.Context()
	prevState, _ := a.store.SetupWizardState(ctx)
	st, err := a.store.SkipSetupWizard(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if prevState.IsPending() {
		a.triggerFirstRunScanRelease(true, true)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"state":      st,
		"redirectTo": "/admin",
	})
}

func (a *App) handleSetupDismissBanner(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) && !a.validCSRF(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	if err := a.store.DismissSetupWizardBanner(r.Context()); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") || r.Header.Get("X-CSRF-Token") != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) handleSetupTestLLM(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		BaseURL         string `json:"baseUrl"`
		APIKey          string `json:"apiKey"`
		Model           string `json:"model"`
		ReviewModel     string `json:"reviewModel"`
		ProxyURL        string `json:"proxyUrl"`
		ReasoningEffort string `json:"reasoningEffort"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	st, err := a.store.AISettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "test_failed", err.Error())
		return
	}
	if u := strings.TrimRight(strings.TrimSpace(body.BaseURL), "/"); u != "" {
		st.BaseURL = u
	}
	if k := strings.TrimSpace(body.APIKey); k != "" {
		st.APIKey = k
	}
	if m := strings.TrimSpace(body.Model); m != "" {
		st.Model = m
	}
	if rm := strings.TrimSpace(body.ReviewModel); rm != "" {
		st.ReviewModel = rm
	}
	if p := strings.TrimSpace(body.ProxyURL); p != "" {
		if !validProxyURL(p) {
			writeAPIError(w, http.StatusBadRequest, "validation_error", "代理地址无效：支持 http://、https://、socks5://")
			return
		}
		st.ProxyURL = p
	}
	if re := strings.TrimSpace(body.ReasoningEffort); re != "" {
		st.ReasoningEffort = re
	}
	if st.BaseURL != "" {
		if err := tagging.ValidateBaseURL(st.BaseURL); err != nil {
			writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
			return
		}
	}
	if !st.Configured() {
		writeAPIError(w, http.StatusBadRequest, "not_configured", "请先填写接口地址、API Key 和日常主模型")
		return
	}

	models := []string{st.Model}
	if st.ReviewModel != "" && st.ReviewModel != st.Model {
		models = append(models, st.ReviewModel)
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var parts []string
	allOK := true
	if a.aiTagging != nil {
		for _, m := range models {
			res, testErr := a.aiTagging.TestConnection(callCtx, st, m)
			if testErr != nil {
				allOK = false
				parts = append(parts, fmt.Sprintf("%s 失败：%s", m, testErr.Error()))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s 正常（%.1fs，输入 %d / 输出 %d token）", m, res.Latency.Seconds(), res.Usage.PromptTokens, res.Usage.CompletionTokens))
		}
	} else {
		chat, chatErr := aiclient.DefaultChatFactory(st)
		if chatErr != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "连接测试失败：" + chatErr.Error()})
			return
		}
		for _, m := range models {
			res, used, testErr := aiclient.CompleteWithFallback(callCtx, a.store, chat, a.sharedAILimiter, st, tagging.ChatRequest{
				Model:  m,
				System: `Reply with JSON: {"ok":true}`,
				User:   `{"ping":1}`,
				Format: "json_object",
			}, "test")
			if testErr != nil {
				allOK = false
				parts = append(parts, fmt.Sprintf("%s 失败：%s", m, testErr.Error()))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s 正常（%.1fs，输入 %d / 输出 %d token）", used, res.Latency.Seconds(), res.Usage.PromptTokens, res.Usage.CompletionTokens))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "message": strings.Join(parts, "；")})
}

func (a *App) handleSetupTestWebSearch(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		Provider    string `json:"provider"`
		TavilyKeys  string `json:"tavilyKeys"`
		DeepSeekKey string `json:"deepSeekKey"`
		LLMBaseURL  string `json:"llmBaseUrl"`
		LLMAPIKey   string `json:"llmApiKey"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	ws, err := a.store.WebSearchSettings(ctx)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "test_failed", err.Error())
		return
	}
	if p := strings.TrimSpace(body.Provider); p != "" {
		ws.Provider = storage.NormalizeWebSearchProvider(p)
	}
	if raw := strings.TrimSpace(body.TavilyKeys); raw != "" {
		ws.TavilyKeys = storage.DedupeAPIKeys(append(storage.ParseAPIKeyList(raw), ws.TavilyKeys...))
	}
	if ds := strings.TrimSpace(body.DeepSeekKey); ds != "" {
		ws.DeepSeekKey = ds
	}
	aliasSt, _ := a.store.ArtistAliasSettings(ctx)
	aiSt, _ := a.store.AISettings(ctx)
	if u := strings.TrimRight(strings.TrimSpace(body.LLMBaseURL), "/"); u != "" {
		aiSt.BaseURL = u
	}
	if k := strings.TrimSpace(body.LLMAPIKey); k != "" {
		aiSt.APIKey = k
	}
	if !ws.Configured(aliasSt, aiSt) {
		writeAPIError(w, http.StatusBadRequest, "not_configured", "请先填写 Tavily API Key 或 DeepSeek 联网密钥")
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	summary, testErr := a.webSearchRegistry().TestConnection(callCtx, ws, aliasSt, aiSt)
	if testErr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "联网搜索测试失败：" + testErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "检索正常：" + summary})
}

func (a *App) handleSetupTestListenBrainz(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		ServerURL string `json:"serverUrl"`
		UserToken string `json:"userToken"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	existing, _ := a.store.ListenBrainzSettings(ctx)
	rawURL := strings.TrimSpace(body.ServerURL)
	if rawURL == "" {
		rawURL = existing.ServerURL
	}
	normURL, err := listenbrainz.NormalizeServerURL(rawURL)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "ListenBrainz 地址无效："+err.Error())
		return
	}
	token := strings.TrimSpace(body.UserToken)
	if token == "" {
		token = existing.UserToken
	}
	if token == "" {
		writeAPIError(w, http.StatusBadRequest, "not_configured", "请先填写 ListenBrainz User Token")
		return
	}
	var username string
	if a.listenbrainz != nil {
		normURL, username, err = a.listenbrainz.ValidateToken(ctx, normURL, token)
	} else {
		username, err = listenbrainz.NewClient(normURL).ValidateToken(ctx, token)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "Token 验证失败：" + err.Error()})
		return
	}
	if err := a.store.SetListenBrainzConnection(ctx, normURL, token, username); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"username": username,
		"message":  "已验证并连接账号：" + username,
	})
}

func (a *App) handleSetupLastFMConnect(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		APIKey    string `json:"apiKey"`
		APISecret string `json:"apiSecret"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	if key := strings.TrimSpace(body.APIKey); key != "" {
		meta, _ := a.store.MetadataSourceSetting(ctx, "lastfm")
		meta.Source = "lastfm"
		meta.APIKey = key
		if err := a.store.SaveMetadataSourceSetting(ctx, meta); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
	}
	if sec := strings.TrimSpace(body.APISecret); sec != "" {
		cur, _ := a.store.LastFMScrobbleSettings(ctx)
		if err := a.store.SaveLastFMScrobblePreferences(ctx, true, cur.NowPlaying, sec); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "save_failed", err.Error())
			return
		}
	}
	if a.lastfm == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "Last.fm 服务未启动")
		return
	}
	authURL, err := a.lastfm.BeginAuthorization(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "无法生成授权链接：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"authUrl": authURL,
		"message": "已生成授权链接，请在新窗口允许访问后点击「完成连接」。",
	})
}

func (a *App) handleSetupLastFMComplete(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	if a.lastfm == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "Last.fm 服务未启动")
		return
	}
	username, err := a.lastfm.CompleteAuthorization(r.Context())
	if err != nil {
		if !errors.Is(err, lastfm.ErrAuthorizationNotApproved) && !errors.Is(err, lastfm.ErrNoPendingAuthorization) {
			a.logger.Warn("setup Last.fm authorisation failed", "error", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "连接 Last.fm 失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"username": username,
		"message":  "已完成授权并连接 Last.fm 账号：" + username,
	})
}

func (a *App) handleSetupTestJackett(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		URL     string `json:"url"`
		Indexer string `json:"indexer"`
		APIKey  string `json:"apiKey"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	curSt, _ := a.store.AcquisitionSettings(ctx)
	curCreds, _ := a.store.AcquisitionCredentials(ctx)

	baseURL := strings.TrimRight(strings.TrimSpace(body.URL), "/")
	if baseURL == "" {
		baseURL = curSt.JackettURL
	}
	indexer := strings.TrimSpace(body.Indexer)
	if indexer == "" {
		indexer = curSt.JackettIndexer
	}
	if indexer == "" {
		indexer = "jpopsuki"
	}
	apiKey := strings.TrimSpace(body.APIKey)
	if apiKey == "" {
		apiKey = curCreds.JackettAPIKey
	}
	if baseURL == "" || apiKey == "" {
		writeAPIError(w, http.StatusBadRequest, "not_configured", "请先填写 Jackett 地址与 API Key")
		return
	}
	client, err := jackett.New(jackett.Config{BaseURL: baseURL, APIKey: apiKey, Indexer: indexer}, nil, nil)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "Jackett 地址无效：请使用不含凭据的 HTTP(S) 地址")
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := client.Search(callCtx, "032 setup connection test"); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "Jackett 连接测试失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": fmt.Sprintf("Jackett 连接正常（Indexer %q 可检索）", indexer)})
}

func (a *App) handleSetupTestQB(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRFHeader(r) {
		writeAPIError(w, http.StatusForbidden, "csrf_invalid", "CSRF 校验失败")
		return
	}
	var body struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Category string `json:"category"`
	}
	if !decodeSetupJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	curSt, _ := a.store.AcquisitionSettings(ctx)
	curCreds, _ := a.store.AcquisitionCredentials(ctx)

	baseURL := strings.TrimRight(strings.TrimSpace(body.URL), "/")
	if baseURL == "" {
		baseURL = curSt.QBURL
	}
	username := strings.TrimSpace(body.Username)
	if username == "" {
		username = curSt.QBUsername
	}
	password := body.Password
	if password == "" {
		password = curCreds.QBPassword
	}
	category := strings.TrimSpace(body.Category)
	if category == "" && body.URL == "" {
		category = curSt.QBCategory
	}
	if baseURL == "" {
		writeAPIError(w, http.StatusBadRequest, "not_configured", "请先填写 qBittorrent 地址")
		return
	}
	client, err := qbit.New(qbit.Config{BaseURL: baseURL, Username: username, Password: password})
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", "qBittorrent 地址无效：请使用不含凭据的 HTTP(S) 地址")
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := client.Probe(callCtx); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "qBittorrent 连接测试失败：" + err.Error()})
		return
	}
	if category != "" {
		cats, err := client.Categories(callCtx)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": "qBittorrent 读取分类失败：" + err.Error()})
			return
		}
		if _, ok := cats[category]; !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": fmt.Sprintf("qBittorrent 登录成功，但未找到分类 %q", category)})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": fmt.Sprintf("qBittorrent 连接正常，分类 %q 可用", category)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "qBittorrent 连接与认证正常"})
}
