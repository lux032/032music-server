package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestArtistBiographyRetranslateBatchAndPlans(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "NativeRich")
	f.importArtistTrack(t, "B/01.flac", "ForeignOnly")
	f.importArtistTrack(t, "C/01.flac", "MergedSource")
	f.importArtistTrack(t, "D/01.flac", "MergedTarget")
	f.importArtistTrack(t, "E/01.flac", "NoBio")

	nativeRich := f.artistID(t, "NativeRich")
	foreignOnly := f.artistID(t, "ForeignOnly")
	mergedSource := f.artistID(t, "MergedSource")
	mergedTarget := f.artistID(t, "MergedTarget")
	noBio := f.artistID(t, "NoBio")

	// NativeRich: 充实中文维基简介 -> zh-Hans 应跳过，但 zh-Hant 必须保留
	longZh := strings.Repeat("这是一个包含详细经历与代表作品信息的中文歌手介绍。", 10)
	if err := f.store.UpsertArtistBiography(f.ctx, nativeRich, ArtistBiography{Source: "wikipedia", Language: "zh", Biography: longZh}); err != nil {
		t.Fatal(err)
	}
	// ForeignOnly: 仅有日文维基简介 -> zh-Hans 和 zh-Hant 都应纳入重译候选
	longJa := strings.Repeat("日本のシンガーソングライターであり数多くのアニメ主題歌を担当した。", 6)
	if err := f.store.UpsertArtistBiography(f.ctx, foreignOnly, ArtistBiography{Source: "wikipedia", Language: "ja", Biography: longJa}); err != nil {
		t.Fatal(err)
	}
	// MergedTarget 合并 MergedSource：MergedSource 有充实中文简介 -> 继承后 zh-Hans 跳过，zh-Hant 保留
	if _, err := f.store.MergeArtists(f.ctx, mergedSource, mergedTarget); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpsertArtistBiography(f.ctx, mergedSource, ArtistBiography{Source: "wikipedia", Language: "zh", Biography: longZh}); err != nil {
		t.Fatal(err)
	}
	_ = noBio

	zhHansCands, err := f.store.ArtistBiographyBackfillCandidatesForMode(f.ctx, "retranslate", "zh-Hans")
	if err != nil {
		t.Fatal(err)
	}
	zhHansIDs := map[int64]bool{}
	for _, c := range zhHansCands {
		zhHansIDs[c.ID] = true
	}
	if !zhHansIDs[foreignOnly] || len(zhHansIDs) != 1 {
		t.Fatalf("zh-Hans retranslate candidates = %v, want only foreignOnly(%d)", zhHansIDs, foreignOnly)
	}

	zhHantCands, err := f.store.ArtistBiographyBackfillCandidatesForMode(f.ctx, "retranslate", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	zhHantIDs := map[int64]bool{}
	for _, c := range zhHantCands {
		zhHantIDs[c.ID] = true
	}
	if !zhHantIDs[nativeRich] || !zhHantIDs[foreignOnly] || !zhHantIDs[mergedTarget] || len(zhHantIDs) != 3 {
		t.Fatalf("zh-Hant retranslate candidates = %v, want nativeRich+foreignOnly+mergedTarget", zhHantIDs)
	}

	// Counts 口径检查（含参数化语言 IN）
	missing, total, err := f.store.ArtistBiographyBackfillCounts(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || missing != 1 {
		t.Fatalf("counts missing=%d total=%d, want missing=1 total=4", missing, total)
	}

	// EXPLAIN QUERY PLAN: performer 检查命中 idx_track_artists_role，displayable 检查不含 json_each，retranslate 拆分为索引 EXISTS
	performerPlan := explainQueryPlan(t, f.store, `SELECT EXISTS(SELECT 1 FROM artists a WHERE a.id=? AND a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+`)`, foreignOnly)
	if !strings.Contains(performerPlan, "idx_track_artists_role") {
		t.Fatalf("performer check did not use idx_track_artists_role:\n%s", performerPlan)
	}
	cond, args := artistBiographyDisplayableSQL("a", f.store.biographySettingsOrDefault(f.ctx))
	if strings.Contains(cond, "json_each") {
		t.Fatalf("displayable SQL still contains json_each: %s", cond)
	}
	dispPlan := explainQueryPlan(t, f.store, `SELECT a.id FROM artists a WHERE a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+` AND COALESCE(a.user_biography,'')='' AND NOT `+cond, args...)
	if strings.Contains(dispPlan, "json_each") || !strings.Contains(dispPlan, "idx_artist_biographies_artist") {
		t.Fatalf("displayable plan unexpected:\n%s", dispPlan)
	}
	retransPlan := explainQueryPlan(t, f.store, artistBiographyRetranslateCandidatesSQL)
	if !strings.Contains(retransPlan, "idx_artist_biographies_artist") {
		t.Fatalf("retranslate plan did not use idx_artist_biographies_artist:\n%s", retransPlan)
	}
}

