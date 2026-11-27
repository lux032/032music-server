package enrichment

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/aiclient"
	"github.com/lux032/032music-server/internal/netfetch"
	"github.com/lux032/032music-server/internal/sourcegate"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/websearch"
)

type Manager struct {
	phaseDone    chan struct{}
	artistDone   chan struct{}
	cooldownMu   sync.Mutex
	blockedUntil map[string]time.Time
	now          func() time.Time
	// autoResumeTimers 管理限流自动恢复的唤醒定时器（key 为 kind:runID）。
	autoResumeMu     sync.Mutex
	autoResumeTimers map[string]*autoResumeEntry

	baseCtx             context.Context
	store               *storage.Store
	logger              *slog.Logger
	client              *http.Client
	mu                     sync.Mutex
	running                bool
	artistRunID            int64
	artistCancel           context.CancelFunc
	artistAutoRerunPending bool
	gate                   *sourcegate.Gate
	phaseMu                sync.Mutex
	phaseRunning           bool
	phaseRunID             int64
	phaseCancel            context.CancelFunc
	phaseAutoRerunPending  bool
	phaseEndpoints         phase4Endpoints
	confirmAlbumSubject    func(context.Context, int64, int64, int64, string, bool, []int64) (string, []int64, error)
	inlineAI               func(context.Context, string, int64) bool
	aiLimiter              *aiclient.Limiter
	aiChatFactory          aiclient.ChatFactory
	webSearch              *websearch.Registry
	bioWebFactory          func(storage.ArtistAliasSettings, storage.AISettings) (websearch.Client, error)
	posterMu               sync.Mutex
	posterBackfilling      bool
	posterBackfillPending  bool
	posterLastResult       *PosterBackfillResult
	posterCacheMu          sync.RWMutex
	posterCacheLoaded      bool
	posterCacheDirMod      time.Time
	posterCache            map[string]posterCacheEntry
	// 头像补全 durable run（B+2）与身份匹配 run 使用同一把 m.mu 但独立字段：
	// 两类任务可以并行，带宽共享由现有各来源限流原语（sourcegate 请求间隔、
	// blockedUntil 共享冷却桶）保证，互不进入对方的等待预算通道。
	imageBackfillRunning bool
	imageBackfillRunID   int64
	imageBackfillCancel  context.CancelFunc
	imageBackfillDone    chan struct{}
	imageSearchQueue     *artistImageSearchQueue
	autoImageQueue       *artistImageAutoQueueState
	// 歌手简介补全 durable run 与身份匹配/头像补全 run 使用同一把 m.mu 但
	// 独立字段：三类任务可以并行，带宽共享由现有各来源限流原语（sourcegate
	// 请求间隔、blockedUntil 共享冷却桶）保证，互不进入对方的等待预算通道。
	bioBackfillRunning bool
	bioBackfillRunID   int64
	bioBackfillCancel  context.CancelFunc
	bioBackfillDone    chan struct{}
	// posterFailed 记录最近补全失败的海报 URL 及失败时间（L3）：自动补全在
	// posterFailureTTL 内跳过它们；手动“补全缺失海报”强制重试（清空本表）。
	posterFailed map[string]time.Time
	// posterBackfillEnabled 控制自动触发（启动延迟、扫描后、每轮增强结束）；
	// 默认关闭，由 main 根据配置开启，测试与 e2e 保持关闭以不访问外网。
	posterBackfillEnabled bool
	musicBrainzBase       string
	bangumiPace           time.Duration
	imageDirectory        string
	wg                    sync.WaitGroup
	// sleep backs waitBangumiRateLimit/waitMBRateLimit; tests replace it to
	// observe backoff waits without really sleeping.
	sleep func(ctx context.Context, d time.Duration) error
	// runMemo remembers request keys already fetched during the current run so a
	// forced refresh still hits the network only once per key (M4).
	runMemo map[string]bool
	// testWorkWriteHook is a test-only injection point (nil in production): it
	// runs inside enrichBangumiWork right before the miss-row writes and before
	// the review-return existence recheck, so tests can delete the work in that
	// exact window (D-4 window 2).
	testWorkWriteHook func()
	// testStageCollectHook 是测试专用注入点（生产为 nil）：在每个阶段开始统计
	// 目标列表之前（“正在统计 X 阶段…”已持久化之后）调用，让测试能确定性地
	// 观察到统计中的任务状态。
	testStageCollectHook func(stage string)
	// testPosterBackfillHook 是测试专用注入点（生产为 nil）：在海报补全
	// goroutine 开始处理列表前调用，让测试确定性地观察“正在补全”状态。
	testPosterBackfillHook func()
	// testImageBackfillHook 是测试专用注入点（生产为 nil）：在头像补全项
	// 下载前（资格复查之后、guarded 写入之前）调用，让测试能在该窗口并发
	// 设置自定义头像或修改身份，验证提交前再验证会拒绝写入。
	testImageBackfillHook func(artistID int64)
	// testBioBackfillHook 是测试专用注入点（生产为 nil）：在简介补全项的
	// 资格/新鲜度复查与身份快照之后、网络抓取之前调用，让测试能在该窗口
	// 并发解除/改认身份或暂停任务，验证 guarded 写入会拒绝迟到简介。
	testBioBackfillHook func(artistID int64)
}

// ArtistMatchPartialError preserves the cause for logs while exposing a safe
// notice. The primary identity is committed; attachment work is deferred.
type ArtistMatchPartialError struct {
	Source string
	Cause  error
}

func (e *ArtistMatchPartialError) Error() string {
	return fmt.Sprintf("identity matched; %s attachment failed: %v", e.Source, e.Cause)
}
func (e *ArtistMatchPartialError) Unwrap() error { return e.Cause }
func (e *ArtistMatchPartialError) Notice() string {
	return "身份已绑定，但 Last.fm 资料写入失败，候选保留待人工确认；图片/简介可稍后手动刷新"
}

type MatchResult struct {
	Outcome            string
	SkipReason         string
	AutoMatched        bool
	InlineAIConfirmed  bool
	MergedIntoArtistID int64
	CandidateCount     int
}

func New(baseCtx context.Context, store *storage.Store, logger *slog.Logger, dataDirectory string) *Manager {
	manager := &Manager{now: time.Now, blockedUntil: map[string]time.Time{}, autoResumeTimers: map[string]*autoResumeEntry{}, baseCtx: baseCtx, store: store, logger: logger, client: &http.Client{Timeout: 20 * time.Second}, phaseEndpoints: bangumiEndpoints(sourcegate.BaseURLFromEnv(logger, "MUSIC_SERVER_BANGUMI_BASE_URL", "https://api.bgm.tv")), musicBrainzBase: sourcegate.BaseURLFromEnv(logger, "MUSIC_SERVER_MUSICBRAINZ_BASE_URL", "https://musicbrainz.org/ws/2"), bangumiPace: BangumiIntervalFromEnv(logger), imageDirectory: filepath.Join(dataDirectory, "artist-images"), sleep: sleepContext, posterFailed: map[string]time.Time{}, webSearch: websearch.NewRegistry(), imageSearchQueue: newArtistImageSearchQueue(), autoImageQueue: newArtistImageAutoQueueState()}
	manager.gate = sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": mbIntervalFromEnv(logger), "bangumi": manager.bangumiPace}, Now: func() time.Time { return manager.clockNow() }, Sleep: func(c context.Context, d time.Duration) error { return manager.sleep(c, d) }})

	if _, err := store.RecoverDurableEnrichmentRuns(context.Background()); err != nil {
		logger.Error("pause interrupted enrichment runs", "error", err)
	}
	if recovered, err := store.FailRunningEnrichmentRuns(context.Background(), "server restarted before the enrichment run completed"); err != nil {
		logger.Warn("recover interrupted enrichment runs", "error", err)
	} else if recovered > 0 {
		logger.Info("recovered interrupted enrichment runs", "count", recovered)
	}
	if _, err := store.RecoverDurableArtistRuns(context.Background()); err != nil {
		logger.Error("pause interrupted artist runs", "error", err)
	}
	if recovered, err := store.FailRunningArtistMatchRuns(context.Background(), "server restarted before the artist matching run completed"); err != nil {
		logger.Warn("recover interrupted artist matching runs", "error", err)
	} else if recovered > 0 {
		logger.Info("recovered interrupted artist matching runs", "count", recovered)
	}
	if recovered, err := store.RecoverArtistImageBackfillRuns(context.Background()); err != nil {
		logger.Error("pause interrupted artist image backfill runs", "error", err)
	} else if recovered > 0 {
		logger.Info("recovered interrupted artist image backfill runs", "count", recovered)
	}
	if recovered, err := store.RecoverArtistBiographyBackfillRuns(context.Background()); err != nil {
		logger.Error("pause interrupted artist biography backfill runs", "error", err)
	} else if recovered > 0 {
		logger.Info("recovered interrupted artist biography backfill runs", "count", recovered)
	}
	if recovered, err := store.RecoverArtistImageAutoQueue(context.Background()); err != nil {
		logger.Error("recover interrupted artist image auto queue", "error", err)
	} else if recovered > 0 {
		logger.Info("recovered interrupted artist image auto queue", "count", recovered)
	}
	return manager
}

func (m *Manager) SetInlineAIReviewer(fn func(context.Context, string, int64) bool) {
	m.inlineAI = fn
}

