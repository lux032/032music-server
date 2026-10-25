package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"
)

var ErrMappingUnconfirmed = errors.New("mapping unconfirmed")
var ErrMappingSubjectConfirmed = errors.New("mapping subject already confirmed")

type DiscoveryWorkSeed struct {
	WorkID, SubjectID, SeriesID, Generation int64
	WorkTitle, Status                       string
}
type DiscoveryMusic struct {
	SubjectID                                  int64
	Name, NameCN, Date                         string
	Artists                                    []string
	SearchUpdated                              bool
	MultiTitle                                 bool
	Status, SearchStatus, Relation, SearchedAt string
}
type DiscoveryMapping struct {
	SubjectID    int64
	RGID, Status string
	Score        float64
	Evidence     []string
}

func discoverySeedKind(v DiscoveryCandidate) string {
	if v.SeedWorkID != 0 {
		return "work"
	}
	return "artist"
}
func nullableDiscoveryID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
func workSeedCount(seeds [][]DiscoveryWorkSeed) int {
	n := 0
	for _, v := range seeds {
		n += len(v)
	}
	return n
}
func discoveryWorkGuard(c context.Context, t *sql.Tx, g int64, seeds []DiscoveryWorkSeed) error {
	if e := discoveryGuard(c, t, g, nil); e != nil {
		return e
	}
	for _, v := range seeds {
		var ok bool
		if e := t.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM works w JOIN work_external_profiles p ON p.work_id=w.id AND p.source='bangumi' WHERE w.id=? AND p.external_id=?)`, v.WorkID, strconv.FormatInt(v.SubjectID, 10)).Scan(&ok); e != nil {
			return e
		}
		if !ok {
			return ErrDiscoveryStale
		}
	}
	return nil
}
func beginDiscoveryWorks(c context.Context, t *sql.Tx, g int64, seeds [][]DiscoveryWorkSeed, force bool, now time.Time) error {
	ids := []int64{}
	for _, list := range seeds {
		for _, v := range list {
			ids = append(ids, v.WorkID)
			if _, e := t.ExecContext(c, `INSERT INTO discovery_work_seeds(work_id,subject_id,generation,status) VALUES(?,?,?,'pending') ON CONFLICT(work_id) DO UPDATE SET status=CASE WHEN ?=0 AND subject_id=excluded.subject_id AND status='done' AND fetched_at>? AND NOT EXISTS(SELECT 1 FROM discovery_work_music wm JOIN discovery_bangumi_music m USING(subject_id) WHERE wm.work_id=excluded.work_id AND m.search_status='pending') THEN 'done' ELSE 'pending' END,subject_id=excluded.subject_id,generation=excluded.generation`, v.WorkID, v.SubjectID, g, force, now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339)); e != nil {
				return e
			}
		}
	}
	raw, _ := json.Marshal(ids)
	for _, q := range []string{`DELETE FROM discovery_work_seeds WHERE work_id NOT IN (SELECT value FROM json_each(?))`, `DELETE FROM discovery_work_music WHERE work_id NOT IN (SELECT value FROM json_each(?))`} {
		if _, e := t.ExecContext(c, q, string(raw)); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) DiscoveryWorkSeedSnapshot(c context.Context) ([]DiscoveryWorkSeed, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, `SELECT w.id,w.title,p.external_id,COALESCE(m.series_id,0) FROM works w JOIN work_external_profiles p ON p.work_id=w.id AND p.source='bangumi' LEFT JOIN work_series_members m ON m.work_id=w.id WHERE EXISTS(SELECT 1 FROM work_tracks wt WHERE wt.work_id=w.id AND `+trackVisibleSQL("wt.track_id")+`) OR EXISTS(SELECT 1 FROM album_works aw JOIN tracks t ON t.album_id=aw.album_id WHERE aw.work_id=w.id AND `+trackVisibleSQL("t.id")+`) ORDER BY w.id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DiscoveryWorkSeed{}
	for rows.Next() {
		var v DiscoveryWorkSeed
		var raw string
		if e = rows.Scan(&v.WorkID, &v.WorkTitle, &raw, &v.SeriesID); e != nil {
			return nil, e
		}
		v.SubjectID, e = strconv.ParseInt(raw, 10, 64)
		if e == nil && v.SubjectID > 0 {
			out = append(out, v)
		}
	}
	return out, rows.Err()
}
func (s *Store) DiscoveryWorkSeeds(c context.Context, g int64) ([]DiscoveryWorkSeed, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, `SELECT d.work_id,d.subject_id,d.generation,d.status,w.title,COALESCE(m.series_id,0) FROM discovery_work_seeds d JOIN works w ON w.id=d.work_id LEFT JOIN work_series_members m ON m.work_id=w.id WHERE generation=? ORDER BY d.work_id`, g)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DiscoveryWorkSeed{}
	for rows.Next() {
		var v DiscoveryWorkSeed
		if e = rows.Scan(&v.WorkID, &v.SubjectID, &v.Generation, &v.Status, &v.WorkTitle, &v.SeriesID); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) DiscoveryMusicSearchState(c context.Context, id int64) (DiscoveryMusic, bool, error) {
	var v DiscoveryMusic
	if e := s.IdentityReady(c); e != nil {
		return v, false, e
	}
	var artists string
	var decided bool
	e := s.db.QueryRowContext(c, `SELECT subject_id,name,name_cn,date,artists_json,multi_title,status,search_status,COALESCE(searched_at,''),EXISTS(SELECT 1 FROM discovery_bangumi_mappings WHERE subject_id=? AND status IN ('confirmed','rejected')) FROM discovery_bangumi_music WHERE subject_id=?`, id, id).Scan(&v.SubjectID, &v.Name, &v.NameCN, &v.Date, &artists, &v.MultiTitle, &v.Status, &v.SearchStatus, &v.SearchedAt, &decided)
	if errors.Is(e, sql.ErrNoRows) {
		return v, false, nil
	}
	if e == nil {
		e = json.Unmarshal([]byte(artists), &v.Artists)
	}
	return v, decided, e
}
func (s *Store) DiscoveryLocalBangumiMusic(c context.Context) (map[int64]bool, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, `SELECT CAST(external_id AS INTEGER) FROM album_subject_candidates a WHERE status='confirmed' AND `+albumVisibleSQL("a.album_id"))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out[id] = true
	}
	return out, rows.Err()
}
func (s *Store) SaveDiscoveryWorkSeed(c context.Context, g int64, seed DiscoveryWorkSeed, music []DiscoveryMusic, groups []DiscoveryReleaseGroup, mappings []DiscoveryMapping, status, msg string, now time.Time) error {
	if e := s.IdentityReady(c); e != nil {
		return e
	}
	t, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	if e = discoveryLock(c, t); e != nil {
		return e
	}
	if e = discoveryWorkGuard(c, t, g, []DiscoveryWorkSeed{seed}); e != nil {
		return e
	}
	if _, e = t.ExecContext(c, `DELETE FROM discovery_work_music WHERE work_id=?`, seed.WorkID); e != nil {
		return e
	}
	for i, v := range music {
		a, _ := json.Marshal(v.Artists)
		if v.Status == "" {
			v.Status = "ok"
		}
		if v.SearchStatus == "" {
			v.SearchStatus = "pending"
		}
		if _, e = t.ExecContext(c, `INSERT INTO discovery_bangumi_music(subject_id,name,name_cn,date,artists_json,multi_title,status,search_status,fetched_at,searched_at) VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,'')) ON CONFLICT(subject_id) DO UPDATE SET name=excluded.name,name_cn=excluded.name_cn,date=excluded.date,artists_json=excluded.artists_json,multi_title=excluded.multi_title,status=excluded.status,search_status=excluded.search_status,fetched_at=excluded.fetched_at,searched_at=excluded.searched_at`, v.SubjectID, v.Name, v.NameCN, v.Date, string(a), v.MultiTitle, v.Status, v.SearchStatus, now.UTC().Format(time.RFC3339), v.SearchedAt); e != nil {
			return e
		}
		if _, e = t.ExecContext(c, `INSERT INTO discovery_work_music(work_id,subject_id,relation,ordinal) VALUES(?,?,?,?)`, seed.WorkID, v.SubjectID, v.Relation, i); e != nil {
			return e
		}
	}
	retired := []string{}
	for _, v := range music {
		if v.SearchUpdated && v.SearchStatus != "pending" && v.SearchStatus != "failed" {
			rows, err := t.QueryContext(c, `SELECT rgid FROM discovery_bangumi_mappings WHERE subject_id=? AND status IN ('auto','suggested') AND NOT EXISTS(SELECT 1 FROM acquisition_tasks a WHERE a.rgid=discovery_bangumi_mappings.rgid) AND NOT EXISTS(SELECT 1 FROM acquisition_searches a WHERE a.rgid=discovery_bangumi_mappings.rgid)`, v.SubjectID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				retired = append(retired, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			if _, e = t.ExecContext(c, `DELETE FROM discovery_bangumi_mappings WHERE subject_id=? AND status IN ('auto','suggested') AND NOT EXISTS(SELECT 1 FROM acquisition_tasks a WHERE a.rgid=discovery_bangumi_mappings.rgid) AND NOT EXISTS(SELECT 1 FROM acquisition_searches a WHERE a.rgid=discovery_bangumi_mappings.rgid)`, v.SubjectID); e != nil {
				return e
			}
		}
	}
	for _, v := range groups {
		a, _ := json.Marshal(v.SecondaryTypes)
		if _, e = t.ExecContext(c, `INSERT INTO discovery_release_groups(rgid,title,primary_type,secondary_types_json,kind,first_release_date,disambiguation,artist_credit,fetched_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(rgid) DO UPDATE SET title=excluded.title,artist_credit=excluded.artist_credit,kind=excluded.kind,first_release_date=excluded.first_release_date`, v.RGID, v.Title, v.PrimaryType, string(a), v.Kind, v.FirstReleaseDate, v.Disambiguation, v.ArtistCredit, now.UTC().Format(time.RFC3339)); e != nil {
			return e
		}
	}
	autoSubjects := map[string]map[int64]bool{}
	for _, v := range mappings {
		if v.Status == "auto" {
			if autoSubjects[v.RGID] == nil {
				autoSubjects[v.RGID] = map[int64]bool{}
			}
			autoSubjects[v.RGID][v.SubjectID] = true
		}
	}
	for i := range mappings {
		v := &mappings[i]
		if v.Status == "auto" && len(autoSubjects[v.RGID]) > 1 {
			v.Status = "suggested"
			v.Evidence = append(v.Evidence, "rg_conflict")
		}
	}
	for i := range mappings {
		v := &mappings[i]
		if v.Status == "auto" {
			var conflict bool
			if e = t.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM discovery_bangumi_mappings WHERE rgid=? AND subject_id<>? AND status='auto')`, v.RGID, v.SubjectID).Scan(&conflict); e != nil {
				return e
			}
			if conflict {
				v.Status = "suggested"
				v.Evidence = append(v.Evidence, "rg_conflict")
				if _, e = t.ExecContext(c, `UPDATE discovery_bangumi_mappings SET status='suggested',evidence_json=json_insert(CASE WHEN json_type(evidence_json)='array' THEN evidence_json ELSE '[]' END,'$[#]','rg_conflict') WHERE rgid=? AND status='auto'`, v.RGID); e != nil {
					return e
				}
			}
		}
	}
	for _, v := range mappings {
		a, _ := json.Marshal(v.Evidence)
		if _, e = t.ExecContext(c, `INSERT INTO discovery_bangumi_mappings(subject_id,rgid,status,score,evidence_json) SELECT ?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM discovery_bangumi_mappings WHERE subject_id=? AND status='confirmed' AND rgid<>?) ON CONFLICT(subject_id,rgid) DO UPDATE SET status=excluded.status,score=excluded.score,evidence_json=excluded.evidence_json,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE discovery_bangumi_mappings.status IN ('auto','suggested')`, v.SubjectID, v.RGID, v.Status, v.Score, string(a), v.SubjectID, v.RGID); e != nil {
			return e
		}
	}
	for _, id := range retired {
		if _, e = t.ExecContext(c, `DELETE FROM discovery_release_groups WHERE rgid=? AND NOT EXISTS(SELECT 1 FROM discovery_bangumi_mappings m WHERE m.rgid=discovery_release_groups.rgid) AND NOT EXISTS(SELECT 1 FROM discovery_artist_release_groups a WHERE a.rgid=discovery_release_groups.rgid) AND NOT EXISTS(SELECT 1 FROM acquisition_tasks a WHERE a.rgid=discovery_release_groups.rgid) AND NOT EXISTS(SELECT 1 FROM acquisition_searches a WHERE a.rgid=discovery_release_groups.rgid) AND NOT EXISTS(SELECT 1 FROM release_group_decisions d WHERE d.rgid=discovery_release_groups.rgid) AND NOT EXISTS(SELECT 1 FROM discovery_match_rejections x WHERE x.rgid=discovery_release_groups.rgid)`, id); e != nil {
			return e
		}
	}
	if _, e = t.ExecContext(c, `UPDATE discovery_work_seeds SET status=?,music_count=?,error_message=?,fetched_at=? WHERE work_id=? AND generation=?`, status, len(music), msg, now.UTC().Format(time.RFC3339), seed.WorkID, g); e != nil {
		return e
	}
	if _, e = t.ExecContext(c, `UPDATE discovery_refresh_state SET processed=processed+1 WHERE id=1 AND status='running'`); e != nil {
		return e
	}
	return t.Commit()
}
func (s *Store) DiscoveryMappingDecision(c context.Context, subjectID int64, rgid, decision string) error {
	if e := s.IdentityReady(c); e != nil {
		return e
	}
	id, e := normalizedDiscoveryID(rgid)
	if e != nil {
		return e
	}
	status := map[string]string{"confirm": "confirmed", "reject": "rejected", "restore": "suggested"}[decision]
	if subjectID <= 0 || status == "" {
		return errors.New("invalid mapping decision")
	}
	t, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	if e = discoveryLock(c, t); e != nil {
		return e
	}
	if decision == "confirm" {
		var exists bool
		if e = t.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM discovery_bangumi_mappings WHERE subject_id=? AND status='confirmed' AND rgid<>?)`, subjectID, id).Scan(&exists); e != nil {
			return e
		}
		if exists {
			return ErrMappingSubjectConfirmed
		}
	}
	res, e := t.ExecContext(c, `UPDATE discovery_bangumi_mappings SET status=?,decided_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE subject_id=? AND rgid=? AND (?<>'restore' OR status='rejected')`, status, subjectID, id, decision)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return t.Commit()
}
func (s *Store) DiscoveryWorkCatalog(c context.Context) ([]DiscoveryCandidate, error) {
	return s.discoveryMappingCatalog(c, false)
}
func (s *Store) discoveryMappingCatalog(c context.Context, suggestions bool) ([]DiscoveryCandidate, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	filter := `m.status IN ('auto','confirmed')`
	if suggestions {
		filter = `m.status IN ('suggested','rejected')`
	}
	rows, e := s.db.QueryContext(c, `SELECT `+discoveryGroupSelect+`,wm.work_id,wm.subject_id,w.title,wm.relation,m.status,COALESCE(sm.series_id,0),m.score FROM discovery_bangumi_mappings m JOIN discovery_release_groups r ON r.rgid=m.rgid JOIN discovery_work_music wm ON wm.subject_id=m.subject_id LEFT JOIN works w ON w.id=wm.work_id LEFT JOIN work_series_members sm ON sm.work_id=w.id WHERE `+filter+` AND NOT EXISTS(SELECT 1 FROM discovery_bangumi_mappings x WHERE x.subject_id=m.subject_id AND x.status='confirmed' AND x.rgid<>m.rgid) AND NOT EXISTS(SELECT 1 FROM album_subject_candidates ac WHERE ac.status='confirmed' AND ac.external_id=CAST(m.subject_id AS TEXT) AND `+albumVisibleSQL("ac.album_id")+`) ORDER BY m.subject_id,m.score DESC,r.rgid,wm.work_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DiscoveryCandidate{}
	for rows.Next() {
		v := DiscoveryCandidate{SeedKind: "work"}
		var raw string
		if e = rows.Scan(&v.RGID, &v.Title, &v.PrimaryType, &raw, &v.Kind, &v.FirstReleaseDate, &v.Disambiguation, &v.ArtistCredit, &v.CoverStatus, &v.CoverExt, &v.CoverCheckedAt, &v.SeedWorkID, &v.SubjectID, &v.WorkTitle, &v.Relation, &v.MappingStatus, &v.SeriesID, &v.Score); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &v.SecondaryTypes); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// The union counts each track once even when it has both album and track links.
const discoveryWorkTracksSQL = `WITH wt AS (SELECT work_id,track_id FROM work_tracks UNION SELECT aw.work_id,t.id FROM album_works aw JOIN tracks t ON t.album_id=aw.album_id)`

func (s *Store) DiscoveryWorkSignals(c context.Context) (map[int64]DiscoverySignals, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	out := map[int64]DiscoverySignals{}
	seriesRows, e := s.db.QueryContext(c, `SELECT w.id,COALESCE(m.series_id,0) FROM works w LEFT JOIN work_series_members m ON m.work_id=w.id`)
	if e != nil {
		return nil, e
	}
	for seriesRows.Next() {
		var id int64
		var v DiscoverySignals
		if e = seriesRows.Scan(&id, &v.SeriesID); e != nil {
			seriesRows.Close()
			return nil, e
		}
		out[id] = v
	}
	e = seriesRows.Err()
	seriesRows.Close()
	if e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, discoveryWorkTracksSQL+` SELECT wt.work_id,COALESCE(SUM(p.play_count),0),count(DISTINCT CASE WHEN t.is_favorite=1 THEN t.id END),count(DISTINCT t.album_id),count(DISTINCT CASE WHEN a.is_favorite=1 THEN a.id END) FROM wt JOIN tracks t ON t.id=wt.track_id JOIN albums a ON a.id=t.album_id LEFT JOIN playback_progress p ON p.track_id=t.id WHERE `+trackVisibleSQL("t.id")+` GROUP BY wt.work_id`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id int64
		var v DiscoverySignals
		if e = rows.Scan(&id, &v.PlaysAll, &v.FavTracks, &v.OwnedAlbums, &v.FavAlbums); e != nil {
			rows.Close()
			return nil, e
		}
		v.SeriesID = out[id].SeriesID
		out[id] = v
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	rows, e = s.db.QueryContext(c, discoveryWorkTracksSQL+` SELECT DISTINCT p.session_id,wt.work_id,julianday('now')-julianday(p.counted_at) FROM playback_sessions p JOIN wt ON wt.track_id=p.track_id WHERE `+trackVisibleSQL("wt.track_id")+` AND p.counted_at IS NOT NULL AND julianday(p.counted_at)>=julianday('now','-90 days')`)
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
		v.Plays90d += math.Pow(.5, days/30)
		out[id] = v
	}
	return out, rows.Err()
}

func (s *Store) DiscoveryMessage(c context.Context, g int64, message string) error {
	if e := s.IdentityReady(c); e != nil {
		return e
	}
	t, e := s.db.BeginTx(c, nil)
	if e != nil {
		return e
	}
	defer t.Rollback()
	if e = discoveryLock(c, t); e != nil {
		return e
	}
	if e = discoveryGuard(c, t, g, nil); e != nil {
		return e
	}
	if _, e = t.ExecContext(c, `UPDATE discovery_refresh_state SET error_message=? WHERE id=1`, message); e != nil {
		return e
	}
	return t.Commit()
}

type discoveryMappingDisplay struct {
	name, nameCN string
	evidence     []string
	score        float64
}

func discoveryMappingKey(subject int64, rgid string) string {
	return strconv.FormatInt(subject, 10) + ":" + rgid
}

// Read display metadata in one query; missing mappings are skipped by ListDiscovery.
func (s *Store) discoveryMappingMetadata(c context.Context) (map[string]discoveryMappingDisplay, error) {
	rows, e := s.db.QueryContext(c, `SELECT m.subject_id,m.rgid,b.name,b.name_cn,m.score,m.evidence_json FROM discovery_bangumi_mappings m JOIN discovery_bangumi_music b ON b.subject_id=m.subject_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := map[string]discoveryMappingDisplay{}
	for rows.Next() {
		var subject int64
		var rgid, raw string
		var v discoveryMappingDisplay
		if e = rows.Scan(&subject, &rgid, &v.name, &v.nameCN, &v.score, &raw); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &v.evidence); e != nil {
			return nil, e
		}
		out[discoveryMappingKey(subject, rgid)] = v
	}
	return out, rows.Err()
}

// DiscoveryHasPendingSeeds reports whether the last round left artist or work
// seeds (or work music searches) unfinished, so a refresh should resume.
func (s *Store) DiscoveryHasPendingSeeds(c context.Context) (bool, error) {
	if e := s.IdentityReady(c); e != nil {
		return false, e
	}
	var pending bool
	e := s.db.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM discovery_seeds WHERE status='pending') OR EXISTS(SELECT 1 FROM discovery_work_seeds WHERE status='pending') OR EXISTS(SELECT 1 FROM discovery_bangumi_music m JOIN discovery_work_music wm USING(subject_id) WHERE m.search_status='pending')`).Scan(&pending)
	return pending, e
}
