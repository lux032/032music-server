package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/bangumi"
	"github.com/lux032/032music-server/internal/sourcegate"
)

type sharedFetchKey struct{}

func isSharedFetch(ctx context.Context) bool {
	shared, _ := ctx.Value(sharedFetchKey{}).(bool)
	return shared
}

func bangumiEndpoints(base string) phase4Endpoints {
	base = strings.TrimRight(base, "/")
	if base == "" {
		base = "https://api.bgm.tv"
	}
	return phase4Endpoints{Bangumi: base + "/v0/search/subjects", BangumiAPI: base}
}

// BangumiEnabled reports the current source setting, independently of AutoMatch.
func (m *Manager) BangumiEnabled(ctx context.Context) (bool, error) {
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	return setting.Enabled, err
}

func sharedBangumiError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return bangumi.ErrGone
	}
	if limited := asRateLimited(err); limited != nil {
		return &sourcegate.CooldownError{Source: "bangumi", RetryAfter: limited.RetryAfter}
	}
	return err
}

// BangumiRelations shares enrichment's persistent cache, but never its run memo.
func (m *Manager) BangumiRelations(ctx context.Context, subjectID int64) ([]bangumi.Relation, error) {
	ctx = context.WithValue(ctx, sharedFetchKey{}, true)
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		return nil, err
	}
	key := strconv.FormatInt(subjectID, 10)
	var relations []bangumi.Relation
	_, err = m.cachedJSON(ctx, "bangumi", "subject-rel:"+key, strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")+"/v0/subjects/"+key+"/subjects", setting, false, nil, &relations)
	return relations, sharedBangumiError(err)
}

// BangumiMusicSubject reads artists from the detail infobox; no persons lookup
// is needed because this API does not perform local-artist matching.
func (m *Manager) BangumiMusicSubject(ctx context.Context, subjectID int64) (bangumi.MusicSubject, error) {
	ctx = context.WithValue(ctx, sharedFetchKey{}, true)
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		return bangumi.MusicSubject{}, err
	}
	key := strconv.FormatInt(subjectID, 10)
	var subject musicSubject
	_, err = m.cachedJSON(ctx, "bangumi", "subject:"+key, strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")+"/v0/subjects/"+key, setting, false, nil, &subject)
	return bangumi.MusicSubject{ID: subject.ID, Name: subject.Name, NameCN: subject.NameCN, Date: subject.Date, Artists: musicArtists(subject, nil)}, sharedBangumiError(err)
}
