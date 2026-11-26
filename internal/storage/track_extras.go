package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// TrackExtras is shared by full and sync track responses. audio_files.bitrate is kbps.
type TrackExtras struct {
	Codec             string   `json:"codec,omitempty"`
	SampleRate        int      `json:"sampleRate,omitempty"`
	BitDepth          int      `json:"bitDepth,omitempty"`
	BitrateKbps       int      `json:"bitrateKbps,omitempty"`
	Channels          int      `json:"channels,omitempty"`
	ViewCount         *int64   `json:"viewCount,omitempty"`
	SkipCount         *int64   `json:"skipCount,omitempty"`
	LastViewedAt      *int64   `json:"lastViewedAt,omitempty"`
	LyricsURL         string   `json:"lyricsUrl,omitempty"`
	LoudnessLUFS      *float64 `json:"loudnessLufs,omitempty"`
	TruePeakDB        *float64 `json:"truePeakDb,omitempty"`
	LoudnessRange     *float64 `json:"loudnessRange,omitempty"`
	AlbumLoudnessLUFS *float64 `json:"albumLoudnessLufs,omitempty"`
	AlbumTruePeakDB   *float64 `json:"albumTruePeakDb,omitempty"`
}

const trackArtistSQL = `COALESCE(` + trackCreditDisplaySQL + `,` + albumCreditDisplaySQL + `,a.user_performed_by,a.performed_by,'Unknown Artist')`

// albumArtworkURLSQL is the single cover-selection rule for every album
// artwork projection: a user-uploaded custom cover always wins, then the
// primary flag, then the earliest row. Requires the albums table alias `a`.
const albumArtworkURLSQL = `COALESCE((SELECT '/api/v1/artwork/'||aw_art.id FROM artworks aw_art WHERE aw_art.album_id=a.id ORDER BY (aw_art.source_type='custom') DESC,aw_art.is_primary DESC,aw_art.id LIMIT 1),'')`

// artistImageURLSQL renders the artist image URL expression for the given
// artists table alias. A custom image wins over the Last.fm/MusicBrainz cache
// and carries a content-hash version (?v=) so clients pick up replacements.
// D50 inheritance: the artist's own custom image wins; with several merged-in
// sources carrying custom images, the smallest artist id wins (deterministic);
// the automatic cache is only a fallback.
func artistImageURLSQL(alias string) string {
	// Inner aliases (ma) matter: reusing the outer alias would make the
	// merged-from subquery self-referencing and silently empty.
	// Separate self PK lookup from merged-source index lookup so SQLite never
	// falls back to scanning artists via an OR IN predicate.
	return `COALESCE(` +
		`(SELECT '/api/v1/artists/'||` + alias + `.id||'/image?v='||substr(ci.content_hash,1,12) FROM artist_custom_images ci WHERE ci.artist_id=` + alias + `.id LIMIT 1),` +
		`(SELECT '/api/v1/artists/'||` + alias + `.id||'/image?v='||substr(ci.content_hash,1,12) FROM artists ma JOIN artist_custom_images ci ON ci.artist_id=ma.id WHERE ma.merged_into_artist_id=` + alias + `.id ORDER BY ci.artist_id ASC LIMIT 1),` +
		`(SELECT '/api/v1/artists/'||` + alias + `.id||'/image?v='||substr(ai.content_hash,1,12) FROM artist_image_cache ai WHERE ai.artist_id=` + alias + `.id LIMIT 1),` +
		`(SELECT '/api/v1/artists/'||` + alias + `.id||'/image?v='||substr(ai.content_hash,1,12) FROM artists ma JOIN artist_image_cache ai ON ai.artist_id=ma.id WHERE ma.merged_into_artist_id=` + alias + `.id ORDER BY ai.fetched_at DESC LIMIT 1),` +
		`'')`
}

