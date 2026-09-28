package storage

import (
	"github.com/lux032/032music-server/internal/metadata"
	"strings"
	"testing"
	"time"
)

func TestD3AlbumsForBangumiTieupIncludesAutoButExcludesManualAndBangumi(t *testing.T) {
	for _, source := range []string{"auto", "manual", "bangumi"} {
		t.Run(source, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Album A", "Track", 1, 1)
			albumID := f.albumID(t, "Album A")
			work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Work", Type: "anime"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source) VALUES(?,?,?)`, albumID, work.ID, source); err != nil {
				t.Fatal(err)
			}
			targets, err := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
			want := 0
			if source == "auto" {
				want = 1
			}
			if err != nil || len(targets) != want {
				t.Fatalf("targets=%+v want=%d err=%v", targets, want, err)
			}
		})
	}
}

func TestM4AlbumsForBangumiTieupFiltersMissForceAndTarget(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album A", "Track", 1, 1)
	id := f.albumID(t, "Album A")
	targets, e := f.store.AlbumsForBangumiTieup(f.ctx, false, 0)
	if e != nil || len(targets) != 0 {
		t.Fatalf("ordinary %+v: %v", targets, e)
	}
	targets, e = f.store.AlbumsForBangumiTieup(f.ctx, false, id)
	if e != nil || len(targets) != 1 {
		t.Fatalf("target %+v: %v", targets, e)
	}
	if e = f.store.SetAlbumBangumiMiss(f.ctx, targets[0]); e != nil {
		t.Fatal(e)
	}
	targets, e = f.store.AlbumsForBangumiTieup(f.ctx, false, id)
	if e != nil || len(targets) != 0 {
		t.Fatalf("miss %+v: %v", targets, e)
	}
	targets, e = f.store.AlbumsForBangumiTieup(f.ctx, true, 0)
	if e != nil || len(targets) != 1 {
		t.Fatalf("forced %+v: %v", targets, e)
	}
	if _, e = f.store.db.ExecContext(f.ctx, `UPDATE album_enrichment_misses SET checked_at=? WHERE album_id=?`, time.Now().Add(-40*24*time.Hour).UTC().Format(time.RFC3339Nano), id); e != nil {
		t.Fatal(e)
	}
	targets, e = f.store.AlbumsForBangumiTieup(f.ctx, false, id)
	if e != nil || len(targets) != 1 || !targets[0].Recheck {
		t.Fatalf("expired %+v: %v", targets, e)
	}
	if _, e = f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'bangumi:660542')`, id); e != nil {
		t.Fatal(e)
	}
	targets, e = f.store.AlbumsForBangumiTieup(f.ctx, true, id)
	if e != nil || len(targets) != 0 {
		t.Fatalf("suppressed %+v: %v", targets, e)
	}
}
func TestM5RemoveBangumiLinkSuppressesLocalIdentityAndRefreshKeepsOtherBangumi(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Hollow Knight Original Soundtrack", "Track", 1, 1)
	id := f.albumID(t, "Hollow Knight Original Soundtrack")
	work, e := f.store.CreateWork(f.ctx, WorkInput{Title: "Hollow Knight", Type: "game"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,role,source,inferred_key) VALUES(?,?,'ost','bangumi','bangumi:10:20')`, id, work.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.RefreshAlbumWorks(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	links, e := f.store.AlbumsForWork(f.ctx, work.ID)
	if e != nil || len(links) != 1 || links[0].Source != "bangumi" {
		t.Fatalf("refresh links %+v: %v", links, e)
	}
	if e = f.store.RemoveWorkAlbum(f.ctx, work.ID, id); e != nil {
		t.Fatal(e)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=?`, id); n != 2 {
		t.Fatalf("suppression count %d", n)
	}
	if _, e = f.store.RefreshAlbumWorks(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	links, e = f.store.AlbumsForWork(f.ctx, work.ID)
	if e != nil || len(links) != 0 {
		t.Fatalf("resurrected %+v: %v", links, e)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, id); n != 0 {
		t.Fatalf("album work rows=%d", n)
	}
}

func TestB2ResolveBangumiWorkReusesNFKCAndSeason(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "ぼっち・ざ・ろっく！ Original Soundtrack", "Track", 1, 1)
	albumID := f.albumID(t, "ぼっち・ざ・ろっく！ Original Soundtrack")
	targets, e := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if e != nil || len(targets) != 1 {
		t.Fatalf("targets %+v: %v", targets, e)
	}
	ties := []BangumiTieup{{SubjectID: 541285, Title: "ぼっち・ざ・ろっく!", Type: "anime", Role: "ost"}}
	if e = f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "660542", Title: "Single", Tieups: ties}}); e != nil {
		t.Fatal(e)
	}
	candidates, e := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	if e != nil {
		t.Fatal(e)
	}
	outcome, ids, e := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{541285})
	if e != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("confirm %s %+v: %v", outcome, ids, e)
	}
	if _, e = f.store.RefreshAlbumWorks(f.ctx, true); e != nil {
		t.Fatal(e)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM works`); n != 1 {
		t.Fatalf("duplicate work count=%d", n)
	}
}

