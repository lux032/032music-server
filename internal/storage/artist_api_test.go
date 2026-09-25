package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestArtistFavoriteRescanAndMergeRollback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "artist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	input := ImportInput{LibraryID: library.ID, RelativePath: "one.flac", FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "One", Album: "Album", Artists: []string{"Source"}, AlbumArtists: []string{"Source"}, DiscNumber: 1, TrackNumber: 1}}
	if err = store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	insertTargetArtist := insertRoleTestArtist // existing helper for independent targets
	artists, err := store.ListArtists(ctx, Filters{})
	if err != nil || len(artists) != 1 {
		t.Fatalf("artists=%+v err=%v", artists, err)
	}
	sourceID := artists[0].ID
	if err = store.SetArtistFavorite(ctx, sourceID, true); err != nil {
		t.Fatal(err)
	}
	input.ModifiedAtNS = 2
	if err = store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err = store.CleanupOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	artists, err = store.ListArtists(ctx, Filters{Favorite: true})
	if err != nil || len(artists) != 1 || !artists[0].IsFavorite {
		t.Fatalf("favorite after rescan=%+v err=%v", artists, err)
	}
	for _, targetPreFavorite := range []bool{false, true} {
		targetID := insertTargetArtist(t, ctx, store, map[bool]string{false: "Unfavorited Target", true: "Favorited Target"}[targetPreFavorite], map[bool]string{false: "unfavorite", true: "favorite"}[targetPreFavorite])
		if targetPreFavorite {
			if err = store.SetArtistFavorite(ctx, targetID, true); err != nil {
				t.Fatal(err)
			}
		}
		operation, mergeErr := store.MergeArtists(ctx, sourceID, targetID)
		if mergeErr != nil {
			t.Fatal(mergeErr)
		}
		var favorite, flag int
		if err = store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, targetID).Scan(&favorite); err != nil || favorite != 1 {
			t.Fatalf("merged favorite=%d err=%v", favorite, err)
		}
		if err = store.db.QueryRowContext(ctx, `SELECT favorite_set_by_merge FROM artist_merge_operations WHERE id=?`, operation).Scan(&flag); err != nil || flag != boolInt(!targetPreFavorite) {
			t.Fatalf("merge flag=%d err=%v", flag, err)
		}
		if err = store.RollbackArtistMerge(ctx, operation); err != nil {
			t.Fatal(err)
		}
		if err = store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, targetID).Scan(&favorite); err != nil || favorite != boolInt(targetPreFavorite) {
			t.Fatalf("rollback favorite=%d err=%v", favorite, err)
		}
		if err = store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, sourceID).Scan(&favorite); err != nil || favorite != 1 {
			t.Fatalf("source favorite=%d err=%v", favorite, err)
		}
	}
	// Reopening and migrating an existing database must be idempotent.
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestArtistFavoritesMigrationOnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "existing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = store.db.ExecContext(ctx, `CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL) STRICT`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 14; version++ {
		matches, err := migrationFiles.ReadDir("migrations")
		if err != nil {
			t.Fatal(err)
		}
		var name string
		for _, entry := range matches {
			if len(entry.Name()) >= 3 && entry.Name()[:3] == fmt.Sprintf("%03d", version) {
				name = entry.Name()
				break
			}
		}
		if name == "" {
			t.Fatalf("missing migration %d", version)
		}
		script, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.ExecContext(ctx, string(script)); err != nil {
			t.Fatalf("migration %d: %v", version, err)
		}
		if _, err = store.db.ExecContext(ctx, `INSERT INTO schema_migrations(version,name) VALUES(?,?)`, version, name); err != nil {
			t.Fatal(err)
		}
	}
	id := insertRoleTestArtist(t, ctx, store, "Existing", "existing")
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.SetArtistFavorite(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, id).Scan(new(int)); err != nil {
		t.Fatal(err)
	}
}

