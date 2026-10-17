package tagging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lux032/032music-server/internal/lyrics"
	"github.com/lux032/032music-server/internal/storage"
)

// State 是打标任务的状态。
type State string

const (
	StateNotConfigured State = "not_configured" // 未填写接口地址/模型/Key
	StateDisabled      State = "disabled"
	StatePaused        State = "paused"
	StateIdle          State = "idle"
	StateRunning       State = "running"
	StateReviewing     State = "reviewing"
	StateSampling      State = "sampling"
	StateBackoff       State = "backoff" // 限流/网络错误，稍后自动重试
	StateBlocked       State = "blocked" // Key 无效、模型不存在等，修改设置后才会继续
	StateError         State = "error"
)

const (
	statsTTL = 5 * time.Second
	// permanentAfter：同一输入连续失败（输出无效、被过滤等）这么多次后暂停重试。
	permanentAfter = 4
	requestTimeout = 90 * time.Second
)

// Chat 是模型调用接口（测试中替换）。
type Chat interface {
	Complete(ctx context.Context, req ChatRequest) (ChatResult, error)
}

// Options 配置服务。
type Options struct {
	// AudioActive 报告声学分析是否已部署且开启；开启时等待声学特征再打标。
	AudioActive func(ctx context.Context) bool
	Now         func() time.Time
	// NewChat 根据设置创建模型客户端；默认为 NewChatClient。
	NewChat func(s storage.AISettings) (Chat, error)
	// Lyrics 返回曲目的歌词原文；默认读取外置 .lrc，再回退到内嵌歌词。
	Lyrics func(ctx context.Context, info storage.TaggingTrackInfo) string
}

// Current 是正在处理的曲目。
type Current struct {
	TrackID int64  `json:"trackId"`
	Title   string `json:"title"`
	Mode    string `json:"mode"` // tag | review | sample
}

// Service 是后台打标任务。
type Service struct {
	store  *storage.Store
	logger *slog.Logger
	opts   Options
	wake   chan struct{}
	wg     sync.WaitGroup
	limit  *limiter

	mu           sync.Mutex
	state        State
	detail       string
	current      map[int64]Current
	lastErr      string
	lastErrAt    time.Time
	backoffUntil time.Time
	backoffStep  int
	blockedAt    string // 被阻塞时设置的 UpdatedAt；设置变化后解除
	processed    int64
	session      storage.AIUsage
	jsonObject   map[string]bool // 不支持 json_schema 的模型
	labels       map[string]Labels
	chat         Chat
	chatKey      string
	statsCache   *storage.TaggingStats
	statsAt      time.Time
	statsAudio   bool
}

// New 创建服务；调用 Start 运行。
func New(store *storage.Store, logger *slog.Logger, opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AudioActive == nil {
		opts.AudioActive = func(context.Context) bool { return false }
	}
	if opts.NewChat == nil {
		opts.NewChat = func(s storage.AISettings) (Chat, error) {
			return NewChatClient(s.BaseURL, s.APIKey, s.ProxyURL, requestTimeout)
		}
	}
	s := &Service{store: store, logger: logger, opts: opts, wake: make(chan struct{}, 1), state: StateDisabled,
		current: map[int64]Current{}, jsonObject: map[string]bool{}, labels: map[string]Labels{}, limit: &limiter{}}
	if opts.Lyrics == nil {
		s.opts.Lyrics = s.defaultLyrics
	}
	return s
}

// Start 运行后台任务直到 ctx 取消。
func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

// Wait 等待后台任务退出。
func (s *Service) Wait() { s.wg.Wait() }

