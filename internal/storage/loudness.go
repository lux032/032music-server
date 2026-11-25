package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// LoudnessAnalyzerVersion is the current EBU R128 measurement schema version.
// Bumping it invalidates cached measurements and triggers background re-measurement.
const LoudnessAnalyzerVersion = 1

const loudnessMedianTTL = 30 * time.Second

type loudnessMedianCache struct {
	mu      sync.Mutex
	valid   bool
	version int
	value   *float64
	at      time.Time
}

// AudioLoudnessSettings stores persistent admin controls for the background loudness worker.
type AudioLoudnessSettings struct {
	Paused bool
}

// AudioLoudnessCandidate represents one primary audio file needing EBU R128 measurement.
type AudioLoudnessCandidate struct {
	AudioFileID  int64
	TrackID      int64
	AlbumID      int64
	RootPath     string
	RelativePath string
	Title        string
	FileSize     int64
	ModifiedAtNS int64
	DurationMs   int64
}

// AudioLoudnessRecord holds the EBU R128 measurement result for one audio file.
type AudioLoudnessRecord struct {
	AudioFileID     int64
	FileSize        int64
	ModifiedAtNS    int64
	AnalyzerVersion int
	IntegratedLUFS  float64
	TruePeakDB      float64
	LoudnessRange   *float64
	AnalyzeMs       int64
	MeasuredAt      string
}

// AudioLoudnessStats summarizes background loudness analysis progress and library baseline.
type AudioLoudnessStats struct {
	Eligible          int64    `json:"eligible"`
	Analyzed          int64    `json:"analyzed"`
	RetryPending      int64    `json:"retryPending"`
	PermanentFailures int64    `json:"permanentFailures"`
	LibraryMedianLUFS *float64 `json:"libraryMedianLufs,omitempty"`
	LastMeasuredAt    string   `json:"lastMeasuredAt,omitempty"`
	RecentAvgMs       int64    `json:"recentAvgMs,omitempty"`
}

// AudioLoudnessFailure is one active failure row displayed on the admin page.
type AudioLoudnessFailure struct {
	AudioFileID  int64  `json:"audioFileId"`
	TrackID      int64  `json:"trackId"`
	Title        string `json:"title"`
	RelativePath string `json:"relativePath"`
	Attempts     int    `json:"attempts"`
	Permanent    bool   `json:"permanent"`
	LastError    string `json:"lastError"`
	NextRetryAt  string `json:"nextRetryAt"`
	UpdatedAt    string `json:"updatedAt"`
}

func (s *Store) invalidateLoudnessMedian() {
	s.loudnessMedian.mu.Lock()
	s.loudnessMedian.valid = false
	s.loudnessMedian.mu.Unlock()
}

// AudioLoudnessSettings reads persistent loudness worker settings.
func (s *Store) AudioLoudnessSettings(ctx context.Context) (AudioLoudnessSettings, error) {
	var st AudioLoudnessSettings
	var paused int
	err := s.db.QueryRowContext(ctx, `SELECT paused FROM audio_loudness_settings WHERE id = 1`).Scan(&paused)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.Paused = paused != 0
	return st, nil
}

