package aireview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lux032/032music-server/internal/aiclient"
	"github.com/lux032/032music-server/internal/artistaliases"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

const (
	autoThresholdAlbum       = 88
	autoThresholdTrack       = 88
	autoThresholdWork        = 90
	autoThresholdArtist      = 90
	autoThresholdCreditSplit = 92
	autoThresholdSeries      = 92
)

type Options struct {
	Limiter           *aiclient.Limiter
	NewChat           aiclient.ChatFactory
	NewWeb            func(storage.ArtistAliasSettings, storage.AISettings) (artistaliases.Web, error)
	OnWorkConfirmed   func(context.Context, int64)
	OnArtistConfirmed func(context.Context, int64)
}

type Service struct {
	store  *storage.Store
	logger *slog.Logger
	opts   Options

	mu        sync.Mutex
	activeRun *activeRunState
}

type EvalResult struct {
	Kind        string
	TargetID    int64
	Action      string
	Applied     bool
	Skipped     bool
	Confidence  int
	Reason      string
	WebSearched bool
}

type webPageEvidence struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func New(store *storage.Store, logger *slog.Logger, opts Options) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Limiter == nil {
		opts.Limiter = aiclient.NewLimiter(60)
	}
	if opts.NewChat == nil {
		opts.NewChat = aiclient.DefaultChatFactory
	}
	if opts.NewWeb == nil {
		opts.NewWeb = defaultWebFactory
	}
	if _, err := store.RecoverAIReviewRuns(context.Background()); err != nil {
		logger.Error("recover ai review runs", "error", err)
	}
	return &Service{store: store, logger: logger, opts: opts}
}

func defaultWebFactory(alias storage.ArtistAliasSettings, ai storage.AISettings) (artistaliases.Web, error) {
	key := strings.TrimSpace(alias.DeepSeekKey)
	u, _ := url.Parse(ai.BaseURL)
	if key == "" && u != nil && u.Hostname() == "api.deepseek.com" && u.Scheme == "https" {
		key = ai.APIKey
	}
	if key == "" {
		return nil, errors.New("未配置 DeepSeek 联网 API Key")
	}
	client, err := tagging.NewChatClient("https://api.deepseek.com", key, ai.ProxyURL, 30*time.Second)
	if err != nil {
		return nil, err
	}
	return &artistaliases.DeepSeekWeb{HTTP: client.HTTP, Key: key, BaseURL: "https://api.deepseek.com"}, nil
}

// EvaluateInline runs AI review synchronously right before an enrichment/matching
// item falls back to manual review. It returns true only when AI auto-applied
// the decision so the caller can count the item as matched/succeeded.
func (s *Service) EvaluateInline(ctx context.Context, kind string, targetID int64) bool {
	rev, err := s.store.AIReviewSettings(ctx)
	if err != nil || !rev.Enabled || !rev.InlineEnabled || rev.ModeForKind(kind) == storage.AIReviewModeOff {
		return false
	}
	res, err := s.EvaluateTarget(ctx, kind, targetID, false)
	if err != nil {
		s.logger.Warn("inline ai review failed", "kind", kind, "targetId", targetID, "error", err)
		return false
	}
	return res.Applied
}

func (s *Service) EvaluateTarget(ctx context.Context, kind string, targetID int64, force bool) (EvalResult, error) {
	ai, err := s.store.AISettings(ctx)
	if err != nil {
		return EvalResult{}, err
	}
	if !ai.Configured() {
		return EvalResult{}, errors.New("请先在 AI 设置中配置模型服务")
	}
	rev, err := s.store.AIReviewSettings(ctx)
	if err != nil {
		return EvalResult{}, err
	}
	mode := rev.ModeForKind(kind)
	if mode == storage.AIReviewModeOff {
		return EvalResult{Kind: kind, TargetID: targetID, Skipped: true}, nil
	}
	s.opts.Limiter.SetRate(ai.RequestsPerMinute)
	chat, err := s.opts.NewChat(ai)
	if err != nil {
		return EvalResult{}, err
	}
	model := aiclient.ResolveReviewModel(ai, rev)

	switch kind {
	case storage.AITargetAlbumSubject:
		return s.evalAlbum(ctx, ai, rev, chat, model, mode, targetID, force)
	case storage.AITargetTrackSubject:
		return s.evalTrack(ctx, ai, rev, chat, model, mode, targetID, force)
	case storage.AITargetWorkMatch:
		return s.evalWork(ctx, ai, rev, chat, model, mode, targetID, force)
	case storage.AITargetArtistMatch:
		return s.evalArtist(ctx, ai, rev, chat, model, mode, targetID, force)
	case storage.AITargetCreditSplit:
		return s.evalCreditSplit(ctx, ai, chat, model, mode, targetID, force)
	case storage.AITargetSeriesSuggestion:
		return s.evalSeriesSuggestion(ctx, ai, chat, model, mode, targetID, force)
	default:
		return EvalResult{}, fmt.Errorf("unsupported ai review kind %q", kind)
	}
}

