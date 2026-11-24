package enrichment

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func TestCachedWorkPosterMemoryIndexAndInvalidation(t *testing.T) {
	store := batch8Store(t)
	ctx := context.Background()
	urlA := "https://example.com/poster-a.jpg"
	urlB := "https://example.com/poster-b.png"
	urlC := "https://example.com/poster-c.webp"

	if _, err := store.CreateWork(ctx, storage.WorkInput{Title: "Work A", Type: "anime", PosterURL: urlA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWork(ctx, storage.WorkInput{Title: "Work B", Type: "anime", PosterURL: urlB}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateWork(ctx, storage.WorkInput{Title: "Work C", Type: "anime", PosterURL: urlC}); err != nil {
		t.Fatal(err)
	}

	m := batch8Manager(t, store)
	dir := m.workPosterDirectory()

	// Empty / non-existent directory reports ErrNotExist and loads empty index.
	if _, _, err := m.CachedWorkPoster(urlA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing dir err = %v, want ErrNotExist", err)
	}

	// Write poster A externally and verify miss refreshes index via directory mtime.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	keyA := fmt.Sprintf("%x", sha256.Sum256([]byte(urlA)))
	pathA := filepath.Join(dir, keyA+".jpg")
	if err := os.WriteFile(pathA, []byte("jpeg-bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Non-poster temp file must be ignored by ReadDir indexing.
	if err := os.WriteFile(filepath.Join(dir, keyA+".tmp"), []byte("tmp"), 0o640); err != nil {
		t.Fatal(err)
	}

	gotPath, gotMIME, err := m.CachedWorkPoster(urlA)
	if err != nil || gotPath != pathA || gotMIME != "image/jpeg" {
		t.Fatalf("first lookup A: path=%q mime=%q err=%v", gotPath, gotMIME, err)
	}

	// Memory hit serves directly from index even before invalidation.
	if err := os.Remove(pathA); err != nil {
		t.Fatal(err)
	}
	if gotPath, gotMIME, err := m.CachedWorkPoster(urlA); err != nil || gotPath != pathA || gotMIME != "image/jpeg" {
		t.Fatalf("expected in-memory cache hit before invalidation, got path=%q mime=%q err=%v", gotPath, gotMIME, err)
	}

	// Simulate ENOENT invalidation on open failure; subsequent lookup misses cleanly.
	m.InvalidateWorkPoster(urlA)
	// Ensure directory mtime advances cleanly for external change detection.
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(dir, future, future)
	if _, _, err := m.CachedWorkPoster(urlA); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after invalidation err = %v, want ErrNotExist", err)
	}

	// Record a newly written poster via recordCachedWorkPoster (the CacheWorkPoster write path).
	keyB := fmt.Sprintf("%x", sha256.Sum256([]byte(urlB)))
	pathB := filepath.Join(dir, keyB+".png")
	if err := os.WriteFile(pathB, []byte("png-bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	m.recordCachedWorkPoster(keyB, ".png", "image/png")
	if gotPath, gotMIME, err := m.CachedWorkPoster(urlB); err != nil || gotPath != pathB || gotMIME != "image/png" {
		t.Fatalf("recorded write B: path=%q mime=%q err=%v", gotPath, gotMIME, err)
	}

	// External addition of C with updated directory mtime is picked up by WorkPosterCacheStats and CachedWorkPoster.
	keyC := fmt.Sprintf("%x", sha256.Sum256([]byte(urlC)))
	pathC := filepath.Join(dir, keyC+".webp")
	if err := os.WriteFile(pathC, []byte("webp-bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	later := future.Add(2 * time.Second)
	_ = os.Chtimes(dir, later, later)

	cached, total, err := m.WorkPosterCacheStats(ctx)
	if err != nil || cached != 2 || total != 3 {
		t.Fatalf("stats cached=%d total=%d err=%v, want 2/3", cached, total, err)
	}
	if gotPath, gotMIME, err := m.CachedWorkPoster(urlC); err != nil || gotPath != pathC || gotMIME != "image/webp" {
		t.Fatalf("external add C: path=%q mime=%q err=%v", gotPath, gotMIME, err)
	}

	// Concurrent reads, stats, writes, and invalidations must remain race-free.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_, _, _ = m.CachedWorkPoster(urlB)
				_, _, _ = m.CachedWorkPoster(urlA)
				if idx%2 == 0 {
					_, _, _ = m.WorkPosterCacheStats(ctx)
				} else {
					m.recordCachedWorkPoster(keyB, ".png", "image/png")
					m.InvalidateWorkPoster("https://example.com/nonexistent.jpg")
				}
			}
		}(i)
	}
	wg.Wait()
}
