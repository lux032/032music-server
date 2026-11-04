package httpapi

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

// 作品关联审核页的批量审核（Issue #3）：在专辑候选、曲目候选与系列建议
// Tab 勾选多张卡片后一次接受或拒绝。每一条仍走与单卡按钮完全相同的存储
// 路径（各自一个事务），所以拒绝/抑制的永久记忆、冲突检查与海报排队都
// 不变；一条失败不影响其余各条，结果汇总成一条提示。

// maxWorkReviewBatch 与审核页单 Tab 最多显示的条数（200）一致。
const maxWorkReviewBatch = 200

// workReviewBatchItem 是一条批量审核目标，表单值形如
// "album:<albumId>:<candidateId>"、"track:<trackId>:<candidateId>" 或
// "series:<suggestionId>"。
type workReviewBatchItem struct {
	Kind        string
	OwnerID     int64
	CandidateID int64
	Key         string
}

func parseWorkReviewBatchItem(raw string) (workReviewBatchItem, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	ids := make([]int64, 0, 2)
	for _, part := range parts[1:] {
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return workReviewBatchItem{}, false
		}
		ids = append(ids, id)
	}
	item := workReviewBatchItem{Kind: parts[0], Key: strings.TrimSpace(raw)}
	switch {
	case (item.Kind == "album" || item.Kind == "track") && len(ids) == 2:
		item.OwnerID, item.CandidateID = ids[0], ids[1]
	case item.Kind == "series" && len(ids) == 1:
		item.CandidateID = ids[0]
	default:
		return workReviewBatchItem{}, false
	}
	return item, true
}

// workReviewBatchTab 是一类目标所在的 Tab，用于缺省返回地址。
func workReviewBatchTab(kind string) string {
	switch kind {
	case "track":
		return "tracks"
	case "series":
		return "series"
	default:
		return "albums"
	}
}

// decideWorkReviewBatchItem 执行一条决定。state 是该卡片接受表单的当前
// 取值（勾选的作品、改过的角色、合并命名），由页面脚本按卡片序列化；缺失
// 时按卡片默认值处理：专辑接受全部、系列按规则命名。曲目候选必须带勾选。
func (a *App) decideWorkReviewBatchItem(r *http.Request, item workReviewBatchItem, accept bool, state url.Values) error {
	ctx := r.Context()
	switch item.Kind {
	case "album":
		var selected []int64
		if accept && state != nil && state.Get("acceptAll") != "1" {
			for _, raw := range state["workSubjectIds"] {
				if id := parseInt64(raw); id > 0 {
					selected = append(selected, id)
				}
			}
			if len(selected) == 0 {
				return errors.New("请至少选择一部作品")
			}
		}
		return a.decideAlbumSubject(ctx, item.OwnerID, item.CandidateID, accept, selected)
	case "track":
		var selected []int64
		var roles map[int64]string
		if accept && state == nil {
			// 没有卡片状态（页面脚本未运行）：按卡片默认值，即全部作品、Bangumi 角色。
			cands, err := a.store.TrackSubjectCandidates(ctx, item.OwnerID)
			if err != nil {
				return err
			}
			for _, cand := range cands {
				if cand.ID != item.CandidateID {
					continue
				}
				for _, tie := range cand.Tieups {
					selected = append(selected, tie.SubjectID)
				}
			}
			if len(selected) == 0 {
				return errors.New("请至少选择一部作品")
			}
		} else if accept {
			roles = map[int64]string{}
			for _, raw := range state["workSubjectIds"] {
				if id := parseInt64(raw); id > 0 {
					selected = append(selected, id)
				}
			}
			if len(selected) == 0 {
				return errors.New("请至少选择一部作品")
			}
			for key, values := range state {
				if !strings.HasPrefix(key, "role-") || len(values) == 0 {
					continue
				}
				if id := parseInt64(strings.TrimPrefix(key, "role-")); id > 0 {
					roles[id] = values[0]
				}
			}
		}
		return a.decideTrackSubject(ctx, item.OwnerID, item.CandidateID, accept, selected, roles)
	case "series":
		if !accept {
			return a.store.RejectSeriesSuggestion(ctx, item.CandidateID)
		}
		mergeTitle, formErr := mergeTitleFromValues(state)
		if formErr != "" {
			return errors.New(formErr)
		}
		return a.store.AcceptSeriesSuggestion(ctx, item.CandidateID, mergeTitle)
	}
	return fmt.Errorf("unknown review item %q", item.Key)
}