func (s *Service) alreadyEvaluated(ctx context.Context, kind string, targetID int64, fingerprint string, force bool) (EvalResult, bool) {
	if force || fingerprint == "" {
		return EvalResult{}, false
	}
	existing, ok, err := s.store.AIReviewDecisionByTarget(ctx, kind, targetID)
	if err != nil || !ok || existing.Fingerprint != fingerprint || existing.Reverted {
		return EvalResult{}, false
	}
	return EvalResult{
		Kind: kind, TargetID: targetID, Action: existing.Action,
		Applied: existing.Applied, Skipped: true, Confidence: existing.Confidence,
		Reason: existing.Reason, WebSearched: existing.WebSearched,
	}, true
}

func (s *Service) invokeModel(ctx context.Context, ai storage.AISettings, chat tagging.Chat, model, system string, schema json.RawMessage, payload any, purpose string, out any) (string, error) {
	userBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	res, usedModel, err := aiclient.CompleteWithFallback(ctx, s.store, chat, s.opts.Limiter, ai, tagging.ChatRequest{
		Model:  model,
		System: system,
		User:   string(userBytes),
		Schema: schema,
		Format: "json_schema",
	}, purpose)
	if err != nil {
		return usedModel, err
	}
	if err = json.Unmarshal([]byte(res.Content), out); err != nil {
		return usedModel, fmt.Errorf("模型返回 JSON 解析失败: %w", err)
	}
	return usedModel, nil
}

func (s *Service) fetchWebEvidence(ctx context.Context, ai storage.AISettings, query string) ([]webPageEvidence, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	aliasSettings, err := s.store.ArtistAliasSettings(ctx)
	if err != nil {
		return nil, err
	}
	web, err := s.opts.NewWeb(aliasSettings, ai)
	if err != nil {
		return nil, err
	}
	results, err := web.Search(ctx, clipRunes(query, 120))
	if err != nil {
		return nil, err
	}
	pages := make([]webPageEvidence, 0, 2)
	for _, r := range results {
		if len(pages) >= 2 {
			break
		}
		text := strings.TrimSpace(r.Content)
		if text == "" && r.URL != "" {
			fetched, fetchErr := web.Fetch(ctx, r.URL)
			if fetchErr == nil {
				text = strings.TrimSpace(fetched)
			}
		}
		if text == "" {
			continue
		}
		pages = append(pages, webPageEvidence{URL: r.URL, Title: r.Title, Content: clipRunes(text, 8000)})
	}
	return pages, nil
}

func quoteGroundedInPages(quote string, pages []webPageEvidence) bool {
	quote = strings.TrimSpace(quote)
	if quote == "" || utf8.RuneCountInString(quote) < 4 {
		return false
	}
	for _, p := range pages {
		if strings.Contains(p.Content, quote) {
			return true
		}
	}
	return false
}

func clipRunes(v string, limit int) string {
	r := []rune(v)
	if len(r) > limit {
		return string(r[:limit])
	}
	return v
}

func decisionActionFromSelect(decision string) string {
	switch decision {
	case "select":
		return storage.AIActionRecommendAccept
	case "reject_all":
		return storage.AIActionRecommendReject
	default:
		return storage.AIActionUncertain
	}
}

// ---------------------------------------------------------------- albums

