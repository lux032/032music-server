package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestClientFeaturesLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "features.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	result, err := store.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Test','/music')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := result.LastInsertId()
	result, err = store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,?,?,?)`, libraryID, "Album", "album", "album")
	if err != nil {
		t.Fatal(err)
	}
	albumID, _ := result.LastInsertId()
	result, err = store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number,duration_ms) VALUES(?,?,?,1,1,240000)`, albumID, "Track", "track")
	if err != nil {
		t.Fatal(err)
	}
	trackID, _ := result.LastInsertId()

	if err := store.SetAlbumFavorite(ctx, albumID, true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, trackID, true); err != nil {
		t.Fatal(err)
	}
	if albums, total, err := store.FavoriteAlbums(ctx, 100, 0); err != nil || total != 1 || len(albums) != 1 {
		t.Fatalf("favorite albums = %d/%d, err=%v", len(albums), total, err)
	}
	if tracks, total, err := store.FavoriteTracks(ctx, 100, 0); err != nil || total != 1 || len(tracks) != 1 {
		t.Fatalf("favorite tracks = %d/%d, err=%v", len(tracks), total, err)
	}

	playlist, err := store.CreatePlaylist(ctx, "Test Playlist", "Description")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplacePlaylistItems(ctx, playlist.ID, []int64{trackID, trackID}); err != nil {
		t.Fatal(err)
	}
	detail, err := store.PlaylistDetail(ctx, playlist.ID)
	if err != nil || len(detail.Tracks) != 1 {
		t.Fatalf("playlist tracks = %d, err=%v", len(detail.Tracks), err)
	}

	if err := store.UpdatePlayback(ctx, PlaybackUpdate{TrackID: trackID, State: "playing", PositionMillis: 30000, DurationMillis: 240000}); err != nil {
		t.Fatal(err)
	}
	if err := store.Scrobble(ctx, trackID, 220000, 240000); err != nil {
		t.Fatal(err)
	}
	history, total, err := store.PlaybackHistory(ctx, 100, 0)
	if err != nil || total != 1 || len(history) != 1 || history[0].PlayCount != 1 {
		t.Fatalf("history = %#v, total=%d, err=%v", history, total, err)
	}
}
