package storage

import (
	"context"
	"fmt"
	"time"
)

// TrackExtras is shared by full and sync track responses. audio_files.bitrate is kbps.
type TrackExtras struct {
	Codec        string `json:"codec,omitempty"`
	SampleRate   int    `json:"sampleRate,omitempty"`
	BitDepth     int    `json:"bitDepth,omitempty"`
	BitrateKbps  int    `json:"bitrateKbps,omitempty"`
	Channels     int    `json:"channels,omitempty"`
	ViewCount    *int64 `json:"viewCount,omitempty"`
	SkipCount    *int64 `json:"skipCount,omitempty"`
	LastViewedAt *int64 `json:"lastViewedAt,omitempty"`
	LyricsURL    string `json:"lyricsUrl,omitempty"`
}

const trackArtistSQL = `COALESCE((SELECT GROUP_CONCAT(name, ', ') FROM (SELECT COALESCE(ar.user_display_name,ar.display_name) name FROM track_artists ta JOIN artists ar ON ar.id=ta.artist_id WHERE ta.track_id=t.id AND ta.role='primary' ORDER BY ta.position,ar.id)),(SELECT GROUP_CONCAT(name, ', ') FROM (SELECT COALESCE(ar.user_display_name,ar.display_name) name FROM album_artists aa JOIN artists ar ON ar.id=aa.artist_id WHERE aa.album_id=a.id ORDER BY aa.position,ar.id)),a.user_performed_by,a.performed_by,'Unknown Artist')`

const albumArtworkURLSQL = `COALESCE((SELECT '/api/v1/artwork/'||aw_art.id FROM artworks aw_art WHERE aw_art.album_id=a.id ORDER BY aw_art.is_primary DESC,aw_art.id LIMIT 1),'')`

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
		rows, err := s.db.QueryContext(ctx, `SELECT t.id,COALESCE(t.user_disc_number,t.disc_number),COALESCE(t.user_track_number,t.track_number),COALESCE(af.codec,''),COALESCE(af.sample_rate,0),COALESCE(af.bit_depth,0),COALESCE(af.bitrate,0),COALESCE(af.channels,0),COALESCE(pp.play_count,0),COALESCE(pp.skip_count,0),COALESCE(pp.last_played_at,''),CASE WHEN TRIM(COALESCE(t.lyrics,''))<>'' OR COALESCE(af.has_external_lrc,0)=1 THEN 1 ELSE 0 END FROM tracks t LEFT JOIN audio_files af ON af.id=(SELECT id FROM audio_files WHERE track_id=t.id AND status='available' ORDER BY id LIMIT 1) LEFT JOIN playback_progress pp ON pp.track_id=t.id WHERE t.id IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, plays, skips int64
			var d, n, hasLyrics int
			var date string
			var x TrackExtras
			if err = rows.Scan(&id, &d, &n, &x.Codec, &x.SampleRate, &x.BitDepth, &x.BitrateKbps, &x.Channels, &plays, &skips, &date, &hasLyrics); err != nil {
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
	return hydrateTrackExtras(ctx, s, ptrs)
}
