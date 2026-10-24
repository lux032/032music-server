package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lux032/032music-server/internal/metadata"
	"time"
)

var ErrDiscoveryStale = errors.New("discovery generation or identity changed")

type ErrReplacesReleaseGroup struct{ RGID string }

func (e *ErrReplacesReleaseGroup) Error() string { return "replaces release group " + e.RGID }

type DiscoveryStatus struct {
	Generation                          int64
	Status                              string
	Total, Processed                    int
	ErrorMessage, StartedAt, FinishedAt string
}
type DiscoverySeed struct {
	ArtistID             int64
	Artist, MBID, Status string
	Generation           int64
}
type DiscoverySignals struct {
	SeriesID                                    int64
	Plays90d                                    float64
	PlaysAll, FavAlbums, FavTracks, OwnedAlbums int
	FavArtist                                   bool
}
type DiscoveryReason struct {
	Work     string `json:"work,omitempty"`
	Relation string `json:"relation,omitempty"`
	Code     string `json:"code"`
	Artist   string `json:"artist,omitempty"`
	N        int    `json:"n,omitempty"`
	Year     int    `json:"year,omitempty"`
}
type DiscoveryReleaseGroup struct {
	RGID, Title, PrimaryType, Kind, FirstReleaseDate, Disambiguation, ArtistCredit string
	SecondaryTypes                                                                 []string
	CoverStatus, CoverExt, CoverCheckedAt                                          string
}
type DiscoveryCandidate struct {
	DiscoveryReleaseGroup
	SubjectName, SubjectNameCN         string
	MappingEvidence                    []string
	MappingScore                       float64
	SeedKind                           string
	SeedWorkID, SubjectID, SeriesID    int64
	WorkTitle, Relation, MappingStatus string
	SeedArtistID, Generation           int64
	Artist                             string
	Score                              float64
	Lane                               string
	Rank                               int
	Reasons                            []DiscoveryReason
}
type DiscoveryLocalAlbum struct {
	ID                int64
	Title             string
	Year, TrackCount  int
	CurrentRG, Reason string
	ArtistIDs         []int64
	WorkIDs           []int64
}
type DiscoveryMatch struct {
	RGID    string
	AlbumID int64
	Reason  string
}
type Ownership struct {
	Acquiring bool
	Albums    []int64
	Ignored   bool
	Possible  []DiscoveryLocalAlbum
}
type DiscoveryItem struct {
	MappingOptions []DiscoveryItem
	DiscoveryCandidate
	Year           int
	CoverAvailable bool
	Possible       []DiscoveryLocalAlbum
}
type DiscoveryCounts struct{ Recommended, Possible, Acquiring, Ignored, Mapping int }