func TestArtistImageCacheScanAndEnrichmentDynamicBranches(t *testing.T) {
	f := newBackfillFixture(t)
	f.importArtistTrack(t, "A/01.flac", "MatchedArtist")
	f.importArtistTrack(t, "B/01.flac", "PendingCandArtist")
	f.importArtistTrack(t, "C/01.flac", "CooldownTier1Artist")
	f.importArtistTrack(t, "D/01.flac", "CooldownWebArtist")

	matchedID := f.artistID(t, "MatchedArtist")
	pendingID := f.artistID(t, "PendingCandArtist")
	tier1ID := f.artistID(t, "CooldownTier1Artist")
	webID := f.artistID(t, "CooldownWebArtist")

	f.confirmIdentity(t, matchedID, "musicbrainz", "mb-1", "")
	// PendingCandArtist: 有 pending 候选头像 -> 非 rescan_all 模式跳过
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO artist_image_candidates(artist_id, source, image_url, status) VALUES(?, 'web', 'https://example.test/p.jpg', 'pending')`, pendingID); err != nil {
		t.Fatal(err)
	}
	// CooldownTier1Artist: tier_reached='structured' 在冷却期；CooldownWebArtist: tier_reached='web_fallback' 在冷却期
	future := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO artist_image_attempts(artist_id, tier_reached, last_outcome, cooldown_until) VALUES(?, 'structured', 'no_url', ?)`, tier1ID, future); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO artist_image_attempts(artist_id, tier_reached, last_outcome, cooldown_until) VALUES(?, 'web_fallback', 'no_url', ?)`, webID, future); err != nil {
		t.Fatal(err)
	}

	// 1) web_fallback_enabled=0 时，structured 和 web_fallback 冷却均应跳过
	settings, err := f.store.ArtistImageSettings(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.WebFallbackEnabled = false
	if err := f.store.SaveArtistImageSettings(f.ctx, settings); err != nil {
		t.Fatal(err)
	}
	rowsAllDisabled, err := f.store.ArtistImageBackfillCacheScanForMode(f.ctx, "all")
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsAllDisabled) != 1 || rowsAllDisabled[0].Candidate.ID != matchedID {
		t.Fatalf("all (web_fallback=0) = %+v, want only matchedID", rowsAllDisabled)
	}

	// 2) web_fallback_enabled=1 时，tier_reached='structured' 允许重新尝试，web_fallback 仍跳过
	settings.WebFallbackEnabled = true
	if err := f.store.SaveArtistImageSettings(f.ctx, settings); err != nil {
		t.Fatal(err)
	}
	rowsAllEnabled, err := f.store.ArtistImageBackfillCacheScanForMode(f.ctx, "all")
	if err != nil {
		t.Fatal(err)
	}
	enabledIDs := map[int64]bool{}
	for _, r := range rowsAllEnabled {
		enabledIDs[r.Candidate.ID] = true
	}
	if !enabledIDs[matchedID] || !enabledIDs[tier1ID] || enabledIDs[pendingID] || enabledIDs[webID] {
		t.Fatalf("all (web_fallback=1) = %v, want matched+tier1 only", enabledIDs)
	}

	// 3) rescan_all 忽略冷却与待审核候选
	rowsRescan, err := f.store.ArtistImageBackfillCacheScanForMode(f.ctx, "rescan_all")
	if err != nil || len(rowsRescan) != 4 {
		t.Fatalf("rescan_all = %d err=%v, want 4", len(rowsRescan), err)
	}

	// 4) EXPLAIN QUERY PLAN: ArtistImageBackfillCacheScanForMode 命中 idx_artist_image_candidates_artist_status 且无 artist_image_settings 子查询
	imgSQL := buildArtistImageBackfillCacheScanQuery("all", true)
	if strings.Contains(imgSQL, "artist_image_settings") {
		t.Fatalf("image scan SQL still queries artist_image_settings: %s", imgSQL)
	}
	imgPlan := explainQueryPlan(t, f.store, imgSQL)
	if !strings.Contains(imgPlan, "idx_artist_image_candidates_artist_status") {
		t.Fatalf("image scan plan did not use idx_artist_image_candidates_artist_status:\n%s", imgPlan)
	}

	// 5) ArtistForMatching 单歌手主键直查
	singleMatch, err := f.store.ArtistForMatching(f.ctx, matchedID)
	if err != nil || singleMatch.ID != matchedID {
		t.Fatalf("ArtistForMatching = %+v err=%v", singleMatch, err)
	}
	matchPlan := explainQueryPlan(t, f.store, buildArtistsForMatchingQuery(true), matchedID)
	if !strings.Contains(matchPlan, "SEARCH ar USING INTEGER PRIMARY KEY") {
		t.Fatalf("ArtistForMatching did not use primary key seek:\n%s", matchPlan)
	}

	// 6) AlbumsForEnrichment & WorksForEnrichment 强制模式动态省略无效过滤分支
	var albumID int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT id FROM albums ORDER BY id LIMIT 1`).Scan(&albumID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_external_profiles(album_id, source, external_id, title, raw_json, fetched_at) VALUES(?, 'vgmdb', 'ext-1', 'T', '{}', strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, albumID); err != nil {
		t.Fatal(err)
	}
	nonForceAlbums, err := f.store.AlbumsForEnrichment(f.ctx, "vgmdb", false, 100)
	if err != nil || len(nonForceAlbums) != 3 {
		t.Fatalf("non-force albums = %d err=%v, want 3", len(nonForceAlbums), err)
	}
	forceAlbums, err := f.store.AlbumsForEnrichment(f.ctx, "vgmdb", true, 100)
	if err != nil || len(forceAlbums) != 4 {
		t.Fatalf("force albums = %d err=%v, want 4", len(forceAlbums), err)
	}
	if strings.Contains(buildAlbumsForEnrichmentQuery(true), "album_external_profiles") {
		t.Fatal("force AlbumsForEnrichment query should omit album_external_profiles NOT EXISTS")
	}

	workID := makeBoundReferencedWork(t, f.albumMergeFixture, "ForceWork", "99", `{"id":99,"type":2,"platform":""}`)
	nonForceWorks, err := f.store.WorksForEnrichment(f.ctx, "bangumi", false, 10, workID)
	if err != nil || len(nonForceWorks) != 0 {
		t.Fatalf("non-force works = %+v err=%v, want 0", nonForceWorks, err)
	}
	forceWorks, err := f.store.WorksForEnrichment(f.ctx, "bangumi", true, 10, workID)
	if err != nil || len(forceWorks) != 1 || forceWorks[0].ID != workID {
		t.Fatalf("force works = %+v err=%v, want work %d", forceWorks, err, workID)
	}
	forceWorkSQL := buildWorksForEnrichmentQuery(true, true, true)
	if strings.Contains(forceWorkSQL, "json_extract") || strings.Contains(forceWorkSQL, "work_match_candidates") {
		t.Fatalf("force WorksForEnrichment should omit non-force predicates: %s", forceWorkSQL)
	}
	forceWorkPlan := explainQueryPlan(t, f.store, forceWorkSQL, "bangumi", workID, 10)
	if !strings.Contains(forceWorkPlan, "SEARCH w USING INTEGER PRIMARY KEY") {
		t.Fatalf("force WorksForEnrichment target lookup did not seek PK:\n%s", forceWorkPlan)
	}
}

