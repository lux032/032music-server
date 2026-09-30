package storage

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// 发行类型（专辑/单曲/EP/合辑…）的判定。
//
// 优先级：用户手动纠正 > 文件标签/外部补全写入的类型 > 本地推断。
// 本地推断只用于“没有任何类型来源”的专辑，结果不写回 album_type，
// 仅以 Album.ReleaseKind / SyncAlbum.ReleaseKind 形式返回；album_type 的
// 原语义保持不变（作品页 R2 规则仍只认 AlbumType=='single'）。

// albumTypeSourceTag / albumTypeSourceEnrichment 是 albums.album_type_source
// 的取值；NULL 表示类型未知（album_type 只是默认的 'album'）。
const (
	albumTypeSourceTag        = "tag"
	albumTypeSourceEnrichment = "enrichment"
)

// releaseTypeTagKeys 覆盖各容器的类型标签写法：
//   - FLAC/Vorbis：jaudiotagger（MusicAutoTagger）写 MUSICBRAINZ_ALBUMTYPE，Picard 写 RELEASETYPE；
//   - MP3：TXXX:MusicBrainz Album Type（reader 已按描述展开为同名 key）；
//   - M4A：----:com.apple.iTunes:MusicBrainz Album Type。
var releaseTypeTagKeys = []string{"RELEASETYPE", "MUSICBRAINZ_ALBUMTYPE", "MUSICBRAINZ ALBUM TYPE", "MUSICBRAINZ_ALBUM_TYPE", "ALBUMTYPE", "RELEASE TYPE"}

// compilationTagKeys：Vorbis COMPILATION、ID3 TCMP、MP4 cpil。
var compilationTagKeys = []string{"COMPILATION", "TCMP", "CPIL"}

// releaseTypeTokenKinds 把 MusicBrainz 主/次类型映射成本库类型；未列出的
// 次类型（remix、dj-mix、demo…）忽略，由主类型决定。
var releaseTypeTokenKinds = map[string]string{
	"album":       "album",
	"single":      "single",
	"ep":          "ep",
	"compilation": "compilation",
	"live":        "live",
	"soundtrack":  "soundtrack",
	"bootleg":     "bootleg",
	"other":       "other",
	"broadcast":   "other",
	"spokenword":  "other",
	"audiobook":   "other",
	"interview":   "other",
	"audio drama": "other",
}

// releaseTypePriority：多值标签（如 "album; compilation"、"single; soundtrack"）
// 取优先级最高的一个。合辑/现场优先于主类型；单曲/EP 优先于原声，使动画
// OP/ED 单曲归入“单曲与 EP”。
var releaseTypePriority = []string{"compilation", "live", "single", "ep", "soundtrack", "bootleg", "album", "other"}

// inferAlbumType 从文件标签解析发行类型。tagged=false 表示文件里没有可识别
// 的类型信息，此时返回默认的 "album"，调用方应把来源记为 NULL。
//
// MusicAutoTagger/Picard 只把 MusicBrainz 主类型写进类型字段，合辑（次类型）
// 单独写成 COMPILATION/TCMP/cpil=1，因此合辑标记与类型值一起参与优先级判定。
func inferAlbumType(raw map[string][]string) (kind string, tagged bool) {
	values := rawValues(raw, releaseTypeTagKeys...)
	if truthyTag(rawValues(raw, compilationTagKeys...)) {
		values = append(append([]string(nil), values...), "compilation")
	}
	if kind = parseReleaseTypeValues(values); kind != "" {
		return kind, true
	}
	return "album", false
}

func rawValues(raw map[string][]string, keys ...string) []string {
	for _, key := range keys {
		if values := raw[key]; len(values) > 0 {
			return values
		}
	}
	return nil
}

func parseReleaseTypeValues(values []string) string {
	found := map[string]bool{}
	for _, value := range values {
		// MP4 自定义字段的 data 子 atom 会带 NUL 前缀/分隔，统一当分隔符。
		value = strings.ToLower(strings.ReplaceAll(value, "\x00", ";"))
		for _, token := range strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' || r == '/' || r == '|' }) {
			token = strings.Join(strings.Fields(token), " ")
			if kind, ok := releaseTypeTokenKinds[token]; ok {
				found[kind] = true
			}
		}
	}
	for _, kind := range releaseTypePriority {
		if found[kind] {
			return kind
		}
	}
	return ""
}

