package enrichment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

// Series grouping (4.5.3, D21~D25): works bound to Bangumi type-2 subjects
// are grouped into single-layer series by walking only 续集/前传 relations as
// an undirected graph. Library-external subjects may bridge library works,
// but a path may leave the library for at most seriesMaxOutsideHops hops and
// a component is truncated at seriesMaxNodes nodes. Local heuristic grouping
// (title prefixes, similar years) is deliberately absent: Bangumi is the
// only authority (R2).
const (
	seriesMaxOutsideHops = 3
	seriesMaxNodes       = 40
)

func isSeriesRelation(relation string) bool {
	return relation == "续集" || relation == "前传"
}

// fetchSeriesRelations returns the raw Bangumi subject relations for one
// subject using the shared subject-rel cache key, throttle and run memo.
func (m *Manager) fetchSeriesRelations(ctx context.Context, setting storage.MetadataSourceSetting, subjectID int64, force bool) ([]musicRelation, error) {
	base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
	if base == "" {
		base = "https://api.bgm.tv"
	}
	key := strconv.FormatInt(subjectID, 10)
	var related []musicRelation
	_, err := m.cachedJSON(ctx, "bangumi", "subject-rel:"+key, base+"/v0/subjects/"+key+"/subjects", setting, force, nil, &related)
	if err != nil {
		return nil, err
	}
	return related, nil
}