// Wake 让后台任务立即重新检查（设置变化、新的声学分析结果、扫描完成）。
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) run(ctx context.Context) {
	for {
		delay := s.Step(ctx)
		if ctx.Err() != nil {
			return
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Service) set(state State, detail string) {
	s.mu.Lock()
	s.state, s.detail = state, detail
	s.mu.Unlock()
}

func (s *Service) fail(err error) {
	s.mu.Lock()
	s.lastErr, s.lastErrAt = err.Error(), s.opts.Now()
	s.mu.Unlock()
}

// Step 执行一批工作，返回下一次执行前的等待时间（0 表示立即继续）。导出供测试同步驱动。
func (s *Service) Step(ctx context.Context) time.Duration {
	st, err := s.store.AISettings(ctx)
	if err != nil {
		s.fail(err)
		s.set(StateError, "读取设置失败")
		return time.Minute
	}
	if !st.Configured() {
		s.set(StateNotConfigured, "")
		return time.Hour
	}
	s.mu.Lock()
	blocked := s.blockedAt != "" && s.blockedAt == st.UpdatedAt
	if !blocked {
		s.blockedAt = ""
	}
	s.mu.Unlock()
	if blocked {
		return time.Hour
	}
	if wait := s.backoffRemaining(); wait > 0 {
		return wait
	}
	chat, err := s.client(st)
	if err != nil {
		s.block(st, err)
		return time.Hour
	}
	s.limit.setRate(st.RequestsPerMinute)

	if run, ok, err := s.store.LatestAISampleRun(ctx); err != nil {
		s.fail(err)
	} else if ok && (run.Status == "pending" || run.Status == "running") {
		return s.stepSample(ctx, chat, st, run)
	}

	if !st.TaggingEnabled {
		s.set(StateDisabled, "")
		return time.Hour
	}
	if st.TaggingPaused {
		s.set(StatePaused, "")
		return time.Hour
	}
	audioActive := s.opts.AudioActive(ctx)
	batch := st.Concurrency * 2
	cands, err := s.store.NextTaggingCandidates(ctx, audioActive, VocabVersion, PromptVersion, s.opts.Now(), nil, batch)
	if err != nil {
		s.fail(err)
		s.set(StateError, "读取待打标曲目失败")
		return time.Minute
	}
	review := false
	if len(cands) == 0 && st.ReviewActive() {
		if cands, err = s.store.NextReviewCandidates(ctx, s.opts.Now(), nil, batch); err != nil {
			s.fail(err)
			return time.Minute
		}
		review = len(cands) > 0
	}
	if len(cands) == 0 {
		s.set(StateIdle, "")
		return time.Hour
	}
	if review {
		s.set(StateReviewing, "")
	} else {
		s.set(StateRunning, "")
	}
	errs := make([]error, len(cands))
	sem := make(chan struct{}, st.Concurrency)
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c storage.TaggingCandidate) {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = s.tagOne(ctx, chat, st, c, review, audioActive)
		}(i, c)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return 0
	}
	if d := s.handleGlobal(st, errs); d > 0 {
		return d
	}
	return 0
}

// handleGlobal 处理批次中的整体性错误（Key 无效、限流、网络），返回需要等待的时间。
func (s *Service) handleGlobal(st storage.AISettings, errs []error) time.Duration {
	var worst *LLMError
	ok := false
	for _, err := range errs {
		var le *LLMError
		if err == nil {
			ok = true
			continue
		}
		if errors.As(err, &le) && isGlobal(le.Kind) {
			if worst == nil || severity(le.Kind) > severity(worst.Kind) {
				worst = le
			}
		}
	}
	if worst == nil {
		s.mu.Lock()
		s.backoffStep = 0
		s.mu.Unlock()
		return 0
	}
	if worst.Kind == KindAuth || worst.Kind == KindModel {
		s.block(st, worst)
		return time.Hour
	}
	if ok {
		// 部分成功：只做一次短暂退避，避免整体卡住。
		return s.backoff(worst, 0)
	}
	return s.backoff(worst, 1)
}

func isGlobal(k ErrorKind) bool {
	switch k {
	case KindAuth, KindModel, KindRateLimit, KindQuota, KindServer, KindNetwork:
		return true
	}
	return false
}

