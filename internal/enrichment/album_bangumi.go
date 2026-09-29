package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/storage"
	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"
)

type musicSearchResponse struct {
	Data []musicSubject `json:"data"`
}
type musicSubject struct {
	ID       int64  `json:"id"`
	Type     int    `json:"type"`
	Name     string `json:"name"`
	NameCN   string `json:"name_cn"`
	Date     string `json:"date"`
	Platform string `json:"platform"`
	Tags     []struct {
		Name string `json:"name"`
	} `json:"tags"`
	MetaTags []string `json:"meta_tags"`
	Infobox  []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	} `json:"infobox"`
	Images struct {
		Large  string `json:"large"`
		Common string `json:"common"`
	} `json:"images"`
}
type musicPerson struct {
	Name     string `json:"name"`
	Relation string `json:"relation"`
}
type musicRelation struct {
	ID       int64  `json:"id"`
	Type     int    `json:"type"`
	Name     string `json:"name"`
	NameCN   string `json:"name_cn"`
	Date     string `json:"date"`
	Platform string `json:"platform"`
	Relation string `json:"relation"`
	Images   struct {
		Large  string `json:"large"`
		Common string `json:"common"`
	} `json:"images"`
}

var versionSuffix = regexp.MustCompile(`(?i)\s*(\([^()]*\)|\[[^\[\]]*\]|（[^（）]*）|【[^【】]*】|〈[^〈〉]*〉|［[^［］]*］)$`)
var versionKeywords = regexp.MustCompile(`(?i)盤|限定|edition|付|cd|dvd|blu-ray|bd|single|ep|ver`)

func stripMusicVersion(s string) string {
	s = strings.TrimSpace(s)
	for {
		if strings.HasSuffix(strings.ToLower(s), " - single") {
			s = strings.TrimSpace(s[:len(s)-9])
			continue
		}
		if strings.HasSuffix(strings.ToLower(s), " - ep") {
			s = strings.TrimSpace(s[:len(s)-5])
			continue
		}
		loc := versionSuffix.FindStringIndex(s)
		if loc == nil {
			break
		}
		fragment := s[loc[0]:]
		if !versionKeywords.MatchString(fragment) {
			break
		}
		s = strings.TrimSpace(s[:loc[0]])
	}
	return s
}
func musicTitleScore(local, remote string) (int, []string) {
	if strictTitleKey(local) != "" && strictTitleKey(local) == strictTitleKey(remote) {
		return 40, []string{"标题精确一致"}
	}
	if looseTitleKey(local) != "" && looseTitleKey(local) == looseTitleKey(remote) {
		return 30, []string{"标题宽松一致"}
	}
	l, r := stripMusicVersion(local), stripMusicVersion(remote)
	if (l != local || r != remote) && (strictTitleKey(l) == strictTitleKey(r) || looseTitleKey(l) == looseTitleKey(r)) && strictTitleKey(l) != "" {
		return 30, []string{"去版本后缀后标题一致"}
	}
	return 0, nil
}

var artistSeparators = regexp.MustCompile(`(?i)(?:feat\.|ft\.|cv\.|cv:|vo\.)`)

