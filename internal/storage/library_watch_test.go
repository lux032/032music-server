package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestLibraryWatchSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := Open(filepath.Join(t.TempDir(), "watch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.LibraryWatchSettings(ctx); err != nil || found {
		t.Fatalf("fresh database: found=%v err=%v, want no override", found, err)
	}
	for _, want := range []LibraryWatchSettings{{Enabled: true, Interval: 5 * time.Minute}, {Enabled: false, Interval: 30 * time.Second}} {
		if err := store.SaveLibraryWatchSettings(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, found, err := store.LibraryWatchSettings(ctx)
		if err != nil || !found || got != want {
			t.Fatalf("got %+v found=%v err=%v, want %+v", got, found, err, want)
		}
	}
	if err := store.SaveLibraryWatchSettings(ctx, LibraryWatchSettings{Enabled: true}); err == nil {
		t.Fatal("zero interval must be rejected")
	}
	if err := store.ResetLibraryWatchSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.LibraryWatchSettings(ctx); found {
		t.Fatal("reset must remove the override")
	}
}
