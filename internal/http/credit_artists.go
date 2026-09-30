package httpapi

import (
	"github.com/lux032/032music-server/internal/storage"
	"net/http"
)

func (a *App) handleCreditArtistsPage(w http.ResponseWriter, r *http.Request) {
	rememberSort(w, r, "credits")
	data, err := a.pageBase(r, "artists")
	data.Nav = "credits"
	data.ArtistRole = "credits"
	data.ArtistRoleLabel = "幕后人员"
	data.CreditRole = r.URL.Query().Get("credit")
	if !storage.IsCreditRole(data.CreditRole) {
		data.CreditRole = ""
	}
	data.ClearPath = clearPathFor(r, "/admin/credits")
	data.IndexLinks = indexLinks(r, "/admin/credits")
	data.FilterTags = filterTags(r, "/admin/credits")
	for _, role := range append([]string{""}, storage.CreditRoles...) {
		q := filterQuery(r)
		q.Del("credit")
		if role != "" {
			q.Set("credit", role)
		}
		label := "全部"
		if role != "" {
			label = creditLabel(role)
		}
		data.CreditTabs = append(data.CreditTabs, indexLink{Value: label, URL: "/admin/credits?" + q.Encode(), Current: role == data.CreditRole})
	}
	f := filters(r)
	applyPage(r, &f, 60)
	cf := storage.CreditArtistFilters{Role: data.CreditRole, Query: f.Query, Index: f.Index, Sort: f.Sort, Limit: f.Limit, Offset: f.Offset}
	if err == nil {
		data.Artists, err = a.store.ListCreditArtists(r.Context(), cf)
	}
	if err == nil {
		data.Total, err = a.store.CountCreditArtists(r.Context(), cf)
	}
	data.TotalLabel = formatLibraryCount(data.Total)
	setPagination(r, &data, f)
	a.renderLibrary(w, data, err)
}
