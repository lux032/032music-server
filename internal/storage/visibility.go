package storage

// 缺失文件可见性（参照 Navidrome）：扫描发现文件被删除时只把 audio_files
// 标记为 'missing'，歌曲、专辑、歌手的记录与收藏/播放数据都保留；面向浏览
// 的查询统一用下列谓词把“已无可用文件”的条目隐藏。文件恢复后再次扫描会把
// 状态改回 'available'，条目随即自动重新出现。真正删除由 PurgeMissing 完成。
//
// 可见性定义：
//   - 歌曲：至少有一个 status='available' 的音频文件；
//   - 专辑：至少有一首可见歌曲；
//   - 歌手：至少有一张可见专辑（album_artists）或一首可见歌曲（track_artists）。
//
// 均为实时计算的相关子查询，依赖 idx_audio_files_available_track（部分索引）
// 与 idx_tracks_album_sync；不在表上冗余存放标记，因此导入、合并、编辑等写
// 路径无需维护任何状态。内部别名统一以 vis 前缀命名，避免与外层查询冲突。

// trackVisibleSQL 判断 idExpr 指向的歌曲是否可见。
func trackVisibleSQL(idExpr string) string {
	return "EXISTS(SELECT 1 FROM audio_files visf WHERE visf.track_id=" + idExpr + " AND visf.status='available')"
}

// albumVisibleSQL 判断 idExpr 指向的专辑是否可见。
func albumVisibleSQL(idExpr string) string {
	return "EXISTS(SELECT 1 FROM tracks vist JOIN audio_files visaf ON visaf.track_id=vist.id AND visaf.status='available' WHERE vist.album_id=" + idExpr + ")"
}

// artistVisibleSQL 判断 idExpr 指向的歌手是否可见。
func artistVisibleSQL(idExpr string) string {
	return "(EXISTS(SELECT 1 FROM album_artists visaa WHERE visaa.artist_id=" + idExpr + " AND " + albumVisibleSQL("visaa.album_id") + ") OR EXISTS(SELECT 1 FROM track_artists visra WHERE visra.artist_id=" + idExpr + " AND " + trackVisibleSQL("visra.track_id") + "))"
}
