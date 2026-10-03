package enrichment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lux032/032music-server/internal/storage"
)

type phaseItemSnapshot struct {
	Album storage.AlbumBangumiTarget
	Track storage.TrackBangumiTarget
	Work  storage.WorkEnrichmentTarget
}

func phaseStages(r RunRequest) []string {
	var stages []string
	if r.Scope == "all" || r.Scope == "albums" || r.Scope == "album" {
		stages = append(stages, "albums")
	}
	if r.Scope == "all" || r.Scope == "tracks" || r.Scope == "album" {
		stages = append(stages, "tracks")
	}
	if r.Scope == "all" || r.Scope == "works" || r.Scope == "work" || r.Scope == "album" {
		stages = append(stages, "works")
	}
	if r.Scope == "all" || r.Scope == "works" {
		stages = append(stages, "series")
	}
	return stages
}
func (m *Manager) collectDurablePhaseStage(ctx context.Context, r RunRequest, stage string) ([]storage.EnrichmentItemInput, error) {
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil {
		return nil, err
	}
	if !setting.Enabled {
		return nil, nil
	}
	var items []storage.EnrichmentItemInput
	add := func(id int64, v phaseItemSnapshot) error {
		raw, e := json.Marshal(v)
		if e != nil {
			return e
		}
		items = append(items, storage.EnrichmentItemInput{ObjectID: id, Parameters: raw})
		return nil
	}
	target := int64(0)
	if r.Scope == "album" || r.Scope == "work" {
		target = r.TargetID
	}
	switch stage {
	case "albums":
		albums, e := m.store.AlbumsForBangumiTieup(ctx, r.Force, target)
		if e != nil {
			return nil, e
		}
		for _, a := range albums {
			if e = add(a.ID, phaseItemSnapshot{Album: a}); e != nil {
				return nil, e
			}
		}
	case "tracks":
		album := int64(0)
		if r.Scope == "album" {
			album = target
		}
		tracks, e := m.store.TracksForBangumiTieup(ctx, r.Force, album)
		if e != nil {
			return nil, e
		}
		for _, t := range tracks {
			if e = add(t.ID, phaseItemSnapshot{Track: t}); e != nil {
				return nil, e
			}
		}
	case "works":
		work := int64(0)
		if r.Scope == "work" {
			work = target
		}
		works, e := m.store.WorksForEnrichment(ctx, "bangumi", r.Force, 0, work)
		if e != nil {
			return nil, e
		}
		if r.Scope == "album" {
			works = worksLinkedToAlbum(ctx, m.store, target, works)
		}
		for _, w := range works {
			if e = add(w.ID, phaseItemSnapshot{Work: w}); e != nil {
				return nil, e
			}
		}
	case "series":
		items = append(items, storage.EnrichmentItemInput{ObjectID: 0, Parameters: json.RawMessage(`{}`)})
	}
	return items, nil
}
func (m *Manager) phaseRuntimeFailure(runID, epoch int64, err error) {
	// Shutdown and user pause/cancel cancel the worker ctx; those are expected
	// stops, not storage or runtime failures.
	if m.baseCtx.Err() != nil {
		m.phasePauseShutdown(runID, epoch)
		return
	}
	if errors.Is(err, context.Canceled) {
		m.logger.Debug("durable enrichment worker stopped", "runId", runID, "error", err)
		return
	}
	m.logger.Error("durable enrichment failure", "runId", runID, "error", err)
	if e := m.store.TransitionEnrichmentWorker(context.Background(), runID, epoch, "pause", "storage_or_runtime_error"); e != nil && !errors.Is(e, storage.ErrEnrichmentRunState) {
		m.logger.Error("pause enrichment failure", "error", e)
	}
}

// phasePauseShutdown pauses for process shutdown without an error log: it is
// an expected stop, not a storage or runtime failure.
func (m *Manager) phasePauseShutdown(runID, epoch int64) {
	if err := m.store.TransitionEnrichmentWorker(context.Background(), runID, epoch, "pause", "shutdown"); err != nil && !errors.Is(err, storage.ErrEnrichmentRunState) {
		m.logger.Error("pause shutdown enrichment", "error", err)
	}
}

