package httpapi

import (
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestGroupArtistReleasesUsesReleaseKind(t *testing.T) {
	albums := []storage.Album{
		{ID: 1, AlbumType: "album", ReleaseKind: "single"}, // 推断为单曲
		{ID: 2, AlbumType: "album", ReleaseKind: "album"},
		{ID: 3, AlbumType: "live", ReleaseKind: "live"},
		{ID: 4, AlbumType: "compilation", ReleaseKind: "compilation"},
		{ID: 5, AlbumType: "ep", ReleaseKind: "ep"},
		{ID: 6, AlbumType: "bootleg", ReleaseKind: "bootleg"},
		{ID: 7, AlbumType: "single"}, // ReleaseKind 缺省时回退 AlbumType
	}
	groups := groupArtistReleases(albums)
	want := map[string][]int64{
		"专辑":     {2},
		"单曲与 EP": {1, 5, 7},
		"合辑与现场":  {3, 4},
		"其他发行":   {6},
	}
	order := []string{"专辑", "单曲与 EP", "合辑与现场", "其他发行"}
	if len(groups) != len(order) {
		t.Fatalf("groups = %d, want %d", len(groups), len(order))
	}
	for i, g := range groups {
		if g.Title != order[i] {
			t.Fatalf("group %d = %q, want %q", i, g.Title, order[i])
		}
		ids := make([]int64, 0, len(g.Releases))
		for _, r := range g.Releases {
			ids = append(ids, r.ID)
		}
		if len(ids) != len(want[g.Title]) {
			t.Fatalf("%s = %v, want %v", g.Title, ids, want[g.Title])
		}
		for j := range ids {
			if ids[j] != want[g.Title][j] {
				t.Fatalf("%s = %v, want %v", g.Title, ids, want[g.Title])
			}
		}
	}
}
