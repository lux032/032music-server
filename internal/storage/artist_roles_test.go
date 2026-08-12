package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestArtistRoleFilters(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "artist-roles.db"))
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
	result, err = store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number) VALUES(?,?,?,1,1)`, albumID, "Track", "track")
	if err != nil {
		t.Fatal(err)
	}
	trackID, _ := result.LastInsertId()

	albumArtist := insertRoleTestArtist(t, ctx, store, "Album Artist", "album artist")
	trackArtist := insertRoleTestArtist(t, ctx, store, "Track Artist", "track artist")
	bothArtist := insertRoleTestArtist(t, ctx, store, "Both Artist", "both artist")
	if _, err = store.db.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,0),(?,?,1)`, albumID, albumArtist, albumID, bothArtist); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position) VALUES(?,?,0),(?,?,1)`, trackID, trackArtist, trackID, bothArtist); err != nil {
		t.Fatal(err)
	}

	assertArtistRoleNames(t, ctx, store, "album", []string{"Album Artist", "Both Artist"})
	assertArtistRoleNames(t, ctx, store, "track", []string{"Both Artist", "Track Artist"})
	assertArtistRoleNames(t, ctx, store, "all", []string{"Album Artist", "Both Artist", "Track Artist"})
}

func insertRoleTestArtist(t *testing.T, ctx context.Context, store *Store, name, key string) int64 {
	t.Helper()
	result, err := store.db.ExecContext(ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?)`, name, key, key)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func assertArtistRoleNames(t *testing.T, ctx context.Context, store *Store, role string, want []string) {
	t.Helper()
	artists, err := store.ListArtists(ctx, Filters{ArtistRole: role, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != len(want) {
		t.Fatalf("role %s returned %d artists, want %d: %#v", role, len(artists), len(want), artists)
	}
	for index, name := range want {
		if artists[index].Name != name {
			t.Fatalf("role %s artist %d = %q, want %q", role, index, artists[index].Name, name)
		}
	}
	total, err := store.CountArtists(ctx, Filters{ArtistRole: role})
	if err != nil || total != int64(len(want)) {
		t.Fatalf("role %s total = %d, want %d, err=%v", role, total, len(want), err)
	}
}
