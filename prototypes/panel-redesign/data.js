// Demo data grounded in prototypes/assets/ real covers and 032music-server domain entities.

export const COVERS = {
  theBook: '../assets/the-book.jpg',
  theBook2: '../assets/the-book-2.jpg',
  gentou: '../assets/gentou.jpg',
  walpurgis: '../assets/walpurgis.jpg',
  zankyo: '../assets/zankyo-sanka.jpg',
  lander: '../assets/lander.jpg',
  is6: '../assets/infinite-synthesis-6.jpg',
  iv: '../assets/iv.jpg',
  aot: '../assets/attack-on-titan-ost.jpg',
};

export const ALBUMS = [
  {
    id: 101, title: '幻燈', version: '画集版', artist: 'ヨルシカ', year: 2023, date: '2023-04-05',
    kind: '专辑', tracks: 10, discCount: 1, duration: '41:18', genre: 'J-Rock',
    label: 'Universal Music Japan', spec: 'FLAC 24bit / 96kHz', added: '2 天前',
    cover: COVERS.gentou, favorite: true, mbid: '88ee1a12-f362-4737-b7f4-b2f0b079e218',
    works: [
      { id: 1, title: '大雪海のカイナ', translated: '大雪海的卡纳', type: '动画', year: 2023, role: 'OP', poster: COVERS.gentou },
      { id: 2, title: '僕ヤバ', translated: '我心里危险的东西', type: '动画', year: 2023, role: 'OP', poster: COVERS.theBook2 },
    ],
  },
  {
    id: 102, title: 'THE BOOK', version: '完全生产限定盘', artist: 'YOASOBI', year: 2021, date: '2021-01-06',
    kind: 'EP', tracks: 9, discCount: 1, duration: '31:52', genre: 'J-Pop',
    label: 'Sony Music Entertainment', spec: 'FLAC 24bit / 96kHz', added: '3 天前',
    cover: COVERS.theBook, favorite: true, mbid: '6ddc7e3d-6fc7-4859-9e35-b666e49b40f5',
    works: [
      { id: 3, title: 'BEASTARS', translated: '动物狂想曲 第 2 期', type: '动画', year: 2021, role: 'OP', poster: COVERS.theBook },
    ],
  },
  {
    id: 103, title: 'Walpurgis', version: '', artist: 'Aimer', year: 2021, date: '2021-04-14',
    kind: '专辑', tracks: 14, discCount: 1, duration: '57:20', genre: 'J-Pop',
    label: 'SACRA MUSIC', spec: 'FLAC 24bit / 48kHz', added: '4 天前',
    cover: COVERS.walpurgis, favorite: true, mbid: '9a1a0031-e552-4386-b67a-c46ebb7f666d',
    works: [
      { id: 4, title: 'Fate/stay night [Heaven\'s Feel] III', translated: '命运之夜——天之杯 III', type: '电影', year: 2020, role: '主题曲', poster: COVERS.walpurgis },
      { id: 5, title: 'ヴィンランド・サガ', translated: '海盗战记', type: '动画', year: 2019, role: 'ED', poster: COVERS.aot },
    ],
  },
  {
    id: 104, title: '残響散歌 / 朝が来る', version: '', artist: 'Aimer', year: 2021, date: '2021-12-06',
    kind: '单曲', tracks: 4, discCount: 1, duration: '15:40', genre: 'Anisong',
    label: 'SACRA MUSIC', spec: 'FLAC 24bit / 96kHz', added: '5 天前',
    cover: COVERS.zankyo, favorite: true, mbid: 'f2899e9e-1995-4f81-9578-1586132ac462',
    works: [
      { id: 6, title: '鬼滅の刃 遊郭編', translated: '鬼灭之刃 游郭篇', type: '动画', year: 2021, role: 'OP · ED', poster: COVERS.zankyo },
    ],
  },
  {
    id: 105, title: 'LANDER', version: '', artist: 'LiSA', year: 2022, date: '2022-11-16',
    kind: '专辑', tracks: 14, discCount: 1, duration: '58:04', genre: 'Anisong',
    label: 'SACRA MUSIC', spec: 'FLAC 24bit / 48kHz', added: '6 天前',
    cover: COVERS.lander, favorite: false, mbid: '3c1aa9ef-67ff-4171-bd84-40bbd19f960b',
    works: [
      { id: 7, title: '劇場版 鬼滅の刃 無限列車編', translated: '鬼灭之刃 无限列车篇', type: '电影', year: 2020, role: '主题曲', poster: COVERS.lander },
    ],
  },
  {
    id: 106, title: 'infinite synthesis 6', version: '', artist: 'fripSide', year: 2022, date: '2022-03-23',
    kind: '专辑', tracks: 14, discCount: 1, duration: '64:15', genre: 'Synthpop',
    label: 'NBCUniversal', spec: 'FLAC 16bit / 44.1kHz', added: '1 周前',
    cover: COVERS.is6, favorite: false, mbid: 'c03178ba-4eb0-41ca-873e-cbc339689fd4',
    works: [
      { id: 8, title: 'とある科学の超電磁砲T', translated: '某科学的超电磁炮 T', type: '动画', year: 2020, role: 'OP', poster: COVERS.is6 },
    ],
  },
  {
    id: 107, title: 'iv', version: '', artist: 'SawanoHiroyuki[nZk]', year: 2021, date: '2021-03-03',
    kind: '专辑', tracks: 16, discCount: 2, duration: '71:44', genre: 'Soundtrack',
    label: 'SACRA MUSIC', spec: 'FLAC 24bit / 48kHz', added: '1 周前',
    cover: COVERS.iv, favorite: true, mbid: '704ca992-2976-426a-9e72-12cf62d7827e',
    works: [
      { id: 9, title: '86―エイティシックス―', translated: '86-不存在的战区-', type: '动画', year: 2021, role: 'ED', poster: COVERS.iv },
    ],
  },
  {
    id: 108, title: 'TVアニメ「進撃の巨人」Season 3 OST', version: '', artist: '澤野弘之', year: 2019, date: '2019-06-26',
    kind: '原声集 OST', tracks: 22, discCount: 2, duration: '94:10', genre: 'Soundtrack',
    label: 'Pony Canyon', spec: 'FLAC 24bit / 48kHz', added: '2 周前',
    cover: COVERS.aot, favorite: true, mbid: '58d84467-7a4d-428b-8637-417ac36ac42b',
    works: [
      { id: 10, title: '進撃の巨人 Season 3', translated: '进击的巨人 第 3 季', type: '动画', year: 2019, role: 'OST', poster: COVERS.aot },
    ],
  },
  {
    id: 109, title: 'THE BOOK 2', version: '', artist: 'YOASOBI', year: 2021, date: '2021-12-01',
    kind: 'EP', tracks: 8, discCount: 1, duration: '28:19', genre: 'J-Pop',
    label: 'Sony Music Entertainment', spec: 'FLAC 24bit / 96kHz', added: '2 周前',
    cover: COVERS.theBook2, favorite: false, mbid: '96f4ba04-a052-4a04-ad39-121be2c896fd',
    works: [
      { id: 3, title: 'BEASTARS', translated: '动物狂想曲 第 2 期', type: '动画', year: 2021, role: 'OP · ED', poster: COVERS.theBook2 },
    ],
  },
];