// enrichBangumiSeries recomputes automatic series membership for the whole
// library. Components whose fetches all succeeded are applied; a component
// with any failed request — or touching any node a failed component visited —
// is skipped so existing membership survives a partial outage, and the next
// successful run heals it (B2).
func (m *Manager) enrichBangumiSeries(ctx context.Context, runID int64, force bool) (string, error) {
	setting, err := m.store.MetadataSourceSetting(ctx, "bangumi")
	if err != nil || !setting.Enabled {
		return "skipped", err
	}
	seeds, err := m.store.BangumiSeriesSeeds(ctx)
	if err != nil {
		return "", err
	}
	if len(seeds) == 0 {
		return "skipped", nil
	}
	// Only type-2 subjects take part (R2). Profiles cached before the type was
	// recorded are resolved through the subject detail cache.
	library := map[int64]int64{} // subject id -> work id
	consecutiveFailures := 0
	abortFailures := 0
	aborted := false
	seedFailed := map[int64]bool{}
	for _, seed := range seeds {
		if aborted {
			// The circuit breaker tripped: stop issuing detail requests.
			break
		}
		subjectType := seed.SubjectType
		if subjectType == 0 {
			base := strings.TrimRight(m.phaseEndpoints.BangumiAPI, "/")
			if base == "" {
				base = "https://api.bgm.tv"
			}
			var subject struct {
				Type int `json:"type"`
			}
			key := strconv.FormatInt(seed.SubjectID, 10)
			_, fetchErr := m.cachedJSON(ctx, "bangumi", "subject:"+key, base+"/v0/subjects/"+key, setting, force, nil, &subject)
			if errors.Is(fetchErr, sql.ErrNoRows) {
				continue
			}
			if fetchErr != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				seedFailed[seed.WorkID] = true
				consecutiveFailures++
				if consecutiveFailures >= maxConsecutiveFailures {
					aborted = true
					abortFailures = consecutiveFailures
				}
				continue
			}
			consecutiveFailures = 0
			subjectType = subject.Type
		}
		if subjectType != 2 {
			continue
		}
		if _, exists := library[seed.SubjectID]; !exists {
			library[seed.SubjectID] = seed.WorkID
		}
	}
	libraryWorks := map[int64]bool{}
	for _, workID := range library {
		libraryWorks[workID] = true
	}
	// BFS state is (subject, minimal outside hops): a node re-enters the queue
	// whenever a strictly smaller outside count is found, so the result does
	// not depend on traversal order (M1, D23: ≤3 hops outside the library).
	visited := map[int64]bool{}
	tainted := map[int64]bool{} // nodes a failed component touched
	var components [][]int64
	seedOrder := make([]int64, 0, len(library))
	for subjectID := range library {
		seedOrder = append(seedOrder, subjectID)
	}
	// Deterministic traversal: by owning work id, then subject id.
	sortSubjectsByWork(seedOrder, library)
	for _, seedSubject := range seedOrder {
		if visited[seedSubject] || aborted {
			continue
		}
		visited[seedSubject] = true
		dist := map[int64]int{seedSubject: 0}
		inComponent := map[int64]bool{seedSubject: true}
		dequeued := map[int64]bool{}
		deque := []int64{seedSubject}
		var subjects []int64
		dirty := false
		for len(deque) > 0 {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if len(subjects) >= seriesMaxNodes {
				// Node cap (M2): stop dequeuing as well as enqueuing, so the
				// component never exceeds seriesMaxNodes nodes.
				break
			}
			node := deque[0]
			deque = deque[1:]
			// A relaxed node may be dequeued again; it is recorded once but its
			// edges are re-walked with the improved outside count.
			if !dequeued[node] {
				dequeued[node] = true
				subjects = append(subjects, node)
			}
			current := dist[node]
			relations, fetchErr := m.fetchSeriesRelations(ctx, setting, node, force)
			if errors.Is(fetchErr, sql.ErrNoRows) {
				continue
			}
			if fetchErr != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				// A failed component keeps its current membership this run.
				dirty = true
				consecutiveFailures++
				if consecutiveFailures >= maxConsecutiveFailures {
					aborted = true
					abortFailures = consecutiveFailures
				}
				break
			}
			consecutiveFailures = 0
			taintedHit := false
			for _, relation := range relations {
				if relation.Type != 2 || !isSeriesRelation(relation.Relation) {
					continue
				}
				if tainted[relation.ID] {
					// Any component reaching a node a failed component touched is
					// dirty as well (B2).
					taintedHit = true
					break
				}
				next := 0
				if _, isLibrary := library[relation.ID]; !isLibrary {
					next = current + 1
				}
				if next > seriesMaxOutsideHops {
					continue
				}
				old, seen := dist[relation.ID]
				if seen && next >= old {
					continue
				}
				if !seen && len(inComponent) >= seriesMaxNodes {
					continue
				}
				dist[relation.ID] = next
				if !seen {
					inComponent[relation.ID] = true
					visited[relation.ID] = true
				}
				// 0-1 BFS: a library node resets the outside count and is
				// expanded first.
				if next <= current {
					deque = append([]int64{relation.ID}, deque...)
				} else {
					deque = append(deque, relation.ID)
				}
			}
			if taintedHit {
				dirty = true
				break
			}
		}
		if dirty {
			// Taint every node this component touched, including the queued
			// remainder, so later components skip them too (B2).
			for subject := range inComponent {
				tainted[subject] = true
			}
			continue
		}
		if aborted {
			continue
		}
		works := map[int64]bool{}
		var component []int64
		for _, subject := range subjects {
			if workID, ok := library[subject]; ok && !works[workID] {
				works[workID] = true
				component = append(component, workID)
			}
		}
		if len(component) > 0 {
			components = append(components, component)
		}
	}
	// M3: works still holding automatic membership that were not part of this
	// run's library at all (e.g. their binding moved to a non type-2 subject)
	// are passed as singleton components so the stale membership is cleaned.
	// Works whose own fetch failed keep their membership instead.
	if !aborted {
		autoMembers, memberErr := m.store.AutoSeriesMemberWorkIDs(ctx)
		if memberErr != nil {
			return "", memberErr
		}
		for _, workID := range autoMembers {
			if !libraryWorks[workID] && !seedFailed[workID] {
				components = append(components, []int64{workID})
			}
		}
	}
	stats, err := m.store.ApplyAutoSeries(ctx, runID, components)
	if err != nil {
		return "", err
	}
	m.logger.Info("bangumi series grouping applied", "runId", runID, "components", len(components), "created", stats.SeriesCreated, "deleted", stats.SeriesDeleted, "membersAdded", stats.MembersAdded, "membersRemoved", stats.MembersRemoved, "aborted", aborted)
	if aborted {
		if abortFailures == 0 {
			abortFailures = consecutiveFailures
		}
		return "", fmt.Errorf("bangumi series grouping aborted after %d consecutive failures", abortFailures)
	}
	return "succeeded", nil
}

// sortSubjectsByWork orders subject ids by their owning work id so component
// traversal is deterministic across runs.
func sortSubjectsByWork(subjects []int64, library map[int64]int64) {
	sort.Slice(subjects, func(i, j int) bool {
		left, right := library[subjects[i]], library[subjects[j]]
		if left != right {
			return left < right
		}
		return subjects[i] < subjects[j]
	})
}
