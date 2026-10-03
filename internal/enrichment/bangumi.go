package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
	"golang.org/x/text/unicode/norm"
)

type bangumiSearchResponse struct {
	Data []struct {
		ID       int64   `json:"id"`
		Type     int     `json:"type"`
		Name     string  `json:"name"`
		NameCN   string  `json:"name_cn"`
		Date     *string `json:"date"`
		Platform string  `json:"platform"`
		Images   struct {
			Large  string `json:"large"`
			Common string `json:"common"`
		} `json:"images"`
	} `json:"data"`
}

func strictTitleKey(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	s = strings.NewReplacer("〜", "~", "‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-", "−", "-").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func looseTitleKey(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(" ・~:;/-「」『』【】()[]\"'‘’“”", r) {
			return -1
		}
		return r
	}, strictTitleKey(s))
}

func scoreBangumi(work storage.WorkEnrichmentTarget, name, translated, date string, subjectType int) (int, []string) {
	score := 0
	var evidence []string
	title, original := strictTitleKey(work.Title), strictTitleKey(name)
	loose, remoteLoose := looseTitleKey(work.Title), looseTitleKey(name)
	switch {
	case title != "" && title == original:
		score = 70
		evidence = append(evidence, "日文原名精确匹配")
	case loose != "" && loose == remoteLoose:
		score = 50
		evidence = append(evidence, "日文原名宽松匹配")
	case loose != "" && remoteLoose != "" && (strings.Contains(loose, remoteLoose) || strings.Contains(remoteLoose, loose)):
		score = 30
		evidence = append(evidence, "日文原名部分匹配")
	}
	if title != "" && title == strictTitleKey(translated) && score < 40 {
		score = 40
		evidence = append(evidence, "中文标题精确匹配")
	}
	if work.TranslatedTitle != "" && strictTitleKey(work.TranslatedTitle) == strictTitleKey(translated) {
		score += 20
		evidence = append(evidence, "译名精确匹配")
	}
	if work.Type == "anime" && subjectType == 2 || work.Type == "movie" && subjectType == 2 || work.Type == "game" && subjectType == 4 || work.Type == "other" && (subjectType == 2 || subjectType == 4) {
		score += 10
		evidence = append(evidence, "类型一致")
	}
	if work.Year > 0 && len(date) >= 4 {
		if year, err := strconv.Atoi(date[:4]); err == nil {
			if year == work.Year {
				score += 10
				evidence = append(evidence, "年份一致")
			} else {
				score -= 20
				evidence = append(evidence, "年份冲突")
			}
		}
	}
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return score, evidence
}

func bangumiWorkType(subjectType int, platform string) string {
	switch subjectType {
	case 4:
		return "game"
	case 2:
		if strings.Contains(platform, "剧场版") || strings.Contains(platform, "劇場版") {
			return "movie"
		}
		return "anime"
	default:
		return ""
	}
}

// BangumiWorkCandidateByID loads one subject through the shared Bangumi
// cache/throttle path and converts it to a user-selected work candidate.
func (m *Manager) BangumiWorkCandidateByID(ctx context.Context, subjectID int64) (storage.WorkMatchCandidate, error) {
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		return storage.WorkMatchCandidate{}, err
	}
	base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
	if base == "" {
		base = "https://api.bgm.tv"
	}
	key := strconv.FormatInt(subjectID, 10)
	var subject struct {
		ID       int64  `json:"id"`
		Type     int    `json:"type"`
		Name     string `json:"name"`
		NameCN   string `json:"name_cn"`
		Date     string `json:"date"`
		Platform string `json:"platform"`
		Images   struct {
			Large  string `json:"large"`
			Common string `json:"common"`
		} `json:"images"`
	}
	_, err = m.cachedJSON(ctx, "bangumi", "subject:"+key, base+"/v0/subjects/"+key, setting, false, nil, &subject)
	if err != nil {
		return storage.WorkMatchCandidate{}, err
	}
	typ := bangumiWorkType(subject.Type, subject.Platform)
	if typ == "" {
		return storage.WorkMatchCandidate{}, fmt.Errorf("只支持动画或游戏条目")
	}
	year := 0
	if len(subject.Date) >= 4 {
		year, _ = strconv.Atoi(subject.Date[:4])
	}
	poster := subject.Images.Large
	if poster == "" {
		poster = subject.Images.Common
	}
	raw, _ := json.Marshal(subject)
	return storage.WorkMatchCandidate{Source: "bangumi", ExternalID: key, Title: subject.Name, TranslatedTitle: subject.NameCN, Type: typ, Year: year, PageURL: "https://bgm.tv/subject/" + key, PosterURL: poster, Score: 100, Evidence: []string{"管理员手动指定 Bangumi 条目"}, Payload: raw}, nil
}

