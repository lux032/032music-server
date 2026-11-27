package main

import (
	"io"
	"log/slog"
	"testing"
)

func TestFirstRunScanControllerPendingHoldAndRelease(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	watchApplied := 0
	var starts []bool
	ctrl := newFirstRunScanController(
		func() { watchApplied++ },
		func(startupScan bool) { starts = append(starts, startupScan) },
		logger,
	)

	// Pending startup must hold both watcher and startup scan.
	ctrl.InitAtStartup(true)
	if watchApplied != 0 || len(starts) != 0 {
		t.Fatalf("pending startup released prematurely: watchApplied=%d starts=%v", watchApplied, starts)
	}

	// Release with local-only first scan (startScan=true, autoEnrich=false).
	ctrl.Release(true, false)
	if watchApplied != 1 || len(starts) != 1 || !starts[0] {
		t.Fatalf("after first release: watchApplied=%d starts=%v", watchApplied, starts)
	}
	// First scan suppresses post-scan enrichment once; subsequent scans resume normally.
	if ctrl.ShouldRunPostScanEnrich() {
		t.Fatal("first scan should suppress post-scan enrichment when autoEnrich=false")
	}
	if !ctrl.ShouldRunPostScanEnrich() {
		t.Fatal("subsequent scan should resume normal post-scan enrichment")
	}

	// Second Release call must be a no-op (sync.Once idempotency).
	ctrl.Release(false, false)
	if watchApplied != 1 || len(starts) != 1 {
		t.Fatalf("duplicate release was not idempotent: watchApplied=%d starts=%v", watchApplied, starts)
	}
}

func TestFirstRunScanControllerCompletedBootAndSaveOnlyRelease(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Case 1: Completed/upgraded instance starts watcher with immediate startup scan.
	var completedStarts []bool
	ctrlCompleted := newFirstRunScanController(nil, func(s bool) { completedStarts = append(completedStarts, s) }, logger)
	ctrlCompleted.InitAtStartup(false)
	if len(completedStarts) != 1 || !completedStarts[0] {
		t.Fatalf("completed boot starts = %v, want [true]", completedStarts)
	}
	if !ctrlCompleted.ShouldRunPostScanEnrich() {
		t.Fatal("completed boot should run normal post-scan enrichment")
	}

	// Case 2: Pending instance completes with startScan=false (save only, scan later).
	var saveOnlyStarts []bool
	ctrlSaveOnly := newFirstRunScanController(nil, func(s bool) { saveOnlyStarts = append(saveOnlyStarts, s) }, logger)
	ctrlSaveOnly.InitAtStartup(true)
	ctrlSaveOnly.Release(false, false)
	if len(saveOnlyStarts) != 1 || saveOnlyStarts[0] {
		t.Fatalf("save-only starts = %v, want [false]", saveOnlyStarts)
	}
	// Because no scan was started, a later manual scan must NOT be suppressed.
	if !ctrlSaveOnly.ShouldRunPostScanEnrich() {
		t.Fatal("save-only release must not suppress a later manual scan")
	}
}
