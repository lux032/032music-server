// Package tagging 实现 AI 融合打标：把声学证据、元数据和歌词片段交给 OpenAI 兼容的模型，
// 让它只从固定词表（docs/daily-mix-design.md 第 2.2 节）中选择情绪与风格。模型不选曲、
// 不生成曲目 ID；输出一律在服务端校验。
package tagging

// VocabVersion 在词表（标签集合或定义）变化时递增，会触发全库重新打标。
const VocabVersion = 1

// PromptVersion 在提示词或输入结构有实质变化时递增，会触发全库重新打标。
const PromptVersion = 1

// Tag 是词表中的一个标签。
type Tag struct {
	ID    string
	Label string // 中文名（界面显示）
	Hint  string // 给模型的判定说明
	// Energy：1 = 高能量情绪，-1 = 低能量情绪，0 = 不限（用于与唤醒度的冲突检查）
	Energy int
	// Negative 为内部负面标记：只用于把伤感/黑暗曲目排除出正向主题，不作为主题、不展示。
	Negative bool
	// MaxConfidence 是置信度上限（0 表示不限）。
	MaxConfidence float64
}

// Moods 是情绪词表 v2：12 个主题情绪 + 3 个内部负面标记。
var Moods = []Tag{
	{ID: "hotblooded", Label: "热血", Hint: "燃、冲劲、战斗感；战斗/竞技类 OP、不屈与守护的歌词", Energy: 1},
	{ID: "uplifting", Label: "励志振奋", Hint: "明亮向上、像大合唱；未来、约定、出发、梦想", Energy: 1},
	{ID: "cheerful", Label: "元气欢快", Hint: "轻快开心；日常、朋友、夏天", Energy: 1},
	{ID: "adventure", Label: "冒险旅途", Hint: "出发、开阔、风景感；远方、天空、游戏原野曲"},
	{ID: "playful", Label: "俏皮诙谐", Hint: "古灵精怪、洗脑；拟声词、电波歌词、搞笑角色歌"},
	{ID: "cool", Label: "帅气律动", Hint: "酷、有型、律动感；都市、夜晚、爵士、City Pop"},
	{ID: "epic", Label: "壮阔史诗", Hint: "宏大、管弦、命运感；世界、传说", Energy: 1},
	{ID: "romantic", Label: "浪漫甜蜜", Hint: "心动、恋爱、告白（甜，而不是失恋）"},
	{ID: "healing", Label: "治愈温柔", Hint: "温暖、被安慰；陪伴、没关系、家；常见于 ED", Energy: -1},
	{ID: "relaxed", Label: "宁静放松", Hint: "安静、背景感；多为纯音乐", Energy: -1},
	{ID: "dreamy", Label: "梦幻空灵", Hint: "空灵、飘浮；星空、梦、月、空灵女声", Energy: -1},
	{ID: "nostalgic", Label: "温暖怀旧", Hint: "美好回忆、旧时光，偏温暖而不是悲伤；主要依据歌词", MaxConfidence: 0.5},
	{ID: "sad", Label: "伤感", Hint: "悲伤、失恋、离别、哭泣（内部标记）", Negative: true},
	{ID: "dark", Label: "黑暗压抑", Hint: "阴暗、绝望、恐怖、压迫感（内部标记）", Negative: true},
	{ID: "lonely", Label: "孤独", Hint: "孤独、寂寞、被遗弃（内部标记）", Negative: true},
}

// Styles 是风格词表 v1。
var Styles = []Tag{
	{ID: "pop", Label: "流行", Hint: "主流流行歌曲（含 J-pop、动画流行）"},
	{ID: "rock", Label: "摇滚", Hint: "以吉他乐队为主的摇滚"},
	{ID: "metal", Label: "金属", Hint: "重失真吉他、金属乐"},
	{ID: "punk", Label: "朋克", Hint: "朋克、流行朋克、后硬核"},
	{ID: "electronic", Label: "电子", Hint: "以合成器为主的电子音乐（非舞曲）"},
	{ID: "dance", Label: "舞曲/EDM", Hint: "四拍舞曲、EDM、Eurobeat、Trance 等"},
	{ID: "ballad", Label: "抒情慢歌", Hint: "慢速抒情的歌唱曲"},
	{ID: "idol", Label: "偶像流行", Hint: "偶像组合/偶像企划/声优偶像团体的歌曲；必须有元数据依据"},
	{ID: "folk_acoustic", Label: "民谣/原声", Hint: "原声吉他、民谣、不插电"},
	{ID: "ethnic", Label: "古风/民族", Hint: "中国风、和风、民族乐器、世界音乐"},
	{ID: "orchestral", Label: "交响/管弦", Hint: "管弦乐、配乐、交响"},
	{ID: "piano", Label: "钢琴", Hint: "以钢琴为主（多为纯音乐）"},
	{ID: "jazz", Label: "爵士/融合", Hint: "爵士、融合、蓝调"},
	{ID: "citypop_funk", Label: "City Pop/放克", Hint: "City Pop、放克、迪斯科、灵魂乐"},
	{ID: "hiphop", Label: "说唱/嘻哈", Hint: "说唱、嘻哈"},
	{ID: "denpa", Label: "电波/洗脑", Hint: "电波歌、洗脑神曲、高速可爱电子"},
	{ID: "ambient", Label: "氛围/环境", Hint: "氛围、环境音乐"},
	{ID: "experimental", Label: "后摇/实验", Hint: "后摇、数学摇滚、实验"},
}

var (
	moodByID  = index(Moods)
	styleByID = index(Styles)
)

func index(tags []Tag) map[string]Tag {
	m := make(map[string]Tag, len(tags))
	for _, t := range tags {
		m[t.ID] = t
	}
	return m
}

// MoodByID / StyleByID 查找词表标签。
func MoodByID(id string) (Tag, bool)  { t, ok := moodByID[id]; return t, ok }
func StyleByID(id string) (Tag, bool) { t, ok := styleByID[id]; return t, ok }

// TagLabel 返回标签的中文名（未知标签原样返回）。
func TagLabel(kind, id string) string {
	if kind == "mood" {
		if t, ok := moodByID[id]; ok {
			return t.Label
		}
	} else if t, ok := styleByID[id]; ok {
		return t.Label
	}
	return id
}

func ids(tags []Tag) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.ID
	}
	return out
}