func personTokens(raw string) map[string]bool {
	raw = artistSeparators.ReplaceAllString(norm.NFKC.String(strings.ToLower(raw)), "/")
	tokens := map[string]bool{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return strings.ContainsRune("()[]/、,，&×・;", r) }) {
		part = strings.Join(strings.Fields(part), "")
		if part != "" {
			tokens[part] = true
		}
	}
	return tokens
}
func peopleOverlap(left, right []string) int {
	tokens := map[string]bool{}
	for _, name := range left {
		for key := range personTokens(name) {
			tokens[key] = true
		}
	}
	seen := map[string]bool{}
	for _, name := range right {
		for key := range personTokens(name) {
			if tokens[key] {
				seen[key] = true
			}
		}
	}
	return len(seen)
}
func infoboxValues(v json.RawMessage) []string {
	var text string
	if json.Unmarshal(v, &text) == nil {
		return []string{text}
	}
	var parts []struct {
		V string `json:"v"`
	}
	if json.Unmarshal(v, &parts) == nil {
		result := []string{}
		for _, p := range parts {
			result = append(result, p.V)
		}
		return result
	}
	return nil
}
func musicArtists(v musicSubject, people []musicPerson) []string {
	var artists []string
	for _, field := range v.Infobox {
		if field.Key == "艺术家" || field.Key == "演唱" {
			artists = append(artists, infoboxValues(field.Value)...)
		}
	}
	for _, p := range people {
		if p.Relation == "艺术家" || p.Relation == "演唱" {
			artists = append(artists, p.Name)
		}
	}
	return artists
}
func scoreMusicSubject(local storage.AlbumBangumiTarget, remote musicSubject, people []musicPerson) (int, []string, bool) {
	score, evidence := musicTitleScore(local.Title, remote.Name)
	if score < 30 {
		return 0, nil, false
	}
	artists := musicArtists(remote, people)
	matched := peopleOverlap(local.Artists, artists) > 0
	if matched {
		score += 30
		evidence = append(evidence, "艺术家一致")
	} else {
		evidence = append(evidence, "艺术家未匹配")
	}
	parse := func(s string) (time.Time, bool) {
		for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
			if t, e := time.Parse(layout, s); e == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	if a, ok := parse(local.ReleaseDate); ok {
		if b, ok := parse(remote.Date); ok {
			delta := a.Sub(b)
			if delta < 0 {
				delta = -delta
			}
			switch {
			case local.ReleaseDate == remote.Date && len(local.ReleaseDate) == 10:
				score += 15
				evidence = append(evidence, "发售同日")
			case delta <= 30*24*time.Hour:
				score += 10
				evidence = append(evidence, "发售相差不超过30天")
			case a.Year() == b.Year():
				score += 5
				evidence = append(evidence, "发售同年")
			case delta > 365*24*time.Hour:
				score -= 20
				evidence = append(evidence, "发售相差超过一年")
			}
		}
	}
	var credits []string
	for _, p := range people {
		if p.Relation == "作曲" || p.Relation == "作词" || p.Relation == "編曲" || p.Relation == "编曲" {
			credits = append(credits, p.Name)
		}
	}
	overlap := peopleOverlap(local.Credits, credits)
	if overlap >= 2 {
		score += 10
		evidence = append(evidence, "演职员重合两人以上")
	} else if overlap == 1 {
		score += 5
		evidence = append(evidence, "演职员重合一人")
	}
	for _, tag := range remote.MetaTags {
		if strings.Contains(tag, "单曲") || strings.EqualFold(tag, "OP") || strings.EqualFold(tag, "ED") {
			evidence = append(evidence, "Bangumi 标签："+tag)
		}
	}
	for _, tag := range remote.Tags {
		if strings.Contains(tag.Name, "单曲") || strings.EqualFold(tag.Name, "OP") || strings.EqualFold(tag.Name, "ED") {
			evidence = append(evidence, "Bangumi 标签："+tag.Name)
		}
	}
	if score > 100 {
		score = 100
	}
	if score < 0 {
		score = 0
	}
	return score, evidence, matched
}

// specificBangumiRole is a concrete song role. Album-level ost and the generic
// other/theme/image_song relations are not unique enough to auto-confirm a track.
func specificBangumiRole(role string) bool {
	switch role {
	case "op", "ed", "insert", "character":
		return true
	}
	return false
}

func relationRole(s string) string {
	switch s {
	case "片头曲":
		return "op"
	case "片尾曲":
		return "ed"
	case "插入歌":
		return "insert"
	case "原声集":
		return "ost"
	case "角色歌":
		return "character"
	case "主题歌":
		return "theme"
	case "印象曲", "意象歌":
		return "image_song"
	}
	return "other"
}
func (m *Manager) musicTieups(ctx context.Context, setting storage.MetadataSourceSetting, id int64, force bool) ([]storage.BangumiTieup, error) {
	base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
	if base == "" {
		base = "https://api.bgm.tv"
	}
	var related []musicRelation
	key := strconv.FormatInt(id, 10)
	_, err := m.cachedJSON(ctx, "bangumi", "subject-rel:"+key, base+"/v0/subjects/"+key+"/subjects", setting, force, nil, &related)
	if err != nil {
		return nil, err
	}
	var result []storage.BangumiTieup
	for _, r := range related {
		if r.Type != 2 && r.Type != 4 {
			continue
		}
		typ := "game"
		if r.Type == 2 {
			typ = "anime"
			// Relation summaries may omit platform; the subject endpoint is authoritative.
			if r.Platform == "" {
				var subject musicSubject
				workID := strconv.FormatInt(r.ID, 10)
				_, err = m.cachedJSON(ctx, "bangumi", "subject:"+workID, base+"/v0/subjects/"+workID, setting, force, nil, &subject)
				if err != nil {
					return nil, err
				}
				r.Platform = subject.Platform
				if r.Date == "" {
					r.Date = subject.Date
				}
			}
			if strings.Contains(r.Platform, "剧场版") || strings.Contains(r.Platform, "劇場版") {
				typ = "movie"
			}
		}
		var reverse []musicRelation
		workID := strconv.FormatInt(r.ID, 10)
		_, err = m.cachedJSON(ctx, "bangumi", "subject-rel:"+workID, base+"/v0/subjects/"+workID+"/subjects", setting, force, nil, &reverse)
		if err != nil {
			return nil, err
		}
		role := "other"
		for _, relation := range reverse {
			if relation.ID == id {
				role = relationRole(relation.Relation)
				break
			}
		}
		poster := r.Images.Large
		if poster == "" {
			poster = r.Images.Common
		}
		raw, _ := json.Marshal(r)
		result = append(result, storage.BangumiTieup{SubjectID: r.ID, Title: r.Name, NameCN: r.NameCN, Type: typ, Role: role, Date: r.Date, PosterURL: poster, Raw: raw})
	}
	return result, nil
}
func (m *Manager) enrichBangumiAlbum(ctx context.Context, runID int64, album storage.AlbumBangumiTarget, force bool) (string, error) {
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil || !setting.Enabled {
		return "skipped", err
	}
	// H1 applies to album search too: the request keyword replaces hyphens so a
	// leading "-" is not a Bangumi exclusion. Scoring still uses album.Title.
	hits, err := m.searchMusicSubjectsQuery(ctx, setting, album.Title, bangumiSearchKeyword(album.Title), force || album.Recheck, 25)
	if errors.Is(err, sql.ErrNoRows) {
		return "skipped", m.store.SetAlbumBangumiMiss(ctx, album)
	}
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
	if base == "" {
		base = "https://api.bgm.tv"
	}
	ranked := []musicSubject{}
	for _, v := range hits {
		score, _ := musicTitleScore(album.Title, v.Name)
		if v.Type == 3 && score >= 30 {
			ranked = append(ranked, v)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, _ := musicTitleScore(album.Title, ranked[i].Name)
		b, _ := musicTitleScore(album.Title, ranked[j].Name)
		return a > b
	})
	if len(ranked) > 3 {
		ranked = ranked[:3]
	}
	var candidates []storage.AlbumSubjectCandidate
	var artistMatched []bool
	allCandidateScores := map[string]int{}
	for _, hit := range ranked {
		key := strconv.FormatInt(hit.ID, 10)
		var detail musicSubject
		_, err = m.cachedJSON(ctx, "bangumi", "subject:"+key, base+"/v0/subjects/"+key, setting, force || album.Recheck, nil, &detail)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", err
		}
		var people []musicPerson
		_, err = m.cachedJSON(ctx, "bangumi", "subject-persons:"+key, base+"/v0/subjects/"+key+"/persons", setting, force || album.Recheck, nil, &people)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		score, evidence, matched := scoreMusicSubject(album, detail, people)
		if score < 30 {
			continue
		}
		allCandidateScores[key] = score
		ties, e := m.musicTieups(ctx, setting, hit.ID, force || album.Recheck)
		if e != nil {
			return "", e
		}
		// A music subject without any anime/game relation cannot create a work
		// association. Do not put an impossible-to-accept item in review.
		if len(ties) == 0 {
			continue
		}
		payload, _ := json.Marshal(detail)
		artists := strings.Join(musicArtists(detail, people), ", ")
		candidates = append(candidates, storage.AlbumSubjectCandidate{ExternalID: key, Title: detail.Name, ReleaseDate: detail.Date, Artist: artists, Score: score, Evidence: evidence, Tieups: ties, Payload: payload})
		artistMatched = append(artistMatched, matched)
	}
	if len(candidates) == 0 {
		return "skipped", m.store.SetAlbumBangumiMiss(ctx, album)
	}
	if _, lookupErr := m.store.AlbumByID(ctx, album.ID); errors.Is(lookupErr, sql.ErrNoRows) {
		return "skipped", nil
	} else if lookupErr != nil {
		return "", lookupErr
	}
	if err = m.store.SaveAlbumSubjectCandidates(ctx, album.ID, candidates); err != nil {
		return "", err
	}
	stored, err := m.store.AlbumSubjectCandidates(ctx, album.ID)
	if err != nil {
		return "", err
	}
	best := candidates[0]
	match := artistMatched[0]
	for i := 1; i < len(candidates); i++ {
		if candidates[i].Score > best.Score {
			best = candidates[i]
			match = artistMatched[i]
		}
	}
	second := -1
	for externalID, score := range allCandidateScores {
		if externalID != best.ExternalID && score > second {
			second = score
		}
	}
	specific := []int64{}
	for _, tie := range best.Tieups {
		switch tie.Role {
		case "op", "ed", "insert", "ost", "character":
			specific = append(specific, tie.SubjectID)
		}
	}
	if setting.AutoMatch && best.Score >= 80 && match && (second < 0 || best.Score-second >= 20) && len(specific) == 1 {
		for _, c := range stored {
			if c.ExternalID != best.ExternalID || c.Status != "candidate" {
				continue
			}
			var ids []int64
			var outcome string
			outcome, ids, err = m.confirmMusicWithRetry(ctx, runID, album, c.ID, specific)
			if err != nil {
				return "", err
			}
			if outcome == "succeeded" {
				for _, id := range ids {
					m.QueueWorkPoster(id)
				}
			}
			return outcome, nil
		}
	}
	return "review", nil
}

