package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"
)

const artistImageSearchQueuePace = 250 * time.Millisecond

// ArtistImageSearchOutcome captures the terminal result of a single candidate
// processed by the manual avatar search queue.
type ArtistImageSearchOutcome struct {
	CandidateID    int64  `json:"candidateId"`
	ArtistID       int64  `json:"artistId"`
	ArtistName     string `json:"artistName"`
	Outcome        string `json:"outcome"` // "found" | "not_found" | "failed" | "skipped"
	NewCandidateID int64  `json:"newCandidateId,omitempty"`
	Note           string `json:"note,omitempty"`
	SearchedAt     string `json:"searchedAt,omitempty"`
}

// ArtistImageSearchQueueSnapshot is a JSON-safe snapshot of the independent
// in-process manual avatar search queue rendered on /admin/matches?tab=avatars.
type ArtistImageSearchQueueSnapshot struct {
	Status           string                     `json:"status"` // "idle" | "running" | "completed" | "stopped"
	Total            int                        `json:"total"`
	Completed        int                        `json:"completed"`
	Waiting          int                        `json:"waiting"`
	Found            int                        `json:"found"`
	NotFound         int                        `json:"notFound"`
	Failed           int                        `json:"failed"`
	CurrentID        int64                      `json:"currentId"`
	CurrentArtist    string                     `json:"currentArtist"`
	QueuedIDs        []int64                    `json:"queuedIds"`
	QueuedPositions  map[int64]int              `json:"queuedPositions"`
	CompletedResults []ArtistImageSearchOutcome `json:"completedResults,omitempty"`
	UpdatedAt        string                     `json:"updatedAt,omitempty"`
}

type queuedAuditSearchItem struct {
	candidateID int64
	artistID    int64
	artistName  string
}

type artistImageSearchQueue struct {
	mu               sync.Mutex
	status           string
	running          bool
	workerGen        uint64
	stopAfterCurrent bool
	pending          []queuedAuditSearchItem
	current          *queuedAuditSearchItem
	total            int
	completed        int
	found            int
	notFound         int
	failed           int
	completedResults []ArtistImageSearchOutcome
	updatedAt        string
	testItemHook     func(candidateID int64)
}

func newArtistImageSearchQueue() *artistImageSearchQueue {
	return &artistImageSearchQueue{status: "idle"}
}

func (q *artistImageSearchQueue) snapshotLocked() ArtistImageSearchQueueSnapshot {
	snap := ArtistImageSearchQueueSnapshot{
		Status:          q.status,
		Total:           q.total,
		Completed:       q.completed,
		Waiting:         len(q.pending),
		Found:           q.found,
		NotFound:        q.notFound,
		Failed:          q.failed,
		QueuedIDs:       make([]int64, 0, len(q.pending)),
		QueuedPositions: make(map[int64]int, len(q.pending)+1),
		UpdatedAt:       q.updatedAt,
	}
	pos := 1
	if q.current != nil {
		snap.CurrentID = q.current.candidateID
		snap.CurrentArtist = q.current.artistName
		snap.QueuedPositions[q.current.candidateID] = pos
		pos++
	}
	for _, item := range q.pending {
		snap.QueuedIDs = append(snap.QueuedIDs, item.candidateID)
		snap.QueuedPositions[item.candidateID] = pos
		pos++
	}
	if len(q.completedResults) > 0 {
		snap.CompletedResults = make([]ArtistImageSearchOutcome, len(q.completedResults))
		copy(snap.CompletedResults, q.completedResults)
	}
	return snap
}

func (m *Manager) ensureImageSearchQueue() *artistImageSearchQueue {
	if m.imageSearchQueue == nil {
		m.imageSearchQueue = newArtistImageSearchQueue()
	}
	return m.imageSearchQueue
}

// ArtistImageSearchQueueSnapshot returns the live state of the independent
// manual web-search queue for pending avatar review cards.
func (m *Manager) ArtistImageSearchQueueSnapshot() ArtistImageSearchQueueSnapshot {
	q := m.ensureImageSearchQueue()
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.snapshotLocked()
}