func bangumiTypes(workType string) []int {
	switch workType {
	case "anime", "movie":
		return []int{2}
	case "game":
		return []int{4}
	case "other":
		return []int{2, 4}
	default:
		return nil
	}
}

// workGone reports whether the work was deleted while the enrichment request
// was in flight (D-4). A removed work must read as "skipped": it must not be
// counted as a failure (which would also feed the consecutive-failure
// breaker) and must not surface as a pending review.
func (m *Manager) workGone(ctx context.Context, workID int64) bool {
	_, err := m.store.WorkByID(ctx, workID)
	return errors.Is(err, sql.ErrNoRows)
}

func (m *Manager) enrichBangumiWork(ctx context.Context, runID int64, work storage.WorkEnrichmentTarget, force bool) (string, error) {
	if _, err := m.store.WorkByID(ctx, work.ID); errors.Is(err, sql.ErrNoRows) {
		return "skipped", nil
	} else if err != nil {
		return "", err
	}
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil || !setting.Enabled {
		return "skipped", err
	}
	if work.BangumiExternalID != "" {
		var raw struct {
			Type     int    `json:"type"`
			Platform string `json:"platform"`
		}
		_ = json.Unmarshal(work.BangumiRaw, &raw)
		corrected := bangumiWorkType(raw.Type, raw.Platform)
		if raw.Type == 0 || raw.Type == 2 && raw.Platform == "" {
			base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
			if base == "" {
				base = "https://api.bgm.tv"
			}
			var subject map[string]any
			_, fetchErr := m.cachedJSON(ctx, "bangumi", "subject:"+work.BangumiExternalID, base+"/v0/subjects/"+work.BangumiExternalID, setting, force, nil, &subject)
			if errors.Is(fetchErr, sql.ErrNoRows) {
				if evidenceErr := m.store.RecordBangumiTypeCorrectionEvidence(ctx, work.ID, work.BangumiExternalID, "Bangumi subject detail not found", runID); evidenceErr != nil {
					return "", evidenceErr
				}
				return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
			}
			if fetchErr != nil {
				return "", fetchErr
			}
			detailRaw, marshalErr := json.Marshal(subject)
			if marshalErr != nil {
				return "", marshalErr
			}
			if updateErr := m.store.UpdateBangumiWorkProfileRaw(ctx, work.ID, detailRaw); updateErr != nil {
				return "", updateErr
			}
			var detail struct {
				Type     int    `json:"type"`
				Platform string `json:"platform"`
			}
			if unmarshalErr := json.Unmarshal(detailRaw, &detail); unmarshalErr != nil {
				return "", unmarshalErr
			}
			corrected = bangumiWorkType(detail.Type, detail.Platform)
		}
		if corrected == "" {
			return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
		}
		if work.Type == corrected {
			return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
		}
		changed, correctionErr := m.store.CorrectBangumiWorkType(ctx, work.ID, work.Type, corrected, work.BangumiExternalID, runID)
		if correctionErr != nil {
			return "", correctionErr
		}
		if changed {
			return "succeeded", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
		}
		return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	types := bangumiTypes(work.Type)
	if len(types) == 0 {
		return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	confirmed, err := m.store.WorkBangumiConfirmed(ctx, work.ID)
	if err != nil {
		return "", err
	}
	if confirmed {
		return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	endpoint, err := url.Parse(m.phaseEndpoints.Bangumi)
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	query.Set("limit", "10")
	endpoint.RawQuery = query.Encode()
	var typeKeys []string
	for _, v := range types {
		typeKeys = append(typeKeys, strconv.Itoa(v))
	}
	body := map[string]any{"keyword": work.Title, "filter": map[string]any{"type": types}}
	var response bangumiSearchResponse
	_, err = m.cachedJSON(ctx, "bangumi", "search:v2:"+strings.Join(typeKeys, ",")+":"+work.Title, endpoint.String(), setting, force, body, &response)
	if errors.Is(err, sql.ErrNoRows) {
		if err = m.store.SetWorkEnrichmentMiss(ctx, work.ID, "bangumi"); err != nil {
			// D-4 window 1: the work was deleted while the search request was
			// in flight, so the miss row fails its foreign key. That is a
			// skip, not a provider failure.
			if m.workGone(ctx, work.ID) {
				return "skipped", nil
			}
			return "", err
		}
		return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	if err != nil {
		return "", err
	}
	var candidates []storage.WorkMatchCandidate
	for _, v := range response.Data {
		date := ""
		if v.Date != nil {
			date = *v.Date
		}
		score, evidence := scoreBangumi(work, v.Name, v.NameCN, date, v.Type)
		// Type/year alone are not identity evidence. Search hits without any
		// title match must not become an administrative review task.
		titleTarget := work
		titleTarget.Type, titleTarget.Year = "", 0
		titleScore, _ := scoreBangumi(titleTarget, v.Name, v.NameCN, "", v.Type)
		if titleScore == 0 {
			continue
		}
		raw, _ := json.Marshal(v)
		poster := v.Images.Large
		if poster == "" {
			poster = v.Images.Common
		}
		year := 0
		if len(date) >= 4 {
			year, _ = strconv.Atoi(date[:4])
		}
		candidateType := bangumiWorkType(v.Type, v.Platform)
		candidates = append(candidates, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: strconv.FormatInt(v.ID, 10), Title: v.Name, TranslatedTitle: v.NameCN, Type: candidateType, Year: year, PageURL: "https://bgm.tv/subject/" + strconv.FormatInt(v.ID, 10), PosterURL: poster, Score: score, Evidence: evidence, Payload: raw})
	}
	if _, err = m.store.WorkByID(ctx, work.ID); errors.Is(err, sql.ErrNoRows) {
		return "skipped", nil
	} else if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		// Clear obsolete pending search hits, but preserve prior decisions.
		if err = m.store.ReplaceWorkMatchCandidates(ctx, work.ID, nil); err != nil {
			if m.workGone(ctx, work.ID) {
				return "skipped", nil
			}
			return "", err
		}
		if m.testWorkWriteHook != nil {
			m.testWorkWriteHook()
		}
		if err = m.store.SetWorkEnrichmentMiss(ctx, work.ID, "bangumi"); err != nil {
			// D-4 window 2: the work was deleted between the existence
			// recheck above and this write.
			if m.workGone(ctx, work.ID) {
				return "skipped", nil
			}
			return "", err
		}
		return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	if err = m.store.DeleteWorkEnrichmentMiss(ctx, work.ID, "bangumi"); err != nil {
		return "", err
	}
	if err = m.store.ReplaceWorkMatchCandidates(ctx, work.ID, candidates); err != nil {
		if _, lookupErr := m.store.WorkByID(ctx, work.ID); errors.Is(lookupErr, sql.ErrNoRows) {
			return "skipped", nil
		}
		return "", err
	}
	stored, err := m.store.WorkMatchCandidates(ctx, work.ID)
	if err != nil {
		return "", err
	}
	pending := 0
	matches := 0
	var matchID int64
	allowed := map[int]bool{}
	for _, v := range types {
		allowed[v] = true
	}
	for _, v := range stored {
		if v.Source != "bangumi" || v.Status != "candidate" {
			continue
		}
		pending++
		var payload struct {
			Type int `json:"type"`
		}
		_ = json.Unmarshal(v.Payload, &payload)
		if allowed[payload.Type] && strictTitleKey(work.Title) != "" && strictTitleKey(work.Title) == strictTitleKey(v.Title) {
			matches++
			if work.Year == 0 || v.Year == 0 || work.Year == v.Year {
				matchID = v.ID
			}
		}
	}
	if matches == 1 && matchID != 0 {
		if err = m.store.AutoConfirmWorkMatchCandidate(ctx, work.ID, matchID, runID); errors.Is(err, storage.ErrAutoConfirmConflict) {
			// D-4: a conflict read as review only while the work still exists.
			if m.workGone(ctx, work.ID) {
				return "skipped", nil
			}
			return "review", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
		} else if err != nil {
			if _, lookupErr := m.store.WorkByID(ctx, work.ID); errors.Is(lookupErr, sql.ErrNoRows) {
				return "skipped", nil
			}
			return "", err
		}
		if posterErr := m.CacheWorkPoster(ctx, work.ID); posterErr != nil && ctx.Err() == nil {
			// 确认结果已经写入。持久化 run 内：海报主机限流只记 warning，
			// 绝不能把已确认的 work item 拖进限流等待预算；海报由后续回填
			// 任务补上（与回填遇 429 停止本轮的处理方式一致）。非持久化路径
			// （手动刷新/旧循环）保持立即上报限流的原语义。
			if _, durable := storage.EnrichmentCheckpointFromContext(ctx); durable {
				m.logger.Warn("cache work poster deferred to backfill", "workId", work.ID, "error", posterErr)
			} else if asRateLimited(posterErr) != nil {
				return "", posterErr
			} else {
				m.logger.Warn("cache work poster", "workId", work.ID, "error", posterErr)
			}
		}
		return "succeeded", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	if pending > 0 {
		// D-4 window 2 (review half): the work may have been cleaned after the
		// candidates were written; their cascade delete leaves nothing to
		// review, so the outcome is a skip.
		if m.testWorkWriteHook != nil {
			m.testWorkWriteHook()
		}
		if m.workGone(ctx, work.ID) {
			return "skipped", nil
		}
		return "review", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
}
