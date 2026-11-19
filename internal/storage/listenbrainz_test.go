package storage

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestListenBrainzScrobbleAndMBIDSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "lb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	const recMBID = "11111111-1111-4111-8111-111111111111"
	const relMBID = "22222222-2222-4222-8222-222222222222"
	const artMBID = "33333333-3333-4333-8333-333333333333"
	if err = store.ImportTrack(ctx, ImportInput{
		LibraryID: lib.ID, RelativePath: "lb.flac", FileSize: 10, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Idol", Album: "Single", Artists: []string{"YOASOBI"}, DurationMillis: 210000, TrackNumber: 1, DiscNumber: 1,
			Raw: map[string][]string{
				"MUSICBRAINZ_TRACKID":  {recMBID},
				"MUSICBRAINZ_ALBUMID":  {relMBID},
				"MUSICBRAINZ_ARTISTID": {artMBID},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.ListTracks(ctx, Filters{Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks: %v %v", tracks, err)
	}
	trackID := tracks[0].ID

	if err = store.SaveListenBrainzPreferences(ctx, true, true, "https://lb.example.com/"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetListenBrainzConnection(ctx, "https://lb.example.com/", "tok-123", "listener"); err != nil {
		t.Fatal(err)
	}
	settings, err := store.ListenBrainzSettings(ctx)
	if err != nil || !settings.Ready() || settings.ServerURL != "https://lb.example.com" || settings.Username != "listener" {
		t.Fatalf("settings = %+v err=%v", settings, err)
	}

	res, err := store.RecordPlaybackEvent(ctx, PlaybackEventInput{
		ClientID: "web-client-1", ClientKind: "web", SessionID: "session-lb-1", Seq: 1, Type: "start",
		TrackID: trackID, State: "playing", PositionMillis: 0, DurationMillis: 210000,
	})
	if err != nil || res.QueuedForListenBrainz {
		t.Fatalf("start event unexpectedly queued: %+v %v", res, err)
	}
	res, err = store.RecordPlaybackEvent(ctx, PlaybackEventInput{
		ClientID: "web-client-1", ClientKind: "web", SessionID: "session-lb-1", Seq: 2, Type: "heartbeat",
		TrackID: trackID, State: "playing", PositionMillis: 110000, DurationMillis: 210000,
	})
	if err != nil || !res.Counted || !res.QueuedForListenBrainz {
		t.Fatalf("expected scrobble enqueue at >50%%: %+v %v", res, err)
	}

	due, err := store.DueListenBrainzScrobbles(ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due = %+v err=%v", due, err)
	}
	if due[0].RecordingMBID != recMBID || due[0].ReleaseMBID != relMBID || !reflect.DeepEqual(due[0].ArtistMBIDs, []string{artMBID}) {
		t.Fatalf("mbids = rec:%q rel:%q art:%v", due[0].RecordingMBID, due[0].ReleaseMBID, due[0].ArtistMBIDs)
	}
}

func TestListenBrainzLoveQueueAndTrackIDCache(t *testing.T) {
	store, ctx, trackID := loveTestStore(t)

	// Without token or switch, SetTrackFavorite does not queue ListenBrainz love.
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	items, err := store.DueListenBrainzLoves(ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("unexpected queue: %+v %v", items, err)
	}

	if err = store.SetListenBrainzConnection(ctx, "", "tok", "user"); err != nil {
		t.Fatal(err)
	}
	if err = store.SetListenBrainzLoveSync(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err = store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	if err = store.SetTrackFavorite(ctx, trackID, false); err != nil {
		t.Fatal(err)
	}
	items, err = store.DueListenBrainzLoves(ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(items) != 1 || items[0].Desired {
		t.Fatalf("collapsed queue = %+v err=%v", items, err)
	}

	const msid = "44444444-4444-4444-8444-444444444444"
	if err = store.UpsertListenBrainzRecordingMSID(ctx, trackID, items[0].Artist, items[0].Track, items[0].Album, msid); err != nil {
		t.Fatal(err)
	}
	cache, err := store.ListenBrainzTrackIDCache(ctx, trackID, items[0].Artist, items[0].Track, items[0].Album)
	if err != nil || cache.RecordingMSID != msid {
		t.Fatalf("cache = %+v err=%v", cache, err)
	}
	// Changed metadata fingerprint invalidates cached MSID.
	stale, err := store.ListenBrainzTrackIDCache(ctx, trackID, "Other Artist", items[0].Track, items[0].Album)
	if err != nil || stale.RecordingMSID != "" {
		t.Fatalf("stale cache = %+v err=%v", stale, err)
	}
}
