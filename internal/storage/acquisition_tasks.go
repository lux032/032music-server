package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lux032/032music-server/internal/acquisition/catalog"
	"github.com/lux032/032music-server/internal/acquisition/sanitize"
)

var ErrAcquisitionIgnored = errors.New("release group is ignored")
var ErrAcquisitionOwned = errors.New("release group already owned")

var ErrAcquisitionStale = errors.New("acquisition task version changed")
var ErrAcquisitionRGActive = errors.New("release group already has an active acquisition")
var ErrAcquisitionHashBound = errors.New("torrent hash already bound to an active acquisition")

const AcquisitionActiveStates = `'QB_SUBMITTING','DOWNLOADING','AWAITING_LIBRARY','ATTENTION'`

type AcquisitionTask struct {
	ID                                                                                           int64
	PublicID, IdempotencyKey, RGID, Title, Artist, Kind, TorrentTitle                            string
	TorrentSize                                                                                  int64
	InfoHash, State, AttentionReason, AttentionDetail, AddOutcome                                string
	DownloadRef                                                                                  string `json:"-"`
	SubmitAttempts                                                                               int
	NextCheckAt, QBSeenAt, QBMissingSince, DownloadCompletedAt, CreatedAt, UpdatedAt, FinishedAt string
	AddUnknownSince, QBUnreachableSince                                                          string
	Version                                                                                      int64
}

const acquisitionTaskColumns = `id,public_id,idempotency_key,rgid,title,artist,kind,torrent_title,torrent_size,coalesce(info_hash,''),download_ref,state,attention_reason,attention_detail,submit_attempts,add_outcome,coalesce(next_check_at,''),coalesce(qb_seen_at,''),coalesce(qb_missing_since,''),coalesce(download_completed_at,''),created_at,updated_at,coalesce(finished_at,''),version,coalesce(add_unknown_since,''),coalesce(qb_unreachable_since,'')`

func scanAcquisitionTask(row interface{ Scan(...any) error }) (AcquisitionTask, error) {
	var t AcquisitionTask
	e := row.Scan(&t.ID, &t.PublicID, &t.IdempotencyKey, &t.RGID, &t.Title, &t.Artist, &t.Kind, &t.TorrentTitle, &t.TorrentSize, &t.InfoHash, &t.DownloadRef, &t.State, &t.AttentionReason, &t.AttentionDetail, &t.SubmitAttempts, &t.AddOutcome, &t.NextCheckAt, &t.QBSeenAt, &t.QBMissingSince, &t.DownloadCompletedAt, &t.CreatedAt, &t.UpdatedAt, &t.FinishedAt, &t.Version, &t.AddUnknownSince, &t.QBUnreachableSince)
	return t, e
}
func (s *Store) AcquisitionTask(ctx context.Context, id int64) (AcquisitionTask, error) {
	return scanAcquisitionTask(s.db.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE id=?`, id))
}
func (s *Store) AcquisitionTaskByPublicID(ctx context.Context, id string) (AcquisitionTask, error) {
	return scanAcquisitionTask(s.db.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE public_id=?`, id))
}
func (s *Store) AcquisitionTaskByIdempotencyKey(ctx context.Context, key string) (AcquisitionTask, error) {
	return scanAcquisitionTask(s.db.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE idempotency_key=?`, key))
}
func (s *Store) CreateAcquisitionTask(ctx context.Context, t AcquisitionTask) (AcquisitionTask, error) {
	if old, e := s.AcquisitionTaskByIdempotencyKey(ctx, t.IdempotencyKey); e == nil {
		return old, nil
	} else if !errors.Is(e, sql.ErrNoRows) {
		return t, e
	}
	if t.IdempotencyKey == "" {
		return t, errors.New("missing idempotency key")
	}
	id, e := normalizedDiscoveryID(t.RGID)
	if e != nil {
		return t, e
	}
	t.RGID = id
	if t.PublicID == "" {
		h := acquisitionRandomID()
		t.PublicID = h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:]
	}
	if t.DownloadRef != "" {
		ref, mag, e := catalog.DownloadRef(t.DownloadRef)
		if e != nil {
			return t, e
		}
		if mag {
			return t, errors.New("不支持磁力链接")
		}
		t.DownloadRef = ref
	}
	// Serialize creation with discovery decisions, then recheck under the write lock.
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return t, e
	}
	defer tx.Rollback()
	if e = discoveryLock(ctx, tx); e != nil {
		return t, e
	}
	old, e := scanAcquisitionTask(tx.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE idempotency_key=?`, t.IdempotencyKey))
	if e == nil {
		return old, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return t, e
	}
	var ignored, owned bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM release_group_decisions WHERE rgid=? AND decision='ignore'),EXISTS(SELECT 1 FROM album_release_groups WHERE rgid=? AND status='resolved')`, t.RGID, t.RGID).Scan(&ignored, &owned); e != nil {
		return t, e
	}
	if owned {
		return t, ErrAcquisitionOwned
	}
	if ignored {
		return t, ErrAcquisitionIgnored
	}
	result, e := tx.ExecContext(ctx, `INSERT INTO acquisition_tasks(public_id,idempotency_key,rgid,title,artist,kind,torrent_title,torrent_size,info_hash,download_ref,state) VALUES(?,?,?,?,?,?,?,?,?,?,'QB_SUBMITTING') ON CONFLICT(idempotency_key) DO NOTHING`, t.PublicID, t.IdempotencyKey, t.RGID, t.Title, t.Artist, t.Kind, t.TorrentTitle, t.TorrentSize, nullableAcquisitionString(t.InfoHash), t.DownloadRef)
	if e != nil {
		if strings.Contains(e.Error(), "acquisition_tasks.rgid") {
			return t, ErrAcquisitionRGActive
		}
		if strings.Contains(e.Error(), "acquisition_tasks.info_hash") {
			return t, ErrAcquisitionHashBound
		}
		return t, e
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return scanAcquisitionTask(tx.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE idempotency_key=?`, t.IdempotencyKey))
	}
	t.ID, e = result.LastInsertId()
	if e != nil {
		return t, e
	}
	if e = tx.Commit(); e != nil {
		return t, e
	}
	return s.AcquisitionTask(ctx, t.ID)
}