export const ALBUM_TRACKS = [
  { id: 1, disc: 1, no: 1, title: '春のひさぎ', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '3:52', sec: 232, fav: true, moods: ['清澈', '春意'], styles: ['Indie Rock'], tieup: null },
  { id: 2, disc: 1, no: 2, title: 'アルジャーノン', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '4:14', sec: 254, fav: true, moods: ['温柔', '叙事'], styles: ['J-Rock'], tieup: { role: '主题曲', work: '夕暮れに、手をつなぐ', cn: '黄昏时分、牵起手', type: '日剧', year: 2023, poster: COVERS.gentou } },
  { id: 3, disc: 1, no: 3, title: '斜陽', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '3:18', sec: 198, fav: true, moods: ['清爽', '怀旧'], styles: ['Pop Rock'], tieup: { role: 'OP', work: '僕の心のヤバイやつ', cn: '我心里危险的东西', type: '动画', year: 2023, poster: COVERS.theBook2 } },
  { id: 4, disc: 1, no: 4, title: 'テレパス', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '3:43', sec: 223, fav: false, moods: ['跃动', '轻快'], styles: ['J-Rock'], tieup: { role: 'OP', work: '大雪海のカイナ', cn: '大雪海的卡纳', type: '动画', year: 2023, poster: COVERS.gentou } },
  { id: 5, disc: 1, no: 5, title: '左右盲', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '4:27', sec: 267, fav: true, moods: ['抒情', '细腻'], styles: ['Ballad'], tieup: { role: '主题曲', work: '今夜、世界からこの恋が消えても', cn: '即使今晚这段恋情从世上消失', type: '电影', year: 2022, poster: COVERS.walpurgis } },
  { id: 6, disc: 1, no: 6, title: 'ブレーメン', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '4:42', sec: 282, fav: false, moods: ['松弛', '律动'], styles: ['Indie Pop'], tieup: null },
  { id: 7, disc: 1, no: 7, title: 'チノカテ', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '4:09', sec: 249, fav: false, moods: ['静谧', '思索'], styles: ['Folk Rock'], tieup: null },
  { id: 8, disc: 1, no: 8, title: '都落ち', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '4:10', sec: 250, fav: false, moods: ['淡雅', '古典'], styles: ['Acoustic'], tieup: null },
  { id: 9, disc: 1, no: 9, title: '第一夜', artist: 'ヨルシカ', credits: '作词・作曲・编曲 n-buna', spec: 'FLAC 24/96', dur: '4:20', sec: 260, fav: true, moods: ['梦幻', '夜色'], styles: ['Post-Rock'], tieup: null },
];