func (s *Store) IdentityReady(c context.Context) error { return s.requireIdentityReady(c) }
func (s *Store) DiscoveryStatus(c context.Context) (DiscoveryStatus, error) {
	var v DiscoveryStatus
	e := s.db.QueryRowContext(c, `SELECT generation,status,total,processed,error_message,COALESCE(started_at,''),COALESCE(finished_at,'') FROM discovery_refresh_state WHERE id=1`).Scan(&v.Generation, &v.Status, &v.Total, &v.Processed, &v.ErrorMessage, &v.StartedAt, &v.FinishedAt)
	return v, e
}
func (s *Store) DiscoverySeedSnapshot(c context.Context) ([]DiscoverySeed, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, artistMergeChainSQL+`, identities AS (
 SELECT chain.root,p.external_id,ROW_NUMBER() OVER(PARTITION BY chain.root ORDER BY (p.artist_id=chain.root) DESC,p.artist_id) AS priority
 FROM chain JOIN artist_external_profiles p ON p.artist_id=chain.id AND p.source='musicbrainz')
 SELECT a.id,COALESCE(a.user_display_name,a.display_name),i.external_id FROM artists a JOIN identities i ON i.root=a.id AND i.priority=1 WHERE a.merged_into_artist_id IS NULL AND `+artistBiographyPerformerPredicate+` ORDER BY a.id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DiscoverySeed{}
	for rows.Next() {
		var v DiscoverySeed
		if e = rows.Scan(&v.ArtistID, &v.Artist, &v.MBID); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func discoveryLock(c context.Context, t *sql.Tx) error {
	_, e := t.ExecContext(c, `UPDATE discovery_refresh_state SET generation=generation WHERE 0`)
	return e
}
func discoveryGuard(c context.Context, t *sql.Tx, g int64, seeds []DiscoverySeed) error {
	var current int64
	var status string
	if e := t.QueryRowContext(c, `SELECT generation,status FROM discovery_refresh_state WHERE id=1`).Scan(&current, &status); e != nil {
		return e
	}
	if current != g || status != "running" && status != "completed" {
		return ErrDiscoveryStale
	}
	for _, v := range seeds {
		var owner int64
		var mbid string
		e := t.QueryRowContext(c, artistConfirmedIdentitySQL, v.ArtistID, v.ArtistID, v.ArtistID).Scan(&owner, &mbid)
		if errors.Is(e, sql.ErrNoRows) || e == nil && mbid != v.MBID {
			return ErrDiscoveryStale
		}
		if e != nil {
			return e
		}
		var canonical bool
		if e = t.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM artists WHERE id=? AND merged_into_artist_id IS NULL)`, v.ArtistID).Scan(&canonical); e != nil {
			return e
		}
		if !canonical {
			return ErrDiscoveryStale
		}
	}
	return nil
}
func (s *Store) BeginDiscoveryRefresh(c context.Context, seeds []DiscoverySeed, force bool, now time.Time, workSeeds ...[]DiscoveryWorkSeed) (int64, error) {
	if e := s.IdentityReady(c); e != nil {
		return 0, e
	}
	t, e := s.db.BeginTx(c, nil)
	if e != nil {
		return 0, e
	}
	defer t.Rollback()
	if e = discoveryLock(c, t); e != nil {
		return 0, e
	}
	var g int64
	if e = t.QueryRowContext(c, `UPDATE discovery_refresh_state SET generation=generation+1,status='running',total=?,processed=0,error_message='',started_at=?,finished_at=NULL WHERE id=1 RETURNING generation`, len(seeds)+workSeedCount(workSeeds), now.UTC().Format(time.RFC3339)).Scan(&g); e != nil {
		return 0, e
	}
	ids := []int64{}
	for _, v := range seeds {
		ids = append(ids, v.ArtistID)
		_, e = t.ExecContext(c, `INSERT INTO discovery_seeds(artist_id,mbid,generation,status) VALUES(?,?,?,'pending') ON CONFLICT(artist_id) DO UPDATE SET status=CASE WHEN ?=0 AND mbid=excluded.mbid AND status='done' AND fetched_at>? THEN 'done' ELSE 'pending' END,mbid=excluded.mbid,generation=excluded.generation`, v.ArtistID, v.MBID, g, force, now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339))
		if e != nil {
			return 0, e
		}
	}
	for _, ws := range workSeeds {
		if e = discoveryWorkGuard(c, t, g, ws); e != nil {
			return 0, e
		}
	}
	if e = beginDiscoveryWorks(c, t, g, workSeeds, force, now); e != nil {
		return 0, e
	}
	raw, _ := json.Marshal(ids)
	for _, q := range []string{`DELETE FROM discovery_artist_release_groups WHERE artist_id NOT IN (SELECT value FROM json_each(?))`, `DELETE FROM discovery_seeds WHERE artist_id NOT IN (SELECT value FROM json_each(?))`} {
		if _, e = t.ExecContext(c, q, string(raw)); e != nil {
			return 0, e
		}
	}
	_, e = t.ExecContext(c, `UPDATE discovery_refresh_state SET processed=(SELECT count(*) FROM discovery_seeds WHERE generation=? AND status='done')+(SELECT count(*) FROM discovery_work_seeds WHERE generation=? AND status='done') WHERE id=1`, g, g)
	if e != nil {
		return 0, e
	}
	return g, t.Commit()
}
func (s *Store) DiscoverySeeds(c context.Context, g int64) ([]DiscoverySeed, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, `SELECT d.artist_id,COALESCE(a.user_display_name,a.display_name),d.mbid,d.status,d.generation FROM discovery_seeds d JOIN artists a ON a.id=d.artist_id WHERE generation=? ORDER BY artist_id`, g)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DiscoverySeed{}
	for rows.Next() {
		var v DiscoverySeed
		if e = rows.Scan(&v.ArtistID, &v.Artist, &v.MBID, &v.Status, &v.Generation); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SaveDiscoverySeed(c context.Context, g int64, seed DiscoverySeed, groups []DiscoveryReleaseGroup, status, message string, now time.Time) error {
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
	if e = discoveryGuard(c, t, g, []DiscoverySeed{seed}); e != nil {
		return e
	}
	if _, e = t.ExecContext(c, `DELETE FROM discovery_artist_release_groups WHERE artist_id=?`, seed.ArtistID); e != nil {
		return e
	}
	for _, v := range groups {
		raw, _ := json.Marshal(v.SecondaryTypes)
		_, e = t.ExecContext(c, `INSERT INTO discovery_release_groups(rgid,title,primary_type,secondary_types_json,kind,first_release_date,disambiguation,artist_credit,fetched_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(rgid) DO UPDATE SET title=excluded.title,primary_type=excluded.primary_type,secondary_types_json=excluded.secondary_types_json,kind=excluded.kind,first_release_date=excluded.first_release_date,disambiguation=excluded.disambiguation,artist_credit=excluded.artist_credit,fetched_at=excluded.fetched_at`, v.RGID, v.Title, v.PrimaryType, string(raw), v.Kind, v.FirstReleaseDate, v.Disambiguation, v.ArtistCredit, now.UTC().Format(time.RFC3339))
		if e != nil {
			return e
		}
		if _, e = t.ExecContext(c, `INSERT INTO discovery_artist_release_groups VALUES(?,?)`, seed.ArtistID, v.RGID); e != nil {
			return e
		}
	}
	if _, e = t.ExecContext(c, `UPDATE discovery_seeds SET status=?,rg_count=?,error_message=?,fetched_at=? WHERE artist_id=? AND generation=?`, status, len(groups), message, now.UTC().Format(time.RFC3339), seed.ArtistID, g); e != nil {
		return e
	}
	_, e = t.ExecContext(c, `UPDATE discovery_refresh_state SET processed=processed+1 WHERE id=1 AND status='running'`)
	if e != nil {
		return e
	}
	return t.Commit()
}
func (s *Store) FinishDiscovery(c context.Context, g int64, status, message string) error {
	_, e := s.db.ExecContext(c, `UPDATE discovery_refresh_state SET status=?,error_message=?,finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND generation=? AND status='running'`, status, message, g)
	return e
}
func (s *Store) CancelDiscovery(c context.Context) error {
	_, e := s.db.ExecContext(c, `UPDATE discovery_refresh_state SET generation=generation+1,status='cancelled',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND status='running'`)
	return e
}

const discoveryGroupSelect = `r.rgid,r.title,r.primary_type,r.secondary_types_json,r.kind,r.first_release_date,r.disambiguation,r.artist_credit,r.cover_status,r.cover_ext,COALESCE(r.cover_checked_at,'')`

func (s *Store) DiscoveryCatalog(c context.Context) ([]DiscoveryCandidate, error) {
	if e := s.IdentityReady(c); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(c, `SELECT `+discoveryGroupSelect+`,COALESCE(a.id,0),COALESCE(a.user_display_name,a.display_name,'') FROM discovery_release_groups r JOIN discovery_artist_release_groups d ON d.rgid=r.rgid LEFT JOIN artists a ON a.id=d.artist_id ORDER BY r.rgid,a.id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []DiscoveryCandidate{}
	for rows.Next() {
		v := DiscoveryCandidate{SeedKind: "artist"}
		var raw string
		e = rows.Scan(&v.RGID, &v.Title, &v.PrimaryType, &raw, &v.Kind, &v.FirstReleaseDate, &v.Disambiguation, &v.ArtistCredit, &v.CoverStatus, &v.CoverExt, &v.CoverCheckedAt, &v.SeedArtistID, &v.Artist)
		if e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(raw), &v.SecondaryTypes); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) SaveDiscoveryCandidates(c context.Context, g int64, seeds []DiscoverySeed, items []DiscoveryCandidate, matches []DiscoveryMatch, workSeeds ...[]DiscoveryWorkSeed) error {
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
	if e = discoveryGuard(c, t, g, seeds); e != nil {
		return e
	}
	checkedWorks := map[int64]bool{}
	for _, ws := range workSeeds {
		for _, seed := range ws {
			checkedWorks[seed.WorkID] = true
		}
		if e = discoveryWorkGuard(c, t, g, ws); e != nil {
			return e
		}
	}
	for _, q := range []string{`DELETE FROM discovery_candidates`, `DELETE FROM discovery_possible_matches`} {
		if _, e = t.ExecContext(c, q); e != nil {
			return e
		}
	}
	for _, v := range items {
		if v.SeedWorkID != 0 && !checkedWorks[v.SeedWorkID] {
			var subject int64
			if e = t.QueryRowContext(c, `SELECT subject_id FROM discovery_work_seeds WHERE work_id=? AND generation=?`, v.SeedWorkID, g).Scan(&subject); errors.Is(e, sql.ErrNoRows) {
				return ErrDiscoveryStale
			} else if e != nil {
				return e
			}
			if e = discoveryWorkGuard(c, t, g, []DiscoveryWorkSeed{{WorkID: v.SeedWorkID, SubjectID: subject}}); e != nil {
				return e
			}
			checkedWorks[v.SeedWorkID] = true
		}
	}
	for _, v := range items {
		raw, _ := json.Marshal(v.Reasons)
		var rank any
		if v.Rank > 0 {
			rank = v.Rank
		}
		if _, e = t.ExecContext(c, `INSERT INTO discovery_candidates(rgid,seed_kind,seed_artist_id,seed_work_id,bangumi_subject_id,generation,score,lane,rank,reasons_json) VALUES(?,?,?,?,?,?,?,?,?,?)`, v.RGID, discoverySeedKind(v), nullableDiscoveryID(v.SeedArtistID), nullableDiscoveryID(v.SeedWorkID), nullableDiscoveryID(v.SubjectID), g, v.Score, v.Lane, rank, string(raw)); e != nil {
			return e
		}
	}
	for _, v := range matches {
		if _, e = t.ExecContext(c, `INSERT OR IGNORE INTO discovery_possible_matches(rgid,album_id,reason) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM discovery_match_rejections WHERE rgid=? AND album_id=?)`, v.RGID, v.AlbumID, v.Reason, v.RGID, v.AlbumID); e != nil {
			return e
		}
	}
	_, e = t.ExecContext(c, `UPDATE discovery_refresh_state SET status='completed',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=1 AND status='running'`)
	if e != nil {
		return e
	}
	return t.Commit()
}
func (s *Store) UpdateDiscoveryCover(c context.Context, g int64, seeds []DiscoverySeed, rgid, status, ext string, now time.Time) error {
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
	_, e = t.ExecContext(c, `UPDATE discovery_release_groups SET cover_status=?,cover_ext=?,cover_checked_at=? WHERE rgid=?`, status, ext, now.UTC().Format(time.RFC3339), rgid)
	if e != nil {
		return e
	}
	return t.Commit()
}
func normalizedDiscoveryID(raw string) (string, error) {
	id, ok := metadata.NormalizeMusicBrainzID(raw)
	if !ok {
		return "", fmt.Errorf("invalid release group ID")
	}
	return id, nil
}
