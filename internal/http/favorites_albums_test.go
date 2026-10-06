package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The favorites album grid reuses the shared album card, so it offers the
// same play / queue / select-for-merge-or-delete actions as the album browser,
// and bulk actions return to the favorites page.
func TestAdminFavoritesAlbumsUseSharedCardActions(t *testing.T) {
	app, cookie, csrf := csrfSessionApp(t)
	ctx := context.Background()
	alpha := importSoloAlbum(t, app, "FavSinger", "FavAlpha", "")
	beta := importSoloAlbum(t, app, "FavSinger", "FavBeta", "")
	for _, id := range []int64{alpha, beta} {
		if err := app.store.SetAlbumFavorite(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}

	rec := followLocation(t, app, cookie, "/admin/favorites")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	for _, want := range []string{
		`data-album-id="` + strconv.FormatInt(alpha, 10) + `"`,
		`class="play-disc album-queue-action" data-mode="play"`,
		`data-mode="next"`,
		`data-mode="append"`,
		`class="album-select"`,
		`class="album-selection-bar"`,
		`data-album-bulk="merge"`,
		`data-album-bulk="delete"`,
		`name="returnTo" value="/admin/favorites"`,
		`class="album-fav is-favorite" aria-pressed="true"`,
		`#icon-heart-fill`,
		`取消收藏`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("favorites page missing %q", want)
		}
	}

	merge := postAlbumBulkReturnTo(t, app, cookie, "/admin/albums/merge", csrf, "/admin/favorites", alpha, beta)
	if merge.Code != http.StatusSeeOther || !strings.HasPrefix(merge.Header().Get("Location"), "/admin/favorites?") {
		t.Fatalf("merge status = %d, Location = %q", merge.Code, merge.Header().Get("Location"))
	}
	del := postAlbumBulkReturnTo(t, app, cookie, "/admin/albums/delete", csrf, "/admin/favorites", alpha)
	if del.Code != http.StatusSeeOther || !strings.HasPrefix(del.Header().Get("Location"), "/admin/favorites?") {
		t.Fatalf("delete status = %d, Location = %q", del.Code, del.Header().Get("Location"))
	}
	if follow := followLocation(t, app, cookie, del.Header().Get("Location")); follow.Code != http.StatusOK || !strings.Contains(follow.Body.String(), "还没有收藏专辑") {
		t.Fatalf("followed redirect status = %d", follow.Code)
	}
}

// With no favorite albums there is nothing to select, so no bulk bar.
func TestAdminFavoritesNoAlbumsHidesSelectionBar(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	rec := followLocation(t, app, cookie, "/admin/favorites")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `class="album-selection-bar"`) {
		t.Fatalf("status = %d, selection bar should be absent", rec.Code)
	}
}

// The album browser shares the card, so un-favorited albums get an outline
// heart that posts favorite=1 back to the current page.
func TestAdminAlbumsCardHasFavoriteHeart(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	id := importSoloAlbum(t, app, "HeartSinger", "HeartAlbum", "")
	rec := followLocation(t, app, cookie, "/admin/albums")
	body := rec.Body.String()
	form := `<form class="album-fav-form" method="post" action="/admin/favorites/albums/` + strconv.FormatInt(id, 10) + `">`
	if rec.Code != http.StatusOK || !strings.Contains(body, form) || !strings.Contains(body, `class="album-fav" aria-pressed="false"`) || !strings.Contains(body, `#icon-heart"`) {
		t.Fatalf("status = %d, album browser missing outline heart", rec.Code)
	}
}