// Wait blocks until all background enrichment work has finished. Call after
// cancelling the base context during shutdown.
func (m *Manager) Wait() { m.wg.Wait() }

// goBackground runs fn as a tracked background worker: panics are recovered
// and logged instead of crashing the process, and Wait() blocks until it
// returns.
func (m *Manager) goBackground(name string, fn func()) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer func() {
			if recovered := recover(); recovered != nil {
				m.logger.Error("panic in background enrichment worker", "worker", name, "panic", recovered, "stack", string(debug.Stack()))
			}
		}()
		fn()
	}()
}

func (m *Manager) StartAuto(ctx context.Context) {
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		m.logger.Warn("load automatic metadata settings", "error", err)
		return
	}
	phaseEnabled, identityEnabled := autoEnrichmentFlags(settings)
	if phaseEnabled {
		m.startAutoPhaseEnabled(ctx, true)
	}
	if identityEnabled {
		m.startAutoArtistEnabled(ctx, true)
	}
	if m.posterBackfillAutoEnabled() {
		m.requestAutoWorkPosterBackfill()
	}
}

func autoEnrichmentFlags(settings []storage.MetadataSourceSetting) (phaseEnabled, identityEnabled bool) {
	for _, setting := range settings {
		if !setting.Enabled || !setting.AutoMatch {
			continue
		}
		switch setting.Source {
		case "bangumi":
			phaseEnabled = true
		case "musicbrainz", "lastfm":
			identityEnabled = true
		}
	}
	return phaseEnabled, identityEnabled
}

func (m *Manager) startAutoPhase(ctx context.Context, queueIfBusy bool) {
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		m.logger.Warn("load automatic metadata settings", "error", err)
		return
	}
	phaseEnabled, _ := autoEnrichmentFlags(settings)
	if !phaseEnabled {
		return
	}
	m.startAutoPhaseEnabled(ctx, queueIfBusy)
}

func (m *Manager) startAutoPhaseEnabled(ctx context.Context, queueIfBusy bool) {
	_, queuedState, err := m.startRun(ctx, RunRequest{Scope: "all"}, queueIfBusy)
	switch queuedState {
	case "active":
		m.logger.Info("automatic metadata enrichment queued: will rerun after current run completes")
		return
	case "paused":
		m.logger.Info("automatic metadata enrichment queued behind paused run: resume or cancel it from the admin page")
		return
	}
	if err != nil {
		if errors.Is(err, storage.ErrEnrichmentRunState) {
			m.logger.Info("automatic metadata enrichment skipped: another run is active or paused; resume or cancel it from the admin page", "error", err)
		} else {
			m.logger.Warn("automatic metadata enrichment was not started", "error", err)
		}
	}
}

func (m *Manager) startAutoArtist(ctx context.Context, queueIfBusy bool) {
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		m.logger.Warn("load automatic metadata settings", "error", err)
		return
	}
	_, identityEnabled := autoEnrichmentFlags(settings)
	if !identityEnabled {
		return
	}
	m.startAutoArtistEnabled(ctx, queueIfBusy)
}

func (m *Manager) startAutoArtistEnabled(ctx context.Context, queueIfBusy bool) {
	_, queuedState, err := m.startAllWithMode(ctx, storage.ArtistScanIncremental, queueIfBusy)
	switch queuedState {
	case "active":
		m.logger.Info("automatic artist matching queued: will rerun after current run completes")
		return
	case "paused":
		m.logger.Info("automatic artist matching queued behind paused run: resume or cancel it from the admin page")
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, ErrNoEligibleArtists):
			m.logger.Debug("automatic artist matching skipped: no eligible artists")
		case errors.Is(err, storage.ErrArtistRunState), errors.Is(err, ErrRunNotActive):
			m.logger.Info("automatic artist matching skipped: another run is active or paused; resume or cancel it from the admin page", "error", err)
		default:
			m.logger.Warn("automatic artist matching was not started", "error", err)
		}
	}
}

func (m *Manager) StartAll(ctx context.Context) (int64, error) {
	return m.StartAllWithMode(ctx, storage.ArtistScanIncremental)
}

func (m *Manager) StartAllWithMode(ctx context.Context, mode storage.ArtistScanMode) (int64, error) {
	runID, _, err := m.startAllWithMode(ctx, mode, false)
	return runID, err
}

func (m *Manager) startAllWithMode(ctx context.Context, mode storage.ArtistScanMode, queueAutoOnBusy bool) (int64, string, error) {
	if mode != storage.ArtistScanFull {
		mode = storage.ArtistScanIncremental
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		if queueAutoOnBusy {
			m.artistAutoRerunPending = true
			return 0, "active", nil
		}
		return 0, "", ErrRunNotActive
	}
	if _, err := m.store.UnfinishedDurableArtistRun(ctx); err == nil {
		if queueAutoOnBusy {
			m.artistAutoRerunPending = true
			return 0, "paused", nil
		}
		return 0, "", storage.ErrArtistRunState
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	m.artistAutoRerunPending = false
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		return 0, "", err
	}
	hasAutomaticSource := false
	for _, setting := range settings {
		if (setting.Source == "musicbrainz" || setting.Source == "lastfm") && setting.Enabled && setting.AutoMatch {
			hasAutomaticSource = true
			break
		}
	}
	if !hasAutomaticSource {
		return 0, "", errors.New("no automatic metadata source is enabled")
	}
	artists, err := m.store.ArtistsForMatching(ctx)
	if err != nil {
		return 0, "", err
	}
	eligible, composites, err := m.store.FilterArtistsForScan(ctx, artists, settings, mode)
	if err != nil {
		return 0, "", err
	}
	if len(eligible) == 0 {
		return 0, "", ErrNoEligibleArtists
	}
	artists = append(eligible, composites...)
	var snapshots []storage.ArtistRunItemInput
	for _, artist := range artists {
		snapshot := storage.ArtistRunItemInput{Artist: artist}
		for _, setting := range settings {
			if setting.Source == "musicbrainz" || setting.Source == "lastfm" {
				ik, ck := storage.ArtistMatchKeys(artist, setting)
				snapshot.Sources = append(snapshot.Sources, storage.ArtistRunSource{Source: setting.Source, Language: setting.Language, InputKey: ik, ConfigKey: ck, CacheDays: setting.CacheDays, Enabled: setting.Enabled, AutoMatch: setting.AutoMatch, HasAPIKey: setting.APIKey != ""})
			}
		}
		snapshots = append(snapshots, snapshot)
	}
	runID, err := m.store.CreateDurableArtistRunWithMode(ctx, snapshots, mode)
	if err != nil {
		return 0, "", err
	}
	m.launchArtistRunLocked(runID)
	return runID, "", nil
}

var ErrNoEligibleArtists = errors.New("没有需要检查的艺术家")

var ErrRunNotActive = errors.New("run is not active")

func (m *Manager) CancelArtistMatching(runID int64) error { return m.stopArtistRun(runID, "cancel") }
func (m *Manager) PauseArtistMatching(runID int64) error  { return m.stopArtistRun(runID, "pause") }
func (m *Manager) stopArtistRun(runID int64, action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.TransitionArtistRun(context.Background(), runID, action, "manual"); err != nil {
		if errors.Is(err, storage.ErrArtistRunState) {
			return ErrRunNotActive
		}
		return err
	}
	if action == "cancel" {
		m.artistAutoRerunPending = false
	}
	m.cancelAutoResume(artistAutoResumeKind, runID)
	if m.artistRunID == runID && m.artistCancel != nil {
		m.artistCancel()
	}
	return nil
}
func (m *Manager) ResumeArtistMatching(ctx context.Context, runID int64) error {
	// Join before transitioning: no old loop can claim a new generation/token.
	m.mu.Lock()
	done := m.artistDone
	oldID := m.artistRunID
	m.mu.Unlock()
	if oldID == runID && done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return ErrRunNotActive
	}
	if err := m.store.TransitionArtistRun(ctx, runID, "resume", ""); err != nil {
		return err
	}
	m.cancelAutoResume(artistAutoResumeKind, runID)
	m.launchArtistRunLocked(runID)
	return nil
}

func (m *Manager) MatchArtist(ctx context.Context, artistID int64) (MatchResult, error) {
	return m.matchArtist(ctx, artistID, false)
}

type artistCheckpointKey struct{}

func (m *Manager) matchArtist(ctx context.Context, artistID int64, automatic bool) (result MatchResult, matchErr error) {
	return m.matchArtistWithMode(ctx, artistID, automatic, storage.ArtistScanIncremental)
}

