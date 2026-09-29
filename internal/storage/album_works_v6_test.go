package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

const v6CyberpunkAlbum = "Cyberpunk: Edgerunners (Original Series Soundtrack)"

// seedV5AlbumWork recreates the pre-upgrade state of an album: an auto work
// named by the frozen v5 inference plus an album link carrying the v5
// inferred_key. It fails the test when v5/v6 do not actually differ.
func seedV5AlbumWork(t *testing.T, s *Store, ctx context.Context, albumID int64, albumTitle, source string) (workID int64, oldKey, newKey string) {
	t.Helper()
	oldAssoc, ok := metadata.InferAlbumWorkV5(albumTitle, "", false)
	if !ok {
		t.Fatalf("v5 no longer infers %q", albumTitle)
	}
	newAssoc, ok := metadata.InferAlbumWork(albumTitle, "", false)
	if !ok {
		t.Fatalf("v6 no longer infers %q", albumTitle)
	}
	oldKey, newKey = inferredWorkKey(oldAssoc), inferredWorkKey(newAssoc)
	if oldKey == newKey {
		t.Fatalf("v5/v6 keys for %q must differ", albumTitle)
	}
	if err := s.db.QueryRowContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES(?,?,?,'auto') RETURNING id`, oldAssoc.Title, metadata.Normalize(oldAssoc.Title), oldAssoc.Type).Scan(&workID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO work_aliases(normalized_key,work_id) VALUES(?,?)`, metadata.Normalize(oldAssoc.Title), workID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,?,?,?)`, albumID, workID, oldAssoc.Role, source, oldKey); err != nil {
		t.Fatal(err)
	}
	return workID, oldKey, newKey
}

func v6AlbumLink(t *testing.T, s *Store, ctx context.Context, albumID int64) (workID int64, inferredKey, source string) {
	t.Helper()
	rows, err := s.db.QueryContext(ctx, `SELECT work_id,COALESCE(inferred_key,''),source FROM album_works WHERE album_id=?`, albumID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
		if err = rows.Scan(&workID, &inferredKey, &source); err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 {
		t.Fatalf("album %d links=%d, want 1", albumID, n)
	}
	return workID, inferredKey, source
}

func v6WorkCount(t *testing.T, s *Store, ctx context.Context) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRefreshAlbumWorksV6ReplacesTruncatedWork: an unprotected v5-truncated
// work is replaced by the newly inferred identity; its pending (never
// reviewed) candidates cascade away with it (F2).
func TestRefreshAlbumWorksV6ReplacesTruncatedWork(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
	oldID, _, _ := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "auto")
	if _, err := s.db.ExecContext(ctx, `INSERT INTO work_match_candidates(work_id,source,external_id,title,score,status) VALUES(?,'bangumi','1','Candidate',50,'candidate')`, oldID); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	workID, key, _ := v6AlbumLink(t, s, ctx, album)
	if workID == oldID {
		t.Fatal("unprotected work was carried over")
	}
	var title string
	if err = s.db.QueryRowContext(ctx, `SELECT title FROM works WHERE id=?`, workID).Scan(&title); err != nil || title != "Cyberpunk: Edgerunners" {
		t.Fatalf("new work title %q %v", title, err)
	}
	var old int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works WHERE id=?`, oldID).Scan(&old); err != nil || old != 0 {
		t.Fatalf("old work still present: %d %v", old, err)
	}
	var candidates int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_match_candidates WHERE work_id=?`, oldID).Scan(&candidates); err != nil || candidates != 0 {
		t.Fatalf("pending candidates survived: %d %v", candidates, err)
	}
	want := inferredWorkKey(metadata.WorkAssociation{Title: "Cyberpunk: Edgerunners", Type: "other", Role: "ost"})
	if key != want {
		t.Fatalf("inferred_key %q, want %q", key, want)
	}
}

// TestRefreshAlbumWorksV6MergesIntoBoundWork: the newly inferred identity
// resolves to the already Bangumi-bound work of the same name (R5).
func TestRefreshAlbumWorksV6MergesIntoBoundWork(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
	var boundID int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('Cyberpunk: Edgerunners','cyberpunk: edgerunners','anime','auto') RETURNING id`).Scan(&boundID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,raw_json,fetched_at) VALUES(?,'bangumi','309311','Cyberpunk: Edgerunners','{}','2024-01-01')`, boundID); err != nil {
		t.Fatal(err)
	}
	oldID, _, _ := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "auto")
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	workID, _, _ := v6AlbumLink(t, s, ctx, album)
	if workID != boundID {
		t.Fatalf("album linked to %d, want bound work %d", workID, boundID)
	}
	var old int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM works WHERE id=?`, oldID).Scan(&old); err != nil || old != 0 {
		t.Fatalf("truncated work survived: %d %v", old, err)
	}
}