func TestBackgroundCandidatePerformanceRegression(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "bg_perf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}

	const (
		albumCount      = 200
		tracksPerAlbum  = 10
		totalTracks     = albumCount * tracksPerAlbum
	)
	batch := make([]ImportInput, 0, 50)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if _, err := s.ImportTrackBatch(ctx, batch); err != nil {
			t.Fatal(err)
		}
		batch = batch[:0]
	}
	for a := 0; a < albumCount; a++ {
		artist := fmt.Sprintf("Artist %03d", a%50)
		album := fmt.Sprintf("Album %03d", a)
		for tr := 0; tr < tracksPerAlbum; tr++ {
			idx := a*tracksPerAlbum + tr
			batch = append(batch, ImportInput{
				LibraryID:      lib.ID,
				RelativePath:   fmt.Sprintf("A%03d/%02d.flac", a, tr+1),
				FileSize:       int64(5_000_000 + idx),
				ModifiedAtNS:   1700000000 + int64(idx),
				HasExternalLRC: idx%5 == 0,
				Metadata: metadata.AudioMetadata{
					Title:          fmt.Sprintf("Song %04d", idx),
					Album:          album,
					Artists:        []string{artist},
					AlbumArtists:   []string{artist},
					Genres:         []string{"J-Pop", "Anime"},
					DurationMillis: 210000,
					Year:           2020 + (a % 6),
					DiscNumber:     1,
					TrackNumber:    tr + 1,
				},
			})
			if len(batch) == 50 {
				flush()
			}
		}
	}
	flush()

	// 标记一半曲目已打标，模拟真实增量/半程曲库状态
	now := time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO track_tag_meta(track_id, source_stamp, input_hash, vocab_version, prompt_version, model)
		SELECT id, 'init', 'h', 1, 1, 'm' FROM tracks WHERE id <= ?`, totalTracks/2); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now()
	cands, err := s.NextTaggingCandidates(ctx, false, 1, 1, now, nil, 8)
	phase1Dur := time.Since(t0)
	if err != nil || len(cands) != 8 || !cands[0].Fresh {
		t.Fatalf("Phase 1 NextTaggingCandidates = %d err=%v", len(cands), err)
	}

	t1 := time.Now()
	stamp, ok, err := s.TrackTaggingStamp(ctx, cands[0].TrackID)
	singleStampDur := time.Since(t1)
	if err != nil || !ok || stamp != cands[0].Stamp {
		t.Fatalf("TrackTaggingStamp = (%q,%v,%v)", stamp, ok, err)
	}

	t2 := time.Now()
	tagStats, err := s.TaggingStats(ctx, false, 1, 1, now)
	tagStatsDur := time.Since(t2)
	if err != nil || tagStats.Eligible != totalTracks {
		t.Fatalf("TaggingStats = %+v err=%v", tagStats, err)
	}

	t3 := time.Now()
	audioCand, ok, err := s.NextAudioAnalysisCandidate(ctx, testAnalysisVersion, now)
	audioNextDur := time.Since(t3)
	if err != nil || !ok || audioCand.TrackID <= 0 {
		t.Fatalf("NextAudioAnalysisCandidate = (%+v,%v,%v)", audioCand, ok, err)
	}

	t4 := time.Now()
	calCand, ok, err := s.NextAudioCalibrationCandidate(ctx, testAnalysisVersion, 100, now)
	calDur := time.Since(t4)
	if err != nil || !ok || calCand.TrackID <= 0 {
		t.Fatalf("NextAudioCalibrationCandidate = (%+v,%v,%v)", calCand, ok, err)
	}

	t5 := time.Now()
	audioStats, err := s.AudioAnalysisStats(ctx, testAnalysisVersion, now)
	audioStatsDur := time.Since(t5)
	if err != nil || audioStats.Eligible != totalTracks {
		t.Fatalf("AudioAnalysisStats = %+v err=%v", audioStats, err)
	}

	t6 := time.Now()
	eligibleCnt, err := s.AudioEligibleTrackCount(ctx)
	eligibleDur := time.Since(t6)
	if err != nil || eligibleCnt != totalTracks {
		t.Fatalf("AudioEligibleTrackCount = %d err=%v", eligibleCnt, err)
	}

	t.Logf("2000-track perf: NextTaggingCandidates(fresh,8)=%s TrackTaggingStamp=%s TaggingStats=%s NextAudioAnalysis=%s NextAudioCalibration(100)=%s AudioAnalysisStats=%s AudioEligibleCount=%s",
		phase1Dur, singleStampDur, tagStatsDur, audioNextDur, calDur, audioStatsDur, eligibleDur)

	// 宽松 CI 保护阈值（单核/race 下也留足余量，同时能捕获 O(N²) 退化）
	if phase1Dur > 500*time.Millisecond {
		t.Fatalf("Phase 1 NextTaggingCandidates too slow: %s", phase1Dur)
	}
	if singleStampDur > 100*time.Millisecond {
		t.Fatalf("TrackTaggingStamp too slow: %s", singleStampDur)
	}
	if audioNextDur > 200*time.Millisecond {
		t.Fatalf("NextAudioAnalysisCandidate too slow: %s", audioNextDur)
	}
}