func (m *Manager) matchArtistWithMode(ctx context.Context, artistID int64, automatic bool, scanMode storage.ArtistScanMode) (result MatchResult, matchErr error) {
	checkpoint, _ := ctx.Value(artistCheckpointKey{}).(*storage.ArtistRunCheckpoint)

	defer func() {
		if result.Outcome == "" {
			switch {
			case result.AutoMatched:
				result.Outcome = "matched"
			case matchErr != nil:
				result.Outcome = "failed"
			case result.SkipReason != "":
				result.Outcome = "skipped"
			case result.CandidateCount > 0:
				result.Outcome = "review"
			default:
				result.Outcome = "no_result"
			}
		}
	}()

	if automatic {
		input, err := m.store.ArtistForMatching(ctx, artistID)
		if errors.Is(err, sql.ErrNoRows) {
			return MatchResult{Outcome: "skipped", SkipReason: "object_missing"}, nil
		}
		if err != nil {
			return MatchResult{}, err
		}
		isComposite, compErr := m.store.IsCompositeArtist(ctx, artistID, input.Name)
		if compErr != nil {
			return MatchResult{}, compErr
		}
		if isComposite {
			if m.inlineAI != nil && m.inlineAI(ctx, storage.AITargetCreditSplit, artistID) {
				return MatchResult{AutoMatched: true, InlineAIConfirmed: true, Outcome: "matched"}, nil
			}
			if err := m.store.SaveArtistCompositeSkip(ctx, input, checkpoint); err != nil {
				return MatchResult{}, err
			}
			return MatchResult{SkipReason: "skipped_composite"}, nil
		}
	}
	artist, err := m.store.ArtistForMatching(ctx, artistID)
	if err != nil {
		return MatchResult{}, err
	}
	if checkpoint != nil {
		checkpoint.ExpectedName = artist.Name
	}
	artist, err = m.store.ArtistMatchQueryContext(ctx, artist)
	if err != nil {
		return MatchResult{}, err
	}
	if !automatic {
		if biographyErr := m.RefreshArtistBiographies(ctx, artistID, true); biographyErr != nil && !errors.Is(biographyErr, sql.ErrNoRows) {
			if asRateLimited(biographyErr) != nil {
				return MatchResult{}, biographyErr
			}
			m.logger.Warn("refresh confirmed artist biographies", "artistId", artistID, "error", biographyErr)
		}
	}
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		return MatchResult{}, err
	}
	bySource := map[string]storage.MetadataSourceSetting{}
	for _, setting := range settings {
		if setting.Enabled && (!automatic || setting.AutoMatch) {
			bySource[setting.Source] = setting
		}
	}
	if len(bySource) == 0 {
		if automatic {
			return MatchResult{Outcome: "skipped", SkipReason: "source_disabled"}, nil
		}
		return MatchResult{}, errors.New("no metadata source is enabled")
	}
	var candidates []storage.ArtistCandidate
	unsafeTaggedIdentity := false
	compositeCredit, err := m.store.IsCompositeArtist(ctx, artistID, artist.Name)
	if err != nil {
		return MatchResult{}, err
	}
	queriedSources := 0
	successfulSources := 0
	var refreshedSources []string
	var mbProfiles = map[string]storage.ExternalArtistProfile{}
	var lastProfile *storage.ExternalArtistProfile
	lastFMIndependent := artist.LastFMQueryMBID == ""
	checks := map[string]storage.ArtistSourceCheck{}
	recoveredSources := 0
	if automatic {
		for _, source := range []string{"musicbrainz", "lastfm"} {
			if setting, ok := bySource[source]; ok {
				check, e := m.store.ArtistSourceCheckNeededForMode(ctx, artist, setting, scanMode)
				if e != nil {
					return MatchResult{}, e
				}
				if checkpoint != nil {
					ik, ck := storage.ArtistMatchKeys(artist, setting)
					saved, savedErr := m.store.ArtistRunItemSource(ctx, checkpoint.ItemID, source, ik, ck, setting.CacheDays)
					if savedErr == nil {
						if _, identityErr := m.store.ArtistEffectiveExternalID(ctx, artistID, source); errors.Is(identityErr, sql.ErrNoRows) {
							check.Snapshot = &saved
							check.Eligible = false
							recoveredSources++
						} else if identityErr != nil {
							return MatchResult{}, identityErr
						}
					} else if !errors.Is(savedErr, sql.ErrNoRows) {
						return MatchResult{}, savedErr
					}
					// A durable unfinished item must not freeze expired review evidence:
					// re-query when it has no fresh snapshot, unless identity is already bound.
					hasCheckpoint, existsErr := m.store.ArtistRunItemHasSource(ctx, checkpoint.ItemID, source)
					if existsErr != nil {
						return MatchResult{}, existsErr
					}
					if hasCheckpoint && check.Snapshot == nil && !check.Eligible {
						if _, identityErr := m.store.ArtistEffectiveExternalID(ctx, artistID, source); errors.Is(identityErr, sql.ErrNoRows) {
							check.Eligible = true
						} else if identityErr != nil {
							return MatchResult{}, identityErr
						}
					}
				}
				checks[source] = check
				if !check.Eligible && check.Snapshot != nil {
					candidates = append(candidates, check.Snapshot.Candidates...)
					if source == "musicbrainz" {
						mbProfiles = check.Snapshot.Profiles
						unsafeTaggedIdentity = check.Snapshot.UnsafeTaggedIdentity
						if artist.TaggedMBID != "" {
							for _, p := range mbProfiles {
								if !artistProfileNameMatches(artist.Name, p) {
									unsafeTaggedIdentity = true
								}
							}
						}
					} else {
						lastFMIndependent = check.Snapshot.Independent != nil && *check.Snapshot.Independent && check.Snapshot.QueriedByMBID == ""
						for _, p := range check.Snapshot.Profiles {
							copy := p
							lastProfile = &copy
						}
					}
				}
			}
		}
	}
	persistSource := func(source string, found []storage.ArtistCandidate, profiles map[string]storage.ExternalArtistProfile) error {
		normalizeArtistCandidates(found, unsafeTaggedIdentity, compositeCredit)
		snapshot := storage.ArtistSourceSnapshot{Candidates: found, Profiles: profiles, UnsafeTaggedIdentity: unsafeTaggedIdentity}
		if source == "lastfm" {
			snapshot.Independent = &lastFMIndependent
			snapshot.QueriedByMBID = artist.LastFMQueryMBID
		}
		if checkpoint != nil {
			return m.store.SaveArtistRunSourceCheck(ctx, artist, bySource[source], snapshot, *checkpoint)
		}
		return m.store.SaveArtistSourceCheck(ctx, artist, bySource[source], snapshot)
	}

	usedLocalMBProfile := false
	mbDetailedFetched := false
	if setting, ok := bySource["musicbrainz"]; ok {
		if automatic && !checks["musicbrainz"].Eligible {
		} else {
			queriedSources++
			var found []storage.ArtistCandidate
			if artist.TaggedMBID != "" {
				// Fast-path: reuse locally cached MusicBrainz profile owned by ANOTHER
				// artist for this TaggedMBID whenever its cached names/aliases already
				// validate artist.Name, avoiding redundant MB HTTP requests and rate limits.
				// Never intercept an artist refreshing its own profile.
				if localProfile, localOwnerID, localErr := m.store.ExternalArtistProfileByID(ctx, "musicbrainz", artist.TaggedMBID); localErr == nil && localOwnerID != artistID && artistProfileNameMatches(artist.Name, localProfile) {
					usedLocalMBProfile = true
					mbDetailedFetched = true
					successfulSources++
					refreshedSources = append(refreshedSources, "musicbrainz")
					candidate := storage.ArtistCandidate{
						ArtistID:       artistID,
						Source:         "musicbrainz",
						ExternalID:     localProfile.ExternalID,
						DisplayName:    localProfile.DisplayName,
						SortName:       localProfile.SortName,
						Disambiguation: localProfile.Disambiguation,
						Country:        localProfile.Country,
						ArtistType:     localProfile.ArtistType,
						MBID:           localProfile.ExternalID,
						Score:          100,
						Evidence:       []string{"文件标签包含 MusicBrainz ID", "复用本地已缓存 MusicBrainz 资料"},
						Payload:        localProfile.Raw,
					}
					found = append(found, candidate)
					mbProfiles[candidate.MBID] = localProfile
				} else {
					candidate, profile, e := m.musicBrainzLookup(ctx, artist.TaggedMBID, setting)
					if e == nil {
						mbDetailedFetched = true
						successfulSources++
						refreshedSources = append(refreshedSources, "musicbrainz")
						candidate.ArtistID = artistID
						candidate.Score = 100
						candidate.Evidence = []string{"文件标签包含 MusicBrainz ID"}
						if !artistProfileNameMatches(artist.Name, profile) {
							unsafeTaggedIdentity = true
							candidate.Score = 85
							candidate.RequiresManualReview = true
							candidate.Evidence = append(candidate.Evidence, "标签身份与歌手名称不一致，需要人工核对")
						}
						found = append(found, candidate)
						mbProfiles[candidate.MBID] = profile
					} else if asRateLimited(e) != nil {
						return MatchResult{}, e
					}
				}
			} else {
				found, mbProfiles, err = m.musicBrainzSearch(ctx, artist, setting)
				if err != nil {
					if asRateLimited(err) != nil {
						return MatchResult{}, err
					}
					m.logger.Warn("musicbrainz search failed", "artist", artist.Name, "error", err)
				} else {
					successfulSources++
					refreshedSources = append(refreshedSources, "musicbrainz")
				}
			}
			if len(refreshedSources) > 0 && refreshedSources[len(refreshedSources)-1] == "musicbrainz" {
				if e := persistSource("musicbrainz", found, mbProfiles); e != nil {
					return MatchResult{}, e
				}
			}
			candidates = append(candidates, found...)
		}
	}
	if setting, ok := bySource["lastfm"]; ok && !usedLocalMBProfile {
		if automatic && !checks["lastfm"].Eligible {
		} else {
			queriedSources++
			candidate, profile, e := m.lastFMInfo(ctx, artist, setting)
			if errors.Is(e, errArtistNotFound) {
				if e = persistSource("lastfm", nil, nil); e != nil {
					return MatchResult{}, e
				}
				successfulSources++
				refreshedSources = append(refreshedSources, "lastfm")
			} else if e != nil {
				if asRateLimited(e) != nil {
					return MatchResult{}, e
				}
				// P2-4：网络层错误可能携带含 api_key 的请求 URL，先脱敏再记日志。
				m.logger.Warn("lastfm lookup failed", "artist", artist.Name, "error", RedactSourceError(e))
			} else {
				successfulSources++
				refreshedSources = append(refreshedSources, "lastfm")
				candidate.ArtistID = artistID
				candidates = append(candidates, candidate)
				lastProfile = &profile
				if e := persistSource("lastfm", []storage.ArtistCandidate{candidate}, map[string]storage.ExternalArtistProfile{profile.ExternalID: profile}); e != nil {
					return MatchResult{}, e
				}
			}
		}
	}
	if automatic && queriedSources == 0 && recoveredSources == 0 {
		return MatchResult{Outcome: "skipped", SkipReason: "source_checked"}, nil
	}
	if queriedSources > 0 && successfulSources+recoveredSources == 0 {
		return MatchResult{}, errors.New("all enabled metadata sources failed")
	}
	confirmedMBID, _ := m.store.ArtistEffectiveExternalID(ctx, artistID, "musicbrainz")
	independentLastFM := confirmedMBID == "" && lastFMIndependent
	for i := range candidates {
		if independentLastFM && candidates[i].Source == "musicbrainz" && candidates[i].MBID != "" && lastProfile != nil && lastProfile.ExternalID == candidates[i].MBID && artistProfileNameMatches(artist.Name, *lastProfile) {
			candidates[i].Score = 98
			candidates[i].Evidence = append(candidates[i].Evidence, "MusicBrainz 与 Last.fm 返回相同 MBID")
		}
	}
	if lastProfile != nil && lastProfile.ExternalID != "" {
		for i := range candidates {
			if candidates[i].Source == "lastfm" {
				if profile, ok := mbProfiles[lastProfile.ExternalID]; ok && independentLastFM && artistProfileNameMatches(artist.Name, profile) && artistProfileNameMatches(artist.Name, *lastProfile) {
					candidates[i].Score = 98
					candidates[i].Evidence = append(candidates[i].Evidence, "MusicBrainz 与 Last.fm 返回相同 MBID")
				} else if confirmedMBID == lastProfile.ExternalID {
					candidates[i].Evidence = append(candidates[i].Evidence, "Last.fm 查询沿用已绑定 MusicBrainz 身份，不作为独立确认依据")
				}
			}
		}
	}
	// Agreement between sources does not validate a misattributed file tag.
	normalizeArtistCandidates(candidates, unsafeTaggedIdentity, compositeCredit)
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	if checkpoint == nil {
		if err = m.store.ReplaceArtistCandidatesForSources(ctx, artistID, candidates, refreshedSources); err != nil {
			return MatchResult{}, err
		}
	}
	auto := false
	if lastProfile != nil && checkpoint == nil {
		refreshed, refreshErr := m.store.RefreshArtistCandidate(ctx, artistID, *lastProfile)
		if refreshErr != nil {
			return MatchResult{}, refreshErr
		}
		auto = refreshed
	}
	if len(candidates) > 0 && candidates[0].Score >= 92 {
		best := candidates[0]
		var profile storage.ExternalArtistProfile
		if best.Source == "musicbrainz" {
			profile = mbProfiles[best.MBID]
			if !mbDetailedFetched && profile.RemoteImageURL == "" && !(automatic && !checks["musicbrainz"].Eligible) {
				if setting, enabled := bySource["musicbrainz"]; enabled {
					if _, detailed, lookupErr := m.musicBrainzLookup(ctx, best.MBID, setting); lookupErr == nil {
						profile = detailed
					} else if asRateLimited(lookupErr) != nil {
						return MatchResult{AutoMatched: auto}, lookupErr
					}
				}
			}
		} else if best.Source == "lastfm" && lastProfile != nil {
			profile = *lastProfile
		}
		if profile.Source != "" && profile.ExternalID == best.ExternalID {
			var bindRes storage.ArtistBindResult
			var bindErr error
			if checkpoint != nil {
				bindRes, bindErr = m.store.AutoBindOrMergeArtistRunCandidate(ctx, artistID, profile, *checkpoint)
			} else {
				bindRes, bindErr = m.store.AutoBindOrMergeArtistCandidate(ctx, artistID, profile)
			}
			if bindErr != nil {
				return MatchResult{AutoMatched: auto}, bindErr
			}
			if bindRes.Bound || bindRes.Merged {
				effectiveID := artistID
				var mergedInto int64
				if bindRes.Merged && bindRes.TargetArtistID > 0 {
					effectiveID = bindRes.TargetArtistID
					mergedInto = bindRes.TargetArtistID
				}
				// Independently corroborated sources bind through their own transaction.
				// A secondary manual decision/conflict cannot roll back the primary;
				// real storage errors still propagate with the persisted primary result.
				if !bindRes.Merged && !auto && lastProfile != nil && best.Source == "musicbrainz" && lastProfile.ExternalID == best.MBID {
					for _, candidate := range candidates {
						if candidate.Source == "lastfm" && candidate.ExternalID == lastProfile.ExternalID && candidate.Score >= 92 {
							bindSecondary := func() (bool, error) {
								if checkpoint != nil {
									return m.store.AutoBindArtistRunCandidate(ctx, artistID, *lastProfile, *checkpoint)
								}
								return m.store.AutoBindArtistCandidate(ctx, artistID, *lastProfile)
							}
							if _, secondaryErr := bindSecondary(); secondaryErr != nil {
								return MatchResult{AutoMatched: true, CandidateCount: len(candidates)}, &ArtistMatchPartialError{Source: "lastfm", Cause: secondaryErr}
							}
							break
						}
					}
				}
				if automatic {
					return MatchResult{AutoMatched: true, MergedIntoArtistID: mergedInto, CandidateCount: len(candidates)}, nil
				}
				if err = m.CacheArtistImage(ctx, effectiveID); err != nil {
					if asRateLimited(err) != nil {
						// The confirmation is already persisted; report AutoMatched so
						// interactive callers can say the match succeeded but the
						// image/biography fetch was deferred by the rate limit.
						return MatchResult{AutoMatched: true, MergedIntoArtistID: mergedInto, CandidateCount: len(candidates)}, err
					}
					m.logger.Warn("cache artist image failed", "artistId", effectiveID, "error", err)
				}
				if err = m.RefreshArtistBiographies(ctx, effectiveID, false); err != nil && !errors.Is(err, sql.ErrNoRows) {
					if asRateLimited(err) != nil {
						return MatchResult{AutoMatched: true, MergedIntoArtistID: mergedInto, CandidateCount: len(candidates)}, err
					}
					m.logger.Warn("cache artist biographies failed", "artistId", effectiveID, "error", err)
				}
				auto = true
				if mergedInto > 0 {
					return MatchResult{AutoMatched: true, MergedIntoArtistID: mergedInto, CandidateCount: len(candidates)}, nil
				}
			}
		}
	}
	pendingCount, err := m.store.PendingArtistCandidateCount(ctx, artistID)
	if err != nil {
		return MatchResult{AutoMatched: auto}, err
	}
	inlineConfirmed := false
	if !auto && pendingCount > 0 && m.inlineAI != nil && automatic {
		if m.inlineAI(ctx, storage.AITargetArtistMatch, artistID) {
			auto = true
			inlineConfirmed = true
		}
	}
	return MatchResult{AutoMatched: auto, InlineAIConfirmed: inlineConfirmed, CandidateCount: pendingCount}, nil
}

