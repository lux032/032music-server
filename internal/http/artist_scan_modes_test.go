package httpapi

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/storage"
)

func TestArtistMatchRunIncrementalVsFullEndpoints(t *testing.T) {
	_, s, handler, manager, cookie, csrf := rateLimitTestApp(t)
	ctx := context.Background()
	artists, _ := s.ArtistsForMatching(ctx)
	if len(artists) != 1 {
		t.Fatalf("artists=%+v", artists)
	}
	artistID := artists[0].ID

	mb, err := s.MetadataSourceSetting(ctx, "musicbrainz")
	if err != nil {
		t.Fatal(err)
	}
	mb.Enabled = true
	mb.AutoMatch = true
	if err = s.SaveMetadataSourceSetting(ctx, mb); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArtistSourceCheck(ctx, artists[0], mb, storage.ArtistSourceSnapshot{
		Candidates: []storage.ArtistCandidate{{Source: "musicbrainz", ExternalID: "mb-cand-1", DisplayName: "Cand", Score: 82}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RejectAllArtistCandidates(ctx, artistID, ""); err != nil {
		t.Fatal(err)
	}

	// Incremental scan skips the rejected artist.
	rec := postAdminForm(t, handler, cookie, csrf, "/admin/matches/run", nil)
	if notice := notice303Of(t, rec); !strings.Contains(notice, "没有未初次扫描的艺术家") {
		t.Fatalf("incremental notice=%q", notice)
	}

	// Full scan ("全局补全扫描") starts a full-mode run.
	rec = postAdminForm(t, handler, cookie, csrf, "/admin/matches/run", url.Values{"mode": {"full"}})
	if notice := notice303Of(t, rec); !strings.Contains(notice, "全局补全扫描任务已启动") {
		t.Fatalf("full notice=%q", notice)
	}
	manager.Wait()

	runs, err := s.ListArtistMatchRuns(ctx, 1)
	if err != nil || len(runs) != 1 || runs[0].ScanMode != "full" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	body := adminGetBody(t, handler, cookie, "/admin/matches")
	if !strings.Contains(body, "全局补全") {
		t.Fatalf("matches page missing full scan mode label")
	}
}