func (s *Service) evalAlbum(ctx context.Context, ai storage.AISettings, rev storage.AIReviewSettings, chat tagging.Chat, model, mode string, albumID int64, force bool) (EvalResult, error) {
	info, err := s.store.LoadAlbumAIContext(ctx, albumID)
	if err != nil {
		return EvalResult{}, err
	}
	if len(info.Candidates) == 0 {
		return EvalResult{Kind: storage.AITargetAlbumSubject, TargetID: albumID, Skipped: true}, nil
	}
	if cached, ok := s.alreadyEvaluated(ctx, storage.AITargetAlbumSubject, albumID, info.Fingerprint, force); ok {
		return cached, nil
	}

	var out albumReviewOutput
	usedModel, err := s.invokeModel(ctx, ai, chat, model, albumReviewPrompt, albumReviewSchema, info, "album_review", &out)
	if err != nil {
		return EvalResult{}, err
	}
	var pages []webPageEvidence
	webSearched := false
	if rev.WebSearchEnabled && out.Decision == "uncertain" && out.NeedWebSearch && strings.TrimSpace(out.SearchQuery) != "" {
		if fetched, webErr := s.fetchWebEvidence(ctx, ai, out.SearchQuery); webErr == nil && len(fetched) > 0 {
			pages = fetched
			webSearched = true
			payload := struct {
				storage.AlbumAIContext
				WebPages []webPageEvidence `json:"webPages"`
			}{AlbumAIContext: info, WebPages: pages}
			var second albumReviewOutput
			if m2, err2 := s.invokeModel(ctx, ai, chat, model, albumReviewPrompt, albumReviewSchema, payload, "album_review", &second); err2 == nil {
				out = second
				usedModel = m2
			}
		}
	}

	selectedCand, selectedWorks := validateAlbumOutput(info, out)
	action := decisionActionFromSelect(out.Decision)
	if out.Decision == "select" && (selectedCand == 0 || len(selectedWorks) == 0) {
		action = storage.AIActionUncertain
	}

	applied := false
	var snap json.RawMessage
	if mode == storage.AIReviewModeAuto && action == storage.AIActionRecommendAccept && out.Confidence >= autoThresholdAlbum {
		if !webSearched || quoteGroundedInPages(out.Quote, pages) {
			outcome, workIDs, sRaw, confirmErr := s.store.ConfirmAlbumSubjectCandidateByAI(ctx, albumID, selectedCand, selectedWorks)
			if confirmErr != nil {
				s.logger.Warn("ai album auto confirm failed", "albumId", albumID, "error", confirmErr)
			} else if outcome == "succeeded" {
				applied = true
				action = storage.AIActionAutoConfirm
				snap = sRaw
				if s.opts.OnWorkConfirmed != nil {
					for _, wid := range workIDs {
						s.opts.OnWorkConfirmed(ctx, wid)
					}
				}
			}
		}
	}

	payloadJSON, _ := json.Marshal(map[string]any{
		"candidateId":    selectedCand,
		"workSubjectIds": selectedWorks,
		"quote":          out.Quote,
	})
	_, err = s.store.SaveAIReviewDecision(ctx, storage.AIReviewDecision{
		TargetKind:          storage.AITargetAlbumSubject,
		TargetID:            albumID,
		SelectedCandidateID: selectedCand,
		Fingerprint:         info.Fingerprint,
		Action:              action,
		Payload:             payloadJSON,
		Confidence:          out.Confidence,
		Reason:              out.Reason,
		Evidence:            out.Evidence,
		WebSearched:         webSearched,
		Model:               usedModel,
		Applied:             applied,
		AppliedSnapshot:     snap,
	})
	return EvalResult{
		Kind: storage.AITargetAlbumSubject, TargetID: albumID, Action: action,
		Applied: applied, Confidence: out.Confidence, Reason: out.Reason, WebSearched: webSearched,
	}, err
}

func validateAlbumOutput(info storage.AlbumAIContext, out albumReviewOutput) (int64, []int64) {
	if out.Decision != "select" {
		return 0, nil
	}
	var cand *storage.AlbumSubjectCandidate
	for i := range info.Candidates {
		if info.Candidates[i].ID == out.CandidateID {
			cand = &info.Candidates[i]
			break
		}
	}
	if cand == nil {
		return 0, nil
	}
	allowed := make(map[int64]bool, len(cand.Tieups))
	for _, t := range cand.Tieups {
		allowed[t.SubjectID] = true
	}
	seen := map[int64]bool{}
	var works []int64
	for _, id := range out.WorkSubjectIDs {
		if allowed[id] && !seen[id] {
			seen[id] = true
			works = append(works, id)
		}
	}
	if len(works) == 0 {
		return 0, nil
	}
	return cand.ID, works
}