func severity(k ErrorKind) int {
	switch k {
	case KindAuth, KindModel:
		return 4
	case KindQuota:
		return 3
	case KindRateLimit:
		return 2
	}
	return 1
}

func (s *Service) block(st storage.AISettings, err error) {
	s.mu.Lock()
	s.blockedAt = st.UpdatedAt
	s.lastErr, s.lastErrAt = err.Error(), s.opts.Now()
	s.state, s.detail = StateBlocked, blockedDetail(err)
	s.mu.Unlock()
	s.logger.Warn("AI tagging blocked until settings change", "error", err)
}

func blockedDetail(err error) string {
	var le *LLMError
	if errors.As(err, &le) {
		switch le.Kind {
		case KindAuth:
			return "API Key 无效或没有权限"
		case KindModel:
			return "模型或接口地址不存在"
		}
	}
	return err.Error()
}

// backoff 设置整体退避。step=0 表示不增加退避级数。
func (s *Service) backoff(le *LLMError, step int) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backoffStep += step
	n := min(max(s.backoffStep, 1), 6)
	var d time.Duration
	switch le.Kind {
	case KindQuota:
		d = time.Hour
	case KindRateLimit:
		d = min(30*time.Second<<(n-1), 10*time.Minute)
	default:
		d = min(15*time.Second<<(n-1), 5*time.Minute)
	}
	if le.RetryAfter > d {
		d = min(le.RetryAfter, 2*time.Hour)
	}
	s.backoffUntil = s.opts.Now().Add(d)
	s.lastErr, s.lastErrAt = le.Error(), s.opts.Now()
	s.state, s.detail = StateBackoff, backoffDetail(le.Kind)
	return d
}

func backoffDetail(k ErrorKind) string {
	switch k {
	case KindQuota:
		return "配额已用完"
	case KindRateLimit:
		return "请求过于频繁（限流）"
	case KindServer:
		return "模型服务暂时不可用"
	}
	return "网络错误"
}

func (s *Service) backoffRemaining() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.backoffUntil.Sub(s.opts.Now()); d > 0 {
		return d
	}
	return 0
}

func (s *Service) client(st storage.AISettings) (Chat, error) {
	key := st.BaseURL + "\x00" + st.APIKey + "\x00" + st.ProxyURL
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chat != nil && s.chatKey == key {
		return s.chat, nil
	}
	c, err := s.opts.NewChat(st)
	if err != nil {
		return nil, err
	}
	s.chat, s.chatKey = c, key
	return c, nil
}

// ---------------------------------------------------------------- one track

type prepared struct {
	info   storage.TaggingTrackInfo
	ev     *AudioEvidence
	input  Input
	user   string
	hash   string
	lyrics bool
}

func (s *Service) prepare(ctx context.Context, st storage.AISettings, trackID int64) (prepared, error) {
	var p prepared
	info, err := s.store.TaggingTrackInfo(ctx, trackID)
	if err != nil {
		return p, err
	}
	p.info = info
	if f, ok, err := s.store.TrackAudioFeaturesByID(ctx, trackID); err != nil {
		return p, err
	} else if ok {
		labels, err := s.labelsFor(ctx, f.AnalysisVersion)
		if err != nil {
			return p, err
		}
		if len(labels.Genre) > 0 {
			ev := BuildAudioEvidence(f, labels)
			p.ev = &ev
		}
	}
	var album *AlbumSummary
	if p.ev != nil && info.AlbumTracks > 1 {
		rows, err := s.store.AlbumAudioFeatures(ctx, info.AlbumID, 60)
		if err != nil {
			return p, err
		}
		if len(rows) > 0 {
			if labels, err := s.labelsFor(ctx, rows[0].Version); err == nil && len(labels.Genre) > 0 {
				album = BuildAlbumSummary(rows, labels)
			}
		}
	}
	text := s.opts.Lyrics(ctx, info)
	p.input = BuildInput(info, p.ev, album, text, st.IncludeLyrics)
	p.lyrics = p.input.Lyrics != ""
	p.user, p.hash = p.input.Marshal()
	return p, nil
}