// ArtistImageURLs resolves display image URLs for the given artist IDs in a
// single query per chunk, reusing the canonical custom/cache image rule.
func (s *Store) ArtistImageURLs(ctx context.Context, artistIDs []int64) (map[int64]string, error) {
	result := make(map[int64]string, len(artistIDs))
	if len(artistIDs) == 0 {
		return result, nil
	}
	seen := make(map[int64]struct{}, len(artistIDs))
	unique := make([]int64, 0, len(artistIDs))
	for _, id := range artistIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	for start := 0; start < len(unique); start += 500 {
		end := min(start+500, len(unique))
		placeholders, args := inClause(unique[start:end])
		rows, err := s.db.QueryContext(ctx, `SELECT ar.id,`+artistImageURLSQL("ar")+` FROM artists ar WHERE ar.id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var url string
			if err = rows.Scan(&id, &url); err != nil {
				break
			}
			if url != "" {
				result[id] = url
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

type extraTrack interface {
	trackID() int64
	extras() *TrackExtras
	setNumbers(int, int)
}

func (t *Track) trackID() int64           { return t.ID }
func (t *Track) extras() *TrackExtras     { return &t.TrackExtras }
func (t *Track) setNumbers(d, n int)      { t.DiscNumber = d; t.TrackNumber = n }
func (t *SyncTrack) trackID() int64       { return t.ID }
func (t *SyncTrack) extras() *TrackExtras { return &t.TrackExtras }
func (t *SyncTrack) setNumbers(d, n int)  { t.DiscNumber = d; t.TrackNumber = n }

func hydrateTrackExtras[T extraTrack](ctx context.Context, s *Store, items []T) error {
	for start := 0; start < len(items); start += 500 {
		end := min(start+500, len(items))
		ids := make([]int64, 0, end-start)
		byID := make(map[int64][]T, end-start)
		for _, item := range items[start:end] {
			id := item.trackID()
			if _, ok := byID[id]; !ok {
				ids = append(ids, id)
			}
			byID[id] = append(byID[id], item)
		}
		placeholders, args := inClause(ids)
		rows, err := s.db.QueryContext(ctx, `SELECT t.id,COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),COALESCE(af.codec,''),COALESCE(af.sample_rate,0),COALESCE(af.bit_depth,0),COALESCE(af.bitrate,0),COALESCE(af.channels,0),COALESCE(pp.play_count,0),COALESCE(pp.skip_count,0),COALESCE(pp.last_played_at,''),CASE WHEN TRIM(COALESCE(t.lyrics,''))<>'' OR COALESCE(af.has_external_lrc,0)=1 THEN 1 ELSE 0 END,al.integrated_lufs,al.true_peak_db,al.loudness_range,abl.integrated_lufs,abl.true_peak_db FROM tracks t LEFT JOIN audio_files af ON af.id=(SELECT id FROM audio_files WHERE track_id=t.id AND status='available' ORDER BY id LIMIT 1) LEFT JOIN playback_progress pp ON pp.track_id=t.id LEFT JOIN audio_loudness al ON al.audio_file_id=af.id AND al.file_size=af.file_size AND al.modified_at_ns=af.modified_at_ns AND al.analyzer_version=? LEFT JOIN album_loudness abl ON abl.album_id=t.album_id WHERE t.id IN (`+placeholders+`)`, append([]any{LoudnessAnalyzerVersion}, args...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, plays, skips int64
			var d, n, hasLyrics int
			var date string
			var x TrackExtras
			var lufs, peak, lra, albumLufs, albumPeak sql.NullFloat64
			if err = rows.Scan(&id, &d, &n, &x.Codec, &x.SampleRate, &x.BitDepth, &x.BitrateKbps, &x.Channels, &plays, &skips, &date, &hasLyrics, &lufs, &peak, &lra, &albumLufs, &albumPeak); err != nil {
				break
			}
			if date != "" {
				if parsed, e := time.Parse(time.RFC3339Nano, date); e == nil {
					epoch := parsed.Unix()
					x.LastViewedAt = &epoch
					count := plays
					x.ViewCount = &count
					x.SkipCount = &skips
				}
			}
			if hasLyrics != 0 {
				x.LyricsURL = fmt.Sprintf("/api/v1/tracks/%d/lyrics.lrc", id)
			}
			x.LoudnessLUFS = nullFloat(lufs)
			x.TruePeakDB = nullFloat(peak)
			x.LoudnessRange = nullFloat(lra)
			x.AlbumLoudnessLUFS = nullFloat(albumLufs)
			x.AlbumTruePeakDB = nullFloat(albumPeak)
			for _, item := range byID[id] {
				*item.extras() = x
				item.setNumbers(d, n)
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) hydrateTracks(ctx context.Context, items []Track) error {
	ptrs := make([]*Track, len(items))
	for i := range items {
		ptrs[i] = &items[i]
	}
	if err := hydrateTrackExtras(ctx, s, ptrs); err != nil {
		return err
	}
	if err := s.hydrateTrackCredits(ctx, items); err != nil {
		return err
	}
	ids := make([]int64, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	refs, err := s.trackArtistRefs(ctx, ids)
	if err != nil {
		return err
	}
	for i := range items {
		for _, ref := range refs[items[i].ID] {
			items[i].Artists = append(items[i].Artists, Artist{ID: ref.ID, Name: ref.Name, JoinPhrase: ref.JoinPhrase})
		}
	}
	return s.hydrateTrackVersionCounts(ctx, items)
}