// ---------------------------------------------------------------- tracks

func (s *Service) evalTrack(ctx context.Context, ai storage.AISettings, rev storage.AIReviewSettings, chat tagging.Chat, model, mode string, trackID int64, force bool) (EvalResult, error) {
	info, err := s.store.LoadTrackAIContext(ctx, trackID)
	if err != nil {
		return EvalResult{}, err
	}
	if len(info.Candidates) == 0 {
		return EvalResult{Kind: storage.AITargetTrackSubject, TargetID: trackID, Skipped: true}, nil
	}
	if cached, ok := s.alreadyEvaluated(ctx, storage.AITargetTrackSubject, trackID, info.Fingerprint, force); ok {
		return cached, nil
	}

	var out trackReviewOutput
	usedModel, err := s.invokeModel(ctx, ai, chat, model, trackReviewPrompt, trackReviewSchema, info, "track_review", &out)
	if err != nil {
		return EvalResult{}, err
	}
	var pages []webPageEvidence
	webSearched := false
	if rev.WebSearchEnabled && out.Decision == "uncertain" && out.NeedWebSearch && strings.TrimSpace(out.SearchQuery) != "" {
		if fetched, webErr := s.fetchWebEvidence(ctx, ai, out.SearchQuery); webErr == nil && len(fetched) > 0 {
			pages = fetched
			webSearched = true
			payload := struct {
				storage.TrackAIContext
				WebPages []webPageEvidence `json:"webPages"`
			}{TrackAIContext: info, WebPages: pages}
			var second trackReviewOutput
			if m2, err2 := s.invokeModel(ctx, ai, chat, model, trackReviewPrompt, trackReviewSchema, payload, "track_review", &second); err2 == nil {
				out = second
				usedModel = m2
			}
		}
	}

	selectedCand, selectedWorks, roles := validateTrackOutput(info, out)
	action := decisionActionFromSelect(out.Decision)
	if out.Decision == "select" && (selectedCand == 0 || len(selectedWorks) == 0) {
		action = storage.AIActionUncertain
	}

	applied := false
	var snap json.RawMessage
	if mode == storage.AIReviewModeAuto && action == storage.AIActionRecommendAccept && out.Confidence >= autoThresholdTrack {
		if !webSearched || quoteGroundedInPages(out.Quote, pages) {
			outcome, workIDs, sRaw, confirmErr := s.store.ConfirmTrackSubjectCandidateByAI(ctx, trackID, selectedCand, selectedWorks, roles)
			if confirmErr != nil {
				s.logger.Warn("ai track auto confirm failed", "trackId", trackID, "error", confirmErr)
			} else if outcome == "succeeded" {
				applied = true
				action = storage.AIActionAutoConfirm
				snap = sRaw
				if s.opts.OnWorkConfirmed != nil {
					for _, wid := range workIDs {
						s.opts.OnWorkConfirmed(ctx, wid)
					}
				}
			}
		}
	}

	payloadJSON, _ := json.Marshal(map[string]any{
		"candidateId":    selectedCand,
		"workSubjectIds": selectedWorks,
		"roles":          roles,
		"quote":          out.Quote,
	})
	_, err = s.store.SaveAIReviewDecision(ctx, storage.AIReviewDecision{
		TargetKind:          storage.AITargetTrackSubject,
		TargetID:            trackID,
		SelectedCandidateID: selectedCand,
		Fingerprint:         info.Fingerprint,
		Action:              action,
		Payload:             payloadJSON,
		Confidence:          out.Confidence,
		Reason:              out.Reason,
		Evidence:            out.Evidence,
		WebSearched:         webSearched,
		Model:               usedModel,
		Applied:             applied,
		AppliedSnapshot:     snap,
	})
	return EvalResult{
		Kind: storage.AITargetTrackSubject, TargetID: trackID, Action: action,
		Applied: applied, Confidence: out.Confidence, Reason: out.Reason, WebSearched: webSearched,
	}, err
}

