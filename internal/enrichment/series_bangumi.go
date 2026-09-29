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

// seriesCrossRelationPairs is the D52 whitelist for cross-media suggestions:
// only edges where the two directions register one of these relation pairs
// are believed. Pairs like 不同世界观 / 相同世界观 / 联动 / 角色出演 / 其他 /
// 外传 / 合集 / 不同版本 are intentionally absent; extend this list when a
// new pair proves trustworthy.
var seriesCrossRelationPairs = map[[2]string]bool{
	{"游戏", "动画"}:     true, // type 4 ↔ type 2
	{"动画", "游戏"}:     true,
	{"衍生", "主线故事"}:   true,
	{"主线故事", "衍生"}:   true,
	{"番外篇", "主线故事"}:  true,
	{"主线故事", "番外篇"}:  true,
	{"总集篇", "全集"}:    true,
	{"全集", "总集篇"}:    true,
	{"不同演绎", "不同演绎"}: true,
}

// seriesSuggestionPair is one suggested work pair (D53: the suggestion is
// work-to-work; the subject pair is kept for the permanent decision record).
type seriesSuggestionPair struct {
	workA, workB       int64
	subjectA, subjectB int64
	relationAB         string
	relationBA         string
	kind               string // "cross" | "sequel"
}

// seriesSuggestionPairs is the pure pairing core of the suggestion scan.
// relations holds the successfully fetched relation lists by subject id; lib
// maps every suggestable subject (type 2 and type 4) to its work. An edge is
// only considered when both ends are in the library and both relation lists
// were fetched this run. The two directions must either form a whitelisted
// cross pair (D52, kind='cross'), or be a reciprocal 续集/前传 pair that
// survives the D67 narrowing (kind='sequel'): (a) at least one end is a
// manual series member — the merged-chain new-season gap of D60 — or (b)
// both ends are type-4 subjects (game sequels the BFS never walks). Plain
// type-2 sequel pairs get no suggestion: automatic grouping owns them and a
// pair skipped this run (tainted, node cap) heals on the next. Pairs
// already sharing a series, touching a locked work (D56) or already decided
// (D55) are skipped.
func seriesSuggestionPairs(relations map[int64][]musicRelation, lib map[int64]int64, workSeries map[int64]int64, manualMember map[int64]bool, type4 map[int64]bool, locked map[int64]bool, decided map[[2]int64]bool) []seriesSuggestionPair {
	relationTo := func(from, to int64) (string, bool) {
		for _, relation := range relations[from] {
			if relation.ID == to {
				return relation.Relation, true
			}
		}
		return "", false
	}
	seen := map[[2]int64]bool{}
	var pairs []seriesSuggestionPair
	for subjectA, outgoing := range relations {
		workA, ok := lib[subjectA]
		if !ok {
			continue
		}
		for _, relation := range outgoing {
			subjectB := relation.ID
			workB, ok := lib[subjectB]
			if !ok {
				continue
			}
			if subjectA == subjectB || workA == workB {
				continue
			}
			if _, fetched := relations[subjectB]; !fetched {
				// The reverse direction was not fetched this run: the pair
				// cannot be verified (D52).
				continue
			}
			key := [2]int64{subjectA, subjectB}
			if subjectA > subjectB {
				key = [2]int64{subjectB, subjectA}
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			relationBA, ok := relationTo(subjectB, subjectA)
			if !ok {
				continue
			}
			if locked[workA] || locked[workB] {
				continue
			}
			if seriesA, seriesB := workSeries[workA], workSeries[workB]; seriesA != 0 && seriesA == seriesB {
				continue
			}
			if decided[key] {
				continue
			}
			kind := ""
			switch {
			case isSeriesRelation(relation.Relation) && isSeriesRelation(relationBA):
				// D67: reciprocal sequel/prequel pairs only become suggestions
				// when (a) one end is a manual series member (the merged-chain
				// gap, D60) or (b) both ends are type-4 subjects (game sequels
				// the BFS never groups). Plain type-2 pairs belong to automatic
				// grouping and heal on the next run.
				if !manualMember[workA] && !manualMember[workB] && !(type4[subjectA] && type4[subjectB]) {
					continue
				}
				kind = "sequel"
			case seriesCrossRelationPairs[[2]string{relation.Relation, relationBA}]:
				kind = "cross"
			default:
				continue
			}
			pair := seriesSuggestionPair{workA: workA, workB: workB, subjectA: subjectA, subjectB: subjectB, relationAB: relation.Relation, relationBA: relationBA, kind: kind}
			if pair.subjectA > pair.subjectB {
				pair.subjectA, pair.subjectB = pair.subjectB, pair.subjectA
				pair.workA, pair.workB = pair.workB, pair.workA
				pair.relationAB, pair.relationBA = pair.relationBA, pair.relationAB
			}
			pairs = append(pairs, pair)
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].subjectA != pairs[j].subjectA {
			return pairs[i].subjectA < pairs[j].subjectA
		}
		return pairs[i].subjectB < pairs[j].subjectB
	})
	return pairs
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
	library := map[int64]int64{}        // subject id -> work id (type 2)
	suggestLibrary := map[int64]int64{} // subject id -> work id (type 2 and 4)
	type4Subjects := map[int64]bool{}   // subjects bound to type-4 entries
	seedResolved := map[int64]bool{}    // works whose binding was resolved this run without failure
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
		// M1: a seed whose binding resolved (from the cached profile or a
		// successful detail fetch) counts as processed for suggestion
		// cleanup, even when its subject type keeps it out of suggestLibrary
		// (e.g. rebound to a non type-2/4 entry, or the subject is shared by
		// another work) — its stale suggestions must not linger.
		seedResolved[seed.WorkID] = true
		if subjectType == 2 {
			if _, exists := library[seed.SubjectID]; !exists {
				library[seed.SubjectID] = seed.WorkID
			}
		}
		// The suggestion pass also covers games (type 4), but in a separate
		// map: the type-2 library used by the BFS must stay unchanged so a
		// game is never pulled into automatic grouping by a sequel chain.
		if subjectType == 2 || subjectType == 4 {
			if _, exists := suggestLibrary[seed.SubjectID]; !exists {
				suggestLibrary[seed.SubjectID] = seed.WorkID
			}
			if subjectType == 4 {
				type4Subjects[seed.SubjectID] = true
			}
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
	tainted := map[int64]bool{}            // nodes a failed component touched
	fetched := map[int64][]musicRelation{} // relation lists fetched successfully this run (404 = empty)
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
				fetched[node] = nil
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
			fetched[node] = relations
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
	// Cross-media and sequel suggestions (4.6 batch 5, D52~D60) run only when
	// the grouping pass completed: the pairing requires both relation lists of
	// a pair, and an aborted run cannot know which lists are current.
	suggestions := 0
	if !aborted {
		var suggestErr error
		suggestions, suggestErr = m.generateSeriesSuggestions(ctx, runID, setting, force, suggestLibrary, library, fetched, seedResolved, type4Subjects)
		if suggestErr != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", suggestErr
		}
		m.logger.Info("bangumi series suggestions applied", "runId", runID, "suggestions", suggestions)
	}
	if aborted {
		if abortFailures == 0 {
			abortFailures = consecutiveFailures
		}
		return "", fmt.Errorf("bangumi series grouping aborted after %d consecutive failures", abortFailures)
	}
	return "succeeded", nil
}