func (s *Service) labelsFor(ctx context.Context, version string) (Labels, error) {
	if version == "" {
		return Labels{}, nil
	}
	s.mu.Lock()
	l, ok := s.labels[version]
	s.mu.Unlock()
	if ok {
		return l, nil
	}
	raw, found, err := s.store.AudioAnalysisLabels(ctx, version)
	if err != nil || !found {
		return Labels{}, err
	}
	if l, err = ParseLabels(raw); err != nil {
		return Labels{}, err
	}
	s.mu.Lock()
	s.labels[version] = l
	s.mu.Unlock()
	return l, nil
}

func (s *Service) track(id int64, title, mode string) func() {
	s.mu.Lock()
	s.current[id] = Current{TrackID: id, Title: title, Mode: mode}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.current, id)
		s.mu.Unlock()
	}
}

// tagOne 为一首曲目打标（review=true 时用复核模型重新打标）。返回的错误只用于判断整体性问题；
// 单曲问题已记入失败表。
func (s *Service) tagOne(ctx context.Context, chat Chat, st storage.AISettings, c storage.TaggingCandidate, review, audioActive bool) error {
	p, err := s.prepare(ctx, st, c.TrackID)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.recordFailure(ctx, c, "准备输入失败："+err.Error())
		return nil
	}
	mode := "tag"
	if review {
		mode = "review"
	}
	defer s.track(c.TrackID, p.info.Title, mode)()
	meta, hasMeta, err := s.store.TrackTagMeta(ctx, c.TrackID)
	if err != nil {
		return err
	}
	if !review && hasMeta && meta.InputHash == p.hash && meta.VocabVersion == VocabVersion && meta.PromptVersion == PromptVersion {
		return s.store.TouchTrackTagStamp(ctx, c.TrackID, c.Stamp)
	}
	model := st.Model
	purpose := "tag"
	if review {
		model, purpose = st.ReviewModel, "review"
	}
	res, usage, err := s.call(ctx, chat, st, model, purpose, p)
	// The quality-review model remains independent from availability fallback.
	if !review && ShouldFallback(ctx, model, st.FallbackModel, err) {
		s.logger.Warn("AI primary unavailable; trying fallback", "model", model, "fallback", st.FallbackModel)
		model = strings.TrimSpace(st.FallbackModel)
		var extra storage.AIUsage
		res, extra, err = s.call(ctx, chat, st, model, purpose, p)
		usage.Add(extra)
	}
	if err != nil {
		return s.callFailed(ctx, c, err)
	}
	reasons := ReviewReasons(res, p.ev)
	reviewedBy := ""
	if review {
		reviewedBy = model
		if hasMeta && meta.ReviewReasons != "" {
			reasons = strings.Split(meta.ReviewReasons, ",")
		}
	} else if len(reasons) > 0 && st.ReviewActive() {
		res2, usage2, err2 := s.call(ctx, chat, st, st.ReviewModel, "review", p)
		usage.Add(usage2)
		if err2 == nil {
			res, model, reviewedBy = res2, st.ReviewModel, st.ReviewModel
		} else {
			var le *LLMError
			if errors.As(err2, &le) && isGlobal(le.Kind) {
				// 主模型结果先保存（标记待复核），整体问题交给上层退避。
				if serr := s.save(ctx, c, p, res, reasons, model, "", usage); serr != nil {
					return serr
				}
				return err2
			}
			s.logger.Warn("AI review failed; keeping primary result", "trackId", c.TrackID, "error", err2)
		}
	}
	return s.save(ctx, c, p, res, reasons, model, reviewedBy, usage)
}

