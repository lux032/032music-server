package enrichment

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// Generic relations (其他 / 主题歌) are a review candidate, not a miss, and
// they must not fall through to another recording.
func TestGenericRelationGoesToReviewWithoutTrackLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/search/subjects":
			_, _ = w.Write([]byte(`{"data":[{"id":9001,"type":3,"name":"Generic Song","infobox":[{"key":"艺术家","value":"Singer"}]}]}`))
		case "/v0/subjects/9001/subjects":
			_, _ = w.Write([]byte(`[{"id":91,"type":2,"name":"Other Show","relation":"其他"},{"id":92,"type":2,"name":"Theme Show","relation":"主题歌"}]`))
		case "/v0/subjects/91":
			_, _ = w.Write([]byte(`{"id":91,"type":2,"name":"Other Show","platform":"TV"}`))
		case "/v0/subjects/92":
			_, _ = w.Write([]byte(`{"id":92,"type":2,"name":"Theme Show","platform":"TV"}`))
		case "/v0/subjects/91/subjects":
			_, _ = w.Write([]byte(`[{"id":9001,"type":3,"relation":"其他"}]`))
		case "/v0/subjects/92/subjects":
			_, _ = w.Write([]byte(`[{"id":9001,"type":3,"relation":"主题歌"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager, store, lib := newTrackManager(t, server)
	ctx := context.Background()
	setting, err := store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	setting.AutoMatch = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	target := importTrackSong(t, store, lib, "Generic Album", "Generic Song", "Singer", "tv_size")
	outcome, err := manager.enrichBangumiTrack(ctx, 0, target, false)
	if err != nil || outcome != "review" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := mustCount(t, store, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, target.ID); n != 0 {
		t.Fatalf("wrote %d work_tracks", n)
	}
	candidates, err := store.TrackSubjectCandidates(ctx, target.ID)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "candidate" || candidates[0].ExternalID != "9001" {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
}

func TestTrackAutoMatchOffWritesCandidateOnly(t *testing.T) {
	server := trackFixtureServer(t)
	defer server.Close()
	manager, store, lib := newTrackManager(t, server)
	ctx := context.Background()
	setting, err := store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	setting.AutoMatch = false
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	target := importTrackSong(t, store, lib, "Album", "legendary future", "fripSide", "")
	outcome, err := manager.enrichBangumiTrack(ctx, 0, target, false)
	if err != nil || outcome != "review" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := mustCount(t, store, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, target.ID); n != 0 {
		t.Fatalf("auto wrote %d", n)
	}
	candidates, err := store.TrackSubjectCandidates(ctx, target.ID)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "candidate" {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
}

func TestMultiTitleCandidateKeepsOriginalEntryName(t *testing.T) {
	server := trackFixtureServer(t)
	defer server.Close()
	manager, store, lib := newTrackManager(t, server)
	target := importTrackSong(t, store, lib, "Album", "朝が来る", "Aimer", "")
	outcome, err := manager.enrichBangumiTrack(context.Background(), 0, target, false)
	if err != nil || outcome != "review" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	candidates, err := store.TrackSubjectCandidates(context.Background(), target.ID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	if candidates[0].ExternalID != "350774" || candidates[0].Title != "残響散歌 / 朝が来る" {
		t.Fatalf("candidate=%+v", candidates[0])
	}
	var payload struct {
		Name string `json:"name"`
	}
	if err = json.Unmarshal(candidates[0].Payload, &payload); err != nil || payload.Name != "残響散歌 / 朝が来る" {
		t.Fatalf("payload name=%q err=%v", payload.Name, err)
	}
}

func TestHelloAndLinkCompareSingers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v0/search/subjects" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Keyword string `json:"keyword"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Keyword {
		case "Hello":
			_, _ = w.Write([]byte(`{"data":[{"id":1,"type":3,"name":"Hello","infobox":[{"key":"艺术家","value":"Other Singer"}]},{"id":2,"type":3,"name":"Hello","infobox":[{"key":"艺术家","value":"Third"}]}]}`))
		case "Link":
			_, _ = w.Write([]byte(`{"data":[{"id":3,"type":3,"name":"Link","infobox":[{"key":"艺术家","value":"Not The One"}]}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer server.Close()
	manager, store, lib := newTrackManager(t, server)
	for _, title := range []string{"Hello", "Link"} {
		target := importTrackSong(t, store, lib, "Album "+title, title, "Local Singer", "")
		outcome, err := manager.enrichBangumiTrack(context.Background(), 0, target, false)
		if err != nil || outcome != "skipped" {
			t.Fatalf("%s outcome=%s err=%v", title, outcome, err)
		}
		if n := mustCount(t, store, `SELECT COUNT(*) FROM track_subject_candidates WHERE track_id=?`, target.ID); n != 0 {
			t.Fatalf("%s candidates=%d", title, n)
		}
		if n := mustCount(t, store, `SELECT COUNT(*) FROM track_enrichment_misses WHERE track_id=?`, target.ID); n != 1 {
			t.Fatalf("%s miss=%d", title, n)
		}
	}
}

func TestAlbumSearchKeywordRewritesHyphenButScoreUsesOriginal(t *testing.T) {
	var keyword string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/search/subjects":
			var req struct {
				Keyword string `json:"keyword"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			keyword = req.Keyword
			_, _ = w.Write([]byte(`{"data":[{"id":77,"type":3,"name":"only my railgun -version2020-","date":"2020-01-01","infobox":[{"key":"艺术家","value":"fripSide"}]}]}`))
		case "/v0/subjects/77":
			_, _ = w.Write([]byte(`{"id":77,"type":3,"name":"only my railgun -version2020-","date":"2020-01-01","infobox":[{"key":"艺术家","value":"fripSide"}]}`))
		case "/v0/subjects/77/persons":
			_, _ = w.Write([]byte(`[]`))
		case "/v0/subjects/77/subjects":
			_, _ = w.Write([]byte(`[{"id":1,"type":2,"name":"Railgun","relation":"片头曲"}]`))
		case "/v0/subjects/1":
			_, _ = w.Write([]byte(`{"id":1,"type":2,"name":"Railgun","platform":"TV"}`))
		case "/v0/subjects/1/subjects":
			_, _ = w.Write([]byte(`[{"id":77,"relation":"片头曲"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager, store, lib := newTrackManager(t, server)
	ctx := context.Background()
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib, RelativePath: "Hyphen/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Track", Album: "only my railgun -version2020-", Artists: []string{"fripSide"}, AlbumArtists: []string{"fripSide"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	albums, err := store.AlbumsForBangumiTieup(ctx, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	var album storage.AlbumBangumiTarget
	for _, candidate := range albums {
		if candidate.Title == "only my railgun -version2020-" {
			album = candidate
		}
	}
	if album.ID == 0 {
		t.Fatalf("album not eligible: %+v", albums)
	}
	outcome, err := manager.enrichBangumiAlbum(ctx, 0, album, true)
	if err != nil {
		t.Fatal(err)
	}
	if keyword != "only my railgun version2020" {
		t.Fatalf("keyword=%q", keyword)
	}
	score, _ := musicTitleScore(album.Title, "only my railgun -version2020-")
	candidates, err := store.AlbumSubjectCandidates(ctx, album.ID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("outcome=%s candidates=%+v err=%v", outcome, candidates, err)
	}
	if candidates[0].Score < score || score < 30 {
		t.Fatalf("stored score=%d title score=%d", candidates[0].Score, score)
	}
}