// SetAudioLoudnessPaused updates the persisted paused state for background loudness analysis.
func (s *Store) SetAudioLoudnessPaused(ctx context.Context, paused bool) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audio_loudness_settings(id, paused)
		VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET
			paused = excluded.paused,
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`, boolInt(paused))
	return err
}

func buildExcludeClause(col string, ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	placeholders, args := inClause(ids)
	return " AND " + col + " NOT IN (" + placeholders + ")", args
}

func scanLoudnessCandidates(rows *sql.Rows) ([]AudioLoudnessCandidate, error) {
	var out []AudioLoudnessCandidate
	for rows.Next() {
		var c AudioLoudnessCandidate
		if err := rows.Scan(
			&c.AudioFileID,
			&c.TrackID,
			&c.AlbumID,
			&c.RootPath,
			&c.RelativePath,
			&c.Title,
			&c.FileSize,
			&c.ModifiedAtNS,
			&c.DurationMs,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NextAudioLoudnessCandidates selects up to `limit` unmeasured or stale primary audio files.
// Tier 1 prioritizes albums containing favorited albums/tracks or recent playback progress.
// Tier 2 clusters remaining albums by (album_id DESC, audio_file_id ASC) for HDD locality
// and rapid per-album loudness completion.
func (s *Store) NextAudioLoudnessCandidates(ctx context.Context, version int, now time.Time, excludeFileIDs []int64, limit int) ([]AudioLoudnessCandidate, error) {
	if limit <= 0 {
		limit = 16
	}
	ts := AnalysisTimestamp(now)

	// Tier 1: favorited or recently played albums first.
	exclSQL, exclArgs := buildExcludeClause("af.id", excludeFileIDs)
	prioQuery := `WITH prio_albums AS (
		SELECT album_id, MAX(score) AS prio_score FROM (
			SELECT id AS album_id, COALESCE(updated_at, '') AS score FROM albums WHERE is_favorite = 1
			UNION ALL
			SELECT album_id, COALESCE(updated_at, '') AS score FROM tracks WHERE is_favorite = 1
			UNION ALL
			SELECT t.album_id, COALESCE(pp.last_played_at, '') AS score
			FROM playback_progress pp
			JOIN tracks t ON t.id = pp.track_id
			WHERE pp.last_played_at IS NOT NULL AND pp.last_played_at <> ''
		) GROUP BY album_id
	)
	SELECT af.id, t.id, t.album_id, l.root_path, af.relative_path,
		COALESCE(t.user_title, t.title), af.file_size, af.modified_at_ns, COALESCE(t.duration_ms, 0)
	FROM prio_albums pa
	JOIN tracks t ON t.album_id = pa.album_id
	JOIN audio_files af
		ON af.id = (SELECT MIN(a2.id) FROM audio_files a2 WHERE a2.track_id = t.id AND a2.status = 'available')
	JOIN libraries l ON l.id = af.library_id
	LEFT JOIN audio_loudness al ON al.audio_file_id = af.id
	WHERE (al.audio_file_id IS NULL OR al.analyzer_version <> ? OR al.file_size <> af.file_size OR al.modified_at_ns <> af.modified_at_ns)
	  AND NOT EXISTS (
		SELECT 1 FROM audio_loudness_failures f
		WHERE f.audio_file_id = af.id
		  AND f.analyzer_version = ?
		  AND f.file_size = af.file_size
		  AND f.modified_at_ns = af.modified_at_ns
		  AND (f.permanent = 1 OR f.next_retry_at > ?)
	  )` + exclSQL + `
	ORDER BY pa.prio_score DESC, pa.album_id DESC, af.id ASC
	LIMIT ?`

	prioArgs := make([]any, 0, 4+len(exclArgs))
	prioArgs = append(prioArgs, version, version, ts)
	prioArgs = append(prioArgs, exclArgs...)
	prioArgs = append(prioArgs, limit)

	rows, err := s.db.QueryContext(ctx, prioQuery, prioArgs...)
	if err != nil {
		return nil, err
	}
	results, err := scanLoudnessCandidates(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(results) >= limit {
		return results, nil
	}

	// Tier 2: remaining library clustered by album_id DESC, audio_file_id ASC.
	remaining := limit - len(results)
	combinedExcl := make([]int64, 0, len(excludeFileIDs)+len(results))
	combinedExcl = append(combinedExcl, excludeFileIDs...)
	for _, c := range results {
		combinedExcl = append(combinedExcl, c.AudioFileID)
	}
	exclSQL2, exclArgs2 := buildExcludeClause("af.id", combinedExcl)
	regQuery := `SELECT af.id, t.id, t.album_id, l.root_path, af.relative_path,
		COALESCE(t.user_title, t.title), af.file_size, af.modified_at_ns, COALESCE(t.duration_ms, 0)
	FROM tracks t
	CROSS JOIN audio_files af
		ON af.id = (SELECT MIN(a2.id) FROM audio_files a2 WHERE a2.track_id = t.id AND a2.status = 'available')
	JOIN libraries l ON l.id = af.library_id
	LEFT JOIN audio_loudness al ON al.audio_file_id = af.id
	WHERE (al.audio_file_id IS NULL OR al.analyzer_version <> ? OR al.file_size <> af.file_size OR al.modified_at_ns <> af.modified_at_ns)
	  AND NOT EXISTS (
		SELECT 1 FROM audio_loudness_failures f
		WHERE f.audio_file_id = af.id
		  AND f.analyzer_version = ?
		  AND f.file_size = af.file_size
		  AND f.modified_at_ns = af.modified_at_ns
		  AND (f.permanent = 1 OR f.next_retry_at > ?)
	  )` + exclSQL2 + `
	ORDER BY t.album_id DESC, af.id ASC
	LIMIT ?`

	regArgs := make([]any, 0, 4+len(exclArgs2))
	regArgs = append(regArgs, version, version, ts)
	regArgs = append(regArgs, exclArgs2...)
	regArgs = append(regArgs, remaining)

	rows2, err := s.db.QueryContext(ctx, regQuery, regArgs...)
	if err != nil {
		return nil, err
	}
	more, err := scanLoudnessCandidates(rows2)
	rows2.Close()
	if err != nil {
		return nil, err
	}
	results = append(results, more...)
	return results, nil
}

// SaveAudioLoudness persists one audio file's EBU R128 measurement, clears any prior failure,
// and refreshes the owning album's aggregated loudness inside the same transaction.
func (s *Store) SaveAudioLoudness(ctx context.Context, rec AudioLoudnessRecord, albumID int64) error {
	if math.IsNaN(rec.IntegratedLUFS) || math.IsInf(rec.IntegratedLUFS, 0) {
		return fmt.Errorf("invalid integrated loudness: %v", rec.IntegratedLUFS)
	}
	if math.IsNaN(rec.TruePeakDB) || math.IsInf(rec.TruePeakDB, 0) {
		return fmt.Errorf("invalid true peak: %v", rec.TruePeakDB)
	}
	if rec.LoudnessRange != nil && (math.IsNaN(*rec.LoudnessRange) || math.IsInf(*rec.LoudnessRange, 0)) {
		return fmt.Errorf("invalid loudness range: %v", *rec.LoudnessRange)
	}
	if rec.AnalyzerVersion <= 0 {
		rec.AnalyzerVersion = LoudnessAnalyzerVersion
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO audio_loudness(
			audio_file_id, file_size, modified_at_ns, analyzer_version,
			integrated_lufs, true_peak_db, loudness_range, analyze_ms, measured_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(audio_file_id) DO UPDATE SET
			file_size = excluded.file_size,
			modified_at_ns = excluded.modified_at_ns,
			analyzer_version = excluded.analyzer_version,
			integrated_lufs = excluded.integrated_lufs,
			true_peak_db = excluded.true_peak_db,
			loudness_range = excluded.loudness_range,
			analyze_ms = excluded.analyze_ms,
			measured_at = excluded.measured_at`,
		rec.AudioFileID, rec.FileSize, rec.ModifiedAtNS, rec.AnalyzerVersion,
		rec.IntegratedLUFS, rec.TruePeakDB, rec.LoudnessRange, max(rec.AnalyzeMs, 0)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM audio_loudness_failures WHERE audio_file_id = ?`, rec.AudioFileID); err != nil {
		return err
	}
	if albumID > 0 {
		if err = refreshAlbumLoudnessTx(ctx, tx, albumID, rec.AnalyzerVersion); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.invalidateLoudnessMedian()
	return nil
}

// RecordAudioLoudnessFailure records a permanent or retryable measurement failure with
// exponential backoff tied to the file fingerprint and analyzer version.
func (s *Store) RecordAudioLoudnessFailure(ctx context.Context, c AudioLoudnessCandidate, version int, message string, permanent bool, now time.Time) error {
	if len(message) > 500 {
		message = message[:500]
	}
	if version <= 0 {
		version = LoudnessAnalyzerVersion
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	attempts := 0
	var prevSize, prevMod int64
	var prevVer int
	err = tx.QueryRowContext(ctx,
		`SELECT attempts, file_size, modified_at_ns, analyzer_version FROM audio_loudness_failures WHERE audio_file_id = ?`,
		c.AudioFileID).Scan(&attempts, &prevSize, &prevMod, &prevVer)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if prevSize != c.FileSize || prevMod != c.ModifiedAtNS || prevVer != version {
		attempts = 0
	}
	attempts++
	next := AnalysisTimestamp(now.Add(audioRetryDelay(attempts)))
	if _, err = tx.ExecContext(ctx, `INSERT INTO audio_loudness_failures(
			audio_file_id, file_size, modified_at_ns, analyzer_version,
			permanent, attempts, next_retry_at, last_error, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(audio_file_id) DO UPDATE SET
			file_size = excluded.file_size,
			modified_at_ns = excluded.modified_at_ns,
			analyzer_version = excluded.analyzer_version,
			permanent = excluded.permanent,
			attempts = excluded.attempts,
			next_retry_at = excluded.next_retry_at,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at`,
		c.AudioFileID, c.FileSize, c.ModifiedAtNS, version, boolInt(permanent), attempts, next, message); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearAudioLoudnessFailures deletes all recorded loudness failures so they are retried.
func (s *Store) ClearAudioLoudnessFailures(ctx context.Context) (int64, error) {
	r, err := s.db.ExecContext(ctx, `DELETE FROM audio_loudness_failures`)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

// AudioLoudnessFailures returns active failures matching the current file fingerprint and version.
func (s *Store) AudioLoudnessFailures(ctx context.Context, version int, limit int) ([]AudioLoudnessFailure, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.audio_file_id, COALESCE(af.track_id, 0),
			COALESCE(t.user_title, t.title, af.relative_path), af.relative_path,
			f.attempts, f.permanent, f.last_error, f.next_retry_at, f.updated_at
		FROM audio_loudness_failures f
		JOIN audio_files af ON af.id = f.audio_file_id
		LEFT JOIN tracks t ON t.id = af.track_id
		WHERE af.status = 'available'
		  AND f.analyzer_version = ?
		  AND f.file_size = af.file_size
		  AND f.modified_at_ns = af.modified_at_ns
		ORDER BY f.updated_at DESC, f.audio_file_id
		LIMIT ?`, version, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AudioLoudnessFailure
	for rows.Next() {
		var item AudioLoudnessFailure
		var permanent int
		if err := rows.Scan(
			&item.AudioFileID, &item.TrackID, &item.Title, &item.RelativePath,
			&item.Attempts, &permanent, &item.LastError, &item.NextRetryAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.Permanent = permanent != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func refreshAlbumLoudnessTx(ctx context.Context, tx *sql.Tx, albumID int64, version int) error {
	rows, err := tx.QueryContext(ctx, `SELECT al.integrated_lufs, al.true_peak_db, COALESCE(t.duration_ms, 0)
		FROM tracks t
		JOIN audio_files af
			ON af.id = (SELECT MIN(a2.id) FROM audio_files a2 WHERE a2.track_id = t.id AND a2.status = 'available')
		JOIN audio_loudness al
			ON al.audio_file_id = af.id
		   AND al.file_size = af.file_size
		   AND al.modified_at_ns = af.modified_at_ns
		   AND al.analyzer_version = ?
		WHERE t.album_id = ?`, version, albumID)
	if err != nil {
		return err
	}
	var sumWeight, sumEnergy float64
	maxPeak := math.Inf(-1)
	count := 0
	for rows.Next() {
		var lufs, peak float64
		var durMs int64
		if err = rows.Scan(&lufs, &peak, &durMs); err != nil {
			rows.Close()
			return err
		}
		if math.IsNaN(lufs) || math.IsInf(lufs, 0) || math.IsNaN(peak) || math.IsInf(peak, 0) {
			continue
		}
		w := float64(max(durMs, 1000))
		sumWeight += w
		sumEnergy += w * math.Pow(10.0, lufs/10.0)
		if peak > maxPeak {
			maxPeak = peak
		}
		count++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if count == 0 || sumWeight <= 0 || sumEnergy <= 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM album_loudness WHERE album_id = ?`, albumID)
		return err
	}
	albumLUFS := math.Round(10.0*math.Log10(sumEnergy/sumWeight)*100) / 100
	albumPeak := math.Round(maxPeak*100) / 100
	_, err = tx.ExecContext(ctx, `INSERT INTO album_loudness(album_id, integrated_lufs, true_peak_db, track_count, updated_at)
		VALUES (?, ?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
		ON CONFLICT(album_id) DO UPDATE SET
			integrated_lufs = excluded.integrated_lufs,
			true_peak_db = excluded.true_peak_db,
			track_count = excluded.track_count,
			updated_at = excluded.updated_at`,
		albumID, albumLUFS, albumPeak, count)
	return err
}

// RefreshAlbumLoudness recomputes the materialized loudness aggregate for one album.
func (s *Store) RefreshAlbumLoudness(ctx context.Context, albumID int64) error {
	if albumID <= 0 {
		return nil
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = refreshAlbumLoudnessTx(ctx, tx, albumID, LoudnessAnalyzerVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// ReconcileAlbumLoudness refreshes every materialized album aggregate whose track set or
// available files may have changed (for example after a library scan deletes/marks missing files).
func (s *Store) ReconcileAlbumLoudness(ctx context.Context) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT album_id FROM album_loudness`)
	if err != nil {
		return err
	}
	var albumIDs []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		albumIDs = append(albumIDs, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, id := range albumIDs {
		if err = refreshAlbumLoudnessTx(ctx, tx, id, LoudnessAnalyzerVersion); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.invalidateLoudnessMedian()
	return nil
}

// LibraryMedianLUFS returns the median integrated loudness across currently available
// measured tracks, cached briefly in memory to keep /capabilities fast.
func (s *Store) LibraryMedianLUFS(ctx context.Context, version int) (*float64, error) {
	if version <= 0 {
		version = LoudnessAnalyzerVersion
	}
	s.loudnessMedian.mu.Lock()
	if s.loudnessMedian.valid && s.loudnessMedian.version == version && time.Since(s.loudnessMedian.at) < loudnessMedianTTL {
		val := s.loudnessMedian.value
		s.loudnessMedian.mu.Unlock()
		if val == nil {
			return nil, nil
		}
		cp := *val
		return &cp, nil
	}
	s.loudnessMedian.mu.Unlock()

	var count int64
	err := s.db.QueryRowContext(ctx, audioAvailFilesCTE+`
		SELECT COUNT(*)
		FROM avail a
		JOIN audio_files af ON af.id = a.file_id
		JOIN audio_loudness al
			ON al.audio_file_id = af.id
		   AND al.file_size = af.file_size
		   AND al.modified_at_ns = af.modified_at_ns
		   AND al.analyzer_version = ?`, version).Scan(&count)
	if err != nil {
		return nil, err
	}
	var median *float64
	if count > 0 {
		offset := (count - 1) / 2
		limit := 1
		if count%2 == 0 {
			limit = 2
		}
		rows, err := s.db.QueryContext(ctx, audioAvailFilesCTE+`
			SELECT al.integrated_lufs
			FROM avail a
			JOIN audio_files af ON af.id = a.file_id
			JOIN audio_loudness al
				ON al.audio_file_id = af.id
			   AND al.file_size = af.file_size
			   AND al.modified_at_ns = af.modified_at_ns
			   AND al.analyzer_version = ?
			ORDER BY al.integrated_lufs ASC
			LIMIT ? OFFSET ?`, version, limit, offset)
		if err != nil {
			return nil, err
		}
		var vals []float64
		for rows.Next() {
			var v float64
			if err = rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			vals = append(vals, v)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		if len(vals) == 1 {
			m := math.Round(vals[0]*10) / 10
			median = &m
		} else if len(vals) == 2 {
			m := math.Round(((vals[0]+vals[1])/2.0)*10) / 10
			median = &m
		}
	}

	s.loudnessMedian.mu.Lock()
	s.loudnessMedian.valid = true
	s.loudnessMedian.version = version
	s.loudnessMedian.at = time.Now()
	if median != nil {
		cp := *median
		s.loudnessMedian.value = &cp
	} else {
		s.loudnessMedian.value = nil
	}
	s.loudnessMedian.mu.Unlock()
	return median, nil
}

// AudioLoudnessStats returns progress counts, recent throughput, and library median LUFS.
func (s *Store) AudioLoudnessStats(ctx context.Context, version int, now time.Time) (AudioLoudnessStats, error) {
	if version <= 0 {
		version = LoudnessAnalyzerVersion
	}
	var st AudioLoudnessStats
	ts := AnalysisTimestamp(now)
	err := s.db.QueryRowContext(ctx, audioAvailFilesCTE+`
		SELECT COUNT(*),
			COALESCE(SUM(CASE WHEN al.audio_file_id IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN al.audio_file_id IS NULL AND f.permanent = 0 AND f.next_retry_at > ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN al.audio_file_id IS NULL AND f.permanent = 1 THEN 1 ELSE 0 END), 0)
		FROM avail a
		JOIN audio_files af ON af.id = a.file_id
		LEFT JOIN audio_loudness al
			ON al.audio_file_id = af.id
		   AND al.file_size = af.file_size
		   AND al.modified_at_ns = af.modified_at_ns
		   AND al.analyzer_version = ?
		LEFT JOIN audio_loudness_failures f
			ON f.audio_file_id = af.id
		   AND f.file_size = af.file_size
		   AND f.modified_at_ns = af.modified_at_ns
		   AND f.analyzer_version = ?`, ts, version, version).
		Scan(&st.Eligible, &st.Analyzed, &st.RetryPending, &st.PermanentFailures)
	if err != nil {
		return st, err
	}
	var last sql.NullString
	var avg sql.NullFloat64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(measured_at), AVG(analyze_ms) FROM (
			SELECT measured_at, analyze_ms FROM audio_loudness
			WHERE analyzer_version = ?
			ORDER BY measured_at DESC LIMIT 50
		)`, version).Scan(&last, &avg); err != nil {
		return st, err
	}
	st.LastMeasuredAt = last.String
	st.RecentAvgMs = int64(avg.Float64)
	if st.Analyzed > 0 {
		median, err := s.LibraryMedianLUFS(ctx, version)
		if err != nil {
			return st, err
		}
		st.LibraryMedianLUFS = median
	}
	return st, nil
}
