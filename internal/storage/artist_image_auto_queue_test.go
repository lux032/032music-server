package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestMigration093ArtistImageAutoQueue(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "upgrade093.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.MigrateUpTo(ctx, 92); err != nil {
		t.Fatalf("migrate up to 92: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("upgrade 092 -> 093: %v", err)
	}
	st, err := s.ArtistImageSettings(ctx)
	if err != nil {
		t.Fatalf("ArtistImageSettings: %v", err)
	}
	if !st.AutoIngestAIEnabled {
		t.Fatal("expected AutoIngestAIEnabled default true after migration 093")
	}
	st.AutoIngestAIEnabled = false
	if err := s.SaveArtistImageSettings(ctx, st); err != nil {
		t.Fatalf("SaveArtistImageSettings: %v", err)
	}
	st2, err := s.ArtistImageSettings(ctx)
	if err != nil || st2.AutoIngestAIEnabled {
		t.Fatalf("expected AutoIngestAIEnabled=false after save, got %+v err=%v", st2, err)
	}
}

func TestArtistImageAutoQueueLifecycleAndGuards(t *testing.T) {
	f := newBackfillFixture(t)
	ctx := f.ctx

	f.importArtistTrack(t, "aq/1.flac", "MBArtist")
	f.importArtistTrack(t, "aq/2.flac", "NoMBArtist")
	f.importArtistTrack(t, "aq/3.flac", "CustomMBArtist")
	mbID := f.artistID(t, "MBArtist")
	noMBID := f.artistID(t, "NoMBArtist")
	customID := f.artistID(t, "CustomMBArtist")

	bindMB := func(aid int64, mbid string) {
		if err := f.store.UpsertExternalArtistProfile(ctx, aid, ExternalArtistProfile{
			Source: "musicbrainz", ExternalID: mbid,
		}); err != nil {
			t.Fatal(err)
		}
	}
	bindMB(mbID, "mbid-0001")
	bindMB(customID, "mbid-0003")
	if _, err := f.store.SaveCustomArtistImage(ctx, customID, CustomImageInput{
		Hash: "custhash093", MIMEType: "image/png", FileName: "c.png", Width: 500, Height: 500, ByteSize: 5,
	}); err != nil {
		t.Fatal(err)
	}

	// Only MB-confirmed non-custom artist should enqueue.
	ok, err := f.store.EnqueueArtistImageAutoQueue(ctx, noMBID, "auto_match")
	if err != nil || ok {
		t.Fatalf("expected NoMBArtist rejected, got (%v, %v)", ok, err)
	}
	ok, err = f.store.EnqueueArtistImageAutoQueue(ctx, customID, "auto_match")
	if err != nil || ok {
		t.Fatalf("expected CustomMBArtist rejected, got (%v, %v)", ok, err)
	}
	ok, err = f.store.EnqueueArtistImageAutoQueue(ctx, mbID, "manual_confirm")
	if err != nil || !ok {
		t.Fatalf("expected MBArtist enqueued, got (%v, %v)", ok, err)
	}

	stats, err := f.store.ArtistImageAutoQueueStats(ctx)
	if err != nil || stats.Pending != 1 || stats.Running != 0 || stats.Failed != 0 {
		t.Fatalf("stats after enqueue = %+v err=%v", stats, err)
	}

	// Claim item -> transitions to running; second claim returns sql.ErrNoRows.
	claimed, err := f.store.ClaimNextArtistImageAutoQueueItem(ctx)
	if err != nil || claimed.ArtistID != mbID || claimed.TriggerSource != "manual_confirm" || claimed.Status != "running" {
		t.Fatalf("claimed = (%+v, %v)", claimed, err)
	}
	if _, err := f.store.ClaimNextArtistImageAutoQueueItem(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows when queue empty, got %v", err)
	}

	// Crash recovery resets running -> pending.
	recovered, err := f.store.RecoverArtistImageAutoQueue(ctx)
	if err != nil || recovered != 1 {
		t.Fatalf("RecoverArtistImageAutoQueue = (%d, %v)", recovered, err)
	}
	claimed, err = f.store.ClaimNextArtistImageAutoQueueItem(ctx)
	if err != nil || claimed.ArtistID != mbID {
		t.Fatalf("re-claim after recover = (%+v, %v)", claimed, err)
	}

	// Retry backoff: attempts 1 and 2 schedule future retry; attempt 3 marks failed.
	failed, err := f.store.RetryArtistImageAutoQueueItem(ctx, mbID, 60*time.Second, 3, "timeout 1")
	if err != nil || failed {
		t.Fatalf("retry #1 = (%v, %v)", failed, err)
	}
	stats, _ = f.store.ArtistImageAutoQueueStats(ctx)
	if stats.Pending != 1 || stats.NextRetryAt == "" {
		t.Fatalf("stats with future retry = %+v", stats)
	}
	// Future next_retry_at prevents immediate claim.
	if _, err := f.store.ClaimNextArtistImageAutoQueueItem(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected ErrNoRows while retry is in future, got %v", err)
	}
	failed, err = f.store.RetryArtistImageAutoQueueItem(ctx, mbID, time.Second, 3, "timeout 2")
	if err != nil || failed {
		t.Fatalf("retry #2 = (%v, %v)", failed, err)
	}
	failed, err = f.store.RetryArtistImageAutoQueueItem(ctx, mbID, time.Second, 3, "timeout 3")
	if err != nil || !failed {
		t.Fatalf("retry #3 should mark failed, got (%v, %v)", failed, err)
	}
	stats, _ = f.store.ArtistImageAutoQueueStats(ctx)
	if stats.Failed != 1 || stats.Pending != 0 {
		t.Fatalf("stats after 3rd failure = %+v", stats)
	}

	// Re-enqueue resets failed item back to pending; Complete deletes it.
	ok, err = f.store.EnqueueArtistImageAutoQueue(ctx, mbID, "manual_confirm")
	if err != nil || !ok {
		t.Fatalf("re-enqueue failed item = (%v, %v)", ok, err)
	}
	if err := f.store.CompleteArtistImageAutoQueueItem(ctx, mbID); err != nil {
		t.Fatalf("CompleteArtistImageAutoQueueItem: %v", err)
	}
	stats, _ = f.store.ArtistImageAutoQueueStats(ctx)
	if stats.Pending != 0 || stats.Running != 0 || stats.Failed != 0 {
		t.Fatalf("stats after complete = %+v", stats)
	}
}
