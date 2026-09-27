package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func (m *Manager) enrichBangumiWork(ctx context.Context, runID int64, work storage.WorkEnrichmentTarget, force bool) (string, error) {
	if _, err := m.store.WorkByID(ctx, work.ID); errors.Is(err, sql.ErrNoRows) {
		return "skipped", nil
	} else if err != nil {
		return "", err
	}
	types := bangumiTypes(work.Type)
	if len(types) == 0 {
		return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil || !setting.Enabled {
		return "skipped", err
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
		raw, _ := json.Marshal(v)
		poster := v.Images.Large
		if poster == "" {
			poster = v.Images.Common
		}
		year := 0
		if len(date) >= 4 {
			year, _ = strconv.Atoi(date[:4])
		}
		candidateType := ""
		if v.Type == 2 {
			candidateType = "anime"
		} else if v.Type == 4 {
			candidateType = "game"
		}
		candidates = append(candidates, storage.WorkMatchCandidate{Source: "bangumi", ExternalID: strconv.FormatInt(v.ID, 10), Title: v.Name, TranslatedTitle: v.NameCN, Type: candidateType, Year: year, PageURL: "https://bgm.tv/subject/" + strconv.FormatInt(v.ID, 10), PosterURL: poster, Score: score, Evidence: evidence, Payload: raw})
	}
	if _, err = m.store.WorkByID(ctx, work.ID); errors.Is(err, sql.ErrNoRows) {
		return "skipped", nil
	} else if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		if err = m.store.SetWorkEnrichmentMiss(ctx, work.ID, "bangumi"); err != nil {
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
			return "review", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
		} else if err != nil {
			if _, lookupErr := m.store.WorkByID(ctx, work.ID); errors.Is(lookupErr, sql.ErrNoRows) {
				return "skipped", nil
			}
			return "", err
		}
		if posterErr := m.CacheWorkPoster(ctx, work.ID); posterErr != nil && ctx.Err() == nil {
			m.logger.Warn("cache work poster", "workId", work.ID, "error", posterErr)
		}
		return "succeeded", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	if pending > 0 {
		return "review", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
	}
	return "skipped", m.store.DeleteWorkEnrichmentRetry(ctx, work.ID, "bangumi")
}