// TestRefreshAlbumWorksV6CarryOverProtectedWork: a protected work keeps its id
// across the rule change (D71/F1): new-name alias, re-keyed link, and — for
// untouched auto names — a rename. User-named (origin=manual) works keep
// their display name.
func TestRefreshAlbumWorksV6CarryOverProtectedWork(t *testing.T) {
	protections := map[string]func(t *testing.T, s *Store, ctx context.Context, workID, albumID int64){
		"bangumi profile": func(t *testing.T, s *Store, ctx context.Context, workID, albumID int64) {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,raw_json,fetched_at) VALUES(?,'bangumi','446296','Lazarus','{}','2024-01-01')`, workID); err != nil {
				t.Fatal(err)
			}
		},
		"confirmed candidate": func(t *testing.T, s *Store, ctx context.Context, workID, albumID int64) {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO work_match_candidates(work_id,source,external_id,title,score,status) VALUES(?,'bangumi','446296','Lazarus',95,'confirmed')`, workID); err != nil {
				t.Fatal(err)
			}
		},
		"rejected candidate": func(t *testing.T, s *Store, ctx context.Context, workID, albumID int64) {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO work_match_candidates(work_id,source,external_id,title,score,status) VALUES(?,'bangumi','675584','Other',40,'rejected')`, workID); err != nil {
				t.Fatal(err)
			}
		},
		"series lock": func(t *testing.T, s *Store, ctx context.Context, workID, albumID int64) {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO work_series_locks(work_id) VALUES(?)`, workID); err != nil {
				t.Fatal(err)
			}
		},
		"manual series member": func(t *testing.T, s *Store, ctx context.Context, workID, albumID int64) {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO work_series(title,title_source) VALUES('S','manual')`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `INSERT INTO work_series_members(work_id,series_id,source) VALUES(?,1,'manual')`, workID); err != nil {
				t.Fatal(err)
			}
		},
		"external id": func(t *testing.T, s *Store, ctx context.Context, workID, albumID int64) {
			if _, err := s.db.ExecContext(ctx, `UPDATE works SET external_id='446296' WHERE id=?`, workID); err != nil {
				t.Fatal(err)
			}
		},
		"manual origin": func(t *testing.T, s *Store, ctx context.Context, workID, albumID int64) {
			if _, err := s.db.ExecContext(ctx, `UPDATE works SET origin='manual' WHERE id=?`, workID); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, protect := range protections {
		t.Run(name, func(t *testing.T) {
			s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
			oldID, _, newKey := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "auto")
			protect(t, s, ctx, oldID, album)
			stats, err := s.RefreshAlbumWorks(ctx, true)
			if err != nil || stats.AlbumsFailed != 0 || stats.ProtectedUnreferenced != 0 {
				t.Fatalf("refresh %+v %v", stats, err)
			}
			workID, key, _ := v6AlbumLink(t, s, ctx, album)
			if workID != oldID {
				t.Fatalf("protected work %d was replaced by %d", oldID, workID)
			}
			if key != newKey {
				t.Fatalf("inferred_key %q, want carried %q", key, newKey)
			}
			var aliases int
			if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_aliases WHERE work_id=? AND normalized_key=?`, oldID, metadata.Normalize("Cyberpunk: Edgerunners")).Scan(&aliases); err != nil || aliases != 1 {
				t.Fatalf("new-name alias missing: %d %v", aliases, err)
			}
			var title, origin string
			if err = s.db.QueryRowContext(ctx, `SELECT title,origin FROM works WHERE id=?`, oldID).Scan(&title, &origin); err != nil {
				t.Fatal(err)
			}
			if name == "manual origin" {
				if title != "Cyberpunk: Edgerunners (Original Series" {
					t.Fatalf("user-named work was renamed: %q", title)
				}
			} else if title != "Cyberpunk: Edgerunners" {
				t.Fatalf("auto work not renamed: %q", title)
			}
			_ = origin
		})
	}
}

// TestRefreshAlbumWorksV6CarryOverManualLink: a manual album link whose
// inferred_key matches the v5 identity also carries over and stays manual.
func TestRefreshAlbumWorksV6CarryOverManualLink(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
	oldID, _, newKey := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "manual")
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	workID, key, source := v6AlbumLink(t, s, ctx, album)
	if workID != oldID || key != newKey || source != "manual" {
		t.Fatalf("manual link = (%d,%q,%s), want (%d,%q,manual)", workID, key, source, oldID, newKey)
	}
}

