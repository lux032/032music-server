package httpapi

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/lux032/032music-server/internal/lyrics"
	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/storage"
)

// Lyrics sources reported to clients besides the online source ids.
const (
	lyricsSourceFile     = "file"
	lyricsSourceEmbedded = "embedded"
)

const (
	// onlineLyricsRetry is how long a "not found online" result is remembered.
	onlineLyricsRetry = 12 * time.Hour
	// onlineLyricsTimeout bounds one automatic online lookup (all sources).
	onlineLyricsTimeout = 20 * time.Second
	// onlineLyricsIncompleteRetry: nothing matched while a source was failing,
	// so the miss may be wrong; retry soon without searching on every play.
	onlineLyricsIncompleteRetry = 30 * time.Minute
)

// SetOnlineLyrics enables NetEase / LRCLIB lookups for tracks without local lyrics.
func (a *App) SetOnlineLyrics(client *lyricsonline.Client) { a.onlineLyrics = client }

var errNoLyrics = errors.New("no lyrics")

// resolvedLyrics is the lyrics a track resolves to.
type resolvedLyrics struct {
	Result       lyrics.LyricsResult
	Raw          string // original text (LRC or plain)
	Source       string // file | embedded | netease | lrclib
	Manual       bool
	RemoteTitle  string
	RemoteArtist string
	// TranslationLRC is the source's own translation (NetEase).
	TranslationLRC string
}

func parseLyricsText(raw string) lyrics.LyricsResult {
	result := lyrics.ParseLRC(raw)
	if len(result.Lines) == 0 && strings.TrimSpace(raw) != "" {
		// Treat raw text as unsynced single-line lyrics
		result = lyrics.LyricsResult{Synced: false, Lines: []lyrics.LyricLine{{Text: raw}}}
	}
	return result
}

func fromOnlineRow(row storage.OnlineLyrics) resolvedLyrics {
	return resolvedLyrics{Result: parseLyricsText(row.Lyrics), Raw: row.Lyrics, Source: row.Source, Manual: row.Manual,
		RemoteTitle: row.RemoteTitle, RemoteArtist: row.RemoteArtist, TranslationLRC: row.TranslationLyrics}
}

// localLyrics picks between the sidecar .lrc and the embedded tag: synced
// beats unsynced, then the sidecar beats the tag. errNoLyrics when neither has lyrics.
func (a *App) localLyrics(ctx context.Context, trackID int64) (resolvedLyrics, error) {
	var sidecar *resolvedLyrics
	audioPath, err := a.store.AudioFilePath(ctx, trackID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return resolvedLyrics{}, err
	}
	if err == nil {
		if lrcPath := lyrics.DetectLRCPath(audioPath); lrcPath != "" {
			content, readErr := readExternalLRC(lrcPath)
			if errors.Is(readErr, errExternalLRCTooLarge) {
				return resolvedLyrics{}, readErr
			}
			if readErr == nil {
				raw := string(normalizeLyricsBOM(content))
				if result := lyrics.ParseLRC(raw); len(result.Lines) > 0 {
					sidecar = &resolvedLyrics{Result: result, Raw: raw, Source: lyricsSourceFile}
					if result.Synced {
						return *sidecar, nil
					}
				}
			}
		}
	}
	embedded, err := a.store.TrackLyrics(ctx, trackID)
	if err != nil {
		return resolvedLyrics{}, err
	}
	embedded = string(normalizeLyricsBOM([]byte(embedded)))
	if strings.TrimSpace(embedded) != "" {
		if res := parseLyricsText(embedded); res.Synced || sidecar == nil {
			return resolvedLyrics{Result: res, Raw: embedded, Source: lyricsSourceEmbedded}, nil
		}
	}
	if sidecar != nil {
		return *sidecar, nil
	}
	return resolvedLyrics{}, errNoLyrics
}

