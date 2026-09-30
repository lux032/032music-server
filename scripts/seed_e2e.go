//go:build ignore

// seed_e2e.go 为 Playwright e2e fixture 数据库写入批次 3 UI 的示例数据。
// 数据尽量接近真实：紅蓮華 单曲专辑、infinite synthesis 6 原创专辑、鬼滅の刃 系列三部。
// 幂等：已有作品数据时直接退出。由 scripts/e2e-global-setup.mjs 在 fixture 启动后调用。
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

// 退出码约定：0=成功或已播种；2=fixture 首次扫描还没完成，global setup 可重试；
// 1=真实错误（编译问题除外），立即失败。
func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "seed_e2e: "+format+"\n", args...)
	os.Exit(1)
}

func notReady(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "seed_e2e: scan not ready: "+format+"\n", args...)
	os.Exit(2)
}

// 播种标记：最后一步写入的 bocchi work_match_candidate。
const seedMarkerWork = "ぼっち・ざ・ろっく！"
const seedMarkerExternalID = "328609"

func main() {
	if len(os.Args) < 2 {
		fail("usage: seed_e2e.go <db-path>")
	}
	dbPath := os.Args[1]
	ctx := context.Background()

	// 先用原始连接查播种标记：有标记 → 幂等退出；有作品但没标记 → 半播种状态，明确报错。
	raw, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		notReady("open raw db: %v", err)
	}
	var marker int
	err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_match_candidates c JOIN works w ON w.id=c.work_id WHERE w.title=? AND c.external_id=? AND c.status='candidate'`, seedMarkerWork, seedMarkerExternalID).Scan(&marker)
	if err != nil {
		raw.Close()
		notReady("marker query: %v", err)
	}
	var workCount int
	if err = raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM works`).Scan(&workCount); err != nil {
		raw.Close()
		notReady("works count query: %v", err)
	}
	raw.Close()
	if marker > 0 {
		return // 已播种
	}
	if workCount > 0 {
		fail("database has %d works but lacks the seed marker (%s / %s): half-seeded state, refusing to continue", workCount, seedMarkerWork, seedMarkerExternalID)
	}

	s, err := storage.Open(dbPath)
	if err != nil {
		fail("open db: %v", err)
	}
	defer s.Close()

	// fixture 扫描完成后才会有曲目；否则以退出码 2 告知 global setup 重试。
	compAlbums, err := s.ListAlbums(ctx, storage.Filters{Limit: 10})
	if err != nil || len(compAlbums) == 0 {
		notReady("no albums found: %v", err)
	}
	compAlbumID := compAlbums[0].ID
	compTracks, err := s.ListTracks(ctx, storage.Filters{AlbumID: compAlbumID, Limit: 20})
	if err != nil {
		fail("list compilation tracks: %v", err)
	}
	if len(compTracks) < 20 {
		// 等 fixture 的 20 首曲目全部扫描完成再写种子，避免写到一半的数据集。
		notReady("fixture scan incomplete: %d/20 tracks", len(compTracks))
	}

	// ImportTrack 需要 library id，storage API 不暴露列表，直接读表。
	libDB, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		fail("open raw db: %v", err)
	}
	var libraryID int64
	if err = libDB.QueryRowContext(ctx, `SELECT id FROM libraries ORDER BY id LIMIT 1`).Scan(&libraryID); err != nil {
		fail("read library id: %v", err)
	}
	libDB.Close()

	importTracks := func(album, artist string, year int, titles ...string) []int64 {
		ids := make([]int64, 0, len(titles))
		for i, title := range titles {
			err := s.ImportTrack(ctx, storage.ImportInput{
				LibraryID:    libraryID,
				RelativePath: fmt.Sprintf("%s/%02d.flac", album, i+1),
				FileSize:     100,
				ModifiedAtNS: int64(i + 1),
				Metadata: metadata.AudioMetadata{
					Title:          title,
					Album:          album,
					Artists:        []string{artist},
					AlbumArtists:   []string{artist},
					Year:           year,
					DiscNumber:     1,
					TrackNumber:    i + 1,
					Composer:       map[string]string{"LiSA": "LiSA"}[artist],
					Lyricist:       map[string]string{"LiSA": "LiSA"}[artist],
					DurationMillis: 240000,
				},
			})
			if err != nil {
				fail("import %s/%s: %v", album, title, err)
			}
		}
		albums, err := s.ListAlbums(ctx, storage.Filters{Query: album})
		if err != nil || len(albums) == 0 {
			fail("album %q not found after import: %v", album, err)
		}
		tracks, err := s.ListTracks(ctx, storage.Filters{AlbumID: albums[0].ID, Limit: 50})
		if err != nil {
			fail("list tracks of %q: %v", album, err)
		}
		for _, tr := range tracks {
			ids = append(ids, tr.ID)
		}
		return ids
	}

	// 紅蓮華 单曲专辑（LiSA, 2019）：标题曲 + 两首 c/w + Instrumental
	gurengeIDs := importTracks("紅蓮華", "LiSA", 2019,
		"紅蓮華", "PROPAGANDA", "やくそくのうた", "紅蓮華 -Instrumental-")
	gurengeAlbum, err := s.ListAlbums(ctx, storage.Filters{Query: "紅蓮華"})
	if err != nil || len(gurengeAlbum) != 1 {
		fail("gurenge album: %v", err)
	}
	if err = s.UpdateAlbum(ctx, gurengeAlbum[0].ID, storage.AlbumEdit{AlbumType: "single"}); err != nil {
		fail("mark single: %v", err)
	}

	// infinite synthesis 6 原创专辑（fripSide, 2022）
	is6IDs := importTracks("infinite synthesis 6", "fripSide", 2022,
		"legendary future", "Leap of faith", "Daybreak", "Your breeze",
		"for Seasons", "The way home", "Trust in you", "faraway sky",
		"crossroads", "Fox messages", "on this night", "regret")
	is6Album, err := s.ListAlbums(ctx, storage.Filters{Query: "infinite synthesis 6"})
	if err != nil || len(is6Album) != 1 {
		fail("is6 album: %v", err)
	}

	// 精选集：复用 fixture 的 E2E Album，标记为 compilation
	if err = s.UpdateAlbum(ctx, compAlbumID, storage.AlbumEdit{AlbumType: "compilation"}); err != nil {
		fail("mark compilation: %v", err)
	}

	mustWork := func(input storage.WorkInput) storage.Work {
		w, err := s.CreateWork(ctx, input)
		if err != nil {
			fail("create work %s: %v", input.Title, err)
		}
		return w
	}

	// 鬼滅の刃 系列三部
	kimetsu1 := mustWork(storage.WorkInput{Title: "鬼滅の刃 竈門炭治郎 立志編", TranslatedTitle: "鬼灭之刃 灶门炭治郎 立志篇", ReadingTitle: "きめつのやいば", Type: "anime", Year: 2019, ExternalID: "245665"})
	kimetsu2 := mustWork(storage.WorkInput{Title: "劇場版「鬼滅の刃」無限列車編", TranslatedTitle: "剧场版 鬼灭之刃 无限列车篇", Type: "movie", Year: 2020, ExternalID: "285756"})
	kimetsu3 := mustWork(storage.WorkInput{Title: "鬼滅の刃 遊郭編", TranslatedTitle: "鬼灭之刃 游郭篇", Type: "anime", Year: 2021, ExternalID: "326624"})
	kingsraid := mustWork(storage.WorkInput{Title: "キングスレイド 意志を継ぐものたち", TranslatedTitle: "王之逆袭 意志的继承者", Type: "anime", Year: 2020, ExternalID: "292870"})
	shikkakumon := mustWork(storage.WorkInput{Title: "失格紋の最強賢者", TranslatedTitle: "失格纹的最强贤者", Type: "anime", Year: 2022, ExternalID: "326870"})
	bocchi := mustWork(storage.WorkInput{Title: "ぼっち・ざ・ろっく！", TranslatedTitle: "孤独摇滚！", Type: "anime", Year: 2022})

	if _, err = s.ApplyAutoSeries(ctx, 0, [][]int64{{kimetsu1.ID, kimetsu2.ID, kimetsu3.ID}}); err != nil {
		fail("apply series: %v", err)
	}

	linkAlbum := func(workID, albumID int64, role string) {
		if err := s.AddWorkAlbum(ctx, workID, albumID, role); err != nil {
			fail("link album %d -> work %d: %v", albumID, workID, err)
		}
	}
	linkTrack := func(workID, trackID int64, role string) {
		if err := s.AddWorkTrack(ctx, workID, storage.WorkTrackInput{TrackID: trackID, Role: role}); err != nil {
			fail("link track %d -> work %d: %v", trackID, workID, err)
		}
	}

	// 紅蓮華：整张关联鬼滅 立志編（other），标题曲为 Bangumi 曲目级 OP
	linkAlbum(kimetsu1.ID, gurengeAlbum[0].ID, "other")
	linkTrack(kimetsu1.ID, gurengeIDs[0], "op")

	// infinite synthesis 6：legendary future → キングスレイド OP；Leap of faith → 失格紋 OP
	linkAlbum(kingsraid.ID, is6Album[0].ID, "other")
	linkAlbum(shikkakumon.ID, is6Album[0].ID, "other")
	linkTrack(kingsraid.ID, is6IDs[0], "op")
	linkTrack(shikkakumon.ID, is6IDs[1], "op")

	// 精选集：多部作品的曲目级关联（胶囊 +N 折叠示例）
	linkTrack(kimetsu3.ID, compTracks[0].ID, "op")
	linkTrack(kimetsu1.ID, compTracks[1].ID, "ed")
	linkTrack(shikkakumon.ID, compTracks[2].ID, "op")
	linkTrack(bocchi.ID, compTracks[3].ID, "insert")
	linkTrack(kingsraid.ID, compTracks[4].ID, "ed")
	linkTrack(kimetsu2.ID, compTracks[7].ID, "theme") // 剧场版《炎》位，避免无引用作品

	// 标题曲的 OP 关联模拟 Bangumi 自动确认写入（source='bangumi'）
	rw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fail("open rw db: %v", err)
	}
	defer rw.Close()
	for _, trackID := range []int64{gurengeIDs[0], is6IDs[0], is6IDs[1]} {
		if _, err = rw.ExecContext(ctx, `UPDATE work_tracks SET source='bangumi' WHERE track_id=?`, trackID); err != nil {
			fail("mark bangumi source on track %d: %v", trackID, err)
		}
	}

	// -------------------------------------------------------------
	// 批次 6 种子（必须在 bocchi 播种标记写入之前完成）：系列建议与系列
	// 管理的示例数据。所有系列/建议标题都排在 鬼滅（U+9B3C）之后或不影响
	// 鬼滅 系列行，避免干扰批次 3 的“第一条系列行”断言。
	// -------------------------------------------------------------
	hinokami := mustWork(storage.WorkInput{Title: "鬼滅の刃 ヒノカミ血風譚", TranslatedTitle: "鬼灭之刃 火之神血风谭", Type: "game", Year: 2021, ExternalID: "302688"})
	fateA := mustWork(storage.WorkInput{Title: "Fate/stay night", Type: "anime", Year: 2006, ExternalID: "290"})
	fateB := mustWork(storage.WorkInput{Title: "Fate/stay night [Unlimited Blade Works]", Type: "anime", Year: 2014, ExternalID: "24255"})
	index := mustWork(storage.WorkInput{Title: "とある魔術の禁書目録", TranslatedTitle: "魔法禁书目录", Type: "anime", Year: 2008, ExternalID: "1014"})
	railgun := mustWork(storage.WorkInput{Title: "とある科学の超電磁砲", TranslatedTitle: "某科学的超电磁炮", Type: "anime", Year: 2009, ExternalID: "2585"})
	ryuuA := mustWork(storage.WorkInput{Title: "龍の国 第一部", Type: "anime", Year: 2016})
	ryuuB := mustWork(storage.WorkInput{Title: "龍の国 第二部", Type: "anime", Year: 2018})

	// 避免新作品进入“受保护但无引用”区：给它们一张单独的专辑（不加进
	// 精选集，否则会改变批次 3 胶囊 +N 断言的作品数）。
	b6Tracks := importTracks("E2E Batch6 Album", "E2E Artist", 2024,
		"B6 Song 1", "B6 Song 2", "B6 Song 3", "B6 Song 4", "B6 Song 5", "B6 Song 6", "B6 Song 7")
	linkTrack(hinokami.ID, b6Tracks[0], "op")
	linkTrack(fateA.ID, b6Tracks[1], "op")
	linkTrack(fateB.ID, b6Tracks[2], "theme")
	linkTrack(index.ID, b6Tracks[3], "op")
	linkTrack(railgun.ID, b6Tracks[4], "op")
	linkTrack(ryuuA.ID, b6Tracks[5], "op")
	linkTrack(ryuuB.ID, b6Tracks[6], "op")

	// 龍の国：两部作品各在一个改过名的系列里（合并建议需要选择名字）。
	ryuuSeriesA, err := s.CreateWorkSeries(ctx, "龍の国シリーズ甲", []int64{ryuuA.ID})
	if err != nil {
		fail("create ryuu series A: %v", err)
	}
	ryuuSeriesB, err := s.CreateWorkSeries(ctx, "龍の国シリーズ乙", []int64{ryuuB.ID})
	if err != nil {
		fail("create ryuu series B: %v", err)
	}
	_ = ryuuSeriesA
	_ = ryuuSeriesB

	// 四条系列建议：加入已有系列（接受）、新建系列（接受）、合并需选名
	// （选择名字后接受）、普通建议（拒绝）。RelationAB 是 A 的关系列表里
	// 对 B 的标注（描述 B），方向与生产语义一致（见 series_bangumi.go）。
	if err = s.ReplaceSeriesSuggestions(ctx, 1, []storage.SeriesSuggestionInput{
		{WorkA: kimetsu1.ID, WorkB: hinokami.ID, SubjectA: 245665, SubjectB: 302688, RelationAB: "游戏", RelationBA: "动画", Kind: "cross"},
		{WorkA: fateA.ID, WorkB: fateB.ID, SubjectA: 290, SubjectB: 24255, RelationAB: "不同演绎", RelationBA: "不同演绎", Kind: "cross"},
		{WorkA: ryuuA.ID, WorkB: ryuuB.ID, SubjectA: 770001, SubjectB: 770002, RelationAB: "续集", RelationBA: "前传", Kind: "sequel"},
		{WorkA: index.ID, WorkB: railgun.ID, SubjectA: 1014, SubjectB: 2585, RelationAB: "衍生", RelationBA: "主线故事", Kind: "cross"},
	}, []int64{kimetsu1.ID, hinokami.ID, fateA.ID, fateB.ID, ryuuA.ID, ryuuB.ID, index.ID, railgun.ID}); err != nil {
		fail("seed series suggestions: %v", err)
	}

	// 待审候选：专辑 / 曲目 / 作品对齐各一条
	if err = s.SaveAlbumSubjectCandidates(ctx, gurengeAlbum[0].ID, []storage.AlbumSubjectCandidate{
		{
			ExternalID: "262706",
			Title:      "紅蓮華",
			Artist:     "LiSA",
			Score:      88,
			Evidence:   []string{"专辑名完全一致", "歌手一致 (LiSA)", "Bangumi 登记主题歌"},
			Tieups: []storage.BangumiTieup{
				{SubjectID: 245665, Title: "鬼滅の刃 竈門炭治郎 立志編", NameCN: "鬼灭之刃 灶门炭治郎 立志篇", Type: "anime", Role: "op", Date: "2019-04-06"},
			},
		},
	}); err != nil {
		fail("save album candidate: %v", err)
	}
	if err = s.SaveTrackSubjectCandidates(ctx, compTracks[5].ID, []storage.TrackSubjectCandidate{
		{
			ExternalID: "359216",
			Title:      "残響散歌 / 朝が来る",
			Artist:     "Aimer",
			MatchKind:  "multi_title",
			Evidence:   []string{"歌手一致 (Aimer)", "条目含两首独立单曲"},
			Tieups: []storage.BangumiTieup{
				{SubjectID: 326624, Title: "鬼滅の刃 遊郭編", NameCN: "鬼灭之刃 游郭篇", Type: "anime", Role: "op", Date: "2021-12-05"},
			},
		},
	}); err != nil {
		fail("save track candidate: %v", err)
	}
	if err = s.ReplaceWorkMatchCandidates(ctx, bocchi.ID, []storage.WorkMatchCandidate{
		{
			WorkID:          bocchi.ID,
			Source:          "bangumi",
			ExternalID:      "328609",
			Title:           "ぼっち・ざ・ろっく！",
			TranslatedTitle: "孤独摇滚！",
			Type:            "anime",
			Year:            2022,
			Score:           96,
			Status:          "candidate",
		},
	}); err != nil {
		fail("save work candidate: %v", err)
	}
}