export const QUEUE_ITEMS = [
  { id: 3, title: '斜陽', artist: 'ヨルシカ', album: '幻燈', spec: 'FLAC 24bit / 96kHz', dur: '3:18', sec: 198, cover: COVERS.gentou, fav: true },
  { id: 1011, title: '夜に駆ける', artist: 'YOASOBI', album: 'THE BOOK', spec: 'FLAC 24bit / 96kHz', dur: '4:21', sec: 261, cover: COVERS.theBook, fav: true },
  { id: 1012, title: '残響散歌', artist: 'Aimer', album: '残響散歌 / 朝が来る', spec: 'FLAC 24bit / 96kHz', dur: '3:05', sec: 185, cover: COVERS.zankyo, fav: true },
  { id: 1013, title: '炎', artist: 'LiSA', album: 'LANDER', spec: 'FLAC 24bit / 48kHz', dur: '4:35', sec: 275, cover: COVERS.lander, fav: false },
  { id: 1014, title: 'Avid', artist: 'SawanoHiroyuki[nZk]:mizuki', album: 'iv', spec: 'FLAC 24bit / 48kHz', dur: '4:08', sec: 248, cover: COVERS.iv, fav: true },
  { id: 1015, title: 'final phase', artist: 'fripSide', album: 'infinite synthesis 6', spec: 'FLAC 16bit / 44.1kHz', dur: '4:03', sec: 243, cover: COVERS.is6, fav: false },
  { id: 1016, title: 'Before Lights Out', artist: '澤野弘之', album: '進撃の巨人 Season 3 OST', spec: 'FLAC 24bit / 48kHz', dur: '2:55', sec: 175, cover: COVERS.aot, fav: true },
];

export const LYRICS_LINES = [
  { time: 18, orig: '頬に落ちる夕陽が', tr: '落在脸颊上的夕阳' },
  { time: 26, orig: '君の影を伸ばしていく', tr: '将你的身影渐渐拉长' },
  { time: 35, orig: '言えないまま飲み込んだ言葉だけ', tr: '只有没能说出口而咽下的话语' },
  { time: 46, orig: '斜陽の向こうへ溶けていく', tr: '正融化在斜阳的彼端' },
  { time: 58, orig: '振り返ればまだ夏の名残が', tr: '回首望去 盛夏的余温尚存' },
  { time: 70, orig: '風に揺れるたび胸が鳴る', tr: '每当微风拂过 心弦便随之轻颤' },
  { time: 84, orig: 'ただ綺麗だとそう思えたなら', tr: '如果仅仅觉得这片景色很美' },
  { time: 98, orig: 'それだけで今日を終えられるのに', tr: '明明仅凭如此就能结束今天' },
];

export const PLAYLISTS = [
  { id: 1, title: '深夜动漫主题曲精选', sub: '38 首 · 2 小时 36 分', cover: COVERS.walpurgis },
  { id: 2, title: 'ヨルシカ · 文学摇滚', sub: '24 首 · 1 小时 42 分', cover: COVERS.gentou },
  { id: 3, title: '通勤高燃 Anisong', sub: '64 首 · 4 小时 18 分', cover: COVERS.zankyo },
];