// resolveLyrics returns a track's lyrics: a user's manual choice, then synced
// local lyrics (sidecar .lrc, embedded tag), then the online cache, then a
// fresh online lookup (cached, misses remembered for onlineLyricsRetry).
// Local lyrics without timestamps do not stop the online lookup: they are
// returned right away while the lookup runs in the background, and synced
// online lyrics replace them from the next request on. Unsynced online lyrics
// never replace local ones.
// Errors: sql.ErrNoRows (no such track), errExternalLRCTooLarge, errNoLyrics.
func (a *App) resolveLyrics(ctx context.Context, trackID int64) (resolvedLyrics, error) {
	info, err := a.store.LyricsTrackInfo(ctx, trackID)
	if err != nil {
		return resolvedLyrics{}, err
	}
	row, err := a.store.OnlineLyrics(ctx, trackID)
	hasRow := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return resolvedLyrics{}, err
	}
	if hasRow && row.Manual && row.Status == storage.OnlineLyricsFound {
		return fromOnlineRow(row), nil
	}
	local, err := a.localLyrics(ctx, trackID)
	if err != nil && !errors.Is(err, errNoLyrics) {
		return resolvedLyrics{}, err
	}
	hasLocal := err == nil
	if hasLocal && local.Result.Synced {
		return local, nil
	}
	// pick prefers online lyrics only when they are synced or nothing local exists.
	pick := func(online resolvedLyrics) resolvedLyrics {
		if hasLocal && !online.Result.Synced {
			return local
		}
		return online
	}
	orLocal := func() (resolvedLyrics, error) {
		if hasLocal {
			return local, nil
		}
		return resolvedLyrics{}, errNoLyrics
	}
	if hasRow {
		switch row.Status {
		case storage.OnlineLyricsFound:
			return pick(fromOnlineRow(row)), nil
		case storage.OnlineLyricsSuppressed:
			a.logger.Debug("online lyrics skipped: cleared by a client", "trackId", trackID)
			return orLocal()
		case storage.OnlineLyricsNotFound:
			if retry, err := time.Parse(time.RFC3339Nano, row.RetryAfter); err == nil && time.Now().Before(retry) {
				a.logger.Debug("online lyrics skipped: remembered miss", "trackId", trackID, "retryAfter", row.RetryAfter)
				return orLocal()
			}
		}
	}
	if a.onlineLyrics == nil {
		return orLocal()
	}
	if info.Instrumental() {
		a.logger.Info("online lyrics skipped: instrumental track", "trackId", trackID, "title", info.Title, "trackType", info.TrackType)
		return orLocal()
	}
	if hasLocal {
		// Do not make the client wait for lyrics it can already show.
		a.logger.Info("local lyrics have no timestamps, searching online in the background", "trackId", trackID, "localSource", local.Source)
		a.lyricsBackground.Add(1)
		go func() {
			defer a.lyricsBackground.Done()
			_, _ = a.sharedOnlineLookup(ctx, info)
		}()
		return local, nil
	}
	online, err := a.sharedOnlineLookup(ctx, info)
	if err != nil {
		if errors.Is(err, errNoLyrics) {
			return orLocal()
		}
		return resolvedLyrics{}, err
	}
	return pick(online), nil
}

// sharedOnlineLookup runs one online lookup per track at a time (concurrent
// callers share it). It finishes and caches even when ctx is canceled, e.g.
// when the client skips to the next song.
func (a *App) sharedOnlineLookup(ctx context.Context, info storage.LyricsTrackInfo) (resolvedLyrics, error) {
	return a.lyricsFlights.do("track:"+strconv.FormatInt(info.TrackID, 10), func() (resolvedLyrics, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), onlineLyricsTimeout)
		defer cancel()
		return a.fetchOnlineLyrics(fetchCtx, info)
	})
}

func lyricsQueryFor(info storage.LyricsTrackInfo) lyricsonline.Query {
	return lyricsonline.Query{Title: info.Title, Artist: info.Artist, Album: info.Album, DurationMs: info.DurationMs}
}

func (a *App) fetchOnlineLyrics(ctx context.Context, info storage.LyricsTrackInfo) (resolvedLyrics, error) {
	res, _, err := a.lookupOnlineLyrics(ctx, info, true)
	return res, err
}

// lookupOnlineLyrics searches online, logs the outcome and, when save is set,
// caches it (found, or a miss remembered for onlineLyricsRetry /
// onlineLyricsIncompleteRetry). The report explains what each source answered.
func (a *App) lookupOnlineLyrics(ctx context.Context, info storage.LyricsTrackInfo, save bool) (resolvedLyrics, lyricsonline.Report, error) {
	start := time.Now()
	found, cand, report, err := a.onlineLyrics.Lookup(ctx, lyricsQueryFor(info))
	logArgs := []any{"trackId", info.TrackID, "title", info.Title, "artist", info.Artist, "durationMs", info.DurationMs,
		"elapsedMs", time.Since(start).Milliseconds(), "report", report.Summary()}
	switch {
	case errors.Is(err, lyricsonline.ErrNotFound):
		a.logger.Info("online lyrics not found", logArgs...)
		if !save {
			return resolvedLyrics{}, report, errNoLyrics
		}
		if err := a.store.SaveOnlineLyricsNotFound(ctx, info.TrackID, time.Now().Add(onlineLyricsRetry)); err != nil {
			a.logger.Error("save online lyrics miss", "trackId", info.TrackID, "error", err)
		}
		return resolvedLyrics{}, report, errNoLyrics
	case errors.Is(err, lyricsonline.ErrIncomplete):
		a.logger.Warn("online lyrics lookup incomplete", append(logArgs, "error", err)...)
		if !save {
			return resolvedLyrics{}, report, errNoLyrics
		}
		if err := a.store.SaveOnlineLyricsNotFound(ctx, info.TrackID, time.Now().Add(onlineLyricsIncompleteRetry)); err != nil {
			a.logger.Error("save online lyrics miss", "trackId", info.TrackID, "error", err)
		}
		return resolvedLyrics{}, report, errNoLyrics
	case err != nil:
		// Network / rate limit: not cached, the next request tries again.
		a.logger.Warn("online lyrics lookup failed", append(logArgs, "error", err)...)
		return resolvedLyrics{}, report, errNoLyrics
	}
	a.logger.Info("online lyrics found", append(logArgs, "source", found.Source, "remoteId", found.ID)...)
	row := storage.OnlineLyrics{TrackID: info.TrackID, Source: found.Source, RemoteID: found.ID, RemoteTitle: cand.Title,
		RemoteArtist: cand.Artist, Lyrics: found.LRC, TranslationLyrics: found.TranslationLRC}
	if !save {
		return fromOnlineRow(row), report, nil
	}
	if err := a.store.SaveOnlineLyricsFound(ctx, row); err != nil {
		a.logger.Error("save online lyrics", "trackId", info.TrackID, "error", err)
	}
	return fromOnlineRow(row), report, nil
}