type mbAliasResponse struct {
	Name    string `json:"name"`
	Locale  string `json:"locale"`
	Primary bool   `json:"primary"`
	Type    string `json:"type"`
	Ended   bool   `json:"ended"`
}

type mbSearchResponse struct {
	Artists []struct {
		ID, Name                      string
		SortName                      string `json:"sort-name"`
		Type, Country, Disambiguation string
		Score                         int
		Aliases                       []mbAliasResponse
		Tags                          []struct{ Name string }
	} `json:"artists"`
}
type mbArtistResponse struct {
	ID, Name                      string
	SortName                      string `json:"sort-name"`
	Type, Country, Disambiguation string
	Aliases                       []mbAliasResponse
	Tags                          []struct{ Name string }
	Relations                     []struct {
		Type string
		URL  struct{ Resource string }
	}
}

func unpackMBAliases(raw []mbAliasResponse) ([]string, []storage.StructuredArtistAlias) {
	aliases := make([]string, 0, len(raw))
	structured := make([]storage.StructuredArtistAlias, 0, len(raw))
	for _, v := range raw {
		name := strings.TrimSpace(v.Name)
		if name == "" {
			continue
		}
		aliases = append(aliases, name)
		structured = append(structured, storage.StructuredArtistAlias{
			Name:    name,
			Locale:  strings.TrimSpace(v.Locale),
			Primary: v.Primary,
			Type:    strings.TrimSpace(v.Type),
			Ended:   v.Ended,
		})
	}
	return aliases, structured
}

