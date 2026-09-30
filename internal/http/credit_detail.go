package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/lux032/032music-server/internal/storage"
)

func artistProfilePath(id int64) string {
	return "/admin/artists/" + strconv.FormatInt(id, 10) + "?view=profile"
}
func creditDetailPath(id int64, q url.Values, legacy bool) string {
	out := url.Values{}
	role, offset := q.Get("role"), q.Get("offset")
	if legacy {
		role, offset = q.Get("credit"), q.Get("creditOffset")
	}
	if storage.IsCreditRole(role) {
		out.Set("role", role)
	}
	if !legacy || storage.IsCreditRole(role) {
		if n, e := strconv.Atoi(offset); e == nil && n > 0 {
			out.Set("offset", strconv.Itoa(n))
		}
	}
	if q.Get("notice") != "" {
		out.Set("notice", q.Get("notice"))
	}
	path := "/admin/credits/" + strconv.FormatInt(id, 10)
	if len(out) > 0 {
		path += "?" + out.Encode()
	}
	return path
}

type creditDetailData struct {
	Chrome
	Artist                                    storage.ArtistDetail
	Notice, Role, FocusRole, PrevURL, NextURL string
	Roles                                     []storage.CreditRoleCount
	Tabs                                      []indexLink
	Pages                                     []pageLink
	Tracks                                    []storage.Track
	Collaborators, MoreCollaborators          []storage.Artist
	Albums, MoreAlbums                        []storage.Album
	AlbumTotal, SelfCount                     int64
}

func (a *App) handleCreditArtistPage(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	canonical, err := a.store.CanonicalArtistID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if canonical != id {
		http.Redirect(w, r, creditDetailPath(canonical, r.URL.Query(), false), 303)
		return
	}
	detail, err := a.store.ArtistDetail(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if detail.CreditTrackCount == 0 {
		if notice := r.URL.Query().Get("notice"); notice != "" {
			redirectWithNotice(w, r, artistProfilePath(id), notice)
		} else {
			http.Redirect(w, r, artistProfilePath(id), 303)
		}
		return
	}
	session, _ := a.sessions.get(r)
	data := creditDetailData{Chrome: a.chromeFor(r.Context(), session, "credits"), Artist: detail, Notice: r.URL.Query().Get("notice"), FocusRole: "any"}
	data.Roles, err = a.store.ArtistCreditRoles(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// Hero facts put composing before lyrics.
	if len(data.Roles) > 1 && data.Roles[0].Role == "lyricist" && data.Roles[1].Role == "composer" {
		data.Roles[0], data.Roles[1] = data.Roles[1], data.Roles[0]
	}
	data.Role = r.URL.Query().Get("role")
	validRole := false
	for _, role := range data.Roles {
		if role.Role == data.Role {
			validRole = true
		}
	}
	if !validRole {
		data.Role = ""
	}
	if data.Role != "" {
		data.FocusRole = data.Role
	}
	offset := max(0, int(parseInt64(r.URL.Query().Get("offset"))))
	f := storage.Filters{Limit: 20, Offset: offset, Focus: storage.TrackFocus{Credits: []storage.CreditFilter{{Role: data.FocusRole, ArtistID: id}}}}
	data.Tracks, err = a.store.ListTracks(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	total, err := a.store.CountTracks(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	link := func(n int) string {
		q := url.Values{}
		if data.Role != "" {
			q.Set("role", data.Role)
		}
		if n > 0 {
			q.Set("offset", strconv.Itoa(n))
		}
		path := "/admin/credits/" + strconv.FormatInt(id, 10)
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		return path
	}
	if total > 20 {
		if offset > 0 {
			data.PrevURL = link(max(0, offset-20))
		}
		if int64(offset+20) < total {
			data.NextURL = link(offset + 20)
		}
	}
	if total > 20 {
		page, count := offset/20+1, int((total+19)/20)
		start := max(1, page-2)
		end := min(count, start+4)
		start = max(1, end-4)
		for n := start; n <= end; n++ {
			data.Pages = append(data.Pages, pageLink{Number: n, URL: link((n - 1) * 20), Current: n == page})
		}
	}
	if len(data.Roles) > 1 {
		for _, role := range append([]storage.CreditRoleCount{{Count: detail.CreditTrackCount}}, data.Roles...) {
			label := "全部"
			if role.Role != "" {
				label = creditLabel(role.Role)
			}
			q := url.Values{}
			if role.Role != "" {
				q.Set("role", role.Role)
			}
			data.Tabs = append(data.Tabs, indexLink{Value: label + " " + strconv.FormatInt(role.Count, 10), URL: creditDetailPath(id, q, false), Current: role.Role == data.Role})
		}
	}
	// SelfCount restricts to backstage tracks, resolves merged performers, and
	// uses album artists only when primary credits are absent. PerformedTrackCount
	// unions current-ID primary and album-artist tracks regardless of primary
	// presence or backstage participation, so these counts intentionally differ.
	if err == nil {
		data.SelfCount, err = a.store.CreditSelfPerformedCount(r.Context(), id)
	}
	var people []storage.Artist
	var albums []storage.Album
	if err == nil {
		people, err = a.store.CreditCollaborators(r.Context(), id, 50)
	}
	if err == nil {
		albums, data.AlbumTotal, err = a.store.CreditAlbums(r.Context(), id, 60)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	n := min(12, len(people))
	data.Collaborators, data.MoreCollaborators = people[:n], people[n:]
	n = min(12, len(albums))
	data.Albums, data.MoreAlbums = albums[:n], albums[n:]
	a.render(w, 200, "credit.html", data)
}
