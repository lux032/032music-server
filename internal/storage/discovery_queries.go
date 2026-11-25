package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

func (s *Store) DiscoveryLocalAlbums(c context.Context) ([]DiscoveryLocalAlbum, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, `SELECT a.id,COALESCE(a.user_title,a.title),COALESCE(a.user_release_year,a.release_year,0),(SELECT count(*) FROM tracks t WHERE t.album_id=a.id),COALESCE((SELECT rgid FROM album_release_groups g WHERE g.album_id=a.id AND status='resolved'),'') FROM albums a WHERE `+albumVisibleSQL("a.id"))
	if e != nil {
		return nil, e
	}
	out := []DiscoveryLocalAlbum{}
	for rows.Next() {
		var v DiscoveryLocalAlbum
		if e = rows.Scan(&v.ID, &v.Title, &v.Year, &v.TrackCount, &v.CurrentRG); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = s.db.QueryContext(c, artistMergeChainSQL+` SELECT aa.album_id,a.root FROM album_artists aa JOIN chain a ON a.id=aa.artist_id`)
	if e != nil {
		return nil, e
	}
	index := map[int64]int{}
	for i, v := range out {
		index[v.ID] = i
	}
	for rows.Next() {
		var album, artist int64
		if e = rows.Scan(&album, &artist); e != nil {
			rows.Close()
			return nil, e
		}
		if i, ok := index[album]; ok {
			out[i].ArtistIDs = append(out[i].ArtistIDs, artist)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = s.db.QueryContext(c, `SELECT album_id,work_id FROM album_works`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var a, w int64
		if e = rows.Scan(&a, &w); e != nil {
			return nil, e
		}
		if i, ok := index[a]; ok {
			out[i].WorkIDs = append(out[i].WorkIDs, w)
		}
	}
	return out, rows.Err()
}
func (s *Store) DiscoveryOwnership(c context.Context, ids []string) (map[string]Ownership, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	out := make(map[string]Ownership, len(ids))
	keys := make([]string, 0, len(ids))
	for _, raw := range ids {
		id, e := normalizedDiscoveryID(raw)
		if e != nil {
			return nil, e
		}
		if _, exists := out[id]; !exists {
			out[id] = Ownership{}
			keys = append(keys, id)
		}
	}
	if len(keys) == 0 {
		return out, nil
	}
	raw, _ := json.Marshal(keys)
	rows, e := s.db.QueryContext(c, `SELECT j.value,COALESCE(g.album_id,0),EXISTS(SELECT 1 FROM release_group_decisions d WHERE d.rgid=j.value),EXISTS(SELECT 1 FROM acquisition_tasks t WHERE t.rgid=j.value AND t.state IN (`+AcquisitionActiveStates+`)) FROM json_each(?) j LEFT JOIN album_release_groups g ON g.rgid=j.value AND g.status='resolved'`, string(raw))
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id string
		var album int64
		var ignored, acquiring bool
		if e = rows.Scan(&id, &album, &ignored, &acquiring); e != nil {
			rows.Close()
			return nil, e
		}
		v := out[id]
		v.Ignored = ignored
		v.Acquiring = acquiring
		if album != 0 {
			v.Albums = append(v.Albums, album)
		}
		out[id] = v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = s.db.QueryContext(c, `SELECT p.rgid,a.id,COALESCE(a.user_title,a.title),COALESCE(a.user_release_year,a.release_year,0),(SELECT count(*) FROM tracks t WHERE t.album_id=a.id),COALESCE((SELECT rgid FROM album_release_groups g WHERE g.album_id=a.id AND status='resolved'),''),p.reason,`+albumArtworkURLSQL+` FROM discovery_possible_matches p JOIN albums a ON a.id=p.album_id WHERE p.rgid IN (SELECT value FROM json_each(?)) AND `+albumVisibleSQL("a.id")+` AND NOT EXISTS(SELECT 1 FROM discovery_match_rejections r WHERE r.rgid=p.rgid AND r.album_id=p.album_id) ORDER BY a.id`, string(raw))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var a DiscoveryLocalAlbum
		if e = rows.Scan(&id, &a.ID, &a.Title, &a.Year, &a.TrackCount, &a.CurrentRG, &a.Reason, &a.ArtworkURL); e != nil {
			return nil, e
		}
		v := out[id]
		v.Possible = append(v.Possible, a)
		out[id] = v
	}
	return out, rows.Err()
}
func (s *Store) DiscoveryRejections(c context.Context) (map[string]map[int64]bool, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, `SELECT rgid,album_id FROM discovery_match_rejections`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]map[int64]bool{}
	for rows.Next() {
		var id string
		var a int64
		if e = rows.Scan(&id, &a); e != nil {
			return nil, e
		}
		if out[id] == nil {
			out[id] = map[int64]bool{}
		}
		out[id][a] = true
	}
	return out, rows.Err()
}
const listDiscoveryCandidatesSQL = `SELECT ` + discoveryGroupSelect + `,
	COALESCE(d.seed_artist_id,0),COALESCE(a.user_display_name,a.display_name,''),
	d.generation,d.score,d.lane,COALESCE(d.rank,0),d.reasons_json,
	d.seed_kind,COALESCE(d.seed_work_id,0),COALESCE(d.bangumi_subject_id,0),
	COALESCE(w.title,''),COALESCE(sm.series_id,0),
	COALESCE(wm.relation,''),
	CASE WHEN m.status IS NOT NULL AND mc.rgid IS NULL THEN m.status ELSE '' END
	FROM discovery_candidates d
	JOIN discovery_release_groups r ON r.rgid=d.rgid
	LEFT JOIN artists a ON a.id=d.seed_artist_id
	LEFT JOIN works w ON w.id=d.seed_work_id
	LEFT JOIN work_series_members sm ON sm.work_id=w.id
	LEFT JOIN discovery_work_music wm ON wm.work_id=d.seed_work_id AND wm.subject_id=d.bangumi_subject_id
	LEFT JOIN discovery_bangumi_mappings m ON m.subject_id=d.bangumi_subject_id AND m.rgid=d.rgid
	LEFT JOIN discovery_bangumi_mappings mc ON mc.subject_id=m.subject_id AND mc.status='confirmed' AND mc.rgid<>m.rgid
	ORDER BY COALESCE(d.rank,2147483647),d.score DESC,r.rgid`

