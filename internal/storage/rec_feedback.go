package storage

import (
	"context"
	"database/sql"
	"math"
	"time"
)

// Implicit feedback from plays with a daily mix context (docs/daily-mix-design.md §7).
// Ended sessions are folded in once (context_processed) into per-mix track rows,
// recommendation affinities for tracks and tags (30-day half-life) and the theme
// α/β of the mix (mix-level signal, applied as a delta so re-aggregation is idempotent).

// RecAffinityHalfLife is the half-life of track / tag affinities.
const RecAffinityHalfLife = 30 * 24 * time.Hour

const (
	signalComplete  = 1.0
	signalMidSkip   = -0.4
	signalQuickSkip = -1.0
	signalFavorite  = 2.0
	signalRepeat    = 0.5
	// quickSkipMillis: a skip before 30 s is a “秒切”.
	quickSkipMillis = 30_000
	// playedEvidenceMillis: an interrupted session counts as played after 10 s.
	playedEvidenceMillis = 10_000
	feedbackBatch        = 2000
)

// SessionSignal is the single-track signal of one ended session (§7.2) and whether it
// shows the track was actually played. Stops, closes, replacements, errors and expiries
// are neutral: leaving is not disliking.
func SessionSignal(endReason string, position, initial, duration int64, counted bool) (signal float64, played, quickSkip bool) {
	played = endReason == "completed" || endReason == "skipped" || counted || position-initial >= playedEvidenceMillis
	frac := 0.0
	if duration > 0 {
		frac = float64(position) / float64(duration)
	}
	switch endReason {
	case "completed":
		return signalComplete, true, false
	case "skipped":
		switch {
		case duration > 0 && frac >= 0.8:
			return signalComplete, true, false
		case duration > 0 && frac >= 0.5:
			return 0, true, false
		case position >= quickSkipMillis:
			return signalMidSkip, true, false
		default:
			return signalQuickSkip, true, true
		}
	}
	return 0, played, false
}

// decayed returns score aged from updatedAt to now.
func decayed(score float64, updatedAt string, now time.Time) float64 {
	t, err := time.Parse(playbackTimeLayout, updatedAt)
	if err != nil || score == 0 {
		return score
	}
	age := now.Sub(t)
	if age <= 0 {
		return score
	}
	return score * math.Pow(0.5, float64(age)/float64(RecAffinityHalfLife))
}

// FeedbackRun summarises one aggregation pass.
type FeedbackRun struct {
	Sessions int
	Mixes    int
}

type feedbackSession struct {
	id               string
	trackID, mixID   int64
	endReason        string
	position         int64
	initial          int64
	duration         int64
	counted          bool
	favoritedDuring  bool
	themeType, theme string
}

// ProcessPlaybackFeedback folds ended, not yet processed sessions with a daily mix
// context into the feedback tables. Safe to run at any cadence.
func (s *Store) ProcessPlaybackFeedback(ctx context.Context, now time.Time) (FeedbackRun, error) {
	var run FeedbackRun
	err := withBusyRetry(ctx, func() error {
		run = FeedbackRun{}
		tx, err := s.beginWrite(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// Sessions of mixes that no longer exist carry nothing to learn from.
		if _, err = tx.ExecContext(ctx, `UPDATE playback_sessions SET context_processed=1 WHERE context_type IS NOT NULL AND context_processed=0 AND ended_at IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM daily_mixes m WHERE m.id=playback_sessions.context_id)`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT s.session_id, s.track_id, s.context_id, COALESCE(s.end_reason,''), s.position_ms, s.initial_position_ms,
				CASE WHEN COALESCE(t.duration_ms,0) > 0 THEN t.duration_ms ELSE s.duration_ms END,
				s.counted_at IS NOT NULL,
				t.is_favorite=1 AND t.favorited_at IS NOT NULL AND t.favorited_at >= s.started_at AND t.favorited_at <= strftime('%Y-%m-%dT%H:%M:%fZ', s.ended_at, '+5 minutes'),
				m.theme_type, m.theme_key
			FROM playback_sessions s JOIN daily_mixes m ON m.id = s.context_id JOIN tracks t ON t.id = s.track_id
			WHERE s.context_type = 'dailyMix' AND s.context_processed = 0 AND s.ended_at IS NOT NULL
			ORDER BY s.ended_at, s.session_id LIMIT ?`, feedbackBatch)
		if err != nil {
			return err
		}
		var batch []feedbackSession
		for rows.Next() {
			var f feedbackSession
			if err := rows.Scan(&f.id, &f.trackID, &f.mixID, &f.endReason, &f.position, &f.initial, &f.duration, &f.counted, &f.favoritedDuring, &f.themeType, &f.theme); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, f)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return tx.Commit()
		}
		stamp := now.UTC().Format(playbackTimeLayout)
		mixes := map[int64]bool{}
		for _, f := range batch {
			mixes[f.mixID] = true
			if err := applySessionFeedback(ctx, tx, f, now, stamp); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE playback_sessions SET context_processed=1 WHERE session_id=?`, f.id); err != nil {
				return err
			}
		}
		for id := range mixes {
			if err := applyMixFeedback(ctx, tx, id, stamp); err != nil {
				return err
			}
		}
		run = FeedbackRun{Sessions: len(batch), Mixes: len(mixes)}
		return tx.Commit()
	})
	return run, err
}