// errBangumiConfirmBusy permits deterministic transaction-retry tests; real SQLite
// BUSY/LOCKED errors are classified by their primary SQLite result code.
var errBangumiConfirmBusy = errors.New("bangumi confirmation busy")

// retryBusy retries a confirmation that lost the SQLite writer. A contested
// writer is reported as skipped, never as a provider failure.
func (m *Manager) retryBusy(ctx context.Context, label string, fn func() (string, []int64, error)) (string, []int64, error) {
	for attempt := 0; ; attempt++ {
		outcome, ids, err := fn()
		var sqliteErr *sqlite.Error
		if err == nil || !errors.Is(err, errBangumiConfirmBusy) && (!errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 && sqliteErr.Code()&0xff != 6) {
			return outcome, ids, err
		}
		if attempt >= 2 {
			m.logger.Warn("bangumi confirmation busy; skipping", "label", label, "error", err)
			return "skipped", nil, nil
		}
		wait := []time.Duration{200 * time.Millisecond, 500 * time.Millisecond}[attempt]
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// H6: a contested SQLite writer is a skipped album, not a provider failure.
func (m *Manager) confirmMusicWithRetry(ctx context.Context, runID int64, album storage.AlbumBangumiTarget, candidateID int64, selected []int64) (string, []int64, error) {
	confirm := m.confirmAlbumSubject
	if confirm == nil {
		confirm = m.store.ConfirmAlbumSubjectCandidate
	}
	return m.retryBusy(ctx, "album "+strconv.FormatInt(album.ID, 10), func() (string, []int64, error) {
		return confirm(ctx, album.ID, candidateID, runID, album.Fingerprint, true, selected)
	})
}
