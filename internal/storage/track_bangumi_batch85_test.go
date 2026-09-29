package storage

import (
	"strings"
	"testing"
)

// 批次 8.5：migration 033 的两个性能索引必须存在。
func TestMigration033CreatesTrackTieupPerfIndexes(t *testing.T) {
	f := newAlbumMergeFixture(t)
	for _, index := range []string{"idx_track_artists_track_role", "idx_track_subject_candidates_track_status"} {
		var n int
		if err := f.store.db.QueryRowContext(f.ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name=?`, index).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s count=%d err=%v", index, n, err)
		}
	}
}

// 批次 8.5：TracksForBangumiTieup 等价性——覆盖各排除条件（confirmed /
// rejected / candidate 候选、新鲜 miss）与字段组装（别名、合并艺术家的名字）。
func TestTracksForBangumiTieupEquivalenceBatch85(t *testing.T) {
	f := newAlbumMergeFixture(t)
	f.importFile(t, "Keep/01.flac", "Keep Album", "Song", 1, 1)
	f.importFile(t, "Confirmed/01.flac", "Confirmed Album", "Song", 1, 1)
	f.importFile(t, "Rejected/01.flac", "Rejected Album", "Song", 1, 1)
	f.importFile(t, "Pending/01.flac", "Pending Album", "Song", 1, 1)
	f.importFile(t, "FreshMiss/01.flac", "Fresh Miss Album", "Song", 1, 1)

	// confirmed / rejected / candidate 三种定论的候选都排除。
	confirmed := trackIDOf(t, f, "Confirmed Album")
	rejected := trackIDOf(t, f, "Rejected Album")
	pending := trackIDOf(t, f, "Pending Album")
	saveOneTrackCandidate(t, f, confirmed, "c1", nil, "exact")
	saveOneTrackCandidate(t, f, rejected, "c2", nil, "exact")
	saveOneTrackCandidate(t, f, pending, "c3", nil, "exact")
	for id, status := range map[int64]string{confirmed: "confirmed", rejected: "rejected"} {
		if _, err := f.store.db.ExecContext(f.ctx, `UPDATE track_subject_candidates SET status=? WHERE track_id=?`, status, id); err != nil {
			t.Fatal(err)
		}
	}

	// 新鲜 miss（TTL 内）且 fingerprint 一致才排除。
	keep := trackIDOf(t, f, "Keep Album")
	freshMiss := trackIDOf(t, f, "Fresh Miss Album")
	targets, err := f.store.TracksForBangumiTieup(f.ctx, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]TrackBangumiTarget{}
	for _, v := range targets {
		byID[v.ID] = v
	}
	keepTarget, ok := byID[keep]
	if !ok {
		t.Fatalf("keep track missing: %+v", targets)
	}
	missTarget, ok := byID[freshMiss]
	if !ok {
		t.Fatalf("fresh-miss track missing: %+v", targets)
	}
	if err = f.store.SetTrackBangumiMiss(f.ctx, keepTarget, "test miss"); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SetTrackBangumiMiss(f.ctx, missTarget, "test miss"); err != nil {
		t.Fatal(err)
	}

	targets, err = f.store.TracksForBangumiTieup(f.ctx, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID = map[int64]TrackBangumiTarget{}
	for _, v := range targets {
		byID[v.ID] = v
	}
	for _, id := range []int64{confirmed, rejected, pending, freshMiss} {
		if _, found := byID[id]; found {
			t.Fatalf("track %d must be excluded: %+v", id, byID[id])
		}
	}
	// fingerprint 一致的新鲜 miss：keep 也被排除。
	if _, found := byID[keep]; found {
		t.Fatal("fresh miss with matching fingerprint must exclude the track")
	}
	// force 绕过 pending 候选与 miss，但不绕过 confirmed/rejected。
	targets, err = f.store.TracksForBangumiTieup(f.ctx, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID = map[int64]TrackBangumiTarget{}
	for _, v := range targets {
		byID[v.ID] = v
	}
	if _, found := byID[pending]; !found {
		t.Fatal("force must revisit pending candidates")
	}
	if _, found := byID[keep]; !found {
		t.Fatal("force must revisit fresh misses")
	}
	if _, found := byID[confirmed]; found {
		t.Fatal("force must NOT revisit confirmed candidates")
	}
	if _, found := byID[rejected]; found {
		t.Fatal("force must NOT revisit rejected candidates")
	}

	// 字段组装：别名单元（artist_names）与合并艺术家的名字都进 Artists。
	var artistID int64
	if err = f.store.db.QueryRowContext(f.ctx, `SELECT ta.artist_id FROM track_artists ta WHERE ta.track_id=? AND ta.role='primary'`, keep).Scan(&artistID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `INSERT INTO artist_names(artist_id,value,name_type) VALUES(?,'Alias XY','alias')`, artistID); err != nil {
		t.Fatal(err)
	}
	// 合并：新建目标艺术家，把当前艺术家合并进去。
	var merged int64
	if err = f.store.db.QueryRowContext(f.ctx, `INSERT INTO artists(display_name,sort_name,identity_key) VALUES('Merged Target','merged target','merged target') RETURNING id`).Scan(&merged); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.db.ExecContext(f.ctx, `UPDATE artists SET merged_into_artist_id=? WHERE id=?`, merged, artistID); err != nil {
		t.Fatal(err)
	}
	targets, err = f.store.TracksForBangumiTieup(f.ctx, true, f.albumID(t, "Keep Album"))
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v %v", targets, err)
	}
	joined := strings.Join(targets[0].Artists, "|")
	if !strings.Contains(joined, "Alias XY") {
		t.Fatalf("alias missing from Artists: %v", targets[0].Artists)
	}
	if !strings.Contains(joined, "Merged Target") {
		t.Fatalf("merged artist name missing from Artists: %v", targets[0].Artists)
	}
	if targets[0].Title != "Song" || targets[0].AlbumTitle != "Keep Album" {
		t.Fatalf("target=%+v", targets[0])
	}
}
