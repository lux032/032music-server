package discovery

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func TestRankExplorePrecomputedSortParityAndScale(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	const artistCount = 400
	const rgPerArtist = 15
	input := make([]storage.DiscoveryCandidate, 0, artistCount*rgPerArtist)
	signals := make(map[int64]storage.DiscoverySignals, artistCount)
	kinds := []string{"Album", "OST", "EP", "Single"}
	dates := []string{"2025-04-01", "2024-10-01", "2019-06-15", ""}
	for a := int64(1); a <= artistCount; a++ {
		signals[a] = storage.DiscoverySignals{
			OwnedAlbums: int(a % 4),
			FavArtist:   a <= 40,
		}
		for j := 0; j < rgPerArtist; j++ {
			input = append(input, storage.DiscoveryCandidate{
				DiscoveryReleaseGroup: storage.DiscoveryReleaseGroup{
					RGID:             fmt.Sprintf("%08d-0000-0000-0000-%012d", a, j),
					Title:            fmt.Sprintf("Release %d-%d", a, j),
					Kind:             kinds[(int(a)+j)%len(kinds)],
					FirstReleaseDate: dates[(int(a)+j)%len(dates)],
				},
				SeedArtistID: a,
				Artist:       fmt.Sprintf("Artist %d", a),
			})
		}
	}

	start := time.Now()
	out := Rank(input, signals, nil, nil, now)
	dur := time.Since(start)
	t.Logf("Rank %d candidates in %s", len(input), dur)
	if dur > 2*time.Second {
		t.Fatalf("Rank too slow: %s", dur)
	}

	// 验证 explore 槽位（rank 5, 10, 15, ...）严格符合原始 SHA-256 比较器顺序
	var exploreRanked []storage.DiscoveryCandidate
	for _, c := range out {
		if c.Rank > 0 && c.Lane == "explore" {
			exploreRanked = append(exploreRanked, c)
		}
	}
	if len(exploreRanked) != 12 {
		t.Fatalf("explore ranked = %d, want 12", len(exploreRanked))
	}
	sort.Slice(exploreRanked, func(i, j int) bool { return exploreRanked[i].Rank < exploreRanked[j].Rank })
	for i := 1; i < len(exploreRanked); i++ {
		a, b := exploreRanked[i-1], exploreRanked[i]
		wa := kindWeight(a.Kind) * recency(a.FirstReleaseDate, now)
		wb := kindWeight(b.Kind) * recency(b.FirstReleaseDate, now)
		if wa < wb {
			t.Fatalf("explore rank %d weight %v < rank %d weight %v", a.Rank, wa, b.Rank, wb)
		}
		if wa == wb {
			ha := sha256.Sum256([]byte(a.RGID + now.Format("2006-01-02")))
			hb := sha256.Sum256([]byte(b.RGID + now.Format("2006-01-02")))
			if string(ha[:]) >= string(hb[:]) {
				t.Fatalf("explore hash order mismatch between rank %d and %d", a.Rank, b.Rank)
			}
		}
	}
}