func validateTrackOutput(info storage.TrackAIContext, out trackReviewOutput) (int64, []int64, map[int64]string) {
	if out.Decision != "select" {
		return 0, nil, nil
	}
	var cand *storage.TrackSubjectCandidate
	for i := range info.Candidates {
		if info.Candidates[i].ID == out.CandidateID {
			cand = &info.Candidates[i]
			break
		}
	}
	if cand == nil {
		return 0, nil, nil
	}
	allowed := make(map[int64]string, len(cand.Tieups))
	for _, t := range cand.Tieups {
		allowed[t.SubjectID] = t.Role
	}
	var works []int64
	roles := map[int64]string{}
	seen := map[int64]bool{}
	for _, link := range out.Links {
		origRole, ok := allowed[link.WorkSubjectID]
		if !ok || seen[link.WorkSubjectID] {
			continue
		}
		seen[link.WorkSubjectID] = true
		works = append(works, link.WorkSubjectID)
		role := strings.ToLower(strings.TrimSpace(link.Role))
		switch role {
		case "op", "ed", "insert", "theme", "character", "image_song", "ost", "other":
		default:
			role = origRole
		}
		if origRole == "op" || origRole == "ed" || origRole == "insert" || origRole == "character" {
			role = origRole
		}
		roles[link.WorkSubjectID] = role
	}
	if len(works) == 0 {
		return 0, nil, nil
	}
	return cand.ID, works, roles
}

// ---------------------------------------------------------------- works

func (s *Service) evalWork(ctx context.Context, ai storage.AISettings, rev storage.AIReviewSettings, chat tagging.Chat, model, mode string, workID int64, force bool) (EvalResult, error) {
	info, err := s.store.LoadWorkAIContext(ctx, workID)
	if err != nil {
		return EvalResult{}, err
	}
	if len(info.Candidates) == 0 {
		return EvalResult{Kind: storage.AITargetWorkMatch, TargetID: workID, Skipped: true}, nil
	}
	if cached, ok := s.alreadyEvaluated(ctx, storage.AITargetWorkMatch, workID, info.Fingerprint, force); ok {
		return cached, nil
	}

	var out workReviewOutput
	usedModel, err := s.invokeModel(ctx, ai, chat, model, workReviewPrompt, workReviewSchema, info, "work_review", &out)
	if err != nil {
		return EvalResult{}, err
	}
	var pages []webPageEvidence
	webSearched := false
	if rev.WebSearchEnabled && out.Decision == "uncertain" && out.NeedWebSearch && strings.TrimSpace(out.SearchQuery) != "" {
		if fetched, webErr := s.fetchWebEvidence(ctx, ai, out.SearchQuery); webErr == nil && len(fetched) > 0 {
			pages = fetched
			webSearched = true
			payload := struct {
				storage.WorkAIContext
				WebPages []webPageEvidence `json:"webPages"`
			}{WorkAIContext: info, WebPages: pages}
			var second workReviewOutput
			if m2, err2 := s.invokeModel(ctx, ai, chat, model, workReviewPrompt, workReviewSchema, payload, "work_review", &second); err2 == nil {
				out = second
				usedModel = m2
			}
		}
	}

	var selectedCand *storage.WorkMatchCandidate
	if out.Decision == "select" {
		for i := range info.Candidates {
			if info.Candidates[i].ID == out.CandidateID {
				selectedCand = &info.Candidates[i]
				break
			}
		}
	}
	action := decisionActionFromSelect(out.Decision)
	var selectedID int64
	if selectedCand != nil {
		selectedID = selectedCand.ID
		if selectedCand.OwnerWorkID != 0 {
			action = storage.AIActionUncertain
		}
	} else if out.Decision == "select" {
		action = storage.AIActionUncertain
	}

	applied := false
	var snap json.RawMessage
	if mode == storage.AIReviewModeAuto && action == storage.AIActionRecommendAccept && out.Confidence >= autoThresholdWork && selectedCand != nil && selectedCand.OwnerWorkID == 0 {
		if !webSearched || quoteGroundedInPages(out.Quote, pages) {
			if sRaw, confirmErr := s.store.ConfirmWorkMatchCandidateByAI(ctx, workID, selectedCand.ID); confirmErr != nil {
				if !errors.Is(confirmErr, storage.ErrAutoConfirmConflict) {
					s.logger.Warn("ai work auto confirm failed", "workId", workID, "error", confirmErr)
				}
			} else {
				applied = true
				action = storage.AIActionAutoConfirm
				snap = sRaw
				if s.opts.OnWorkConfirmed != nil {
					s.opts.OnWorkConfirmed(ctx, workID)
				}
			}
		}
	}

	payloadJSON, _ := json.Marshal(map[string]any{"candidateId": selectedID, "quote": out.Quote})
	_, err = s.store.SaveAIReviewDecision(ctx, storage.AIReviewDecision{
		TargetKind:          storage.AITargetWorkMatch,
		TargetID:            workID,
		SelectedCandidateID: selectedID,
		Fingerprint:         info.Fingerprint,
		Action:              action,
		Payload:             payloadJSON,
		Confidence:          out.Confidence,
		Reason:              out.Reason,
		Evidence:            out.Evidence,
		WebSearched:         webSearched,
		Model:               usedModel,
		Applied:             applied,
		AppliedSnapshot:     snap,
	})
	return EvalResult{
		Kind: storage.AITargetWorkMatch, TargetID: workID, Action: action,
		Applied: applied, Confidence: out.Confidence, Reason: out.Reason, WebSearched: webSearched,
	}, err
}

