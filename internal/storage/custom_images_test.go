package storage

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

// importFileWithArtwork imports one track with embedded artwork; bump
// modified to simulate a rescan, change hash to simulate changed embedded art.
func (f albumMergeFixture) importFileWithArtwork(t *testing.T, path, album, title, hash string, modified int) {
	t.Helper()
	input := ImportInput{LibraryID: f.library, RelativePath: path, FileSize: 100, ModifiedAtNS: int64(modified), Metadata: metadata.AudioMetadata{Title: title, Album: album, Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}, Artwork: &ArtworkInput{Hash: hash, MIMEType: "image/jpeg", CachePath: t.TempDir() + "/" + hash + ".jpg", SourceType: "embedded", SourcePath: path, ByteSize: 1000}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
}

// customImage builds an input for a file name under <data>/custom-images/;
// only the bare name is stored (M3), so tests use plain names.
func (f albumMergeFixture) customImage(name string) CustomImageInput {
	return CustomImageInput{Hash: "customhash-" + name, MIMEType: "image/png", FileName: name, Width: 600, Height: 600, ByteSize: 12345}
}

func (f albumMergeFixture) artworkCount(t *testing.T, albumID int64, sourceType string) int {
	t.Helper()
	if sourceType == "" {
		return f.count(t, `SELECT COUNT(*) FROM artworks WHERE album_id=?`, albumID)
	}
	return f.count(t, `SELECT COUNT(*) FROM artworks WHERE album_id=? AND source_type=?`, albumID, sourceType)
}

func (f albumMergeFixture) updatedAt(t *testing.T, albumID int64) string {
	t.Helper()
	var v string
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT updated_at FROM albums WHERE id=?`, albumID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// rewindUpdatedAt moves updated_at into the past so a bump is observable even
// within the same millisecond (strftime has millisecond resolution).
func (f albumMergeFixture) rewindUpdatedAt(t *testing.T, albumID int64) {
	t.Helper()
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET updated_at='2000-01-01T00:00:00.000Z' WHERE id=?`, albumID); err != nil {
		t.Fatal(err)
	}
}

