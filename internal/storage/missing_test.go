package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

type missingFixture struct {
	store   *Store
	library Library
	tracks  map[string]int64 // relative path -> track id
}

func newMissingFixture(t *testing.T) (*missingFixture, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = store.EnsureLibrary(ctx, "Test", "/missing"); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, "/missing")
	if err != nil {
		t.Fatal(err)
	}
	fx := &missingFixture{store: store, library: library, tracks: map[string]int64{}}
	for i, file := range []struct{ path, title, album, artist, composer string }{
		{"Keep/01.flac", "Keep One", "Keep", "Keeper", "Writer"},
		{"Keep/02.flac", "Keep Two", "Keep", "Keeper", ""},
		{"Gone/01.flac", "Gone One", "Gone", "Leaver", "Writer"},
	} {
		if err = store.ImportTrack(ctx, ImportInput{LibraryID: library.ID, RelativePath: file.path, FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: file.title, Album: file.album, Artists: []string{file.artist}, AlbumArtists: []string{file.artist}, Composer: file.composer, DiscNumber: 1, TrackNumber: i + 1}}); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err = store.db.QueryRowContext(ctx, `SELECT track_id FROM audio_files WHERE relative_path=?`, file.path).Scan(&id); err != nil {
			t.Fatal(err)
		}
		fx.tracks[file.path] = id
	}
	return fx, ctx
}

func (fx *missingFixture) setMissing(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	if _, err := fx.store.db.ExecContext(ctx, `UPDATE audio_files SET status='missing' WHERE relative_path=?`, path); err != nil {
		t.Fatal(err)
	}
}

