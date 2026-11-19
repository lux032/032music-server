package httpapi

import "github.com/lux032/032music-server/internal/storage"

// artistLinkItem is one performing artist in the shared artist-links partial.
// JoinPhrase is the original separator that belongs in front of this name
// (empty for the first). Empty source phrases are stored as ", ".
type artistLinkItem struct {
	ID         int64
	Name       string
	JoinPhrase string
}

// artistLinksView is the context for the "artist-links" template.
// Mode "detail" prints original join phrases. Any other mode is the compact
// one-line treatment (pipe separators, +N when the line overflows).
type artistLinksView struct {
	Mode     string
	Items    []artistLinkItem
	Fallback string
}

// artistLinks builds the shared performing-artist view. artists accepts the
// two credit slices the templates already have ([]storage.Artist and
// []storage.ArtistRef). primary is preferred over secondary when the slice
// is empty (PerformedBy, then the legacy Artist string).
func artistLinks(mode string, artists any, primary, secondary string) artistLinksView {
	if mode != "detail" {
		mode = "compact"
	}
	items := artistLinkItems(artists)
	fallback := primary
	if fallback == "" {
		fallback = secondary
	}
	if len(items) == 0 {
		return artistLinksView{Mode: mode, Fallback: fallback}
	}
	return artistLinksView{Mode: mode, Items: items}
}

func artistLinkItems(value any) []artistLinkItem {
	switch artists := value.(type) {
	case []storage.Artist:
		return mapArtistLinks(len(artists), func(i int) (int64, string, *string) {
			return artists[i].ID, artists[i].Name, artists[i].JoinPhrase
		})
	case []storage.ArtistRef:
		return mapArtistLinks(len(artists), func(i int) (int64, string, *string) {
			return artists[i].ID, artists[i].Name, artists[i].JoinPhrase
		})
	default:
		return nil
	}
}

func mapArtistLinks(n int, at func(int) (int64, string, *string)) []artistLinkItem {
	if n == 0 {
		return nil
	}
	items := make([]artistLinkItem, 0, n)
	for i := 0; i < n; i++ {
		id, name, phrase := at(i)
		join := ""
		if i > 0 {
			if phrase != nil && *phrase != "" {
				join = *phrase
			} else {
				join = ", "
			}
		}
		items = append(items, artistLinkItem{ID: id, Name: name, JoinPhrase: join})
	}
	return items
}