// TestCustomAlbumArtworkPreferredAcrossReads uploads a custom cover and checks
// that every cover-selection entry point returns it: album list, album detail,
// client track endpoints, sync, artist tracks and the work page.
func TestCustomAlbumArtworkPreferredAcrossReads(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "embedded-1", 1)
	albumID := f.albumID(t, "Album")
	album, err := f.store.AlbumByID(f.ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	embeddedURL := album.ArtworkURL
	if embeddedURL == "" || album.HasCustomArtwork {
		t.Fatalf("expected embedded artwork without custom flag, got %q custom=%v", embeddedURL, album.HasCustomArtwork)
	}
	before := f.updatedAt(t, albumID)
	f.rewindUpdatedAt(t, albumID)

	customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("custom.png"))
	if err != nil {
		t.Fatal(err)
	}
	wantURL := fmt.Sprintf("/api/v1/artwork/%d", customID)
	if wantURL == embeddedURL {
		t.Fatal("custom upload must produce a new artwork id")
	}
	if after := f.updatedAt(t, albumID); after <= before || after == "2000-01-01T00:00:00.000Z" {
		t.Fatalf("updated_at not bumped: %q -> %q", before, after)
	}

	// Album detail.
	album, err = f.store.AlbumByID(f.ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if album.ArtworkURL != wantURL || !album.HasCustomArtwork {
		t.Fatalf("AlbumByID artwork = %q custom=%v, want %q", album.ArtworkURL, album.HasCustomArtwork, wantURL)
	}
	// Album list (browse hydration).
	albums, err := f.store.ListAlbums(f.ctx, Filters{Limit: 10})
	if err != nil || len(albums) != 1 {
		t.Fatalf("ListAlbums = %v, %v", albums, err)
	}
	if albums[0].ArtworkURL != wantURL {
		t.Fatalf("ListAlbums artwork = %q, want %q", albums[0].ArtworkURL, wantURL)
	}
	// Client track list + track detail.
	tracks, err := f.store.ListTracks(f.ctx, Filters{AlbumID: albumID, Limit: 10})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("ListTracks = %v, %v", tracks, err)
	}
	if tracks[0].ArtworkURL != wantURL {
		t.Fatalf("ListTracks artwork = %q, want %q", tracks[0].ArtworkURL, wantURL)
	}
	track, err := f.store.TrackByID(f.ctx, tracks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if track.ArtworkURL != wantURL {
		t.Fatalf("TrackByID artwork = %q, want %q", track.ArtworkURL, wantURL)
	}
	// Sync albums + tracks.
	syncAlbums, err := f.store.SyncAlbums(f.ctx, SyncAlbumsParams{Limit: 10})
	if err != nil || len(syncAlbums.Items) != 1 {
		t.Fatalf("SyncAlbums = %v, %v", syncAlbums, err)
	}
	if syncAlbums.Items[0].ArtworkURL != wantURL {
		t.Fatalf("SyncAlbums artwork = %q, want %q", syncAlbums.Items[0].ArtworkURL, wantURL)
	}
	syncTracks, err := f.store.SyncTracks(f.ctx, SyncTracksParams{Limit: 10})
	if err != nil || len(syncTracks.Items) != 1 {
		t.Fatalf("SyncTracks = %v, %v", syncTracks, err)
	}
	if syncTracks.Items[0].ArtworkURL != wantURL {
		t.Fatalf("SyncTracks artwork = %q, want %q", syncTracks.Items[0].ArtworkURL, wantURL)
	}
	// Artist page track list.
	var artistID int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT artist_id FROM album_artists WHERE album_id=?`, albumID).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	artistTracks, _, err := f.store.ArtistTracks(f.ctx, artistID, ArtistTrackQuery{Limit: 100})
	if err != nil || len(artistTracks) != 1 {
		t.Fatalf("ArtistTracks = %v, %v", artistTracks, err)
	}
	if artistTracks[0].ArtworkURL != wantURL {
		t.Fatalf("ArtistTracks artwork = %q, want %q", artistTracks[0].ArtworkURL, wantURL)
	}
	// Work page.
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO works(title,normalized_title,type) VALUES('Work','work','anime')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source) VALUES(?,1,'ost','manual')`, albumID); err != nil {
		t.Fatal(err)
	}
	workTracks, err := f.store.TracksForWork(f.ctx, 1)
	if err != nil || len(workTracks) != 1 {
		t.Fatalf("TracksForWork = %v, %v", workTracks, err)
	}
	if workTracks[0].ArtworkURL != wantURL {
		t.Fatalf("TracksForWork artwork = %q, want %q", workTracks[0].ArtworkURL, wantURL)
	}
	// Playlist cover prefers the custom cover of its first album.
	playlist, err := f.store.CreatePlaylistWithItems(f.ctx, "Mix", "", []int64{tracks[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := f.store.PlaylistByID(f.ctx, playlist.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ArtworkURL != wantURL {
		t.Fatalf("PlaylistByID artwork = %q, want %q", loaded.ArtworkURL, wantURL)
	}
}

// TestCustomAlbumArtworkReplaceProducesNewID: every upload creates a new
// artwork id (cache invalidation) and replaces the previous custom row.
func TestCustomAlbumArtworkReplaceProducesNewID(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "embedded-1", 1)
	albumID := f.albumID(t, "Album")
	firstID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("one.png"))
	if err != nil {
		t.Fatal(err)
	}
	secondID, oldNames, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("two.png"))
	if err != nil {
		t.Fatal(err)
	}
	if secondID == firstID {
		t.Fatal("replacement upload must produce a new artwork id")
	}
	if got := f.artworkCount(t, albumID, "custom"); got != 1 {
		t.Fatalf("custom rows = %d, want 1", got)
	}
	if len(oldNames) != 1 || oldNames[0] != "one.png" {
		t.Fatalf("old names = %v", oldNames)
	}
	album, err := f.store.AlbumByID(f.ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if album.ArtworkURL != fmt.Sprintf("/api/v1/artwork/%d", secondID) {
		t.Fatalf("artwork = %q after replace", album.ArtworkURL)
	}
}

// TestResetCustomAlbumArtwork restores the embedded cover and bumps updated_at.
func TestResetCustomAlbumArtwork(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "embedded-1", 1)
	albumID := f.albumID(t, "Album")
	album, _ := f.store.AlbumByID(f.ctx, albumID)
	embeddedURL := album.ArtworkURL
	if _, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("c.png")); err != nil {
		t.Fatal(err)
	}
	before := f.updatedAt(t, albumID)
	f.rewindUpdatedAt(t, albumID)
	oldNames, err := f.store.ResetCustomAlbumArtwork(f.ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldNames) != 1 || oldNames[0] != "c.png" {
		t.Fatalf("old names = %v", oldNames)
	}
	if got := f.artworkCount(t, albumID, "custom"); got != 0 {
		t.Fatalf("custom rows = %d after reset", got)
	}
	album, err = f.store.AlbumByID(f.ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if album.ArtworkURL != embeddedURL || album.HasCustomArtwork {
		t.Fatalf("after reset artwork = %q custom=%v, want %q", album.ArtworkURL, album.HasCustomArtwork, embeddedURL)
	}
	if after := f.updatedAt(t, albumID); after < before || after == "2000-01-01T00:00:00.000Z" {
		t.Fatalf("updated_at not bumped on reset: %q -> %q", before, after)
	}
}

