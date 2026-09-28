package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"golang.org/x/text/unicode/norm"
)

// bangumiSearchKeyword makes a keyword safe for Bangumi search. A leading "-"
// is an exclusion operator, so ASCII hyphens become spaces (H1).
func bangumiSearchKeyword(s string) string {
	s = norm.NFKC.String(s)
	s = strings.ReplaceAll(s, "-", " ")
	return strings.Join(strings.Fields(s), " ")
}

// searchMusicSubjects returns music entries whose name matches the keyword.
// It reads the next page only while the last result of the current page is
// still an exact name match, and never past the third page (M2).
func (m *Manager) searchMusicSubjects(ctx context.Context, setting storage.MetadataSourceSetting, keyword string, force bool) ([]musicSubject, error) {
	return m.searchMusicSubjectsQuery(ctx, setting, keyword, bangumiSearchKeyword(keyword), force)
}

// searchMusicSubjectsQuery searches with an already-chosen keyword. Both album
// and track lookup pass bangumiSearchKeyword so a leading "-" is not an exclusion
// (H1). Scoring and title comparison still use the original title.
func (m *Manager) searchMusicSubjectsQuery(ctx context.Context, setting storage.MetadataSourceSetting, title, query string, force bool) ([]musicSubject, error) {
	base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
	if base == "" {
		base = "https://api.bgm.tv"
	}
	if strings.TrimSpace(query) == "" {
		return nil, sql.ErrNoRows
	}
	var all []musicSubject
	for page := 0; page < 3; page++ {
		var result musicSearchResponse
		endpoint := base + "/v0/search/subjects?limit=20&offset=" + strconv.Itoa(page*20)
		_, err := m.cachedJSON(ctx, "bangumi", "music-search:v2:"+query+":"+strconv.Itoa(page), endpoint, setting, force, map[string]any{"keyword": query, "filter": map[string]any{"type": []int{3}}}, &result)
		if errors.Is(err, sql.ErrNoRows) {
			if page == 0 {
				return nil, err
			}
			break
		}
		if err != nil {
			return nil, err
		}
		all = append(all, result.Data...)
		if len(result.Data) < 20 {
			break
		}
		last := result.Data[len(result.Data)-1]
		if storage.BangumiTrackTitleKey(last.Name, false) != storage.BangumiTrackTitleKey(title, false) && storage.BangumiTrackTitleKey(last.Name, true) != storage.BangumiTrackTitleKey(title, true) {
			break
		}
	}
	if len(all) == 0 {
		return nil, sql.ErrNoRows
	}
	return all, nil
}

// musicSubjectArtists prefers the artists already present in the search result
// and only fetches the persons endpoint when those do not match (M3).
func (m *Manager) musicSubjectArtists(ctx context.Context, setting storage.MetadataSourceSetting, subject musicSubject, local []string, force bool) ([]string, error) {
	artists := musicArtists(subject, nil)
	if peopleOverlap(local, artists) > 0 || len(local) == 0 {
		return artists, nil
	}
	base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
	if base == "" {
		base = "https://api.bgm.tv"
	}
	key := strconv.FormatInt(subject.ID, 10)
	var people []musicPerson
	_, err := m.cachedJSON(ctx, "bangumi", "subject-persons:"+key, base+"/v0/subjects/"+key+"/persons", setting, force, nil, &people)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return musicArtists(subject, people), nil
}

