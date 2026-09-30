package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

var trackFocusNames = []string{"decade", "yearFrom", "yearTo", "trackType", "excludeTypes", "tieupRole", "workType", "quality", "format", "credit", "composerArtist", "lyricistArtist", "arrangerArtist", "producerArtist", "hideInstrumental"}

func focusValues(q url.Values, name string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range q[name] {
		for _, v := range strings.Split(raw, ",") {
			v = strings.TrimSpace(v)
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}
func parseTrackFocus(q url.Values) (storage.TrackFocus, []string) {
	f := storage.TrackFocus{}
	bad := []string{}
	list := func(name, allowed string) []string {
		out := []string{}
		for _, v := range focusValues(q, name) {
			if strings.Contains(","+allowed+",", ","+v+",") {
				out = append(out, v)
			} else {
				bad = append(bad, name+"="+v)
			}
		}
		return out
	}
	f.TrackTypes = list("trackType", "regular,instrumental,off_vocal,tv_size,drama_track,remix")
	f.ExcludeTypes = list("excludeTypes", "regular,instrumental,off_vocal,tv_size,drama_track,remix")
	f.TieupRoles = list("tieupRole", "op,ed,insert,theme,character,ost,image_song,other")
	f.WorkTypes = list("workType", "anime,drama,movie,game,commercial,other")
	f.Formats = list("format", "flac,alac,aac,mp3,opus,vorbis")
	qualities := list("quality", "lossless,hires,lossy")
	if len(qualities) > 1 {
		bad = append(bad, "quality")
	} else if len(qualities) == 1 {
		f.Quality = qualities[0]
	}
	number := func(name string, decade bool) []int {
		out := []int{}
		for _, v := range focusValues(q, name) {
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > 9999 || (decade && n%10 != 0) {
				bad = append(bad, name+"="+v)
			} else {
				out = append(out, n)
			}
		}
		return out
	}
	f.Decades = number("decade", true)
	for _, name := range []string{"yearFrom", "yearTo"} {
		ns := number(name, false)
		if len(ns) > 1 {
			bad = append(bad, name)
		} else if len(ns) == 1 {
			if name == "yearFrom" {
				f.YearFrom = ns[0]
			} else {
				f.YearTo = ns[0]
			}
		}
	}
	if f.YearFrom > 0 && f.YearTo > 0 && f.YearFrom > f.YearTo {
		bad = append(bad, "yearFrom/yearTo")
		f.YearFrom = 0
		f.YearTo = 0
	}
	if q.Get("hideInstrumental") == "true" {
		for _, v := range []string{"instrumental", "off_vocal"} {
			found := false
			for _, x := range f.ExcludeTypes {
				if x == v {
					found = true
				}
			}
			if !found {
				f.ExcludeTypes = append(f.ExcludeTypes, v)
			}
		}
	}
	for _, v := range f.TrackTypes {
		for _, x := range f.ExcludeTypes {
			if x == v {
				bad = append(bad, "trackType/excludeTypes="+v)
				f.TrackTypes = nil
				break
			}
		}
	}
	credits := focusValues(q, "credit")
	for _, role := range storage.CreditRoles {
		for _, id := range focusValues(q, role+"Artist") {
			credits = append(credits, role+":"+id)
		}
	}
	seen := map[string]bool{}
	for _, c := range credits {
		parts := strings.Split(c, ":")
		if len(parts) != 2 || (!storage.IsCreditRole(parts[0]) && parts[0] != "any") {
			bad = append(bad, "credit="+c)
			continue
		}
		id, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || id <= 0 {
			bad = append(bad, "credit="+c)
			continue
		}
		key := parts[0] + ":" + strconv.FormatInt(id, 10)
		if !seen[key] {
			f.Credits = append(f.Credits, storage.CreditFilter{Role: parts[0], ArtistID: id})
			seen[key] = true
		}
	}
	return f, bad
}
func creditLabel(role string) string {
	switch role {
	case "any":
		return "幕后"
	case "lyricist":
		return "作词"
	case "composer":
		return "作曲"
	case "arranger":
		return "编曲"
	case "producer":
		return "制作"
	}
	return role
}
func (a *App) handleAPIArtistCredits(w http.ResponseWriter, r *http.Request) {
	id, err := a.store.CanonicalArtistID(r.Context(), parseInt64(r.PathValue("id")))
	if errors.Is(err, sql.ErrNoRows) {
		writeAPIError(w, 404, "not_found", "歌手不存在")
		return
	}
	if err != nil {
		writeAPIError(w, 500, "query_failed", err.Error())
		return
	}
	f := filters(r)
	var bad []string
	f.Focus, bad = parseTrackFocus(r.URL.Query())
	role := r.URL.Query().Get("role")
	if role != "" && !storage.IsCreditRole(role) {
		bad = append(bad, "role="+role)
	}
	if len(bad) > 0 {
		writeAPIError(w, 400, "invalid_filter", strings.Join(bad, ", "))
		return
	}
	roles, err := a.store.ArtistCreditRoles(r.Context(), id)
	if err != nil {
		apiResult(w, nil, err)
		return
	}
	limit, offset := pageValues(r)
	f.Limit, f.Offset = limit, offset
	// Any backstage role is an OR within this artist dimension; explicit role is exact.
	if role != "" {
		f.Focus.Credits = append(f.Focus.Credits, storage.CreditFilter{Role: role, ArtistID: id})
	} else {
		f.Focus.CreditArtistID = id
	}
	items, err := a.store.ListTracks(r.Context(), f)
	if err != nil {
		apiResult(w, nil, err)
		return
	}
	total, err := a.store.CountTracks(r.Context(), f)
	if err != nil {
		apiResult(w, nil, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset, "roles": roles})
}
