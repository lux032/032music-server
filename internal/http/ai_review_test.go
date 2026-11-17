package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/aiclient"
	"github.com/lux032/032music-server/internal/aireview"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

type fakeAIReviewChat struct {
	fn func(tagging.ChatRequest) (tagging.ChatResult, error)
}

func (f fakeAIReviewChat) Complete(_ context.Context, req tagging.ChatRequest) (tagging.ChatResult, error) {
	return f.fn(req)
}

func TestAISettingsPageAndSave(t *testing.T) {
	store := credentialTestStore(t)
	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	page := getAdmin(t, handler, cookie, "/admin/settings/ai")
	for _, want := range []string{"AI 设置中心", `name="ai_review_enabled"`, `name="album_mode"`, `name="series_mode"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("ai settings page missing %q", want)
		}
	}

	rec := postSecurity(t, app, handler, cookie, "/admin/settings/ai", url.Values{
		"base_url":          {"https://api.example.test/v1"},
		"api_key":           {"sk-secret"},
		"model":             {"gemini-2.5-flash-lite"},
		"review_model":      {"gemini-2.5-flash"},
		"fallback_model":    {"backup"},
		"rpm":               {"120"},
		"concurrency":       {"4"},
		"ai_review_enabled": {"on"},
		"ai_review_inline":  {"on"},
		"album_mode":        {"auto"},
		"track_mode":        {"recommend"},
		"work_mode":         {"auto"},
		"artist_mode":       {"auto"},
		"credit_mode":       {"recommend"},
		"series_mode":       {"recommend"},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), url.QueryEscape("AI 设置已保存")) {
		t.Fatalf("unexpected save response: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	st, err := store.AISettings(context.Background())
	if err != nil || !st.Configured() || st.ReviewModel != "gemini-2.5-flash" || st.RequestsPerMinute != 120 {
		t.Fatalf("unexpected saved AI settings: %+v err=%v", st, err)
	}
	rev, err := store.AIReviewSettings(context.Background())
	if err != nil || !rev.Enabled || !rev.InlineEnabled || rev.TrackMode != "recommend" {
		t.Fatalf("unexpected saved AI review settings: %+v err=%v", rev, err)
	}
}

func TestWorkReviewAndMatchReviewAIIntegration(t *testing.T) {
	store := credentialTestStore(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	if err := store.ImportTrack(ctx, storage.ImportInput{
		LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 100, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Song A", Album: "Album A", Artists: []string{"Aimer feat. Sawano"}, AlbumArtists: []string{"Aimer"}, DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveAISettings(ctx, storage.AISettings{
		BaseURL: "https://api.example.test/v1", APIKey: "sk-test", Model: "lite", ReviewModel: "pro", RequestsPerMinute: 6000, Concurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}
	rev := storage.DefaultAIReviewSettings()
	rev.Enabled = true
	if err := store.SaveAIReviewSettings(ctx, rev); err != nil {
		t.Fatal(err)
	}

	albums, _ := store.ListAlbums(ctx, storage.Filters{})
	albumID := albums[0].ID
	_ = store.SaveAlbumSubjectCandidates(ctx, albumID, []storage.AlbumSubjectCandidate{{
		ExternalID: "1234", Title: "Album A", Artist: "Aimer", Score: 75,
		Tieups: []storage.BangumiTieup{{SubjectID: 9901, Title: "Anime X", Type: "anime", Role: "op"}},
	}})
	cands, _ := store.AlbumSubjectCandidates(ctx, albumID)

	artists, _ := store.ListArtists(ctx, storage.Filters{})
	var compositeID, normalID int64
	for _, a := range artists {
		if a.Name == "Aimer feat. Sawano" {
			compositeID = a.ID
		} else if a.Name == "Aimer" {
			normalID = a.ID
		}
	}
	_ = store.ReplaceArtistCandidatesForSources(ctx, compositeID, []storage.ArtistCandidate{{
		Source: "musicbrainz", ExternalID: "mb-1", DisplayName: "Aimer", Score: 70,
	}}, []string{"musicbrainz"})
	_ = store.ReplaceArtistCandidatesForSources(ctx, normalID, []storage.ArtistCandidate{{
		Source: "musicbrainz", ExternalID: "mb-aimer", DisplayName: "Aimer", Score: 85,
	}}, []string{"musicbrainz"})

	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	aiSvc := aireview.New(store, slog.New(slog.NewTextHandler(io.Discard, nil)), aireview.Options{
		Limiter: aiclient.NewLimiter(6000),
		NewChat: func(storage.AISettings) (tagging.Chat, error) {
			return fakeAIReviewChat{fn: func(req tagging.ChatRequest) (tagging.ChatResult, error) {
				if strings.Contains(req.System, "composite collaboration credit") {
					return tagging.ChatResult{
						Content: `{"isComposite":true,"parts":["Aimer","Sawano"],"confidence":95,"reason":"包含 feat. 合作署名"}`,
						Usage:   storage.AIUsage{Requests: 1},
					}, nil
				}
				return tagging.ChatResult{
					Content: `{"decision":"select","candidateId":` + strconv.FormatInt(cands[0].ID, 10) + `,"workSubjectIds":[9901],"confidence":93,"needWebSearch":false,"searchQuery":"","quote":"","reason":"标题歌手一致","evidence":[]}`,
					Usage:   storage.AIUsage{Requests: 1},
				}, nil
			}}, nil
		},
	})
	app.SetAIReview(aiSvc)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	// Work review page renders AI toolbar and start button.
	wrBody := getAdmin(t, handler, cookie, "/admin/work-review?tab=albums")
	if !strings.Contains(wrBody, "AI 处理存量待审") || !strings.Contains(wrBody, "/admin/ai-review/run") {
		t.Fatal("work review page missing AI backlog toolbar")
	}

	// Start AI backlog run on albums tab.
	rec := postSecurity(t, app, handler, cookie, "/admin/ai-review/run", url.Values{
		"scope":    {"albums"},
		"returnTo": {"/admin/work-review?tab=albums"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", rec.Code)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw := getAdmin(t, handler, cookie, "/admin/ai-review/runs/active.json?group=works")
		var payload struct {
			Run *storage.AIReviewRun `json:"run"`
		}
		if json.Unmarshal([]byte(raw), &payload) == nil && payload.Run == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Album should now appear in AI recent applied list and revert should restore it.
	wrAfter := getAdmin(t, handler, cookie, "/admin/work-review?tab=albums")
	if !strings.Contains(wrAfter, "AI 最近自动处理记录") || !strings.Contains(wrAfter, "撤销") {
		t.Fatal("expected AI recent applied list with revert button on work-review")
	}
	dec, ok, err := store.AIReviewDecisionByTarget(ctx, storage.AITargetAlbumSubject, albumID)
	if err != nil || !ok || !dec.Applied {
		t.Fatalf("expected applied album decision, ok=%v err=%v dec=%+v", ok, err, dec)
	}
	revRec := postSecurity(t, app, handler, cookie, "/admin/ai-review/decisions/"+strconv.FormatInt(dec.ID, 10)+"/revert", url.Values{
		"returnTo": {"/admin/work-review?tab=albums"},
	})
	if revRec.Code != http.StatusSeeOther {
		t.Fatalf("expected revert redirect, got %d", revRec.Code)
	}

	// Run AI on composite credit split and apply via HTTP endpoint.
	if _, err := aiSvc.EvaluateTarget(ctx, storage.AITargetCreditSplit, compositeID, false); err != nil {
		t.Fatal(err)
	}
	mrBody := getAdmin(t, handler, cookie, "/admin/matches")
	if !strings.Contains(mrBody, "一键应用拆分到关联曲目") || !strings.Contains(mrBody, "Aimer / Sawano") {
		t.Fatal("match review page missing AI credit split recommendation")
	}
	splitRec := postSecurity(t, app, handler, cookie, "/admin/artists/"+strconv.FormatInt(compositeID, 10)+"/apply-credit-split", url.Values{
		"part":     {"Aimer", "Sawano"},
		"returnTo": {"/admin/matches"},
	})
	if splitRec.Code != http.StatusSeeOther || !strings.Contains(splitRec.Header().Get("Location"), url.QueryEscape("更新 1 首曲目")) {
		t.Fatalf("unexpected credit split apply response: code=%d loc=%q", splitRec.Code, splitRec.Header().Get("Location"))
	}
}

func TestMatchReviewSourceLinksReturnToAndRejectAll(t *testing.T) {
	store := credentialTestStore(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	for i, name := range []string{"Aimer", "Sawano"} {
		if err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID: lib.ID, RelativePath: fmt.Sprintf("T%d/01.flac", i), FileSize: 100, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title: "Song", Album: fmt.Sprintf("Album %d", i), Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	artists, _ := store.ListArtists(ctx, storage.Filters{})
	var aimerID, sawanoID int64
	for _, a := range artists {
		switch a.Name {
		case "Aimer":
			aimerID = a.ID
		case "Sawano":
			sawanoID = a.ID
		}
	}
	if err := store.ReplaceArtistCandidatesForSources(ctx, aimerID, []storage.ArtistCandidate{
		{Source: "musicbrainz", ExternalID: "mb-aimer-1", DisplayName: "Aimer (JP)", Score: 90, MBID: "mb-aimer-1"},
		{Source: "lastfm", ExternalID: "lf-aimer", DisplayName: "Aimer", Score: 70, Payload: []byte(`{"pageUrl":"https://www.last.fm/music/Aimer"}`)},
	}, []string{"musicbrainz", "lastfm"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceArtistCandidatesForSources(ctx, sawanoID, []storage.ArtistCandidate{
		{Source: "musicbrainz", ExternalID: "mb-sawano-1", DisplayName: "Hiroyuki Sawano", Score: 85},
		{Source: "lastfm", ExternalID: "lf-sawano", DisplayName: "Sawano", Score: 60},
	}, []string{"musicbrainz", "lastfm"}); err != nil {
		t.Fatal(err)
	}

	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	// /admin/matches renders clickable external source links, reject-all button, and returnTo hidden inputs.
	returnTarget := "/admin/matches?q=Aimer"
	body := getAdmin(t, handler, cookie, returnTarget)
	for _, want := range []string{
		`https://musicbrainz.org/artist/mb-aimer-1`,
		`https://www.last.fm/music/Aimer`,
		`来源主页 ↗`,
		`/admin/artists/` + strconv.FormatInt(aimerID, 10) + `/reject-all`,
		`name="returnTo" value="/admin/matches?q=Aimer"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("match review missing %q", want)
		}
	}

	aimerCands, _ := store.ArtistCandidates(ctx, aimerID)
	var mbCandID, lfCandID int64
	for _, c := range aimerCands {
		if c.Source == "musicbrainz" {
			mbCandID = c.ID
		} else {
			lfCandID = c.ID
		}
	}

	// Rejecting single candidate with returnTo redirects back to /admin/matches?q=Aimer.
	rejRec := postSecurity(t, app, handler, cookie, fmt.Sprintf("/admin/artists/%d/reject/%d", aimerID, lfCandID), url.Values{
		"returnTo": {returnTarget},
	})
	if rejRec.Code != http.StatusSeeOther || !strings.HasPrefix(rejRec.Header().Get("Location"), returnTarget+"&notice=") {
		t.Fatalf("reject redirect = %d %q, want prefix %q", rejRec.Code, rejRec.Header().Get("Location"), returnTarget+"&notice=")
	}

	// Confirming candidate with returnTo redirects back to /admin/matches?q=Aimer.
	confRec := postSecurity(t, app, handler, cookie, fmt.Sprintf("/admin/artists/%d/confirm/%d", aimerID, mbCandID), url.Values{
		"returnTo": {returnTarget},
	})
	if confRec.Code != http.StatusSeeOther || !strings.HasPrefix(confRec.Header().Get("Location"), returnTarget+"&notice=") {
		t.Fatalf("confirm redirect = %d %q, want prefix %q", confRec.Code, confRec.Header().Get("Location"), returnTarget+"&notice=")
	}

	// Confirming/rejecting without returnTo (or unsafe external URL) still redirects to artist profile.
	sawanoCands, _ := store.ArtistCandidates(ctx, sawanoID)
	unsafeRec := postSecurity(t, app, handler, cookie, fmt.Sprintf("/admin/artists/%d/reject/%d", sawanoID, sawanoCands[0].ID), url.Values{
		"returnTo": {"https://evil.example.test/phish"},
	})
	wantArtistLoc := fmt.Sprintf("/admin/artists/%d?view=profile&notice=", sawanoID)
	if unsafeRec.Code != http.StatusSeeOther || !strings.HasPrefix(unsafeRec.Header().Get("Location"), wantArtistLoc) {
		t.Fatalf("unsafe returnTo redirect = %d %q, want prefix %q", unsafeRec.Code, unsafeRec.Header().Get("Location"), wantArtistLoc)
	}

	// Reject-all clears remaining pending candidates for Sawano and honors returnTo.
	allRec := postSecurity(t, app, handler, cookie, fmt.Sprintf("/admin/artists/%d/reject-all", sawanoID), url.Values{
		"returnTo": {"/admin/matches?page=2"},
	})
	if allRec.Code != http.StatusSeeOther || !strings.HasPrefix(allRec.Header().Get("Location"), "/admin/matches?page=2&notice=") {
		t.Fatalf("reject-all redirect = %d %q", allRec.Code, allRec.Header().Get("Location"))
	}
	afterSawano, _ := store.ArtistCandidates(ctx, sawanoID)
	for _, c := range afterSawano {
		if c.Status != "rejected" {
			t.Fatalf("expected all candidates rejected after reject-all, got %+v", afterSawano)
		}
	}
}

func TestMatchReviewIdentityConflictLinksAndDirectMerge(t *testing.T) {
	store := credentialTestStore(t)
	ctx := context.Background()
	if err := store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, "/music")
	for i, name := range []string{"Amane", "Amane Oikawa"} {
		if err := store.ImportTrack(ctx, storage.ImportInput{
			LibraryID: lib.ID, RelativePath: fmt.Sprintf("C%d/01.flac", i), FileSize: 100, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title: fmt.Sprintf("Track %d", i), Album: fmt.Sprintf("Album %d", i), Artists: []string{name}, AlbumArtists: []string{name}, DiscNumber: 1, TrackNumber: 1,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	artists, _ := store.ListArtists(ctx, storage.Filters{})
	var sourceID, targetID int64
	for _, a := range artists {
		switch a.Name {
		case "Amane":
			sourceID = a.ID
		case "Amane Oikawa":
			targetID = a.ID
		}
	}
	if err := store.UpsertExternalArtistProfile(ctx, targetID, storage.ExternalArtistProfile{
		Source: "musicbrainz", ExternalID: "mb-amane-oikawa", DisplayName: "Amane Oikawa",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceArtistCandidatesForSources(ctx, sourceID, []storage.ArtistCandidate{
		{Source: "musicbrainz", ExternalID: "mb-amane-oikawa", DisplayName: "Amane Oikawa", Score: 100},
	}, []string{"musicbrainz"}); err != nil {
		t.Fatal(err)
	}
	cands, _ := store.ArtistCandidates(ctx, sourceID)
	candID := cands[0].ID

	app := newCredentialTestApp(t, credentialTestConfig(t), store)
	handler := app.Handler()
	cookie := mustLogin(t, handler, "admin", testAdminPassword)

	// Target artist name links directly to targetID, alongside direct merge action.
	body := getAdmin(t, handler, cookie, "/admin/matches?q=Amane")
	for _, want := range []string{
		fmt.Sprintf(`href="/admin/artists/%d?view=profile" target="_blank" rel="noopener noreferrer">《Amane Oikawa》</a>`, targetID),
		fmt.Sprintf(`/admin/artists/%d/identity-conflict/%d/merge`, sourceID, candID),
		fmt.Sprintf(`name="expectedTarget" value="%d"`, targetID),
		`将 Amane 并入对方`,
		fmt.Sprintf(`href="/admin/artists/%d?view=profile&amp;identityConflict=%d"`, sourceID, candID),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in match-review body", want)
		}
	}

	// Submitting direct merge from match-review merges Amane -> Amane Oikawa and stays on returnTo.
	rec := postSecurity(t, app, handler, cookie, fmt.Sprintf("/admin/artists/%d/identity-conflict/%d/merge", sourceID, candID), url.Values{
		"expectedTarget": {strconv.FormatInt(targetID, 10)},
		"confirm":        {"1"},
		"returnTo":       {"/admin/matches?q=Amane"},
	})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/admin/matches?q=Amane&notice=") {
		t.Fatalf("direct merge redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	detail, err := store.ArtistDetail(ctx, sourceID)
	if err != nil || detail.MergedIntoID != targetID {
		t.Fatalf("expected source merged into %d, got detail=%+v err=%v", targetID, detail, err)
	}
}