// splitMultiTitle separates an entry that names several songs ("A / B").
// Every slash is a separator, including a single slash inside one title
// ("赤い罠(who loves it?)/ADAMAS"). Fewer than two non-empty parts is not a multi-title.
func splitMultiTitle(name string) []string {
	name = norm.NFKC.String(strings.TrimSpace(name))
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' })
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

type musicHit struct {
	subject   musicSubject
	kind      string
	entryName string
	artists   []string
	tieups    []storage.BangumiTieup
	specific  []storage.BangumiTieup
}

func titleLayer(name, query string) string {
	if storage.BangumiTrackTitleKey(name, false) != "" && storage.BangumiTrackTitleKey(name, false) == storage.BangumiTrackTitleKey(query, false) {
		return "strict"
	}
	if storage.BangumiTrackTitleKey(name, true) != "" && storage.BangumiTrackTitleKey(name, true) == storage.BangumiTrackTitleKey(query, true) {
		return "loose"
	}
	return ""
}

// strictestTitleLayer keeps only the strongest layer present. A loose match is
// used only when no strict match exists (H3).
func strictestTitleLayer(subjects []musicSubject, query string) (string, []musicSubject) {
	best := ""
	var exact []musicSubject
	for _, subject := range subjects {
		if subject.Type != 3 {
			continue
		}
		got := titleLayer(subject.Name, query)
		switch {
		case got == "":
			continue
		case best == "" || got == "strict" && best == "loose":
			best = got
			exact = []musicSubject{subject}
		case got == best:
			exact = append(exact, subject)
		}
	}
	return best, exact
}

func (m *Manager) collectMusicHits(ctx context.Context, setting storage.MetadataSourceSetting, keyword, kind string, artists []string, force bool) ([]musicHit, error) {
	subjects, err := m.searchMusicSubjects(ctx, setting, keyword, force)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, exact := strictestTitleLayer(subjects, keyword)
	var hits []musicHit
	for _, subject := range exact {
		remote, e := m.musicSubjectArtists(ctx, setting, subject, artists, force)
		if e != nil {
			return nil, e
		}
		if len(artists) == 0 || peopleOverlap(artists, remote) == 0 {
			continue
		}
		ties, e := m.musicTieups(ctx, setting, subject.ID, force)
		if e != nil {
			return nil, e
		}
		var specific []storage.BangumiTieup
		for _, tie := range ties {
			if specificBangumiRole(tie.Role) {
				specific = append(specific, tie)
			}
		}
		// An entry that registers no work at all is a real miss, not a reason to
		// fall through to a different recording (only my railgun -version2020-,
		// subject 303221). Ties that are only generic relations (other, theme,
		// drama, image song, soundtrack) are a review candidate, not a miss.
		if len(ties) == 0 {
			continue
		}
		hits = append(hits, musicHit{subject: subject, kind: kind, artists: remote, tieups: ties, specific: specific})
	}
	return hits, nil
}

func (m *Manager) enrichBangumiTrack(ctx context.Context, runID int64, track storage.TrackBangumiTarget, force bool) (string, error) {
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil || !setting.Enabled {
		return "skipped", err
	}
	hits, err := m.collectMusicHits(ctx, setting, track.Title, "exact", track.Artists, force || track.Recheck)
	if err != nil {
		return "", err
	}
	// A hit that only has generic relations is already a review candidate.
	// Do not keep searching version suffixes or multi-title entries.
	genericOnly := false
	for _, hit := range hits {
		if len(hit.specific) == 0 {
			genericOnly = true
			break
		}
	}
	if len(hits) == 0 && (track.TrackType == "tv_size" || track.TrackType == "instrumental" || track.TrackType == "off_vocal") {
		base := metadata.StripTrackTypeSuffix(track.Title, track.TrackType)
		if base != track.Title && storage.BangumiTrackTitleKey(base, false) != "" {
			hits, err = m.collectMusicHits(ctx, setting, base, "version_base", track.Artists, force || track.Recheck)
			if err != nil {
				return "", err
			}
		}
	}
	multi := false
	if len(hits) == 0 && !genericOnly {
		multi = true
		hits, err = m.multiTitleHits(ctx, setting, track, force || track.Recheck)
		if err != nil {
			return "", err
		}
	}
	if len(hits) == 0 {
		return "skipped", m.store.SetTrackBangumiMiss(ctx, track, "no matching music entry")
	}
	candidates := make([]storage.TrackSubjectCandidate, 0, len(hits))
	for _, hit := range hits {
		title := hit.subject.Name
		var payload []byte
		if hit.kind == "multi_title" && hit.entryName != "" {
			// The candidate keeps the original entry name; the slash-separated
			// segment that matched the track is stored as its own field (L-3).
			title = hit.entryName
			payload, _ = json.Marshal(struct {
				musicSubject
				Name         string `json:"name"`
				MatchedTitle string `json:"matched_title"`
			}{musicSubject: hit.subject, Name: hit.entryName, MatchedTitle: hit.subject.Name})
		} else {
			payload, _ = json.Marshal(hit.subject)
		}
		candidates = append(candidates, storage.TrackSubjectCandidate{ExternalID: strconv.FormatInt(hit.subject.ID, 10), Title: title, Artist: strings.Join(hit.artists, ", "), MatchKind: hit.kind, Evidence: []string{"条目名一致", "歌手一致"}, Tieups: hit.tieups, Payload: payload})
	}
	if _, lookupErr := m.store.TrackSubjectCandidates(ctx, track.ID); errors.Is(lookupErr, sql.ErrNoRows) {
		return "skipped", nil
	} else if lookupErr != nil {
		return "", lookupErr
	}
	if err = m.store.SaveTrackSubjectCandidates(ctx, track.ID, candidates); err != nil {
		return "", err
	}
	stored, err := m.store.TrackSubjectCandidates(ctx, track.ID)
	if err != nil {
		return "", err
	}
	if multi || genericOnly {
		return "review", nil
	}
	chosen := hits[0]
	signature := tieSignature(chosen.specific)
	for _, hit := range hits[1:] {
		if tieSignature(hit.specific) != signature {
			return "review", nil
		}
		if hit.subject.ID < chosen.subject.ID {
			chosen = hit
		}
	}
	if len(chosen.specific) != 1 {
		return "review", nil
	}
	for _, candidate := range stored {
		if candidate.ExternalID != strconv.FormatInt(chosen.subject.ID, 10) || candidate.Status != "candidate" {
			continue
		}
		if !setting.AutoMatch {
			return "review", nil
		}
		outcome, ids, e := m.retryBusy(ctx, "track "+strconv.FormatInt(track.ID, 10), func() (string, []int64, error) {
			return m.store.ConfirmTrackSubjectCandidate(ctx, track.ID, candidate.ID, runID, track.Fingerprint, true, []int64{chosen.specific[0].SubjectID}, nil)
		})
		if e != nil {
			return "", e
		}
		if outcome == "succeeded" {
			for _, id := range ids {
				m.QueueWorkPoster(id)
			}
		}
		return outcome, nil
	}
	return "review", nil
}

// renameMultiParts exposes each slash-separated song as its own name so the
// strict-before-loose rule applies to the parts instead of the whole entry.
func renameMultiParts(subjects []musicSubject, query string) []musicSubject {
	var renamed []musicSubject
	for _, subject := range subjects {
		for _, part := range splitMultiTitle(subject.Name) {
			if titleLayer(part, query) == "" {
				continue
			}
			copy := subject
			copy.Name = part
			renamed = append(renamed, copy)
		}
	}
	return renamed
}

func (m *Manager) multiTitleHits(ctx context.Context, setting storage.MetadataSourceSetting, track storage.TrackBangumiTarget, force bool) ([]musicHit, error) {
	subjects, err := m.searchMusicSubjects(ctx, setting, track.Title, force)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var multi []musicSubject
	for _, subject := range subjects {
		if subject.Type != 3 || len(splitMultiTitle(subject.Name)) < 2 {
			continue
		}
		multi = append(multi, subject)
	}
	_, exact := strictestTitleLayer(renameMultiParts(multi, track.Title), track.Title)
	var hits []musicHit
	for _, subject := range exact {
		remote, e := m.musicSubjectArtists(ctx, setting, subject, track.Artists, force)
		if e != nil {
			return nil, e
		}
		if len(track.Artists) == 0 || peopleOverlap(track.Artists, remote) == 0 {
			continue
		}
		ties, e := m.musicTieups(ctx, setting, subject.ID, force)
		if e != nil {
			return nil, e
		}
		// Same as the single-title lookup: an entry that registers no work at
		// all is a real miss, not a review candidate (B1).
		if len(ties) == 0 {
			continue
		}
		original := subject.Name
		for _, source := range multi {
			if source.ID == subject.ID {
				original = source.Name
				break
			}
		}
		hits = append(hits, musicHit{subject: subject, kind: "multi_title", entryName: original, artists: remote, tieups: ties})
	}
	return hits, nil
}

func tieSignature(ties []storage.BangumiTieup) string {
	parts := make([]string, len(ties))
	for i, tie := range ties {
		parts[i] = strconv.FormatInt(tie.SubjectID, 10) + "=" + tie.Role
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
