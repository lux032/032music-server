package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func seedSyntheticLibrary(t testing.TB, root string, albums, tracksPerAlbum int) {
	t.Helper()
	for a := 1; a <= albums; a++ {
		dir := filepath.Join(root, fmt.Sprintf("Album_%04d", a))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if a%2 == 0 {
			if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte(fmt.Sprintf("cover-bytes-%04d", a)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for n := 1; n <= tracksPerAlbum; n++ {
			audio := filepath.Join(dir, fmt.Sprintf("%02d.flac", n))
			if err := os.WriteFile(audio, []byte("fLaC"), 0o600); err != nil {
				t.Fatal(err)
			}
			if n == 1 && a%10 == 0 {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.lrc", n)), []byte("[00:01.00]lyric"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func syntheticMetadataReader(path string) (metadata.AudioMetadata, error) {
	album := filepath.Base(filepath.Dir(path))
	trackBase := strings.TrimSuffix(filepath.Base(path), ".flac")
	trackNum, _ := strconv.Atoi(trackBase)
	albumNum, _ := strconv.Atoi(strings.TrimPrefix(album, "Album_"))
	artist := fmt.Sprintf("Artist_%03d", (albumNum-1)%25+1)
	return metadata.AudioMetadata{
		Title:          fmt.Sprintf("%s Track %02d", album, trackNum),
		Album:          album,
		Artists:        []string{artist},
		AlbumArtists:   []string{artist},
		Composer:       fmt.Sprintf("Composer_%02d", (albumNum-1)%10+1),
		DiscNumber:     1,
		TrackNumber:    trackNum,
		Year:           2000 + albumNum%25,
		DurationMillis: 200000,
		Container:      "flac",
		MIMEType:       "audio/flac",
		Raw: map[string][]string{
			"TITLE":                      {fmt.Sprintf("%s Track %02d", album, trackNum)},
			"ALBUM":                      {album},
			"ARTIST":                     {artist},
			"MUSICBRAINZ_RELEASEGROUPID": {fmt.Sprintf("%08x-1111-4111-8111-111111111111", albumNum)},
		},
	}, nil
}

func TestScannerScaleFullAndZeroChangeIncremental(t *testing.T) {
	if testing.Short() {
		t.Skip("scale scanner test skipped in short mode")
	}
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "scale.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	const albums, tracksPerAlbum = 100, 10
	const totalTracks = albums * tracksPerAlbum
	seedSyntheticLibrary(t, root, albums, tracksPerAlbum)

	if err := store.EnsureLibrary(ctx, "Scale", root); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	m := New(ctx, store, logger(), library, t.TempDir())
	m.workers = 4
	m.SetMetadataReader(syntheticMetadataReader)
	m.probeAudio = func(string, string) metadata.AudioProps {
		return metadata.AudioProps{Codec: "flac", SampleRate: 44100, BitDepth: 16, Channels: 2, BitrateKbps: 900}
	}

	waitDone := func(jobID int64) storage.ScanJob {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			job, e := store.LatestScanJob(ctx)
			if e == nil && job.ID == jobID && (job.Status == "completed" || job.Status == "failed") {
				return job
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("scan job %d timed out", jobID)
		return storage.ScanJob{}
	}

	// 1. Initial full import (1,000 tracks across 100 albums, batched & concurrent).
	fullStart := time.Now()
	jobID, err := m.Start(ctx, "full")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	fullElapsed := time.Since(fullStart)
	fullJob := waitDone(jobID)
	if fullJob.Status != "completed" || fullJob.DiscoveredFiles != totalTracks || fullJob.ProcessedFiles != totalTracks || fullJob.FailedFiles != 0 {
		t.Fatalf("full scan: %+v", fullJob)
	}

	// 2. Zero-change incremental scan must execute zero audio_files writes.
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rawDB.Close() })
	if _, err := rawDB.ExecContext(ctx, `CREATE TRIGGER forbid_zero_change_af_write BEFORE UPDATE ON audio_files BEGIN SELECT RAISE(ABORT, 'audio_files updated on zero-change scan'); END`); err != nil {
		t.Fatal(err)
	}

	incrStart := time.Now()
	jobID2, err := m.Start(ctx, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	incrElapsed := time.Since(incrStart)
	incrJob := waitDone(jobID2)
	if incrJob.Status != "completed" || incrJob.DiscoveredFiles != totalTracks || incrJob.SkippedFiles != totalTracks || incrJob.ProcessedFiles != 0 || incrJob.FailedFiles != 0 || incrJob.MissingFiles != 0 {
		t.Fatalf("zero-change incremental scan: %+v", incrJob)
	}

	if _, err := rawDB.ExecContext(ctx, `DROP TRIGGER forbid_zero_change_af_write`); err != nil {
		t.Fatal(err)
	}

	// 3. Delta incremental scan: add 5 .lrc files, modify 2 audio files, remove 2 audio files.
	for a := 1; a <= 5; a++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("Album_%04d", a), "02.lrc"), []byte("[00:00]hello"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for a := 1; a <= 2; a++ {
		p := filepath.Join(root, fmt.Sprintf("Album_%04d", a), "03.flac")
		if err := os.WriteFile(p, []byte("fLaC-modified"), 0o600); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(2 * time.Second)
		_ = os.Chtimes(p, future, future)
	}
	for a := 1; a <= 2; a++ {
		if err := os.Remove(filepath.Join(root, fmt.Sprintf("Album_%04d", a), "04.flac")); err != nil {
			t.Fatal(err)
		}
	}

	deltaStart := time.Now()
	jobID3, err := m.Start(ctx, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	deltaElapsed := time.Since(deltaStart)
	deltaJob := waitDone(jobID3)
	if deltaJob.Status != "completed" || deltaJob.DiscoveredFiles != totalTracks-2 || deltaJob.ProcessedFiles != 2 || deltaJob.SkippedFiles != totalTracks-4 || deltaJob.MissingFiles != 2 || deltaJob.FailedFiles != 0 {
		t.Fatalf("delta incremental scan: %+v", deltaJob)
	}

	var lrcCount int
	if err := rawDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM audio_files WHERE status='available' AND has_external_lrc=1`).Scan(&lrcCount); err != nil {
		t.Fatal(err)
	}
	// Original 10 albums (a%10==0) + 5 newly added .lrc files = 15.
	if lrcCount != 15 {
		t.Fatalf("available has_external_lrc count = %d, want 15", lrcCount)
	}

	t.Logf("scanner scale (%d tracks, %d albums, 4 workers): full=%s zero-change-incremental=%s delta-incremental=%s",
		totalTracks, albums, fullElapsed, incrElapsed, deltaElapsed)
}