func (m *Manager) musicBrainzSearch(ctx context.Context, artist storage.ArtistMatchInput, setting storage.MetadataSourceSetting) ([]storage.ArtistCandidate, map[string]storage.ExternalArtistProfile, error) {
	return m.musicBrainzSearchQuery(ctx, artist, "artist:\""+artist.Name+"\"", 5, setting)
}

func (m *Manager) musicBrainzSearchQuery(ctx context.Context, artist storage.ArtistMatchInput, query string, limit int, setting storage.MetadataSourceSetting) ([]storage.ArtistCandidate, map[string]storage.ExternalArtistProfile, error) {
	endpoint := strings.TrimRight(m.musicBrainzBase, "/") + "/artist/?query=" + url.QueryEscape(query) + "&fmt=json&limit=" + strconv.Itoa(limit)
	var response mbSearchResponse
	if err := m.mbRequest(ctx, endpoint, setting, &response); err != nil {
		return nil, nil, err
	}
	profiles := map[string]storage.ExternalArtistProfile{}
	var result []storage.ArtistCandidate
	for _, value := range response.Artists {
		score := value.Score
		if normalize(value.Name) == normalize(artist.Name) && score < 85 {
			score = 85
		}
		if score > 91 {
			score = 85
		}
		aliases, structuredAliases := unpackMBAliases(value.Aliases)
		tags := make([]string, 0, len(value.Tags))
		for _, v := range value.Tags {
			tags = append(tags, v.Name)
		}
		payload := profilePayloadFull("https://musicbrainz.org/artist/"+value.ID, "", "", aliases, tags, nil, structuredAliases)
		result = append(result, storage.ArtistCandidate{Source: "musicbrainz", ExternalID: value.ID, DisplayName: value.Name, SortName: value.SortName, Disambiguation: value.Disambiguation, Country: value.Country, ArtistType: value.Type, MBID: value.ID, Score: score, Evidence: []string{"MusicBrainz 搜索评分"}, Payload: payload})
		profiles[value.ID] = storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: value.ID, DisplayName: value.Name, SortName: value.SortName, PageURL: "https://musicbrainz.org/artist/" + value.ID, Country: value.Country, ArtistType: value.Type, Disambiguation: value.Disambiguation, Aliases: aliases, StructuredAliases: structuredAliases, Tags: tags, Raw: payload}
	}
	return result, profiles, nil
}

func (m *Manager) musicBrainzLookup(ctx context.Context, mbid string, setting storage.MetadataSourceSetting) (storage.ArtistCandidate, storage.ExternalArtistProfile, error) {
	return m.musicBrainzLookupWithImageSources(ctx, mbid, setting, false)
}

func (m *Manager) musicBrainzLookupWithImageSources(ctx context.Context, mbid string, setting storage.MetadataSourceSetting, expandedImages bool) (storage.ArtistCandidate, storage.ExternalArtistProfile, error) {
	var value mbArtistResponse
	endpoint := strings.TrimRight(m.musicBrainzBase, "/") + "/artist/" + url.PathEscape(mbid) + "?inc=aliases+tags+url-rels&fmt=json"
	if err := m.mbRequest(ctx, endpoint, setting, &value); err != nil {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, err
	}
	aliases, structuredAliases := unpackMBAliases(value.Aliases)
	tags := make([]string, 0, len(value.Tags))
	for _, v := range value.Tags {
		tags = append(tags, v.Name)
	}
	imageURL := ""
	var wikidataResource string
	var spotifyURLs []string
	var deezerIDs []string
	for _, relation := range value.Relations {
		if relation.Type == "wikidata" {
			if wikidataResource == "" {
				wikidataResource = relation.URL.Resource
			}
			var imageErr error
			imageURL, imageErr = m.wikidataImage(ctx, relation.URL.Resource)
			if imageErr != nil {
				// Rate limiting must abort the match; ordinary image lookup
				// failures stay best-effort.
				if asRateLimited(imageErr) != nil {
					return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, imageErr
				}
			}
			if imageURL != "" {
				break
			}
		}
		if strings.Contains(relation.URL.Resource, "open.spotify.com/artist/") {
			spotifyURLs = append(spotifyURLs, relation.URL.Resource)
		}
		if dzID := extractDeezerArtistID(relation.URL.Resource); dzID != "" {
			deezerIDs = append(deezerIDs, dzID)
		}
	}
	if imageURL == "" {
		for _, spotifyURL := range spotifyURLs {
			var imageErr error
			imageURL, imageErr = m.spotifyImage(ctx, spotifyURL)
			if imageErr != nil {
				// Stop the loop on rate limiting instead of hammering the next URL.
				if asRateLimited(imageErr) != nil {
					return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, imageErr
				}
				continue
			}
			if imageURL != "" {
				break
			}
		}
	}
	var imgSt storage.ArtistImageSettings
	if expandedImages && imageURL == "" && (len(deezerIDs) > 0 || wikidataResource != "") {
		imgSt, _ = m.store.ArtistImageSettings(ctx)
	}
	if imageURL == "" && imgSt.DeezerEnabled {
		for _, dzID := range deezerIDs {
			dzImg, dzErr := m.deezerArtistImageByID(ctx, dzID)
			if dzErr != nil {
				if asRateLimited(dzErr) != nil {
					return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, dzErr
				}
				continue
			}
			if dzImg != "" {
				imageURL = dzImg
				break
			}
		}
	}
	if imageURL == "" && wikidataResource != "" && imgSt.WikipediaEnabled {
		wikiImg, wikiErr := m.wikipediaArtistImage(ctx, wikidataResource)
		if wikiErr != nil {
			if asRateLimited(wikiErr) != nil {
				return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, wikiErr
			}
		} else {
			imageURL = wikiImg
		}
	}
	payload := profilePayloadFull("https://musicbrainz.org/artist/"+value.ID, imageURL, "", aliases, tags, deezerIDs, structuredAliases)
	profile := storage.ExternalArtistProfile{Source: "musicbrainz", ExternalID: value.ID, DisplayName: value.Name, SortName: value.SortName, PageURL: "https://musicbrainz.org/artist/" + value.ID, RemoteImageURL: imageURL, Country: value.Country, ArtistType: value.Type, Disambiguation: value.Disambiguation, Aliases: aliases, StructuredAliases: structuredAliases, Tags: tags, Raw: payload}
	return storage.ArtistCandidate{Source: "musicbrainz", ExternalID: value.ID, DisplayName: value.Name, SortName: value.SortName, Disambiguation: value.Disambiguation, Country: value.Country, ArtistType: value.Type, MBID: value.ID, Payload: payload}, profile, nil
}

func (m *Manager) waitBangumiRateLimit(ctx context.Context) error {
	err := m.gate.Wait(ctx, "bangumi")
	var cd *sourcegate.CooldownError
	if errors.As(err, &cd) {
		return &RateLimitError{CooldownOnly: true, Source: cd.Source, StatusCode: 429, RetryAfter: cd.RetryAfter}
	}
	return err
}

// waitMBRateLimit enforces the MusicBrainz request-spacing policy (default
// 2s, below MB's official 1 request/second cap; see defaultMBInterval). Every
// request to musicbrainz.org — direct or cache-backed — must go through this.
func (m *Manager) waitMBRateLimit(ctx context.Context) error {
	err := m.gate.Wait(ctx, "musicbrainz")
	var cd *sourcegate.CooldownError
	if errors.As(err, &cd) {
		return &RateLimitError{CooldownOnly: true, Source: cd.Source, StatusCode: 429, RetryAfter: cd.RetryAfter}
	}
	return err
}

func (m *Manager) mbRequest(ctx context.Context, endpoint string, setting storage.MetadataSourceSetting, target any) error {
	if err := m.waitMBRateLimit(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent(setting))
	return m.doSourceJSON(m.client, req, "musicbrainz", target)
}

type lastFMResponse struct {
	Error   int    `json:"error"`
	Message string `json:"message"`
	Artist  struct {
		Name, MBID, URL string
		Image           []struct {
			URL  string `json:"#text"`
			Size string `json:"size"`
		} `json:"image"`
		Bio     struct{ Summary, Content string }
		Tags    struct{ Tag []struct{ Name string } }
		Similar struct{ Artist []struct{ Name string } }
	} `json:"artist"`
}

func (m *Manager) lastFMInfo(ctx context.Context, artist storage.ArtistMatchInput, setting storage.MetadataSourceSetting) (storage.ArtistCandidate, storage.ExternalArtistProfile, error) {
	return m.lastFMInfoLanguage(ctx, artist, setting, setting.Language)
}

