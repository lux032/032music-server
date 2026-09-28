package httpapi

import (
	"sort"
	"strings"

	"github.com/lux032/032music-server/internal/storage"
)

func workRoleLabel(role string) string {
	switch role {
	case "ost":
		return "原声集"
	case "op":
		return "片头曲"
	case "ed":
		return "片尾曲"
	case "insert":
		return "插入歌"
	case "character":
		return "角色歌"
	case "theme":
		return "主题曲"
	case "image_song":
		return "印象曲"
	default:
		return "相关"
	}
}

func workSourceLabel(source string) string {
	if source == "bangumi" {
		return "Bangumi"
	}
	if source == "manual" {
		return "手动"
	}
	if source == "album" {
		return "专辑"
	}
	return "自动"
}

func workRoleShortLabel(role string) string {
	switch role {
	case "op":
		return "OP"
	case "ed":
		return "ED"
	case "insert":
		return "插入歌"
	case "theme":
		return "主题歌"
	case "character":
		return "角色歌"
	case "image_song":
		return "印象曲"
	case "ost":
		return "OST"
	default:
		return "相关"
	}
}

func workRoleBadgeLabel(role string) string {
	switch role {
	case "op":
		return "OP · 片头曲"
	case "ed":
		return "ED · 片尾曲"
	case "insert":
		return "插入歌"
	case "theme":
		return "主题曲"
	case "character":
		return "角色歌"
	case "image_song":
		return "印象曲"
	case "ost":
		return "OST · 原声集"
	default:
		return "相关"
	}
}

func matchKindLabel(kind string) string {
	switch kind {
	case "exact":
		return "完全一致匹配"
	case "suffix":
		return "去后缀继承匹配"
	case "multi_title":
		return "多曲名条目 (A / B)"
	default:
		return "仅泛关系"
	}
}

func matchKindClass(kind string) string {
	switch kind {
	case "exact":
		return "exact"
	case "suffix":
		return "suffix"
	case "multi_title":
		return "multi"
	default:
		return "generic"
	}
}

// workAlbumUsageLabel 是专辑页胶囊与作品页角标共用的用途合并函数（D47/D48）：
// 专辑级 role='ost' 与曲目级用途同时存在时两者合并显示（如 "OST · OP"）；
// 只有专辑级 ost 时显示 "OST"；只有曲目级用途时显示用途；只有专辑级 other 时不显示用途。
// 曲目级用途先按优先级排序（与 TrackWorksForAlbum 的 CASE 顺序一致）再去重。
func workAlbumUsageLabel(albumRole string, trackRoles []string) string {
	sorted := append([]string(nil), trackRoles...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return storage.WorkRoleRank(sorted[i]) < storage.WorkRoleRank(sorted[j])
	})
	parts := make([]string, 0, len(sorted)+1)
	if albumRole == "ost" {
		parts = append(parts, "OST")
	}
	for _, r := range sorted {
		// 曲目级 other 不是有效用途，不参与合并（否则会出现 "相关 专辑" 这类拼接）。
		if r == "other" || r == "" {
			continue
		}
		short := workRoleShortLabel(r)
		dup := false
		for _, p := range parts {
			if p == short {
				dup = true
				break
			}
		}
		if !dup {
			parts = append(parts, short)
		}
	}
	return strings.Join(parts, " · ")
}

// workAlbumIsOST（D48）：OST 专辑的身份只由 album_works.role='ost' 决定；
// 曲目级 role='ost' 只是该曲目的用途，不能推出专辑是 OST。
func workAlbumIsOST(albumRole string) bool {
	return albumRole == "ost"
}

func workAlbumRelationLabel(aw storage.AlbumWork) string {
	// D45/D48：OST 身份只认 album_works.role='ost'（AlbumRole 即原值），
	// album_type=soundtrack 不能推出 OST。
	// 专辑级是 ost 且曲目级没有 ost 以外的用途时，统一显示“原声集 OST”，
	// 不出现 "OST 专辑"。
	meaningful := make([]string, 0, len(aw.TrackRoles))
	for _, r := range aw.TrackRoles {
		if r == "other" || r == "" {
			continue
		}
		if r == "ost" && workAlbumIsOST(aw.AlbumRole) {
			continue // 专辑级 ost 已覆盖曲目级 ost 的语义
		}
		meaningful = append(meaningful, r)
	}
	if workAlbumIsOST(aw.AlbumRole) && len(meaningful) == 0 {
		return "原声集 OST"
	}
	// R2：不加本地推测启发式，“单曲”只看 AlbumType=='single'。
	suffix := "专辑"
	if aw.AlbumType == "single" {
		suffix = "单曲"
	}
	// D47：专辑级 ost 与曲目级用途合并显示（如 "OST · OP 单曲"）。
	if usage := workAlbumUsageLabel(aw.AlbumRole, meaningful); usage != "" {
		return usage + " " + suffix
	}
	// R4：用途记在曲目上；只有专辑级 other 时不显示用途。
	return "相关" + suffix
}