func (s *Service) save(ctx context.Context, c storage.TaggingCandidate, p prepared, res Result, reasons []string, model, reviewedBy string, usage storage.AIUsage) error {
	resultJSON, _ := json.Marshal(res)
	meta := storage.TrackTagMeta{
		TrackID: c.TrackID, SourceStamp: c.Stamp, InputHash: p.hash, VocabVersion: VocabVersion, PromptVersion: PromptVersion,
		Model: model, Energy: res.Energy, Vocal: res.Vocal, Idol: res.Idol, UsedAudio: p.ev != nil, UsedLyrics: p.lyrics,
		NeedsReview: len(reasons) > 0, ReviewReasons: strings.Join(reasons, ","), ReviewedBy: reviewedBy,
		PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens, ResultJSON: string(resultJSON),
	}
	if err := s.store.SaveTrackTagResult(ctx, meta, res.Tags(), p.ev.AudioTags()); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.fail(err)
		return err
	}
	s.mu.Lock()
	s.processed++
	s.statsAt = time.Time{}
	s.mu.Unlock()
	return nil
}

// call 调用一次模型并校验输出；不支持 json_schema 时自动改用 json_object，输出无效时重试一次。
func (s *Service) call(ctx context.Context, chat Chat, st storage.AISettings, model, purpose string, p prepared) (Result, storage.AIUsage, error) {
	var total storage.AIUsage
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := s.limit.wait(ctx); err != nil {
			return Result{}, total, err
		}
		s.mu.Lock()
		format := "json_schema"
		if s.jsonObject[model] {
			format = "json_object"
		}
		s.mu.Unlock()
		res, err := chat.Complete(ctx, ChatRequest{Model: model, System: SystemPrompt(), User: p.user, Schema: ResponseSchema(), Format: format, ReasoningEffort: st.ReasoningEffort})
		if res.Usage.Requests > 0 || res.Usage.PromptTokens > 0 {
			total.Add(res.Usage)
			s.recordUsage(ctx, model, purpose, res.Usage)
		}
		if err != nil {
			var le *LLMError
			if errors.As(err, &le) && le.Kind == KindFormat && format == "json_schema" {
				s.mu.Lock()
				s.jsonObject[model] = true
				s.mu.Unlock()
				s.logger.Info("model does not accept json_schema; falling back to json_object", "model", model)
				lastErr = err
				continue
			}
			return Result{}, total, err
		}
		parsed, perr := ParseResult(res.Content, p.ev)
		if perr == nil {
			return parsed, total, nil
		}
		lastErr = perr
		if attempt >= 1 {
			break
		}
	}
	return Result{}, total, lastErr
}

func (s *Service) recordUsage(ctx context.Context, model, purpose string, u storage.AIUsage) {
	if u.Requests == 0 {
		u.Requests = 1
	}
	if err := s.store.AddAIUsage(ctx, s.opts.Now(), model, purpose, u); err != nil && ctx.Err() == nil {
		s.logger.Warn("record AI usage", "error", err)
	}
	s.mu.Lock()
	s.session.Add(u)
	s.mu.Unlock()
}

// callFailed 区分整体问题（返回给上层退避/阻塞）与单曲问题（记入失败表）。
func (s *Service) callFailed(ctx context.Context, c storage.TaggingCandidate, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var le *LLMError
	if errors.As(err, &le) && isGlobal(le.Kind) {
		return err
	}
	msg := err.Error()
	if errors.Is(err, ErrInvalidOutput) {
		msg = "模型输出无效：" + msg
	}
	s.recordFailure(ctx, c, msg)
	return err
}

func (s *Service) recordFailure(ctx context.Context, c storage.TaggingCandidate, msg string) {
	attempts, permanent, err := s.store.RecordTaggingFailure(ctx, c.TrackID, c.Stamp, msg, s.opts.Now(), permanentAfter)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("record tagging failure", "trackId", c.TrackID, "error", err)
		}
		return
	}
	s.logger.Warn("AI tagging failed", "trackId", c.TrackID, "attempts", attempts, "permanent", permanent, "error", msg)
	s.mu.Lock()
	s.lastErr, s.lastErrAt = msg, s.opts.Now()
	s.statsAt = time.Time{}
	s.mu.Unlock()
}

