package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

// browseFixture is a small deterministic library covering multi-artist albums,
// album/track genre overrides, kana index entries, renamed artists and credit
// roles, used by the browse-consistency regression tests.
type browseFixture struct {
	store     *Store
	libraryID int64
	artists   map[string]int64
	albums    map[string]int64
	tracks    map[string]int64
	genres    map[string]int64
	seq       int
}

func newBrowseFixture(t *testing.T) *browseFixture {
	t.Helper()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "browse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLibrary(ctx, "Test", "/music"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	fx := &browseFixture{store: store, libraryID: library.ID, artists: map[string]int64{}, albums: map[string]int64{}, tracks: map[string]int64{}, genres: map[string]int64{}}
	return fx
}

func (fx *browseFixture) artist(t *testing.T, name string) int64 {
	t.Helper()
	if id, ok := fx.artists[name]; ok {
		return id
	}
	key := metadata.Normalize(name)
	res, err := fx.store.db.Exec(`INSERT INTO artists(display_name,sort_name,identity_key) VALUES(?,?,?)`, name, key, key)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	fx.artists[name] = id
	return id
}

func (fx *browseFixture) genre(t *testing.T, name string) int64 {
	t.Helper()
	if id, ok := fx.genres[name]; ok {
		return id
	}
	res, err := fx.store.db.Exec(`INSERT INTO genres(name) VALUES(?)`, name)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	fx.genres[name] = id
	return id
}