export const DAILY_MIXES = [
  { id: 1, title: '暮色霓虹的文学摇滚', sub: '清澈叙事 / J-Rock · 25 首 · 1 小时 38 分', covers: [COVERS.gentou, COVERS.theBook, COVERS.walpurgis, COVERS.theBook2], source: '模型起名', score: '0.91 × 1.18 = 1.07', pool: '142 首（直选 88）', familiar: 16, explore: 9 },
  { id: 2, title: '燃魂交响与剧场原声', sub: '史诗壮阔 / Soundtrack · 22 首 · 1 小时 31 分', covers: [COVERS.aot, COVERS.iv, COVERS.lander, COVERS.zankyo], source: '模型起名', score: '0.86 × 1.12 = 0.96', pool: '96 首（直选 61）', familiar: 14, explore: 8 },
  { id: 3, title: '合成器脉动 · 深夜疾走', sub: 'Synthpop / Anisong · 24 首 · 1 小时 40 分', covers: [COVERS.is6, COVERS.theBook2, COVERS.zankyo, COVERS.lander], source: '模型起名', score: '0.82 × 1.09 = 0.89', pool: '118 首（直选 70）', familiar: 15, explore: 9 },
  { id: 4, title: '从 n-buna 出发', sub: '创作者电台 · 18 首 · 1 小时 14 分', covers: [COVERS.gentou, COVERS.walpurgis, COVERS.theBook, COVERS.iv], source: '模板标题', score: '0.79 × 1.15 = 0.91', pool: '64 首（直选 45）', familiar: 12, explore: 6 },
];

export const RECENT_PLAYED = [
  { id: 3, title: '斜陽', artist: 'ヨルシカ', album: '幻燈', when: '正在播放', dur: '3:18', cover: COVERS.gentou },
  { id: 1011, title: '夜に駆ける', artist: 'YOASOBI', album: 'THE BOOK', when: '今天 22:14', dur: '4:21', cover: COVERS.theBook },
  { id: 1012, title: '残響散歌', artist: 'Aimer', album: '残響散歌 / 朝が来る', when: '今天 21:48', dur: '3:05', cover: COVERS.zankyo },
  { id: 1014, title: 'Avid', artist: 'SawanoHiroyuki[nZk]', album: 'iv', when: '昨天 23:31', dur: '4:08', cover: COVERS.iv },
  { id: 1016, title: 'Before Lights Out', artist: '澤野弘之', album: '進撃の巨人 Season 3 OST', when: '昨天 22:50', dur: '2:55', cover: COVERS.aot },
];

export const RESUME_ITEMS = [
  { title: 'Walpurgis', sub: 'Aimer · 第 4 首', pct: 64, time: '2:45 / 4:18', cover: COVERS.walpurgis },
  { title: 'LANDER', sub: 'LiSA · 第 7 首', pct: 38, time: '1:40 / 4:24', cover: COVERS.lander },
  { title: 'infinite synthesis 6', sub: 'fripSide · 第 2 首', pct: 76, time: '3:05 / 4:03', cover: COVERS.is6 },
  { title: '進撃の巨人 Season 3 OST', sub: '澤野弘之 · 第 11 首', pct: 48, time: '2:12 / 4:34', cover: COVERS.aot },
];

export const WORKS_BROWSE = [
  { id: 6, title: '鬼滅の刃 遊郭編', type: '动画', year: 2021, roles: ['OP 1', 'ED 1'], count: '收录 6 首', poster: COVERS.zankyo },
  { id: 10, title: '進撃の巨人 Season 3', type: '动画', year: 2019, roles: ['OST 22', 'OP 2'], count: '收录 26 首', poster: COVERS.aot },
  { id: 2, title: '僕の心のヤバイやつ', type: '动画', year: 2023, roles: ['OP 1', 'ED 1'], count: '收录 4 首', poster: COVERS.theBook2 },
  { id: 9, title: '86―エイティシックス―', type: '动画', year: 2021, roles: ['ED 2', '插曲 3'], count: '收录 9 首', poster: COVERS.iv },
  { id: 4, title: 'Fate/stay night [HF] III', type: '电影', year: 2020, roles: ['主题曲 1', 'OST 12'], count: '收录 13 首', poster: COVERS.walpurgis },
];

