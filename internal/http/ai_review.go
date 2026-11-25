package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/aiclient"
	"github.com/lux032/032music-server/internal/aireview"
	"github.com/lux032/032music-server/internal/storage"
)

// SetAIReview attaches the AI review service.
func (a *App) SetAIReview(service *aireview.Service) { a.aiReview = service }

// SetSharedAILimiter attaches the shared provider rate limiter.
func (a *App) SetSharedAILimiter(limiter *aiclient.Limiter) { a.sharedAILimiter = limiter }

type aiDecisionView struct {
	ID                  int64
	Action              string
	ActionLabel         string
	BadgeTone           string // "accept" | "reject" | "uncertain" | "auto"
	Confidence          int
	Reason              string
	Evidence            []string
	WebSearched         bool
	Model               string
	SelectedCandidateID int64
	SelectedWorks       map[int64]bool
	SelectedRoles       map[int64]string
	SuggestedTitle      string
	CreditIsComposite   bool
	CreditParts         []string
	CreditPartsCSV      string
}

func buildAIDecisionView(d storage.AIReviewDecision) *aiDecisionView {
	v := &aiDecisionView{
		ID:                  d.ID,
		Action:              d.Action,
		Confidence:          d.Confidence,
		Reason:              d.Reason,
		Evidence:            d.Evidence,
		WebSearched:         d.WebSearched,
		Model:               d.Model,
		SelectedCandidateID: d.SelectedCandidateID,
		SelectedWorks:       map[int64]bool{},
		SelectedRoles:       map[int64]string{},
	}
	switch d.Action {
	case storage.AIActionAutoConfirm:
		v.ActionLabel = "AI 已自动应用"
		v.BadgeTone = "auto"
	case storage.AIActionRecommendAccept:
		v.ActionLabel = "AI 推荐接受"
		v.BadgeTone = "accept"
	case storage.AIActionRecommendReject:
		v.ActionLabel = "AI 建议拒绝/排除"
		v.BadgeTone = "reject"
	default:
		v.ActionLabel = "AI 不确定"
		v.BadgeTone = "uncertain"
	}

	var payload struct {
		WorkSubjectIDs []int64          `json:"workSubjectIds"`
		Roles          map[int64]string `json:"roles"`
		SuggestedTitle string           `json:"suggestedTitle"`
		IsComposite    bool             `json:"isComposite"`
		Parts          []string         `json:"parts"`
	}
	if len(d.Payload) > 0 && json.Unmarshal(d.Payload, &payload) == nil {
		for _, id := range payload.WorkSubjectIDs {
			v.SelectedWorks[id] = true
		}
		if payload.Roles != nil {
			v.SelectedRoles = payload.Roles
		}
		v.SuggestedTitle = payload.SuggestedTitle
		v.CreditIsComposite = payload.IsComposite
		v.CreditParts = payload.Parts
		v.CreditPartsCSV = strings.Join(payload.Parts, " / ")
	}
	return v
}

func aiTargetKindLabel(kind string) string {
	switch kind {
	case storage.AITargetAlbumSubject:
		return "专辑关联"
	case storage.AITargetTrackSubject:
		return "曲目关联"
	case storage.AITargetWorkMatch:
		return "作品对齐"
	case storage.AITargetArtistMatch:
		return "艺术家匹配"
	case storage.AITargetCreditSplit:
		return "复合署名拆分"
	case storage.AITargetSeriesSuggestion:
		return "系列建议"
	default:
		return kind
	}
}

func aiReviewScopeLabel(scope string) string {
	switch scope {
	case "works_all":
		return "全部作品关联待审"
	case "albums":
		return "专辑候选"
	case "tracks":
		return "曲目候选"
	case "works":
		return "作品对齐候选"
	case "series":
		return "系列建议"
	case "artists_all":
		return "艺术家待审与复合署名"
	case "artist_matches":
		return "艺术家身份候选"
	case "credit_splits":
		return "复合署名拆分"
	default:
		return scope
	}
}

