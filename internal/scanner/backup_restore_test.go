package scanner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreFirstScanNeverPurges(t *testing.T) {
	s, library := testStore(t)
	m := New(context.Background(), s, logger(), library, t.TempDir())
	m.SetPurgeMissingPolicy(func(context.Context) string { return "always" })
	marker := filepath.Join(t.TempDir(), "restore-scan.json")
	if e := os.WriteFile(marker, []byte("true"), 0600); e != nil {
		t.Fatal(e)
	}
	m.ProtectRestoreScan(marker)
	if m.shouldPurge(context.Background(), "incremental") {
		t.Fatal("first scan purged")
	}
	if !m.shouldPurge(context.Background(), "incremental") {
		t.Fatal("subsequent scan did not restore policy")
	}
}
