-- 发行类型来源：区分“标签/外部补全明确给出的类型”和“没有类型信息时默认的 album”。
-- tag = 文件标签；enrichment = 外部元数据补全；NULL = 未知（由服务端本地推断）。
-- 历史数据：此前没有任何补全源写入 album_type，非 'album' 的值只可能来自标签；
-- 值为 'album' 的行无法区分是否明确标注，按未知处理，完整重扫后会按标签修正。
ALTER TABLE albums ADD COLUMN album_type_source TEXT;
UPDATE albums SET album_type_source='tag' WHERE album_type IS NOT NULL AND album_type NOT IN ('', 'album');

-- 旧的编辑表单没有“自动”选项，保存任何字段都会把当时显示的类型
-- （COALESCE(user_album_type, album_type)）写成 user_album_type。与扫描值相同
-- 的手动值并不是真正的纠正，清空后改为自动判定；真正改过的值保留。
UPDATE albums SET user_album_type=NULL WHERE user_album_type IS NOT NULL AND user_album_type = COALESCE(album_type, 'album');