func (a *App) aiReviewMeta(ctx context.Context, scopes []string) (configured, enabled bool, active *storage.AIReviewRun, latest *storage.AIReviewRun) {
	if ai, err := a.store.AISettings(ctx); err == nil {
		configured = ai.Configured()
	}
	if rev, err := a.store.AIReviewSettings(ctx); err == nil {
		enabled = rev.Enabled
	}
	if r, ok, err := a.store.ActiveAIReviewRun(ctx, scopes); err == nil && ok {
		copy := r
		active = &copy
	}
	if active == nil {
		if r, ok, err := a.store.LatestAIReviewRun(ctx, scopes); err == nil && ok {
			copy := r
			latest = &copy
		}
	}
	return configured, enabled, active, latest
}

func (a *App) handleStartAIReviewRun(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	scope := strings.TrimSpace(r.FormValue("scope"))
	defaultReturn := "/admin/work-review"
	if scope == "artists_all" || scope == "artist_matches" || scope == "credit_splits" {
		defaultReturn = "/admin/matches"
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), defaultReturn)
	if a.aiReview == nil {
		redirectWithNotice(w, r, returnTo, "AI 审核服务未初始化")
		return
	}
	force := r.FormValue("force") != ""
	run, err := a.aiReview.StartRun(r.Context(), scope, force)
	if err != nil {
		redirectWithNotice(w, r, returnTo, err.Error())
		return
	}
	redirectWithNotice(w, r, returnTo, "已启动 AI 存量预审任务 #"+strconv.FormatInt(run.ID, 10)+"（共 "+strconv.Itoa(run.Total)+" 项）")
}

func (a *App) handlePauseAIReviewRun(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review")
	id := parseInt64(r.PathValue("id"))
	if a.aiReview == nil || id <= 0 {
		redirectWithNotice(w, r, returnTo, "无效的任务")
		return
	}
	if err := a.aiReview.PauseRun(r.Context(), id); err != nil {
		redirectWithNotice(w, r, returnTo, err.Error())
		return
	}
	redirectWithNotice(w, r, returnTo, "已暂停 AI 预审任务")
}

func (a *App) handleResumeAIReviewRun(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review")
	id := parseInt64(r.PathValue("id"))
	if a.aiReview == nil || id <= 0 {
		redirectWithNotice(w, r, returnTo, "无效的任务")
		return
	}
	if err := a.aiReview.ResumeRun(r.Context(), id); err != nil {
		redirectWithNotice(w, r, returnTo, err.Error())
		return
	}
	redirectWithNotice(w, r, returnTo, "已继续 AI 预审任务")
}

func (a *App) handleCancelAIReviewRun(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review")
	id := parseInt64(r.PathValue("id"))
	if a.aiReview == nil || id <= 0 {
		redirectWithNotice(w, r, returnTo, "无效的任务")
		return
	}
	if err := a.aiReview.CancelRun(r.Context(), id); err != nil {
		redirectWithNotice(w, r, returnTo, err.Error())
		return
	}
	redirectWithNotice(w, r, returnTo, "已停止 AI 预审任务")
}

