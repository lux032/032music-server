package storage

import (
	"testing"
	"time"
)

func markTagged(t *testing.T, s *Store, trackID int64, stamp string) {
	t.Helper()
	if err := s.SaveTrackTagResult(t.Context(), TrackTagMeta{TrackID: trackID, SourceStamp: stamp, InputHash: "h", VocabVersion: 1, PromptVersion: 1, Model: "m"}, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// 收藏、更换封面等不影响歌曲内容的操作不应让已打标曲目重新排队；修改标题等内容则应重新排队。
func TestTaggingStampIgnoresNonContentChanges(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 2)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	stamps := taggingIDs(t, s, false, now)
	for _, id := range ids {
		markTagged(t, s, id, stamps[id])
	}
	if got := taggingIDs(t, s, false, now); len(got) != 0 {
		t.Fatalf("all tagged, got %v", got)
	}

	time.Sleep(2 * time.Millisecond) // updated_at 精度为毫秒
	if err := s.SetTrackFavorite(ctx, ids[0], true); err != nil {
		t.Fatal(err)
	}
	var albumID int64
	if err := s.db.QueryRowContext(ctx, `SELECT album_id FROM tracks WHERE id = ?`, ids[0]).Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlbumFavorite(ctx, albumID, true); err != nil {
		t.Fatal(err)
	}
	if got := taggingIDs(t, s, false, now); len(got) != 0 {
		t.Fatalf("favorite must not re-queue tagging, got %v", got)
	}

	if err := s.UpdateTrack(ctx, ids[1], "New Title", 1, 1, "", "", nil); err != nil {
		t.Fatal(err)
	}
	got := taggingIDs(t, s, false, now)
	if _, ok := got[ids[1]]; !ok || len(got) != 1 {
		t.Fatalf("title edit must re-queue only that track, got %v", got)
	}
}

// 新增或删除外置 .lrc（扫描器只更新 audio_files.has_external_lrc）应让曲目重新排队。
func TestTaggingStampFollowsExternalLRC(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 1)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	lib, _ := s.LibraryByRoot(ctx, "/music")
	var rel string
	if err := s.db.QueryRowContext(ctx, `SELECT relative_path FROM audio_files WHERE track_id = ?`, ids[0]).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	for _, lrc := range []bool{true, false} {
		markTagged(t, s, ids[0], taggingIDs(t, s, false, now)[ids[0]])
		if got := taggingIDs(t, s, false, now); len(got) != 0 {
			t.Fatalf("tagged track queued: %v", got)
		}
		if err := s.TouchAudioFile(ctx, lib.ID, rel, lrc); err != nil {
			t.Fatal(err)
		}
		if _, ok := taggingIDs(t, s, false, now)[ids[0]]; !ok {
			t.Fatalf("lrc=%v must re-queue the track", lrc)
		}
	}
}

// 迁移 055：与旧 stamp 一致的记录换算为新 stamp（不重新排队）；旧 stamp 已过期的保持过期。
func TestUpgradeTaggingStampsV2(t *testing.T) {
	s, ctx, ids := audioAnalysisFixture(t, 2)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	var legacy string
	if err := s.db.QueryRowContext(ctx, `SELECT `+legacyTaggingStampV1SQL+` FROM tracks t JOIN albums a ON a.id = t.album_id
		LEFT JOIN track_audio_features f ON f.track_id = t.id WHERE t.id = ?`, ids[0]).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	markTagged(t, s, ids[0], legacy)
	markTagged(t, s, ids[1], "stale")
	if _, _, err := s.RecordTaggingFailure(ctx, ids[0], legacy, "x", now, 4); err != nil {
		t.Fatal(err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeTaggingStampsV2(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got := taggingIDs(t, s, false, now)
	if _, ok := got[ids[0]]; ok {
		t.Fatalf("up-to-date track must stay tagged after upgrade, got %v", got)
	}
	if _, ok := got[ids[1]]; !ok {
		t.Fatalf("stale track must stay queued, got %v", got)
	}
	current, _, err := s.TrackTaggingStamp(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	var failStamp string
	if err := s.db.QueryRowContext(ctx, `SELECT source_stamp FROM ai_tagging_failures WHERE track_id = ?`, ids[0]).Scan(&failStamp); err != nil {
		t.Fatal(err)
	}
	if failStamp != current {
		t.Fatalf("failure stamp not upgraded: %q vs %q", failStamp, current)
	}
}