func truthyTag(values []string) bool {
	for _, value := range values {
		switch strings.ToLower(strings.Trim(value, "\x00 \t")) {
		case "1", "true", "yes":
			return true
		}
	}
	return false
}

// releaseKindInput 是判定一张专辑生效发行类型所需的全部输入。
type releaseKindInput struct {
	UserType   string // user_album_type（手动纠正）
	StoredType string // album_type
	Source     string // album_type_source：tag / enrichment / ""
	Title      string
	DiscCount  int
	// CoreTracks / DurationMillis 不含伴奏/off vocal/TV size 等衍生版本；
	// TotalTracks 为全部曲目。
	CoreTracks     int64
	TotalTracks    int64
	DurationMillis int64
}

const releaseKindLongPlayMillis = 30 * 60 * 1000

// resolveReleaseKind 返回专辑生效的发行类型。
func resolveReleaseKind(in releaseKindInput) string {
	if user := strings.ToLower(strings.TrimSpace(in.UserType)); user != "" {
		return user
	}
	if strings.TrimSpace(in.Source) != "" {
		if stored := strings.ToLower(strings.TrimSpace(in.StoredType)); stored != "" {
			return stored
		}
	}
	return inferReleaseKind(in)
}

var (
	titleLivePattern        = regexp.MustCompile(`(?:^|[^A-Z0-9])LIVE(?:$|[^A-Z0-9!])|\bCONCERT\b|现场|現場|ライブ|ライヴ`)
	titleCompilationPattern = regexp.MustCompile(`^(?:THE )?BEST$|\bBEST OF\b|\bBEST ALBUM\b|\bGREATEST HITS\b|\bANTHOLOGY\b|ベスト|精选|精選|合集|合輯|合辑`)
	titleSinglePattern      = regexp.MustCompile(`(?:^|[^A-Z0-9])SINGLE(?:$|[^A-Z0-9])|シングル`)
	titleEPPattern          = regexp.MustCompile(`(?:^|[^A-Z0-9])EP(?:$|[^A-Z0-9])|\bMINI[ -]?ALBUM\b|ミニアルバム|ミニ・アルバム`)
	// 作品名里的 “Live” 不代表现场（ラブライブ! / Love Live! / Date A Live …），
	// 先剔除再匹配。
	titleLiveFalsePositive = regexp.MustCompile(`ラブライブ|LOVE ?LIVE|DATE A LIVE|デート・ア・ライブ`)
)

// inferReleaseKind：没有任何类型来源时的本地推断（参考 MusicBridge
// AlbumCategory，并针对日系单曲与长篇作品做了修正）。
func inferReleaseKind(in releaseKindInput) string {
	title := strings.ToUpper(strings.TrimSpace(norm.NFKC.String(in.Title)))
	if title != "" {
		if titleLivePattern.MatchString(titleLiveFalsePositive.ReplaceAllString(title, " ")) {
			return "live"
		}
		if titleCompilationPattern.MatchString(title) {
			return "compilation"
		}
		if titleSinglePattern.MatchString(title) {
			return "single"
		}
		if titleEPPattern.MatchString(title) {
			return "ep"
		}
	}
	if in.DiscCount > 1 {
		return "album"
	}
	if in.DurationMillis >= releaseKindLongPlayMillis {
		return "album"
	}
	count := in.CoreTracks
	if count <= 0 {
		count = in.TotalTracks
	}
	switch {
	case count <= 0:
		return "album"
	case count <= 3:
		return "single"
	case count <= 6:
		return "ep"
	default:
		return "album"
	}
}

// releaseKindCoreTracksSQL / releaseKindDurationSQL：推断输入的相关子查询
// （外层专辑别名 a）。伴奏、off vocal、TV size 视为同曲衍生版本，不计入。
const releaseKindCoreTracksSQL = `(SELECT COUNT(*) FROM tracks rk WHERE rk.album_id=a.id AND COALESCE(rk.user_track_type,rk.track_type,'regular') NOT IN ('instrumental','off_vocal','tv_size'))`
const releaseKindDurationSQL = `(SELECT COALESCE(SUM(rk.duration_ms),0) FROM tracks rk WHERE rk.album_id=a.id AND COALESCE(rk.user_track_type,rk.track_type,'regular') NOT IN ('instrumental','off_vocal','tv_size'))`