func (a *App) handleActiveAIReviewRun(w http.ResponseWriter, r *http.Request) {
	group := r.URL.Query().Get("group")
	scopes := []string{"works_all", "albums", "tracks", "works", "series"}
	if group == "artists" {
		scopes = []string{"artists_all", "artist_matches", "credit_splits"}
	}
	run, ok, err := a.store.ActiveAIReviewRun(r.Context(), scopes)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"run": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (a *App) handleRevertAIReviewDecision(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review")
	id := parseInt64(r.PathValue("id"))
	if err := a.store.RevertAIReviewDecision(r.Context(), id); err != nil {
		redirectWithNotice(w, r, returnTo, "撤销失败："+err.Error())
		return
	}
	redirectWithNotice(w, r, returnTo, "已撤销 AI 自动处理，候选已退回待审队列")
}

func parseSplitPartsText(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	replacer := strings.NewReplacer("\r\n", "\n", " / ", "\n", "/", "\n", "、", "\n", "，", "\n", ";", "\n", "；", "\n")
	raw = replacer.Replace(raw)
	var parts []string
	for _, line := range strings.Split(raw, "\n") {
		p := strings.TrimSpace(line)
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func (a *App) handleApplyCreditSplit(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	artistID := parseInt64(r.PathValue("id"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches?tab=credits")
	if artistID <= 0 {
		redirectWithNotice(w, r, returnTo, "无效的艺术家 ID")
		return
	}
	var parts []string
	if err := r.ParseForm(); err == nil {
		if raw := strings.TrimSpace(r.FormValue("parts")); raw != "" {
			parts = parseSplitPartsText(raw)
		}
		if len(parts) == 0 {
			for _, p := range r.Form["part"] {
				p = strings.TrimSpace(p)
				if p != "" {
					parts = append(parts, p)
				}
			}
		}
	}
	if len(parts) == 0 {
		redirectWithNotice(w, r, returnTo, "没有有效的拆分艺人名单")
		return
	}
	res, err := a.store.ApplyCompositeCreditSplit(r.Context(), artistID, parts)
	if errors.Is(err, storage.ErrNoCompositeReferences) {
		redirectWithNotice(w, r, returnTo, "该复合署名当前没有可更新的曲目或专辑关联，已清理残留待审状态")
		return
	}
	if err != nil {
		redirectWithNotice(w, r, returnTo, "应用拆分失败："+err.Error())
		return
	}
	msg := "已将复合署名拆分为 " + strings.Join(res.PartNames, "、") + "（更新 " + strconv.Itoa(res.TracksUpdated) + " 首曲目"
	if res.AlbumsUpdated > 0 {
		msg += "、" + strconv.Itoa(res.AlbumsUpdated) + " 张专辑"
	}
	msg += "）"
	redirectWithNotice(w, r, returnTo, msg)
}

func (a *App) handleBatchApplyHighConfidenceCreditSplits(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches?tab=credits")
	const minConfidence = 92
	candidates, err := a.store.HighConfidencePendingCreditSplits(r.Context(), minConfidence)
	if err != nil {
		redirectWithNotice(w, r, returnTo, "加载高置信复合署名建议失败："+err.Error())
		return
	}
	if len(candidates) == 0 {
		redirectWithNotice(w, r, returnTo, "当前没有待应用的高置信（≥92%）复合署名建议")
		return
	}
	applied, skipped, tracksUpdated, albumsUpdated := 0, 0, 0, 0
	for _, c := range candidates {
		res, snap, splitErr := a.store.ApplyCompositeCreditSplitByAI(r.Context(), c.ArtistID, c.Parts)
		if splitErr != nil {
			skipped++
			continue
		}
		if dec, ok, dErr := a.store.AIReviewDecisionByTarget(r.Context(), storage.AITargetCreditSplit, c.ArtistID); dErr == nil && ok {
			dec.Action = storage.AIActionAutoConfirm
			dec.Applied = true
			dec.AppliedSnapshot = snap
			if decID, saveErr := a.store.SaveAIReviewDecision(r.Context(), dec); saveErr == nil && decID > 0 {
				_ = a.store.LinkCreditSplitDecision(r.Context(), c.ArtistID, decID)
			}
		}
		applied++
		tracksUpdated += res.TracksUpdated
		albumsUpdated += res.AlbumsUpdated
	}
	msg := "已批量拆分 " + strconv.Itoa(applied) + " 位高置信复合署名（更新 " + strconv.Itoa(tracksUpdated) + " 首曲目"
	if albumsUpdated > 0 {
		msg += "、" + strconv.Itoa(albumsUpdated) + " 张专辑"
	}
	msg += "）"
	if skipped > 0 {
		msg += "，跳过 " + strconv.Itoa(skipped) + " 位"
	}
	redirectWithNotice(w, r, returnTo, msg)
}

func (a *App) handleExemptCompositeArtist(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	artistID := parseInt64(r.PathValue("id"))
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), "/admin/matches?tab=credits")
	if artistID <= 0 {
		redirectWithNotice(w, r, returnTo, "无效的艺术家 ID")
		return
	}
	if err := a.store.ExemptCompositeArtist(r.Context(), artistID, "人工确认为单一艺人"); err != nil {
		redirectWithNotice(w, r, returnTo, "确认单一艺人失败："+err.Error())
		return
	}
	redirectWithNotice(w, r, returnTo, "已确认为单一艺人，已移出复合署名并放行身份匹配扫描")
}