// EnqueueArtistImageCandidateSearches validates the requested pending
// audit_review candidate IDs, deduplicates them against any already queued or
// running entries, appends them to the independent serial search queue, and
// starts the background worker if idle.
func (m *Manager) EnqueueArtistImageCandidateSearches(ctx context.Context, candidateIDs []int64) (int, ArtistImageSearchQueueSnapshot, error) {
	if len(candidateIDs) == 0 {
		return 0, m.ArtistImageSearchQueueSnapshot(), nil
	}
	seenInput := make(map[int64]bool, len(candidateIDs))
	validItems := make([]queuedAuditSearchItem, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		if id <= 0 || seenInput[id] {
			continue
		}
		seenInput[id] = true
		cand, err := m.store.ArtistImageCandidateByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, m.ArtistImageSearchQueueSnapshot(), err
		}
		if cand.Status != "pending" || cand.Kind != "audit_review" {
			continue
		}
		validItems = append(validItems, queuedAuditSearchItem{
			candidateID: cand.ID,
			artistID:    cand.ArtistID,
			artistName:  cand.ArtistName,
		})
	}

	q := m.ensureImageSearchQueue()
	q.mu.Lock()
	existing := make(map[int64]bool, len(q.pending)+1)
	if q.current != nil {
		existing[q.current.candidateID] = true
	}
	for _, item := range q.pending {
		existing[item.candidateID] = true
	}
	toAdd := make([]queuedAuditSearchItem, 0, len(validItems))
	for _, item := range validItems {
		if existing[item.candidateID] {
			continue
		}
		existing[item.candidateID] = true
		toAdd = append(toAdd, item)
	}
	if len(toAdd) == 0 {
		snap := q.snapshotLocked()
		q.mu.Unlock()
		return 0, snap, nil
	}

	startWorker := false
	var workerGen uint64
	if !q.running {
		q.status = "running"
		q.stopAfterCurrent = false
		q.total = len(toAdd)
		q.completed = 0
		q.found = 0
		q.notFound = 0
		q.failed = 0
		q.completedResults = nil
		q.pending = append([]queuedAuditSearchItem(nil), toAdd...)
		q.workerGen++
		workerGen = q.workerGen
		q.running = true
		startWorker = true
	} else {
		q.stopAfterCurrent = false
		q.pending = append(q.pending, toAdd...)
		q.total += len(toAdd)
	}
	q.updatedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
	snap := q.snapshotLocked()
	q.mu.Unlock()

	if startWorker {
		gen := workerGen
		m.goBackground("artist-image-search-queue", func() {
			m.runArtistImageSearchQueue(gen)
		})
	}
	return len(toAdd), snap, nil
}

// RemoveArtistImageCandidateSearch drops a waiting (not yet running) item from
// the manual search queue.
func (m *Manager) RemoveArtistImageCandidateSearch(candidateID int64) (bool, ArtistImageSearchQueueSnapshot) {
	q := m.ensureImageSearchQueue()
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, item := range q.pending {
		if item.candidateID == candidateID {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			if q.total > q.completed+len(q.pending) {
				q.total--
			}
			q.updatedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
			return true, q.snapshotLocked()
		}
	}
	return false, q.snapshotLocked()
}

// StopArtistImageCandidateSearchQueue clears all pending items and marks the
// queue to stop as soon as any currently executing lookup finishes.
func (m *Manager) StopArtistImageCandidateSearchQueue() ArtistImageSearchQueueSnapshot {
	q := m.ensureImageSearchQueue()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.status != "running" {
		return q.snapshotLocked()
	}
	dropped := len(q.pending)
	q.pending = nil
	if dropped > 0 && q.total >= dropped {
		q.total -= dropped
	}
	q.stopAfterCurrent = true
	if !q.running || q.current == nil {
		q.status = "stopped"
		q.stopAfterCurrent = false
	}
	q.updatedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
	return q.snapshotLocked()
}

// DismissArtistImageCandidateSearchQueue resets a finished or stopped queue
// banner back to idle.
func (m *Manager) DismissArtistImageCandidateSearchQueue() ArtistImageSearchQueueSnapshot {
	q := m.ensureImageSearchQueue()
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running {
		return q.snapshotLocked()
	}
	q.status = "idle"
	q.stopAfterCurrent = false
	q.pending = nil
	q.current = nil
	q.total = 0
	q.completed = 0
	q.found = 0
	q.notFound = 0
	q.failed = 0
	q.completedResults = nil
	q.updatedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
	return q.snapshotLocked()
}

