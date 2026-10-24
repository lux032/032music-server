package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

const rgA = "12345678-1234-1234-1234-123456789abc"
const rgB = "87654321-1234-1234-1234-123456789abc"

func importRG(t *testing.T, f albumMergeFixture, path, album string, groups ...string) {
	t.Helper()
	if err := f.store.ImportTrack(f.ctx, ImportInput{LibraryID: f.library, RelativePath: path, Metadata: metadata.AudioMetadata{Title: path, Album: album, DiscNumber: 1, TrackNumber: 1, Artists: []string{"Singer"}, Raw: map[string][]string{"MUSICBRAINZ_RELEASEGROUPID": groups, "MUSICBRAINZ_ALBUMID": {rgA, rgB}}}}); err != nil {
		t.Fatal(err)
	}
}
func setReleaseGroupIdentityReady(t *testing.T, f albumMergeFixture) {
	t.Helper()
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE release_group_rebuild SET status='ready' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseGroupFileAggregation(t *testing.T) {
	f := newAlbumMergeFixture(t)
	state, err := f.store.ReleaseGroupRebuildState(f.ctx)
	if err != nil || state.Status != "not_ready" {
		t.Fatalf("%+v %v", state, err)
	}
	setReleaseGroupIdentityReady(t, f)
	importRG(t, f, "A/1.flac", "A")
	importRG(t, f, "A/2.flac", "A", rgA)
	importRG(t, f, "B/1.flac", "B", rgA)
	a := f.albumID(t, "A")
	if got, err := f.store.AlbumReleaseGroup(f.ctx, a); err != nil || got != rgA {
		t.Fatalf("%q %v", got, err)
	}
	albums, err := f.store.ResolvedAlbumsByReleaseGroup(f.ctx, rgA)
	if err != nil || len(albums) != 2 {
		t.Fatalf("%v %v", albums, err)
	}
	evidence, err := f.store.ReleaseGroupFileEvidence(f.ctx, rgA, "")
	if err != nil || len(evidence) != 2 {
		t.Fatalf("%v %v", evidence, err)
	}
	if evidence[0].CreatedAt == "" || evidence[0].AudioFileID == 0 {
		t.Fatal(evidence)
	}
	evidence, err = f.store.ReleaseGroupFileEvidence(f.ctx, rgA, "9999")
	if err != nil || len(evidence) != 0 {
		t.Fatalf("%v %v", evidence, err)
	}
	importRG(t, f, "A/1.flac", "A", rgA, rgB)
	if _, err = f.store.AlbumReleaseGroup(f.ctx, a); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("conflict resolved: %v", err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_release_groups WHERE album_id=? AND status='conflict'`, a); n != 2 {
		t.Fatal(n)
	}
	importRG(t, f, "A/1.flac", "A")
	if got, err := f.store.AlbumReleaseGroup(f.ctx, a); err != nil || got != rgA {
		t.Fatalf("%q %v", got, err)
	}
	if err = f.store.AssociateAlbumReleaseGroup(f.ctx, a, rgB); err != nil {
		t.Fatal(err)
	}
	importRG(t, f, "A/2.flac", "A")
	if got, err := f.store.AlbumReleaseGroup(f.ctx, a); err != nil || got != rgB {
		t.Fatalf("manual removed: %q %v", got, err)
	}
	if err = f.store.AssociateAlbumReleaseGroup(f.ctx, 9999, rgA); err == nil {
		t.Fatal("external-owned association allowed")
	}
}
func TestReleaseGroupMergeDeletePersistence(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		targetManual, sourceManual bool
		sourceRG                   string
		want                       string
	}{
		{"files conflict", false, false, rgB, ""},
		{"target manual", true, false, rgB, rgA},
		{"source manual", false, true, rgB, rgB},
		{"same manual", true, true, rgA, rgA},
		{"manual conflict", true, true, rgB, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			setReleaseGroupIdentityReady(t, f)
			importRG(t, f, "A/1.flac", "A", rgA)
			importRG(t, f, "B/1.flac", "B", tc.sourceRG)
			a, b := f.albumID(t, "A"), f.albumID(t, "B")
			if tc.targetManual {
				if err := f.store.AssociateAlbumReleaseGroup(f.ctx, a, rgA); err != nil {
					t.Fatal(err)
				}
			}
			if tc.sourceManual {
				if err := f.store.AssociateAlbumReleaseGroup(f.ctx, b, tc.sourceRG); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.store.MergeAlbums(f.ctx, a, []int64{b}); err != nil {
				t.Fatal(err)
			}
			got, err := f.store.AlbumReleaseGroup(f.ctx, a)
			if tc.want == "" {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("conflict %q %v", got, err)
				}
				if n := f.count(t, `SELECT COUNT(DISTINCT rgid) FROM album_release_groups WHERE album_id=? AND status='conflict'`, a); n != 2 {
					t.Fatal(n)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("%q %v", got, err)
			}
			if err = f.store.SetReleaseGroupIgnored(f.ctx, rgA, true); err != nil {
				t.Fatal(err)
			}
			var key string
			if err = f.store.db.QueryRowContext(f.ctx, `SELECT grouping_key FROM albums WHERE id=?`, a).Scan(&key); err != nil {
				t.Fatal(err)
			}
			if _, err = f.store.DeleteAlbums(f.ctx, []int64{a}); err != nil {
				t.Fatal(err)
			}
			if n := f.count(t, `SELECT COUNT(*) FROM album_release_groups`); n != 0 {
				t.Fatal(n)
			}
			ignored, err := f.store.ReleaseGroupIgnored(f.ctx, rgA)
			if err != nil || !ignored {
				t.Fatalf("ignore lost: %v %v", ignored, err)
			}
			action, _, err := f.store.AlbumImportRule(f.ctx, f.library, key)
			if err != nil || action != "delete" {
				t.Fatalf("%s %v", action, err)
			}
			importRG(t, f, "A/1.flac", "A", rgA)
			if n := f.count(t, `SELECT COUNT(*) FROM albums`); n != 0 {
				t.Fatal("delete rule bypassed")
			}
			if err = f.store.SetReleaseGroupIgnored(f.ctx, rgA, false); err != nil {
				t.Fatal(err)
			}
			if ignored, err = f.store.ReleaseGroupIgnored(f.ctx, rgA); err != nil || ignored {
				t.Fatalf("ignore not cleared: %v %v", ignored, err)
			}
		})
	}
}
func TestReleaseGroupSchema056Upgrade(t *testing.T) {
	s, _ := backupFixture(t, 56)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM release_group_rebuild`).Scan(&state); err != nil || state != "not_ready" {
		t.Fatalf("%s %v", state, err)
	}
	if n, err := s.PendingMigrations(ctx); err != nil || n != 0 {
		t.Fatalf("%d %v", n, err)
	}
}
func TestReleaseGroupResolvedUniqueAndRebuildRecovery(t *testing.T) {
	f := newAlbumMergeFixture(t)
	importRG(t, f, "A/1.flac", "A", rgA)
	a := f.albumID(t, "A")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO release_groups(rgid) VALUES(?)`, rgB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_release_groups(album_id,rgid,source,status) VALUES(?,?,'file','resolved')`, a, rgB); err == nil {
		t.Fatal("two resolved rows allowed")
	}
	job, err := f.store.CreateScanJob(f.ctx, f.library, "full")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.BeginReleaseGroupRebuild(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = f.store.RecoverReleaseGroupRebuild(f.ctx); err != nil {
		t.Fatal(err)
	}
	state, err := f.store.ReleaseGroupRebuildState(f.ctx)
	if err != nil || state.Status != "error" {
		t.Fatalf("%+v %v", state, err)
	}
	if err = f.store.BeginReleaseGroupRebuild(f.ctx, job); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishScanJob(f.ctx, job, "completed", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err = f.store.RecoverReleaseGroupRebuild(f.ctx); err != nil {
		t.Fatal(err)
	}
	state, err = f.store.ReleaseGroupRebuildState(f.ctx)
	if err != nil || state.Status != "ready" {
		t.Fatalf("%+v %v", state, err)
	}
}

func TestReleaseGroupMissingPurgeAndOldEAV(t *testing.T) {
	f := newAlbumMergeFixture(t)
	setReleaseGroupIdentityReady(t, f)
	importRG(t, f, "A/1.flac", "A", rgA)
	importRG(t, f, "A/2.flac", "A", rgB)
	a := f.albumID(t, "A")
	// Only one file goes missing; automatic identity must cease using its RG.
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE audio_files SET last_scanned_at='9999' WHERE relative_path='A/1.flac'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.MarkMissing(f.ctx, f.library, "9998"); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.AlbumReleaseGroup(f.ctx, a); err != nil || got != rgA {
		t.Fatalf("missing conflict retained: %q %v", got, err)
	}
	if err := f.store.SetReleaseGroupIgnored(f.ctx, rgB, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PurgeMissing(f.ctx, f.library, nil); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM audio_file_release_groups WHERE rgid=?`, rgB); n != 0 {
		t.Fatal(n)
	}
	// Poisoned EAV is deliberately never an identity source.
	if _, err := f.store.db.ExecContext(f.ctx, `UPDATE audio_file_tags SET value=? WHERE field_name='MUSICBRAINZ_RELEASEGROUPID'`, rgB); err != nil {
		t.Fatal(err)
	}
	if err := f.store.RefreshReleaseGroupIdentities(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := f.store.AlbumReleaseGroup(f.ctx, a); err != nil || got != rgA {
		t.Fatalf("used old EAV: %q %v", got, err)
	}
	if _, err := f.store.MarkMissing(f.ctx, f.library, "zzzz"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.PurgeMissing(f.ctx, f.library, nil); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT COUNT(*) FROM album_release_groups`); n != 0 {
		t.Fatal(n)
	}
	ignored, err := f.store.ReleaseGroupIgnored(f.ctx, rgB)
	if err != nil || !ignored {
		t.Fatalf("purge removed ignore: %v %v", ignored, err)
	}
}

func TestReleaseGroupFileMovesAlbum(t *testing.T) {
	f := newAlbumMergeFixture(t)
	setReleaseGroupIdentityReady(t, f)
	importRG(t, f, "A/1.flac", "A", rgA)
	a := f.albumID(t, "A")
	importRG(t, f, "A/1.flac", "New", rgB)
	if _, err := f.store.AlbumReleaseGroup(f.ctx, a); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old identity retained: %v", err)
	}
	if got, err := f.store.AlbumReleaseGroup(f.ctx, f.albumID(t, "New")); err != nil || got != rgB {
		t.Fatalf("%q %v", got, err)
	}
}
func TestReleaseGroupRebuildFailureAndPartialScan(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed", true: "partial"}[partial], func(t *testing.T) {
			f := newAlbumMergeFixture(t)
			job, err := f.store.CreateScanJob(f.ctx, f.library, "full")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.BeginReleaseGroupRebuild(f.ctx, job); err != nil {
				t.Fatal(err)
			}
			if partial {
				if err = f.store.UpdateScanJob(f.ctx, job, 1, 0, 1, ""); err != nil {
					t.Fatal(err)
				}
				if err = f.store.FinishScanJob(f.ctx, job, "completed", 0, ""); err != nil {
					t.Fatal(err)
				}
				if err = f.store.CompleteReleaseGroupRebuild(f.ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = f.store.FinishScanJob(f.ctx, job, "failed", 0, "broken mount"); err != nil {
					t.Fatal(err)
				}
				if err = f.store.CheckReleaseGroupRebuildFailure(f.ctx, job); err != nil {
					t.Fatal(err)
				}
			}
			state, err := f.store.ReleaseGroupRebuildState(f.ctx)
			if err != nil || state.Status != "error" || state.Error == "" {
				t.Fatalf("%+v %v", state, err)
			}
		})
	}
}