// TestRescanKeepsCustomArtwork: a rescan (even with changed embedded art)
// never overwrites or shadows the custom cover.
func TestRescanKeepsCustomArtwork(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "embedded-1", 1)
	albumID := f.albumID(t, "Album")
	customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("c.png"))
	if err != nil {
		t.Fatal(err)
	}
	wantURL := fmt.Sprintf("/api/v1/artwork/%d", customID)
	// Rescan with new embedded artwork: the custom row must survive and stay
	// preferred, and the new embedded row must not mark itself primary.
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "embedded-2", 2)
	if got := f.artworkCount(t, albumID, "custom"); got != 1 {
		t.Fatalf("custom rows = %d after rescan", got)
	}
	var primaryNonCustom int
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM artworks WHERE album_id=? AND source_type<>'custom' AND is_primary=1`, albumID).Scan(&primaryNonCustom); err != nil {
		t.Fatal(err)
	}
	album, err := f.store.AlbumByID(f.ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if album.ArtworkURL != wantURL {
		t.Fatalf("artwork = %q after rescan, want %q", album.ArtworkURL, wantURL)
	}
}

// TestRescanKeepsCustomArtworkNoInitialArt: an album imported without any
// artwork gets a custom cover; a later rescan adding embedded art must not
// mark it primary over the custom cover (the custom-first rule covers reads,
// and the scanner write must not crown a new primary either).
func TestRescanKeepsCustomArtworkNoInitialArt(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	albumID := f.albumID(t, "Album")
	customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("c.png"))
	if err != nil {
		t.Fatal(err)
	}
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "embedded-1", 2)
	var primary int
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT is_primary FROM artworks WHERE album_id=? AND source_type='embedded'`, albumID).Scan(&primary); err != nil {
		t.Fatal(err)
	}
	if primary != 0 {
		t.Fatal("embedded artwork must not become primary while a custom cover exists")
	}
	album, err := f.store.AlbumByID(f.ctx, albumID)
	if err != nil {
		t.Fatal(err)
	}
	if album.ArtworkURL != fmt.Sprintf("/api/v1/artwork/%d", customID) {
		t.Fatalf("artwork = %q after rescan", album.ArtworkURL)
	}
}