func (m *Manager) runArtistImageSearchQueue(gen uint64) {
	q := m.ensureImageSearchQueue()
	defer func() {
		q.mu.Lock()
		if q.workerGen == gen && q.running {
			q.current = nil
			q.running = false
			if q.status == "running" {
				if q.stopAfterCurrent || m.baseCtx.Err() != nil {
					q.status = "stopped"
				} else {
					q.status = "completed"
				}
			}
			q.stopAfterCurrent = false
			q.updatedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
		}
		q.mu.Unlock()
	}()

	for {
		if m.baseCtx.Err() != nil {
			q.mu.Lock()
			q.pending = nil
			q.stopAfterCurrent = true
			q.mu.Unlock()
			return
		}

		q.mu.Lock()
		if q.stopAfterCurrent || len(q.pending) == 0 {
			q.current = nil
			if q.stopAfterCurrent {
				q.status = "stopped"
			} else {
				q.status = "completed"
			}
			q.stopAfterCurrent = false
			q.running = false
			q.updatedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
			q.mu.Unlock()
			return
		}
		item := q.pending[0]
		q.pending = q.pending[1:]
		itemCopy := item
		q.current = &itemCopy
		hook := q.testItemHook
		q.updatedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
		q.mu.Unlock()

		if hook != nil {
			hook(item.candidateID)
		}

		outcome := m.executeQueuedAuditSearch(m.baseCtx, item)

		q.mu.Lock()
		q.current = nil
		q.completed++
		switch outcome.Outcome {
		case "found":
			q.found++
		case "not_found":
			q.notFound++
		case "failed":
			q.failed++
		}
		q.completedResults = append(q.completedResults, outcome)
		if len(q.completedResults) > 100 {
			q.completedResults = q.completedResults[len(q.completedResults)-100:]
		}
		q.updatedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
		stopNow := q.stopAfterCurrent || len(q.pending) == 0
		if stopNow {
			if q.stopAfterCurrent {
				q.status = "stopped"
			} else {
				q.status = "completed"
			}
			q.stopAfterCurrent = false
			q.running = false
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()

		if m.sleep(m.baseCtx, artistImageSearchQueuePace) != nil {
			q.mu.Lock()
			q.pending = nil
			q.stopAfterCurrent = true
			q.mu.Unlock()
			return
		}
	}
}

func (m *Manager) executeQueuedAuditSearch(ctx context.Context, item queuedAuditSearchItem) ArtistImageSearchOutcome {
	out := ArtistImageSearchOutcome{
		CandidateID: item.candidateID,
		ArtistID:    item.artistID,
		ArtistName:  item.artistName,
	}
	cand, err := m.store.ArtistImageCandidateByID(ctx, item.candidateID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			out.Outcome = "skipped"
			out.Note = "待审项已处理"
			return out
		}
		out.Outcome = "failed"
		out.Note = RedactSourceError(err).Error()
		return out
	}
	if cand.Status != "pending" || cand.Kind != "audit_review" {
		out.Outcome = "skipped"
		out.Note = "待审项已处理"
		return out
	}
	found, ok, searchErr := m.SearchArtistImageCandidateForAudit(ctx, cand)
	if searchErr != nil {
		note := RedactSourceError(searchErr).Error()
		ts, _ := m.store.UpdateArtistImageCandidateSearchState(ctx, cand.ID, "failed", note)
		out.Outcome = "failed"
		out.Note = note
		out.SearchedAt = ts
		return out
	}
	if !ok || found.ID == 0 {
		const notFoundNote = "手动联网找图未找到更合适的候选头像"
		ts, _ := m.store.UpdateArtistImageCandidateSearchState(ctx, cand.ID, "not_found_manual", notFoundNote)
		out.Outcome = "not_found"
		out.Note = notFoundNote
		out.SearchedAt = ts
		return out
	}
	out.Outcome = "found"
	out.NewCandidateID = found.ID
	out.Note = found.Reason
	out.SearchedAt = m.clockNow().UTC().Format(time.RFC3339Nano)
	return out
}
