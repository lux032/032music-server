package discovery

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/lux032/032music-server/internal/bangumi"
	"github.com/lux032/032music-server/internal/discovery/mb"
	"github.com/lux032/032music-server/internal/storage"
)

// BangumiSource is supplied by enrichment, sharing its cache, settings and source gate.
type BangumiSource interface {
	BangumiEnabled(context.Context) (bool, error)
	BangumiRelations(context.Context, int64) ([]bangumi.Relation, error)
	BangumiMusicSubject(context.Context, int64) (bangumi.MusicSubject, error)
}

func (s *Service) workSnapshot(ctx context.Context) ([]storage.DiscoveryWorkSeed, error) {
	if s.bangumi == nil {
		return nil, nil
	}
	enabled, e := s.bangumi.BangumiEnabled(ctx)
	if e != nil || !enabled {
		return nil, e
	}
	seeds, e := s.store.DiscoveryWorkSeedSnapshot(ctx)
	if e != nil {
		return nil, e
	}
	signals, e := s.store.DiscoveryWorkSignals(ctx)
	if e != nil {
		return nil, e
	}
	affinities := map[int64]float64{}
	for _, v := range seeds {
		affinities[v.WorkID] = WorkAffinity(v.WorkID, v.SeriesID, signals)
	}
	sort.Slice(seeds, func(i, j int) bool {
		a, b := affinities[seeds[i].WorkID], affinities[seeds[j].WorkID]
		if a != b {
			return a > b
		}
		return seeds[i].WorkID < seeds[j].WorkID
	})
	if len(seeds) > MaxWorkSeeds {
		seeds = seeds[:MaxWorkSeeds]
	}
	return seeds, nil
}
func (s *Service) executeWorks(ctx context.Context, g int64, mbThrottled bool) (string, error) {
	if s.bangumi == nil {
		return "Bangumi 未启用，已跳过作品来源", nil
	}
	enabled, e := s.bangumi.BangumiEnabled(ctx)
	if e != nil {
		return "", e
	}
	if !enabled {
		return "Bangumi 未启用，已跳过作品来源", nil
	}
	seeds, e := s.store.DiscoveryWorkSeeds(ctx, g)
	if e != nil {
		return "", e
	}
	local, e := s.store.DiscoveryLocalBangumiMusic(ctx)
	if e != nil {
		return "", e
	}
	type work struct {
		seed      storage.DiscoveryWorkSeed
		relations []bangumi.Relation
		music     []storage.DiscoveryMusic
		groups    []storage.DiscoveryReleaseGroup
		mappings  []storage.DiscoveryMapping
		status    string
	}
	works := []work{}
	// Collect search jobs across works, oldest searches first, to prevent budget starvation.
	type job struct {
		work, index int
		cached      storage.DiscoveryMusic
		decided     bool
	}
	jobs := []job{}
	throttled := false
	for _, seed := range seeds {
		if seed.Status != "pending" {
			continue
		}
		relations, e := withCooldownLimit(ctx, s, workCooldownRetries, func() ([]bangumi.Relation, error) { return s.bangumi.BangumiRelations(ctx, seed.SubjectID) })
		status := "done"
		if errors.Is(e, bangumi.ErrGone) {
			relations = nil
			status = "gone"
		} else if isCooldown(e) && s.cooldowns > 0 {
			// Remaining work seeds stay pending for the next refresh.
			throttled = true
			break
		} else if e != nil {
			return "", e
		}
		filtered := []bangumi.Relation{}
		seenSubjects := map[int64]bool{}
		for _, r := range relations {
			if r.ID > 0 && !seenSubjects[r.ID] && r.Type == 3 && RelationWeight(r.Relation) > 0 && !local[r.ID] {
				seenSubjects[r.ID] = true
				filtered = append(filtered, r)
			}
		}
		sort.SliceStable(filtered, func(i, j int) bool {
			return RelationWeight(filtered[i].Relation) > RelationWeight(filtered[j].Relation)
		})
		if len(filtered) > MaxMusicPerWork {
			filtered = filtered[:MaxMusicPerWork]
		}
		w := work{seed: seed, relations: relations, status: status}
		wi := len(works)
		for i, r := range filtered {
			cached, decided, e := s.store.DiscoveryMusicSearchState(ctx, r.ID)
			if e != nil {
				return "", e
			}
			if cached.SubjectID == 0 {
				cached = storage.DiscoveryMusic{SubjectID: r.ID, Name: r.Name, NameCN: r.NameCN, SearchStatus: "pending", Status: "ok"}
			}
			cached.Relation = r.Relation
			w.music = append(w.music, cached)
			jobs = append(jobs, job{wi, i, cached, decided})
		}
		works = append(works, w)
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].cached.SearchedAt < jobs[j].cached.SearchedAt })
	budget := 0
	truncated := false
	// Each subject is fetched and searched at most once even if related to several works.
	type result struct {
		music    storage.DiscoveryMusic
		groups   []storage.DiscoveryReleaseGroup
		mappings []storage.DiscoveryMapping
	}
	results := map[int64]result{}
	remaining := map[int]int{}
	for _, j := range jobs {
		remaining[j.work]++
	}
	saveCheckpoints := func() error {
		for wi, w := range works {
			if remaining[wi] == 0 {
				if err := s.store.SaveDiscoveryWorkSeed(ctx, g, w.seed, w.music, w.groups, w.mappings, w.status, "", s.now()); err != nil {
					return err
				}
			}
		}
		return nil
	}
	fail := func(err error, wi, mi int) (string, error) {
		works[wi].music[mi].SearchStatus = "failed"
		works[wi].status = "failed"
		remaining[wi] = 0
		if checkpointErr := saveCheckpoints(); checkpointErr != nil {
			return "", checkpointErr
		}
		return "", err
	}
	for _, j := range jobs {
		remaining[j.work]--
		w := &works[j.work]
		m := w.music[j.index]
		if prior, ok := results[m.SubjectID]; ok {
			relation := m.Relation
			w.music[j.index] = prior.music
			w.music[j.index].Relation = relation
			w.groups = append(w.groups, prior.groups...)
			w.mappings = append(w.mappings, prior.mappings...)
			continue
		}
		fresh := false
		if t, e := time.Parse(time.RFC3339, m.SearchedAt); e == nil {
			fresh = s.now().Sub(t) < MappingTTL
		}
		if j.decided || fresh {
			continue
		}
		if budget >= MaxMBSearchesPerRefresh || throttled || mbThrottled {
			m.SearchStatus = "pending"
			w.music[j.index] = m
			truncated = true
			continue
		}
		subject, e := withCooldownLimit(ctx, s, workCooldownRetries, func() (bangumi.MusicSubject, error) { return s.bangumi.BangumiMusicSubject(ctx, m.SubjectID) })
		if errors.Is(e, bangumi.ErrGone) {
			m.Status = "gone"
			m.SearchStatus = "none"
			m.SearchUpdated = true
			m.SearchedAt = s.now().UTC().Format(time.RFC3339)
			w.music[j.index] = m
			continue
		}
		if isCooldown(e) && s.cooldowns > 0 {
			throttled, truncated = true, true
			m.SearchStatus = "pending"
			w.music[j.index] = m
			continue
		}
		if e != nil {
			return fail(e, j.work, j.index)
		}
		m.Name = subject.Name
		m.NameCN = subject.NameCN
		m.Date = subject.Date
		m.Artists = subject.Artists
		m.MultiTitle = bangumi.SplitMultiTitle(subject.Name) != nil
		fallback := false
		hits, e := withCooldownLimit(ctx, s, workCooldownRetries, func() ([]mb.ReleaseGroup, error) { return s.mb.SearchReleaseGroups(ctx, subject.Name) })
		budget++
		if isCooldown(e) && s.cooldowns > 0 {
			throttled, truncated = true, true
			m.SearchStatus = "pending"
			w.music[j.index] = m
			continue
		}
		if e != nil {
			return fail(e, j.work, j.index)
		}
		if len(hits) == 0 && subject.NameCN != "" && subject.NameCN != subject.Name {
			if budget < MaxMBSearchesPerRefresh {
				fallback = true
				hits, e = withCooldownLimit(ctx, s, workCooldownRetries, func() ([]mb.ReleaseGroup, error) { return s.mb.SearchReleaseGroups(ctx, subject.NameCN) })
				budget++
				if isCooldown(e) && s.cooldowns > 0 {
					throttled, truncated = true, true
					m.SearchStatus = "pending"
					w.music[j.index] = m
					continue
				}
				if e != nil {
					return fail(e, j.work, j.index)
				}
			} else {
				m.SearchStatus = "pending"
				w.music[j.index] = m
				truncated = true
				continue
			}
		}
		duplicate := false
		for _, r := range w.relations {
			if r.ID != m.SubjectID && NormalizeTitle(r.Name) == NormalizeTitle(subject.Name) {
				duplicate = true
			}
		}
		classificationSubject := subject
		if fallback {
			classificationSubject.Name = subject.NameCN
		}
		mappings := ClassifyMapping(classificationSubject, hits, duplicate, s.now())
		mappedIDs := map[string]bool{}
		for i := range mappings {
			mappedIDs[mappings[i].RGID] = true
			if fallback {
				mappings[i].Status = "suggested"
			}
		}
		eligibleCount := 0
		groups := []storage.DiscoveryReleaseGroup{}
		for _, rg := range hits {
			if kind, ok := Eligible(rg, s.now()); ok {
				eligibleCount++
				if mappedIDs[rg.ID] {
					groups = append(groups, discoveryGroup(rg, kind))
				}
			}
		}
		m.SearchStatus = "none"
		if len(hits) > 0 && eligibleCount == 0 {
			m.SearchStatus = "filtered"
		}
		if len(mappings) > 0 {
			m.SearchStatus = "hits"
		}
		m.SearchUpdated = true
		m.SearchedAt = s.now().UTC().Format(time.RFC3339)
		w.music[j.index] = m
		w.groups = append(w.groups, groups...)
		w.mappings = append(w.mappings, mappings...)
		results[m.SubjectID] = result{m, groups, mappings}
	}
	all := []storage.DiscoveryMapping{}
	for _, w := range works {
		all = append(all, w.mappings...)
	}
	reconcileMappingConflicts(all)
	pos := 0
	for _, w := range works {
		copy(w.mappings, all[pos:pos+len(w.mappings)])
		pos += len(w.mappings)
		if e = s.store.SaveDiscoveryWorkSeed(ctx, g, w.seed, w.music, w.groups, w.mappings, w.status, "", s.now()); e != nil {
			return "", e
		}
	}
	if throttled {
		return "MusicBrainz/Bangumi 持续限流，部分作品对应尚未完成，再次刷新会从断点继续", nil
	}
	if truncated {
		return "Bangumi 检索预算已用完，下次刷新继续", nil
	}
	return "", nil
}
func discoveryGroup(rg mb.ReleaseGroup, kind string) storage.DiscoveryReleaseGroup {
	credit := ""
	for _, c := range rg.ArtistCredit {
		name := c.Name
		if name == "" {
			name = c.Artist.Name
		}
		if credit != "" {
			credit += ", "
		}
		credit += name
	}
	return storage.DiscoveryReleaseGroup{RGID: rg.ID, Title: rg.Title, PrimaryType: rg.PrimaryType, SecondaryTypes: rg.SecondaryTypes, Kind: kind, FirstReleaseDate: rg.FirstReleaseDate, Disambiguation: rg.Disambiguation, ArtistCredit: credit}
}

