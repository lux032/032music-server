package enrichment

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func TestRVM4SingleTagIsEvidenceOnlyAndDoesNotIncreaseScore(t *testing.T) {
	local := storage.AlbumBangumiTarget{Title: "Single", Artists: []string{"Singer"}, TrackCount: 1}
	remote := musicSubject{Name: "Single"}
	remote.MetaTags = []string{"单曲", "OP"}
	remote.Infobox = []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}{{Key: "艺术家", Value: []byte(`"Singer"`)}}
	score, evidence, matched := scoreMusicSubject(local, remote, nil)
	if score != 70 || !matched || !strings.Contains(strings.Join(evidence, " "), "Bangumi 标签") {
		t.Fatalf("score=%d matched=%v evidence=%v", score, matched, evidence)
	}
}

func TestRVM1RecheckForcesSearchAndSubjectCacheRefresh(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "recheck.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	setting, _ := store.MetadataSourceSetting(ctx, "bangumi")
	setting.Enabled = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/search/subjects":
			io.WriteString(w, `{"data":[{"id":1,"type":3,"name":"Fresh"}]}`)
		case "/v0/subjects/1":
			io.WriteString(w, `{"id":1,"type":3,"name":"Fresh","infobox":[{"key":"艺术家","value":"Singer"}]}`)
		case "/v0/subjects/1/persons", "/v0/subjects/1/subjects":
			io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := New(ctx, store, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	m.phaseEndpoints.BangumiAPI = server.URL
	m.client = server.Client()
	m.bangumiInterval = 0
	album := storage.AlbumBangumiTarget{ID: 1, Title: "Fresh", Artists: []string{"Singer"}, Recheck: true}
	_, _ = m.enrichBangumiAlbum(ctx, 0, album, false)
	_, _ = m.enrichBangumiAlbum(ctx, 0, album, false)
	if counts["/v0/search/subjects"] != 2 || counts["/v0/subjects/1"] != 2 {
		t.Fatalf("request counts=%v", counts)
	}
}

func TestH2MusicTitleAndDateScores(t *testing.T) {
	local := storage.AlbumBangumiTarget{Title: "Amore (初回限定盤)", ReleaseDate: "2026-07-02", Artists: []string{"ReoNa"}, TrackCount: 5}
	remote := musicSubject{Name: "Amore", Date: "2026-07-22"}
	remote.Infobox = []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}{{Key: "艺术家", Value: []byte(`[{"v":"ReoNa"}]`)}}
	score, _, matched := scoreMusicSubject(local, remote, nil)
	if score != 70 || !matched {
		t.Fatalf("score=%d matched=%v", score, matched)
	}
	remote.Date = "2024-07-22"
	score, _, _ = scoreMusicSubject(local, remote, nil)
	if score != 40 {
		t.Fatalf("old date score=%d", score)
	}
	if peopleOverlap([]string{"長谷川育美"}, []string{"結束バンド(Vo.喜多郁代/長谷川育美)"}) != 1 {
		t.Fatal("CV artist token lost")
	}
}