// ---------------------------------------------------------------- artists

func (s *Service) evalArtist(ctx context.Context, ai storage.AISettings, rev storage.AIReviewSettings, chat tagging.Chat, model, mode string, artistID int64, force bool) (EvalResult, error) {
	info, err := s.store.LoadArtistAIContext(ctx, artistID)
	if err != nil {
		return EvalResult{}, err
	}
	if len(info.Candidates) == 0 {
		return EvalResult{Kind: storage.AITargetArtistMatch, TargetID: artistID, Skipped: true}, nil
	}
	if cached, ok := s.alreadyEvaluated(ctx, storage.AITargetArtistMatch, artistID, info.Fingerprint, force); ok {
		return cached, nil
	}

	var out artistReviewOutput
	usedModel, err := s.invokeModel(ctx, ai, chat, model, artistReviewPrompt, artistReviewSchema, info, "artist_review", &out)
	if err != nil {
		return EvalResult{}, err
	}
	var pages []webPageEvidence
	webSearched := false
	if rev.WebSearchEnabled && out.Decision == "uncertain" && out.NeedWebSearch && strings.TrimSpace(out.SearchQuery) != "" {
		if fetched, webErr := s.fetchWebEvidence(ctx, ai, out.SearchQuery); webErr == nil && len(fetched) > 0 {
			pages = fetched
			webSearched = true
			payload := struct {
				storage.ArtistAIContext
				WebPages []webPageEvidence `json:"webPages"`
			}{ArtistAIContext: info, WebPages: pages}
			var second artistReviewOutput
			if m2, err2 := s.invokeModel(ctx, ai, chat, model, artistReviewPrompt, artistReviewSchema, payload, "artist_review", &second); err2 == nil {
				out = second
				usedModel = m2
			}
		}
	}

	var selectedCand *storage.ArtistCandidateAIView
	if out.Decision == "select" {
		for i := range info.Candidates {
			if info.Candidates[i].ID == out.CandidateID {
				selectedCand = &info.Candidates[i]
				break
			}
		}
	}
	action := decisionActionFromSelect(out.Decision)
	var selectedID int64
	if selectedCand != nil {
		selectedID = selectedCand.ID
		if info.NeedsSplit || selectedCand.ConflictOwnerID != 0 || selectedCand.RequiresManualReview {
			action = storage.AIActionUncertain
		}
	} else if out.Decision == "select" {
		action = storage.AIActionUncertain
	}

	applied := false
	var snap json.RawMessage
	if mode == storage.AIReviewModeAuto && action == storage.AIActionRecommendAccept && out.Confidence >= autoThresholdArtist && selectedCand != nil && !info.NeedsSplit && selectedCand.ConflictOwnerID == 0 && !selectedCand.RequiresManualReview {
		if !webSearched || quoteGroundedInPages(out.Quote, pages) {
			bound, sRaw, bindErr := s.store.ConfirmArtistCandidateByAI(ctx, artistID, selectedCand.ID)
			if bindErr != nil {
				s.logger.Warn("ai artist auto confirm failed", "artistId", artistID, "error", bindErr)
			} else if bound {
				applied = true
				action = storage.AIActionAutoConfirm
				snap = sRaw
				if s.opts.OnArtistConfirmed != nil {
					s.opts.OnArtistConfirmed(ctx, artistID)
				}
			}
		}
	}

	payloadJSON, _ := json.Marshal(map[string]any{"candidateId": selectedID, "quote": out.Quote})
	_, err = s.store.SaveAIReviewDecision(ctx, storage.AIReviewDecision{
		TargetKind:          storage.AITargetArtistMatch,
		TargetID:            artistID,
		SelectedCandidateID: selectedID,
		Fingerprint:         info.Fingerprint,
		Action:              action,
		Payload:             payloadJSON,
		Confidence:          out.Confidence,
		Reason:              out.Reason,
		Evidence:            out.Evidence,
		WebSearched:         webSearched,
		Model:               usedModel,
		Applied:             applied,
		AppliedSnapshot:     snap,
	})
	return EvalResult{
		Kind: storage.AITargetArtistMatch, TargetID: artistID, Action: action,
		Applied: applied, Confidence: out.Confidence, Reason: out.Reason, WebSearched: webSearched,
	}, err
}