// TestMergeAlbumsCustomArtworkRules covers D-merge: target custom wins;
// otherwise the first source's custom transfers; non-custom behavior stays.
func TestMergeAlbumsCustomArtworkRules(t *testing.T) {
	t.Run("target custom keeps, source custom dropped", func(t *testing.T) {
		f := newAlbumMergeFixture(t)
		f.importFileWithArtwork(t, "T/01.flac", "Target", "A", "emb-t", 1)
		f.importFileWithArtwork(t, "S/01.flac", "Source", "B", "emb-s", 1)
		target, source := f.albumID(t, "Target"), f.albumID(t, "Source")
		customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, target, f.customImage("t.png"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, source, f.customImage("s.png")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MergeAlbums(f.ctx, target, []int64{source}); err != nil {
			t.Fatal(err)
		}
		album, err := f.store.AlbumByID(f.ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if album.ArtworkURL != fmt.Sprintf("/api/v1/artwork/%d", customID) {
			t.Fatalf("target custom not kept: %q", album.ArtworkURL)
		}
		if got := f.artworkCount(t, target, "custom"); got != 1 {
			t.Fatalf("custom rows = %d, want 1 (source custom dropped)", got)
		}
	})
	t.Run("target without custom inherits source custom", func(t *testing.T) {
		f := newAlbumMergeFixture(t)
		f.importFileWithArtwork(t, "T/01.flac", "Target", "A", "emb-t", 1)
		f.importFileWithArtwork(t, "S/01.flac", "Source", "B", "emb-s", 1)
		target, source := f.albumID(t, "Target"), f.albumID(t, "Source")
		customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, source, f.customImage("s.png"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MergeAlbums(f.ctx, target, []int64{source}); err != nil {
			t.Fatal(err)
		}
		album, err := f.store.AlbumByID(f.ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if album.ArtworkURL != fmt.Sprintf("/api/v1/artwork/%d", customID) {
			t.Fatalf("source custom not transferred: %q", album.ArtworkURL)
		}
		// The target's own embedded artwork must still be there.
		if got := f.artworkCount(t, target, "embedded"); got != 1 {
			t.Fatalf("embedded rows = %d, want 1", got)
		}
	})
	t.Run("first source custom wins over later sources", func(t *testing.T) {
		f := newAlbumMergeFixture(t)
		f.importFileWithArtwork(t, "T/01.flac", "Target", "A", "emb-t", 1)
		f.importFileWithArtwork(t, "S1/01.flac", "Source1", "B", "emb-s1", 1)
		f.importFileWithArtwork(t, "S2/01.flac", "Source2", "C", "emb-s2", 1)
		target := f.albumID(t, "Target")
		source1, source2 := f.albumID(t, "Source1"), f.albumID(t, "Source2")
		customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, source1, f.customImage("s1.png"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, source2, f.customImage("s2.png")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MergeAlbums(f.ctx, target, []int64{source1, source2}); err != nil {
			t.Fatal(err)
		}
		album, err := f.store.AlbumByID(f.ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if album.ArtworkURL != fmt.Sprintf("/api/v1/artwork/%d", customID) {
			t.Fatalf("first source custom should win: %q", album.ArtworkURL)
		}
		if got := f.artworkCount(t, target, "custom"); got != 1 {
			t.Fatalf("custom rows = %d, want 1", got)
		}
	})
	t.Run("non-custom move behavior unchanged", func(t *testing.T) {
		f := newAlbumMergeFixture(t)
		f.importFileWithArtwork(t, "T/01.flac", "Target", "A", "emb-t", 1)
		f.importFileWithArtwork(t, "S/01.flac", "Source", "B", "emb-s", 1)
		target, source := f.albumID(t, "Target"), f.albumID(t, "Source")
		// Target has artwork: source embedded stays away (historical rule).
		if _, err := f.store.MergeAlbums(f.ctx, target, []int64{source}); err != nil {
			t.Fatal(err)
		}
		if got := f.artworkCount(t, target, "embedded"); got != 1 {
			t.Fatalf("embedded rows = %d, want 1 (source embedded not moved)", got)
		}
	})
	t.Run("target with only custom also receives source embedded (L-c)", func(t *testing.T) {
		f := newAlbumMergeFixture(t)
		f.importFile(t, "T/01.flac", "Target", "A", 1, 1)
		f.importFileWithArtwork(t, "S/01.flac", "Source", "B", "emb-s", 1)
		target, source := f.albumID(t, "Target"), f.albumID(t, "Source")
		customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, target, f.customImage("t.png"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MergeAlbums(f.ctx, target, []int64{source}); err != nil {
			t.Fatal(err)
		}
		album, err := f.store.AlbumByID(f.ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if album.ArtworkURL != fmt.Sprintf("/api/v1/artwork/%d", customID) {
			t.Fatalf("custom cover must stay primary: %q", album.ArtworkURL)
		}
		if got := f.artworkCount(t, target, "embedded"); got != 1 {
			t.Fatalf("embedded rows = %d, want 1 (source embedded follows despite custom)", got)
		}
		// After 恢复默认 the moved embedded cover takes over instead of nothing.
		embedded, err := f.store.AlbumByID(f.ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.ResetCustomAlbumArtwork(f.ctx, target); err != nil {
			t.Fatal(err)
		}
		album, err = f.store.AlbumByID(f.ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		var embeddedURL string
		if err := f.store.db.QueryRowContext(f.ctx, `SELECT '/api/v1/artwork/'||id FROM artworks WHERE album_id=? AND source_type='embedded'`, target).Scan(&embeddedURL); err != nil {
			t.Fatal(err)
		}
		if album.ArtworkURL != embeddedURL {
			t.Fatalf("after reset artwork = %q, want moved embedded %q (was %q)", album.ArtworkURL, embeddedURL, embedded.ArtworkURL)
		}
	})
	t.Run("empty target receives source embedded", func(t *testing.T) {
		f := newAlbumMergeFixture(t)
		f.importFile(t, "T/01.flac", "Target", "A", 1, 1)
		f.importFileWithArtwork(t, "S/01.flac", "Source", "B", "emb-s", 1)
		target, source := f.albumID(t, "Target"), f.albumID(t, "Source")
		if _, err := f.store.MergeAlbums(f.ctx, target, []int64{source}); err != nil {
			t.Fatal(err)
		}
		if got := f.artworkCount(t, target, "embedded"); got != 1 {
			t.Fatalf("embedded rows = %d, want 1", got)
		}
	})
	t.Run("empty target receives source custom and embedded", func(t *testing.T) {
		f := newAlbumMergeFixture(t)
		f.importFile(t, "T/01.flac", "Target", "A", 1, 1)
		f.importFileWithArtwork(t, "S/01.flac", "Source", "B", "emb-s", 1)
		target, source := f.albumID(t, "Target"), f.albumID(t, "Source")
		customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, source, f.customImage("s.png"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MergeAlbums(f.ctx, target, []int64{source}); err != nil {
			t.Fatal(err)
		}
		album, err := f.store.AlbumByID(f.ctx, target)
		if err != nil {
			t.Fatal(err)
		}
		if album.ArtworkURL != fmt.Sprintf("/api/v1/artwork/%d", customID) {
			t.Fatalf("custom not transferred: %q", album.ArtworkURL)
		}
		if got := f.artworkCount(t, target, "embedded"); got != 1 {
			t.Fatalf("embedded rows = %d, want 1 (embedded follows custom)", got)
		}
	})
}

// TestCleanupOrphansCascadesCustomArtwork (D31): when a trackless album is
// cleaned up, its custom artwork rows disappear and the file becomes
// unreferenced for the GC.
func TestCleanupOrphansCascadesCustomArtwork(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "emb-1", 1)
	albumID := f.albumID(t, "Album")
	if _, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("cascade.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `DELETE FROM tracks WHERE album_id=?`, albumID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CleanupOrphans(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.count(t, `SELECT COUNT(*) FROM albums WHERE id=?`, albumID); got != 0 {
		t.Fatalf("album still present: %d", got)
	}
	if got := f.artworkCount(t, albumID, "custom"); got != 0 {
		t.Fatalf("custom rows = %d after album cleanup", got)
	}
	refs, err := f.store.CustomImageFiles(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if refs["cascade.png"] {
		t.Fatal("file name should be unreferenced after cascade")
	}
}

// TestArtistCustomImagePreferredAndProtected: custom artist images win over
// the Last.fm/MusicBrainz cache everywhere, carry a ?v= version, and cache
// refreshes cannot overwrite them.
func TestArtistCustomImagePreferredAndProtected(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	albumID := f.albumID(t, "Album")
	var artistID int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT artist_id FROM album_artists WHERE album_id=?`, albumID).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	plainURL := fmt.Sprintf("/api/v1/artists/%d/image", artistID)
	// Seed the automatic cache first (Last.fm/MusicBrainz path).
	if err := f.store.SaveArtistImage(f.ctx, ArtistImageInput{ArtistID: artistID, Source: "lastfm", RemoteURL: "https://img", Hash: "h", MIMEType: "image/jpeg", CachePath: t.TempDir() + "/cache.jpg", ByteSize: 10}); err != nil {
		t.Fatal(err)
	}
	detail, err := f.store.ArtistDetail(f.ctx, artistID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ImageURL != plainURL || detail.HasCustomImage {
		t.Fatalf("cache image URL = %q custom=%v, want %q", detail.ImageURL, detail.HasCustomImage, plainURL)
	}

	if _, err := f.store.SaveCustomArtistImage(f.ctx, artistID, f.customImage("artist.png")); err != nil {
		t.Fatal(err)
	}
	detail, err = f.store.ArtistDetail(f.ctx, artistID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(detail.ImageURL, plainURL+"?v=") || !detail.HasCustomImage {
		t.Fatalf("custom image URL = %q custom=%v", detail.ImageURL, detail.HasCustomImage)
	}
	versionedURL := detail.ImageURL

	// Artist list, album artists and per-track artists all prefer custom.
	artists, err := f.store.ListArtists(f.ctx, Filters{Limit: 10})
	if err != nil || len(artists) != 1 {
		t.Fatalf("ListArtists = %v, %v", artists, err)
	}
	if artists[0].ImageURL != versionedURL {
		t.Fatalf("ListArtists image = %q, want %q", artists[0].ImageURL, versionedURL)
	}
	albumArtists, err := f.store.ArtistsForAlbum(f.ctx, albumID)
	if err != nil || len(albumArtists) != 1 {
		t.Fatalf("ArtistsForAlbum = %v, %v", albumArtists, err)
	}
	if albumArtists[0].ImageURL != versionedURL {
		t.Fatalf("ArtistsForAlbum image = %q, want %q", albumArtists[0].ImageURL, versionedURL)
	}
	trackArtists, err := f.store.ArtistsForAlbumTracks(f.ctx, albumID)
	if err != nil || len(trackArtists) != 1 {
		t.Fatalf("ArtistsForAlbumTracks = %v, %v", trackArtists, err)
	}
	for _, list := range trackArtists {
		if list[0].ImageURL != versionedURL {
			t.Fatalf("ArtistsForAlbumTracks image = %q, want %q", list[0].ImageURL, versionedURL)
		}
	}
	// The served file is the custom one.
	gotName, gotMIME, isCustom, err := f.store.ArtistImagePath(f.ctx, artistID)
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "artist.png" || gotMIME != "image/png" || !isCustom {
		t.Fatalf("ArtistImagePath = %q, %q, custom=%v", gotName, gotMIME, isCustom)
	}
	// An automatic refresh of the cache must not shadow the custom image.
	if err := f.store.SaveArtistImage(f.ctx, ArtistImageInput{ArtistID: artistID, Source: "musicbrainz", RemoteURL: "https://img2", Hash: "h2", MIMEType: "image/jpeg", CachePath: t.TempDir() + "/cache2.jpg", ByteSize: 10}); err != nil {
		t.Fatal(err)
	}
	detail, err = f.store.ArtistDetail(f.ctx, artistID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ImageURL != versionedURL {
		t.Fatalf("cache refresh overrode custom image: %q", detail.ImageURL)
	}
	gotName, _, isCustom, err = f.store.ArtistImagePath(f.ctx, artistID)
	if err != nil || gotName != "artist.png" || !isCustom {
		t.Fatalf("ArtistImagePath after refresh = %q, custom=%v, %v", gotName, isCustom, err)
	}

	// Reset restores the cache-backed plain URL.
	oldNames, err := f.store.ResetCustomArtistImage(f.ctx, artistID)
	if err != nil || len(oldNames) != 1 || oldNames[0] != "artist.png" {
		t.Fatalf("ResetCustomArtistImage = %v, %v", oldNames, err)
	}
	detail, err = f.store.ArtistDetail(f.ctx, artistID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.ImageURL != plainURL || detail.HasCustomImage {
		t.Fatalf("after reset image = %q custom=%v, want %q", detail.ImageURL, detail.HasCustomImage, plainURL)
	}
}

// TestArtistMergeInheritsCustomImage: merging artist S into T keeps T's custom
// image; when T has none, S's custom image shows through (merged-from
// inheritance, rollback-safe).
func TestArtistMergeInheritsCustomImage(t *testing.T) {
	setup := func(t *testing.T) (albumMergeFixture, int64, int64) {
		f := newAlbumMergeFixture(t)
		f.importFile(t, "T/01.flac", "TargetAlbum", "A", 1, 1)
		if err := f.store.ImportTrack(f.ctx, ImportInput{LibraryID: f.library, RelativePath: "S/01.flac", FileSize: 100, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "B", Album: "SourceAlbum", Artists: []string{"Other"}, AlbumArtists: []string{"Other"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
			t.Fatal(err)
		}
		var targetID, sourceID int64
		if err := f.store.db.QueryRowContext(f.ctx, `SELECT id FROM artists WHERE display_name='Singer'`).Scan(&targetID); err != nil {
			t.Fatal(err)
		}
		if err := f.store.db.QueryRowContext(f.ctx, `SELECT id FROM artists WHERE display_name='Other'`).Scan(&sourceID); err != nil {
			t.Fatal(err)
		}
		return f, targetID, sourceID
	}
	t.Run("target without custom inherits source custom", func(t *testing.T) {
		f, targetID, sourceID := setup(t)
		if _, err := f.store.SaveCustomArtistImage(f.ctx, sourceID, f.customImage("s.png")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MergeArtists(f.ctx, sourceID, targetID); err != nil {
			t.Fatal(err)
		}
		gotName, _, isCustom, err := f.store.ArtistImagePath(f.ctx, targetID)
		if err != nil || gotName != "s.png" || !isCustom {
			t.Fatalf("ArtistImagePath = %q, custom=%v, %v, want inherited custom s.png", gotName, isCustom, err)
		}
		detail, err := f.store.ArtistDetail(f.ctx, targetID)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail.ImageURL, "?v=") {
			t.Fatalf("target image URL = %q, want versioned custom URL", detail.ImageURL)
		}
		// D50: inherited images count as custom and name their origin.
		if !detail.HasCustomImage || detail.CustomImageFrom != "Other" {
			t.Fatalf("HasCustomImage = %v from = %q, want true / Other", detail.HasCustomImage, detail.CustomImageFrom)
		}
	})
	t.Run("target custom wins over source custom", func(t *testing.T) {
		f, targetID, sourceID := setup(t)
		if _, err := f.store.SaveCustomArtistImage(f.ctx, targetID, f.customImage("target-own.png")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.SaveCustomArtistImage(f.ctx, sourceID, f.customImage("s.png")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MergeArtists(f.ctx, sourceID, targetID); err != nil {
			t.Fatal(err)
		}
		gotName, _, _, err := f.store.ArtistImagePath(f.ctx, targetID)
		if err != nil || gotName != "target-own.png" {
			t.Fatalf("ArtistImagePath = %q, %v, want target custom target-own.png", gotName, err)
		}
		detail, err := f.store.ArtistDetail(f.ctx, targetID)
		if err != nil {
			t.Fatal(err)
		}
		if !detail.HasCustomImage || detail.CustomImageFrom != "" {
			t.Fatalf("HasCustomImage = %v from = %q, want true / own", detail.HasCustomImage, detail.CustomImageFrom)
		}
	})
	t.Run("reset on target removes own and inherited custom rows", func(t *testing.T) {
		f, targetID, sourceID := setup(t)
		// Target has its own automatic cache image to fall back to.
		if err := f.store.SaveArtistImage(f.ctx, ArtistImageInput{ArtistID: targetID, Source: "lastfm", RemoteURL: "https://img", Hash: "h", MIMEType: "image/jpeg", CachePath: "cache.jpg", ByteSize: 10}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.SaveCustomArtistImage(f.ctx, sourceID, f.customImage("s.png")); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.MergeArtists(f.ctx, sourceID, targetID); err != nil {
			t.Fatal(err)
		}
		oldNames, err := f.store.ResetCustomArtistImage(f.ctx, targetID)
		if err != nil {
			t.Fatal(err)
		}
		if len(oldNames) != 1 || oldNames[0] != "s.png" {
			t.Fatalf("old names = %v, want [s.png] (merged-from row removed too)", oldNames)
		}
		if got := f.count(t, `SELECT COUNT(*) FROM artist_custom_images`); got != 0 {
			t.Fatalf("custom rows = %d after reset, want 0", got)
		}
		detail, err := f.store.ArtistDetail(f.ctx, targetID)
		if err != nil {
			t.Fatal(err)
		}
		wantURL := fmt.Sprintf("/api/v1/artists/%d/image", targetID)
		if detail.ImageURL != wantURL || detail.HasCustomImage {
			t.Fatalf("after reset image = %q custom=%v, want own cache %q", detail.ImageURL, detail.HasCustomImage, wantURL)
		}
		cachePath, _, isCustom, err := f.store.ArtistImagePath(f.ctx, targetID)
		if err != nil || cachePath != "cache.jpg" || isCustom {
			t.Fatalf("ArtistImagePath after reset = %q, custom=%v, %v", cachePath, isCustom, err)
		}
	})
}

// TestCustomImageFilesReferences covers the union used by the GC.
func TestCustomImageFilesReferences(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "emb-1", 1)
	albumID := f.albumID(t, "Album")
	var artistID int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT artist_id FROM album_artists WHERE album_id=?`, albumID).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("a.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveCustomArtistImage(f.ctx, artistID, f.customImage("ar.png")); err != nil {
		t.Fatal(err)
	}
	refs, err := f.store.CustomImageFiles(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !refs["a.png"] || !refs["ar.png"] || len(refs) != 2 {
		t.Fatalf("refs = %v", refs)
	}
}

// TestSaveCustomImageMissingTarget rejects uploads for unknown ids.
func TestSaveCustomImageMissingTarget(t *testing.T) {
	f := newAlbumMergeFixture(t)
	if _, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, 9999, f.customImage("x")); err == nil {
		t.Fatal("expected error for missing album")
	}
	if _, err := f.store.SaveCustomArtistImage(f.ctx, 9999, f.customImage("x")); err == nil {
		t.Fatal("expected error for missing artist")
	}
}

// TestSyncAlbumsReflectsCustomArtwork: upload and reset both change the
// album's updated_at and the artwork URL visible to Sync clients.
func TestSyncAlbumsReflectsCustomArtwork(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFileWithArtwork(t, "A/01.flac", "Album", "Song", "emb-1", 1)
	albumID := f.albumID(t, "Album")
	before, err := f.store.SyncAlbums(f.ctx, SyncAlbumsParams{Limit: 10})
	if err != nil || len(before.Items) != 1 {
		t.Fatalf("SyncAlbums = %v, %v", before, err)
	}
	f.rewindUpdatedAt(t, albumID)
	customID, _, err := f.store.SaveCustomAlbumArtwork(f.ctx, albumID, f.customImage("c.png"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.store.SyncAlbums(f.ctx, SyncAlbumsParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if after.Items[0].UpdatedAt <= before.Items[0].UpdatedAt {
		t.Fatalf("updated_at not bumped: %q -> %q", before.Items[0].UpdatedAt, after.Items[0].UpdatedAt)
	}
	if after.Items[0].ArtworkURL != fmt.Sprintf("/api/v1/artwork/%d", customID) {
		t.Fatalf("sync artwork = %q", after.Items[0].ArtworkURL)
	}
	f.rewindUpdatedAt(t, albumID)
	if _, err := f.store.ResetCustomAlbumArtwork(f.ctx, albumID); err != nil {
		t.Fatal(err)
	}
	reset, err := f.store.SyncAlbums(f.ctx, SyncAlbumsParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if reset.Items[0].ArtworkURL != before.Items[0].ArtworkURL {
		t.Fatalf("after reset sync artwork = %q, want %q", reset.Items[0].ArtworkURL, before.Items[0].ArtworkURL)
	}
	if reset.Items[0].UpdatedAt < after.Items[0].UpdatedAt || reset.Items[0].UpdatedAt == "2000-01-01T00:00:00.000Z" {
		t.Fatalf("updated_at not bumped on reset: %q -> %q", after.Items[0].UpdatedAt, reset.Items[0].UpdatedAt)
	}
}

// TestCustomArtistImageWithRelativeCachePath mirrors the HTTP-layer relative
// data dir case at storage level: an automatic cache row may carry a
// relative cache_path (the scanner stores paths verbatim when the data dir is
// relative). A custom upload still wins and is stored as a bare file name;
// after a reset the relative cache path comes back verbatim — the storage
// layer never absolutizes it.
func TestCustomArtistImageWithRelativeCachePath(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album", "Song", 1, 1)
	var artistID int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT id FROM artists WHERE display_name='Singer'`).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	relativeCache := "artist-cache/singer.jpg"
	if err := f.store.SaveArtistImage(f.ctx, ArtistImageInput{ArtistID: artistID, Source: "lastfm", RemoteURL: "https://img", Hash: "h1", MIMEType: "image/jpeg", CachePath: relativeCache, ByteSize: 10}); err != nil {
		t.Fatal(err)
	}
	gotName, _, isCustom, err := f.store.ArtistImagePath(f.ctx, artistID)
	if err != nil || gotName != relativeCache || isCustom {
		t.Fatalf("cache image = %q custom=%v, want the relative path verbatim", gotName, isCustom)
	}
	if _, err = f.store.SaveCustomArtistImage(f.ctx, artistID, f.customImage("rel-artist.png")); err != nil {
		t.Fatal(err)
	}
	gotName, _, isCustom, err = f.store.ArtistImagePath(f.ctx, artistID)
	if err != nil || gotName != "rel-artist.png" || !isCustom {
		t.Fatalf("custom image = %q custom=%v, want bare name rel-artist.png", gotName, isCustom)
	}
	if filepath.IsAbs(gotName) || strings.ContainsAny(gotName, `/\`) {
		t.Fatalf("custom image path must stay a bare file name, got %q", gotName)
	}
	if _, err = f.store.ResetCustomArtistImage(f.ctx, artistID); err != nil {
		t.Fatal(err)
	}
	gotName, _, isCustom, err = f.store.ArtistImagePath(f.ctx, artistID)
	if err != nil || gotName != relativeCache || isCustom {
		t.Fatalf("after reset = %q custom=%v, want relative cache path verbatim", gotName, isCustom)
	}
}