type rawDiscoveryItem struct {
	item         DiscoveryItem
	secondaryRaw string
	reasonsRaw   string
	optionRaws   []string
}

func (s *Store) CountAcquisitionTasks(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM acquisition_tasks WHERE state IN (`+AcquisitionActiveStates+`) OR (state='IN_LIBRARY' AND finished_at>strftime('%Y-%m-%dT%H:%M:%fZ','now','-7 days'))`).Scan(&n)
	return n, err
}

func (s *Store) ListDiscovery(c context.Context, tab, source string, limit, offset int) ([]DiscoveryItem, DiscoveryCounts, error) {
	var counts DiscoveryCounts
	if e := s.IdentityReady(c); e != nil {
		return nil, counts, e
	}
	if source != "all" && source != "artist" && source != "work" {
		return nil, counts, errors.New("invalid discovery source")
	}
	if tab != "recommended" && tab != "possible" && tab != "ignored" && tab != "acquiring" && tab != "mapping" {
		return nil, counts, errors.New("invalid discovery tab")
	}
	rows, e := s.db.QueryContext(c, listDiscoveryCandidatesSQL)
	if e != nil {
		return nil, counts, e
	}
	all := []rawDiscoveryItem{}
	ids := []string{}
	for rows.Next() {
		var entry rawDiscoveryItem
		v := &entry.item
		e = rows.Scan(&v.RGID, &v.Title, &v.PrimaryType, &entry.secondaryRaw, &v.Kind, &v.FirstReleaseDate, &v.Disambiguation, &v.ArtistCredit, &v.CoverStatus, &v.CoverExt, &v.CoverCheckedAt, &v.SeedArtistID, &v.Artist, &v.Generation, &v.Score, &v.Lane, &v.Rank, &entry.reasonsRaw, &v.SeedKind, &v.SeedWorkID, &v.SubjectID, &v.WorkTitle, &v.SeriesID, &v.Relation, &v.MappingStatus)
		if e != nil {
			rows.Close()
			return nil, counts, e
		}
		if len(v.FirstReleaseDate) >= 4 {
			if y, err := strconv.Atoi(v.FirstReleaseDate[:4]); err == nil && y > 0 {
				v.Year = y
			}
		}
		v.CoverAvailable = v.CoverStatus == "cached"
		all = append(all, entry)
		ids = append(ids, v.RGID)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, counts, e
	}
	mapping, e := s.discoveryMappingCatalogRaw(c, true)
	if e != nil {
		return nil, counts, e
	}
	for _, entry := range mapping {
		ids = append(ids, entry.cand.RGID)
	}
	own, e := s.DiscoveryOwnership(c, ids)
	if e != nil {
		return nil, counts, e
	}
	out := []rawDiscoveryItem{}
	if tab == "acquiring" {
		tasks, e := s.ListAcquisitionTasks(c)
		if e != nil {
			return nil, counts, e
		}
		counts.Acquiring = len(tasks)
		for _, task := range tasks {
			out = append(out, rawDiscoveryItem{item: DiscoveryItem{DiscoveryCandidate: DiscoveryCandidate{DiscoveryReleaseGroup: DiscoveryReleaseGroup{RGID: task.RGID, Title: task.Title, ArtistCredit: task.Artist, Kind: task.Kind}}}})
		}
	} else {
		n, e := s.CountAcquisitionTasks(c)
		if e != nil {
			return nil, counts, e
		}
		counts.Acquiring = n
	}
	metadata, err := s.discoveryMappingMetadata(c)
	if err != nil {
		return nil, counts, err
	}
	for _, entry := range all {
		v := entry.item
		if v.SubjectID > 0 {
			if _, ok := metadata[discoveryMappingKey(v.SubjectID, v.RGID)]; !ok {
				continue
			}
		}
		if v.SeedKind == "work" && v.MappingStatus != "auto" && v.MappingStatus != "confirmed" {
			continue
		}
		o := own[v.RGID]
		if len(o.Albums) > 0 || o.Acquiring {
			continue
		}
		kind := "recommended"
		if o.Ignored {
			kind = "ignored"
			counts.Ignored++
		} else if len(o.Possible) > 0 {
			kind = "possible"
			counts.Possible++
		} else {
			if v.Rank <= 0 {
				continue
			}
			counts.Recommended++
		}
		if kind == tab && (source == "all" || source == v.SeedKind) {
			entry.item.Possible = o.Possible
			out = append(out, entry)
		}
	}
	seenMappings := map[string]bool{}
	perSubject := map[int64]int{}
	groupIndexes := map[string]int{}
	counted := map[string]bool{}
	for _, mEntry := range mapping {
		v := mEntry.cand
		if _, ok := metadata[discoveryMappingKey(v.SubjectID, v.RGID)]; !ok {
			continue
		}
		key := fmt.Sprintf("%d:%s", v.SubjectID, v.RGID)
		if seenMappings[key] {
			continue
		}
		seenMappings[key] = true
		o := own[v.RGID]
		if len(o.Albums) > 0 || o.Acquiring || o.Ignored {
			continue
		}
		kind := "mapping"
		if v.MappingStatus == "rejected" {
			kind = "ignored"

			countKey := fmt.Sprintf("ignored:%d", v.SubjectID)
			if !counted[countKey] {
				counts.Ignored++
				counted[countKey] = true
			}
		} else {
			if perSubject[v.SubjectID] >= 3 {
				continue
			}
			perSubject[v.SubjectID]++

			countKey := fmt.Sprintf("mapping:%d", v.SubjectID)
			if !counted[countKey] {
				counts.Mapping++
				counted[countKey] = true
			}
		}
		if kind == tab && source != "artist" {
			item := DiscoveryItem{DiscoveryCandidate: v, CoverAvailable: v.CoverStatus == "cached"}
			if len(v.FirstReleaseDate) >= 4 {
				item.Year, _ = strconv.Atoi(v.FirstReleaseDate[:4])
			}
			groupKey := fmt.Sprintf("%s:%d", kind, v.SubjectID)
			if index, ok := groupIndexes[groupKey]; ok {
				out[index].item.MappingOptions = append(out[index].item.MappingOptions, item)
				out[index].optionRaws = append(out[index].optionRaws, mEntry.secondaryRaw)
			} else {
				item.MappingOptions = []DiscoveryItem{item}
				groupIndexes[groupKey] = len(out)
				out = append(out, rawDiscoveryItem{
					item:         item,
					secondaryRaw: mEntry.secondaryRaw,
					optionRaws:   []string{mEntry.secondaryRaw},
				})
			}
		}
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	if offset >= len(out) {
		return []DiscoveryItem{}, counts, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	pageRaw := out[offset:end]
	page := make([]DiscoveryItem, 0, len(pageRaw))
	for _, entry := range pageRaw {
		if entry.secondaryRaw != "" {
			if e = json.Unmarshal([]byte(entry.secondaryRaw), &entry.item.SecondaryTypes); e != nil {
				return nil, counts, e
			}
		}
		if entry.reasonsRaw != "" {
			if e = json.Unmarshal([]byte(entry.reasonsRaw), &entry.item.Reasons); e != nil {
				return nil, counts, e
			}
		}
		for i := range entry.item.MappingOptions {
			if i < len(entry.optionRaws) && entry.optionRaws[i] != "" {
				if e = json.Unmarshal([]byte(entry.optionRaws[i]), &entry.item.MappingOptions[i].SecondaryTypes); e != nil {
					return nil, counts, e
				}
			}
		}
		item, ok, e := enrichDiscoveryMappingItem(entry.item, metadata)
		if e != nil {
			return nil, counts, e
		}
		if ok {
			page = append(page, item)
		}
	}
	return page, counts, nil
}
func (s *Store) DiscoveryIgnore(c context.Context, raw string, ignored bool) error {
	if e := s.IdentityReady(c); e != nil {
		return e
	}
	id, e := normalizedDiscoveryID(raw)
	if e != nil {
		return e
	}
	var exists bool
	if e = s.db.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM discovery_release_groups WHERE rgid=?)`, id).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return sql.ErrNoRows
	}
	if !ignored {
		return s.SetReleaseGroupIgnored(c, id, false)
	}
	tx, e := s.beginWrite(c)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var active bool
	if e = tx.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM acquisition_tasks WHERE rgid=? AND state IN (`+AcquisitionActiveStates+`))`, id).Scan(&active); e != nil {
		return e
	}
	if active {
		return ErrAcquisitionRGActive
	}
	if _, e = tx.ExecContext(c, `INSERT OR IGNORE INTO release_group_decisions(rgid,decision) VALUES(?,'ignore')`, id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) DiscoveryNotSame(c context.Context, raw string, album int64) error {
	if e := s.IdentityReady(c); e != nil {
		return e
	}
	id, e := normalizedDiscoveryID(raw)
	if e != nil {
		return e
	}
	t, e := s.beginWrite(c)
	if e != nil {
		return e
	}
	defer t.Rollback()
	res, e := t.ExecContext(c, `INSERT OR IGNORE INTO discovery_match_rejections SELECT ?,? WHERE EXISTS(SELECT 1 FROM discovery_release_groups WHERE rgid=?) AND EXISTS(SELECT 1 FROM albums WHERE id=? AND `+albumVisibleSQL("albums.id")+`)`, id, album, id, album)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var exists bool
		if e = t.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM discovery_match_rejections WHERE rgid=? AND album_id=?)`, id, album).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			return sql.ErrNoRows
		}
	}
	if _, e = t.ExecContext(c, `DELETE FROM discovery_possible_matches WHERE rgid=? AND album_id=?`, id, album); e != nil {
		return e
	}
	return t.Commit()
}
func (s *Store) DiscoveryOwned(c context.Context, raw string, album int64, confirmReplace bool) error {
	if e := s.IdentityReady(c); e != nil {
		return e
	}
	id, e := normalizedDiscoveryID(raw)
	if e != nil {
		return e
	}
	t, e := s.beginWrite(c)
	if e != nil {
		return e
	}
	defer t.Rollback()
	var active bool
	if e = t.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM acquisition_tasks WHERE rgid=? AND state IN (`+AcquisitionActiveStates+`))`, id).Scan(&active); e != nil {
		return e
	}
	if active {
		return ErrAcquisitionRGActive
	}
	var exists bool
	if e = t.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM albums a WHERE a.id=? AND `+albumVisibleSQL("a.id")+`) AND EXISTS(SELECT 1 FROM discovery_release_groups WHERE rgid=?)`, album, id).Scan(&exists); e != nil {
		return e
	}
	if !exists {
		return sql.ErrNoRows
	}
	var current string
	e = t.QueryRowContext(c, `SELECT rgid FROM album_release_groups WHERE album_id=? AND status='resolved'`, album).Scan(&current)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if current != "" && current != id && !confirmReplace {
		return &ErrReplacesReleaseGroup{current}
	}
	if _, e = t.ExecContext(c, `INSERT OR IGNORE INTO release_groups(rgid) VALUES(?)`, id); e != nil {
		return e
	}
	if _, e = t.ExecContext(c, `DELETE FROM album_release_groups WHERE album_id=? AND source='manual'`, album); e != nil {
		return e
	}
	if _, e = t.ExecContext(c, `INSERT INTO album_release_groups VALUES(?,?,'manual','observed')`, album, id); e != nil {
		return e
	}
	if e = resolveAlbumReleaseGroups(c, t, album); e != nil {
		return e
	}
	if _, e = t.ExecContext(c, `DELETE FROM discovery_possible_matches WHERE rgid=?`, id); e != nil {
		return e
	}
	return t.Commit()
}
const discoveryArtistPlays90dSQL = artistMergeChainSQL + `, recent AS MATERIALIZED (SELECT session_id,track_id,counted_at FROM playback_sessions WHERE counted_at IS NOT NULL AND counted_at>=strftime('%Y-%m-%dT%H:%M:%fZ','now','-90 days')) SELECT DISTINCT p.session_id,a.root,julianday('now')-julianday(p.counted_at) FROM recent p JOIN track_artists ta ON ta.track_id=p.track_id AND ta.role='primary' JOIN chain a ON a.id=ta.artist_id`

