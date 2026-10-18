package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// plainOnlineSource returns lyrics without timestamps for every matching song.
type plainOnlineSource struct{ fakeLyricsSource }

func (p *plainOnlineSource) Get(ctx context.Context, id string) (lyricsonline.Lyrics, error) {
	if _, err := p.fakeLyricsSource.Get(ctx, id); err != nil {
		return lyricsonline.Lyrics{}, err
	}
	return lyricsonline.Lyrics{Source: lyricsonline.SourceNetEase, ID: id, LRC: "online plain one\nonline plain two"}, nil
}

func TestUnsyncedLocalLyricsTriggerOnlineLookup(t *testing.T) {
	app, _, handler, _, trackID := lyricsTestApp(t, "Plain Song", "local plain one\nlocal plain two")
	src := &fakeLyricsSource{}
	app.SetOnlineLyrics(lyricsonline.NewWithProviders(src))

	// The local lyrics are returned at once; the online lookup runs in the background.
	got, code := getLyrics(t, handler, trackID)
	if code != 200 || got.Source != "embedded" || got.Synced {
		t.Fatalf("first request must not wait for the online lookup: %d %+v", code, got)
	}
	app.WaitBackgroundLyrics()
	searches, _ := src.counts()
	if searches != 1 {
		t.Fatalf("background searches = %d", searches)
	}
	// From the next request on the synced online lyrics replace the local ones.
	for i := 0; i < 2; i++ {
		if got, code = getLyrics(t, handler, trackID); code != 200 || got.Source != "netease" || !got.Synced || got.Lines[0].Text != "online one" {
			t.Fatalf("round %d: %d %+v", i, code, got)
		}
	}
	if s2, _ := src.counts(); s2 != searches {
		t.Fatal("later requests must use the cache")
	}
	// Clearing the online lyrics falls back to the local ones without searching again.
	apiRequest(handler, "DELETE", "/api/v1/tracks/"+itoa(trackID)+"/lyrics/source", "")
	if got, _ = getLyrics(t, handler, trackID); got.Source != "embedded" || got.Synced {
		t.Fatalf("after clear = %+v", got)
	}
	if s2, _ := src.counts(); s2 != searches {
		t.Fatal("a cleared track must not search again")
	}
}

func TestUnsyncedLocalLyricsBeatUnsyncedOnline(t *testing.T) {
	app, _, handler, _, trackID := lyricsTestApp(t, "Plain Song", "local plain one\nlocal plain two")
	src := &plainOnlineSource{}
	app.SetOnlineLyrics(lyricsonline.NewWithProviders(src))
	for i := 0; i < 2; i++ { // background lookup, then cached row
		if got, code := getLyrics(t, handler, trackID); code != 200 || got.Source != "embedded" || got.Lines[0].Text != "local plain one" {
			t.Fatalf("round %d: local unsynced must win over online unsynced: %d %+v", i, code, got)
		}
		app.WaitBackgroundLyrics()
	}
	if s, _ := src.counts(); s != 1 {
		t.Fatalf("searches = %d", s)
	}

	// Without local lyrics the unsynced online ones are still used.
	app2, _, handler2, _, trackID2 := lyricsTestApp(t, "Plain Song", "")
	app2.SetOnlineLyrics(lyricsonline.NewWithProviders(&plainOnlineSource{}))
	if got, code := getLyrics(t, handler2, trackID2); code != 200 || got.Source != "netease" || got.Synced {
		t.Fatalf("online unsynced = %d %+v", code, got)
	}
}

func TestUnsyncedLocalLyricsKeptWhenOnlineMisses(t *testing.T) {
	app, _, handler, _, trackID := lyricsTestApp(t, "Plain Song", "local plain")
	src := &fakeLyricsSource{none: true}
	app.SetOnlineLyrics(lyricsonline.NewWithProviders(src))
	for i := 0; i < 2; i++ { // miss, then remembered miss
		if got, code := getLyrics(t, handler, trackID); code != 200 || got.Source != "embedded" {
			t.Fatalf("round %d: %d %+v", i, code, got)
		}
		app.WaitBackgroundLyrics()
	}
	if s, _ := src.counts(); s != 1 {
		t.Fatalf("searches = %d", s)
	}
	src.none, src.fail = false, true
	if _, err := app.store.DeleteOnlineLyrics(context.Background(), []int64{trackID}); err != nil {
		t.Fatal(err)
	}
	if got, code := getLyrics(t, handler, trackID); code != 200 || got.Source != "embedded" {
		t.Fatalf("network failure: %d %+v", code, got)
	}
	app.WaitBackgroundLyrics()
	if _, err := app.store.OnlineLyrics(context.Background(), trackID); err == nil {
		t.Fatal("a network failure must not be cached")
	}
}

func TestLocalLyricsPreferSyncedEmbeddedOverPlainSidecar(t *testing.T) {
	app, store, _ := setupTestApp(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	lib, _ := store.LibraryByRoot(ctx, root)
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: 1, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", DiscNumber: 1, DurationMillis: 1, Lyrics: "[00:01.00]embedded synced"}}); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := tracks.Items[0].ID
	sidecar := filepath.Join(root, "song.lrc")
	cases := []struct{ file, wantSource, wantText string }{
		{"sidecar plain", "embedded", "embedded synced"},
		{"[00:02.00]sidecar synced", "file", "sidecar synced"},
	}
	for _, c := range cases {
		if err := os.WriteFile(sidecar, []byte(c.file), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := app.localLyrics(ctx, id)
		if err != nil || got.Source != c.wantSource || got.Result.Lines[0].Text != c.wantText {
			t.Fatalf("%q: %+v %v", c.file, got, err)
		}
	}
}