// album inserts an album row; sortTitle defaults to a lowercased title and
// readingTitle is optional (used by the kana index).
func (fx *browseFixture) album(t *testing.T, title string, year int, performedBy string, artistNames []string) int64 {
	t.Helper()
	sortTitle := strings.ToLower(title)
	fx.seq++
	res, err := fx.store.db.Exec(`INSERT INTO albums(library_id,title,sort_title,grouping_key,release_year,performed_by) VALUES(?,?,?,?,?,?)`, fx.libraryID, title, sortTitle, "key-"+sortTitle+"-"+strconv.Itoa(fx.seq), year, performedBy)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	fx.albums[title] = id
	for position, name := range artistNames {
		if _, err = fx.store.db.Exec(`INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,?)`, id, fx.artist(t, name), position); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func (fx *browseFixture) setAlbumReading(t *testing.T, albumID int64, reading string) {
	t.Helper()
	if _, err := fx.store.db.Exec(`UPDATE albums SET reading_title=? WHERE id=?`, reading, albumID); err != nil {
		t.Fatal(err)
	}
}

func (fx *browseFixture) track(t *testing.T, name string, albumID int64, number int, rawGenres []string, overrideGenres []string) int64 {
	t.Helper()
	res, err := fx.store.db.Exec(`INSERT INTO tracks(album_id,title,sort_title,track_number) VALUES(?,?,?,?)`, albumID, name, strings.ToLower(name), number)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	fx.tracks[name] = id
	if _, err = fx.store.db.Exec(`INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,container,mime_type,status) VALUES(?,?,?,?,?,?,'audio/flac','available')`, fx.libraryID, id, fmt.Sprintf("music/%d.flac", id), 1000+id, 1, "flac"); err != nil {
		t.Fatal(err)
	}
	for position, genre := range rawGenres {
		if _, err = fx.store.db.Exec(`INSERT INTO track_genres(track_id,genre_id,position) VALUES(?,?,?)`, id, fx.genre(t, genre), position); err != nil {
			t.Fatal(err)
		}
	}
	for position, genre := range overrideGenres {
		if _, err = fx.store.db.Exec(`INSERT INTO track_genre_overrides(track_id,genre_id,position) VALUES(?,?,?)`, id, fx.genre(t, genre), position); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func (fx *browseFixture) trackCredit(t *testing.T, trackID int64, artistName, role string, position int) {
	t.Helper()
	if _, err := fx.store.db.Exec(`INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,?,?)`, trackID, fx.artist(t, artistName), position, role); err != nil {
		t.Fatal(err)
	}
}

func (fx *browseFixture) artwork(t *testing.T, albumID int64, primary bool) int64 {
	t.Helper()
	res, err := fx.store.db.Exec(`INSERT INTO artworks(album_id,source_type,source_path,content_hash,mime_type,byte_size,is_primary) VALUES(?,'embedded',?,?, 'image/jpeg',10,?)`, albumID, fmt.Sprintf("art/%d.jpg", albumID), fmt.Sprintf("hash-%d-%v", albumID, primary), boolInt(primary))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (fx *browseFixture) setAlbumGenreOverrides(t *testing.T, albumID int64, genres []string) {
	t.Helper()
	for position, genre := range genres {
		if _, err := fx.store.db.Exec(`INSERT INTO album_genre_overrides(album_id,genre_id,position) VALUES(?,?,?)`, albumID, fx.genre(t, genre), position); err != nil {
			t.Fatal(err)
		}
	}
}

// buildStandardFixture creates:
//
//	Alpha (2001, artists Kinoko + Teikoku, performed_by "Kinoko, Teikoku")
//	  track Alpha One (raw Rock, primary Kinoko)
//	  track Alpha Two (raw Rock, override Jazz; primary Kinoko; composer credit "Composer X")
//	Beta (2003, artist Kinoko) track Beta One (raw Rock, primary Kinoko), instrumental
//	Gamma (2005, artist Teikoku) track Gamma One (raw Pop, primary Teikoku), album override Classical
//	海辺 (reading うみべ, 2010, artist うた子) track 海辺 One (raw Pop, primary うた子)
//	Solo (2015, artist うた子) track Solo Song (raw Blues, override Soul; primary うた子)
//	1st Album (2020, artist Kinoko) track 1st Song (raw Rock, primary Kinoko)
func buildStandardFixture(t *testing.T) *browseFixture {
	t.Helper()
	fx := newBrowseFixture(t)
	alpha := fx.album(t, "Alpha", 2001, "Kinoko, Teikoku", []string{"Kinoko", "Teikoku"})
	one := fx.track(t, "Alpha One", alpha, 1, []string{"Rock"}, nil)
	fx.trackCredit(t, one, "Kinoko", "primary", 0)
	two := fx.track(t, "Alpha Two", alpha, 2, []string{"Rock"}, []string{"Jazz"})
	fx.trackCredit(t, two, "Kinoko", "primary", 0)
	fx.trackCredit(t, two, "Composer X", "composer", 1)
	fx.artwork(t, alpha, true)
	beta := fx.album(t, "Beta", 2003, "Kinoko", []string{"Kinoko"})
	fx.artwork(t, beta, false)
	betaOne := fx.track(t, "Beta One", beta, 1, []string{"Rock"}, nil)
	fx.trackCredit(t, betaOne, "Kinoko", "primary", 0)
	if _, err := fx.store.db.Exec(`UPDATE tracks SET track_type='instrumental' WHERE id=?`, betaOne); err != nil {
		t.Fatal(err)
	}
	gamma := fx.album(t, "Gamma", 2005, "Teikoku", []string{"Teikoku"})
	gammaOne := fx.track(t, "Gamma One", gamma, 1, []string{"Pop"}, nil)
	fx.trackCredit(t, gammaOne, "Teikoku", "primary", 0)
	fx.setAlbumGenreOverrides(t, gamma, []string{"Classical", "Soundtrack"})
	umibe := fx.album(t, "海辺", 2010, "うた子", []string{"うた子"})
	fx.setAlbumReading(t, umibe, "うみべ")
	umibeOne := fx.track(t, "海辺 One", umibe, 1, []string{"Pop"}, nil)
	fx.trackCredit(t, umibeOne, "うた子", "primary", 0)
	solo := fx.album(t, "Solo", 2015, "うた子", []string{"うた子"})
	soloSong := fx.track(t, "Solo Song", solo, 1, []string{"Blues"}, []string{"Soul"})
	fx.trackCredit(t, soloSong, "うた子", "primary", 0)
	first := fx.album(t, "1st Album", 2020, "Kinoko", []string{"Kinoko"})
	firstSong := fx.track(t, "1st Song", first, 1, []string{"Rock"}, nil)
	fx.trackCredit(t, firstSong, "Kinoko", "primary", 0)
	return fx
}

func albumIDs(list []Album) []int64 {
	ids := make([]int64, 0, len(list))
	for _, album := range list {
		ids = append(ids, album.ID)
	}
	return ids
}

func TestAlbumListCountConsistency(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	alpha := fx.albums["Alpha"]
	if err := fx.store.UpdateAlbum(ctx, alpha, AlbumEdit{Title: "Alpha", Year: 1999, Genres: nil}); err != nil {
		t.Fatal(err)
	}
	kinoko := fx.artists["Kinoko"]
	teikoku := fx.artists["Teikoku"]
	composer := fx.artists["Composer X"]
	utaKo := fx.artists["うた子"]

	cases := []struct {
		name    string
		filters Filters
		want    int
	}{
		{"all", Filters{}, 6},
		{"index A", Filters{Index: "A"}, 1},
		{"index A + artist", Filters{Index: "A", ArtistID: kinoko}, 1},
		{"index A + artist mismatch", Filters{Index: "A", ArtistID: utaKo}, 0},
		{"index A + genre", Filters{Index: "A", Genre: "Jazz"}, 1},
		{"index A + query hit", Filters{Index: "A", Query: "Alph"}, 1},
		{"index A + query miss", Filters{Index: "A", Query: "Beta"}, 0},
		{"index A + query artist", Filters{Index: "A", Query: "Teikoku"}, 1},
		{"index kana + query", Filters{Index: "あ", Query: "海辺"}, 1},
		{"index # + query", Filters{Index: "#", Query: "1st"}, 1},
		{"index kana row", Filters{Index: "あ"}, 1},
		{"index #", Filters{Index: "#"}, 1},
		{"genre raw", Filters{Genre: "Rock"}, 3},
		{"genre track override", Filters{Genre: "Jazz"}, 1},
		{"genre track override adds album genre", Filters{Genre: "Rock", ArtistID: teikoku}, 1},
		{"genre hidden by track override", Filters{Genre: "Blues"}, 0},
		{"genre from track override", Filters{Genre: "Soul"}, 1},
		{"genre album override", Filters{Genre: "Classical"}, 1},
		{"genre album override second value", Filters{Genre: "Soundtrack"}, 1},
		{"genre album override hides raw", Filters{Genre: "Pop", ArtistID: teikoku}, 0},
		{"query album title", Filters{Query: "Alph"}, 1},
		{"query album artist", Filters{Query: "Kinoko"}, 3},
		{"query second artist", Filters{Query: "Teikoku"}, 2},
		{"artist id album credit", Filters{ArtistID: teikoku}, 2},
		{"artist id any track credit", Filters{ArtistID: composer}, 1},
		{"year raw", Filters{Year: 2003}, 1},
		{"year user override wins", Filters{Year: 1999}, 1},
		{"year user override hides raw", Filters{Year: 2001}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.filters
			f.Limit = 100
			list, err := fx.store.ListAlbums(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			count, err := fx.store.CountAlbums(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			if int(count) != tc.want || len(list) != tc.want {
				t.Fatalf("list=%d count=%d want=%d (list ids=%v)", len(list), count, tc.want, albumIDs(list))
			}
		})
	}
}

func TestListAlbumsQueryByArtistKeepsAllArtists(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	// Query matches only the second artist; the card must still list both.
	list, err := fx.store.ListAlbums(ctx, Filters{Query: "Teikoku", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var alpha *Album
	for i := range list {
		if list[i].Title == "Alpha" {
			alpha = &list[i]
		}
	}
	if alpha == nil {
		t.Fatalf("Alpha missing from %v", albumIDs(list))
	}
	if alpha.Artist != "Kinoko, Teikoku" {
		t.Fatalf("Artist = %q, want both artists", alpha.Artist)
	}
	// Renaming via user_display_name must keep the album findable.
	if err := fx.store.UpdateArtist(ctx, fx.artists["Teikoku"], "帝国"); err != nil {
		t.Fatal(err)
	}
	list, err = fx.store.ListAlbums(ctx, Filters{Query: "帝国", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, album := range list {
		if album.Title == "Alpha" {
			found = true
			if !strings.Contains(album.Artist, "帝国") {
				t.Fatalf("Artist = %q, want renamed name", album.Artist)
			}
		}
	}
	if !found {
		t.Fatal("renamed artist query did not match")
	}
}

func TestListAlbumsFieldValues(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	list, err := fx.store.ListAlbums(ctx, Filters{Query: "Alpha", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("len=%d", len(list))
	}
	album := list[0]
	if album.Title != "Alpha" || album.Year != 2001 || album.Artist != "Kinoko, Teikoku" || album.PerformedBy != "Kinoko, Teikoku" {
		t.Fatalf("album = %+v", album)
	}
	if album.TrackCount != 2 || album.Genres != "Rock,Jazz" || album.Formats != "FLAC" {
		t.Fatalf("album = %+v", album)
	}
	wantBytes := int64(1000+fx.tracks["Alpha One"]) + int64(1000+fx.tracks["Alpha Two"])
	if album.TotalBytes != wantBytes || album.TotalSize != formatBytes(wantBytes) {
		t.Fatalf("bytes = %d/%q want %d", album.TotalBytes, album.TotalSize, wantBytes)
	}
	if !strings.HasPrefix(album.ArtworkURL, "/api/v1/artwork/") {
		t.Fatalf("ArtworkURL = %q", album.ArtworkURL)
	}
	if album.IsFavorite || album.AlbumType != "album" {
		t.Fatalf("album = %+v", album)
	}
	// Sorting stability with explicit order checks.
	all, err := fx.store.ListAlbums(ctx, Filters{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, a := range all {
		titles = append(titles, a.Title)
	}
	want := []string{"1st Album", "Alpha", "Beta", "Gamma", "Solo", "海辺"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Fatalf("default order = %v", titles)
	}
	byYear, err := fx.store.ListAlbums(ctx, Filters{Sort: "year", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if byYear[0].Title != "1st Album" || byYear[len(byYear)-1].Title != "Alpha" {
		t.Fatalf("year order = %v", albumIDs(byYear))
	}
}

func TestAlbumPaginationStableAndDisjoint(t *testing.T) {
	fx := newBrowseFixture(t)
	ctx := context.Background()
	kinoko := []string{"Kinoko"}
	_ = fx.artist(t, "Kinoko")
	expected := make([]int64, 0, 25)
	for i := 0; i < 25; i++ {
		// Identical titles force ORDER BY ties that only a.id can break.
		id := fx.album(t, "Same", 2000+i, "Kinoko", kinoko)
		expected = append(expected, id)
	}
	seen := map[int64]int{}
	collected := []int64{}
	for page := 0; page < 4; page++ {
		list, err := fx.store.ListAlbums(ctx, Filters{Limit: 10, Offset: page * 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, album := range list {
			seen[album.ID]++
			collected = append(collected, album.ID)
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("album %d seen %d times", id, n)
		}
	}
	if len(collected) != 25 {
		t.Fatalf("collected %d", len(collected))
	}
	// Ties on sort_title resolve by id ascending.
	if fmt.Sprint(collected) != fmt.Sprint(expected) {
		t.Fatalf("order = %v want %v", collected, expected)
	}
	count, err := fx.store.CountAlbums(ctx, Filters{})
	if err != nil {
		t.Fatal(err)
	}
	if count != 25 {
		t.Fatalf("count=%d", count)
	}
}

func TestAlbumByIDEffectiveGenres(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	gamma, err := fx.store.AlbumByID(ctx, fx.albums["Gamma"])
	if err != nil {
		t.Fatal(err)
	}
	if gamma.Genres != "Classical,Soundtrack" {
		t.Fatalf("Gamma genres = %q, want album override in position order", gamma.Genres)
	}
	alpha, err := fx.store.AlbumByID(ctx, fx.albums["Alpha"])
	if err != nil {
		t.Fatal(err)
	}
	if alpha.Genres != "Rock,Jazz" {
		t.Fatalf("Alpha genres = %q, want effective union", alpha.Genres)
	}
	beta, err := fx.store.AlbumByID(ctx, fx.albums["Beta"])
	if err != nil {
		t.Fatal(err)
	}
	if beta.Genres != "Rock" {
		t.Fatalf("Beta genres = %q", beta.Genres)
	}
}

func TestTrackListCountConsistency(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	if err := fx.store.UpdateArtist(ctx, fx.artists["Teikoku"], "帝国"); err != nil {
		t.Fatal(err)
	}
	composer := fx.artists["Composer X"]
	kinoko := fx.artists["Kinoko"]

	cases := []struct {
		name    string
		filters Filters
		want    int
	}{
		{"all", Filters{}, 7},
		{"hide instrumental", Filters{HideInstrumental: true}, 6},
		{"query track title", Filters{Query: "Alpha One"}, 1},
		{"query album title", Filters{Query: "Gamma"}, 1},
		{"query composer credit role", Filters{Query: "Composer X"}, 1},
		{"query user display name", Filters{Query: "帝国"}, 1},
		{"artist any role", Filters{ArtistID: composer}, 1},
		{"artist primary", Filters{ArtistID: kinoko}, 4},
		{"genre hidden by override", Filters{Genre: "Blues"}, 0},
		{"genre from override", Filters{Genre: "Soul"}, 1},
		{"genre raw", Filters{Genre: "Rock"}, 3},
		{"genre track override", Filters{Genre: "Jazz"}, 1},
		{"genre override hides raw", Filters{Genre: "Rock", ArtistID: composer}, 0},
		{"year", Filters{Year: 2010}, 1},
		{"album id", Filters{AlbumID: fx.albums["Alpha"]}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.filters
			f.Limit = 100
			list, err := fx.store.ListTracks(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			count, err := fx.store.CountTracks(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			if int(count) != tc.want || len(list) != tc.want {
				t.Fatalf("list=%d count=%d want=%d", len(list), count, tc.want)
			}
		})
	}
}

func TestArtistAlbumOptions(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()

	albumRole, err := fx.store.ListArtistOptions(ctx, "album", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	// Composer X only has a track credit: absent from album role, present in track role.
	names := []string{}
	for _, a := range albumRole {
		names = append(names, a.Name)
	}
	if strings.Contains(strings.Join(names, "|"), "Composer X") {
		t.Fatalf("album options = %v", names)
	}
	trackRole, err := fx.store.ListArtistOptions(ctx, "track", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range trackRole {
		if a.Name == "Composer X" {
			found = true
		}
	}
	if !found {
		t.Fatalf("track options missing composer: %v", trackRole)
	}
	// Query with kana variant matching (うた子 stored, query in katakana).
	filtered, err := fx.store.ListArtistOptions(ctx, "album", "ウタ", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 1 || filtered[0].Name != "うた子" {
		t.Fatalf("filtered = %+v", filtered)
	}
	// Limit is honoured.
	limited, err := fx.store.ListArtistOptions(ctx, "all", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("limited = %d", len(limited))
	}

	albums, err := fx.store.ListAlbumOptions(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 6 || albums[0].Title != "1st Album" {
		t.Fatalf("albums = %+v", albums)
	}
	filteredAlbums, err := fx.store.ListAlbumOptions(ctx, "うみべ", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(filteredAlbums) != 1 || filteredAlbums[0].Title != "海辺" {
		t.Fatalf("filteredAlbums = %+v", filteredAlbums)
	}

	// Single-id lookups for filter chips outside the first options page.
	name, err := fx.store.ArtistNameByID(ctx, fx.artists["Kinoko"])
	if err != nil || name != "Kinoko" {
		t.Fatalf("ArtistNameByID = %q, %v", name, err)
	}
	title, err := fx.store.AlbumTitleByID(ctx, fx.albums["Gamma"])
	if err != nil || title != "Gamma" {
		t.Fatalf("AlbumTitleByID = %q, %v", title, err)
	}
}

func TestListAlbumsRecentlyPlayedSort(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	// Give Beta the newest play, Alpha an older one.
	for _, tc := range []struct {
		track string
		at    string
	}{{"Alpha One", "2024-01-01T00:00:00.000Z"}, {"Beta One", "2024-06-01T00:00:00.000Z"}} {
		if _, err := fx.store.db.Exec(`INSERT INTO playback_progress(track_id,last_played_at) VALUES(?,?)`, fx.tracks[tc.track], tc.at); err != nil {
			t.Fatal(err)
		}
	}
	list, err := fx.store.ListAlbums(ctx, Filters{Sort: "recentlyPlayed", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 6 || list[0].Title != "Beta" || list[1].Title != "Alpha" {
		t.Fatalf("order = %v", albumIDs(list))
	}
	if list[0].LastPlayedAt != "2024-06-01T00:00:00.000Z" {
		t.Fatalf("LastPlayedAt = %q", list[0].LastPlayedAt)
	}
	// Albums without plays fall back to the stable sort_title order.
	var titles []string
	for _, album := range list {
		titles = append(titles, album.Title)
	}
	want := []string{"Beta", "Alpha", "1st Album", "Gamma", "Solo", "海辺"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Fatalf("order = %v, want %v", titles, want)
	}
}

func TestListAlbumsArtworkFallbackToNonPrimary(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	// Beta has only a non-primary artwork; the listing and the detail view
	// fall back to it. Gamma has no artwork at all.
	list, err := fx.store.ListAlbums(ctx, Filters{Query: "Beta", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ArtworkURL == "" {
		t.Fatalf("Beta artwork fallback missing: %+v", list)
	}
	beta, err := fx.store.AlbumByID(ctx, fx.albums["Beta"])
	if err != nil {
		t.Fatal(err)
	}
	if beta.ArtworkURL != list[0].ArtworkURL {
		t.Fatalf("list %q vs detail %q", list[0].ArtworkURL, beta.ArtworkURL)
	}
	gamma, err := fx.store.AlbumByID(ctx, fx.albums["Gamma"])
	if err != nil {
		t.Fatal(err)
	}
	if gamma.ArtworkURL != "" {
		t.Fatalf("Gamma artwork = %q, want empty", gamma.ArtworkURL)
	}
}

func TestTrackListComposerFilterKeepsPrimaryArtist(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	// Filtering by a credit-role artist must not leak the credit name into
	// the display Artist field: it stays the primary artist.
	list, err := fx.store.ListTracks(ctx, Filters{ArtistID: fx.artists["Composer X"], Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "Alpha Two" {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Artist != "Kinoko" {
		t.Fatalf("Artist = %q, want primary artist", list[0].Artist)
	}
}

func TestUpdateTrackKeepsAlbumOnlyGenreOverride(t *testing.T) {
	fx := buildStandardFixture(t)
	ctx := context.Background()
	// Unique genre only referenced by Gamma's album-level override.
	fx.setAlbumGenreOverrides(t, fx.albums["海辺"], []string{"AlbumOnlyGenre"})
	trackID := fx.tracks["海辺 One"]

	// Editing the track's genres triggers orphan genre cleanup; the
	// album-only genre must survive (previously deleted, cascading the
	// album override away).
	if err := fx.store.UpdateTrack(ctx, trackID, "海辺 One", 1, 1, "", "", []string{"Pop"}); err != nil {
		t.Fatal(err)
	}
	var exists int
	if err := fx.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM genres WHERE name='AlbumOnlyGenre')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 1 {
		t.Fatal("album-only genre was deleted by UpdateTrack cleanup")
	}
	var override int
	if err := fx.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM album_genre_overrides ago JOIN genres g ON g.id=ago.genre_id WHERE ago.album_id=? AND g.name='AlbumOnlyGenre')`, fx.albums["海辺"]).Scan(&override); err != nil {
		t.Fatal(err)
	}
	if override != 1 {
		t.Fatal("album genre override lost")
	}
	list, err := fx.store.ListAlbums(ctx, Filters{Genre: "AlbumOnlyGenre", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Title != "海辺" {
		t.Fatalf("genre filter = %+v", list)
	}
	album, err := fx.store.AlbumByID(ctx, fx.albums["海辺"])
	if err != nil {
		t.Fatal(err)
	}
	if album.Genres != "AlbumOnlyGenre" {
		t.Fatalf("genres = %q", album.Genres)
	}
}
