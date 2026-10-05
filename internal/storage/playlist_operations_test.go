package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func TestPlaylistOperations(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "playlist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO libraries(id,name,root_path) VALUES(1,'test','/test'); INSERT INTO albums(id,library_id,title,sort_title,grouping_key) VALUES(1,1,'a','a','a')`)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 5001)
	for i := range ids {
		ids[i] = int64(i + 1)
		if _, err = tx.ExecContext(ctx, `INSERT INTO tracks(id,album_id,title,sort_title) VALUES(?,1,?,?)`, ids[i], fmt.Sprint(i), fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreatePlaylist(ctx, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, batch := range [][]int64{ids[:10], ids[10:20]} {
		wg.Add(1)
		go func(batch []int64) {
			defer wg.Done()
			_, err := s.AppendPlaylistItems(ctx, p.ID, batch, nil)
			errs <- err
		}(batch)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	d, err := s.PlaylistDetail(ctx, p.ID)
	if err != nil || len(d.Tracks) != 20 || d.Playlist.Revision != 2 {
		t.Fatalf("detail %+v %v", d, err)
	}
	stale := int64(0)
	if _, err = s.RemovePlaylistItems(ctx, p.ID, ids[:1], &stale); !errors.Is(err, ErrPlaylistConflict) {
		t.Fatalf("conflict %v", err)
	}
	r, err := s.AppendPlaylistItems(ctx, p.ID, []int64{1, 21, 21, -1, 9000}, nil)
	if err != nil || r.Added != 1 || r.SkippedDuplicate != 2 || r.SkippedInvalid != 2 {
		t.Fatalf("stats %+v %v", r, err)
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM tracks WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	r, err = s.AppendPlaylistItems(ctx, p.ID, ids[21:5000], nil)
	if err != nil || r.Total != 4999 {
		t.Fatalf("holes %+v %v", r, err)
	}
	r, err = s.AppendPlaylistItems(ctx, p.ID, ids[5000:], nil)
	if err != nil || r.Total != 5000 {
		t.Fatalf("boundary %+v %v", r, err)
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO tracks(id,album_id,title,sort_title) VALUES(6000,1,'x','x')`); err != nil {
		t.Fatal(err)
	}
	_, err = s.AppendPlaylistItems(ctx, p.ID, []int64{6000}, nil)
	var capErr *PlaylistCapacityError
	if !errors.As(err, &capErr) || capErr.Remaining != 0 {
		t.Fatalf("capacity %v", err)
	}
	before, _ := s.PlaylistByID(ctx, p.ID)
	_, _, err = s.CreatePlaylistSkippingInvalid(ctx, "overflow", "", append(ids, 6000))
	if !errors.As(err, &capErr) {
		t.Fatalf("create overflow %v", err)
	}
	after, _ := s.PlaylistByID(ctx, p.ID)
	if before != after {
		t.Fatal("rejected mutation changed playlist")
	}
	_, err = s.RemovePlaylistItems(ctx, p.ID, []int64{1}, &before.Revision)
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.InsertPlaylistItemAt(ctx, p.ID, 1, 0)
	if err != nil || r.Added != 1 {
		t.Fatalf("undo %+v %v", r, err)
	}
	current, _ := s.PlaylistDetail(ctx, p.ID)
	order := make([]int64, len(current.Tracks))
	for i, v := range current.Tracks {
		order[i] = v.ID
	}
	slices.Reverse(order)
	_, err = s.ReorderPlaylistItems(ctx, p.ID, order, &current.Playlist.Revision)
	if err != nil {
		t.Fatal(err)
	}
	img := CustomImageInput{Hash: "abcdef0123456789", FileName: "shared.png", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: 1}
	if _, err = s.SaveCustomPlaylistImage(ctx, p.ID, img); err != nil {
		t.Fatal(err)
	}
	refs, err := s.CustomImageFiles(ctx)
	if err != nil || !refs[img.FileName] {
		t.Fatalf("refs %v %v", refs, err)
	}
	names, err := s.DeletePlaylistWithImages(ctx, p.ID)
	if err != nil || len(names) != 1 {
		t.Fatalf("delete %v %v", names, err)
	}
}

func TestPlaylistWriterLockBusy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "busy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.CreatePlaylist(ctx, "p", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.db.SetMaxOpenConns(1)
	if _, err = other.db.ExecContext(ctx, `PRAGMA busy_timeout=1`); err != nil {
		t.Fatal(err)
	}
	tx, err := playlistWriteTx(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = other.AppendPlaylistItems(ctx, p.ID, nil, nil)
	if !IsBusyError(err) {
		t.Fatalf("expected busy, got %v", err)
	}
}
func TestPlaylistUpgrade044(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Build the pre-044 schema using exactly the embedded historical scripts.
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT 'test', applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')))`); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var version int
		if _, err = fmt.Sscanf(entry.Name(), "%d_", &version); err != nil {
			t.Fatal(err)
		}
		if version >= 44 {
			continue
		}
		script, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
			t.Fatal(err)
		}
		if err = s.applyMigration(ctx, conn, version, entry.Name(), string(script)); err != nil {
			t.Fatal(err)
		}
		conn.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
		conn.Close()
	}
	if _, err = s.db.ExecContext(ctx, `INSERT INTO playlists(id,name,description) VALUES(1,'existing','old')`); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p, err := s.PlaylistByID(ctx, 1)
	if err != nil || p.Revision != 0 || p.HasCustomArtwork || p.Name != "existing" {
		t.Fatalf("upgrade %+v %v", p, err)
	}
}