func TestReleaseGroupReadsRequireReadyAndNormalizeIDs(t *testing.T) {
	f := newAlbumMergeFixture(t)
	importRG(t, f, "A/1.flac", "A", rgA)
	albumID := f.albumID(t, "A")
	for _, status := range []string{"not_ready", "rebuilding", "error"} {
		if _, err := f.store.db.ExecContext(f.ctx, `UPDATE release_group_rebuild SET status=? WHERE id=1`, status); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.AlbumReleaseGroup(f.ctx, albumID); !errors.Is(err, ErrIdentityNotReady) {
			t.Fatalf("%s album: %v", status, err)
		}
		if _, err := f.store.ResolvedAlbumsByReleaseGroup(f.ctx, rgA); !errors.Is(err, ErrIdentityNotReady) {
			t.Fatalf("%s albums: %v", status, err)
		}
		if _, err := f.store.ReleaseGroupFileEvidence(f.ctx, rgA, ""); !errors.Is(err, ErrIdentityNotReady) {
			t.Fatalf("%s evidence: %v", status, err)
		}
	}
	setReleaseGroupIdentityReady(t, f)
	if got, err := f.store.AlbumReleaseGroup(f.ctx, albumID); err != nil || got != rgA {
		t.Fatalf("ready album: %q %v", got, err)
	}
	if err := f.store.SetReleaseGroupIgnored(f.ctx, rgA, true); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{rgA, strings.ToUpper(rgA), " \t" + strings.ToUpper(rgA) + "\n"} {
		albums, err := f.store.ResolvedAlbumsByReleaseGroup(f.ctx, input)
		if err != nil || len(albums) != 1 || albums[0] != albumID {
			t.Fatalf("%q albums: %v %v", input, albums, err)
		}
		evidence, err := f.store.ReleaseGroupFileEvidence(f.ctx, input, "")
		if err != nil || len(evidence) != 1 || evidence[0].AlbumID != albumID {
			t.Fatalf("%q evidence: %v %v", input, evidence, err)
		}
		ignored, err := f.store.ReleaseGroupIgnored(f.ctx, input)
		if err != nil || !ignored {
			t.Fatalf("%q ignored: %v %v", input, ignored, err)
		}
	}
	for _, input := range []string{"", "bad-id"} {
		if _, err := f.store.ResolvedAlbumsByReleaseGroup(f.ctx, input); err == nil {
			t.Fatalf("invalid albums ID accepted: %q", input)
		}
		if _, err := f.store.ReleaseGroupFileEvidence(f.ctx, input, ""); err == nil {
			t.Fatalf("invalid evidence ID accepted: %q", input)
		}
		if _, err := f.store.ReleaseGroupIgnored(f.ctx, input); err == nil {
			t.Fatalf("invalid ignore ID accepted: %q", input)
		}
	}
}