func TestArtistTracksUnionOrderingFallbackAndLimit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "tracks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := store.db.ExecContext(ctx, `INSERT INTO libraries(name,root_path) VALUES('Test','/music')`)
	if err != nil {
		t.Fatal(err)
	}
	library, _ := result.LastInsertId()
	owner := insertRoleTestArtist(t, ctx, store, "Owner", "owner")
	singer := insertRoleTestArtist(t, ctx, store, "Singer", "singer")
	composer := insertRoleTestArtist(t, ctx, store, "Composer", "composer")
	type row struct {
		id, album int64
		name      string
	}
	var entries []row
	for i := 0; i < 3; i++ {
		result, err = store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key,release_year) VALUES(?,'Same','same',?,2020)`, library, fmt.Sprintf("album%d", i))
		if err != nil {
			t.Fatal(err)
		}
		album, _ := result.LastInsertId()
		if i < 2 {
			if _, err = store.db.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,0)`, album, owner); err != nil {
				t.Fatal(err)
			}
		}
		for disc := 1; disc <= 2; disc++ {
			result, err = store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number,lyricist,arranger,track_type) VALUES(?,'Song','song',?,?, 'Words','Arrange','remix')`, album, disc, 3-disc)
			if err != nil {
				t.Fatal(err)
			}
			track, _ := result.LastInsertId()
			entries = append(entries, row{track, album, fmt.Sprintf("%d/%d", i, disc)})
			if i == 0 && disc == 1 { // Duplicate union membership, plus a non-primary credit.
				if _, err = store.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,0,'primary'),(?,?,1,'primary'),(?,?,2,'composer')`, track, singer, track, owner, track, composer); err != nil {
					t.Fatal(err)
				}
			}
			if i == 2 && disc == 1 { // Primary-only song outside the owner's albums.
				if _, err = store.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,0,'primary')`, track, owner); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	tracks, total, err := store.ArtistTracks(ctx, owner)
	if err != nil || total != 5 || len(tracks) != 5 {
		t.Fatalf("tracks=%+v total=%d err=%v", tracks, total, err)
	}
	for i, want := range []int64{entries[0].id, entries[1].id, entries[2].id, entries[3].id, entries[4].id} {
		if tracks[i].ID != want {
			t.Fatalf("order %d got %d want %d", i, tracks[i].ID, want)
		}
	}
	if tracks[0].Artist != "Singer, Owner" || tracks[1].Artist != "Owner" || tracks[4].Artist != "Owner" || tracks[0].Lyricist != "Words" || tracks[0].Arranger != "Arrange" || tracks[0].TrackType != "remix" {
		t.Fatalf("projection=%+v", tracks)
	}
	if _, err = store.db.ExecContext(ctx, `INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,0,'primary')`, entries[5].id, composer); err != nil {
		t.Fatal(err)
	}
	fallback, total, err := store.ArtistTracks(ctx, composer)
	if err != nil || total != 1 || len(fallback) != 1 || fallback[0].Artist != "Composer" {
		t.Fatalf("composer=%+v total=%d err=%v", fallback, total, err)
	}
	// An uncredited track cannot enter the union; execute the detail projection
	// directly to exercise its Unknown Artist fallback.
	result, err = store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,'Uncredited','uncredited','uncredited')`, library)
	if err != nil {
		t.Fatal(err)
	}
	uncreditedAlbum, _ := result.LastInsertId()
	result, err = store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title) VALUES(?,'Uncredited','uncredited')`, uncreditedAlbum)
	if err != nil {
		t.Fatal(err)
	}
	uncreditedTrack, _ := result.LastInsertId()
	// The union subquery is deliberately empty for an uncredited track. Replace
	// only that membership subquery in the test; retain the production projection.
	uncreditedSelect := strings.Replace(artistTrackSelect, `(`+artistTrackIDs+`) matches`, `(SELECT ? AS track_id) matches`, 1)
	unknownTrack, err := scanArtistTrack(store.db.QueryRowContext(ctx, uncreditedSelect, uncreditedTrack))
	if err != nil || unknownTrack.Artist != "Unknown Artist" {
		t.Fatalf("unknown artist fallback=%q err=%v", unknownTrack.Artist, err)
	}
	oldLimit := artistTrackLimit
	artistTrackLimit = 2
	defer func() { artistTrackLimit = oldLimit }()
	limited, total, err := store.ArtistTracks(ctx, owner)
	if err != nil || total != 5 || len(limited) != 2 {
		t.Fatalf("limit=%d/%d err=%v", len(limited), total, err)
	}
}

func TestArtistFavoriteExplicitUpdateAndMultipleMerges(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "merge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	first := insertRoleTestArtist(t, ctx, store, "First", "first")
	second := insertRoleTestArtist(t, ctx, store, "Second", "second")
	target := insertRoleTestArtist(t, ctx, store, "Target", "target")
	for _, id := range []int64{first, second} {
		if err = store.SetArtistFavorite(ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}
	firstOp, err := store.MergeArtists(ctx, first, target)
	if err != nil {
		t.Fatal(err)
	}
	secondOp, err := store.MergeArtists(ctx, second, target)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RollbackArtistMerge(ctx, firstOp); err != nil {
		t.Fatal(err)
	}
	var favorite int
	if err = store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, target).Scan(&favorite); err != nil || favorite != 1 {
		t.Fatalf("remaining merge favorite=%d err=%v", favorite, err)
	}
	var transferredFlag int
	if err = store.db.QueryRowContext(ctx, `SELECT favorite_set_by_merge FROM artist_merge_operations WHERE id=?`, secondOp).Scan(&transferredFlag); err != nil || transferredFlag != 1 {
		t.Fatalf("transferred favorite flag=%d err=%v", transferredFlag, err)
	}
	if err = store.RollbackArtistMerge(ctx, secondOp); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, target).Scan(&favorite); err != nil || favorite != 0 {
		t.Fatalf("all merges rolled back favorite=%d err=%v", favorite, err)
	}
	// LIFO rollback must also return the originally un-favorited target to zero.
	for _, sourceID := range []int64{first, second} {
		if err = store.SetArtistFavorite(ctx, sourceID, true); err != nil {
			t.Fatal(err)
		}
	}
	firstOp, err = store.MergeArtists(ctx, first, target)
	if err != nil {
		t.Fatal(err)
	}
	secondOp, err = store.MergeArtists(ctx, second, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, operationID := range []int64{secondOp, firstOp} {
		if err = store.RollbackArtistMerge(ctx, operationID); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, target).Scan(&favorite); err != nil || favorite != 0 {
		t.Fatalf("LIFO rollback favorite=%d err=%v", favorite, err)
	}
	op, err := store.MergeArtists(ctx, first, second)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetArtistFavorite(ctx, first, false); err != nil {
		t.Fatal(err)
	}
	if err = store.SetArtistFavorite(ctx, first, true); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE artists SET favorited_at='2020-01-01T00:00:00Z' WHERE id=?`, second); err != nil {
		t.Fatal(err)
	}
	if err = store.SetArtistFavorite(ctx, first, true); err != nil {
		t.Fatal(err)
	}
	var stamp string
	if err = store.db.QueryRowContext(ctx, `SELECT favorited_at FROM artists WHERE id=?`, second).Scan(&stamp); err != nil || stamp != "2020-01-01T00:00:00Z" {
		t.Fatalf("repeated PUT timestamp=%q err=%v", stamp, err)
	}
	if err = store.RollbackArtistMerge(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, second).Scan(&favorite); err != nil || favorite != 1 {
		t.Fatalf("explicit favorite rollback=%d err=%v", favorite, err)
	}
}