func (s *Store) DiscoveryArtistSignals(c context.Context) (map[int64]DiscoverySignals, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	out := map[int64]DiscoverySignals{}
	rows, e := s.db.QueryContext(c, artistMergeChainSQL+` SELECT chain.root,MAX(a.is_favorite) FROM artists a JOIN chain ON chain.id=a.id GROUP BY 1`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id int64
		var v DiscoverySignals
		if e = rows.Scan(&id, &v.FavArtist); e != nil {
			rows.Close()
			return nil, e
		}
		out[id] = v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	queries := []string{
		artistMergeChainSQL + ` SELECT a.root,count(DISTINCT al.id),count(DISTINCT CASE WHEN al.is_favorite=1 THEN al.id END) FROM album_artists aa JOIN chain a ON a.id=aa.artist_id JOIN albums al ON al.id=aa.album_id WHERE ` + albumVisibleSQL("al.id") + ` GROUP BY 1`,
		artistMergeChainSQL + ` SELECT a.root,COALESCE(SUM(p.play_count),0),count(DISTINCT CASE WHEN t.is_favorite=1 THEN t.id END) FROM (SELECT DISTINCT track_id,artist_id FROM track_artists WHERE role='primary') ta JOIN chain a ON a.id=ta.artist_id JOIN tracks t ON t.id=ta.track_id LEFT JOIN playback_progress p ON p.track_id=t.id GROUP BY 1`}
	for i, q := range queries {
		rr, e := s.db.QueryContext(c, q)
		if e != nil {
			return nil, e
		}
		for rr.Next() {
			var id int64
			var n, f int
			if e = rr.Scan(&id, &n, &f); e != nil {
				rr.Close()
				return nil, e
			}
			v := out[id]
			if i == 0 {
				v.OwnedAlbums = n
				v.FavAlbums = f
			} else {
				v.PlaysAll = n
				v.FavTracks = f
			}
			out[id] = v
		}
		e = rr.Err()
		rr.Close()
		if e != nil {
			return nil, e
		}
	}
	rows, e = s.db.QueryContext(c, discoveryArtistPlays90dSQL)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var session string
		var id int64
		var days float64
		if e = rows.Scan(&session, &id, &days); e != nil {
			return nil, e
		}
		if days < 0 {
			days = 0
		}
		v := out[id]
		v.Plays90d += math.Pow(0.5, days/30)
		out[id] = v
	}
	return out, rows.Err()
}

