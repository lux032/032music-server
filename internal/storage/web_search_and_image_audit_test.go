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