func (fx *missingFixture) fileID(t *testing.T, ctx context.Context, path string) int64 {
	t.Helper()
	var id int64
	if err := fx.store.db.QueryRowContext(ctx, `SELECT id FROM audio_files WHERE relative_path=?`, path).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func albumTitles(albums []Album) []string {
	titles := make([]string, 0, len(albums))
	for _, album := range albums {
		titles = append(titles, album.Title)
	}
	return titles
}

func TestMissingFilesAreHiddenAndComeBack(t *testing.T) {
	fx, ctx := newMissingFixture(t)
	s := fx.store
	gone := fx.tracks["Gone/01.flac"]
	keepTwo := fx.tracks["Keep/02.flac"]
	goneTrack, err := s.TrackByID(ctx, gone)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetTrackFavorite(ctx, gone, true); err != nil {
		t.Fatal(err)
	}
	if err = s.SetAlbumFavorite(ctx, goneTrack.AlbumID, true); err != nil {
		t.Fatal(err)
	}
	playlist, err := s.CreatePlaylistWithItems(ctx, "Mix", "", []int64{gone, keepTwo})
	if err != nil {
		t.Fatal(err)
	}

	fx.setMissing(t, ctx, "Gone/01.flac")
	fx.setMissing(t, ctx, "Keep/02.flac")

	albums, err := s.ListAlbums(ctx, Filters{Limit: 10})
	if err != nil || len(albums) != 1 || albums[0].Title != "Keep" || albums[0].TrackCount != 1 {
		t.Fatalf("albums = %v err=%v", albumTitles(albums), err)
	}
	if n, err := s.CountAlbums(ctx, Filters{}); err != nil || n != 1 {
		t.Fatalf("album count = %d err=%v", n, err)
	}
	options, err := s.ListAlbumOptions(ctx, "", 10)
	if err != nil || len(options) != 1 {
		t.Fatalf("album options = %+v err=%v", options, err)
	}
	tracks, err := s.ListTracks(ctx, Filters{Limit: 10})
	if err != nil || len(tracks) != 1 || tracks[0].Title != "Keep One" {
		t.Fatalf("tracks = %+v err=%v", tracks, err)
	}
	if n, err := s.CountTracks(ctx, Filters{}); err != nil || n != 1 {
		t.Fatalf("track count = %d err=%v", n, err)
	}
	artists, err := s.ListArtists(ctx, Filters{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]Artist{}
	for _, artist := range artists {
		names[artist.Name] = artist
	}
	if _, ok := names["Leaver"]; ok || names["Keeper"].AlbumCount != 1 || names["Keeper"].TrackCount != 1 {
		t.Fatalf("artists = %+v", artists)
	}
	syncAlbums, err := s.SyncAlbums(ctx, SyncAlbumsParams{})
	if err != nil || syncAlbums.Total != 1 || len(syncAlbums.Items) != 1 || syncAlbums.Items[0].TrackCount != 1 {
		t.Fatalf("sync albums = %+v err=%v", syncAlbums, err)
	}
	syncTracks, err := s.SyncTracks(ctx, SyncTracksParams{})
	if err != nil || syncTracks.Total != 1 || len(syncTracks.Items) != 1 {
		t.Fatalf("sync tracks = %+v err=%v", syncTracks, err)
	}
	syncArtists, err := s.SyncArtists(ctx, SyncArtistsParams{})
	if err != nil || syncArtists.Total != 1 || len(syncArtists.Items) != 1 || syncArtists.Items[0].Name != "Keeper" || syncArtists.Items[0].TrackCount != 1 {
		t.Fatalf("sync artists = %+v err=%v", syncArtists, err)
	}
	if favorites, total, err := s.FavoriteAlbums(ctx, 10, 0); err != nil || total != 0 || len(favorites) != 0 {
		t.Fatalf("favorite albums = %d/%d err=%v", len(favorites), total, err)
	}
	if favorites, total, err := s.FavoriteTracks(ctx, 10, 0); err != nil || total != 0 || len(favorites) != 0 {
		t.Fatalf("favorite tracks = %d/%d err=%v", len(favorites), total, err)
	}
	if credits, err := s.ListCreditArtists(ctx, CreditArtistFilters{}); err != nil || len(credits) != 1 || credits[0].TrackCount != 1 {
		t.Fatalf("credit artists = %+v err=%v", credits, err)
	}
	stats, err := s.Statistics(ctx)
	if err != nil || stats.Albums != 1 || stats.Tracks != 1 || stats.Artists != 2 || stats.MissingFiles != 2 || stats.AudioFiles != 1 {
		// Artists: Keeper + Writer (composer of the visible "Keep One").
		t.Fatalf("stats = %+v err=%v", stats, err)
	}

	// Direct lookups keep working so playlists/history links do not break.
	if _, err = s.AlbumByID(ctx, goneTrack.AlbumID); err != nil {
		t.Fatalf("hidden album lookup: %v", err)
	}
	if detail, err := s.PlaylistDetail(ctx, playlist.ID); err != nil || len(detail.Tracks) != 2 {
		t.Fatalf("playlist keeps missing items: %+v err=%v", detail, err)
	}

	// The file comes back: everything reappears with its client data.
	for _, path := range []string{"Gone/01.flac", "Keep/02.flac"} {
		if err = s.TouchAudioFile(ctx, fx.library.ID, path, false); err != nil {
			t.Fatal(err)
		}
	}
	albums, err = s.ListAlbums(ctx, Filters{Limit: 10})
	if err != nil || len(albums) != 2 {
		t.Fatalf("restored albums = %v err=%v", albumTitles(albums), err)
	}
	if favorites, total, err := s.FavoriteTracks(ctx, 10, 0); err != nil || total != 1 || favorites[0].ID != gone {
		t.Fatalf("restored favorite tracks = %+v total=%d err=%v", favorites, total, err)
	}
}

func TestPurgeMissingDeletesOnlyMissing(t *testing.T) {
	fx, ctx := newMissingFixture(t)
	s := fx.store
	gone := fx.tracks["Gone/01.flac"]
	keepTwo := fx.tracks["Keep/02.flac"]
	if err := s.SetTrackFavorite(ctx, gone, true); err != nil {
		t.Fatal(err)
	}
	playlist, err := s.CreatePlaylistWithItems(ctx, "Mix", "", []int64{gone, keepTwo})
	if err != nil {
		t.Fatal(err)
	}
	fx.setMissing(t, ctx, "Gone/01.flac")
	fx.setMissing(t, ctx, "Keep/02.flac")

	// Selected purge: an available file id in the selection is ignored.
	result, err := s.PurgeMissing(ctx, 0, []int64{fx.fileID(t, ctx, "Gone/01.flac"), fx.fileID(t, ctx, "Keep/01.flac")})
	if err != nil {
		t.Fatal(err)
	}
	if result != (PurgeResult{Files: 1, Tracks: 1, Albums: 1, Artists: 1}) {
		t.Fatalf("selected purge = %+v", result)
	}
	if _, err = s.TrackByID(ctx, gone); err == nil {
		t.Fatal("purged track still exists")
	}
	if _, err = s.TrackByID(ctx, fx.tracks["Keep/01.flac"]); err != nil {
		t.Fatalf("available track was deleted: %v", err)
	}
	after, err := s.PlaylistByID(ctx, playlist.ID)
	if err != nil || after.ItemCount != 1 || after.Revision <= playlist.Revision {
		t.Fatalf("playlist after purge = %+v (before revision %d) err=%v", after, playlist.Revision, err)
	}
	missing, total, err := s.ListMissingFiles(ctx, 10, 0)
	if err != nil || total != 1 || missing[0].RelativePath != "Keep/02.flac" || !missing[0].TrackHidden {
		t.Fatalf("remaining missing = %+v err=%v", missing, err)
	}

	// Purging everything else removes the remaining track but keeps the
	// album, which still has an available track.
	result, err = s.PurgeMissing(ctx, fx.library.ID, nil)
	if err != nil || result != (PurgeResult{Files: 1, Tracks: 1}) {
		t.Fatalf("purge all = %+v err=%v", result, err)
	}
	albums, err := s.ListAlbums(ctx, Filters{Limit: 10})
	if err != nil || len(albums) != 1 || albums[0].TrackCount != 1 {
		t.Fatalf("albums after purge = %+v err=%v", albums, err)
	}
	if result, err = s.PurgeMissing(ctx, 0, nil); err != nil || result != (PurgeResult{}) {
		t.Fatalf("purge with nothing missing = %+v err=%v", result, err)
	}
}

// A track that still has another available file stays visible and survives a
// purge of its missing copy.
func TestPurgeMissingKeepsTrackWithOtherFile(t *testing.T) {
	fx, ctx := newMissingFixture(t)
	s := fx.store
	track := fx.tracks["Gone/01.flac"]
	if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_files(library_id,track_id,relative_path,file_size,modified_at_ns,status) VALUES(?,?,'Copy/01.flac',1,1,'available')`, fx.library.ID, track); err != nil {
		t.Fatal(err)
	}
	fx.setMissing(t, ctx, "Gone/01.flac")
	if n, err := s.CountAlbums(ctx, Filters{}); err != nil || n != 2 {
		t.Fatalf("album with another available file must stay visible: %d err=%v", n, err)
	}
	missing, _, err := s.ListMissingFiles(ctx, 10, 0)
	if err != nil || len(missing) != 1 || missing[0].TrackHidden {
		t.Fatalf("missing = %+v err=%v", missing, err)
	}
	result, err := s.PurgeMissing(ctx, 0, nil)
	if err != nil || result != (PurgeResult{Files: 1}) {
		t.Fatalf("purge = %+v err=%v", result, err)
	}
	if _, err = s.TrackByID(ctx, track); err != nil {
		t.Fatalf("track with another file was deleted: %v", err)
	}
}

func TestPurgeMissingSettingRoundTrip(t *testing.T) {
	fx, ctx := newMissingFixture(t)
	s := fx.store
	if _, found, err := s.PurgeMissingSetting(ctx); err != nil || found {
		t.Fatalf("default setting found=%v err=%v", found, err)
	}
	if err := s.SavePurgeMissingSetting(ctx, "always"); err != nil {
		t.Fatal(err)
	}
	if policy, found, err := s.PurgeMissingSetting(ctx); err != nil || !found || policy != "always" {
		t.Fatalf("saved setting = %q found=%v err=%v", policy, found, err)
	}
	if err := s.SavePurgeMissingSetting(ctx, "bogus"); err == nil {
		t.Fatal("invalid policy must be rejected by the CHECK constraint")
	}
	if err := s.ResetPurgeMissingSetting(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.PurgeMissingSetting(ctx); err != nil || found {
		t.Fatalf("reset setting found=%v err=%v", found, err)
	}
}