export const THEME_SONGS = [
  { title: '斜陽', artist: 'ヨルシカ', role: 'OP', work: '僕の心のヤバイやつ', dur: '3:18', cover: COVERS.gentou, poster: COVERS.theBook2 },
  { title: '残響散歌', artist: 'Aimer', role: 'OP', work: '鬼滅の刃 遊郭編', dur: '3:05', cover: COVERS.zankyo, poster: COVERS.zankyo },
  { title: '怪物', artist: 'YOASOBI', role: 'OP', work: 'BEASTARS 第 2 期', dur: '3:26', cover: COVERS.theBook2, poster: COVERS.theBook },
  { title: 'Avid', artist: 'SawanoHiroyuki[nZk]', role: 'ED', work: '86―エイティシックス―', dur: '4:08', cover: COVERS.iv, poster: COVERS.iv },
  { title: 'final phase', artist: 'fripSide', role: 'OP', work: 'とある科学の超電磁砲T', dur: '4:03', cover: COVERS.is6, poster: COVERS.is6 },
  { title: '春はゆく', artist: 'Aimer', role: '主题曲', work: 'Fate/stay night [HF] III', dur: '5:04', cover: COVERS.walpurgis, poster: COVERS.walpurgis },
];

export const DISCOVER_ITEMS = [
  {
    id: 'rg-1', tab: 'recommended', source: 'artist', title: '盗作', artist: 'ヨルシカ', year: 2020, kind: 'Album',
    cover: COVERS.gentou, reasons: ['来自你收藏的艺人 ヨルシカ（曲库已有 4 张专辑）', '最近 30 天播放 48 次'],
    state: 'normal', rgid: 'a32d9c20-1c4f-41c0-87e1-8f2a99c12b01',
  },
  {
    id: 'rg-2', tab: 'recommended', source: 'work', title: 'TVアニメ「86―エイティシックス―」Original Soundtrack', artist: '澤野弘之 / KOHTA YAMAMOTO', year: 2021, kind: 'OST',
    cover: COVERS.iv, reasons: ['关联作品《86―エイティシックス―》的官方原声集', 'Bangumi 自动对应 · 置信度 96%'],
    state: 'auto-bangumi', work: '86―エイティシックス―', rgid: 'b89e4a11-7d3e-4b02-9a10-2b3c4d5e6f70',
  },
  {
    id: 'rg-3', tab: 'recommended', source: 'artist', title: 'THE BOOK 3', artist: 'YOASOBI', year: 2023, kind: 'EP',
    cover: COVERS.theBook2, reasons: ['来自艺人 YOASOBI，补齐《THE BOOK》三部曲', '曲库检测到相似标题：THE BOOK 2'],
    state: 'possible', localMatch: 'THE BOOK 2 (2021 · 8 首)', rgid: 'c12f8a33-5a1b-4c22-8f11-9a8b7c6d5e43',
  },
  {
    id: 'rg-4', tab: 'recommended', source: 'work', title: '鬼滅の刃 刀鍛冶の里編 主題歌集', artist: 'MAN WITH A MISSION × milet', year: 2023, kind: 'Single',
    cover: COVERS.zankyo, reasons: ['来自同系列作品《鬼灭之刃》'],
    state: 'mapping', work: '鬼滅の刃 刀鍛冶の里編',
    mappingOptions: [
      { rgid: 'm1', title: '絆ノ奇跡 / コイコガレ', artist: 'MAN WITH A MISSION × milet', year: 2023, kind: 'Single', evidence: ['Bangumi 关联曲目完全匹配', '发行日期一致 2023-05-31'] },
      { rgid: 'm2', title: '絆ノ奇跡 (Anime Edition)', artist: 'MAN WITH A MISSION × milet', year: 2023, kind: 'Single', evidence: ['期间生产限定盘 · 含 TV Size'] },
    ],
  },
  {
    id: 'rg-5', tab: 'acquiring', source: 'artist', title: 'Open α Door', artist: 'Aimer', year: 2023, kind: 'Album',
    cover: COVERS.walpurgis, reasons: ['已推送到 qBittorrent（分类：music-pt）'],
    state: 'acquiring', progress: 68, step: 'qBittorrent 下载中 · 68% · 14.2 MB/s',
  },
  {
    id: 'rg-6', tab: 'ignored', source: 'artist', title: 'LEO-NiNE (Instrumental)', artist: 'LiSA', year: 2020, kind: 'Album',
    cover: COVERS.lander, reasons: ['已手动忽略'],
    state: 'ignored',
  },
];

