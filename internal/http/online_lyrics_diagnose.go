package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lux032/032music-server/internal/lyricsonline"
	"github.com/lux032/032music-server/internal/storage"
)

// onlineLyricsDiagnoseTimeout is longer than the automatic lookup so slow sources still report.
const onlineLyricsDiagnoseTimeout = 30 * time.Second

type lyricsDiagnoseCandidate struct {
	lyricsonline.Candidate
	SourceName string
	Duration   string
	SyncedText string
}

type lyricsDiagnoseProvider struct {
	SourceName string
	Results    int
	Err        string
	ElapsedMs  int64
}

type lyricsDiagnosePageData struct {
	Chrome
	Enabled bool
	Query   string
	Matches []storage.LyricsTrackInfo

	Track        *storage.LyricsTrackInfo
	Duration     string
	SearchArtist string
	Instrumental bool
	// Local: which local lyrics exist ("同目录 .lrc", "内嵌歌词", "" for none).
	Local      string
	LocalLines int
	// LocalPreview: the first local lines, to spot empty or wrong lyrics that hide online lyrics.
	LocalPreview []string
	LocalErr     string
	Row       *storage.OnlineLyrics
	RowStatus string
	// Decision explains what GET /tracks/{id}/lyrics does for this track right now.
	Decision string

	Ran        bool
	Save       bool
	Outcome    string
	Providers  []lyricsDiagnoseProvider
	Candidates []lyricsDiagnoseCandidate
	Downloads  []string
}

