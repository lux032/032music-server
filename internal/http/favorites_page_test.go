package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

// The favorites page paginates each kind server-side: page size, page links
// and the showing range all follow the query parameters.
func TestAdminFavoritesPagination(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := context.Background()
	for i := 1; i <= 30; i++ {
		id := importSoloAlbum(t, app, "PageSinger", fmt.Sprintf("PageAlbum%02d", i), "")
		if err := app.store.SetAlbumFavorite(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}

	pageOne := followLocation(t, app, cookie, "/admin/favorites")
	if pageOne.Code != http.StatusOK {
		t.Fatalf("status = %d", pageOne.Code)
	}
	body := pageOne.Body.String()
	mustContain(t, "page one", body, "显示 1–24 项", "共 30 项", "第 1 / 2 页", "PageAlbum30", `aria-label="分页"`, `name="size"`)
	if strings.Contains(body, "PageAlbum06") {
		t.Fatal("page one shows an album belonging to page two")
	}

	pageTwo := followLocation(t, app, cookie, "/admin/favorites?page=2")
	body = pageTwo.Body.String()
	mustContain(t, "page two", body, "显示 25–30 项", "PageAlbum06")
	if strings.Contains(body, "PageAlbum30") {
		t.Fatal("page two shows an album belonging to page one")
	}

	// A stale page beyond the end clamps to the last page instead of
	// rendering an empty grid.
	clamped := followLocation(t, app, cookie, "/admin/favorites?page=9")
	mustContain(t, "clamped", clamped.Body.String(), "显示 25–30 项")

	// Search filters within the active kind and resets the range.
	filtered := followLocation(t, app, cookie, "/admin/favorites?q=PageAlbum11")
	body = filtered.Body.String()
	mustContain(t, "filtered", body, "匹配 共 1 项", "PageAlbum11")
	if strings.Contains(body, "PageAlbum12") {
		t.Fatal("search returned non-matching albums")
	}

	// Page size and tab survive in pagination and tab URLs.
	sized := followLocation(t, app, cookie, "/admin/favorites?size=48")
	mustContain(t, "sized", sized.Body.String(), "显示 1–30 项", `value="48" selected`)
}

// The tracks tab paginates and searches independently from albums.
func TestAdminFavoritesTracksTab(t *testing.T) {
	app, cookie, _ := csrfSessionApp(t)
	ctx := context.Background()
	albumID := importSoloAlbum(t, app, "TrackTabSinger", "TrackTabAlbum", "")
	tracks, err := app.store.ListTracks(ctx, storage.Filters{AlbumID: albumID, Limit: 50})
	if err != nil || len(tracks) == 0 {
		t.Fatalf("fixture album tracks: %v %v", len(tracks), err)
	}
	for _, track := range tracks {
		if err := app.store.SetTrackFavorite(ctx, track.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	rec := followLocation(t, app, cookie, "/admin/favorites?kind=tracks")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, "tracks tab", body, `id="app-main"`, "data-track-list", "播放本页", "随机播放本页")
	filtered := followLocation(t, app, cookie, "/admin/favorites?kind=tracks&q=不存在的歌")
	mustContain(t, "empty search", filtered.Body.String(), "没有找到匹配的收藏歌曲", "清除搜索")
}