// ---------------------------------------------------------------- composite credit splits

func (s *Service) evalCreditSplit(ctx context.Context, ai storage.AISettings, chat tagging.Chat, model, mode string, artistID int64, force bool) (EvalResult, error) {
	info, err := s.store.LoadCreditSplitAIContext(ctx, artistID)
	if err != nil {
		return EvalResult{}, err
	}
	if !force && info.Fingerprint != "" {
		if existing, ok, lookupErr := s.store.AIReviewDecisionByTarget(ctx, storage.AITargetCreditSplit, artistID); lookupErr == nil && ok && existing.Fingerprint == info.Fingerprint && !existing.Reverted {
			if mode == storage.AIReviewModeAuto && !existing.Applied && existing.Action == storage.AIActionRecommendAccept && existing.Confidence >= autoThresholdCreditSplit {
				var cachedPayload struct {
					IsComposite bool     `json:"isComposite"`
					Parts       []string `json:"parts"`
				}
				if json.Unmarshal(existing.Payload, &cachedPayload) == nil && cachedPayload.IsComposite && len(cachedPayload.Parts) >= 2 {
					if _, snap, splitErr := s.store.ApplyCompositeCreditSplitByAI(ctx, artistID, cachedPayload.Parts); splitErr == nil {
						existing.Action = storage.AIActionAutoConfirm
						existing.Applied = true
						existing.AppliedSnapshot = snap
						if decID, saveErr := s.store.SaveAIReviewDecision(ctx, existing); saveErr == nil {
							_ = s.store.LinkCreditSplitDecision(ctx, artistID, decID)
						}
						return EvalResult{
							Kind: storage.AITargetCreditSplit, TargetID: artistID, Action: storage.AIActionAutoConfirm,
							Applied: true, Confidence: existing.Confidence, Reason: existing.Reason,
						}, nil
					} else if !errors.Is(splitErr, storage.ErrInvalidCreditSplit) && !errors.Is(splitErr, storage.ErrNoCompositeReferences) {
						s.logger.Warn("ai credit split cached upgrade failed", "artistId", artistID, "error", splitErr)
					}
				}
			}
			return EvalResult{
				Kind: storage.AITargetCreditSplit, TargetID: artistID, Action: existing.Action,
				Applied: existing.Applied, Skipped: true, Confidence: existing.Confidence,
				Reason: existing.Reason, WebSearched: existing.WebSearched,
			}, nil
		}
	}

	var out creditSplitOutput
	usedModel, err := s.invokeModel(ctx, ai, chat, model, creditSplitPrompt, creditSplitSchema, info, "credit_split", &out)
	if err != nil {
		return EvalResult{}, err
	}

	var parts []string
	seen := map[string]bool{}
	for _, p := range out.Parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		parts = append(parts, p)
	}
	action := storage.AIActionRecommendReject // not composite (legitimate single unit/band)
	if out.IsComposite {
		if len(parts) > 0 {
			action = storage.AIActionRecommendAccept
		} else {
			action = storage.AIActionUncertain
		}
	}

	applied := false
	var snap json.RawMessage
	if mode == storage.AIReviewModeAuto && out.IsComposite && len(parts) >= 2 && out.Confidence >= autoThresholdCreditSplit {
		if _, sRaw, splitErr := s.store.ApplyCompositeCreditSplitByAI(ctx, artistID, parts); splitErr != nil {
			if errors.Is(splitErr, storage.ErrInvalidCreditSplit) {
				action = storage.AIActionUncertain
			} else if !errors.Is(splitErr, storage.ErrNoCompositeReferences) {
				s.logger.Warn("ai credit split auto apply failed", "artistId", artistID, "error", splitErr)
			}
		} else {
			applied = true
			action = storage.AIActionAutoConfirm
			snap = sRaw
		}
	}

	payloadJSON, _ := json.Marshal(map[string]any{"isComposite": out.IsComposite, "parts": parts})
	decID, err := s.store.SaveAIReviewDecision(ctx, storage.AIReviewDecision{
		TargetKind:      storage.AITargetCreditSplit,
		TargetID:        artistID,
		Fingerprint:     info.Fingerprint,
		Action:          action,
		Payload:         payloadJSON,
		Confidence:      out.Confidence,
		Reason:          out.Reason,
		Model:           usedModel,
		Applied:         applied,
		AppliedSnapshot: snap,
	})
	if err == nil && applied && decID > 0 {
		_ = s.store.LinkCreditSplitDecision(ctx, artistID, decID)
	}
	return EvalResult{
		Kind: storage.AITargetCreditSplit, TargetID: artistID, Action: action,
		Applied: applied, Confidence: out.Confidence, Reason: out.Reason,
	}, err
}