// Rerank is synchronous and database-only. It is a no-op during a refresh or before completion.
func (s *Service) Rerank(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false, nil
	}
	state, e := s.store.DiscoveryStatus(ctx)
	if e != nil {
		return false, e
	}
	if state.Status != "completed" {
		return false, nil
	}
	started := time.Now()
	_, e = s.rankAndSave(ctx, state.Generation)
	s.logger.Info("discovery rerank", "duration", time.Since(started).Round(time.Millisecond).String(), "error", e)
	if e == nil && s.autoCovers {
		s.BackfillCovers()
	}
	return e == nil, e
}
func (s *Service) rankAndSave(ctx context.Context, g int64) ([]storage.DiscoveryCandidate, error) {
	seeds, e := s.store.DiscoverySeeds(ctx, g)
	if e != nil {
		return nil, e
	}
	works, e := s.store.DiscoveryWorkSeeds(ctx, g)
	if e != nil {
		return nil, e
	}
	catalog, e := s.store.DiscoveryCatalog(ctx)
	if e != nil {
		return nil, e
	}
	workCatalog, e := s.store.DiscoveryWorkCatalog(ctx)
	if e != nil {
		return nil, e
	}
	catalog = append(catalog, workCatalog...)
	signals, e := s.store.DiscoveryArtistSignals(ctx)
	if e != nil {
		return nil, e
	}
	workSignals, e := s.store.DiscoveryWorkSignals(ctx)
	if e != nil {
		return nil, e
	}
	albums, e := s.store.DiscoveryLocalAlbums(ctx)
	if e != nil {
		return nil, e
	}
	rejections, e := s.store.DiscoveryRejections(ctx)
	if e != nil {
		return nil, e
	}
	matches := WeakMatches(catalog, albums, rejections)
	ids := []string{}
	for _, v := range catalog {
		ids = append(ids, v.RGID)
	}
	owned, e := s.store.DiscoveryOwnership(ctx, ids)
	if e != nil {
		return nil, e
	}
	ranked := Rank(catalog, signals, owned, matches, s.now(), workSignals)
	if e = s.store.SaveDiscoveryCandidates(ctx, g, seeds, ranked, matches, works); e != nil {
		return nil, e
	}
	return ranked, nil
}

// MappingDecision preserves the user decision before reranking without any external calls.
func (s *Service) MappingDecision(ctx context.Context, subjectID int64, rgid, decision string) (bool, error) {
	if e := s.store.DiscoveryMappingDecision(ctx, subjectID, rgid, decision); e != nil {
		return false, e
	}
	reranked, e := s.Rerank(ctx)
	if e != nil {
		s.logger.Warn("mapping decision saved; rerank failed", "error", e)
		return false, nil
	}
	return reranked, e
}