// generateSeriesSuggestions computes and stores this run's series
// suggestions. suggestLibrary covers type-2 and type-4 subjects; library is
// the type-2-only BFS library. Relation lists of type-2 subjects are reused
// from the BFS (no extra requests); type-4 subjects are fetched on demand
// with the shared cache key, throttle, cancellation and circuit breaker.
// Works whose relations could not be fetched stay unprocessed: their old
// suggestions survive the run untouched.
func (m *Manager) generateSeriesSuggestions(ctx context.Context, runID int64, setting storage.MetadataSourceSetting, force bool, suggestLibrary, library map[int64]int64, fetched map[int64][]musicRelation, seedResolved, type4Subjects map[int64]bool) (int, error) {
	if len(suggestLibrary) == 0 {
		return 0, nil
	}
	locked, err := m.store.WorkSeriesLockedIDs(ctx)
	if err != nil {
		return 0, err
	}
	// Type-4 subjects were never fetched by the BFS. Fetch them on demand;
	// locked works are excluded from suggestions altogether (D56), so their
	// relations are not worth a request.
	var type4 []int64
	for subjectID, workID := range suggestLibrary {
		if _, isType2 := library[subjectID]; isType2 {
			continue
		}
		if locked[workID] {
			continue
		}
		type4 = append(type4, subjectID)
	}
	sortSubjectsByWork(type4, suggestLibrary)
	consecutiveFailures := 0
	for _, subjectID := range type4 {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		relations, fetchErr := m.fetchSeriesRelations(ctx, setting, subjectID, force)
		if errors.Is(fetchErr, sql.ErrNoRows) {
			fetched[subjectID] = nil
			continue
		}
		if fetchErr != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			consecutiveFailures++
			m.logger.Warn("bangumi series suggestion relation fetch failed", "subject", subjectID, "error", fetchErr)
			if consecutiveFailures >= maxConsecutiveFailures {
				// Abort without writing: every old suggestion stays in place,
				// so nothing is lost by the outage.
				return 0, fmt.Errorf("bangumi series suggestions aborted after %d consecutive failures", consecutiveFailures)
			}
			continue
		}
		consecutiveFailures = 0
		fetched[subjectID] = relations
	}
	workSeries, err := m.store.WorkSeriesMembership(ctx)
	if err != nil {
		return 0, err
	}
	manualMember, err := m.store.WorkSeriesManualMemberIDs(ctx)
	if err != nil {
		return 0, err
	}
	decided, err := m.store.SeriesSuggestionDecidedPairs(ctx)
	if err != nil {
		return 0, err
	}
	pairs := seriesSuggestionPairs(fetched, suggestLibrary, workSeries, manualMember, type4Subjects, locked, decided)
	inputs := make([]storage.SeriesSuggestionInput, 0, len(pairs))
	for _, pair := range pairs {
		inputs = append(inputs, storage.SeriesSuggestionInput{WorkA: pair.workA, WorkB: pair.workB, SubjectA: pair.subjectA, SubjectB: pair.subjectB, RelationAB: pair.relationAB, RelationBA: pair.relationBA, Kind: pair.kind})
	}
	processed := map[int64]bool{}
	for subjectID, workID := range suggestLibrary {
		if _, ok := fetched[subjectID]; ok {
			processed[workID] = true
		}
	}
	// M1: a work whose binding resolved this run but which is not in
	// suggestLibrary (rebound to a non type-2/4 subject, or its subject is
	// primarily bound to another work) counts as processed too, so its stale
	// suggestions are deleted instead of lingering forever. Works with a
	// failed resolution (seedFailed, absent from seedResolved) keep theirs.
	suggestableWorks := map[int64]bool{}
	for _, workID := range suggestLibrary {
		suggestableWorks[workID] = true
	}
	for workID := range seedResolved {
		if !suggestableWorks[workID] {
			processed[workID] = true
		}
	}
	processedList := make([]int64, 0, len(processed))
	for workID := range processed {
		processedList = append(processedList, workID)
	}
	sort.Slice(processedList, func(i, j int) bool { return processedList[i] < processedList[j] })
	if err = m.store.ReplaceSeriesSuggestions(ctx, runID, inputs, processedList); err != nil {
		return 0, err
	}
	return len(inputs), nil
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
