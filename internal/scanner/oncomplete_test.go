package scanner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

func TestScanOnCompleteSeesScannerNotRunning(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = store.EnsureLibrary(context.Background(), "Test", root); err != nil {
		t.Fatal(err)
	}
	lib, err := store.LibraryByRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	m := New(context.Background(), store, logger(), lib, t.TempDir())
	runningInCallback := make(chan bool, 1)
	m.SetOnComplete(func() {
		runningInCallback <- m.Running()
	})
	if _, err = m.Start(context.Background(), "incremental"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	select {
	case wasRunning := <-runningInCallback:
		if wasRunning {
			t.Fatal("scanner.Running() was still true inside OnComplete callback")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnComplete callback was not invoked")
	}
}