// defaultLyrics 先读外置 .lrc（与音频同名），再回退到内嵌歌词。
func (s *Service) defaultLyrics(ctx context.Context, info storage.TaggingTrackInfo) string {
	if path, err := s.store.AudioFilePath(ctx, info.TrackID); err == nil {
		if f, err := os.Open(lyrics.DetectLRCPath(path)); err == nil {
			data, rerr := io.ReadAll(io.LimitReader(f, 1<<20))
			f.Close()
			if rerr == nil && len(strings.TrimSpace(string(data))) > 0 {
				return strings.TrimPrefix(string(data), "\ufeff")
			}
		}
	}
	return info.Lyrics
}

// TrackEvidence 返回一首曲目的声学证据（没有声学特征时为 nil），供标签页展示。
func (s *Service) TrackEvidence(ctx context.Context, trackID int64) (*AudioEvidence, error) {
	f, ok, err := s.store.TrackAudioFeaturesByID(ctx, trackID)
	if err != nil || !ok {
		return nil, err
	}
	labels, err := s.labelsFor(ctx, f.AnalysisVersion)
	if err != nil || len(labels.Genre) == 0 {
		return nil, err
	}
	ev := BuildAudioEvidence(f, labels)
	return &ev, nil
}

// ---------------------------------------------------------------- sample comparison

// RequestSample 新建一次抽样对比（需要已填写复核模型）。
func (s *Service) RequestSample(ctx context.Context, size int) error {
	st, err := s.store.AISettings(ctx)
	if err != nil {
		return err
	}
	if !st.Configured() || strings.TrimSpace(st.ReviewModel) == "" {
		return errors.New("需要先填写接口、主模型和复核模型")
	}
	if size < 10 || size > 500 {
		return errors.New("抽样数量应在 10–500 之间")
	}
	if _, err := s.store.CreateAISampleRun(ctx, size, st.Model, st.ReviewModel, s.opts.Now().UnixNano()); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// CancelSample 取消正在进行的抽样。
func (s *Service) CancelSample(ctx context.Context) error {
	run, ok, err := s.store.LatestAISampleRun(ctx)
	if err != nil || !ok {
		return err
	}
	if run.Status == "pending" || run.Status == "running" {
		return s.store.SetAISampleRunStatus(ctx, run.ID, "cancelled", "", s.opts.Now())
	}
	return nil
}

func (s *Service) stepSample(ctx context.Context, chat Chat, st storage.AISettings, run storage.AISampleRun) time.Duration {
	if run.Status == "pending" {
		if err := s.store.SetAISampleRunStatus(ctx, run.ID, "running", "", s.opts.Now()); err != nil {
			s.fail(err)
			return time.Minute
		}
	}
	s.set(StateSampling, fmt.Sprintf("%d / %d", run.Done, run.Size))
	ids, err := s.store.PendingAISampleItems(ctx, run.ID, st.Concurrency)
	if err != nil {
		s.fail(err)
		return time.Minute
	}
	if len(ids) == 0 {
		_ = s.store.SetAISampleRunStatus(ctx, run.ID, "done", "", s.opts.Now())
		s.logger.Info("AI sample comparison finished", "run", run.ID, "size", run.Size)
		return 0
	}
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			errs[i] = s.sampleOne(ctx, chat, st, run, id)
		}(i, id)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return 0
	}
	for _, err := range errs {
		var le *LLMError
		if errors.As(err, &le) && (le.Kind == KindAuth || le.Kind == KindModel) {
			_ = s.store.SetAISampleRunStatus(ctx, run.ID, "failed", blockedDetail(le)+"："+le.Message, s.opts.Now())
		}
	}
	return s.handleGlobal(st, errs)
}

