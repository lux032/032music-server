package storage

import "context"

// TranscodeProperties retrieves source properties for output bit depth and rate selection.
func (s *Store) TranscodeProperties(ctx context.Context, trackID int64) (duration int64, rate, depth int, lossless bool, err error) {
	var codec string
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(t.duration_ms,0),COALESCE(af.sample_rate,0),COALESCE(af.bit_depth,0),COALESCE(af.codec,'') FROM tracks t JOIN audio_files af ON af.track_id=t.id WHERE t.id=? AND af.status='available' ORDER BY af.id LIMIT 1`, trackID).Scan(&duration, &rate, &depth, &codec)
	lossless = codec == "flac" || codec == "alac" || codec == "wav" || codec == "pcm_s16le" || codec == "pcm_s24le"
	return
}
