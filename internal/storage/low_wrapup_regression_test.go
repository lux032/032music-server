package storage

import (
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestLW1ManualGameAcceptanceKeepsAnimeSuppression(t *testing.T) {
	f := newAlbumMergeFixture(t)
	input := ImportInput{LibraryID: f.library, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "Song", Album: "X Original Soundtrack", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1}}
	if err := f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	albumID := f.albumID(t, input.Metadata.Album)
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'x|anime|0')`, albumID); err != nil {
		t.Fatal(err)
	}
	targets, _ := f.store.AlbumsForBangumiTieup(f.ctx, true, albumID)
	if err := f.store.SaveAlbumSubjectCandidates(f.ctx, albumID, []AlbumSubjectCandidate{{ExternalID: "1", Title: "X", Tieups: []BangumiTieup{{SubjectID: 401, Title: "X", Type: "game", Role: "op"}}}}); err != nil {
		t.Fatal(err)
	}
	candidates, _ := f.store.AlbumSubjectCandidates(f.ctx, albumID)
	outcome, _, err := f.store.ConfirmAlbumSubjectCandidate(f.ctx, albumID, candidates[0].ID, 0, targets[0].Fingerprint, false, []int64{401})
	if err != nil || outcome != "succeeded" {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key='x|anime|0'`, albumID); n != 1 {
		t.Fatalf("anime suppression removed=%d", n)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE albums SET user_title='Not X',work_fingerprint=NULL WHERE id=?`, albumID); err != nil {
		t.Fatal(err)
	}
	input.Metadata.Raw = map[string][]string{"WORKTITLE": {"TVアニメ『X』オープニングテーマ"}}
	input.ModifiedAtNS = 2
	if err = f.store.ImportTrack(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM work_tracks wt JOIN tracks t ON t.id=wt.track_id WHERE t.album_id=? AND wt.source='auto'`, albumID); n != 0 {
		t.Fatalf("anime track revived=%d", n)
	}
}

func TestLW2RemoveAutoWritesAliasIdentity(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Original Album", "Song", 1, 1)
	albumID := f.albumID(t, "Original Album")
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Current", Type: "other"})
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_aliases(normalized_key,work_id) VALUES('old alias',?)`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'auto','current|other|0')`, albumID, work.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RemoveWorkAlbum(f.ctx, work.ID, albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET user_title='Old Alias Original Soundtrack',work_fingerprint=NULL WHERE id=?`, albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 0 {
		t.Fatalf("alias association revived=%d", n)
	}
}

func TestLW3IdentityOnlySuppressionPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, f albumMergeFixture, workID, albumID, trackID int64)
	}{
		{"remove-album", func(t *testing.T, f albumMergeFixture, workID, albumID, trackID int64) {
			if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'auto','new name|other|0')`, albumID, workID); err != nil {
				t.Fatal(err)
			}
			if err := f.store.RemoveWorkAlbum(f.ctx, workID, albumID); err != nil {
				t.Fatal(err)
			}
		}},
		{"delete-work", func(t *testing.T, f albumMergeFixture, workID, albumID, trackID int64) {
			if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id,work_id,source,inferred_key) VALUES(?,?,'bangumi','title-only')`, albumID, workID); err != nil {
				t.Fatal(err)
			}
			if err := f.store.DeleteWork(f.ctx, workID); err != nil {
				t.Fatal(err)
			}
		}},
		{"remove-track", func(t *testing.T, f albumMergeFixture, workID, albumID, trackID int64) {
			if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_tracks(work_id,track_id,role,source,inferred_key) VALUES(?,?,'op','bangumi','title-only')`, workID, trackID); err != nil {
				t.Fatal(err)
			}
			if err := f.store.RemoveWorkTrack(f.ctx, workID, trackID, "op", 0, 0); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			f.importFile(t, "A/01.flac", "Plain", "Song", 1, 1)
			albumID := f.albumID(t, "Plain")
			var trackID int64
			_ = f.store.db.QueryRowContext(f.ctx, `SELECT id FROM tracks WHERE album_id=?`, albumID).Scan(&trackID)
			work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "New Name", Type: "anime"})
			if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_aliases(normalized_key,work_id) VALUES('old name',?)`, work.ID); err != nil {
				t.Fatal(err)
			}
			tc.run(t, f, work.ID, albumID, trackID)
			if n := f.count(t, `SELECT COUNT(*) FROM album_work_suppressions WHERE album_id=? AND inferred_key LIKE 'bangumi:%'`, albumID); n != 0 {
				t.Fatalf("unexpected album bangumi keys=%d", n)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM track_work_suppressions WHERE track_id=? AND inferred_key LIKE 'bangumi:%'`, trackID); n != 0 {
				t.Fatalf("unexpected track bangumi keys=%d", n)
			}
			if tc.name == "remove-track" {
				input := ImportInput{LibraryID: f.library, RelativePath: "A/01.flac", FileSize: 1, ModifiedAtNS: 2, Metadata: metadata.AudioMetadata{Title: "Song", Album: "Plain", Artists: []string{"Singer"}, AlbumArtists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, Raw: map[string][]string{"WORKTITLE": {"TVアニメ『Old Name』オープニングテーマ"}}}}
				if err := f.store.ImportTrack(f.ctx, input); err != nil {
					t.Fatal(err)
				}
				if n := f.count(t, `SELECT COUNT(*) FROM work_tracks WHERE track_id=? AND source='auto'`, trackID); n != 0 {
					t.Fatalf("identity-only track revived=%d", n)
				}
			} else {
				if _, err := f.store.db.ExecContext(f.ctx, `UPDATE albums SET user_title='Old Name Original Soundtrack',work_fingerprint=NULL WHERE id=?`, albumID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.store.RefreshAlbumWorks(f.ctx, true); err != nil {
					t.Fatal(err)
				}
				if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 0 {
					t.Fatalf("identity-only album revived=%d", n)
				}
			}
		})
	}
}

func TestLW4ExternalSuppressionDoesNotCountCreatedWork(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "A/01.flac", "Alias Original Soundtrack", "Song", 1, 1)
	albumID := f.albumID(t, "Alias Original Soundtrack")
	work, _ := f.store.CreateWork(f.ctx, WorkInput{Title: "Canonical", Type: "game"})
	addFRBangumiProfile(t, f, work.ID, "555", "Canonical", "game")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO work_aliases(normalized_key,work_id) VALUES('alias',?)`, work.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_work_suppressions(album_id,inferred_key) VALUES(?,'bangumi:1:555')`, albumID); err != nil {
		t.Fatal(err)
	}
	stats, err := f.store.RefreshAlbumWorks(f.ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if stats.WorksCreated != 0 {
		t.Fatalf("works created=%d", stats.WorksCreated)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_works WHERE album_id=?`, albumID); n != 0 {
		t.Fatalf("suppressed association=%d", n)
	}
}
