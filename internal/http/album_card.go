package httpapi

import "github.com/lux032/032music-server/internal/storage"

// albumCardView is the context for the shared "album-card" template. The
// template needs the album plus the page-level CSRF token and return address,
// which a sub-template cannot reach through the outer $, so the albumCard
// template function bundles them (High-2).
type albumCardView struct {
	Album     storage.Album
	CSRFToken string
	ReturnTo  string
}

func albumCardContext(album storage.Album, csrfToken, returnTo string) albumCardView {
	return albumCardView{Album: album, CSRFToken: csrfToken, ReturnTo: returnTo}
}

// albumSelectionBarView is the context for the shared "album-selection-bar"
// template: exactly one bulk-action bar per page.
type albumSelectionBarView struct {
	CSRFToken string
	ReturnTo  string
}

func albumSelectionBarContext(csrfToken, returnTo string) albumSelectionBarView {
	return albumSelectionBarView{CSRFToken: csrfToken, ReturnTo: returnTo}
}

// artistFavoriteView is the context for the shared "artist-favorite" heart
// form. Variant picks the look: "hero" for detail-page headers, "icon" for
// list rows and favorite cards.
type artistFavoriteView struct {
	ID         int64
	Name       string
	IsFavorite bool
	CSRFToken  string
	ReturnTo   string
	Variant    string
}

func artistFavoriteContext(id int64, name string, favorite bool, csrfToken, returnTo, variant string) artistFavoriteView {
	return artistFavoriteView{ID: id, Name: name, IsFavorite: favorite, CSRFToken: csrfToken, ReturnTo: returnTo, Variant: variant}
}

// albumKindLabel names the effective release kind on an album card when it
// is anything other than a regular album, so same-titled singles, EPs and
// compilations stay distinguishable in the grid. Regular albums return "".
func albumKindLabel(album storage.Album) string {
	kind := album.ReleaseKind
	if kind == "" {
		kind = album.AlbumType
	}
	if kind == "" || kind == "album" {
		return ""
	}
	return albumTypeLabel(kind)
}
