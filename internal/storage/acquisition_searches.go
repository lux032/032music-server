package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/lux032/032music-server/internal/acquisition/catalog"
)

type AcquisitionSearch struct {
	ID, RGID, Indexer, Query, QueryKey, CreatedAt string
	Results                                       []catalog.Result
}

func AcquisitionQueryKey(query string) string {
	return strings.ToLower(strings.Join(strings.Fields(query), " "))
}
func acquisitionRandomID() string {
	var b [16]byte
	_, e := rand.Read(b[:])
	if e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

// SaveAcquisitionSearch accepts successful searches only, including real empty
// results. Search errors must not call this method.
func (s *Store) SaveAcquisitionSearch(ctx context.Context, v AcquisitionSearch) (AcquisitionSearch, error) {
	id, e := normalizedDiscoveryID(v.RGID)
	if e != nil {
		return v, e
	}
	v.RGID = id
	v.ID = acquisitionRandomID()
	v.QueryKey = AcquisitionQueryKey(v.Query)
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return v, e
	}
	defer tx.Rollback()
	if e = tx.QueryRowContext(ctx, `INSERT INTO acquisition_searches(id,rgid,indexer,query,query_key,result_count) VALUES(?,?,?,?,?,?) RETURNING created_at`, v.ID, v.RGID, v.Indexer, v.Query, v.QueryKey, len(v.Results)).Scan(&v.CreatedAt); e != nil {
		return v, e
	}
	for i, r := range v.Results {
		ref, mag, e := catalog.DownloadRef(r.DownloadRef)
		if r.DownloadRef == "" && r.MagnetOnly {
			mag = true
			e = nil
		}
		if e != nil {
			return v, e
		}
		if !mag && ref == "" {
			return v, errors.New("missing download reference")
		}
		r.DownloadRef = ref
		r.MagnetOnly = mag
		v.Results[i] = r
		if _, e = tx.ExecContext(ctx, `INSERT INTO acquisition_search_results(search_id,idx,title,category,size,seeders,leechers,grabs,download_factor,published_at,download_ref,magnet_only) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, i, r.Title, r.Category, r.Size, r.Seeders, r.Peers, r.Grabs, r.DownloadFactor, r.PublishedAt, ref, mag); e != nil {
			return v, e
		}
	}
	return v, tx.Commit()
}
func (s *Store) AcquisitionSearch(ctx context.Context, id string) (AcquisitionSearch, error) {
	var v AcquisitionSearch
	e := s.db.QueryRowContext(ctx, `SELECT id,rgid,indexer,query,query_key,created_at FROM acquisition_searches WHERE id=? AND created_at>strftime('%Y-%m-%dT%H:%M:%fZ','now','-15 minutes')`, id).Scan(&v.ID, &v.RGID, &v.Indexer, &v.Query, &v.QueryKey, &v.CreatedAt)
	if e != nil {
		return v, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT title,category,size,seeders,leechers,grabs,download_factor,published_at,download_ref,magnet_only FROM acquisition_search_results WHERE search_id=? ORDER BY idx`, id)
	if e != nil {
		return v, e
	}
	defer rows.Close()
	v.Results = []catalog.Result{}
	for rows.Next() {
		var r catalog.Result
		if e = rows.Scan(&r.Title, &r.Category, &r.Size, &r.Seeders, &r.Peers, &r.Grabs, &r.DownloadFactor, &r.PublishedAt, &r.DownloadRef, &r.MagnetOnly); e != nil {
			return v, e
		}
		v.Results = append(v.Results, r)
	}
	return v, rows.Err()
}
func (s *Store) CachedAcquisitionSearch(ctx context.Context, rgid, indexer, query string) (AcquisitionSearch, error) {
	rgid, err := normalizedDiscoveryID(rgid)
	if err != nil {
		return AcquisitionSearch{}, err
	}
	var id string
	e := s.db.QueryRowContext(ctx, `SELECT id FROM acquisition_searches WHERE rgid=? AND indexer=? AND query_key=? AND created_at>strftime('%Y-%m-%dT%H:%M:%fZ','now','-15 minutes') ORDER BY created_at DESC,rowid DESC LIMIT 1`, rgid, indexer, AcquisitionQueryKey(query)).Scan(&id)
	if e != nil {
		return AcquisitionSearch{}, e
	}
	return s.AcquisitionSearch(ctx, id)
}
func (s *Store) CleanupAcquisitionSearches(ctx context.Context) error {
	_, e := s.db.ExecContext(ctx, `DELETE FROM acquisition_searches WHERE created_at<=strftime('%Y-%m-%dT%H:%M:%fZ','now','-15 minutes')`)
	return e
}
