package httpapi

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/lux032/032music-server/internal/storage"
)

// albumPerson is one row of the album detail “参与者” list: everyone who
// performs on or is credited on the album's tracks, built from data the
// detail page already loads.
type albumPerson struct {
	ID       int64
	Name     string
	ImageURL string
	URL      string
	Roles    string
	Tracks   int
	Initial  string
}

var albumCreditOrder = []struct{ role, label string }{
	{"composer", "作曲"},
	{"lyricist", "作词"},
	{"arranger", "编曲"},
	{"producer", "制作"},
}

// albumPeople merges track performers and credits per person, ordered by how
// many tracks they appear on, performers first on ties.
func albumPeople(tracks []storage.Track) []albumPerson {
	type entry struct {
		person    albumPerson
		roles     []string
		trackSet  map[int64]bool
		performer bool
		order     int
	}
	byID := map[int64]*entry{}
	get := func(id int64, name string) *entry {
		e := byID[id]
		if e == nil {
			e = &entry{person: albumPerson{ID: id, Name: name}, trackSet: map[int64]bool{}, order: len(byID)}
			byID[id] = e
		}
		return e
	}
	addRole := func(e *entry, role string) {
		for _, r := range e.roles {
			if r == role {
				return
			}
		}
		e.roles = append(e.roles, role)
	}
	for _, t := range tracks {
		for _, a := range t.Artists {
			if a.ID == 0 || strings.TrimSpace(a.Name) == "" {
				continue
			}
			e := get(a.ID, a.Name)
			e.performer = true
			if a.ImageURL != "" {
				e.person.ImageURL = a.ImageURL
			}
			addRole(e, "演唱")
			e.trackSet[t.ID] = true
		}
		for _, c := range albumCreditOrder {
			for _, a := range creditPeople(t, c.role) {
				if a.ID == 0 || strings.TrimSpace(a.Name) == "" {
					continue
				}
				e := get(a.ID, a.Name)
				addRole(e, c.label)
				e.trackSet[t.ID] = true
				if e.person.URL == "" && !e.performer {
					e.person.URL = fmt.Sprintf("/admin/credits/%d?role=%s", a.ID, c.role)
				}
			}
		}
	}
	list := make([]*entry, 0, len(byID))
	for _, e := range byID {
		list = append(list, e)
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if len(a.trackSet) != len(b.trackSet) {
			return len(a.trackSet) > len(b.trackSet)
		}
		if a.performer != b.performer {
			return a.performer
		}
		return a.order < b.order
	})
	out := make([]albumPerson, 0, len(list))
	for _, e := range list {
		p := e.person
		if e.performer {
			p.URL = fmt.Sprintf("/admin/artists/%d", p.ID)
		}
		p.Roles = strings.Join(e.roles, " · ")
		p.Tracks = len(e.trackSet)
		p.Initial = nameInitial(p.Name)
		out = append(out, p)
	}
	return out
}

// nameInitial is the first letter of a name, used for avatar placeholders.
func nameInitial(name string) string {
	name = strings.TrimSpace(name)
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return strings.ToUpper(string(r))
		}
	}
	for _, r := range name {
		return string(r)
	}
	return ""
}

// tracksDuration sums the playable length of a track list.
func tracksDuration(tracks []storage.Track) int64 {
	var total int64
	for _, t := range tracks {
		if t.DurationMillis > 0 {
			total += t.DurationMillis
		}
	}
	return total
}

// discSummary describes one disc of an album: “5 首 · 26:29”.
func discSummary(tracks []storage.Track, disc int) string {
	count := 0
	var total int64
	for _, t := range tracks {
		if t.DiscNumber == disc {
			count++
			if t.DurationMillis > 0 {
				total += t.DurationMillis
			}
		}
	}
	if total == 0 {
		return fmt.Sprintf("%d 首", count)
	}
	return fmt.Sprintf("%d 首 · %s", count, formatDurationMillis(total))
}