func (m *Manager) lastFMInfoLanguage(ctx context.Context, artist storage.ArtistMatchInput, setting storage.MetadataSourceSetting, language string) (storage.ArtistCandidate, storage.ExternalArtistProfile, error) {
	if setting.APIKey == "" {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, errors.New("Last.fm API key is not configured")
	}
	artist, err := m.store.ArtistMatchQueryContext(ctx, artist)
	if err != nil {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, err
	}
	query := url.Values{"method": {"artist.getinfo"}, "artist": {artist.Name}, "api_key": {setting.APIKey}, "format": {"json"}, "autocorrect": {"1"}, "lang": {normalizeLanguage(language)}}
	if artist.LastFMNoAutocorrect {
		query.Set("autocorrect", "0")
	}
	if mbid := artist.LastFMQueryMBID; mbid != "" {
		query.Del("artist")
		query.Set("mbid", mbid)
	}
	endpoint := "https://ws.audioscrobbler.com/2.0/?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, err
	}
	req.Header.Set("User-Agent", userAgent(setting))
	var response lastFMResponse
	if err = m.doSourceJSON(m.client, req, "lastfm", &response); err != nil {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, err
	}
	if response.Error == 29 {
		// Last.fm error 29 is "rate limit exceeded"; the API reports it with a
		// 200 (or 429) status and a JSON error payload.
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, m.rateLimitedError("lastfm", http.StatusTooManyRequests, defaultRateLimitBackoff)
	}
	if response.Error != 0 && response.Error != 6 {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, fmt.Errorf("Last.fm error %d", response.Error)
	}
	if response.Artist.Name == "" {
		return storage.ArtistCandidate{}, storage.ExternalArtistProfile{}, errArtistNotFound
	}
	var tags []string
	for _, v := range response.Artist.Tags.Tag {
		tags = append(tags, v.Name)
	}
	biography := stripLastFMLink(response.Artist.Bio.Content)
	score := 72
	if normalize(response.Artist.Name) == normalize(artist.Name) {
		score = 82
	}
	externalID := response.Artist.MBID
	if externalID == "" {
		externalID = response.Artist.URL
	}
	imageURL := ""
	for _, image := range response.Artist.Image {
		if strings.TrimSpace(image.URL) != "" {
			imageURL = strings.TrimSpace(image.URL)
		}
	}
	if strings.Contains(imageURL, "2a96cbd8b46e442fc41c2b86b821562f") {
		imageURL = ""
	}
	payload := profilePayload(response.Artist.URL, imageURL, biography, nil, tags)
	profile := storage.ExternalArtistProfile{Source: "lastfm", ExternalID: externalID, DisplayName: response.Artist.Name, PageURL: response.Artist.URL, RemoteImageURL: imageURL, Biography: biography, Tags: tags, Raw: payload}
	candidate := storage.ArtistCandidate{Source: "lastfm", ExternalID: externalID, DisplayName: response.Artist.Name, MBID: response.Artist.MBID, Score: score, Evidence: []string{"Last.fm 名称匹配"}, Payload: payload}
	return candidate, profile, nil
}

// validatePublicImageURL ensures an image URL is http(s) and does not point
// at loopback, private (RFC1918), link-local (incl. 169.254.169.254) or
// otherwise non-public address space.
func validatePublicImageURL(raw string) error {
	return netfetch.ValidatePublicURL(context.Background(), raw)
}

func profilePayload(pageURL, imageURL, biography string, aliases, tags []string) json.RawMessage {
	return profilePayloadWithRelations(pageURL, imageURL, biography, aliases, tags, nil)
}

func profilePayloadWithRelations(pageURL, imageURL, biography string, aliases, tags, deezerIDs []string) json.RawMessage {
	return profilePayloadFull(pageURL, imageURL, biography, aliases, tags, deezerIDs, nil)
}

func profilePayloadFull(pageURL, imageURL, biography string, aliases, tags, deezerIDs []string, structuredAliases []storage.StructuredArtistAlias) json.RawMessage {
	value := map[string]any{"pageUrl": pageURL, "imageUrl": imageURL, "biography": biography, "aliases": aliases, "tags": tags}
	if len(deezerIDs) > 0 {
		value["deezerIds"] = deezerIDs
	}
	if len(structuredAliases) > 0 {
		value["structuredAliases"] = structuredAliases
	}
	data, _ := json.Marshal(value)
	return data
}

func (m *Manager) CacheArtistImage(ctx context.Context, artistID int64) error {
	source, remoteURL, err := m.store.ArtistImageSource(ctx, artistID)
	if err != nil {
		return err
	}
	// The URL originates from upstream metadata (Last.fm, Spotify oEmbed,
	// community-editable MusicBrainz relations), so it is untrusted input:
	// downloadPublicImage refuses private/loopback/link-local targets.
	data, mimeType, extension, err := m.downloadPublicImage(ctx, remoteURL, "artist image")
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	cachePath, err := writeCacheFile(m.imageDirectory, hash+extension, data)
	if err != nil {
		return err
	}
	return m.store.SaveArtistImage(ctx, storage.ArtistImageInput{ArtistID: artistID, ByteSize: int64(len(data)), Source: source, RemoteURL: remoteURL, Hash: hash, MIMEType: mimeType, CachePath: cachePath})
}

func (m *Manager) RefreshConfirmedArtistImage(ctx context.Context, artistID int64) error {
	refreshed := false
	mbid, err := m.store.ArtistExternalID(ctx, artistID, "musicbrainz")
	if err == nil && mbid != "" {
		setting, settingErr := m.store.MetadataSourceSetting(ctx, "musicbrainz")
		if settingErr == nil && setting.Enabled {
			_, profile, lookupErr := m.musicBrainzLookupWithImageSources(ctx, mbid, setting, true)
			if lookupErr == nil {
				if upsertErr := m.store.UpsertExternalArtistProfile(ctx, artistID, profile); upsertErr != nil {
					return upsertErr
				}
				refreshed = true
			} else if asRateLimited(lookupErr) != nil {
				return lookupErr
			}
		}
	}
	if _, lastFMErr := m.store.ArtistExternalID(ctx, artistID, "lastfm"); lastFMErr == nil {
		setting, settingErr := m.store.MetadataSourceSetting(ctx, "lastfm")
		if settingErr == nil && setting.Enabled {
			artist, artistErr := m.store.ArtistForMatching(ctx, artistID)
			if artistErr == nil {
				_, profile, lookupErr := m.lastFMInfo(ctx, artist, setting)
				if lookupErr == nil {
					if upsertErr := m.store.UpsertExternalArtistProfile(ctx, artistID, profile); upsertErr != nil {
						return upsertErr
					}
					refreshed = true
				} else if asRateLimited(lookupErr) != nil {
					return lookupErr
				}
			}
		}
	}
	if !refreshed {
		return sql.ErrNoRows
	}
	return m.CacheArtistImage(ctx, artistID)
}

func (m *Manager) RefreshArtistBiographies(ctx context.Context, artistID int64, force bool) error {
	return m.refreshArtistBiographies(ctx, artistID, force, nil, BiographyRefreshOptions{})
}

func (m *Manager) RefreshArtistBiographiesWithOptions(ctx context.Context, artistID int64, force bool, opts BiographyRefreshOptions) error {
	return m.refreshArtistBiographies(ctx, artistID, force, nil, opts)
}