// ---------------------------------------------------------------- series suggestions

func (s *Service) evalSeriesSuggestion(ctx context.Context, ai storage.AISettings, chat tagging.Chat, model, mode string, suggestionID int64, force bool) (EvalResult, error) {
	info, err := s.store.LoadSeriesSuggestionAIContext(ctx, suggestionID)
	if err != nil {
		return EvalResult{}, err
	}
	if cached, ok := s.alreadyEvaluated(ctx, storage.AITargetSeriesSuggestion, suggestionID, info.Fingerprint, force); ok {
		return cached, nil
	}

	var out seriesReviewOutput
	usedModel, err := s.invokeModel(ctx, ai, chat, model, seriesReviewPrompt, seriesReviewSchema, info, "series_review", &out)
	if err != nil {
		return EvalResult{}, err
	}

	action := storage.AIActionUncertain
	switch out.Recommendation {
	case "accept":
		action = storage.AIActionRecommendAccept
	case "reject":
		action = storage.AIActionRecommendReject
	}

	applied := false
	if mode == storage.AIReviewModeAuto && action == storage.AIActionRecommendAccept && out.Confidence >= autoThresholdSeries {
		if !(info.SeriesAManual && info.SeriesBManual) {
			if acceptErr := s.store.AcceptSeriesSuggestion(ctx, suggestionID, ""); acceptErr == nil {
				applied = true
				action = storage.AIActionAutoConfirm
			} else if !errors.Is(acceptErr, storage.ErrSeriesTitleConflict) && !errors.Is(acceptErr, storage.ErrSeriesSuggestionStale) {
				s.logger.Warn("ai series auto confirm failed", "suggestionId", suggestionID, "error", acceptErr)
			}
		}
	}

	payloadJSON, _ := json.Marshal(map[string]any{"suggestedTitle": strings.TrimSpace(out.SuggestedTitle)})
	_, err = s.store.SaveAIReviewDecision(ctx, storage.AIReviewDecision{
		TargetKind:  storage.AITargetSeriesSuggestion,
		TargetID:    suggestionID,
		Fingerprint: info.Fingerprint,
		Action:      action,
		Payload:     payloadJSON,
		Confidence:  out.Confidence,
		Reason:      out.Reason,
		Model:       usedModel,
		Applied:     applied,
	})
	return EvalResult{
		Kind: storage.AITargetSeriesSuggestion, TargetID: suggestionID, Action: action,
		Applied: applied, Confidence: out.Confidence, Reason: out.Reason,
	}, err
}
