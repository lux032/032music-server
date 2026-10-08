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
	giveTracksFiles(t, store)
	tracks, total, err := store.ArtistTracks(ctx, owner, ArtistTrackQuery{Limit: 100})
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
	fallback, total, err := store.ArtistTracks(ctx, composer, ArtistTrackQuery{Limit: 100})
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
	limited, total, err := store.ArtistTracks(ctx, owner, ArtistTrackQuery{Limit: 2, Offset: 3})
	if err != nil || total != 5 || len(limited) != 2 || limited[0].ID != entries[3].id || limited[1].ID != entries[4].id {
		t.Fatalf("page=%+v total=%d err=%v", limited, total, err)
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
}

func TestArtistFavoriteExplicitUpdateClearsMergeFlag(t *testing.T) {
	for _, steps := range []struct {
		name           string
		deleteFirst    bool
		putAfterDelete bool
		wantFavorite   int
	}{
		{"delete_then_put", true, true, 1},
		{"delete_only", true, false, 0},
		{"repeat_put_confirms", false, true, 1},
	} {
		t.Run(steps.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(filepath.Join(t.TempDir(), "explicit.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err = store.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			source := insertRoleTestArtist(t, ctx, store, "Source", "source")
			target := insertRoleTestArtist(t, ctx, store, "Target", "target")
			if err = store.SetArtistFavorite(ctx, source, true); err != nil {
				t.Fatal(err)
			}
			op, err := store.MergeArtists(ctx, source, target)
			if err != nil {
				t.Fatal(err)
			}
			assertArtistFavoriteState(t, ctx, store, target, 1)
			assertArtistMergeFavoriteFlag(t, ctx, store, op, 1)
			if steps.deleteFirst {
				if err = store.SetArtistFavorite(ctx, target, false); err != nil {
					t.Fatal(err)
				}
				assertArtistMergeFavoriteFlag(t, ctx, store, op, 0)
			}
			if steps.putAfterDelete {
				if err = store.SetArtistFavorite(ctx, target, true); err != nil {
					t.Fatal(err)
				}
				assertArtistMergeFavoriteFlag(t, ctx, store, op, 0)
				if _, err = store.db.ExecContext(ctx, `UPDATE artists SET favorited_at='2020-01-01T00:00:00Z' WHERE id=?`, target); err != nil {
					t.Fatal(err)
				}
				if err = store.SetArtistFavorite(ctx, target, true); err != nil {
					t.Fatal(err)
				}
				var stamp string
				if err = store.db.QueryRowContext(ctx, `SELECT favorited_at FROM artists WHERE id=?`, target).Scan(&stamp); err != nil || stamp != "2020-01-01T00:00:00Z" {
					t.Fatalf("repeated PUT timestamp=%q err=%v", stamp, err)
				}
			}
			if err = store.RollbackArtistMerge(ctx, op); err != nil {
				t.Fatal(err)
			}
			assertArtistFavoriteState(t, ctx, store, target, steps.wantFavorite)
			assertArtistFavoriteState(t, ctx, store, source, 1)
		})
	}
}

func assertArtistFavoriteState(t *testing.T, ctx context.Context, store *Store, id int64, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(ctx, `SELECT is_favorite FROM artists WHERE id=?`, id).Scan(&got); err != nil || got != want {
		t.Fatalf("artist %d favorite=%d want=%d err=%v", id, got, want, err)
	}
}

func assertArtistMergeFavoriteFlag(t *testing.T, ctx context.Context, store *Store, id int64, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(ctx, `SELECT favorite_set_by_merge FROM artist_merge_operations WHERE id=?`, id).Scan(&got); err != nil || got != want {
		t.Fatalf("merge %d flag=%d want=%d err=%v", id, got, want, err)
	}
}

func TestArtistTracksSortsAndTopTracks(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "sorts.db"))
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
	artist := insertRoleTestArtist(t, ctx, store, "Artist", "artist")
	result, err = store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key,release_year) VALUES(?,'Album','album','album',2020)`, library)
	if err != nil {
		t.Fatal(err)
	}
	album, _ := result.LastInsertId()
	if _, err = store.db.ExecContext(ctx, `INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,0)`, album, artist); err != nil {
		t.Fatal(err)
	}
	// Echo has a progress row that was never played (NULL last_played_at).
	fixtures := []struct {
		title      string
		duration   int64
		added      string
		plays      int64
		lastPlayed any
		progress   bool
	}{
		{"Delta", 300, "2024-01-03", 2, "2024-05-01", true},
		{"alpha", 100, "2024-01-01", 5, "2024-04-01", true},
		{"Charlie", 200, "2024-01-04", 2, "2024-06-01", true},
		{"Bravo", 200, "2024-01-02", 0, nil, false},
		{"Echo", 50, "2024-01-05", 0, nil, true},
	}
	ids := map[string]int64{}
	for i, f := range fixtures {
		result, err = store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number,duration_ms,added_at) VALUES(?,?,?,1,?,?,?)`, album, f.title, f.title, i+1, f.duration, f.added)
		if err != nil {
			t.Fatal(err)
		}
		ids[f.title], _ = result.LastInsertId()
		if f.progress {
			if _, err = store.db.ExecContext(ctx, `INSERT INTO playback_progress(track_id,play_count,last_played_at) VALUES(?,?,?)`, ids[f.title], f.plays, f.lastPlayed); err != nil {
				t.Fatal(err)
			}
		}
	}
	giveTracksFiles(t, store)
	titles := func(tracks []Track) string {
		names := make([]string, len(tracks))
		for i, track := range tracks {
			names[i] = track.Title
		}
		return strings.Join(names, ",")
	}
	for _, tc := range []struct {
		sort, order, want string
	}{
		{"", "", "Delta,alpha,Charlie,Bravo,Echo"},
		{"album", "desc", "Echo,Bravo,Charlie,alpha,Delta"},
		{"plays", "", "alpha,Charlie,Delta,Bravo,Echo"},
		{"plays", "asc", "Bravo,Echo,Delta,Charlie,alpha"},
		{"recent", "", "Charlie,Delta,alpha,Bravo,Echo"},
		{"recent", "asc", "alpha,Delta,Charlie,Bravo,Echo"},
		{"title", "", "alpha,Bravo,Charlie,Delta,Echo"},
		{"title", "desc", "Echo,Delta,Charlie,Bravo,alpha"},
		{"added", "", "Echo,Charlie,Delta,Bravo,alpha"},
		{"duration", "", "Echo,alpha,Charlie,Bravo,Delta"},
		{"duration", "desc", "Delta,Charlie,Bravo,alpha,Echo"},
	} {
		tracks, total, err := store.ArtistTracks(ctx, artist, ArtistTrackQuery{Sort: tc.sort, Order: tc.order, Limit: 100})
		if err != nil || total != 5 || titles(tracks) != tc.want {
			t.Errorf("sort=%q order=%q got %s total=%d err=%v, want %s", tc.sort, tc.order, titles(tracks), total, err, tc.want)
		}
	}
	page, total, err := store.ArtistTracks(ctx, artist, ArtistTrackQuery{Sort: "plays", Limit: 2, Offset: 2})
	if err != nil || total != 5 || titles(page) != "Delta,Bravo" || page[0].PlayCount != 2 {
		t.Fatalf("plays page=%s total=%d err=%v", titles(page), total, err)
	}
	for _, q := range []ArtistTrackQuery{{Sort: "popular", Limit: 1}, {Sort: "plays", Order: "down", Limit: 1}} {
		if _, _, err := store.ArtistTracks(ctx, artist, q); err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Errorf("query %+v err=%v", q, err)
		}
	}
	top, err := store.ArtistTopTracks(ctx, artist, ArtistTopTrackLimit)
	if err != nil || titles(top) != "alpha,Charlie,Delta" {
		t.Fatalf("top=%s err=%v", titles(top), err)
	}
	if top, err = store.ArtistTopTracks(ctx, artist, 2); err != nil || titles(top) != "alpha,Charlie" {
		t.Fatalf("top limit=%s err=%v", titles(top), err)
	}
	if count, err := store.ArtistTrackCount(ctx, artist); err != nil || count != 5 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
