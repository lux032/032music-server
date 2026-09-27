package httpapi

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
	if source == "manual" {
		return "手动"
	}
	if source == "album" {
		return "专辑"
	}
	return "自动"
}