type apiLyricsResponse struct {
	Synced       bool               `json:"synced"`
	Lines        []lyrics.LyricLine `json:"lines"`
	Source       string             `json:"source"`
	SourceName   string             `json:"sourceName"`
	Manual       bool               `json:"manual"`
	RemoteTitle  string             `json:"remoteTitle,omitempty"`
	RemoteArtist string             `json:"remoteArtist,omitempty"`
}

func lyricsSourceName(source string) string {
	switch source {
	case lyricsSourceFile:
		return "LRC 文件"
	case lyricsSourceEmbedded:
		return "内嵌歌词"
	}
	return lyricsonline.SourceName(source)
}

func lyricsResponse(r resolvedLyrics) apiLyricsResponse {
	lines := r.Result.Lines
	if lines == nil {
		lines = []lyrics.LyricLine{}
	}
	return apiLyricsResponse{Synced: r.Result.Synced, Lines: lines, Source: r.Source, SourceName: lyricsSourceName(r.Source),
		Manual: r.Manual, RemoteTitle: r.RemoteTitle, RemoteArtist: r.RemoteArtist}
}

// writeLyricsError maps resolveLyrics errors to API errors; it returns false when err is nil.
func (a *App) writeLyricsError(w http.ResponseWriter, trackID int64, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, sql.ErrNoRows):
		writeAPIError(w, http.StatusNotFound, "not_found", "Track not found.")
	case errors.Is(err, errExternalLRCTooLarge):
		writeAPIError(w, http.StatusRequestEntityTooLarge, "lyrics_too_large", "External lyrics exceed 1 MiB.")
	case errors.Is(err, errNoLyrics):
		writeAPIError(w, http.StatusNotFound, "no_lyrics", "No lyrics available for this track.")
	default:
		a.logger.Error("resolve lyrics", "trackId", trackID, "error", err)
		writeAPIError(w, http.StatusInternalServerError, "query_failed", err.Error())
	}
	return true
}

func (a *App) handleAPITrackLyrics(w http.ResponseWriter, r *http.Request) {
	trackID := parseInt64(r.PathValue("id"))
	res, err := a.resolveLyrics(r.Context(), trackID)
	if a.writeLyricsError(w, trackID, err) {
		return
	}
	writeJSON(w, http.StatusOK, lyricsResponse(res))
}

// Large sidecars are rejected rather than read into unbounded memory.
const maxExternalLRCBytes = 1 << 20

var errExternalLRCTooLarge = errors.New("external lyrics exceed 1 MiB")

func readExternalLRC(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil {
		return nil, err
	} else if info.Size() > maxExternalLRCBytes {
		return nil, errExternalLRCTooLarge
	}
	content, err := io.ReadAll(io.LimitReader(file, maxExternalLRCBytes+1))
	if err == nil && len(content) > maxExternalLRCBytes {
		return nil, errExternalLRCTooLarge
	}
	return content, err
}

// handleAPITrackLyricsText returns the resolved lyrics as raw text (LRC when synced).
func (a *App) handleAPITrackLyricsText(w http.ResponseWriter, r *http.Request) {
	trackID := parseInt64(r.PathValue("id"))
	res, err := a.resolveLyrics(r.Context(), trackID)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, errNoLyrics) {
		writeAPIError(w, http.StatusNotFound, "no_lyrics", "No lyrics available for this track.")
		return
	}
	if errors.Is(err, errExternalLRCTooLarge) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "lyrics_too_large", "External lyrics exceed 1 MiB.")
		return
	}
	if err != nil {
		a.writeFeatureError(w, r, err, "query_failed")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(res.Raw))
}

func normalizeLyricsBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		return b[3:]
	}
	if len(b) < 2 || !((b[0] == 0xff && b[1] == 0xfe) || (b[0] == 0xfe && b[1] == 0xff)) {
		return b
	}
	var order binary.ByteOrder = binary.LittleEndian
	if b[0] == 0xfe {
		order = binary.BigEndian
	}
	units := make([]uint16, 0, (len(b)-2)/2)
	for i := 2; i+1 < len(b); i += 2 {
		units = append(units, order.Uint16(b[i:i+2]))
	}
	return []byte(string(utf16.Decode(units)))
}
