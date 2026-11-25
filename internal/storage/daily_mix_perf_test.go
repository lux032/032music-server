package storage

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func TestRecLibraryParityPlansAndDailyMixBatchItems(t *testing.T) {
	f := newBackfillFixture(t)
	if err := f.store.ImportTrack(f.ctx, ImportInput{
		LibraryID: f.library, RelativePath: "A/01.flac", FileSize: 1000, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Hero (Live)", Album: "Album A", Artists: []string{"AliasSinger", "CoSinger"},
			AlbumArtists: []string{"AliasSinger"}, Composer: "CompOne", DurationMillis: 210000,
			DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.ImportTrack(f.ctx, ImportInput{
		LibraryID: f.library, RelativePath: "B/01.flac", FileSize: 1000, ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title: "Canonical Song", Album: "Album B", Artists: []string{"MainSinger"},
			AlbumArtists: []string{"MainSinger"}, DurationMillis: 190000,
			DiscNumber: 1, TrackNumber: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}

	aliasID := f.artistID(t, "AliasSinger")
	mainID := f.artistID(t, "MainSinger")
	if _, err := f.store.MergeArtists(f.ctx, aliasID, mainID); err != nil {
		t.Fatal(err)
	}

	var track1, track2, album1 int64
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT id, album_id FROM tracks WHERE title = 'Hero (Live)'`).Scan(&track1, &album1); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRowContext(f.ctx, `SELECT id FROM tracks WHERE title = 'Canonical Song'`).Scan(&track2); err != nil {
		t.Fatal(err)
	}

	// 同时绑定 work_tracks 与 album_works 验证 UNION ALL 去重语义一致
	work, err := f.store.CreateWork(f.ctx, WorkInput{Title: "Shared Work", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddWorkTrack(f.ctx, work.ID, WorkTrackInput{TrackID: track1, Role: "op"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(f.ctx, `INSERT INTO album_works(album_id, work_id, role, source) VALUES(?, ?, 'other', 'manual')`, album1, work.ID); err != nil {
		t.Fatal(err)
	}

	// 多来源标签（user 覆盖 llm/audio style，llm 覆盖 audio mood）
	if err := f.store.SaveTrackTagResult(f.ctx, TrackTagMeta{
		TrackID: track1, SourceStamp: "s", InputHash: "h", VocabVersion: 1, PromptVersion: 1, Model: "m", Energy: 4, Vocal: "vocal",
	}, []TrackTag{
		{Kind: "mood", Tag: "epic", Weight: 0.8, Confidence: 0.9, Source: "llm"},
		{Kind: "style", Tag: "pop", Weight: 1, Confidence: 0.8, Source: "llm"},
	}, []TrackTag{
		{Kind: "mood", Tag: "calm", Weight: 0.5, Confidence: 0.5, Source: "audio"},
		{Kind: "style", Tag: "rock", Weight: 0.5, Confidence: 0.5, Source: "audio"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetUserTrackTags(f.ctx, track1, "style", []TrackTag{{Tag: "orchestral", Weight: 1}}); err != nil {
		t.Fatal(err)
	}

	lib, err := f.store.RecLibrary(f.ctx)
	if err != nil || len(lib) != 2 {
		t.Fatalf("RecLibrary = %d err=%v", len(lib), err)
	}
	var got RecTrack
	for _, tr := range lib {
		if tr.ID == track1 {
			got = tr
		}
	}
	if len(got.ArtistIDs) != 2 || got.ArtistIDs[0] != mainID || got.ArtistNames[0] != "MainSinger" {
		t.Fatalf("merged canonical primary artist mismatch: ids=%v names=%v", got.ArtistIDs, got.ArtistNames)
	}
	if len(got.Works) != 1 || got.Works[0] != work.ID {
		t.Fatalf("works must be deduplicated: %v", got.Works)
	}
	if got.TagSource != "user" || len(got.Moods) != 1 || got.Moods["epic"].Source != "llm" || len(got.Styles) != 1 || got.Styles["orchestral"].Source != "user" {
		t.Fatalf("effective tags mismatch: src=%q moods=%+v styles=%+v", got.TagSource, got.Moods, got.Styles)
	}
	wantKey := baseTitle(got.Title) + "\x00" + fmt.Sprint(got.ArtistIDs)
	if got.TitleKey != wantKey {
		t.Fatalf("TitleKey = %q, want %q", got.TitleKey, wantKey)
	}

	// 查询计划断言：主唱查询无相关子查询，作品查询无 UNION 临时去重排序
	paPlan := explainQueryPlan(t, f.store, recPrimaryArtistsQuery)
	if strings.Contains(paPlan, "CORRELATED SCALAR SUBQUERY") {
		t.Fatalf("recPrimaryArtistsQuery still has correlated subquery:\n%s", paPlan)
	}
	worksPlan := explainQueryPlan(t, f.store, recWorksQuery)
	if strings.Contains(worksPlan, "UNION USING TEMP B-TREE") {
		t.Fatalf("recWorksQuery still uses UNION temp B-tree:\n%s", worksPlan)
	}

	// DailyMixItemsByMixIDs 与 SaveDailyMixAsPlaylist 并发写锁
	if err := f.store.SaveDailyMixes(f.ctx, "2026-11-25", []NewDailyMix{
		{
			Slot: 0, ThemeType: "mood", ThemeKey: "epic", Title: "Mix 0", TitleSource: "template", PlanJSON: "{}", GeneratorVersion: 4, DurationMs: 400000,
			Items: []DailyMixItem{{Position: 0, TrackID: track1, Bucket: "familiar", ReasonJSON: "{}"}, {Position: 1, TrackID: track2, Bucket: "explore", ReasonJSON: "{}"}},
		},
		{
			Slot: 1, ThemeType: "style", ThemeKey: "rock", Title: "Mix 1", TitleSource: "template", PlanJSON: "{}", GeneratorVersion: 4, DurationMs: 210000,
			Items: []DailyMixItem{{Position: 0, TrackID: track2, Bucket: "familiar", ReasonJSON: "{}"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	mixes, err := f.store.DailyMixes(f.ctx, "2026-11-25")
	if err != nil || len(mixes) != 2 {
		t.Fatalf("mixes = %d err=%v", len(mixes), err)
	}
	batched, err := f.store.DailyMixItemsByMixIDs(f.ctx, []int64{mixes[0].ID, mixes[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range mixes {
		single, err := f.store.DailyMixItems(f.ctx, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(batched[m.ID], single) {
			t.Fatalf("mix %d batched=%+v want=%+v", m.ID, batched[m.ID], single)
		}
	}

	// SaveDailyMixAsPlaylist 与播放落库并发无快照锁冲突
	var wg sync.WaitGroup
	errCh := make(chan error, 16)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i, m := range mixes {
			if _, err := f.store.SaveDailyMixAsPlaylist(f.ctx, m.ID, fmt.Sprintf("Saved Mix %d", i), "desc"); err != nil {
				errCh <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			if _, err := f.store.RecordPlaybackEvent(f.ctx, PlaybackEventInput{
				ClientID: "dm-client", ClientKind: "web", SessionID: fmt.Sprintf("dm-session-%04d", i), Seq: 1,
				Type: "start", State: "playing", TrackID: track1, PositionMillis: 0, DurationMillis: 210000,
			}); err != nil {
				errCh <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