// TestRefreshAlbumWorksV6NoCarryOverWhenUserRenamedAlbum: the carryover keys
// off the v5 inference of the *current* input. The album's original title
// infers under both v5 and v6 with different keys and its work is protected;
// after the user renames the album (to another title that also infers under
// both versions with differing keys), the stored v5 key no longer matches the
// current input's v5 key, so no carryover may happen. The mutation "match any
// stored inferred_key" fails this test.
func TestRefreshAlbumWorksV6NoCarryOverWhenUserRenamedAlbum(t *testing.T) {
	const renamedTo = "イースX -NORDICS- オリジナルサウンドトラック"
	s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
	oldID, _, _ := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "auto")
	if _, err := s.db.ExecContext(ctx, `UPDATE works SET external_id='446296' WHERE id=?`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE albums SET user_title=? WHERE id=?`, renamedTo, album); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	// The protected work is untouched: no carryover alias, no rename.
	var title string
	if err = s.db.QueryRowContext(ctx, `SELECT title FROM works WHERE id=?`, oldID).Scan(&title); err != nil || title != "Cyberpunk: Edgerunners (Original Series" {
		t.Fatalf("carried despite user rename: title=%q %v", title, err)
	}
	var aliases int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_aliases WHERE work_id=? AND normalized_key IN (?,?)`, oldID, metadata.Normalize("Cyberpunk: Edgerunners"), metadata.Normalize("イースX -NORDICS-")).Scan(&aliases); err != nil || aliases != 0 {
		t.Fatalf("alias carried despite user rename: %d %v", aliases, err)
	}
	// Its stale link was removed; it is protected but unreferenced now.
	if stats.ProtectedUnreferenced != 1 {
		t.Fatalf("ProtectedUnreferenced=%d, want 1 (%+v)", stats.ProtectedUnreferenced, stats)
	}
	// The album links to the work inferred from the new title.
	workID, _, _ := v6AlbumLink(t, s, ctx, album)
	if workID == oldID {
		t.Fatal("album still linked to the old work")
	}
	var newTitle string
	if err = s.db.QueryRowContext(ctx, `SELECT title FROM works WHERE id=?`, workID).Scan(&newTitle); err != nil || newTitle != "イースX -NORDICS-" {
		t.Fatalf("new link title %q %v", newTitle, err)
	}
}

// TestRefreshAlbumWorksV6CarryOverSuppression: a suppression written under the
// v5 identity also covers the v6 identity (R3), at album and track level.
func TestRefreshAlbumWorksV6CarryOverSuppression(t *testing.T) {
	t.Run("album level", func(t *testing.T) {
		s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
		oldAssoc, _ := metadata.InferAlbumWorkV5(v6CyberpunkAlbum, "", false)
		newAssoc, _ := metadata.InferAlbumWork(v6CyberpunkAlbum, "", false)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,?)`, album, inferredWorkKey(oldAssoc)); err != nil {
			t.Fatal(err)
		}
		stats, err := s.RefreshAlbumWorks(ctx, true)
		if err != nil || stats.AlbumsFailed != 0 {
			t.Fatalf("refresh %+v %v", stats, err)
		}
		var links, works int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, album).Scan(&links); err != nil || links != 0 {
			t.Fatalf("suppressed link created: %d %v", links, err)
		}
		if works = v6WorkCount(t, s, ctx); works != 0 {
			t.Fatalf("works=%d, want 0", works)
		}
		var carried int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key=?`, album, inferredWorkKey(newAssoc)).Scan(&carried); err != nil || carried != 1 {
			t.Fatalf("v6 suppression missing: %d %v", carried, err)
		}
	})
	t.Run("track level", func(t *testing.T) {
		const tagValue = "Lazarus (Adult Swim original series soundtrack)"
		s, ctx, _ := albumWorkFixture(t, "Plain Album", "Track")
		var trackID int64
		if err := s.db.QueryRowContext(ctx, `SELECT id FROM tracks LIMIT 1`).Scan(&trackID); err != nil {
			t.Fatal(err)
		}
		oldAssoc, ok := metadata.InferAlbumWorkV5(tagValue, "", false)
		if !ok {
			t.Fatal("v5 no longer infers the tag value")
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO track_work_suppressions(track_id,inferred_key) VALUES(?,?)`, trackID, inferredWorkKey(oldAssoc)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO audio_file_tags(audio_file_id,field_name,value,position) SELECT af.id,'CONTENTGROUP',?,0 FROM audio_files af WHERE af.track_id=?`, tagValue, trackID); err != nil {
			t.Fatal(err)
		}
		stats, err := s.RefreshAlbumWorks(ctx, true)
		if err != nil || stats.AlbumsFailed != 0 {
			t.Fatalf("refresh %+v %v", stats, err)
		}
		var links int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_tracks WHERE track_id=?`, trackID).Scan(&links); err != nil || links != 0 {
			t.Fatalf("suppressed track link created: %d %v", links, err)
		}
		if works := v6WorkCount(t, s, ctx); works != 0 {
			t.Fatalf("works=%d, want 0", works)
		}
		newAssoc, ok := metadata.InferAlbumWork(tagValue, "", false)
		if !ok {
			t.Fatal("v6 no longer infers the tag value")
		}
		var carried int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key=?`, trackID, inferredWorkKey(newAssoc)).Scan(&carried); err != nil || carried != 1 {
			t.Fatalf("v6 track suppression missing: %d %v", carried, err)
		}
	})
}

