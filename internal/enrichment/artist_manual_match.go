package enrichment

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

var (
	ErrManualSearchEmpty       = errors.New("请输入歌手名、MusicBrainz 链接或 MBID")
	ErrManualSearchUnavailable = errors.New("MusicBrainz 数据源未启用，无法手动搜索")
	mbidPattern                = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
)

// ManualArtistSearchResult reports what a manual search produced. When the
// query contains an MBID the identity is looked up and ConfirmCandidateID is
// set so the caller can bind it through the regular confirmation path.
type ManualArtistSearchResult struct {
	CandidateCount     int
	ConfirmCandidateID int64
}

// SearchArtistCandidates lets the user look up MusicBrainz identities with a
// custom query (or an explicit MBID / MusicBrainz URL). It never auto-binds:
// text results are stored as pending candidates for manual review.
func (m *Manager) SearchArtistCandidates(ctx context.Context, artistID int64, query string) (ManualArtistSearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return ManualArtistSearchResult{}, ErrManualSearchEmpty
	}
	artist, err := m.store.ArtistForMatching(ctx, artistID)
	if err != nil {
		return ManualArtistSearchResult{}, err
	}
	settings, err := m.store.MetadataSourceSettings(ctx)
	if err != nil {
		return ManualArtistSearchResult{}, err
	}
	var setting storage.MetadataSourceSetting
	enabled := false
	for _, s := range settings {
		if s.Source == "musicbrainz" && s.Enabled {
			setting, enabled = s, true
		}
	}
	if !enabled {
		return ManualArtistSearchResult{}, ErrManualSearchUnavailable
	}

	if mbid := mbidPattern.FindString(query); mbid != "" {
		candidate, _, err := m.musicBrainzLookup(ctx, strings.ToLower(mbid), setting)
		if err != nil {
			return ManualArtistSearchResult{}, err
		}
		candidate.ArtistID = artistID
		candidate.Score = 100
		candidate.Evidence = []string{"手动指定 MusicBrainz ID"}
		if err = m.store.AddArtistCandidates(ctx, artistID, []storage.ArtistCandidate{candidate}); err != nil {
			return ManualArtistSearchResult{}, err
		}
		id, err := m.store.ArtistCandidateID(ctx, artistID, candidate.Source, candidate.ExternalID)
		if err != nil {
			return ManualArtistSearchResult{}, err
		}
		return ManualArtistSearchResult{CandidateCount: 1, ConfirmCandidateID: id}, nil
	}

	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(query)
	found, _, err := m.musicBrainzSearchQuery(ctx, artist, `artist:"`+escaped+`" OR alias:"`+escaped+`"`, 15, setting)
	if err != nil {
		return ManualArtistSearchResult{}, err
	}
	for i := range found {
		found[i].ArtistID = artistID
		// Manual search results always require explicit confirmation.
		if found[i].Score > 85 {
			found[i].Score = 85
		}
		found[i].Evidence = []string{"手动搜索「" + query + "」"}
	}
	if len(found) > 0 {
		if err = m.store.AddArtistCandidates(ctx, artistID, found); err != nil {
			return ManualArtistSearchResult{}, err
		}
	}
	return ManualArtistSearchResult{CandidateCount: len(found)}, nil
}
