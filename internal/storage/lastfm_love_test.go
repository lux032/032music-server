package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// loveTestStore opens a migrated store with one library and one track.
func loveTestStore(t *testing.T) (*Store, context.Context, int64) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "love.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx, seedScrobbleTrack(t, store, ctx, "love.flac", 200000)
}

func connectLove(t *testing.T, store *Store, ctx context.Context) {
	t.Helper()
	if err := store.SaveMetadataSourceSetting(ctx, MetadataSourceSetting{Source: "lastfm", APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLastFMScrobblePreferences(ctx, true, true, "secret"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLastFMSession(ctx, "listener", "SK"); err != nil {
		t.Fatal(err)
	}
}

func loveQueue(t *testing.T, store *Store, ctx context.Context) []LastFMLoveItem {
	t.Helper()
	items, err := store.DueLastFMLoves(ctx, time.Now().Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// A favourite only queues a love change while love sync is enabled and a
// session exists; the local favourite is saved either way.
func TestSetTrackFavoriteQueuesLoveOnlyWhenReady(t *testing.T) {
	store, ctx, trackID := loveTestStore(t)

	// Nothing configured: the favourite is still saved, nothing is queued.
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	if items := loveQueue(t, store, ctx); len(items) != 0 {
		t.Fatalf("queued without a session: %+v", items)
	}
	var favorite int
	if err := store.db.QueryRowContext(ctx, `SELECT is_favorite FROM tracks WHERE id=?`, trackID).Scan(&favorite); err != nil || favorite != 1 {
		t.Fatalf("favourite = %d %v", favorite, err)
	}

	// Connected but the switch is off: still nothing.
	connectLove(t, store, ctx)
	if err := store.SetTrackFavorite(ctx, trackID, false); err != nil {
		t.Fatal(err)
	}
	if items := loveQueue(t, store, ctx); len(items) != 0 {
		t.Fatalf("queued with the switch off: %+v", items)
	}

	// Switch on: the change is queued in the same transaction.
	if err := store.SetLastFMLoveSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	items := loveQueue(t, store, ctx)
	if len(items) != 1 || !items[0].Desired || items[0].TrackID != trackID {
		t.Fatalf("queue: %+v", items)
	}
	if items[0].Artist == "" || items[0].Track == "" {
		t.Fatalf("metadata missing: %+v", items[0])
	}

	// Toggling again collapses into the newest desired state.
	if err := store.SetTrackFavorite(ctx, trackID, false); err != nil {
		t.Fatal(err)
	}
	items = loveQueue(t, store, ctx)
	if len(items) != 1 || items[0].Desired {
		t.Fatalf("after unfavourite: %+v", items)
	}
}

// A completed submission drops the row; a toggle that happened while the
// request was in flight keeps the newer desired state.
func TestCompleteLastFMLoveKeepsNewerState(t *testing.T) {
	store, ctx, trackID := loveTestStore(t)
	connectLove(t, store, ctx)
	if err := store.SetLastFMLoveSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	// The user unfavourites before the love request completes.
	if err := store.SetTrackFavorite(ctx, trackID, false); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteLastFMLove(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	items := loveQueue(t, store, ctx)
	if len(items) != 1 || items[0].Desired {
		t.Fatalf("newer desired state lost: %+v", items)
	}
	if err := store.CompleteLastFMLove(ctx, trackID, false); err != nil {
		t.Fatal(err)
	}
	if items = loveQueue(t, store, ctx); len(items) != 0 {
		t.Fatalf("row not removed: %+v", items)
	}
}

func TestLoveBackfillCursorAndProgress(t *testing.T) {
	store, ctx, first := loveTestStore(t)
	second := seedScrobbleTrack(t, store, ctx, "love-2.flac", 200000)
	for _, id := range []int64{first, second} {
		if err := store.SetTrackFavorite(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	total, err := store.CountLoveBackfillTracks(ctx)
	if err != nil || total != 2 {
		t.Fatalf("total = %d %v", total, err)
	}
	if err := store.BeginLastFMLoveBackfill(ctx, total); err != nil {
		t.Fatal(err)
	}
	run, err := store.LastFMLoveBackfill(ctx)
	if err != nil || run.State != LoveBackfillRunning || run.Total != 2 || run.Percent() != 0 {
		t.Fatalf("begin: %+v %v", run, err)
	}

	next, err := store.NextLoveBackfillTrack(ctx, 0)
	if err != nil || next != first {
		t.Fatalf("first track = %d %v", next, err)
	}
	if err = store.AdvanceLastFMLoveBackfill(ctx, next, 0); err != nil {
		t.Fatal(err)
	}
	if run, _ = store.LastFMLoveBackfill(ctx); run.Processed != 1 || run.Percent() != 50 {
		t.Fatalf("after one: %+v", run)
	}

	// Unfavouriting during the run removes the track from the walk.
	if err = store.SetTrackFavorite(ctx, second, false); err != nil {
		t.Fatal(err)
	}
	if next, err = store.NextLoveBackfillTrack(ctx, first); err != nil || next != 0 {
		t.Fatalf("walk after unfavourite = %d %v", next, err)
	}
	if err = store.FinishLastFMLoveBackfill(ctx, LoveBackfillDone, ""); err != nil {
		t.Fatal(err)
	}
	if run, _ = store.LastFMLoveBackfill(ctx); !run.Done() || run.FinishedAt == "" {
		t.Fatalf("done: %+v", run)
	}
}

func TestCancelLoveBackfillKeepsCursor(t *testing.T) {
	store, ctx, trackID := loveTestStore(t)
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginLastFMLoveBackfill(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceLastFMLoveBackfill(ctx, trackID, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.CancelLastFMLoveBackfill(ctx); err != nil {
		t.Fatal(err)
	}
	run, _ := store.LastFMLoveBackfill(ctx)
	if run.State != LoveBackfillCancelled || run.Cursor != trackID || run.Processed != 1 {
		t.Fatalf("cancelled: %+v", run)
	}
	// Advancing a cancelled run is a no-op, so a stale worker cannot move it.
	if err := store.AdvanceLastFMLoveBackfill(ctx, trackID+5, 0); err != nil {
		t.Fatal(err)
	}
	if run, _ = store.LastFMLoveBackfill(ctx); run.Cursor != trackID || run.Processed != 1 {
		t.Fatalf("cancelled run advanced: %+v", run)
	}
}