// TestRefreshAlbumWorksV6RenameCollision: when another work already owns the
// new identity, the carryover only adds the alias and never renames (and never
// fails the album).
func TestRefreshAlbumWorksV6RenameCollision(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
	if _, err := s.db.ExecContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('Cyberpunk: Edgerunners','cyberpunk: edgerunners','other','auto')`); err != nil {
		t.Fatal(err)
	}
	oldID, _, _ := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "auto")
	if _, err := s.db.ExecContext(ctx, `UPDATE works SET external_id='446296' WHERE id=?`, oldID); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	workID, _, _ := v6AlbumLink(t, s, ctx, album)
	if workID != oldID {
		t.Fatalf("album linked to %d, want carried work %d", workID, oldID)
	}
	var title string
	if err = s.db.QueryRowContext(ctx, `SELECT title FROM works WHERE id=?`, oldID).Scan(&title); err != nil || title != "Cyberpunk: Edgerunners (Original Series" {
		t.Fatalf("collision case was renamed: %q %v", title, err)
	}
	var aliases int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_aliases WHERE work_id=? AND normalized_key='cyberpunk: edgerunners'`, oldID).Scan(&aliases); err != nil || aliases != 1 {
		t.Fatalf("alias missing: %d %v", aliases, err)
	}
}

// TestRefreshAlbumWorksV6SameKeyDedup (F5/D72): a stale auto row with the same
// inferred_key but a different work than this run's resolution is removed.
func TestRefreshAlbumWorksV6SameKeyDedup(t *testing.T) {
	const albumTitle = "映画「けいおん!」オフィシャル バンドやろーよ!! K-ON! MOVIE編"
	s, ctx, album := albumWorkFixture(t, albumTitle, "Track")
	assoc, ok := metadata.InferAlbumWork(albumTitle, "", false)
	if !ok || assoc.Title != "けいおん!" {
		t.Fatalf("inference changed: %+v %v", assoc, ok)
	}
	var first, second int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('けいおん!','けいおん!','anime','auto') RETURNING id`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `INSERT INTO works(title,normalized_title,type,origin) VALUES('けいおん!','けいおん!','movie','auto') RETURNING id`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{first, second} {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO work_external_profiles(work_id,source,external_id,title,raw_json,fetched_at) VALUES(?,'bangumi',?,'けいおん!','{}','2024-01-01')`, id, fmt.Sprint(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,'other','auto',?)`, album, second, inferredWorkKey(assoc)); err != nil {
		t.Fatal(err)
	}
	stats, err := s.RefreshAlbumWorks(ctx, true)
	if err != nil || stats.AlbumsFailed != 0 {
		t.Fatalf("refresh %+v %v", stats, err)
	}
	workID, _, _ := v6AlbumLink(t, s, ctx, album)
	if workID != first {
		t.Fatalf("album linked to %d, want first bound work %d", workID, first)
	}
}

// TestRefreshAlbumWorksV6Idempotent: the upgrade converges — a second regular
// refresh is a no-op and a forced refresh changes nothing.
func TestRefreshAlbumWorksV6Idempotent(t *testing.T) {
	s, ctx, album := albumWorkFixture(t, v6CyberpunkAlbum, "Track")
	oldID, _, _ := seedV5AlbumWork(t, s, ctx, album, v6CyberpunkAlbum, "auto")
	if _, err := s.db.ExecContext(ctx, `UPDATE works SET external_id='446296' WHERE id=?`, oldID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshAlbumWorks(ctx, true); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		rows, err := s.db.QueryContext(ctx, `SELECT 'w',id,title,type,origin FROM works UNION ALL SELECT 'a',album_id,work_id,source,COALESCE(inferred_key,'') FROM album_works UNION ALL SELECT 'x',work_id,normalized_key,'','' FROM work_aliases ORDER BY 1,2,3`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var a, c, d, e string
			var id int64
			if err = rows.Scan(&a, &id, &c, &d, &e); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%s|%d|%s|%s|%s\n", a, id, c, d, e)
		}
		return b.String()
	}
	before := snapshot()
	stats, err := s.RefreshAlbumWorks(ctx, false)
	if err != nil || stats.AlbumsRefreshed != 0 {
		t.Fatalf("second refresh reprocessed: %+v %v", stats, err)
	}
	if _, err = s.RefreshAlbumWorks(ctx, true); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("forced refresh changed state:\n%s\n---\n%s", before, after)
	}
}