export const WORK_REVIEW_DATA = {
  albums: [
    {
      id: 'wr-a1', localTitle: '幻燈 (画集版)', localMeta: 'ヨルシカ · 2023 · 10 首', cover: COVERS.gentou,
      aiSummary: 'AI 建议采纳（94 分）：曲目《テレパス》《斜陽》分别对应 2 部 2023 年新番 OP',
      candidates: [
        { id: 'c1', title: '大雪海のカイナ', cn: '大雪海的卡纳', meta: '动画 · 2023 · Bangumi #365934', role: 'OP', score: 94, aiPick: true, poster: COVERS.gentou },
        { id: 'c2', title: '僕の心のヤバイやつ', cn: '我心里危险的东西', meta: '动画 · 2023 · Bangumi #394623', role: 'OP', score: 91, aiPick: true, poster: COVERS.theBook2 },
      ],
    },
    {
      id: 'wr-a2', localTitle: 'Walpurgis', localMeta: 'Aimer · 2021 · 14 首', cover: COVERS.walpurgis,
      aiSummary: 'AI 建议人工确认（78 分）：含多部剧场版与 TV 动画 tie-up，存在同名衍生条目',
      candidates: [
        { id: 'c3', title: 'Fate/stay night [Heaven\'s Feel] III.spring song', cn: '命运之夜——天之杯 III', meta: '剧场版 · 2020 · Bangumi #175600', role: '主题曲', score: 86, aiPick: true, poster: COVERS.walpurgis },
        { id: 'c4', title: '魔道祖師 羨雲編 (吹替版)', cn: '魔道祖师 羡云篇 日配版', meta: '动画 · 2021 · Bangumi #312904', role: 'ED', score: 62, aiPick: false, poster: COVERS.aot },
      ],
    },
  ],
  tracks: [
    {
      id: 'wr-t1', localTitle: 'Avid', localMeta: 'SawanoHiroyuki[nZk]:mizuki · 专辑《iv》', cover: COVERS.iv,
      aiSummary: 'AI 建议采纳（96 分）：明确标记为《86》第 1 季度片尾曲 ED',
      candidates: [
        { id: 'ct1', title: '86―エイティシックス―', cn: '86-不存在的战区-', meta: '动画 · 2021 · Bangumi #302189', role: 'ED', score: 96, aiPick: true, poster: COVERS.iv },
      ],
    },
    {
      id: 'wr-t2', localTitle: '残響散歌', localMeta: 'Aimer · 专辑《残響散歌 / 朝が来る》', cover: COVERS.zankyo,
      aiSummary: 'AI 建议采纳（98 分）：游郭篇 OP',
      candidates: [
        { id: 'ct2', title: '鬼滅の刃 遊郭編', cn: '鬼灭之刃 游郭篇', meta: '动画 · 2021 · Bangumi #328609', role: 'OP', score: 98, aiPick: true, poster: COVERS.zankyo },
      ],
    },
  ],
  works: [
    {
      id: 'wr-w1', localTitle: '進撃の巨人 Season 3 Part.2', localMeta: '本地待对齐作品 · 关联 2 张专辑 / 24 首曲目', cover: COVERS.aot,
      aiSummary: 'AI 建议对齐（92 分）：Bangumi 主条目无占用，标题与播出年份完全一致',
      candidates: [
        { id: 'cw1', title: '進撃の巨人 Season 3 Part.2', cn: '进击的巨人 第三季 Part.2', meta: '动画 · 2019-04 · Bangumi #263750', role: '身份对齐', score: 92, aiPick: true, poster: COVERS.aot },
      ],
    },
  ],
  series: [
    {
      id: 'wr-s1', proposedName: '鬼灭之刃', members: ['鬼滅の刃 竈門炭治郎 立志編 (2019)', '劇場版 鬼滅の刃 無限列車編 (2020)', '鬼滅の刃 遊郭編 (2021)'],
      aiSummary: 'AI 建议合并为系列（95 分）：Bangumi 前传/续作链路闭合，无命名冲突',
    },
    {
      id: 'wr-s2', proposedName: '某科学的超电磁炮', members: ['とある科学の超電磁砲 (2009)', 'とある科学の超電磁砲S (2013)', 'とある科学の超電磁砲T (2020)'],
      aiSummary: 'AI 建议合并为系列（93 分）：3 部同源系列作品',
    },
  ],
};

