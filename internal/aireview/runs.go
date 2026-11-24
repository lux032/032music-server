package aireview

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/internal/tagging"
)

var (
	ErrRunAlreadyActive = errors.New("已有 AI 预审任务正在运行或暂停中")
	ErrRunNotFound      = errors.New("未找到指定的 AI 预审任务")
	ErrNoPendingItems   = errors.New("当前没有需要 AI 预审的待处理项")
)

var allRunScopes = []string{
	"works_all", "albums", "tracks", "works", "series",
	"artists_all", "artist_matches", "credit_splits",
}

type runTask struct {
	kind  string
	id    int64
	label string
}

type activeRunState struct {
	runID  int64
	cancel context.CancelFunc
	paused bool
}

func (s *Service) StartRun(ctx context.Context, scope string, force bool) (storage.AIReviewRun, error) {
	ai, err := s.store.AISettings(ctx)
	if err != nil {
		return storage.AIReviewRun{}, err
	}
	if !ai.Configured() {
		return storage.AIReviewRun{}, errors.New("请先在 AI 设置中配置模型服务")
	}
	rev, err := s.store.AIReviewSettings(ctx)
	if err != nil {
		return storage.AIReviewRun{}, err
	}
	if !rev.Enabled {
		return storage.AIReviewRun{}, errors.New("请先在 AI 设置中开启 AI 初审")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRun != nil {
		return storage.AIReviewRun{}, ErrRunAlreadyActive
	}
	if _, ok, err := s.store.ActiveAIReviewRun(ctx, allRunScopes); err != nil {
		return storage.AIReviewRun{}, err
	} else if ok {
		return storage.AIReviewRun{}, ErrRunAlreadyActive
	}

	tasks, err := s.collectTasks(ctx, scope, rev)
	if err != nil {
		return storage.AIReviewRun{}, err
	}
	if len(tasks) == 0 {
		return storage.AIReviewRun{}, ErrNoPendingItems
	}
	run, err := s.store.CreateAIReviewRun(ctx, scope, force, len(tasks))
	if err != nil {
		return storage.AIReviewRun{}, err
	}
	s.launchRunLocked(run, tasks, ai.Concurrency)
	return run, nil
}

func (s *Service) PauseRun(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRun == nil || s.activeRun.runID != id {
		return ErrRunNotFound
	}
	s.activeRun.paused = true
	if s.activeRun.cancel != nil {
		s.activeRun.cancel()
	}
	return nil
}

func (s *Service) CancelRun(ctx context.Context, id int64) error {
	s.mu.Lock()
	if s.activeRun != nil && s.activeRun.runID == id {
		s.activeRun.paused = false
		if s.activeRun.cancel != nil {
			s.activeRun.cancel()
		}
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	run, err := s.store.AIReviewRunByID(ctx, id)
	if err != nil {
		return err
	}
	if run.Status == "paused" || run.Status == "running" {
		return s.store.FinishAIReviewRun(ctx, id, "cancelled", "已停止")
	}
	return nil
}

func (s *Service) ResumeRun(ctx context.Context, id int64) error {
	run, err := s.store.AIReviewRunByID(ctx, id)
	if err != nil {
		return err
	}
	if run.Status != "paused" {
		return errors.New("当前任务不在暂停状态")
	}
	ai, err := s.store.AISettings(ctx)
	if err != nil {
		return err
	}
	if !ai.Configured() {
		return errors.New("请先在 AI 设置中配置模型服务")
	}
	rev, err := s.store.AIReviewSettings(ctx)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeRun != nil {
		return ErrRunAlreadyActive
	}
	tasks, err := s.collectTasks(ctx, run.Scope, rev)
	if err != nil {
		return err
	}
	run.Total = len(tasks)
	run.Processed = 0
	run.AutoApplied = 0
	run.Recommended = 0
	run.Uncertain = 0
	run.Skipped = 0
	run.Failed = 0
	if _, err = s.store.ResumeAIReviewRun(ctx, run.ID, len(tasks)); err != nil {
		return err
	}
	s.launchRunLocked(run, tasks, ai.Concurrency)
	return nil
}

func (s *Service) collectTasks(ctx context.Context, scope string, rev storage.AIReviewSettings) ([]runTask, error) {
	var tasks []runTask
	addKind := func(kind string, loader func(context.Context) ([]int64, error), labelPrefix string) error {
		if rev.ModeForKind(kind) == storage.AIReviewModeOff {
			return nil
		}
		ids, err := loader(ctx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			tasks = append(tasks, runTask{kind: kind, id: id, label: fmt.Sprintf("%s #%d", labelPrefix, id)})
		}
		return nil
	}

	switch scope {
	case "works_all":
		if err := addKind(storage.AITargetAlbumSubject, s.store.PendingAlbumReviewTargetIDs, "专辑"); err != nil {
			return nil, err
		}
		if err := addKind(storage.AITargetTrackSubject, s.store.PendingTrackReviewTargetIDs, "曲目"); err != nil {
			return nil, err
		}
		if err := addKind(storage.AITargetWorkMatch, s.store.PendingWorkReviewTargetIDs, "作品"); err != nil {
			return nil, err
		}
		if err := addKind(storage.AITargetSeriesSuggestion, s.store.PendingSeriesSuggestionIDs, "系列建议"); err != nil {
			return nil, err
		}
	case "albums":
		if err := addKind(storage.AITargetAlbumSubject, s.store.PendingAlbumReviewTargetIDs, "专辑"); err != nil {
			return nil, err
		}
	case "tracks":
		if err := addKind(storage.AITargetTrackSubject, s.store.PendingTrackReviewTargetIDs, "曲目"); err != nil {
			return nil, err
		}
	case "works":
		if err := addKind(storage.AITargetWorkMatch, s.store.PendingWorkReviewTargetIDs, "作品"); err != nil {
			return nil, err
		}
	case "series":
		if err := addKind(storage.AITargetSeriesSuggestion, s.store.PendingSeriesSuggestionIDs, "系列建议"); err != nil {
			return nil, err
		}
	case "artists_all":
		if err := addKind(storage.AITargetCreditSplit, s.store.PendingCreditSplitArtistIDs, "复合署名"); err != nil {
			return nil, err
		}
		if err := addKind(storage.AITargetArtistMatch, s.store.PendingArtistReviewTargetIDs, "艺术家"); err != nil {
			return nil, err
		}
	case "artist_matches":
		if err := addKind(storage.AITargetArtistMatch, s.store.PendingArtistReviewTargetIDs, "艺术家"); err != nil {
			return nil, err
		}
	case "credit_splits":
		if err := addKind(storage.AITargetCreditSplit, s.store.PendingCreditSplitArtistIDs, "复合署名"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("未知的 AI 预审范围 %q", scope)
	}
	return tasks, nil
}

func (s *Service) launchRunLocked(run storage.AIReviewRun, tasks []runTask, concurrency int) {
	ctx, cancel := context.WithCancel(context.Background())
	state := &activeRunState{runID: run.ID, cancel: cancel}
	s.activeRun = state
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > 8 {
		concurrency = 8
	}

	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			if s.activeRun == state {
				s.activeRun = nil
			}
			s.mu.Unlock()
		}()

		if len(tasks) == 0 {
			_ = s.store.FinishAIReviewRun(context.Background(), run.ID, "completed", "")
			return
		}

		var progressMu sync.Mutex
		var fatalErr string
		taskCh := make(chan runTask)
		var wg sync.WaitGroup

		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for t := range taskCh {
					if ctx.Err() != nil {
						return
					}
					res, err := s.EvaluateTarget(ctx, t.kind, t.id, run.Force)
					if ctx.Err() != nil {
						return
					}
					progressMu.Lock()
					run.Processed++
					run.CurrentLabel = t.label
					if err != nil {
						run.Failed++
						var le *tagging.LLMError
						if errors.As(err, &le) && (le.Kind == tagging.KindAuth || le.Kind == tagging.KindModel || le.Kind == tagging.KindQuota) {
							fatalErr = le.Error()
							cancel()
						}
					} else if res.Skipped {
						run.Skipped++
					} else if res.Applied {
						run.AutoApplied++
					} else if res.Action == storage.AIActionUncertain {
						run.Uncertain++
					} else {
						run.Recommended++
					}
					_ = s.store.UpdateAIReviewRun(context.Background(), run)
					progressMu.Unlock()
				}
			}()
		}

		for _, t := range tasks {
			select {
			case <-ctx.Done():
				goto doneDispatch
			case taskCh <- t:
			}
		}
	doneDispatch:
		close(taskCh)
		wg.Wait()

		s.mu.Lock()
		paused := state.paused
		s.mu.Unlock()

		switch {
		case fatalErr != "":
			_ = s.store.FinishAIReviewRun(context.Background(), run.ID, "failed", fatalErr)
		case paused:
			_ = s.store.PauseAIReviewRun(context.Background(), run.ID)
		case ctx.Err() != nil:
			_ = s.store.FinishAIReviewRun(context.Background(), run.ID, "cancelled", "已停止")
		default:
			_ = s.store.FinishAIReviewRun(context.Background(), run.ID, "completed", "")
		}
	}()
}
