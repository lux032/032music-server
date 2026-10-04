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
