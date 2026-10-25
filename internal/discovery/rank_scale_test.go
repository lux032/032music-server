package discovery

import (
	"fmt"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func TestWeakMatchesScale(t *testing.T) {
	items := make([]storage.DiscoveryCandidate, 20000)
	for i := range items {
		items[i] = storage.DiscoveryCandidate{DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{RGID: fmt.Sprint(i), Title: fmt.Sprintf("アルバム タイトル (初回限定盤) %d", i%5000)}, SeedArtistID: int64(i % 1200)}
	}
	albums := make([]storage.DiscoveryLocalAlbum, 1800)
	for i := range albums {
		albums[i] = storage.DiscoveryLocalAlbum{ID: int64(i), Title: fmt.Sprintf("アルバム タイトル %d", i), ArtistIDs: []int64{int64(i % 1200)}}
	}
	start := time.Now()
	out := WeakMatches(items, albums, nil)
	// Regression: the old O(catalog×albums) scan took ~45s at this size.
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("WeakMatches took %s for %d matches", d, len(out))
	}
}
