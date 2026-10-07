package scanner

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func testStore(t *testing.T) (*storage.Store, storage.Library) {
	t.Helper()
	ctx := context.Background()
	store, err := storage.Open(filepath.Join(t.TempDir(), "scanner.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, storage.Library{}
}

func logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func waitJob(t *testing.T, store *storage.Store, jobID int64) storage.ScanJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.LatestScanJob(context.Background())
		if err == nil && job.ID == jobID && (job.Status == "completed" || job.Status == "failed") {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scan job did not finish")
	return storage.ScanJob{}
}

// S1: a scan that discovers zero files while the library has available files
// must refuse reconciliation instead of wiping the library.
func TestScanRefusesEmptyDiscovery(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	libraryRoot := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", libraryRoot); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, libraryRoot)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the library with one available track, as a previous scan would.
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "T", Album: "A", Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}

	manager := New(ctx, store, logger(), library, t.TempDir())
	jobID, err := manager.Start(ctx, "full")
	if err != nil {
		t.Fatal(err)
	}
	manager.Wait()
	job := waitJob(t, store, jobID)
	if job.Status != "failed" {
		t.Fatalf("job status = %q, want failed (empty discovery must be refused)", job.Status)
	}
	available, err := store.CountAvailableAudioFiles(ctx, library.ID)
	if err != nil || available != 1 {
		t.Fatalf("available files = %d, err=%v; track must survive the refused scan", available, err)
	}
	stats, err := store.Statistics(ctx)
	if err != nil || stats.Tracks != 1 {
		t.Fatalf("tracks = %d, err=%v; CleanupOrphans must not delete tracks", stats.Tracks, err)
	}
}

// S1: even after files legitimately go missing, the track rows (and their
// client data) must be kept — only the file status changes.
func TestCleanupOrphansKeepsMissingTracks(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	libraryRoot := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", libraryRoot); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, libraryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportTrack(ctx, storage.ImportInput{LibraryID: library.ID, RelativePath: "Album/01.flac", FileSize: 1, ModifiedAtNS: 1, Metadata: metadata.AudioMetadata{Title: "T", Album: "A", Artists: []string{"X"}, AlbumArtists: []string{"X"}, DiscNumber: 1, TrackNumber: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTrackFavorite(ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkMissing(ctx, library.ID, storage.ScanTimestamp()); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	// The row and its favorite survive (lookup by id still works) ...
	track, err := store.TrackByID(ctx, 1)
	if err != nil || !track.IsFavorite {
		t.Fatalf("track=%+v err=%v; missing tracks and their client data must be kept", track, err)
	}
	// ... while browse statistics hide it until the file comes back.
	stats, err := store.Statistics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Tracks != 0 || stats.FavoriteTracks != 0 || stats.MissingFiles != 1 {
		t.Fatalf("stats=%+v; a track without available files must be hidden", stats)
	}
}

func TestUnchangedScanBackfillOverridesAndLRC(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "scan.db")
	store, e := storage.Open(dbPath)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	if e = store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if err := store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	library, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	audio := filepath.Join(root, "track.flac")
	data := make([]byte, 42)
	copy(data, "fLaC")
	data[4] = 0x80
	data[7] = 34
	binary.BigEndian.PutUint64(data[18:26], uint64(44100)<<44|uint64(1)<<41|uint64(15)<<36|44100)
	if err = os.WriteFile(audio, data, 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(audio)
	input := storage.ImportInput{LibraryID: library.ID, RelativePath: "track.flac", FileSize: info.Size(), ModifiedAtNS: info.ModTime().UnixNano(), Metadata: metadata.AudioMetadata{Title: "Original", Album: "Album", Artists: []string{"Singer"}, DiscNumber: 1, TrackNumber: 1, DurationMillis: 1000}}
	if err = store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	tracks, err := store.SyncTracks(ctx, storage.SyncTracksParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := tracks.Items[0].ID
	if err = store.UpdateTrack(ctx, id, "", 2, 9, "", "", nil); err != nil {
		t.Fatal(err)
	}
	// Create a legacy probe-version=0 record without changing the audio fingerprint.
	if err = setLegacyProbe(ctx, dbPath, input.RelativePath); err != nil {
		t.Fatal(err)
	}
	m := New(ctx, store, logger(), library, t.TempDir())
	reads, probes := 0, 0
	m.readMetadata = func(string) (metadata.AudioMetadata, error) {
		reads++
		return input.Metadata, nil
	}
	m.probeAudio = func(path, container string) metadata.AudioProps {
		probes++
		return metadata.ProbeAudio(path, container)
	}
	scan := func(kind string) storage.ScanJob {
		t.Helper()
		jobID, e := m.Start(ctx, kind)
		if e != nil {
			t.Fatal(e)
		}
		m.Wait()
		job := waitJob(t, store, jobID)
		if job.Status != "completed" {
			t.Fatalf("scan %s: %+v", kind, job)
		}
		return job
	}
	if job := scan("incremental"); job.ProcessedFiles != 0 || job.SkippedFiles != 1 || reads != 0 || probes != 1 {
		t.Fatalf("legacy scan: %+v reads=%d probes=%d", job, reads, probes)
	}
	needed, err := store.AudioProbeNeeded(ctx, library.ID, input.RelativePath)
	if err != nil || needed {
		t.Fatalf("probe version: %v %v", needed, err)
	}
	scan("incremental")
	if probes != 1 {
		t.Fatalf("re-probed on third scan: %d", probes)
	}
	lrc := filepath.Join(root, "track.lrc")
	if err = os.WriteFile(lrc, []byte("lyrics"), 0600); err != nil {
		t.Fatal(err)
	}
	scan("incremental")
	track, err := store.TrackByID(ctx, id)
	if err != nil || track.LyricsURL == "" {
		t.Fatalf("new lrc: %+v %v", track, err)
	}
	if err = os.Remove(lrc); err != nil {
		t.Fatal(err)
	}
	scan("incremental")
	track, err = store.TrackByID(ctx, id)
	if err != nil || track.LyricsURL != "" {
		t.Fatalf("removed lrc: %+v %v", track, err)
	}
	if job := scan("full"); job.ProcessedFiles != 1 || job.SkippedFiles != 0 {
		t.Fatalf("full scan must re-import: %+v", job)
	}
	track, err = store.TrackByID(ctx, id)
	if err != nil || track.DiscNumber != 2 || track.TrackNumber != 9 || track.Title != "Original" || reads != 1 || probes != 2 {
		t.Fatalf("full rescan override: %+v %v reads=%d probes=%d", track, err, reads, probes)
	}
	raw, _ := json.Marshal(track)
	var keys map[string]any
	if err = json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if keys["discNumber"] != float64(2) || keys["trackNumber"] != float64(9) {
		t.Fatalf("API JSON: %s", raw)
	}
	// Even a zero-value (failed) probe writes the version and is not retried.
	if err = setLegacyProbe(ctx, dbPath, input.RelativePath); err != nil {
		t.Fatal(err)
	}
	m.probeAudio = func(string, string) metadata.AudioProps { probes++; return metadata.AudioProps{} }
	scan("incremental")
	scan("incremental")
	if probes != 3 {
		t.Fatalf("failed probe retried: %d", probes)
	}
}

func setLegacyProbe(ctx context.Context, databasePath, relative string) error {
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `UPDATE audio_files SET audio_probe_version=0 WHERE relative_path=?`, relative)
	return err
}

func TestFailedAudioProbeStillTouchesFile(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "scan.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = store.EnsureLibrary(ctx, "Test", root); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "song.flac")
	if err = os.WriteFile(path, []byte("fLaC"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	input := storage.ImportInput{LibraryID: lib.ID, RelativePath: "song.flac", FileSize: info.Size(), ModifiedAtNS: info.ModTime().UnixNano(), Metadata: metadata.AudioMetadata{Title: "Song", Album: "Album", DiscNumber: 1, DurationMillis: 1000}}
	if err = store.ImportTrack(ctx, input); err != nil {
		t.Fatal(err)
	}
	if err = setLegacyProbe(ctx, dbPath, input.RelativePath); err != nil {
		t.Fatal(err)
	}
	m := New(ctx, store, logger(), lib, t.TempDir())
	m.updateAudioProbe = func(context.Context, int64, string, metadata.AudioProps, bool) error {
		return errors.New("injected probe update failure")
	}
	jobID, err := m.Start(ctx, "incremental")
	if err != nil {
		t.Fatal(err)
	}
	m.Wait()
	job := waitJob(t, store, jobID)
	if job.Status != "completed" || job.FailedFiles != 1 || job.SkippedFiles != 0 || job.ProcessedFiles != 0 || job.FailedFiles+job.SkippedFiles+job.ProcessedFiles > job.DiscoveredFiles {
		t.Fatalf("scan counters: %+v", job)
	}
	files, err := store.CountAvailableAudioFiles(ctx, lib.ID)
	if err != nil || files != 1 {
		t.Fatalf("failed probe hid file: available=%d err=%v", files, err)
	}
}

func TestExternalLRCRejectsEmptyAndBOMOnly(t *testing.T) {
	audio := filepath.Join(t.TempDir(), "track.flac")
	lrc := filepath.Join(filepath.Dir(audio), "track.lrc")
	for _, data := range [][]byte{nil, {0xef, 0xbb, 0xbf}, {0xff, 0xfe}, {0xfe, 0xff}} {
		if err := os.WriteFile(lrc, data, 0600); err != nil {
			t.Fatal(err)
		}
		if externalLRC(audio) {
			t.Fatalf("BOM-only LRC detected: %x", data)
		}
	}
	if err := os.WriteFile(lrc, []byte("[00:00]line"), 0600); err != nil {
		t.Fatal(err)
	}
	if !externalLRC(audio) {
		t.Fatal("nonempty LRC missing")
	}
}
