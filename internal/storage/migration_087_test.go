package storage

import (
	"bytes"
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestMigration087SplitAudioFeatureVectorsAndBackupValidation(t *testing.T) {
	ctx := context.Background()

	checkSchema := func(t *testing.T, s *Store) {
		t.Helper()
		var integrity string
		if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
			t.Fatalf("integrity_check = %q err=%v", integrity, err)
		}
		fkRows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		if fkRows.Next() {
			fkRows.Close()
			t.Fatal("foreign_key_check returned violations")
		}
		fkRows.Close()

		// 主表不再包含 4 个大向量 BLOB 列；伴随表 track_audio_vectors 包含这 4 列
		colSet := func(table string) map[string]bool {
			rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			cols := map[string]bool{}
			for rows.Next() {
				var cid int
				var name, typ string
				var notnull, pk int
				var dflt any
				if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
					t.Fatal(err)
				}
				cols[name] = true
			}
			return cols
		}
		mainCols := colSet("track_audio_features")
		for _, blobCol := range []string{"genre", "moodtheme", "instrument", "embedding"} {
			if mainCols[blobCol] {
				t.Fatalf("track_audio_features still contains BLOB column %s", blobCol)
			}
		}
		vecCols := colSet("track_audio_vectors")
		for _, blobCol := range []string{"track_id", "genre", "moodtheme", "instrument", "embedding"} {
			if !vecCols[blobCol] {
				t.Fatalf("track_audio_vectors missing column %s", blobCol)
			}
		}

		for _, idx := range []string{
			"idx_track_audio_features_version",
			"idx_track_audio_features_analyzed",
			"idx_track_audio_features_version_analyzed",
			"idx_discovery_mappings_status_subject",
		} {
			var count int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND name=?`, idx).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("missing index %s", idx)
			}
		}
	}

	t.Run("fresh migrate", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "fresh087.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("fresh migrate failed: %v", err)
		}
		checkSchema(t, s)
	})

	t.Run("upgrade 086 to 087 preserves vectors and cascades", func(t *testing.T) {
		s, err := Open(filepath.Join(t.TempDir(), "upgrade087.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err := s.MigrateUpTo(ctx, 86); err != nil {
			t.Fatalf("migrate up to 86: %v", err)
		}
		if err := s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
			t.Fatal(err)
		}
		lib, _ := s.LibraryByRoot(ctx, "/music")
		if err := s.ImportTrack(ctx, ImportInput{
			LibraryID: lib.ID, RelativePath: "A/01.flac", FileSize: 1000, ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", Artists: []string{"Artist"}, AlbumArtists: []string{"Artist"}, DiscNumber: 1, TrackNumber: 1},
		}); err != nil {
			t.Fatal(err)
		}
		var trackID, albumID int64
		if err := s.db.QueryRowContext(ctx, `SELECT id, album_id FROM tracks LIMIT 1`).Scan(&trackID, &albumID); err != nil {
			t.Fatal(err)
		}

		// 在旧 086 表结构下写入真实声学向量
		genreBlob := bytes.Repeat([]byte{0x11, 0x22}, 400)
		moodBlob := bytes.Repeat([]byte{0x33, 0x44}, 56)
		instBlob := bytes.Repeat([]byte{0x55, 0x66}, 40)
		embBlob := embedding(0, 1, 2, 3)
		if _, err := s.db.ExecContext(ctx, `INSERT INTO track_audio_features(
			track_id, file_fingerprint, analysis_version, mode, duration_ms, windows, patches,
			genre, moodtheme, instrument, embedding, heads_json, musicnn_valence, musicnn_arousal, valence, arousal, av_source, analyze_ms)
			VALUES (?, 'fp-86', 'v86', 'full', 180000, 3, 27, ?, ?, ?, ?, '{"voice":0.8}', 4.5, 6.2, 4.5, 6.2, 'musicnn', 950)`,
			trackID, genreBlob, moodBlob, instBlob, embBlob); err != nil {
			t.Fatalf("insert v86 audio features: %v", err)
		}

		// 升级至 087
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("upgrade to 087 failed: %v", err)
		}
		checkSchema(t, s)

		// 校验全部标量与 4 个 BLOB 逐字节无损迁移
		got, found, err := s.TrackAudioFeaturesByID(ctx, trackID)
		if err != nil || !found {
			t.Fatalf("TrackAudioFeaturesByID = (%v,%v)", found, err)
		}
		if got.Fingerprint != "fp-86" || got.AnalysisVersion != "v86" || got.Mode != "full" ||
			got.HeadsJSON != `{"voice":0.8}` || got.Valence == nil || *got.Valence != 4.5 ||
			got.Arousal == nil || *got.Arousal != 6.2 || got.AVSource != "musicnn" || got.AnalyzeMs != 950 {
			t.Fatalf("scalar metadata mismatch: %+v", got)
		}
		if !bytes.Equal(got.Genre, genreBlob) || !bytes.Equal(got.MoodTheme, moodBlob) ||
			!bytes.Equal(got.Instrument, instBlob) || !bytes.Equal(got.Embedding, embBlob) {
			t.Fatal("vector BLOB bytes changed during migration 087")
		}
		albumFeats, err := s.AlbumAudioFeatures(ctx, albumID, 10)
		if err != nil || len(albumFeats) != 1 || !bytes.Equal(albumFeats[0].Genre, genreBlob) || !bytes.Equal(albumFeats[0].Instrument, instBlob) {
			t.Fatalf("AlbumAudioFeatures = %+v err=%v", albumFeats, err)
		}
		if !s.HasAcousticFeatures(ctx) {
			t.Fatal("HasAcousticFeatures must be true after migration")
		}
		snap, err := s.AcousticIndex(ctx)
		if err != nil || snap == nil || snap.Len() != 1 {
			t.Fatalf("AcousticIndex = %v err=%v", snap, err)
		}

		// 删除曲目应通过两跳外键级联删除 track_audio_features 和 track_audio_vectors
		if _, err := s.db.ExecContext(ctx, `DELETE FROM tracks WHERE id = ?`, trackID); err != nil {
			t.Fatal(err)
		}
		var vecLeft int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM track_audio_vectors WHERE track_id = ?`, trackID).Scan(&vecLeft); err != nil || vecLeft != 0 {
			t.Fatalf("track_audio_vectors not cascade deleted: left=%d err=%v", vecLeft, err)
		}
	})

	t.Run("backup schema validation", func(t *testing.T) {
		_, b := backupFixture(t, 0)
		name, err := b.Create(ctx, "manual", 0)
		if err != nil {
			t.Fatalf("backup create failed: %v", err)
		}
		res, err := b.Validate(ctx, filepath.Join(b.Directory, name))
		if err != nil {
			t.Fatalf("backup validate failed: %v", err)
		}
		if res.Version < 87 {
			t.Fatalf("backup schema version = %d, want >= 87", res.Version)
		}
	})
}

