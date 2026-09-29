-- 批次 8：增强任务分阶段进度（专辑 → 曲目 → 作品 → 系列）。
-- stage 记录正在处理的阶段；stage_* 记录各阶段收集到的条目数，
-- -1 表示该阶段尚未统计或不属于本轮范围（旧行安全：全部默认 -1/''）。
ALTER TABLE enrichment_runs ADD COLUMN stage TEXT NOT NULL DEFAULT '';
ALTER TABLE enrichment_runs ADD COLUMN stage_albums INTEGER NOT NULL DEFAULT -1;
ALTER TABLE enrichment_runs ADD COLUMN stage_tracks INTEGER NOT NULL DEFAULT -1;
ALTER TABLE enrichment_runs ADD COLUMN stage_works INTEGER NOT NULL DEFAULT -1;