// Promote the first surviving option if a concurrent refresh retired the head.
func enrichDiscoveryMappingItem(item DiscoveryItem, metadata map[string]discoveryMappingDisplay) (DiscoveryItem, bool, error) {
	if item.SubjectID == 0 {
		return item, true, nil
	}
	if len(item.MappingOptions) > 0 {
		options := []DiscoveryItem{}
		for _, option := range item.MappingOptions {
			if info, ok := metadata[discoveryMappingKey(option.SubjectID, option.RGID)]; ok {
				ev, err := info.decodeEvidence()
				if err != nil {
					return DiscoveryItem{}, false, err
				}
				option.SubjectName = info.name
				option.SubjectNameCN = info.nameCN
				option.MappingEvidence = ev
				option.MappingScore = info.score
				options = append(options, option)
			}
		}
		if len(options) == 0 {
			return DiscoveryItem{}, false, nil
		}
		item = options[0]
		item.MappingOptions = options
		return item, true, nil
	}
	info, ok := metadata[discoveryMappingKey(item.SubjectID, item.RGID)]
	if !ok {
		return DiscoveryItem{}, false, nil
	}
	ev, err := info.decodeEvidence()
	if err != nil {
		return DiscoveryItem{}, false, err
	}
	item.SubjectName = info.name
	item.SubjectNameCN = info.nameCN
	item.MappingEvidence = ev
	item.MappingScore = info.score
	return item, true, nil
}