func (s *Service) sampleOne(ctx context.Context, chat Chat, st storage.AISettings, run storage.AISampleRun, trackID int64) error {
	p, err := s.prepare(ctx, st, trackID)
	if err != nil {
		return s.store.SaveAISampleItem(ctx, run.ID, trackID, "", "", "准备输入失败："+err.Error())
	}
	defer s.track(trackID, p.info.Title, "sample")()
	primary, _, err := s.call(ctx, chat, st, run.PrimaryModel, "sample", p)
	if err != nil {
		var le *LLMError
		if errors.As(err, &le) && isGlobal(le.Kind) {
			return err
		}
		return s.store.SaveAISampleItem(ctx, run.ID, trackID, "", "", err.Error())
	}
	reviewRes, _, err := s.call(ctx, chat, st, run.ReviewModel, "sample", p)
	pj, _ := json.Marshal(primary)
	if err != nil {
		var le *LLMError
		if errors.As(err, &le) && isGlobal(le.Kind) {
			return err
		}
		return s.store.SaveAISampleItem(ctx, run.ID, trackID, string(pj), "", "复核模型："+err.Error())
	}
	rj, _ := json.Marshal(reviewRes)
	return s.store.SaveAISampleItem(ctx, run.ID, trackID, string(pj), string(rj), "")
}

// ---------------------------------------------------------------- connection test

// TestResult 是一次连接测试的结果。
type TestResult struct {
	Model   string
	Format  string
	Latency time.Duration
	Usage   storage.AIUsage
	Result  Result
}

// TestConnection 用一首虚构曲目测试模型（同时验证结构化输出），不写入任何标签。
func (s *Service) TestConnection(ctx context.Context, st storage.AISettings, model string) (TestResult, error) {
	out := TestResult{Model: model, Format: "json_schema"}
	chat, err := s.opts.NewChat(st)
	if err != nil {
		return out, err
	}
	in := Input{Title: "夏の約束", Artist: "テストバンド", Album: "Summer Days", Year: 2020, DurationSec: 245, GenreTags: []string{"J-Pop"},
		Works: []InputWork{{Title: "テストアニメ", Type: "anime", Role: "op"}}, HasLyrics: true,
		Audio: &AudioEvidence{Genres: []Scored{{"Pop---J-pop", 0.62}, {"Rock---Pop Rock", 0.31}}, MoodTheme: []Scored{{"energetic", 0.21}, {"upbeat", 0.18}, {"summer", 0.12}},
			Heads: map[string]float64{"voice": 0.97, "danceable": 0.71, "happy": 0.66, "sad": 0.08}, Arousal: ptr(6.8), Valence: ptr(6.9), Energy: 4,
			StyleHints: []Scored{{"pop", 0.75}, {"rock", 0.31}}, Locked: []string{"pop"}, MoodHints: []Scored{{"cheerful", 1}, {"uplifting", 0.82}}},
		Lyrics: "夏の空に約束した\nいつかまた会えるように\n走り出そう 光の中へ"}
	user, _ := in.Marshal()
	for attempt := 0; attempt < 2; attempt++ {
		res, err := chat.Complete(ctx, ChatRequest{Model: model, System: SystemPrompt(), User: user, Schema: ResponseSchema(), Format: out.Format, ReasoningEffort: st.ReasoningEffort})
		if res.Usage.PromptTokens > 0 {
			out.Usage.Add(res.Usage)
			s.recordUsage(ctx, model, "test", res.Usage)
		}
		out.Latency += res.Latency
		if err != nil {
			var le *LLMError
			if errors.As(err, &le) && le.Kind == KindFormat && out.Format == "json_schema" {
				out.Format = "json_object"
				continue
			}
			return out, err
		}
		parsed, err := ParseResult(res.Content, in.Audio)
		if err != nil {
			return out, err
		}
		out.Result = parsed
		if out.Format == "json_object" {
			s.mu.Lock()
			s.jsonObject[model] = true
			s.mu.Unlock()
		}
		return out, nil
	}
	return out, errors.New("模型不支持结构化输出")
}