// refreshArtistBiographies 抓取并写入一位歌手的简介版本行。简介一律写规范
// （未合并）歌手行；身份快照为沿合并链解析出的 (ownerID, mbid)（分叉 C1，
// 与展示层继承口径一致）。每次落库走 UpsertArtistBiographyGuarded（H1）：
// 网络窗口内身份被解除/改认、歌手被合并或补全任务被暂停/停止/恢复抢先后，
// 迟到的写入一律被拒绝并中止本次刷新。checkpoint 仅补全 run 传入；交互
// 路径传 nil（仍保留身份与合并状态校验）。语言优先级/来源启用/密钥/缓存
// 天数在每次调用时重读，绝不使用快照配置。
func (m *Manager) refreshArtistBiographies(ctx context.Context, artistID int64, force bool, checkpoint *storage.ArtistBiographyBackfillCheckpoint, opts BiographyRefreshOptions) error {
	canonicalID, err := m.store.CanonicalArtistID(ctx, artistID)
	if err != nil {
		return err
	}
	settings, err := m.store.BiographySettings(ctx)
	if err != nil {
		return err
	}
	languages := languageList(settings.PreferredLanguages, settings.EnglishFallback)
	targetTag := resolveTargetLanguageTag(settings)
	targetShort := normalizeBioShortLanguage(targetTag)
	if len(languages) == 0 && !settings.AITranslateEnabled && !settings.AIWebFallbackEnabled && !opts.AllowWebFallback {
		return ErrNoBiographyLanguages
	}
	if len(languages) == 0 && targetShort != "" {
		languages = []string{targetShort}
	}

	wikipediaNeeded := settings.WikipediaEnabled && !opts.cachedTranslationOnly
	lastFMSetting, lastFMErr := m.store.MetadataSourceSetting(ctx, "lastfm")
	lastFMNeeded := !opts.cachedTranslationOnly && lastFMErr == nil && lastFMSetting.Enabled && lastFMSetting.APIKey != ""
	aiTranslateNeeded := settings.AITranslateEnabled && targetShort != ""
	aiWebNeeded := !opts.cachedTranslationOnly && (settings.AIWebFallbackEnabled || opts.AllowWebFallback) && targetShort != ""
	if !force {
		wikipediaNeeded = wikipediaNeeded && m.biographySourceNeedsRefresh(ctx, canonicalID, "wikipedia", languages, settings.CacheDays)
		lastFMNeeded = lastFMNeeded && m.biographySourceNeedsRefresh(ctx, canonicalID, "lastfm", languages, settings.CacheDays)
		if aiTranslateNeeded && !opts.cachedTranslationOnly {
			fresh, _ := m.store.ArtistBiographyFresh(ctx, canonicalID, "ai_translated", targetTag, settings.CacheDays)
			aiTranslateNeeded = !fresh
		}
		if aiWebNeeded && !opts.AllowWebFallback {
			fresh, _ := m.store.ArtistBiographyFresh(ctx, canonicalID, "ai_web", targetTag, settings.CacheDays)
			aiWebNeeded = !fresh
		}
	}
	if !wikipediaNeeded && !lastFMNeeded && !aiTranslateNeeded && !aiWebNeeded {
		return nil
	}

	artist, err := m.store.ArtistForMatching(ctx, canonicalID)
	if err != nil {
		return err
	}
	var upsert func(storage.ArtistBiography) error
	identity, identityErr := m.store.ArtistBiographySource(ctx, canonicalID)
	if identityErr != nil && !errors.Is(identityErr, sql.ErrNoRows) {
		return identityErr
	}
	if identityErr != nil {
		if opts.AllowWebFallback && aiWebNeeded && checkpoint == nil {
			wikipediaNeeded = false
			lastFMNeeded = false
			aiTranslateNeeded = false
			upsert = func(value storage.ArtistBiography) error {
				return m.store.UpsertUnmatchedArtistWebBiographyGuarded(ctx, canonicalID, value)
			}
		} else {
			return sql.ErrNoRows
		}
	} else {
		if identity.Source != "musicbrainz" {
			// Wikipedia lookup relies on MusicBrainz url-rels -> Wikidata; when only
			// a Last.fm identity is confirmed, restrict to anchored Last.fm bios.
			wikipediaNeeded = false
			if !lastFMNeeded && !aiTranslateNeeded && !aiWebNeeded {
				if lastFMErr == nil && lastFMSetting.Enabled && lastFMSetting.APIKey != "" {
					return nil
				}
				return sql.ErrNoRows
			}
		}
		if !configureArtistForBiographySource(&artist, identity) {
			return sql.ErrNoRows
		}
		upsert = func(value storage.ArtistBiography) error {
			return m.store.UpsertArtistBiographyForIdentityGuarded(ctx, canonicalID, identity, value, checkpoint)
		}
	}

	var wikidataResource string
	var artistCountry string
	var mbOfficialURLs []string
	wikidataChecked := false
	resolved := false
	var candidates []biographyCandidate
	var sitelinks map[string]string
	needMBRelations := wikipediaNeeded || (aiWebNeeded && identity.Source == "musicbrainz")
	if needMBRelations && identity.Source == "musicbrainz" {
		mbSetting, settingErr := m.store.MetadataSourceSetting(ctx, "musicbrainz")
		if settingErr == nil {
			var value mbArtistResponse
			endpoint := strings.TrimRight(m.musicBrainzBase, "/") + "/artist/" + url.PathEscape(identity.ExternalID) + "?inc=url-rels&fmt=json"
			if lookupErr := m.mbRequest(ctx, endpoint, mbSetting, &value); lookupErr != nil {
				if asRateLimited(lookupErr) != nil {
					return lookupErr
				}
				m.logger.Warn("load MusicBrainz relations for biography", "artistId", canonicalID, "error", lookupErr)
			} else {
				wikidataChecked = true
				artistCountry = value.Country
				for _, relation := range value.Relations {
					if relation.Type == "wikidata" && wikidataResource == "" {
						wikidataResource = relation.URL.Resource
					}
					switch relation.Type {
					case "official homepage", "discogs", "allmusic", "bandcamp", "other databases":
						if websearch.SafePageURL(relation.URL.Resource) {
							mbOfficialURLs = append(mbOfficialURLs, relation.URL.Resource)
						}
					}
				}
			}
		}
	}

	if wikipediaNeeded && wikidataResource != "" {
		var linkErr error
		sitelinks, linkErr = m.wikidataSitelinks(ctx, wikidataResource)
		if linkErr != nil {
			if asRateLimited(linkErr) != nil {
				return linkErr
			}
			m.logger.Warn("load Wikidata sitelinks", "artistId", canonicalID, "error", linkErr)
		} else {
			fetchLangs := languages
			if settings.AITranslateEnabled {
				fetchLangs = biographyCandidateLanguages(sitelinks, artistCountry, targetShort, languages)
				// Preserve missing cache rows for configured languages absent from sitelinks.
				for _, language := range languages {
					if sitelinks[language] == "" {
						if storeErr := upsert(storage.ArtistBiography{Source: "wikipedia", Language: language, Status: "missing"}); storeErr != nil {
							return storeErr
						}
						resolved = true
					}
				}
			}
			for _, language := range fetchLangs {
				title := sitelinks[language]
				if title == "" {
					if storeErr := upsert(storage.ArtistBiography{Source: "wikipedia", Language: language, Status: "missing"}); storeErr != nil {
						return storeErr
					}
					resolved = true
					continue
				}
				biography, pageURL, fetchErr := m.wikipediaSummary(ctx, language, title)
				if fetchErr != nil {
					if asRateLimited(fetchErr) != nil {
						return fetchErr
					}
					m.logger.Warn("fetch Wikipedia biography", "artistId", canonicalID, "language", language, "error", fetchErr)
					continue
				}
				if storeErr := upsert(storage.ArtistBiography{Source: "wikipedia", Language: language, Biography: biography, PageURL: pageURL}); storeErr != nil {
					return storeErr
				}
				resolved = true
			}
		}
	} else if wikipediaNeeded && wikidataChecked {
		for _, language := range languages {
			if storeErr := upsert(storage.ArtistBiography{Source: "wikipedia", Language: language, Status: "missing"}); storeErr != nil {
				return storeErr
			}
			resolved = true
		}
	}

	if lastFMNeeded {
		for _, language := range languages {
			_, profile, fetchErr := m.lastFMInfoLanguage(ctx, artist, lastFMSetting, language)
			if fetchErr != nil {
				if asRateLimited(fetchErr) != nil {
					return fetchErr
				}
				if errors.Is(fetchErr, errArtistNotFound) {
					// P1-A：明确的“未收录”（Last.fm error 6 / 空结果）写 missing
					// 行并计入 resolved——cache_days 窗口内不再反复打源、不计
					// failed。传输层错误、5xx、鉴权失败与 HTTP 404（Last.fm 对
					// 未收录查询不以此表达）绝不误记 missing。
					if storeErr := upsert(storage.ArtistBiography{Source: "lastfm", Language: language, Status: "missing"}); storeErr != nil {
						return storeErr
					}
					resolved = true
					continue
				}
				m.logger.Warn("fetch Last.fm biography", "artistId", canonicalID, "language", language, "error", RedactSourceError(fetchErr))
				continue
			}
			if storeErr := upsert(storage.ArtistBiography{Source: "lastfm", Language: language, Biography: profile.Biography, PageURL: profile.PageURL}); storeErr != nil {
				return storeErr
			}
			resolved = true
		}
	}

	// Reuse cached native rows when AI translation or web fallback is enabled
	// so toggling AI settings does not force redundant Wikipedia/Last.fm calls.
	if aiTranslateNeeded || aiWebNeeded {
		existing, listErr := m.store.ArtistBiographies(ctx, canonicalID)
		if listErr != nil {
			return listErr
		}
		for _, row := range existing {
			if row.Source == "wikipedia" || row.Source == "lastfm" {
				candidates = append(candidates, biographyCandidate{
					Source:   row.Source,
					Language: row.Language,
					Text:     row.Biography,
					PageURL:  row.PageURL,
					Score:    biographyInfoScore(row.Source, row.Biography),
				})
			}
		}
	}

	best, nativeTarget, hasBest := selectBestBiographyCandidate(candidates, targetShort)

	// Tier 2: translate the most complete source edition into targetLanguage.
	if aiTranslateNeeded && hasBest {
		if shouldSkipAITranslationForTarget(nativeTarget, best, targetTag) {
			resolved = true
		} else {
			// If the best Wikipedia lead is unusually short, try a bounded full-article excerpt.
			if best.Source == "wikipedia" && best.Score < bioShortIntroThreshold && sitelinks[best.Language] != "" {
				if excerpt, exErr := m.wikipediaFullExcerpt(ctx, best.Language, sitelinks[best.Language], bioMaxWikiExcerptRunes); exErr == nil && biographyInfoScore("wikipedia", excerpt) > best.Score {
					best.Text = excerpt
					best.Score = biographyInfoScore("wikipedia", excerpt)
				}
			}
			translated, usedModel, trErr := m.translateArtistBiography(ctx, artist.Name, best, settings)
			if trErr != nil {
				if opts.cachedTranslationOnly {
					return trErr
				}
				m.logger.Warn("AI translate artist biography", "artistId", canonicalID, "originSource", best.Source, "originLanguage", best.Language, "error", RedactSourceError(trErr))
			} else {
				if storeErr := upsert(storage.ArtistBiography{
					Source:         "ai_translated",
					Language:       targetTag,
					Biography:      translated,
					PageURL:        best.PageURL,
					OriginSource:   best.Source,
					OriginLanguage: best.Language,
					Model:          usedModel,
				}); storeErr != nil {
					return storeErr
				}
				resolved = true
			}
		}
	}

	// Tier 3: when no usable Wikipedia/Last.fm biography exists, synthesize one
	// strictly from identity-anchored web search evidence.
	var webFallbackErr error
	if aiWebNeeded && (!hasBest || best.Score < 40) {
		bio, pageURL, usedModel, ok, webErr := m.synthesizeArtistBiographyFromWeb(ctx, canonicalID, mbOfficialURLs, settings)
		if webErr != nil {
			if opts.AllowWebFallback {
				return webErr
			}
			webFallbackErr = webErr
			m.logger.Warn("AI web synthesize artist biography", "artistId", canonicalID, "error", RedactSourceError(webErr))
		} else if ok {
			if storeErr := upsert(storage.ArtistBiography{
				Source:       "ai_web",
				Language:     targetTag,
				Biography:    bio,
				PageURL:      pageURL,
				OriginSource: "web",
				Model:        usedModel,
			}); storeErr != nil {
				return storeErr
			}
			resolved = true
		} else {
			if storeErr := upsert(storage.ArtistBiography{
				Source:   "ai_web",
				Language: targetTag,
				Status:   "missing",
			}); storeErr != nil {
				return storeErr
			}
			resolved = true
		}
	}

	if !resolved {
		if webFallbackErr != nil {
			return webFallbackErr
		}
		return errors.New("all enabled biography sources failed")
	}
	return nil
}