// workReviewBatchErrorMessage 把一条失败翻成与单卡按钮一致的提示。
func workReviewBatchErrorMessage(item workReviewBatchItem, err error) string {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "候选已不存在"
	case errors.Is(err, storage.ErrCandidateNotPending):
		return "候选已处理过"
	case item.Kind == "series" && errors.Is(err, storage.ErrSeriesTitleConflict):
		return "两个系列都改过名，请选择合并后的名字"
	case item.Kind == "series" && errors.Is(err, storage.ErrSeriesSuggestionStale):
		return "建议已失效，已移除"
	case item.Kind == "track" && errors.Is(err, errTrackCandidateConflict):
		return "曲目候选无法写入：该条目可能已被拒绝、抑制，或存在作品身份冲突"
	}
	return err.Error()
}

// handleAdminWorkReviewBatch 处理批量接受/拒绝（管理员 + CSRF + returnTo）。
func (a *App) handleAdminWorkReviewBatch(w http.ResponseWriter, r *http.Request) {
	if !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	accept := r.FormValue("decision") == "accept"
	if !accept && r.FormValue("decision") != "reject" {
		redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review"), "未知的批量操作")
		return
	}
	var items []workReviewBatchItem
	seen := map[string]bool{}
	for _, raw := range r.Form["item"] {
		item, ok := parseWorkReviewBatchItem(raw)
		if !ok {
			redirectWithNotice(w, r, safeAdminReturnTo(r.FormValue("returnTo"), "/admin/work-review"), "批量审核参数无效")
			return
		}
		if !seen[item.Key] {
			seen[item.Key] = true
			items = append(items, item)
		}
	}
	fallback := "/admin/work-review"
	if len(items) > 0 {
		fallback += "?tab=" + workReviewBatchTab(items[0].Kind)
	}
	returnTo := safeAdminReturnTo(r.FormValue("returnTo"), fallback)
	if len(items) == 0 {
		redirectWithNotice(w, r, returnTo, "请先勾选要审核的候选")
		return
	}
	if len(items) > maxWorkReviewBatch {
		redirectWithNotice(w, r, returnTo, fmt.Sprintf("一次最多审核 %d 条", maxWorkReviewBatch))
		return
	}

	done, failed := 0, 0
	firstFailure := ""
	for _, item := range items {
		var state url.Values
		if raw, ok := r.Form["state-"+item.Key]; ok && len(raw) > 0 {
			parsed, err := url.ParseQuery(raw[0])
			if err != nil {
				failed++
				if firstFailure == "" {
					firstFailure = "卡片选择无法读取"
				}
				continue
			}
			state = parsed
		}
		if err := a.decideWorkReviewBatchItem(r, item, accept, state); err != nil {
			failed++
			if firstFailure == "" {
				firstFailure = workReviewBatchErrorMessage(item, err)
			}
			if !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, storage.ErrCandidateNotPending) {
				a.logger.Warn("work review batch item", "item", item.Key, "accept", accept, "error", err)
			}
			continue
		}
		done++
	}

	verb := "拒绝"
	if accept {
		verb = "接受"
	}
	message := fmt.Sprintf("已%s %d 条", verb, done)
	if failed > 0 {
		message += fmt.Sprintf("，%d 条未处理（%s）", failed, firstFailure)
	}
	redirectWithNotice(w, r, returnTo, message)
}