func applySessionFeedback(ctx context.Context, tx *sql.Tx, f feedbackSession, now time.Time, stamp string) error {
	signal, played, quick := SessionSignal(f.endReason, f.position, f.initial, f.duration, f.counted)
	if !played && !f.favoritedDuring {
		return nil
	}
	var plays, favorited int64
	err := tx.QueryRowContext(ctx, `SELECT plays, favorited FROM daily_mix_play_tracks WHERE mix_id=? AND track_id=?`, f.mixID, f.trackID).Scan(&plays, &favorited)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	delta := 0.0
	addPlay, addComplete, addQuick := 0, 0, 0
	if played {
		addPlay = 1
		delta += signal
		if signal >= signalComplete {
			addComplete = 1
		}
		if quick {
			addQuick = 1
		}
		if plays == 1 {
			delta += signalRepeat // the second play of the same track in the same mix
		}
	}
	addFavorite := 0
	if f.favoritedDuring && favorited == 0 {
		delta += signalFavorite
		addFavorite = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO daily_mix_play_tracks(mix_id, track_id, plays, completes, quick_skips, signal, favorited) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(mix_id, track_id) DO UPDATE SET plays=plays+excluded.plays, completes=completes+excluded.completes, quick_skips=quick_skips+excluded.quick_skips,
			signal=signal+excluded.signal, favorited=MAX(favorited, excluded.favorited)`,
		f.mixID, f.trackID, addPlay, addComplete, addQuick, delta, addFavorite); err != nil {
		return err
	}
	if delta == 0 && addPlay == 0 {
		return nil
	}
	// Track affinity: age the stored score to now, then add.
	var score float64
	var updated string
	err = tx.QueryRowContext(ctx, `SELECT score, updated_at FROM rec_track_affinity WHERE track_id=?`, f.trackID).Scan(&score, &updated)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO rec_track_affinity(track_id, score, plays, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(track_id) DO UPDATE SET score=excluded.score, plays=plays+excluded.plays, updated_at=excluded.updated_at`,
		f.trackID, decayed(score, updated, now)+delta, addPlay, stamp); err != nil {
		return err
	}
	if delta == 0 {
		return nil
	}
	// Tag affinity: the signal is shared out by the track's tag weights, per kind.
	rows, err := tx.QueryContext(ctx, `SELECT kind, tag, MAX(weight*confidence) FROM track_tags WHERE track_id=? GROUP BY kind, tag`, f.trackID)
	if err != nil {
		return err
	}
	type tagW struct {
		kind, tag string
		w         float64
	}
	var tags []tagW
	sum := map[string]float64{}
	for rows.Next() {
		var t tagW
		if err := rows.Scan(&t.kind, &t.tag, &t.w); err != nil {
			rows.Close()
			return err
		}
		if t.w > 0 {
			tags = append(tags, t)
			sum[t.kind] += t.w
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range tags {
		share := delta * t.w / sum[t.kind]
		var old float64
		var at string
		err := tx.QueryRowContext(ctx, `SELECT score, updated_at FROM rec_tag_affinity WHERE kind=? AND tag=?`, t.kind, t.tag).Scan(&old, &at)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO rec_tag_affinity(kind, tag, score, updated_at) VALUES (?,?,?,?)
			ON CONFLICT(kind, tag) DO UPDATE SET score=excluded.score, updated_at=excluded.updated_at`, t.kind, t.tag, decayed(old, at, now)+share, stamp); err != nil {
			return err
		}
	}
	return nil
}

// MixPlayStats is the aggregated listening of one mix.
type MixPlayStats struct {
	Played       int     `json:"played"`
	Completes    int     `json:"completes"`
	QuickSkips   int     `json:"quickSkips"`
	EarlyQuick   int     `json:"earlyQuickSkips"`
	Signal       float64 `json:"signal"`
	Length       int     `json:"length"`
	Satisfaction float64 `json:"satisfaction"`
	Alpha        float64 `json:"alpha"`
	Beta         float64 `json:"beta"`
}

// MixFeedbackTargets returns the theme α/β a mix has earned (§7.3): played ≥5 with
// satisfaction ≥0.5 adds 0.5 × engagement to α; ≥3 quick skips in the first five
// tracks adds 0.7 to β.
func MixFeedbackTargets(st MixPlayStats) (alpha, beta float64) {
	if st.Played >= 5 && st.Satisfaction >= 0.5 && st.Length > 0 {
		alpha = 0.5 * math.Min(1, float64(st.Played)/float64(st.Length))
	}
	if st.EarlyQuick >= 3 {
		beta = 0.7
	}
	return alpha, beta
}

const mixPlayStatsSQL = `SELECT
	(SELECT COUNT(*) FROM daily_mix_play_tracks p WHERE p.mix_id=m.id AND p.plays>0),
	(SELECT COALESCE(SUM(p.completes),0) FROM daily_mix_play_tracks p WHERE p.mix_id=m.id),
	(SELECT COALESCE(SUM(p.quick_skips),0) FROM daily_mix_play_tracks p WHERE p.mix_id=m.id),
	(SELECT COUNT(*) FROM daily_mix_play_tracks p JOIN daily_mix_items i ON i.mix_id=p.mix_id AND i.track_id=p.track_id WHERE p.mix_id=m.id AND p.quick_skips>0 AND i.position<5),
	(SELECT COALESCE(SUM(p.signal),0) FROM daily_mix_play_tracks p WHERE p.mix_id=m.id),
	(SELECT COUNT(*) FROM daily_mix_items i WHERE i.mix_id=m.id),
	COALESCE(ps.applied_alpha,0), COALESCE(ps.applied_beta,0)
FROM daily_mixes m LEFT JOIN daily_mix_play_stats ps ON ps.mix_id=m.id WHERE m.id=?`

func scanMixPlayStats(row *sql.Row) (MixPlayStats, error) {
	var st MixPlayStats
	if err := row.Scan(&st.Played, &st.Completes, &st.QuickSkips, &st.EarlyQuick, &st.Signal, &st.Length, &st.Alpha, &st.Beta); err != nil {
		return st, err
	}
	if st.Played > 0 {
		st.Satisfaction = st.Signal / float64(st.Played)
	}
	return st, nil
}

func applyMixFeedback(ctx context.Context, tx *sql.Tx, mixID int64, stamp string) error {
	st, err := scanMixPlayStats(tx.QueryRowContext(ctx, mixPlayStatsSQL, mixID))
	if err != nil {
		return err
	}
	alpha, beta := MixFeedbackTargets(st)
	if alpha == st.Alpha && beta == st.Beta {
		return nil
	}
	var themeType, themeKey string
	if err := tx.QueryRowContext(ctx, `SELECT theme_type, theme_key FROM daily_mixes WHERE id=?`, mixID).Scan(&themeType, &themeKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO daily_mix_theme_stats(theme_type, theme_key) VALUES (?, ?) ON CONFLICT DO NOTHING`, themeType, themeKey); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE daily_mix_theme_stats SET alpha = MAX(1, alpha - ? + ?), beta = MAX(1, beta - ? + ?), updated_at = ? WHERE theme_type = ? AND theme_key = ?`,
		st.Alpha, alpha, st.Beta, beta, stamp, themeType, themeKey); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO daily_mix_play_stats(mix_id, applied_alpha, applied_beta, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(mix_id) DO UPDATE SET applied_alpha=excluded.applied_alpha, applied_beta=excluded.applied_beta, updated_at=excluded.updated_at`, mixID, alpha, beta, stamp)
	return err
}

// DailyMixPlayStats returns the listening summary of the given mixes (admin page).
func (s *Store) DailyMixPlayStats(ctx context.Context, ids []int64) (map[int64]MixPlayStats, error) {
	out := map[int64]MixPlayStats{}
	for _, id := range ids {
		st, err := scanMixPlayStats(s.db.QueryRowContext(ctx, mixPlayStatsSQL, id))
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		if st.Played > 0 || st.QuickSkips > 0 {
			out[id] = st
		}
	}
	return out, nil
}

// RecAffinities are the decayed recommendation affinities.
type RecAffinities struct {
	Tracks map[int64]float64
	// Tags is keyed "mood:cheerful" / "style:rock".
	Tags map[string]float64
}

// RecAffinities loads the affinities aged to now.
func (s *Store) RecAffinities(ctx context.Context, now time.Time) (RecAffinities, error) {
	out := RecAffinities{Tracks: map[int64]float64{}, Tags: map[string]float64{}}
	rows, err := s.db.QueryContext(ctx, `SELECT track_id, score, updated_at FROM rec_track_affinity`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id int64
		var score float64
		var at string
		if err := rows.Scan(&id, &score, &at); err != nil {
			rows.Close()
			return out, err
		}
		if v := decayed(score, at, now); math.Abs(v) >= 0.01 {
			out.Tracks[id] = v
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT kind, tag, score, updated_at FROM rec_tag_affinity`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, tag, at string
		var score float64
		if err := rows.Scan(&kind, &tag, &score, &at); err != nil {
			return out, err
		}
		if v := decayed(score, at, now); math.Abs(v) >= 0.01 {
			out.Tags[kind+":"+tag] = v
		}
	}
	return out, rows.Err()
}