func (m *Manager) biographySourceNeedsRefresh(ctx context.Context, artistID int64, source string, languages []string, days int) bool {
	for _, language := range languages {
		fresh, err := m.store.ArtistBiographyFresh(ctx, artistID, source, language, days)
		if err != nil || !fresh {
			return true
		}
	}
	return false
}

func configureArtistForBiographySource(artist *storage.ArtistMatchInput, identity storage.ArtistBiographySourceIdentity) bool {
	if artist == nil {
		return false
	}
	if identity.Source == "musicbrainz" {
		if identity.ExternalID == "" {
			return false
		}
		artist.LastFMQueryMBID = identity.ExternalID
		artist.LastFMQueryCaptured = true
		artist.LastFMNoAutocorrect = false
		return true
	}
	if identity.Source == "lastfm" {
		artist.LastFMQueryCaptured = true
		if isUUIDString(identity.ExternalID) {
			artist.LastFMQueryMBID = strings.TrimSpace(identity.ExternalID)
			artist.LastFMNoAutocorrect = false
			return true
		}
		name := strings.TrimSpace(identity.DisplayName)
		if name == "" {
			name = lastFMNameFromExternalID(identity.ExternalID)
		}
		if name == "" {
			return false
		}
		artist.Name = name
		artist.LastFMQueryMBID = ""
		artist.LastFMNoAutocorrect = true
		return true
	}
	return false
}

func isUUIDString(raw string) bool {
	raw = strings.TrimSpace(raw)
	if len(raw) != 36 {
		return false
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func lastFMNameFromExternalID(externalID string) string {
	idx := strings.Index(externalID, "/music/")
	if idx < 0 {
		return ""
	}
	slug := externalID[idx+len("/music/"):]
	if q := strings.IndexAny(slug, "?#"); q >= 0 {
		slug = slug[:q]
	}
	slug = strings.Trim(slug, "/")
	if decoded, err := url.PathUnescape(slug); err == nil {
		return strings.TrimSpace(decoded)
	}
	return strings.TrimSpace(slug)
}

func (m *Manager) wikidataSitelinks(ctx context.Context, resource string) (map[string]string, error) {
	entityID, err := wikidataEntityID(resource)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.wikidata.org/wiki/Special:EntityData/"+url.PathEscape(entityID)+".json", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var document struct {
		Entities map[string]struct {
			Sitelinks map[string]struct{ Title string } `json:"sitelinks"`
		} `json:"entities"`
	}
	if err = m.doSourceJSON(m.client, request, "wikidata", &document); err != nil {
		return nil, err
	}
	entity, ok := document.Entities[entityID]
	if !ok {
		return nil, errors.New("Wikidata entity was not returned")
	}
	result := map[string]string{}
	for key, value := range entity.Sitelinks {
		if strings.HasSuffix(key, "wiki") && !strings.Contains(key, "_") {
			result[strings.TrimSuffix(key, "wiki")] = value.Title
		}
	}
	return result, nil
}

func (m *Manager) wikipediaSummary(ctx context.Context, language, title string) (string, string, error) {
	pageURL := "https://" + language + ".wikipedia.org/wiki/" + strings.ReplaceAll(url.PathEscape(title), "%20", "_")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+language+".wikipedia.org/api/rest_v1/page/summary/"+url.PathEscape(title), nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var summary struct {
		Extract     string `json:"extract"`
		ContentURLs struct {
			Desktop struct {
				Page string `json:"page"`
			} `json:"desktop"`
		} `json:"content_urls"`
	}
	if restErr := m.doSourceJSON(m.client, request, "wikipedia", &summary); restErr == nil && strings.TrimSpace(summary.Extract) != "" {
		if summary.ContentURLs.Desktop.Page != "" {
			pageURL = summary.ContentURLs.Desktop.Page
		}
		return strings.TrimSpace(summary.Extract), pageURL, nil
	} else if restErr != nil && asRateLimited(restErr) != nil {
		// Rate limited: back off instead of hammering the action API fallback.
		return "", "", restErr
	}

	query := url.Values{"action": {"query"}, "prop": {"extracts"}, "exintro": {"1"}, "explaintext": {"1"}, "redirects": {"1"}, "format": {"json"}, "formatversion": {"2"}, "titles": {title}}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://"+language+".wikipedia.org/w/api.php?"+query.Encode(), nil)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var action struct {
		Query struct {
			Pages []struct {
				Extract string `json:"extract"`
			}
		} `json:"query"`
	}
	if err = m.doSourceJSON(m.client, request, "wikipedia", &action); err != nil {
		return "", "", err
	}
	if len(action.Query.Pages) == 0 || strings.TrimSpace(action.Query.Pages[0].Extract) == "" {
		return "", pageURL, nil
	}
	return strings.TrimSpace(action.Query.Pages[0].Extract), pageURL, nil
}

func (m *Manager) wikidataImage(ctx context.Context, resource string) (string, error) {
	entityID, err := wikidataEntityID(resource)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.wikidata.org/wiki/Special:EntityData/"+url.PathEscape(entityID)+".json", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var document struct {
		Entities map[string]struct {
			Claims map[string][]struct {
				MainSnak struct {
					DataValue struct{ Value string }
				} `json:"mainsnak"`
			} `json:"claims"`
		} `json:"entities"`
	}
	if err = m.doSourceJSON(m.client, request, "wikidata", &document); err != nil {
		return "", err
	}
	entity, ok := document.Entities[entityID]
	if !ok || len(entity.Claims["P18"]) == 0 {
		return "", errors.New("wikidata artist has no P18 image")
	}
	filename := strings.TrimSpace(entity.Claims["P18"][0].MainSnak.DataValue.Value)
	if filename == "" {
		return "", errors.New("wikidata P18 image is empty")
	}
	return "https://commons.wikimedia.org/wiki/Special:Redirect/file/" + url.PathEscape(filename) + "?width=800", nil
}

func (m *Manager) spotifyImage(ctx context.Context, artistURL string) (string, error) {
	parsed, err := url.Parse(artistURL)
	if err != nil || parsed.Hostname() != "open.spotify.com" || !strings.HasPrefix(parsed.Path, "/artist/") {
		return "", errors.New("invalid Spotify artist URL")
	}
	query := url.Values{"url": {artistURL}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://open.spotify.com/oembed?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", m.metadataUserAgent(ctx))
	var response struct {
		ThumbnailURL string `json:"thumbnail_url"`
	}
	if err = m.doSourceJSON(m.client, request, "spotify", &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.ThumbnailURL) == "" {
		return "", errors.New("Spotify oEmbed returned no artist image")
	}
	return strings.TrimSpace(response.ThumbnailURL), nil
}
func artistProfileNameMatches(name string, profile storage.ExternalArtistProfile) bool {
	return storage.ArtistProfileNameMatches(name, profile)
}

func normalize(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func normalizeLanguage(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "jp" {
		return "ja"
	}
	if index := strings.IndexAny(value, "-_"); index > 0 {
		value = value[:index]
	}
	return value
}

func languageList(csv string, englishFallback bool) []string {
	var result []string
	for _, value := range strings.Split(csv, ",") {
		value = normalizeLanguage(value)
		if value == "" {
			continue
		}
		found := false
		for _, existing := range result {
			found = found || existing == value
		}
		if !found {
			result = append(result, value)
		}
	}
	if englishFallback {
		found := false
		for _, value := range result {
			found = found || value == "en"
		}
		if !found {
			result = append(result, "en")
		}
	}
	return result
}

func wikidataEntityID(resource string) (string, error) {
	parsed, err := url.Parse(resource)
	if err != nil {
		return "", err
	}
	entityID := strings.TrimSpace(filepath.Base(parsed.Path))
	if !strings.HasPrefix(entityID, "Q") {
		return "", errors.New("wikidata relation has no entity ID")
	}
	return entityID, nil
}
func stripLastFMLink(value string) string {
	if index := strings.Index(value, "<a href="); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

var errArtistNotFound = errors.New("artist not found")

func normalizeArtistCandidates(candidates []storage.ArtistCandidate, unsafe, composite bool) {
	if !unsafe && !composite {
		return
	}
	for i := range candidates {
		if unsafe {
			candidates[i].RequiresManualReview = true
		}
		if composite {
			evidence := "疑似合作署名，禁止自动绑定个人身份；请先修正艺术家关系"
			found := false
			for _, old := range candidates[i].Evidence {
				if old == evidence {
					found = true
				}
			}
			if !found {
				candidates[i].Evidence = append(candidates[i].Evidence, evidence)
			}
		}
		if candidates[i].Score >= 92 {
			candidates[i].Score = 85
		}
	}
}
