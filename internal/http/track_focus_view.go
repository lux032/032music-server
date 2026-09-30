package httpapi

import (
	"net/url"
	"sort"
	"strconv"

	"github.com/lux032/032music-server/internal/storage"
)

func creditPeople(t storage.Track, role string) []storage.ArtistRef {
	for _, c := range t.Credits {
		if c.Role == role {
			return c.Artists
		}
	}
	return nil
}

type focusOption struct {
	Value, Label string
	Selected     bool
}
type focusGroup struct {
	Name, Label string
	Multiple    bool
	Options     []focusOption
}

func focusLabel(name, v string) string {
	labels := map[string]string{"regular": "普通曲目", "instrumental": "伴奏", "off_vocal": "Off Vocal", "tv_size": "TV Size", "drama_track": "广播剧", "remix": "混音", "op": "OP", "ed": "ED", "insert": "插曲", "theme": "主题曲", "character": "角色歌", "ost": "原声", "image_song": "印象曲", "other": "其他", "anime": "TV 动画", "movie": "剧场版", "game": "游戏", "drama": "电视剧", "commercial": "广告", "lossless": "无损", "hires": "Hi-Res", "lossy": "有损"}
	label := labels[v]
	if label == "" {
		label = v
	}
	switch name {
	case "decade":
		return v + " 年代"
	case "yearFrom":
		return "起始年份：" + v
	case "yearTo":
		return "结束年份：" + v
	case "excludeTypes":
		return "排除：" + label
	case "hideInstrumental":
		return "排除伴奏"
	case "format":
		return "格式：" + v
	case "credit":
		return "幕后：" + v
	}
	return label
}
func trackFocusGroups(q url.Values, years []int) []focusGroup {
	ds := map[int]bool{}
	for _, y := range years {
		if y > 0 {
			ds[y/10*10] = true
		}
	}
	decades := []int{}
	for d := range ds {
		decades = append(decades, d)
	}
	sort.Ints(decades)
	dv := []string{}
	for _, d := range decades {
		dv = append(dv, strconv.Itoa(d))
	}
	groups := []focusGroup{}
	for _, spec := range []struct {
		name, label string
		values      []string
		multiple    bool
	}{{"decade", "年代", dv, true}, {"trackType", "曲目类型", []string{"regular", "instrumental", "off_vocal", "tv_size", "drama_track", "remix"}, true}, {"excludeTypes", "排除类型", []string{"regular", "instrumental", "off_vocal", "tv_size", "drama_track", "remix"}, true}, {"tieupRole", "主题曲用途", []string{"op", "ed", "insert", "theme", "character", "ost", "image_song", "other"}, true}, {"workType", "作品类型", []string{"anime", "movie", "game", "drama", "commercial", "other"}, true}, {"quality", "音质", []string{"", "lossless", "hires", "lossy"}, false}, {"format", "格式", []string{"flac", "alac", "aac", "mp3", "opus", "vorbis"}, true}} {
		g := focusGroup{Name: spec.name, Label: spec.label, Multiple: spec.multiple}
		selected := focusValues(q, spec.name)
		for _, v := range spec.values {
			on := false
			for _, s := range selected {
				if s == v {
					on = true
				}
			}
			label := focusLabel(spec.name, v)
			if v == "" {
				label = "全部"
			}
			g.Options = append(g.Options, focusOption{v, label, on})
		}
		groups = append(groups, g)
	}
	return groups
}