func TestD2H5ConfirmAlbumBangumiReplacesAutoLink(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album A", "Track", 1, 1)
	albumID := f.albumID(t, "Album A")
	targets, e := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if e != nil || len(targets) != 1 {
		t.Fatalf("targets %+v %v", targets, e)
	}
	if e = f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "660542", Title: "Amore", Tieups: []BangumiTieup{{SubjectID: 541285, Title: "Anime", Type: "anime", Role: "op"}}}}); e != nil {
		t.Fatal(e)
	}
	candidates, e := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	if e != nil {
		t.Fatal(e)
	}
	existing, e := f.store.CreateWork(f.ctx, WorkInput{Title: "Existing", Type: "other"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source) VALUES(?,?,'auto')`, albumID, existing.ID); e != nil {
		t.Fatal(e)
	}
	outcome, ids, e := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, true, []int64{541285})
	if e != nil || outcome != "succeeded" || len(ids) != 1 {
		t.Fatalf("outcome %s ids %+v err %v", outcome, ids, e)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND source='auto'`, albumID); n != 0 {
		t.Fatalf("auto links remain: %d", n)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=? AND source='bangumi'`, albumID); n != 1 {
		t.Fatalf("bangumi links: %d", n)
	}
}

func TestB3RejectAlbumBangumiCandidatePersistsSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Album A", "Track", 1, 1)
	albumID := f.albumID(t, "Album A")
	if e := f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "660542", Title: "Amore"}}); e != nil {
		t.Fatal(e)
	}
	candidates, e := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.store.RejectAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID); e != nil {
		t.Fatal(e)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key='bangumi:660542'`, albumID); n != 1 {
		t.Fatalf("suppression %d", n)
	}
	targets, e := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if e != nil || len(targets) != 0 {
		t.Fatalf("resurrected %+v: %v", targets, e)
	}
}

// F2/M5: both explicit WORKTITLE/CONTENTGROUP and album-title inference
// honor the title-format suppression left by removing a Bangumi link.
func TestF2M5RemovedBangumiWorkDoesNotReappearFromTagsOrAlbumTitle(t *testing.T) {
	for _, tc := range []struct {
		name, album string
		raw         map[string][]string
	}{
		{"track-worktitle", "Plain Album", map[string][]string{"WORKTITLE": {"TVアニメ『ぼっち・ざ・ろっく！』オープニングテーマ"}}},
		{"track-contentgroup", "Plain Album", map[string][]string{"CONTENTGROUP": {"TVアニメ『ぼっち・ざ・ろっく！』オープニングテーマ"}}},
		{"album-title", "TVアニメ『ぼっち・ざ・ろっく！』 Original Soundtrack", map[string][]string{"WORKTITLE": {"TVアニメ『ぼっち・ざ・ろっく！』オープニングテーマ"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ctx, album := albumWorkFixture(t, tc.album, "Song")
			lib, _ := s.LibraryByRoot(ctx, "/music")
			input := ImportInput{LibraryID: lib.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "Song", Album: tc.album, Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1, Raw: tc.raw}}
			// Link first; the user then removes it before the explicit tags are refreshed.
			w, e := s.CreateWork(ctx, WorkInput{Title: "ぼっち・ざ・ろっく!", Type: "anime"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.db.ExecContext(ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'bangumi','bangumi:328609:406604')`, album, w.ID); e != nil {
				t.Fatal(e)
			}
			if e = s.RemoveWorkAlbum(ctx, w.ID, album); e != nil {
				t.Fatal(e)
			}
			if e = s.ImportTrack(ctx, input); e != nil {
				t.Fatal(e)
			}
			if _, e = s.RefreshAlbumWorks(ctx, true); e != nil {
				t.Fatal(e)
			}
			input.ModifiedAtNS = 3
			if e = s.ImportTrack(ctx, input); e != nil {
				t.Fatal(e)
			}
			if _, e = s.RefreshAlbumWorks(ctx, true); e != nil {
				t.Fatal(e)
			}
			for _, query := range []string{`SELECT COUNT(*) FROM album_works WHERE album_id=? AND work_id=?`, `SELECT COUNT(*) FROM work_tracks WHERE work_id=? AND track_id IN (SELECT id FROM tracks WHERE album_id=?)`} {
				args := []any{album, w.ID}
				if strings.Contains(query, "work_tracks") {
					args = []any{w.ID, album}
				}
				var n int
				if e = s.db.QueryRowContext(ctx, query, args...).Scan(&n); e != nil || n != 0 {
					t.Fatalf("resurrected %s count=%d err=%v", query, n, e)
				}
			}
			targets, e := s.AlbumsForBangumiTieup(ctx, true, album)
			if e != nil || len(targets) != 0 {
				t.Fatalf("tieup rerun %+v err=%v", targets, e)
			}
		})
	}
}
