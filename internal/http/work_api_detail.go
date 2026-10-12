package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/lux032/032music-server/internal/storage"
)

// apiWorkSeriesRef is the series a work belongs to, as shown in the work
// page's series bar: Position is the 1-based place in the series' member
// order (“当前：第 N 部”), Source how the work joined (auto / manual).
type apiWorkSeriesRef struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	TitleSource string `json:"titleSource"`
	MemberCount int    `json:"memberCount"`
	Position    int    `json:"position"`
	Source      string `json:"source"`
}

// apiWorkDetail is GET /api/v1/works/{id}: the unchanged Work object plus the
// work page's poster, Bangumi binding and series information. posterUrl keeps
// its stored (remote) value so PATCH round-trips are unaffected.
type apiWorkDetail struct {
	storage.Work
	PosterAPIURL     string            `json:"posterApiUrl"`
	BangumiSubjectID string            `json:"bangumiSubjectId"`
	Series           *apiWorkSeriesRef `json:"series"`
	SeriesLocked     bool              `json:"seriesLocked"`
}

// workSeriesPosition returns the work's 1-based position among the members
// and its membership source; (0, "") when it is not a member.
func workSeriesPosition(members []storage.WorkSeriesMember, workID int64) (int, string) {
	for idx, member := range members {
		if member.Work.ID == workID {
			return idx + 1, member.Source
		}
	}
	return 0, ""
}

func (a *App) workDetail(ctx context.Context, id int64) (apiWorkDetail, error) {
	work, err := a.store.WorkByID(ctx, id)
	if err != nil {
		return apiWorkDetail{}, err
	}
	out := apiWorkDetail{Work: work, PosterAPIURL: workPosterAPIURL(a.enrichment, work, 0)}
	if out.BangumiSubjectID, err = a.store.WorkBangumiExternalID(ctx, id); err != nil {
		return apiWorkDetail{}, err
	}
	series, members, err := a.store.SeriesForWork(ctx, id)
	switch {
	case err == nil:
		position, source := workSeriesPosition(members, id)
		out.Series = &apiWorkSeriesRef{ID: series.ID, Title: series.Title, TitleSource: series.TitleSource, MemberCount: series.MemberCount, Position: position, Source: source}
	case !errors.Is(err, sql.ErrNoRows):
		return apiWorkDetail{}, err
	}
	if out.SeriesLocked, err = a.store.WorkSeriesLocked(ctx, id); err != nil {
		return apiWorkDetail{}, err
	}
	return out, nil
}

func (a *App) handleAPIWork(w http.ResponseWriter, r *http.Request) {
	value, err := a.workDetail(r.Context(), parseInt64(r.PathValue("id")))
	if err != nil {
		writeWorkResult(w, http.StatusOK, storage.Work{}, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
