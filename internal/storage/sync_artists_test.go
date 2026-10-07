package storage

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
)

// 客户端同步模型按歌手 ID 关联：专辑与曲目下发歌手引用，歌手同步接口只列演唱者，
// 专辑数与网页歌手页“参与作品”一致（含仅演唱合辑单曲的专辑）。
func TestSyncArtistModel(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "artist-sync.db"))
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
	libID, _ := res.LastInsertId()
	team := insertRoleTestArtist(t, ctx, store, "アトラスサウンドチーム", "atlus")
	singerA := insertRoleTestArtist(t, ctx, store, "高橋あず美", "azumi")
	singerB := insertRoleTestArtist(t, ctx, store, "Lotus Juice", "lotus")
	guest := insertRoleTestArtist(t, ctx, store, "目黒将司", "meguro")
	composer := insertRoleTestArtist(t, ctx, store, "Composer Only", "composer")
	merged := insertRoleTestArtist(t, ctx, store, "Old Name", "old")

	insertAlbum := func(title string) int64 {
		r, e := store.db.ExecContext(ctx, `INSERT INTO albums(library_id,title,sort_title,grouping_key) VALUES(?,?,?,?)`, libID, title, title, title)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		return id
	}
	insertTrack := func(albumID int64, n int) int64 {
		r, e := store.db.ExecContext(ctx, `INSERT INTO tracks(album_id,title,sort_title,disc_number,track_number) VALUES(?,?,?,1,?)`, albumID, "Track "+strconv.Itoa(n), "t"+strconv.Itoa(n), n)
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		return id
	}
	exec := func(query string, args ...any) {
		if _, e := store.db.ExecContext(ctx, query, args...); e != nil {
			t.Fatal(e)
		}
	}

	ost := insertAlbum("OST")
	duet := insertAlbum("Duet")
	exec(`INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,0)`, ost, team)
	exec(`INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,0)`, duet, singerA)
	exec(`INSERT INTO album_artists(album_id,artist_id,position) VALUES(?,?,1)`, duet, singerB)
	guestTrack := insertTrack(ost, 1)
	plainTrack := insertTrack(ost, 2)
	insertTrack(duet, 1)
	exec(`INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,0,'primary')`, guestTrack, guest)
	exec(`INSERT INTO track_artists(track_id,artist_id,position,role) VALUES(?,?,0,'composer')`, plainTrack, composer)
	exec(`UPDATE artists SET merged_into_artist_id=? WHERE id=?`, team, merged)
	giveTracksFiles(t, store)

	albums, err := store.SyncAlbums(ctx, SyncAlbumsParams{})
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]SyncAlbum{}
	for _, album := range albums.Items {
		byTitle[album.Title] = album
	}
	if refs := byTitle["Duet"].Artists; len(refs) != 2 || refs[0].ID != singerA || refs[1].ID != singerB {
		t.Fatalf("duet album artists = %#v", refs)
	}
	if byTitle["Duet"].Artist != "高橋あず美, Lotus Juice" {
		t.Fatalf("duet artist text = %q", byTitle["Duet"].Artist)
	}

	tracks, err := store.SyncTracks(ctx, SyncTracksParams{AlbumID: ost})
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range tracks.Items {
		switch track.ID {
		case guestTrack:
			if len(track.Artists) != 1 || track.Artists[0].ID != guest || track.Artists[0].Name != "目黒将司" {
				t.Fatalf("guest track artists = %#v", track.Artists)
			}
		case plainTrack:
			// 作曲者不是演唱者；空引用表示沿用专辑歌手。
			if track.Artists == nil || len(track.Artists) != 0 {
				t.Fatalf("plain track artists = %#v", track.Artists)
			}
		}
	}

	page, err := store.SyncArtists(ctx, SyncArtistsParams{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || !page.HasMore || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("first artist page = %#v", page)
	}
	cursor, _ := strconv.ParseInt(page.NextCursor, 10, 64)
	rest, err := store.SyncArtists(ctx, SyncArtistsParams{Cursor: cursor, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	all := map[int64]SyncArtist{}
	for _, artist := range append(page.Items, rest.Items...) {
		all[artist.ID] = artist
	}
	if _, ok := all[composer]; ok {
		t.Fatal("credit-only artist must not be synced")
	}
	if _, ok := all[merged]; ok {
		t.Fatal("merged artist must not be synced")
	}
	if got := all[guest]; got.AlbumCount != 1 || got.TrackCount != 1 {
		t.Fatalf("guest counts = %#v", got)
	}
	if got := all[team]; got.AlbumCount != 1 || got.TrackCount != 2 {
		t.Fatalf("team counts = %#v", got)
	}
	if got := all[singerB]; got.AlbumCount != 1 || got.TrackCount != 1 {
		t.Fatalf("singer B counts = %#v", got)
	}
}
