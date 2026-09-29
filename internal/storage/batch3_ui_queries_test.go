package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lux032/032music-server/internal/metadata"
)

func setupBatch3Store(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "batch3_ui.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureLibrary(ctx, "Music", "/music"); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func TestBatch3AlbumAndWorkQueries(t *testing.T) {
	s, ctx := setupBatch3Store(t)
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}

	// Import an album with 2 tracks
	for i := 1; i <= 2; i++ {
		err = s.ImportTrack(ctx, ImportInput{
			LibraryID:    lib.ID,
			RelativePath: "AlbumA/track" + string(rune('0'+i)) + ".flac",
			FileSize:     100,
			ModifiedAtNS: int64(i),
			Metadata: metadata.AudioMetadata{
				Title:        "Track " + string(rune('0'+i)),
				Album:        "Album A",
				Artists:      []string{"Band A"},
				AlbumArtists: []string{"Band A"},
				DiscNumber:   1,
				TrackNumber:  i,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	albums, err := s.ListAlbums(ctx, Filters{Query: "Album A"})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums: %+v %v", albums, err)
	}
	albumID := albums[0].ID

	tracks, err := s.ListTracks(ctx, Filters{AlbumID: albumID})
	if err != nil || len(tracks) != 2 {
		t.Fatalf("tracks: %+v %v", tracks, err)
	}

	// Create Work 1 and Work 2
	work1, err := s.CreateWork(ctx, WorkInput{Title: "Work 1", Type: "anime", Year: 2021})
	if err != nil {
		t.Fatal(err)
	}
	work2, err := s.CreateWork(ctx, WorkInput{Title: "Work 2", Type: "game", Year: 2022})
	if err != nil {
		t.Fatal(err)
	}

	// Add album-level work1 with role "ost"
	if err = s.AddWorkAlbum(ctx, work1.ID, albumID, "ost"); err != nil {
		t.Fatal(err)
	}

	// Add track-level work2 to track 1 with role "op"
	if err = s.AddWorkTrack(ctx, work2.ID, WorkTrackInput{TrackID: tracks[0].ID, Role: "op"}); err != nil {
		t.Fatal(err)
	}

	// Test AlbumLevelWorks
	levelWorks, err := s.AlbumLevelWorks(ctx, albumID)
	if err != nil {
		t.Fatalf("AlbumLevelWorks: %v", err)
	}
	if len(levelWorks) != 1 || levelWorks[0].Work.ID != work1.ID || levelWorks[0].Role != "ost" {
		t.Fatalf("unexpected AlbumLevelWorks: %+v", levelWorks)
	}

	// Test TrackWorksForAlbum
	trackWorks, err := s.TrackWorksForAlbum(ctx, albumID)
	if err != nil {
		t.Fatalf("TrackWorksForAlbum: %v", err)
	}
	if len(trackWorks) != 1 || trackWorks[0].TrackID != tracks[0].ID || trackWorks[0].Work.ID != work2.ID || trackWorks[0].Role != "op" {
		t.Fatalf("unexpected TrackWorksForAlbum: %+v", trackWorks)
	}

	// Test AlbumsForWork for Work 1
	workAlbums, err := s.AlbumsForWork(ctx, work1.ID)
	if err != nil {
		t.Fatalf("AlbumsForWork: %v", err)
	}
	if len(workAlbums) != 1 || workAlbums[0].AlbumID != albumID || workAlbums[0].Role != "ost" || workAlbums[0].AlbumRole != "ost" {
		t.Fatalf("unexpected AlbumsForWork: %+v", workAlbums)
	}
	if workAlbums[0].Artist != "Band A" || workAlbums[0].TrackCount != 2 {
		t.Fatalf("AlbumsForWork metadata: %+v", workAlbums[0])
	}

	// Test AlbumsForWork for Work 2 (has no album-level link, so should be empty)
	work2Albums, err := s.AlbumsForWork(ctx, work2.ID)
	if err != nil {
		t.Fatalf("AlbumsForWork work2: %v", err)
	}
	if len(work2Albums) != 0 {
		t.Fatalf("expected 0 album-level links for work2, got %+v", work2Albums)
	}
}

func TestPendingWorkReviewCountsAndD17TrackArtist(t *testing.T) {
	s, ctx := setupBatch3Store(t)
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}

	err = s.ImportTrack(ctx, ImportInput{
		LibraryID:    lib.ID,
		RelativePath: "AlbumB/01.flac",
		FileSize:     100,
		ModifiedAtNS: 1,
		Metadata: metadata.AudioMetadata{
			Title:        "Title Song",
			Album:        "Album B",
			Artists:      []string{"Primary Singer"},
			AlbumArtists: []string{"Primary Singer"},
			DiscNumber:   1,
			TrackNumber:  1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	tracks, _ := s.ListTracks(ctx, Filters{Query: "Title Song"})
	if len(tracks) != 1 {
		t.Fatalf("tracks: %+v", tracks)
	}
	trackID := tracks[0].ID
	albumID := tracks[0].AlbumID

	// Save album candidate
	if err = s.SaveAlbumSubjectCandidates(ctx, albumID, []AlbumSubjectCandidate{
		{ExternalID: "111", Title: "Album Cand", Score: 88},
	}); err != nil {
		t.Fatal(err)
	}

	// Save track candidate
	if err = s.SaveTrackSubjectCandidates(ctx, trackID, []TrackSubjectCandidate{
		{ExternalID: "222", Title: "Track Cand", MatchKind: "exact"},
	}); err != nil {
		t.Fatal(err)
	}

	// Check D-17: TrackArtist directly loaded in TrackSubjectCandidates
	cands, err := s.PendingTrackSubjectCandidates(ctx, 0, 10)
	if err != nil || len(cands) != 1 {
		t.Fatalf("PendingTrackSubjectCandidates: %+v %v", cands, err)
	}
	if cands[0].TrackArtist != "Primary Singer" {
		t.Fatalf("D-17 failure: TrackArtist=%q, want 'Primary Singer'", cands[0].TrackArtist)
	}
	if cands[0].TrackTitle != "Title Song" || cands[0].AlbumTitle != "Album B" {
		t.Fatalf("candidate mismatch: %+v", cands[0])
	}

	// Check review counts
	albumC, trackC, workC, seriesC, err := s.PendingWorkReviewCounts(ctx)
	if err != nil {
		t.Fatalf("PendingWorkReviewCounts: %v", err)
	}
	if albumC != 1 || trackC != 1 || workC != 0 || seriesC != 0 {
		t.Fatalf("counts: album=%d track=%d work=%d series=%d", albumC, trackC, workC, seriesC)
	}

	total, err := s.PendingWorkReviewTotal(ctx)
	if err != nil || total != 2 {
		t.Fatalf("total: %d %v", total, err)
	}

	albumCands, trackCands, err := s.PendingAlbumReviewCounts(ctx, albumID)
	if err != nil || albumCands != 1 || trackCands != 1 {
		t.Fatalf("album counts: %d %d %v", albumCands, trackCands, err)
	}
}

func TestListWorkOptions(t *testing.T) {
	s, ctx := setupBatch3Store(t)
	_, _ = s.CreateWork(ctx, WorkInput{Title: "魔法少女小圆", TranslatedTitle: "Madoka Magica", Type: "anime", Year: 2011})
	_, _ = s.CreateWork(ctx, WorkInput{Title: "魔法禁书目录", TranslatedTitle: "Index", Type: "anime", Year: 2008})
	_, _ = s.CreateWork(ctx, WorkInput{Title: "鬼灭之刃", Type: "anime", Year: 2019})

	all, err := s.ListWorkOptions(ctx, "", 20)
	if err != nil || len(all) != 3 {
		t.Fatalf("all: %+v %v", all, err)
	}

	filtered, err := s.ListWorkOptions(ctx, "魔法", 20)
	if err != nil || len(filtered) != 2 {
		t.Fatalf("filtered: %+v %v", filtered, err)
	}

	trans, err := s.ListWorkOptions(ctx, "Madoka", 20)
	if err != nil || len(trans) != 1 || trans[0].Title != "魔法少女小圆" {
		t.Fatalf("trans: %+v %v", trans, err)
	}
}

// L1：AlbumsForWork 的 TrackRoles 按用途优先级排序（GROUP_CONCAT 不保证顺序）。
func TestAlbumsForWorkTrackRolesOrdered(t *testing.T) {
	s, ctx := setupBatch3Store(t)
	lib, err := s.LibraryByRoot(ctx, "/music")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		err = s.ImportTrack(ctx, ImportInput{
			LibraryID:    lib.ID,
			RelativePath: "AlbumC/0" + string(rune('0'+i)) + ".flac",
			FileSize:     100,
			ModifiedAtNS: int64(i),
			Metadata: metadata.AudioMetadata{
				Title:        "C Track " + string(rune('0'+i)),
				Album:        "Album C",
				Artists:      []string{"Band C"},
				AlbumArtists: []string{"Band C"},
				DiscNumber:   1,
				TrackNumber:  i,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	albums, err := s.ListAlbums(ctx, Filters{Query: "Album C"})
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums: %+v %v", albums, err)
	}
	tracks, err := s.ListTracks(ctx, Filters{AlbumID: albums[0].ID})
	if err != nil || len(tracks) != 2 {
		t.Fatalf("tracks: %+v %v", tracks, err)
	}
	work, err := s.CreateWork(ctx, WorkInput{Title: "Ordered Work", Type: "anime"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddWorkAlbum(ctx, work.ID, albums[0].ID, "ost"); err != nil {
		t.Fatal(err)
	}
	// 先写 ed 再写 op：如果按写入顺序或 GROUP_CONCAT 默认顺序，op 会排在后面
	if err = s.AddWorkTrack(ctx, work.ID, WorkTrackInput{TrackID: tracks[0].ID, Role: "ed"}); err != nil {
		t.Fatal(err)
	}
	if err = s.AddWorkTrack(ctx, work.ID, WorkTrackInput{TrackID: tracks[1].ID, Role: "op"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.AlbumsForWork(ctx, work.ID)
	if err != nil || len(got) != 1 {
		t.Fatalf("AlbumsForWork: %v %+v", err, got)
	}
	if len(got[0].TrackRoles) != 2 || got[0].TrackRoles[0] != "op" || got[0].TrackRoles[1] != "ed" {
		t.Fatalf("TrackRoles = %v, want [op ed] (priority order)", got[0].TrackRoles)
	}
}