// CASAcquisitionTask updates mutable reconciliation fields only. Terminal rows
// cannot be resurrected; a cancelled task rejects late workers via Version.
func (s *Store) CASAcquisitionTask(ctx context.Context, t AcquisitionTask) (AcquisitionTask, error) {
	if t.State == "IN_LIBRARY" {
		return t, errors.New("IN_LIBRARY requires acquisition evidence")
	}
	if t.DownloadRef != "" {
		ref, magnet, err := catalog.DownloadRef(t.DownloadRef)
		if err != nil {
			return t, err
		}
		if magnet {
			return t, errors.New("不支持磁力链接")
		}
		t.DownloadRef = ref
	}
	result, e := s.db.ExecContext(ctx, `UPDATE acquisition_tasks SET info_hash=?,download_ref=CASE WHEN ? IN ('DOWNLOADING','AWAITING_LIBRARY','IN_LIBRARY','CANCELLED') THEN '' ELSE ? END,state=?,attention_reason=?,attention_detail=?,submit_attempts=?,add_outcome=?,next_check_at=?,qb_seen_at=?,qb_missing_since=CASE WHEN state='ATTENTION' AND ?='DOWNLOADING' AND download_ref='' THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE ? END,download_completed_at=?,add_unknown_since=CASE WHEN ? IS NULL THEN NULL WHEN submit_attempts<? THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE coalesce(add_unknown_since,strftime('%Y-%m-%dT%H:%M:%fZ','now')) END,qb_unreachable_since=CASE WHEN ? IS NULL THEN NULL ELSE coalesce(qb_unreachable_since,strftime('%Y-%m-%dT%H:%M:%fZ','now')) END,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),finished_at=CASE WHEN ? IN ('IN_LIBRARY','CANCELLED') THEN strftime('%Y-%m-%dT%H:%M:%fZ','now') ELSE NULL END,version=version+1 WHERE id=? AND version=? AND state IN (`+AcquisitionActiveStates+`)`, nullableAcquisitionString(t.InfoHash), t.State, t.DownloadRef, t.State, t.AttentionReason, sanitize.Text(t.AttentionDetail), t.SubmitAttempts, t.AddOutcome, nullableAcquisitionString(t.NextCheckAt), nullableAcquisitionString(t.QBSeenAt), t.State, nullableAcquisitionString(t.QBMissingSince), nullableAcquisitionString(t.DownloadCompletedAt), nullableAcquisitionString(t.AddUnknownSince), t.SubmitAttempts, nullableAcquisitionString(t.QBUnreachableSince), t.State, t.ID, t.Version)
	if e != nil {
		return t, acquisitionConflict(e)
	}
	n, e := result.RowsAffected()
	if e != nil {
		return t, e
	}
	if n == 0 {
		return t, ErrAcquisitionStale
	}
	return s.AcquisitionTask(ctx, t.ID)
}
func (s *Store) ListAcquisitionTasks(ctx context.Context) ([]AcquisitionTask, error) {
	rows, e := s.db.QueryContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE state IN (`+AcquisitionActiveStates+`) OR (state='IN_LIBRARY' AND finished_at>strftime('%Y-%m-%dT%H:%M:%fZ','now','-7 days')) ORDER BY id DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []AcquisitionTask{}
	for rows.Next() {
		t, e := scanAcquisitionTask(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type AcquisitionEvidence struct {
	TaskID, AlbumID, AudioFileID int64
	Source                       string
	DetectedCount                int
	VerifiedAt                   string
}

// AcquisitionFileEvidence must preserve ErrIdentityNotReady so the worker can
// defer reconciliation rather than misclassifying an unrebuilt library.
func (s *Store) AcquisitionFileEvidence(ctx context.Context, t AcquisitionTask) ([]ReleaseGroupEvidence, error) {
	stored, err := s.AcquisitionTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	return s.ReleaseGroupFileEvidence(ctx, stored.RGID, stored.CreatedAt)
}

// CompleteAcquisitionWithEvidence atomically CASes the task and stores evidence;
// stale/cancelled workers can never leave evidence behind.
func (s *Store) CompleteAcquisitionWithEvidence(ctx context.Context, t AcquisitionTask, e AcquisitionEvidence) (AcquisitionTask, error) {
	if err := s.requireIdentityReady(ctx); err != nil {
		return t, err
	}
	return s.completeAcquisition(ctx, t, e, false)
}

// LinkAcquisition atomically associates an album and completes the task under CAS.
func (s *Store) LinkAcquisition(ctx context.Context, t AcquisitionTask, albumID int64, confirmReplace bool, source string) (AcquisitionTask, error) {
	if source != "manual" && source != "owned" {
		return t, errors.New("invalid link source")
	}
	return s.completeAcquisition(ctx, t, AcquisitionEvidence{Source: source, AlbumID: albumID}, confirmReplace)
}
func (s *Store) completeAcquisition(ctx context.Context, t AcquisitionTask, e AcquisitionEvidence, confirmReplace bool) (AcquisitionTask, error) {
	if err := s.requireIdentityReady(ctx); err != nil {
		return t, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return t, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE acquisition_tasks SET id=id WHERE 0`); err != nil {
		return t, err
	}
	stored, err := scanAcquisitionTask(tx.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE id=? AND version=?`, t.ID, t.Version))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrAcquisitionStale
	}
	if err != nil {
		return t, err
	}
	t = stored
	if s.acquisitionAfterRead != nil {
		s.acquisitionAfterRead()
	}
	if t.State == "CANCELLED" || t.State == "IN_LIBRARY" {
		return t, ErrAcquisitionStale
	}
	if e.Source == "manual" || e.Source == "owned" {
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM albums a WHERE a.id=? AND `+albumVisibleSQL("a.id")+`)`, e.AlbumID).Scan(&exists); err != nil {
			return t, err
		}
		if !exists {
			return t, sql.ErrNoRows
		}
		var current string
		err = tx.QueryRowContext(ctx, `SELECT rgid FROM album_release_groups WHERE album_id=? AND status='resolved'`, e.AlbumID).Scan(&current)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return t, err
		}
		if current != "" && current != t.RGID && !confirmReplace {
			return t, &ErrReplacesReleaseGroup{current}
		}
		if err = associateAlbumReleaseGroupTx(ctx, tx, e.AlbumID, t.RGID); err != nil {
			return t, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM discovery_possible_matches WHERE rgid=?`, t.RGID); err != nil {
			return t, err
		}
	} else if e.Source != "file" {
		return t, errors.New("invalid evidence source")
	}
	if e.Source == "file" {
		var eligible bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM audio_file_release_groups g JOIN audio_files af ON af.id=g.audio_file_id JOIN tracks tr ON tr.id=af.track_id JOIN album_release_groups a ON a.album_id=tr.album_id AND a.rgid=g.rgid AND a.status='resolved' WHERE af.id=? AND tr.album_id=? AND g.rgid=? AND af.status='available' AND af.created_at>? AND (SELECT count(*) FROM audio_file_release_groups x WHERE x.audio_file_id=af.id)=1)`, e.AudioFileID, e.AlbumID, t.RGID, t.CreatedAt).Scan(&eligible)
		if err != nil {
			return t, err
		}
		if !eligible {
			return t, fmt.Errorf("no eligible acquisition file evidence")
		}
	}
	r, err := tx.ExecContext(ctx, `UPDATE acquisition_tasks SET state='IN_LIBRARY',download_ref='',attention_reason='',attention_detail='',finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),version=version+1 WHERE id=? AND version=? AND state IN (`+AcquisitionActiveStates+`)`, t.ID, t.Version)
	if err != nil {
		return t, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return t, err
	}
	if n == 0 {
		return t, ErrAcquisitionStale
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT count(DISTINCT af.id) FROM audio_files af JOIN tracks tr ON tr.id=af.track_id JOIN audio_file_release_groups g ON g.audio_file_id=af.id WHERE tr.album_id=? AND g.rgid=? AND af.status='available'`, e.AlbumID, t.RGID).Scan(&count); err != nil {
		return t, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO acquisition_evidence(task_id,source,album_id,evidence_audio_file_id,detected_count) VALUES(?,?,?,?,?)`, t.ID, e.Source, nullableAcquisitionID(e.AlbumID), nullableAcquisitionID(e.AudioFileID), count)
	if err != nil {
		return t, err
	}
	if err = tx.Commit(); err != nil {
		return t, err
	}
	return s.AcquisitionTask(ctx, t.ID)
}
func nullableAcquisitionID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }
func (s *Store) AcquisitionEvidence(ctx context.Context, taskID int64) (AcquisitionEvidence, error) {
	var e AcquisitionEvidence
	err := s.db.QueryRowContext(ctx, `SELECT task_id,source,coalesce(album_id,0),coalesce(evidence_audio_file_id,0),detected_count,verified_at FROM acquisition_evidence WHERE task_id=?`, taskID).Scan(&e.TaskID, &e.Source, &e.AlbumID, &e.AudioFileID, &e.DetectedCount, &e.VerifiedAt)
	return e, err
}

func acquisitionConflict(err error) error {
	if strings.Contains(err.Error(), "acquisition_tasks.info_hash") {
		return ErrAcquisitionHashBound
	}
	if strings.Contains(err.Error(), "acquisition_tasks.rgid") {
		return ErrAcquisitionRGActive
	}
	return err
}

// AcquisitionReleaseGroup reads the durable catalog rather than refresh candidates.
func (s *Store) AcquisitionReleaseGroup(ctx context.Context, rgid string) (DiscoveryReleaseGroup, error) {
	id, err := normalizedDiscoveryID(rgid)
	if err != nil {
		return DiscoveryReleaseGroup{}, err
	}
	var r DiscoveryReleaseGroup
	err = s.db.QueryRowContext(ctx, `SELECT rgid,title,kind,artist_credit FROM discovery_release_groups WHERE rgid=?`, id).Scan(&r.RGID, &r.Title, &r.Kind, &r.ArtistCredit)
	return r, err
}

// ActiveAcquisitionByHash supplies the public ID for a hash_bound conflict link.
func (s *Store) ActiveAcquisitionByHash(ctx context.Context, hash string) (AcquisitionTask, error) {
	return scanAcquisitionTask(s.db.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE info_hash=? AND state IN (`+AcquisitionActiveStates+`)`, hash))
}

// ActiveAcquisitionByRG finds the task used by discovery's owned action.
func (s *Store) ActiveAcquisitionByRG(ctx context.Context, rgid string) (AcquisitionTask, error) {
	return scanAcquisitionTask(s.db.QueryRowContext(ctx, `SELECT `+acquisitionTaskColumns+` FROM acquisition_tasks WHERE rgid=? AND state IN (`+AcquisitionActiveStates+`)`, rgid))
}
