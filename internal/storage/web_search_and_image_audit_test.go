package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWebSearchSettingsAndBackupProtection(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "ws.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	keys := ParseAPIKeyList("  tvly-key-one-1111 \n tvly-key-two-2222, tvly-key-one-1111 ; \n tvly-key-three-3333 ")
	want := []string{"tvly-key-one-1111", "tvly-key-two-2222", "tvly-key-three-3333"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("ParseAPIKeyList = %v, want %v", keys, want)
	}
	if got := MaskAPIKey("tvly-dev-abcdef1234"); got != "tvly-****1234" {
		t.Fatalf("MaskAPIKey = %q, want tvly-****1234", got)
	}
	if got := MaskAPIKey("short"); got != "****" {
		t.Fatalf("MaskAPIKey(short) = %q, want ****", got)
	}

	if err := s.SaveWebSearchSettings(ctx, WebSearchSettings{
		Provider:    WebSearchProviderTavily,
		TavilyKeys:  keys,
		DeepSeekKey: "ds-live-secret",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.WebSearchSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != WebSearchProviderTavily || !reflect.DeepEqual(got.TavilyKeys, want) || got.DeepSeekKey != "ds-live-secret" {
		t.Fatalf("loaded WebSearchSettings = %+v", got)
	}
	// Legacy alias table should also reflect deepseek_key sync.
	aliasSt, err := s.ArtistAliasSettings(ctx)
	if err != nil || aliasSt.DeepSeekKey != "ds-live-secret" {
		t.Fatalf("synced aliasSt.DeepSeekKey = %q err=%v", aliasSt.DeepSeekKey, err)
	}

	// Backup restore must preserve current instance web_search_settings credentials
	// and never resurrect credentials from an older backup snapshot.
	bs, b := backupFixture(t, 0)
	if err := bs.SaveWebSearchSettings(ctx, WebSearchSettings{
		Provider:    WebSearchProviderTavilyThenDeepSeek,
		TavilyKeys:  []string{"tvly-old-backup-key"},
		DeepSeekKey: "ds-old-backup-key",
	}); err != nil {
		t.Fatal(err)
	}
	name, err := b.Create(ctx, "manual", 0)
	if err != nil {
		t.Fatal(err)
	}
	// Rotate/revoke credentials in live DB before restore.
	if err := bs.SaveWebSearchSettings(ctx, WebSearchSettings{
		Provider:    WebSearchProviderTavily,
		TavilyKeys:  []string{"tvly-live-current-key"},
		DeepSeekKey: "",
	}); err != nil {
		t.Fatal(err)
	}
	stage, _, err := b.Stage(ctx, filepath.Join(b.Directory, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Arm(ctx, stage); err != nil {
		t.Fatal(err)
	}
	bs.Close()
	restored, err := b.PrepareRestore(ctx)
	if err != nil || !restored {
		t.Fatalf("PrepareRestore = (%v,%v)", restored, err)
	}
	reopened, err := Open(b.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.CompleteRestore(ctx, reopened); err != nil {
		t.Fatalf("CompleteRestore: %v", err)
	}
	after, err := reopened.WebSearchSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.TavilyKeys, []string{"tvly-live-current-key"}) || after.DeepSeekKey != "" {
		t.Fatalf("restored web_search_settings leaked backup credentials: %+v", after)
	}
}

func TestWebSearchNativeThenSearchProviderAndReadiness(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "ws-native.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	if got := NormalizeWebSearchProvider(" NATIVE_THEN_SEARCH "); got != WebSearchProviderNativeThenSearch {
		t.Fatalf("NormalizeWebSearchProvider = %q, want %q", got, WebSearchProviderNativeThenSearch)
	}

	if err := s.SaveWebSearchSettings(ctx, WebSearchSettings{
		Provider: WebSearchProviderNativeThenSearch,
	}); err != nil {
		t.Fatalf("SaveWebSearchSettings(native_then_search): %v", err)
	}
	got, err := s.WebSearchSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != WebSearchProviderNativeThenSearch {
		t.Fatalf("loaded provider = %q, want %q", got.Provider, WebSearchProviderNativeThenSearch)
	}

	// Unconfigured global AI and no search keys -> not configured.
	if got.Configured(ArtistAliasSettings{}, AISettings{}) {
		t.Fatal("expected Configured=false when neither global AI nor search keys exist")
	}
	// Configured global AI alone -> ready without Tavily/DeepSeek keys.
	readyAI := AISettings{BaseURL: "https://cli.example/v1", APIKey: "sk-test", Model: "gpt-6-luna"}
	if !got.Configured(ArtistAliasSettings{}, readyAI) {
		t.Fatal("expected Configured=true when global AI is configured under native_then_search")
	}
	// Fallback Tavily key also satisfies Configured even before global AI is set.
	withFallback := WebSearchSettings{Provider: WebSearchProviderNativeThenSearch, TavilyKeys: []string{"tvly-fb-1"}}
	if !withFallback.Configured(ArtistAliasSettings{}, AISettings{}) {
		t.Fatal("expected Configured=true when fallback Tavily key exists under native_then_search")
	}

	// Verify ai_usage accepts web_search_native purpose after migration 096.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ai_usage(day,model,purpose,requests,prompt_tokens,completion_tokens) VALUES('2026-11-28','gpt-6-luna','web_search_native',1,8900,220)`); err != nil {
		t.Fatalf("insert web_search_native ai_usage failed: %v", err)
	}
}

func TestMigration096PopulatedUpgradeAndBackupProtection(t *testing.T) {
	ctx := context.Background()

	// 1. Seed a populated migration-095 DB, upgrade to 096, and verify legacy rows survive intact.
	s, err := Open(filepath.Join(t.TempDir(), "upgrade096.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.MigrateUpTo(ctx, 95); err != nil {
		t.Fatalf("MigrateUpTo(95): %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE web_search_settings SET provider='tavily', tavily_keys_json='["tvly-legacy-1","tvly-legacy-2"]', deepseek_key='ds-legacy-1' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ai_usage(day,model,purpose,requests,prompt_tokens,completion_tokens,cached_tokens,reasoning_tokens) VALUES('2026-11-27','gpt-6-luna','artist_image_audit',3,1200,300,400,50)`); err != nil {
		t.Fatal(err)
	}

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("upgrade 095->096 failed: %v", err)
	}
	ws, err := s.WebSearchSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ws.Provider != WebSearchProviderTavily || !reflect.DeepEqual(ws.TavilyKeys, []string{"tvly-legacy-1", "tvly-legacy-2"}) || ws.DeepSeekKey != "ds-legacy-1" {
		t.Fatalf("migrated web_search_settings = %+v", ws)
	}
	totals, err := s.AIUsageTotals(ctx, "")
	if err != nil || len(totals) != 1 || totals[0].Purpose != "artist_image_audit" || totals[0].Usage.Requests != 3 || totals[0].Usage.CachedTokens != 400 || totals[0].Usage.ReasoningTokens != 50 {
		t.Fatalf("migrated ai_usage = (%+v, %v)", totals, err)
	}
	// New provider and new usage purpose both succeed on upgraded schema.
	ws.Provider = WebSearchProviderNativeThenSearch
	if err := s.SaveWebSearchSettings(ctx, ws); err != nil {
		t.Fatalf("SaveWebSearchSettings(native_then_search) after upgrade: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO ai_usage(day,model,purpose,requests,prompt_tokens,completion_tokens) VALUES('2026-11-28','gpt-6-luna','web_search_native',2,9000,200)`); err != nil {
		t.Fatalf("insert web_search_native after upgrade: %v", err)
	}

	// 2. Backup & restore round-trip with native_then_search protects live AI & fallback credentials.
	bs, b := backupFixture(t, 0)
	if err := bs.SaveAISettings(ctx, AISettings{
		BaseURL: "https://old-ai.example/v1", APIKey: "sk-old-backup-ai", Model: "old-model",
		RequestsPerMinute: 60, Concurrency: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bs.SaveWebSearchSettings(ctx, WebSearchSettings{
		Provider: WebSearchProviderNativeThenSearch, TavilyKeys: []string{"tvly-old-backup"}, DeepSeekKey: "ds-old-backup",
	}); err != nil {
		t.Fatal(err)
	}
	snapName, err := b.Create(ctx, "manual", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := bs.SaveAISettings(ctx, AISettings{
		BaseURL: "https://live-ai.example/v1", APIKey: "sk-live-current-ai", Model: "gpt-6-luna",
		RequestsPerMinute: 60, Concurrency: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bs.SaveWebSearchSettings(ctx, WebSearchSettings{
		Provider: WebSearchProviderNativeThenSearch, TavilyKeys: []string{"tvly-live-current"}, DeepSeekKey: "",
	}); err != nil {
		t.Fatal(err)
	}
	stage, _, err := b.Stage(ctx, filepath.Join(b.Directory, snapName))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Arm(ctx, stage); err != nil {
		t.Fatal(err)
	}
	bs.Close()
	if restored, err := b.PrepareRestore(ctx); err != nil || !restored {
		t.Fatalf("PrepareRestore = (%v, %v)", restored, err)
	}
	reopened, err := Open(b.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.CompleteRestore(ctx, reopened); err != nil {
		t.Fatal(err)
	}
	afterAI, _ := reopened.AISettings(ctx)
	afterWS, _ := reopened.WebSearchSettings(ctx)
	if afterAI.BaseURL != "https://live-ai.example/v1" || afterAI.APIKey != "sk-live-current-ai" {
		t.Fatalf("restored ai_settings leaked backup credentials: %+v", afterAI)
	}
	if afterWS.Provider != WebSearchProviderNativeThenSearch || !reflect.DeepEqual(afterWS.TavilyKeys, []string{"tvly-live-current"}) || afterWS.DeepSeekKey != "" {
		t.Fatalf("restored web_search_settings mismatch: %+v", afterWS)
	}
}

func TestArtistImageAuditReplaceClearRevertAndCacheURLVersioning(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := f.ctx
	dir := t.TempDir()

	writeFile := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	oldPath := writeFile("oldhash123456.jpg", "old-bytes")
	newPath := writeFile("newhash654321.jpg", "new-bytes")
	srcPath := writeFile("srchash999999.jpg", "src-bytes")

	f.importArtistTrack(t, "A/01.flac", "AuditTarget")
	f.importArtistTrack(t, "B/01.flac", "MergedSource")
	f.importArtistTrack(t, "C/01.flac", "ClearTarget")
	targetID := f.artistID(t, "AuditTarget")
	sourceID := f.artistID(t, "MergedSource")
	clearID := f.artistID(t, "ClearTarget")

	// Seed own cache on AuditTarget.
	if err := f.store.SaveArtistImage(ctx, ArtistImageInput{
		ArtistID: targetID, Source: "deezer", RemoteURL: "https://old.example/a.jpg",
		Hash: "oldhash1234567890", MIMEType: "image/jpeg", CachePath: oldPath, ByteSize: 9,
	}); err != nil {
		t.Fatal(err)
	}
	// Verify cache URL includes ?v=hash12.
	urls, err := f.store.ArtistImageURLs(ctx, []int64{targetID})
	if err != nil {
		t.Fatal(err)
	}
	wantOldURL := fmt.Sprintf("/api/v1/artists/%d/image?v=oldhash12345", targetID)
	if urls[targetID] != wantOldURL {
		t.Fatalf("initial cache URL = %q, want %q", urls[targetID], wantOldURL)
	}

	// Start audit_all run and claim AuditTarget.
	runID, err := f.store.CreateArtistImageBackfillRunWithMode(ctx, "audit_all", []ArtistImageBackfillCandidate{
		{ID: targetID, Name: "AuditTarget"},
		{ID: clearID, Name: "ClearTarget"},
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.store.ClaimArtistImageBackfillItem(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	cp := ArtistImageBackfillCheckpoint{RunID: runID, ItemID: item.ID, ClaimToken: item.ClaimToken}

	actionID, err := f.store.ReplaceArtistImageByAudit(ctx, cp, ArtistImageInput{
		ArtistID: targetID, Source: "ai_web", RemoteURL: "https://new.example/b.jpg",
		Hash: "newhash6543210000", MIMEType: "image/jpeg", CachePath: newPath, ByteSize: 9,
	}, ArtistImageAuditActionInput{
		RunID: runID, IssueType: "album_cover", Confidence: 92, Reason: "原图是专辑封面，已替换为肖像", Model: "gpt-6-luna",
	})
	if err != nil || actionID <= 0 {
		t.Fatalf("ReplaceArtistImageByAudit = (%d, %v)", actionID, err)
	}
	if err := f.store.CompleteArtistImageBackfillItem(ctx, cp, "replaced", ""); err != nil {
		t.Fatal(err)
	}

	// Cache URL version must immediately reflect new hash.
	urls, _ = f.store.ArtistImageURLs(ctx, []int64{targetID})
	wantNewURL := fmt.Sprintf("/api/v1/artists/%d/image?v=newhash65432", targetID)
	if urls[targetID] != wantNewURL {
		t.Fatalf("replaced cache URL = %q, want %q", urls[targetID], wantNewURL)
	}
	// Old URL and old hash must both be blacklisted.
	rej, err := f.store.ArtistImageRejections(ctx, targetID)
	if err != nil || !rej.URLs["https://old.example/a.jpg"] || !rej.Hashes["oldhash1234567890"] {
		t.Fatalf("rejections = %+v err=%v", rej, err)
	}
	// New hash must be marked kept so audit_all skips it.
	rev, ok, err := f.store.ArtistImageReviewByArtist(ctx, targetID)
	if err != nil || !ok || rev.ImageHash != "newhash6543210000" || rev.Verdict != "keep" {
		t.Fatalf("review = (%+v,%v,%v)", rev, ok, err)
	}
	// Run counters updated in O(1).
	run, err := f.store.DurableArtistImageBackfillRun(ctx, runID)
	if err != nil || run.Replaced != 1 || run.Processed != 1 {
		t.Fatalf("run = %+v err=%v", run, err)
	}

	// Revert replacement -> restores old cache, clears rejection, pins old hash as manual_revert keep.
	if err := f.store.RevertArtistImageAuditAction(ctx, actionID); err != nil {
		t.Fatalf("RevertArtistImageAuditAction: %v", err)
	}
	urls, _ = f.store.ArtistImageURLs(ctx, []int64{targetID})
	if urls[targetID] != wantOldURL {
		t.Fatalf("reverted cache URL = %q, want %q", urls[targetID], wantOldURL)
	}
	rej, _ = f.store.ArtistImageRejections(ctx, targetID)
	if rej.URLs["https://old.example/a.jpg"] || rej.Hashes["oldhash1234567890"] {
		t.Fatalf("old image still rejected after revert: %+v", rej)
	}
	rev, ok, _ = f.store.ArtistImageReviewByArtist(ctx, targetID)
	if !ok || rev.ImageHash != "oldhash1234567890" || rev.Verdict != "keep" || rev.Model != "manual_revert" {
		t.Fatalf("review after revert = %+v ok=%v", rev, ok)
	}

	// Merge sourceID into clearID and seed inherited cache only on sourceID; clearing clearID must remove inherited cache and revert must restore it.
	if err := f.store.SaveArtistImage(ctx, ArtistImageInput{
		ArtistID: sourceID, Source: "lastfm", RemoteURL: "https://bad.example/logo.jpg",
		Hash: "srchash9999990000", MIMEType: "image/jpeg", CachePath: srcPath, ByteSize: 9,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MergeArtists(ctx, sourceID, clearID); err != nil {
		t.Fatal(err)
	}
	item2, err := f.store.ClaimArtistImageBackfillItem(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	cp2 := ArtistImageBackfillCheckpoint{RunID: runID, ItemID: item2.ID, ClaimToken: item2.ClaimToken}
	clearActionID, err := f.store.ClearArtistImageByAudit(ctx, cp2, clearID, ArtistImageAuditActionInput{
		RunID: runID, IssueType: "garbage", Confidence: 96, Reason: "网站占位 Logo", Model: "gpt-6-luna",
	}, 30)
	if err != nil || clearActionID <= 0 {
		t.Fatalf("ClearArtistImageByAudit = (%d, %v)", clearActionID, err)
	}
	if err := f.store.CompleteArtistImageBackfillItem(ctx, cp2, "cleared", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, _, pathErr := f.store.ArtistImagePath(ctx, clearID); pathErr == nil {
		t.Fatal("expected inherited cache to be cleared too")
	}
	// Revert restores merged source cache row.
	if err := f.store.RevertArtistImageAuditAction(ctx, clearActionID); err != nil {
		t.Fatalf("revert clear action: %v", err)
	}
	gotPath, _, isCustom, pathErr := f.store.ArtistImagePath(ctx, clearID)
	if pathErr != nil || gotPath != srcPath || isCustom {
		t.Fatalf("ArtistImagePath after clear revert = (%q, custom=%v, err=%v)", gotPath, isCustom, pathErr)
	}

	// Reverting must refuse if a custom image was uploaded in the meantime.
	if _, err := f.store.SaveCustomArtistImage(ctx, clearID, CustomImageInput{Hash: "cust1", MIMEType: "image/png", FileName: "c.png", Width: 500, Height: 500, ByteSize: 5}); err != nil {
		t.Fatal(err)
	}
	// Reset reverted flag just to test the custom-image guard directly.
	if _, err := f.store.db.ExecContext(ctx, `UPDATE artist_image_audit_actions SET reverted=0 WHERE id=?`, clearActionID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RevertArtistImageAuditAction(ctx, clearActionID); !errors.Is(err, ErrArtistImageBackfillState) {
		t.Fatalf("expected ErrArtistImageBackfillState when custom image present, got %v", err)
	}
}

func TestArtistImageCandidateSearchStateAndResolveReplacement(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := f.ctx
	f.importArtistTrack(t, "q/1.flac", "AuditQueueArtist")
	artistID := f.artistID(t, "AuditQueueArtist")

	// Seed an existing cached avatar so KeepArtistImageCandidate can pin it.
	imgPath := filepath.Join(t.TempDir(), "old.jpg")
	if err := os.WriteFile(imgPath, []byte("old-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveArtistImage(ctx, ArtistImageInput{
		ArtistID: artistID, Source: "deezer", RemoteURL: "https://old.example/cover.jpg",
		Hash: "oldhash092", MIMEType: "image/jpeg", CachePath: imgPath, ByteSize: 9,
	}); err != nil {
		t.Fatal(err)
	}

	// Upsert an audit_review card marked not_found_auto.
	auditID, err := f.store.UpsertArtistImageCandidate(ctx, ArtistImageCandidate{
		ArtistID:        artistID,
		Source:          "ai_audit",
		ImageURL:        "https://old.example/cover.jpg",
		Confidence:      "high",
		ConfidenceScore: 95,
		Kind:            "audit_review",
		IssueType:       "album_cover",
		Reason:          "专辑封面",
		WebSearchState:  "not_found_auto",
		WebSearchNote:   "全量审查已自动联网找图，未找到合适新图",
	})
	if err != nil || auditID <= 0 {
		t.Fatalf("UpsertArtistImageCandidate = (%d, %v)", auditID, err)
	}
	got, err := f.store.ArtistImageCandidateByID(ctx, auditID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WebSearchState != "not_found_auto" || got.WebSearchNote == "" || got.WebSearchedAt == "" {
		t.Fatalf("initial search state = %+v", got)
	}

	// Update search state to not_found_manual.
	ts, err := f.store.UpdateArtistImageCandidateSearchState(ctx, auditID, "not_found_manual", "手动联网找图未找到更合适的候选头像")
	if err != nil || ts == "" {
		t.Fatalf("UpdateArtistImageCandidateSearchState = (%q, %v)", ts, err)
	}
	got, _ = f.store.ArtistImageCandidateByID(ctx, auditID)
	if got.WebSearchState != "not_found_manual" || got.WebSearchedAt != ts {
		t.Fatalf("updated search state = %+v", got)
	}

	// Atomically resolve audit_review into replacement card.
	newID, err := f.store.ResolveAuditCandidateWithReplacement(ctx, auditID, ArtistImageCandidate{
		Source:          "ai_web",
		ImageURL:        "https://new.example/portrait.jpg",
		PageURL:         "https://artist.example/profile",
		Title:           "Official Profile",
		Confidence:      "high",
		ConfidenceScore: 92,
		Reason:          "清晰本人肖像",
		Width:           640,
		Height:          640,
	})
	if err != nil || newID <= 0 || newID == auditID {
		t.Fatalf("ResolveAuditCandidateWithReplacement = (%d, %v)", newID, err)
	}
	pending, err := f.store.PendingArtistImageCandidates(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != newID {
		t.Fatalf("expected only replacement card pending, got %+v err=%v", pending, err)
	}
	if pending[0].Kind != "replacement" || pending[0].IssueType != "album_cover" {
		t.Fatalf("replacement card metadata = %+v", pending[0])
	}
	oldRow, _ := f.store.ArtistImageCandidateByID(ctx, auditID)
	if oldRow.Status != "rejected" {
		t.Fatalf("originating audit card status = %q, want rejected", oldRow.Status)
	}

	// Keeping existing avatar from a replacement card must blacklist the rejected
	// replacement URL and pin the existing avatar hash as keep.
	if err := f.store.KeepArtistImageCandidate(ctx, newID); err != nil {
		t.Fatalf("KeepArtistImageCandidate on replacement: %v", err)
	}
	rej, err := f.store.ArtistImageRejections(ctx, artistID)
	if err != nil || !rej.URLs["https://new.example/portrait.jpg"] {
		t.Fatalf("expected replacement URL blacklisted after keep, got %+v err=%v", rej, err)
	}
	rev, ok, err := f.store.ArtistImageReviewByArtist(ctx, artistID)
	if err != nil || !ok || rev.ImageHash != "oldhash092" || rev.Verdict != "keep" {
		t.Fatalf("review after keep = (%+v, %v, %v)", rev, ok, err)
	}
}