func formatLyricsDuration(ms int64) string {
	if ms <= 0 {
		return "未知"
	}
	s := (ms + 500) / 1000
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// handleOnlineLyricsDiagnose: GET shows how a track's lyrics resolve; POST
// additionally runs a fresh online lookup (saved like an automatic one unless
// the track has a manual pick or was cleared).
func (a *App) handleOnlineLyricsDiagnose(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && !a.validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	session, _ := a.sessions.get(r)
	ctx := r.Context()
	data := lyricsDiagnosePageData{Chrome: a.chromeFor(ctx, session, "console"), Enabled: a.onlineLyrics != nil,
		Query: strings.TrimSpace(r.FormValue("q"))}
	if data.Query == "" {
		a.render(w, http.StatusOK, "online-lyrics-diagnose.html", data)
		return
	}
	trackID, _ := strconv.ParseInt(data.Query, 10, 64)
	if trackID <= 0 {
		matches, err := a.store.FindLyricsTracks(ctx, data.Query, 50)
		if err != nil {
			a.renderAdminFeatureError(w, "console", err)
			return
		}
		if len(matches) != 1 {
			data.Matches = matches
			a.render(w, http.StatusOK, "online-lyrics-diagnose.html", data)
			return
		}
		trackID = matches[0].TrackID
	}
	info, err := a.store.LyricsTrackInfo(ctx, trackID)
	if errors.Is(err, sql.ErrNoRows) {
		a.render(w, http.StatusOK, "online-lyrics-diagnose.html", data)
		return
	}
	if err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	data.Query = strconv.FormatInt(trackID, 10)
	if err := a.fillLyricsDiagnosis(ctx, &data, info); err != nil {
		a.renderAdminFeatureError(w, "console", err)
		return
	}
	if r.Method == http.MethodPost && a.onlineLyrics != nil {
		a.runLyricsDiagnosis(ctx, &data, info)
	}
	a.render(w, http.StatusOK, "online-lyrics-diagnose.html", data)
}

func (a *App) fillLyricsDiagnosis(ctx context.Context, data *lyricsDiagnosePageData, info storage.LyricsTrackInfo) error {
	data.Track = &info
	data.Duration = formatLyricsDuration(info.DurationMs)
	data.SearchArtist = lyricsonline.PrimaryArtist(info.Artist)
	data.Instrumental = info.Instrumental()
	local, err := a.localLyrics(ctx, info.TrackID)
	switch {
	case err == nil:
		data.Local = lyricsSourceName(local.Source)
		data.LocalLines = len(local.Result.Lines)
		for i, l := range local.Result.Lines {
			if i == 5 {
				break
			}
			text := []rune(strings.TrimSpace(l.Text))
			if len(text) > 80 {
				text = append(text[:80], '…')
			}
			data.LocalPreview = append(data.LocalPreview, fmt.Sprintf("[%s] %s", formatLyricsDuration(l.TimeMs), string(text)))
		}
	case !errors.Is(err, errNoLyrics):
		data.LocalErr = err.Error()
	}
	row, err := a.store.OnlineLyrics(ctx, info.TrackID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		data.Row = &row
		data.RowStatus = onlineLyricsStatusLabels[row.Status]
	}
	data.Decision = lyricsDecision(data, a.onlineLyrics != nil)
	return nil
}

// lyricsDecision mirrors resolveLyrics in words.
func lyricsDecision(d *lyricsDiagnosePageData, enabled bool) string {
	row := d.Row
	switch {
	case row != nil && row.Manual && row.Status == storage.OnlineLyricsFound:
		return "使用客户端手动选择的在线歌词。"
	case d.Local != "":
		return "使用本地歌词（" + d.Local + "），不会在线搜索。"
	case d.LocalErr != "":
		return "读取本地歌词出错：" + d.LocalErr
	case row != nil && row.Status == storage.OnlineLyricsFound:
		return "使用已缓存的在线歌词。"
	case row != nil && row.Status == storage.OnlineLyricsSuppressed:
		return "客户端清除过在线歌词，不再自动搜索（删除这条记录或在客户端重新选择可恢复）。"
	case row != nil && row.Status == storage.OnlineLyricsNotFound:
		if retry, err := time.Parse(time.RFC3339Nano, row.RetryAfter); err == nil && time.Now().Before(retry) {
			return "之前在线没找到，" + retry.Local().Format("01-02 15:04") + " 之前不会再搜索，播放时直接返回“没有歌词”。"
		}
		return "之前在线没找到，已过重试时间：下次播放会重新搜索。"
	case !enabled:
		return "在线歌词未启用。"
	case d.Instrumental:
		return "曲目类型为纯音乐 / 伴奏（" + d.Track.TrackType + "），不会在线搜索。类型来自标题、文件夹名或 AI 打标，判断错了可在曲目编辑里改回普通。"
	}
	return "没有缓存：下次播放时会在线搜索。"
}

func (a *App) runLyricsDiagnosis(ctx context.Context, data *lyricsDiagnosePageData, info storage.LyricsTrackInfo) {
	row := data.Row
	// A manual pick or a cleared track is the user's decision: diagnose without overwriting it.
	data.Save = row == nil || (!row.Manual && row.Status != storage.OnlineLyricsSuppressed)
	lookupCtx, cancel := context.WithTimeout(ctx, onlineLyricsDiagnoseTimeout)
	defer cancel()
	res, report, err := a.lookupOnlineLyrics(lookupCtx, info, data.Save)
	data.Ran = true
	for _, p := range report.Providers {
		v := lyricsDiagnoseProvider{SourceName: lyricsonline.SourceName(p.Source), Results: p.Results, ElapsedMs: p.ElapsedMs}
		if p.Err != nil {
			v.Err = p.Err.Error()
		}
		data.Providers = append(data.Providers, v)
	}
	for _, c := range report.Candidates {
		v := lyricsDiagnoseCandidate{Candidate: c, SourceName: lyricsonline.SourceName(c.Source), Duration: formatLyricsDuration(c.DurationMs), SyncedText: "未知"}
		if c.Synced != nil {
			v.SyncedText = map[bool]string{true: "有时间轴", false: "无时间轴"}[*c.Synced]
		}
		data.Candidates = append(data.Candidates, v)
	}
	data.Downloads = report.Downloads
	switch {
	case err == nil:
		data.Outcome = fmt.Sprintf("找到歌词：%s · %s - %s，%d 行。", lyricsonline.SourceName(res.Source), res.RemoteTitle, res.RemoteArtist, len(res.Result.Lines))
	case len(report.Providers) > 0 && len(report.Failed()) == len(report.Providers):
		data.Outcome = "所有来源都请求失败（网络、代理或被限流），见下方错误。"
	case len(report.Failed()) > 0:
		data.Outcome = "没有匹配的歌词，但有来源请求失败：结果可能不准，30 分钟后会再试。"
	default:
		data.Outcome = "来源都正常返回了，但没有可自动使用的结果（标题、艺人或时长对不上），12 小时后再试。可以在客户端手动搜索选择。"
	}
	if !data.Save {
		data.Outcome += "（这首歌是手动选择或已清除，本次结果没有保存。）"
	} else if a.fillLyricsDiagnosis(ctx, data, info) != nil {
		data.Decision = ""
	}
}