export const ARTIST_REVIEW_DATA = {
  identities: [
    {
      id: 'ar-1', name: 'SawanoHiroyuki[nZk]', meta: '3 张专辑 · 42 首演唱 · 当前未绑定 MBID', aiTag: 'recommend',
      candidates: [
        { id: 'ac1', name: 'SawanoHiroyuki[nZk]', source: 'MusicBrainz', detail: 'Group · JP · 澤野弘之 vocal project · MBID cb191900…', score: 98, aiPick: true, conflict: null },
        { id: 'ac2', name: '澤野弘之', source: 'MusicBrainz', detail: 'Person · JP · 作曲家本人（与本地艺人 #412 冲突）', score: 74, aiPick: false, conflict: '本地已有「澤野弘之」(#412)' },
      ],
    },
    {
      id: 'ar-2', name: 'ヨルシカ (Yorushika)', meta: '4 张专辑 · 58 首演唱 · 存在潜在身份冲突', aiTag: 'conflict',
      candidates: [
        { id: 'ac3', name: 'ヨルシカ', source: 'MusicBrainz', detail: 'Group · JP · n-buna & suis · MBID dfc6a151…', score: 95, aiPick: true, conflict: '与本地「ヨルシカ」(#108) 同 MBID，可直接合并' },
      ],
    },
  ],
  credits: [
    {
      id: 'cs-1', raw: 'SawanoHiroyuki[nZk]:mizuki', tracks: 6,
      split: ['SawanoHiroyuki[nZk]', 'mizuki (UNIDOTS)'], reason: 'AI 识别合作分隔符 ":"，右侧为客座主唱',
    },
    {
      id: 'cs-2', raw: 'MAN WITH A MISSION × milet', tracks: 2,
      split: ['MAN WITH A MISSION', 'milet'], reason: 'AI 识别联名符号 "×"，两位独立艺人均在曲库中存在',
    },
  ],
  avatars: [
    { id: 'av-1', artist: 'Aimer', source: 'Deezer Public API', vlm: 'VLM 质检通过 · 92 分（构图居中、无水印）', img: COVERS.walpurgis },
    { id: 'av-2', artist: 'LiSA', source: 'Bangumi Persons', vlm: 'VLM 建议复核 · 76 分（舞台光影偏暗）', img: COVERS.lander },
    { id: 'av-3', artist: '澤野弘之', source: 'Wikipedia pageimages', vlm: 'VLM 质检通过 · 89 分', img: COVERS.aot },
  ],
  aliases: [
    { id: 'al-1', artist: 'ヨルシカ', proposed: '夜鹿', source: 'DeepSeek 联网核验 · Wikipedia zh / Bangumi', evidence: '中文维基百科与网易云音乐均以「夜鹿」作为「ヨルシカ」常用中文译名。', status: 'pending' },
    { id: 'al-2', artist: '澤野弘之', proposed: '泽野弘之', source: 'OpenCC / MusicBrainz zh-Hans', evidence: '简体中文规范写法，匹配用户简体搜索习惯。', status: 'pending' },
    { id: 'al-3', artist: 'キタニタツヤ', proposed: '木谷龙也', source: 'DeepSeek 联网核验 · Bangumi', evidence: '中文 ACG 社区通用译名「木谷龙也」。', status: 'accepted' },
  ],
  relations: [
    { id: 'rel-1', artist: 'ヨルシカ', related: 'n-buna', type: 'member of band', direction: 'backward', source: 'MusicBrainz', score: 99 },
    { id: 'rel-2', artist: 'SawanoHiroyuki[nZk]', related: '澤野弘之', type: 'founder / project of', direction: 'forward', source: 'MusicBrainz', score: 97 },
  ],
  merges: [
    { id: 18, src: 'YOASOBI (日本)', dst: 'YOASOBI', status: 'merged', at: '2026-11-21 21:14', count: '2 张专辑 · 17 首曲目' },
    { id: 17, src: 'Sawano Hiroyuki', dst: '澤野弘之', status: 'merged', at: '2026-11-18 14:32', count: '4 张专辑 · 68 首曲目' },
    { id: 16, src: 'Aimer (エメ)', dst: 'Aimer', status: 'rolled_back', at: '2026-11-12 09:05', count: '已于 2026-11-12 09:18 回退' },
  ],
};
