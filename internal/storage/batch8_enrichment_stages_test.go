package storage

import (
	"context"
	"path/filepath"
	"testing"
)

// 批次 8（migration 032）：enrichment_runs 的分阶段进度列。

func TestEnrichmentRunStageColumns(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	run, err := store.CreateEnrichmentRun(ctx, "all", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	// 迁移默认值：未统计为 -1，stage 为空。
	if run.Stage != "" || run.StageAlbums != -1 || run.StageTracks != -1 || run.StageWorks != -1 {
		t.Fatalf("new run stage fields = %q %d %d %d, want empty/-1/-1/-1", run.Stage, run.StageAlbums, run.StageTracks, run.StageWorks)
	}

	err = store.UpdateEnrichmentRun(ctx, run.ID, EnrichmentRunUpdate{
		Total: 3, Stage: "tracks", Current: "正在统计曲目阶段…",
		StageAlbums: 1, StageTracks: -1, StageWorks: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = store.EnrichmentRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Stage != "tracks" || run.Current != "正在统计曲目阶段…" || run.StageAlbums != 1 || run.StageTracks != -1 || run.StageWorks != -1 {
		t.Fatalf("run=%+v", run)
	}

	// -1 是合法哨兵；小于 -1 拒绝。
	err = store.UpdateEnrichmentRun(ctx, run.ID, EnrichmentRunUpdate{StageTracks: -2, StageAlbums: -1, StageWorks: -1})
	if err == nil {
		t.Fatal("stage counter below -1 must be rejected")
	}

	// 列表读路径同样带出新列。
	runs, err := store.ListEnrichmentRuns(ctx, 10, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v %v", runs, err)
	}
	if runs[0].Stage != "tracks" || runs[0].StageAlbums != 1 {
		t.Fatalf("listed run=%+v", runs[0])
	}
}