func TestB1H1AmoreAlbumAutomaticallyConfirmsOnlyOP(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(filepath.Join(t.TempDir(), "album.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := db.LibraryByRoot(ctx, "/music")
	err = db.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "Amore/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Amore", Album: "Amore", Artists: []string{"ReoNa"}, AlbumArtists: []string{"ReoNa"}, DiscNumber: 1, TrackNumber: 1, TrackType: "tv_size", Year: 2026, Raw: map[string][]string{"DATE": {"2026-07-22"}}}})
	if err != nil {
		t.Fatal(err)
	}
	albums, err := db.AlbumsForBangumiTieup(ctx, false, 0)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums=%+v: %v", albums, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/search/subjects":
			io.WriteString(w, `{"data":[{"id":507031,"type":3,"name":"amore"},{"id":660542,"type":3,"name":"Amore"}]}`)
		case "/v0/subjects/507031":
			io.WriteString(w, `{"id":507031,"name":"amore","date":"2024-01-01","infobox":[{"key":"艺术家","value":"Other"}]}`)
		case "/v0/subjects/660542":
			io.WriteString(w, `{"id":660542,"name":"Amore","date":"2026-07-22","infobox":[{"key":"艺术家","value":"ReoNa"}]}`)
		case "/v0/subjects/660542/subjects":
			io.WriteString(w, `[{"id":541285,"type":2,"name":"きみが死ぬまで恋をしたい","platform":"TV"},{"id":512098,"type":6,"name":"concert"}]`)
		case "/v0/subjects/541285/subjects":
			io.WriteString(w, `[{"id":660542,"type":3,"relation":"片头曲"}]`)
		case "/v0/subjects/507031/subjects", "/v0/subjects/507031/persons", "/v0/subjects/660542/persons":
			io.WriteString(w, `[]`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := New(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	manager.phaseEndpoints.BangumiAPI = server.URL
	manager.bangumiInterval = 0
	manager.client = server.Client()
	setting, err := db.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	if err = db.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	outcome, err := manager.enrichBangumiAlbum(ctx, 0, albums[0], false)
	if err != nil || outcome != "succeeded" {
		t.Fatalf("outcome %s: %v", outcome, err)
	}
	candidates, err := db.AlbumSubjectCandidates(ctx, albums[0].ID)
	if err != nil || len(candidates) != 1 || candidates[0].ExternalID != "660542" {
		t.Fatalf("candidates=%+v: %v", candidates, err)
	}
	works, err := db.ListWorks(ctx, storage.WorkFilters{})
	if err != nil || len(works) != 1 || works[0].Type != "anime" || works[0].Title != "きみが死ぬまで恋をしたい" {
		t.Fatalf("works=%+v: %v", works, err)
	}
	links, err := db.AlbumsForWork(ctx, works[0].ID)
	if err != nil || len(links) != 1 || links[0].Role != "op" || links[0].Source != "bangumi" {
		t.Fatalf("links=%+v: %v", links, err)
	}
	tracks, err := db.TracksForWork(ctx, works[0].ID)
	if err != nil || len(tracks) != 1 || tracks[0].Role != "op" || tracks[0].Source != "bangumi" {
		t.Fatalf("tracks=%+v: %v", tracks, err)
	}
}

func TestB1H2MusicTieupsOnlyAnimeGameAndSpecificRole(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/subjects/328609/subjects":
			io.WriteString(w, `[{"id":406604,"type":2,"platform":"劇場版","name":"Movie"},{"id":375293,"type":4,"name":"Game"},{"id":512098,"type":6,"name":"Concert"},{"id":11,"type":1,"name":"Book"}]`)
		case "/v0/subjects/406604/subjects":
			io.WriteString(w, `[{"id":328609,"relation":"主题歌"}]`)
		case "/v0/subjects/375293/subjects":
			io.WriteString(w, `[{"id":328609,"relation":"角色歌"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := &Manager{phaseEndpoints: phase4Endpoints{BangumiAPI: server.URL}, client: server.Client(), bangumiInterval: 0, baseCtx: context.Background()}
	store, err := storage.Open(filepath.Join(t.TempDir(), "relations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.store = store
	ties, err := m.musicTieups(context.Background(), storage.MetadataSourceSetting{CacheDays: 1}, 328609, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ties) != 2 || ties[0].Type != "movie" || ties[0].Role != "theme" || ties[1].Type != "game" || ties[1].Role != "character" {
		t.Fatalf("ties=%+v", ties)
	}
}

func TestB4VersionSuffixAndUnknownTitle(t *testing.T) {
	for _, title := range []string{"Amore (初回盤) (CD付)", "Amore - Single", "Amore [Limited Edition]"} {
		if score, _ := musicTitleScore(title, "Amore"); score != 30 {
			t.Errorf("%q score=%d", title, score)
		}
	}
	if score, _ := musicTitleScore("Amore", "different"); score != 0 {
		t.Errorf("unknown score=%d", score)
	}
}

func TestH1ArtistMismatchAndMultipleSpecificRemainReview(t *testing.T) {
	local := storage.AlbumBangumiTarget{Title: "Amore", ReleaseDate: "2026-07-22", Artists: []string{"ReoNa"}, TrackCount: 2}
	remote := musicSubject{Name: "Amore", Date: "2026-07-22"}
	remote.Infobox = []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}{{Key: "艺术家", Value: []byte(`"Other"`)}}
	score, _, matched := scoreMusicSubject(local, remote, nil)
	if score != 55 || matched {
		t.Fatalf("mismatch score=%d matched=%t", score, matched)
	}
	if isSpecificRoleCount([]storage.BangumiTieup{{Role: "op"}, {Role: "ed"}}) == 1 {
		t.Fatal("multiple concrete works must enter review")
	}
}
func isSpecificRoleCount(ties []storage.BangumiTieup) int {
	n := 0
	for _, tie := range ties {
		switch tie.Role {
		case "op", "ed", "insert", "ost", "character":
			n++
		}
	}
	return n
}

func TestF1H6BusyConfirmationRetriesAndSucceeds(t *testing.T) {
	manager, store, albumID, _ := phase4TestManager(t, http.NotFoundHandler())
	calls := 0
	manager.confirmAlbumSubject = func(ctx context.Context, id, candidate, run int64, fingerprint string, automatic bool, selected []int64) (string, []int64, error) {
		calls++
		if calls <= 2 {
			return "", nil, errBangumiConfirmBusy
		}
		return "succeeded", []int64{1}, nil
	}
	outcome, ids, err := manager.confirmMusicWithRetry(context.Background(), 0, storage.AlbumBangumiTarget{ID: albumID}, 1, []int64{1})
	if err != nil || outcome != "succeeded" || calls != 3 || len(ids) != 1 {
		t.Fatalf("outcome=%s calls=%d ids=%v err=%v", outcome, calls, ids, err)
	}
	_ = store
}

func TestF1H6BusyAlbumsDoNotTripConsecutiveFailureCircuit(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "busy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	for i := 0; i < maxConsecutiveFailures; i++ {
		title := fmt.Sprintf("Busy %d", i)
		err = store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: fmt.Sprintf("%d/01.flac", i), FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: title, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, TrackType: "tv_size", Year: 2026, Raw: map[string][]string{"DATE": {"2026-01-01"}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v0/search/subjects":
			var request struct {
				Keyword string `json:"keyword"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			var index int
			fmt.Sscanf(request.Keyword, "Busy %d", &index)
			fmt.Fprintf(w, `{"data":[{"id":%d,"type":3,"name":%q}]}`, 660542+index, request.Keyword)
		case strings.HasSuffix(r.URL.Path, "/persons"):
			io.WriteString(w, `[]`)
		case r.URL.Path == "/v0/subjects/541285/subjects":
			var relations []string
			for i := 0; i < maxConsecutiveFailures; i++ {
				relations = append(relations, fmt.Sprintf(`{"id":%d,"relation":"片头曲"}`, 660542+i))
			}
			fmt.Fprintf(w, "[%s]", strings.Join(relations, ","))
		case strings.HasSuffix(r.URL.Path, "/subjects"):
			io.WriteString(w, `[{"id":541285,"type":2,"name":"Anime"}]`)
		case strings.HasPrefix(r.URL.Path, "/v0/subjects/"):
			var id int
			fmt.Sscanf(r.URL.Path, "/v0/subjects/%d", &id)
			fmt.Fprintf(w, `{"id":%d,"name":"Busy %d","date":"2026-01-01","infobox":[{"key":"艺术家","value":"Singer"}]}`, id, id-660542)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	setting, err := store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		t.Fatal(err)
	}
	setting.Enabled = true
	setting.AutoMatch = true
	if err = store.SaveMetadataSourceSetting(ctx, setting); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := New(ctx, store, logger, t.TempDir())
	manager.client = server.Client()
	manager.phaseEndpoints.BangumiAPI = server.URL
	manager.bangumiInterval = 0
	calls := 0
	manager.confirmAlbumSubject = func(context.Context, int64, int64, int64, string, bool, []int64) (string, []int64, error) {
		calls++
		return "", nil, errBangumiConfirmBusy
	}
	run, err := store.CreateEnrichmentRun(ctx, "albums", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	manager.executePhase4Run(ctx, run.ID, RunRequest{Scope: "albums"})
	finished, err := store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "completed" || finished.Failed != 0 || finished.Skipped != maxConsecutiveFailures || calls != maxConsecutiveFailures*3 {
		t.Fatalf("run=%+v confirmCalls=%d", finished, calls)
	}
}