func TestAcousticF16TableAndDotI8ExactParity(t *testing.T) {
	// 全量 65,536 个 uint16 值逐位一致性校验
	for i := 0; i < 65536; i++ {
		got := f16(uint16(i))
		want := f16Decode(uint16(i))
		if math.Float32bits(got) != math.Float32bits(want) {
			t.Fatalf("f16(0x%04x) bits = 0x%08x, want 0x%08x", i, math.Float32bits(got), math.Float32bits(want))
		}
	}

	// dotI8 各种长度（含非 4 倍数尾巴）与参考实现完全一致
	for _, n := range []int{0, 1, 2, 3, 4, 5, 7, 15, 16, 1280} {
		x := make([]int8, n)
		y := make([]int8, n)
		var want int32
		for i := 0; i < n; i++ {
			x[i] = int8((i*37)%255 - 127)
			y[i] = int8((i*53+11)%255 - 127)
			want += int32(x[i]) * int32(y[i])
		}
		if got := dotI8(x, y); got != want {
			t.Fatalf("dotI8(len=%d) = %d, want %d", n, got, want)
		}
	}

	// IndexOf / CosineToIndex 与 CosineTo 完全等价
	snap := NewAcousticSnapshot([]int64{10, 20, 30}, [][]float32{
		{1, 0, 0, 0},
		{0.9, 0.1, 0, 0},
		{0, 0, 1, 0},
	})
	c := snap.Centroid([]int64{10, 20})
	for _, id := range []int64{10, 20, 30} {
		idx, ok := snap.IndexOf(id)
		if !ok {
			t.Fatalf("IndexOf(%d) missing", id)
		}
		got, ok1 := snap.CosineToIndex(idx, c)
		want, ok2 := snap.CosineTo(id, c)
		if ok1 != ok2 || got != want {
			t.Fatalf("CosineToIndex(%d) = (%v,%v), want (%v,%v)", id, got, ok1, want, ok2)
		}
	}
	if idx, ok := snap.IndexOf(999); ok || idx != -1 {
		t.Fatalf("IndexOf(999) = (%d,%v), want (-1,false)", idx, ok)
	}
	if _, ok := snap.CosineToIndex(-1, c); ok {
		t.Fatal("CosineToIndex(-1) must return false")
	}
}