// ---------------------------------------------------------------- snapshot

// Snapshot 是给页面与 API 的状态。
type Snapshot struct {
	Configured    bool                  `json:"configured"`
	Enabled       bool                  `json:"enabled"`
	Paused        bool                  `json:"paused"`
	State         State                 `json:"state"`
	Detail        string                `json:"detail,omitempty"`
	Model         string                `json:"model,omitempty"`
	ReviewModel   string                `json:"reviewModel,omitempty"`
	ReviewActive  bool                  `json:"reviewActive"`
	IncludeLyrics bool                  `json:"includeLyrics"`
	AudioActive   bool                  `json:"audioActive"`
	Stats         *storage.TaggingStats `json:"stats,omitempty"`
	Pending       int64                 `json:"pending"`
	Current       []Current             `json:"current,omitempty"`
	Session       storage.AIUsage       `json:"sessionUsage"`
	Processed     int64                 `json:"processedSinceStart"`
	LastError     string                `json:"lastError,omitempty"`
	LastErrorAt   *time.Time            `json:"lastErrorAt,omitempty"`
	BackoffUntil  *time.Time            `json:"backoffUntil,omitempty"`
	Sample        *storage.AISampleRun  `json:"sample,omitempty"`
}

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	st, err := s.store.AISettings(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	audioActive := s.opts.AudioActive(ctx)
	now := s.opts.Now()
	s.mu.Lock()
	snap := Snapshot{Configured: st.Configured(), Enabled: st.TaggingEnabled, Paused: st.TaggingPaused, State: s.state, Detail: s.detail,
		Model: st.Model, ReviewModel: st.ReviewModel, ReviewActive: st.ReviewActive(), IncludeLyrics: st.IncludeLyrics, AudioActive: audioActive,
		Session: s.session, Processed: s.processed, LastError: s.lastErr}
	for _, c := range s.current {
		snap.Current = append(snap.Current, c)
	}
	if !s.lastErrAt.IsZero() {
		at := s.lastErrAt
		snap.LastErrorAt = &at
	}
	if s.backoffUntil.After(now) {
		at := s.backoffUntil
		snap.BackoffUntil = &at
	}
	cached, at, cachedAudio := s.statsCache, s.statsAt, s.statsAudio
	s.mu.Unlock()
	switch {
	case !snap.Configured:
		snap.State, snap.Detail = StateNotConfigured, ""
	case snap.State == StateBlocked || snap.State == StateSampling:
	case !st.TaggingEnabled:
		snap.State, snap.Detail = StateDisabled, ""
	case st.TaggingPaused && len(snap.Current) == 0:
		snap.State, snap.Detail = StatePaused, ""
	}
	stats := cached
	if stats == nil || cachedAudio != audioActive || now.Sub(at) > statsTTL {
		v, err := s.store.TaggingStats(ctx, audioActive, VocabVersion, PromptVersion, now)
		if err != nil {
			return snap, err
		}
		stats = &v
		s.mu.Lock()
		s.statsCache, s.statsAt, s.statsAudio = stats, now, audioActive
		s.mu.Unlock()
	}
	snap.Stats = stats
	snap.Pending = max(stats.Eligible-stats.Tagged-stats.Permanent-stats.WaitingAudio, 0)
	if run, ok, err := s.store.LatestAISampleRun(ctx); err == nil && ok {
		snap.Sample = &run
	}
	return snap, nil
}

// ---------------------------------------------------------------- limiter

// limiter 是简单的匀速限流器（每分钟 N 次请求，均匀间隔）。
type limiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
}

func (l *limiter) setRate(perMinute int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if perMinute <= 0 {
		perMinute = 1
	}
	l.interval = time.Minute / time.Duration(perMinute)
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	t := l.next
	if t.Before(now) {
		t = now
	}
	l.next = t.Add(l.interval)
	l.mu.Unlock()
	d := t.Sub(now)
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