// phaseItemCurrent is the per-item progress label; the series stage is a
// single long item, so it gets an explicit stage title.
func phaseItemCurrent(stage string, snapshot phaseItemSnapshot) string {
	switch stage {
	case "albums":
		return snapshot.Album.Title
	case "tracks":
		if snapshot.Track.AlbumTitle != "" {
			return snapshot.Track.Title + " — " + snapshot.Track.AlbumTitle
		}
		return snapshot.Track.Title
	case "works":
		return snapshot.Work.Title
	case "series":
		return "作品系列归组"
	}
	return ""
}
func (m *Manager) launchDurablePhaseLocked(run storage.DurableEnrichmentRun) {
	ctx, cancel := context.WithCancel(m.baseCtx)
	done := make(chan struct{})
	m.phaseRunning = true
	m.phaseRunID = run.ID
	m.phaseCancel = cancel
	m.phaseDone = done
	m.goBackground("durable-enrichment", func() {
		defer func() {
			if value := recover(); value != nil {
				m.phaseRuntimeFailure(run.ID, run.Epoch, fmt.Errorf("panic: %v", value))
			}
			cancel()
			m.phaseMu.Lock()
			m.phaseRunning = false
			m.phaseRunID = 0
			m.phaseCancel = nil
			m.phaseMu.Unlock()
			close(done)
		}()
		m.executeDurablePhase(ctx, run)
	})
}
func (m *Manager) executeDurablePhase(ctx context.Context, run storage.DurableEnrichmentRun) {
	request := RunRequest{Scope: run.Scope, TargetID: run.TargetID, Force: run.Force}
	failures := 0
	lastFailure := ""
	for _, stage := range phaseStages(request) {
		if ctx.Err() != nil {
			m.phaseShutdown(run)
			return
		}
		prepared, err := m.store.EnrichmentStagePrepared(ctx, run.ID, stage)
		if err != nil {
			m.phaseRuntimeFailure(run.ID, run.Epoch, err)
			return
		}
		if !prepared {
			if err = m.store.SetEnrichmentCurrent(ctx, run.ID, run.Epoch, stage, "正在统计"+phase4StageLabel(stage)+"阶段…"); err != nil {
				m.phaseRuntimeFailure(run.ID, run.Epoch, err)
				return
			}
			if m.testStageCollectHook != nil {
				m.testStageCollectHook(stage)
			}
			items, e := m.collectDurablePhaseStage(ctx, request, stage)
			if e != nil {
				m.phaseRuntimeFailure(run.ID, run.Epoch, e)
				return
			}
			if e = m.store.PrepareEnrichmentStage(ctx, run.ID, run.Epoch, stage, items); e != nil {
				m.phaseRuntimeFailure(run.ID, run.Epoch, e)
				return
			}
		}
		for {
			if ctx.Err() != nil {
				m.phaseShutdown(run)
				return
			}
			item, e := m.store.ClaimEnrichmentItem(ctx, run.ID, run.Epoch, stage)
			if errors.Is(e, sql.ErrNoRows) {
				break
			}
			if e != nil {
				m.phaseRuntimeFailure(run.ID, run.Epoch, e)
				return
			}
			c := storage.EnrichmentCheckpoint{RunID: run.ID, ItemID: item.ID, Epoch: run.Epoch, Token: item.Token}
			itemCtx := storage.WithEnrichmentCheckpoint(ctx, c)
			var snapshot phaseItemSnapshot
			if e = json.Unmarshal(item.Parameters, &snapshot); e != nil {
				m.phaseRuntimeFailure(run.ID, run.Epoch, e)
				return
			}
			if label := phaseItemCurrent(stage, snapshot); label != "" {
				if e = m.store.SetEnrichmentCurrent(ctx, run.ID, run.Epoch, stage, label); e != nil {
					if errors.Is(e, storage.ErrEnrichmentRunState) {
						return
					}
					m.phaseRuntimeFailure(run.ID, run.Epoch, e)
					return
				}
			}
			for {
				if ctx.Err() != nil {
					m.phaseShutdown(run)
					return
				}
				current, e := m.store.DurableEnrichmentRun(ctx, run.ID)
				if e != nil {
					m.phaseRuntimeFailure(run.ID, run.Epoch, e)
					return
				}
				if current.WaitingUntil != "" {
					deadline, e := time.Parse(time.RFC3339Nano, current.WaitingUntil)
					if e != nil {
						m.phaseRuntimeFailure(run.ID, run.Epoch, e)
						return
					}
					m.blockSourceUntil(current.WaitSource, deadline)
					if e = m.waitEnrichmentSource(ctx, c, current.WaitSource, deadline); e != nil {
						return
					}
				}
				outcome, e := m.store.EnrichmentEffect(ctx, c, "final")
				if errors.Is(e, sql.ErrNoRows) {
					switch stage {
					case "albums":
						outcome, e = m.enrichBangumiAlbum(itemCtx, run.ID, snapshot.Album, run.Force)
					case "tracks":
						outcome, e = m.enrichBangumiTrack(itemCtx, run.ID, snapshot.Track, run.Force)
					case "works":
						outcome, e = m.enrichBangumiWork(itemCtx, run.ID, snapshot.Work, run.Force)
					case "series":
						outcome, e = m.enrichBangumiSeries(itemCtx, run.ID, run.Force)
					}
				}
				if ctx.Err() != nil {
					m.phaseShutdown(run)
					return
				}
				if rate := asRateLimited(e); rate != nil {
					deadline := m.clockNow().Add(rate.RetryAfter)
					if shared := m.sourceBlockedUntil(rate.Source); shared.After(deadline) {
						deadline = shared
					}
					if e = m.store.RecordEnrichmentWait(ctx, c, rate.Source, deadline, 0, !rate.CooldownOnly); e != nil {
						if !errors.Is(e, storage.ErrEnrichmentWaitBudget) {
							m.phaseRuntimeFailure(run.ID, run.Epoch, e)
						}
						return
					}
					if e = m.waitEnrichmentSource(ctx, c, rate.Source, deadline); e != nil {
						return
					}
					continue
				}
				if e != nil {
					if isArtistPersistenceError(e) || errors.Is(e, storage.ErrEnrichmentRunState) {
						m.phaseRuntimeFailure(run.ID, run.Epoch, e)
						return
					}
					outcome = "failed"
					failures++
					lastFailure = e.Error()
					m.logger.Warn("enrichment item failed", "error", e)
				} else {
					failures = 0
					if outcome != "review" && outcome != "skipped" {
						outcome = "matched"
					}
				}
				if e = m.store.CompleteEnrichmentItem(ctx, c, outcome); e != nil {
					m.phaseRuntimeFailure(run.ID, run.Epoch, e)
					return
				}
				if failures >= maxConsecutiveFailures {
					if e = m.store.TransitionEnrichmentWorker(ctx, run.ID, run.Epoch, "fail", "连续五次来源失败，任务已结束"); e != nil {
						m.phaseRuntimeFailure(run.ID, run.Epoch, e)
					}
					return
				}
				break
			}
		}
	}
	// A completed run keeps the last ordinary failure visible (legacy parity):
	// users can tell partial failures apart from a clean pass after recovery.
	if err := m.store.TransitionEnrichmentWorker(ctx, run.ID, run.Epoch, "complete", lastFailure); err != nil {
		m.phaseRuntimeFailure(run.ID, run.Epoch, err)
	}
}
func (m *Manager) phaseShutdown(run storage.DurableEnrichmentRun) {
	if m.baseCtx.Err() != nil {
		if err := m.store.TransitionEnrichmentWorker(context.Background(), run.ID, run.Epoch, "pause", "shutdown"); err != nil && !errors.Is(err, storage.ErrEnrichmentRunState) {
			m.logger.Error("pause shutdown enrichment", "error", err)
		}
	}
}
func (m *Manager) waitEnrichmentSource(ctx context.Context, c storage.EnrichmentCheckpoint, source string, deadline time.Time) error {
	for {
		if shared := m.sourceBlockedUntil(source); shared.After(deadline) {
			deadline = shared
		}
		if !deadline.After(m.clockNow()) {
			return nil
		}
		run, err := m.store.DurableEnrichmentRun(ctx, c.RunID)
		if err != nil {
			m.phaseRuntimeFailure(c.RunID, c.Epoch, err)
			return err
		}
		budget := 30*time.Minute - time.Duration(run.WaitTotalMS-run.BudgetBaselineMS)*time.Millisecond
		wait := deadline.Sub(m.clockNow())
		if wait > budget {
			wait = budget
		}
		if wait < 0 {
			wait = 0
		}
		started := m.clockNow()
		sleepErr := m.sleep(ctx, wait)
		elapsed := m.clockNow().Sub(started)
		if elapsed < 0 {
			elapsed = 0
		}
		if err = m.store.RecordEnrichmentWait(context.Background(), c, source, deadline, elapsed, false); err != nil {
			if !errors.Is(err, storage.ErrEnrichmentWaitBudget) && !errors.Is(err, storage.ErrEnrichmentRunState) {
				m.phaseRuntimeFailure(c.RunID, c.Epoch, err)
			}
			return err
		}
		if sleepErr != nil {
			if m.baseCtx.Err() != nil {
				m.phasePauseShutdown(c.RunID, c.Epoch)
			}
			return sleepErr
		}
	}
}
