package storage

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSyncAlbumsPagination(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "album-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := store.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Music','/music')`)
	if err != nil {
		t.Fatal(err)
	}
	libraryID, _ := res.LastInsertId()
	for i := 1; i <= 3; i++ {
		if _, err := store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key,release_year,is_favorite) VALUES(?,?,?,?,?,?)`, libraryID, "Album "+strconv.Itoa(i), "album "+strconv.Itoa(i), "key-"+strconv.Itoa(i), 2020+i, i%2); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.SyncAlbums(ctx, SyncAlbumsParams{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || !first.HasMore || first.Total != 3 || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	cursor, _ := strconv.ParseInt(first.NextCursor, 10, 64)
	second, err := store.SyncAlbums(ctx, SyncAlbumsParams{Cursor: cursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.HasMore || second.Total != 3 || second.Items[0].Title != "Album 3" {
		t.Fatalf("second page = %#v", second)
	}
}

func TestSyncTracksEmptyDB(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "empty-sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	result, err := store.SyncTracks(ctx, SyncTracksParams{Cursor: 0, Limit: 10})
	if err != nil {
		t.Fatalf("SyncTracks failed: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(result.Items))
	}
	if result.HasMore {
		t.Fatalf("expected hasMore=false, got true")
	}
	if result.NextCursor != "" {
		t.Fatalf("expected empty nextCursor, got %q", result.NextCursor)
	}
	if result.Total != 0 {
		t.Fatalf("expected total=0, got %d", result.Total)
	}
}

func TestSyncTracksPaginationAndFilters(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sync-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// Setup library
	res, err := store.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Music','/music')`)
	if err != nil {
		t.Fatal(err)
	}
	libID, _ := res.LastInsertId()

	// Setup artists
	artist1 := insertRoleTestArtist(t, ctx, store, "Artist One", "artist1")
	artist2 := insertRoleTestArtist(t, ctx, store, "Artist Two", "artist2")

	// Setup album 1
	res, err = store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key,performed_by) VALUES(?,?,'album1','key1','Album Artist 1')`, libID, "First Album")
	if err != nil {
		t.Fatal(err)
	}
	album1ID, _ := res.LastInsertId()

	// Artwork for album 1
	res, err = store.db.ExecContext(ctx, `INSERT INTO artworks(album_id,source_type,content_hash,mime_type,byte_size,is_primary) VALUES(?,'embedded','hash1','image/jpeg',1024,1)`, album1ID)
	if err != nil {
		t.Fatal(err)
	}
	art1ID, _ := res.LastInsertId()

	// Setup album 2
	res, err = store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,?,'album2','key2')`, libID, "Second Album")
	if err != nil {
		t.Fatal(err)
	}
	album2ID, _ := res.LastInsertId()
	if _, err = store.db.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,0)`, album2ID, artist2); err != nil {
		t.Fatal(err)
	}

	// Insert 5 tracks: 3 in album 1, 2 in album 2
	var trackIDs []int64
	for i := 1; i <= 3; i++ {
		r, e := store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number,duration_ms,is_favorite) VALUES(?,?,?,1,?,?,?)`,
			album1ID, "Track 1-"+strconv.Itoa(i), "t1-"+strconv.Itoa(i), i, i*60000, i%2)
		if e != nil {
			t.Fatal(e)
		}
		tID, _ := r.LastInsertId()
		trackIDs = append(trackIDs, tID)
		// Set track artist for track 1
		if i == 1 {
			if _, err = store.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position) VALUES(?,?,0)`, tID, artist1); err != nil {
				t.Fatal(err)
			}
		}
	}

	for i := 1; i <= 2; i++ {
		r, e := store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number,duration_ms) VALUES(?,?,?,1,?,?)`,
			album2ID, "Track 2-"+strconv.Itoa(i), "t2-"+strconv.Itoa(i), i, (i+3)*60000)
		if e != nil {
			t.Fatal(e)
		}
		tID, _ := r.LastInsertId()
		trackIDs = append(trackIDs, tID)
	}

	// 1. Full sync across 3 pages with limit=2
	// Page 1
	p1, err := store.SyncTracks(ctx, SyncTracksParams{Cursor: 0, Limit: 2})
	if err != nil {
		t.Fatalf("page 1 failed: %v", err)
	}
	if len(p1.Items) != 2 || !p1.HasMore || p1.Total != 5 {
		t.Fatalf("page 1 mismatch: len=%d, hasMore=%v, total=%d", len(p1.Items), p1.HasMore, p1.Total)
	}
	if p1.Items[0].ID != trackIDs[0] || p1.Items[1].ID != trackIDs[1] {
		t.Fatalf("page 1 tracks unexpected: %#v", p1.Items)
	}
	if p1.NextCursor != strconv.FormatInt(trackIDs[1], 10) {
		t.Fatalf("page 1 nextCursor = %q, want %d", p1.NextCursor, trackIDs[1])
	}

	// Verify track 1 fields
	t1 := p1.Items[0]
	if t1.Title != "Track 1-1" || t1.Album != "First Album" || t1.Artist != "Artist One" {
		t.Fatalf("track 1 metadata mismatch: title=%q, album=%q, artist=%q", t1.Title, t1.Album, t1.Artist)
	}
	if t1.DurationMillis != 60000 || !t1.IsFavorite {
		t.Fatalf("track 1 duration/favorite mismatch: dur=%d, fav=%v", t1.DurationMillis, t1.IsFavorite)
	}
	if t1.ArtworkURL != "/api/v1/artwork/"+strconv.FormatInt(art1ID, 10) {
		t.Fatalf("track 1 artwork = %q, want /api/v1/artwork/%d", t1.ArtworkURL, art1ID)
	}
	if t1.StreamURL != "/api/v1/tracks/"+strconv.FormatInt(trackIDs[0], 10)+"/stream" {
		t.Fatalf("track 1 stream = %q", t1.StreamURL)
	}

	// Verify track 2 fields (fallback to album performed_by "Album Artist 1")
	t2 := p1.Items[1]
	if t2.Artist != "Album Artist 1" {
		t.Fatalf("track 2 artist = %q, want 'Album Artist 1'", t2.Artist)
	}

	// Page 2
	cursor1, _ := strconv.ParseInt(p1.NextCursor, 10, 64)
	p2, err := store.SyncTracks(ctx, SyncTracksParams{Cursor: cursor1, Limit: 2})
	if err != nil {
		t.Fatalf("page 2 failed: %v", err)
	}
	if len(p2.Items) != 2 || !p2.HasMore || p2.Total != 5 {
		t.Fatalf("page 2 mismatch: len=%d, hasMore=%v, total=%d", len(p2.Items), p2.HasMore, p2.Total)
	}
	if p2.Items[0].ID != trackIDs[2] || p2.Items[1].ID != trackIDs[3] {
		t.Fatalf("page 2 tracks unexpected: %#v", p2.Items)
	}

	// Verify track 4 artist (fallback to album_artists "Artist Two")
	t4 := p2.Items[1]
	if t4.Artist != "Artist Two" {
		t.Fatalf("track 4 artist = %q, want 'Artist Two'", t4.Artist)
	}
	if t4.ArtworkURL != "" {
		t.Fatalf("track 4 artwork should be empty, got %q", t4.ArtworkURL)
	}

	// Page 3
	cursor2, _ := strconv.ParseInt(p2.NextCursor, 10, 64)
	p3, err := store.SyncTracks(ctx, SyncTracksParams{Cursor: cursor2, Limit: 2})
	if err != nil {
		t.Fatalf("page 3 failed: %v", err)
	}
	if len(p3.Items) != 1 || p3.HasMore || p3.NextCursor != "" || p3.Total != 5 {
		t.Fatalf("page 3 mismatch: len=%d, hasMore=%v, nextCursor=%q, total=%d", len(p3.Items), p3.HasMore, p3.NextCursor, p3.Total)
	}
	if p3.Items[0].ID != trackIDs[4] {
		t.Fatalf("page 3 track unexpected: %#v", p3.Items[0])
	}

	// 2. Filter by AlbumID
	albumFilterRes, err := store.SyncTracks(ctx, SyncTracksParams{AlbumID: album2ID, Limit: 10})
	if err != nil {
		t.Fatalf("album filter failed: %v", err)
	}
	if len(albumFilterRes.Items) != 2 || albumFilterRes.Total != 2 || albumFilterRes.HasMore {
		t.Fatalf("album filter result mismatch: len=%d, total=%d, hasMore=%v", len(albumFilterRes.Items), albumFilterRes.Total, albumFilterRes.HasMore)
	}
	if albumFilterRes.Items[0].AlbumID != album2ID || albumFilterRes.Items[1].AlbumID != album2ID {
		t.Fatalf("album filter tracks had unexpected albumID")
	}

	// 3. User title override verification
	_, err = store.db.ExecContext(ctx, `UPDATE tracks SET user_title='Overridden Title' WHERE id=?`, trackIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	overrideRes, err := store.SyncTracks(ctx, SyncTracksParams{Cursor: 0, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if overrideRes.Items[0].Title != "Overridden Title" {
		t.Fatalf("expected overridden title, got %q", overrideRes.Items[0].Title)
	}
}
