import {
  ALBUMS, ALBUM_TRACKS, DAILY_MIXES, RECENT_PLAYED, RESUME_ITEMS,
  WORKS_BROWSE, THEME_SONGS, DISCOVER_ITEMS, COVERS,
} from './data.js';

const ic = (name, cls = '') => `<svg class="ic ${cls}" aria-hidden="true"><use href="#${name}"/></svg>`;

function renderAlbumCard(a, state, opts = {}) {
  const selected = state.selectedAlbums.has(a.id);
  return `
    <article class="album-card ${selected ? 'selected' : ''}" data-album-id="${a.id}">
      <div class="album-art-box" data-nav="album" data-id="${a.id}">
        <img src="${a.cover}" alt="${a.title}" loading="lazy">
        <div class="card-top-tools" data-stop>
          <input type="checkbox" class="card-check" data-action="toggle-album-select" data-id="${a.id}" ${selected ? 'checked' : ''} aria-label="选择 ${a.title}">
          <div style="display:flex;gap:5px">
            <button type="button" class="card-tool-pill" data-drawer="edit-album" data-id="${a.id}" title="编辑专辑">${ic('i-edit', 'xs')}</button>
            <button type="button" class="card-tool-pill" data-action="toggle-album-fav" data-id="${a.id}" title="${a.favorite ? '取消收藏' : '收藏专辑'}">${ic(a.favorite ? 'f-heart' : 'i-heart', a.favorite ? 'xs fill' : 'xs')}</button>
          </div>
        </div>
        <button type="button" class="card-play-disc" data-stop data-action="play-album" data-id="${a.id}" aria-label="播放 ${a.title}">
          ${ic('f-play', 'fill')}
        </button>
      </div>
      <div class="album-card-meta">
        <a class="album-card-title ell" href="#album" data-nav="album" data-id="${a.id}">${a.title}</a>
        <a class="album-card-artist ell" href="#artist" data-nav="artist">${a.artist}</a>
        <span class="album-card-sub ell tnum">${opts.showAdded ? `入库于 ${a.added}` : `${a.year} · ${a.kind} · ${a.tracks} 首`}</span>
      </div>
    </article>`;
}

export function renderHome(state) {
  const tabs = [
    { id: 'recent', label: '最新入库' },
    { id: 'listening', label: '最近在听' },
    { id: 'series', label: '作品聚焦' },
    { id: 'random', label: '随机重温' },
  ];
  const spotMap = {
    recent: ALBUMS[0],
    listening: ALBUMS[1],
    random: ALBUMS[state.randomIdx % ALBUMS.length],
  };
  const spot = spotMap[state.homeTab] || ALBUMS[0];
  const isSeries = state.homeTab === 'series';

  const years = [2011, 2012, 2013, 2014, 2015, 2016, 2017, 2018, 2019, 2020, 2021, 2022, 2023, 2024, 2025, 2026];
  const counts = [14, 22, 18, 28, 36, 44, 52, 61, 78, 84, 96, 88, 104, 91, 76, 42];
  const maxC = Math.max(...counts);

  const filteredWorks = state.workFilter === 'all'
    ? WORKS_BROWSE
    : WORKS_BROWSE.filter((w) => w.type === state.workFilter);

  return `
    <!-- Full-bleed Neutral Editorial Spotlight (032player-rs D3: zero cover blur) -->
    <section class="spotlight" aria-label="精选推荐">
      ${isSeries ? `
        <div class="spotlight-fan" data-nav="albums" style="cursor:pointer">
          <img src="${COVERS.zankyo}" alt="">
          <img src="${COVERS.lander}" alt="">
          <img src="${COVERS.walpurgis}" alt="">
        </div>
      ` : `
        <div class="spotlight-cover" data-nav="album" data-id="${spot.id}">
          <img src="${spot.cover}" alt="${spot.title}">
        </div>
      `}

      <div class="spotlight-info">
        <div>
          <div class="seg" role="tablist" aria-label="首页焦点切换">
            ${tabs.map((t) => `<button type="button" role="tab" aria-selected="${state.homeTab === t.id}" class="${state.homeTab === t.id ? 'active' : ''}" data-action="home-tab" data-val="${t.id}">${t.label}</button>`).join('')}
          </div>
        </div>

        ${isSeries ? `
          <h1 class="spotlight-title" data-nav="albums">鬼灭之刃 动画系列</h1>
          <div class="spotlight-artist">系列 · 4 部作品</div>
          <div class="spotlight-meta tnum">
            <span>9 张关联专辑 · 42 首关联歌曲 · 2019–2024</span>
          </div>
          <div class="chip-row">
            <span class="chip">立志篇 · 2019</span>
            <span class="chip">无限列车篇 · 2020</span>
            <span class="chip">游郭篇 · 2021</span>
          </div>
          <div class="spotlight-actions">
            <button type="button" class="btn btn-primary" data-action="play-track" data-id="1012">${ic('f-play', 'sm fill')} 播放系列全部歌曲</button>
            <button type="button" class="btn btn-secondary" data-nav="admin/series">管理此系列</button>
          </div>
        ` : `
          <h1 class="spotlight-title" data-nav="album" data-id="${spot.id}">${spot.title}</h1>
          <div class="spotlight-artist"><button type="button" data-nav="artist">${spot.artist}</button></div>
          <div class="spotlight-meta tnum">
            <span>${spot.year} · ${spot.tracks} 首 · ${spot.duration}</span>
            <span class="pill">${spot.spec}</span>
            <span>入库于 ${spot.added}</span>
          </div>
          <div class="spotlight-actions">
            <button type="button" class="btn btn-primary" data-action="play-album" data-id="${spot.id}">${ic('f-play', 'sm fill')} 播放</button>
            <button type="button" class="btn btn-secondary" data-action="toast" data-msg="已将《${spot.title}》加入播放队列">${ic('i-listplus', 'sm')} 加入队列</button>
            <button type="button" class="btn-icon-round ${spot.favorite ? 'active' : ''}" data-action="toggle-album-fav" data-id="${spot.id}" title="收藏专辑">${ic(spot.favorite ? 'f-heart' : 'i-heart', spot.favorite ? 'sm fill' : 'sm')}</button>
            ${state.homeTab === 'random' ? `<button type="button" class="btn btn-secondary" data-action="home-reroll">${ic('i-refresh', 'sm')} 换一张</button>` : ''}
            <button type="button" class="btn btn-ghost btn-sm" data-drawer="edit-album" data-id="${spot.id}">${ic('i-edit', 'xs')} 编辑</button>
          </div>
        `}
      </div>

      <div class="spotlight-side">
        <div class="spotlight-side-head tnum">
          <span>${isSeries ? '关联专辑' : '曲目预览'}</span>
          <span>${isSeries ? '9 张专辑' : `${spot.tracks} 首`}</span>
        </div>
        ${isSeries ? ALBUMS.slice(2, 6).map((a) => `
          <div class="preview-row with-art" data-nav="album" data-id="${a.id}">
            <img src="${a.cover}" alt="">
            <div class="ell"><strong class="ell">${a.title}</strong><small class="ell">${a.artist} · ${a.tracks} 首</small></div>
            <span class="p-end tnum">${a.year}</span>
          </div>
        `).join('') : ALBUM_TRACKS.slice(0, 4).map((t) => `
          <div class="preview-row" data-action="play-track" data-id="${t.id}">
            <span class="p-num tnum">${t.no}</span>
            <div class="ell"><strong class="ell">${t.title}</strong><small class="ell">${t.artist}</small></div>
            <span class="p-end tnum">${t.dur}</span>
          </div>
        `).join('')}
        <div style="padding-top:12px">
          <button type="button" class="more-link" data-nav="album" data-id="${spot.id}">专辑详情 ${ic('i-right', 'xs')}</button>
        </div>
      </div>
    </section>

    <div class="editorial-body">
      <!-- Two-Column Core: 今日推荐 + 最近播放 | 最近添加 -->
      <div class="home-duo">
        <div style="display:flex;flex-direction:column;gap:34px;min-width:0">
          <section>
            <div class="section-head">
              <h2>今日推荐</h2>
              <span class="section-sub">每日 06:00 更新</span>
              <span class="section-rule"></span>
              <button type="button" class="more-link" data-nav="admin/daily-mixes">调试与配置 ${ic('i-right', 'xs')}</button>
            </div>
            <div>
              ${DAILY_MIXES.map((m) => `
                <div class="editorial-row" data-action="toast" data-msg="正在播放今日推荐：${m.title}">
                  <div class="mosaic">${m.covers.map((c) => `<img src="${c}" alt="">`).join('')}</div>
                  <div class="ell"><strong class="ell">${m.title}</strong><small class="ell">${m.sub}</small></div>
                  <div class="row-end">${ic('i-right', 'sm')}</div>
                </div>
              `).join('')}
            </div>
          </section>

          <section>
            <div class="section-head">
              <h2>最近播放</h2>
              <span class="section-rule"></span>
              <button type="button" class="more-link" data-action="toast" data-msg="查看完整播放历史">播放记录 ${ic('i-right', 'xs')}</button>
            </div>
            <div>
              ${RECENT_PLAYED.map((r) => `
                <div class="editorial-row compact" data-action="play-track" data-id="${r.id}">
                  <img class="row-art" src="${r.cover}" alt="">
                  <div class="ell"><strong class="ell">${r.title}</strong><small class="ell">${r.artist} · ${r.album}</small></div>
                  <div class="row-end tnum"><span style="color:${r.when === '正在播放' ? 'var(--accent-text)' : 'inherit'}">${r.when}</span><span>${r.dur}</span></div>
                </div>
              `).join('')}
            </div>
          </section>
        </div>

        <section style="min-width:0">
          <div class="section-head">
            <h2>最近添加</h2>
            <span class="section-rule"></span>
            <button type="button" class="more-link" data-nav="albums">查看全部 ${ic('i-right', 'xs')}</button>
          </div>
          <div class="album-grid" style="--cols: 4">
            ${ALBUMS.slice(0, 8).map((a) => renderAlbumCard(a, state, { showAdded: true })).join('')}
          </div>
        </section>
      </div>

      <!-- 接着听 -->
      <section>
        <div class="section-head">
          <h2>接着听</h2>
          <span class="section-sub tnum">4 张专辑未听完</span>
          <span class="section-rule"></span>
        </div>
        <div class="resume-grid">
          ${RESUME_ITEMS.map((r) => `
            <article class="resume-card" data-action="toast" data-msg="继续播放《${r.title}》(${r.time})">
              <img src="${r.cover}" alt="${r.title}">
              <div class="resume-card-body">
                <strong class="ell">${r.title}</strong>
                <small class="ell">${r.sub}</small>
                <div class="mini-progress tnum">
                  <div class="mini-bar"><i style="width:${r.pct}%"></i></div>
                  <span>${r.time}</span>
                </div>
              </div>
            </article>
          `).join('')}
        </div>
      </section>

      <!-- 按作品漫游 -->
      <section>
        <div class="section-head">
          <h2>按作品漫游</h2>
          <span class="section-sub tnum">733 部作品</span>
          <span class="section-rule"></span>
          <div class="section-tools">
            <div class="seg">
              ${['all', '动画', '电影'].map((f) => `<button type="button" class="${state.workFilter === f ? 'active' : ''}" data-action="work-filter" data-val="${f}">${f === 'all' ? '全部' : f}</button>`).join('')}
            </div>
            <button type="button" class="btn btn-ghost btn-sm" data-action="toast" data-msg="已切换下一批关联作品">换一批</button>
          </div>
        </div>
        <div class="works-grid">
          ${filteredWorks.map((w) => `
            <article class="work-card" data-action="toast" data-msg="打开作品详情：《${w.title}》">
              <img class="work-poster" src="${w.poster}" alt="${w.title}">
              <div class="work-card-body">
                <span class="work-kicker tnum">${w.type} · ${w.year}</span>
                <strong class="work-card-title">${w.title}</strong>
                <div class="work-roles">${w.roles.map((r) => `<span class="tag accent">${r}</span>`).join('')}</div>
                <span class="work-count tnum">${w.count}</span>
              </div>
            </article>
          `).join('')}
        </div>
      </section>

      <!-- 主题曲与插曲 -->
      <section>
        <div class="section-head">
          <h2>主题曲与插曲</h2>
          <span class="section-sub">来自作品关联的歌曲角色 · 每日轮换</span>
          <span class="section-rule"></span>
          <button type="button" class="more-link" data-nav="admin/work-review">作品关联审核 ${ic('i-right', 'xs')}</button>
        </div>
        <div class="themes-grid">
          ${THEME_SONGS.map((t, idx) => `
            <div class="theme-row" data-action="play-track" data-id="${idx === 0 ? 3 : 1011 + idx}">
              <img class="t-art" src="${t.cover}" alt="">
              <div class="ell"><strong class="ell" style="display:block;font-size:14px">${t.title}</strong><small class="ell" style="display:block;font-size:12px;color:var(--text-faint)">${t.artist}</small></div>
              <span class="tieup-pill">◆ ${t.role}</span>
              <span class="theme-work ell"><img src="${t.poster}" alt=""><span class="ell">${t.work}</span></span>
              <span class="tnum" style="font-size:12.5px;color:var(--text-faint);text-align:right">${t.dur}</span>
              <button type="button" class="icon-btn" data-stop data-action="toast" data-msg="已将《${t.title}》加入队列" title="加入队列">${ic('i-listplus', 'sm')}</button>
            </div>
          `).join('')}
        </div>
      </section>

      <!-- 编年 -->
      <section>
        <div class="section-head">
          <h2>编年</h2>
          <span class="section-sub">按发行年份浏览 · 点击柱子筛选该年专辑</span>
          <span class="section-rule"></span>
          <span class="tag accent tnum">${state.activeYear} 年 · ${counts[years.indexOf(state.activeYear)] || 104} 张专辑</span>
        </div>
        <div class="year-chart">
          ${years.map((y, i) => `
            <div class="year-col ${state.activeYear === y ? 'active' : ''}" data-action="select-year" data-val="${y}" title="${y} 年：${counts[i]} 张">
              <div class="y-bar" style="height:${Math.round((counts[i] / maxC) * 100)}%"></div>
              <span class="tnum">${String(y).slice(2)}</span>
            </div>
          `).join('')}
        </div>
      </section>
    </div>
  `;
}

export function renderAlbums(state) {
  const letters = ['全部', '#', 'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M', 'N', 'O', 'P', 'R', 'S', 'T', 'U', 'V', 'W', 'Y', 'Z'];
  const q = state.albumQuery.trim().toLowerCase();
  const filtered = ALBUMS.filter((a) => !q || a.title.toLowerCase().includes(q) || a.artist.toLowerCase().includes(q) || a.genre.toLowerCase().includes(q));
  const selCount = state.selectedAlbums.size;

  return `
    <div class="page-wrap ${state.bulkAlbums ? 'bulk-mode' : ''}">
      <header class="list-header">
        <div class="list-title-group">
          <h1>专辑</h1>
          <span class="count tnum">共 ${filtered.length} / 1,751 张专辑</span>
        </div>
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button type="button" class="btn btn-secondary" data-action="toggle-bulk-albums">
            ${ic('i-check-sq', 'sm')} ${state.bulkAlbums ? '退出批量管理' : '批量选择 / 合并'}
          </button>
          <button type="button" class="btn btn-primary" data-action="toast" data-msg="已随机播放当前筛选结果">
            ${ic('i-shuffle', 'sm')} 随机播放全部
          </button>
        </div>
      </header>

      <div class="list-toolbar">
        <div class="search-box">
          ${ic('i-search', 'sm')}
          <input class="input" type="search" value="${state.albumQuery}" placeholder="搜索专辑、歌手、流派…" data-input="album-query">
        </div>
        <select class="select" aria-label="排序方式">
          <option>最近添加</option>
          <option>发行日期</option>
          <option>标题 (A-Z)</option>
          <option>随机重排</option>
        </select>
        <button type="button" class="chip active" data-action="toast" data-msg="已清除流派筛选">流派：J-Rock / Anisong ×</button>
        <button type="button" class="chip" data-action="toast" data-msg="打开年份区间筛选：2019 – 2026">年份区间</button>
        <label class="grid-slider">
          <span>每行</span>
          <input type="range" min="2" max="10" value="${state.albumCols}" data-input="album-cols">
          <span class="tnum">${state.albumCols}</span>
        </label>
      </div>

      <nav class="index-bar" aria-label="首字母与假名索引">
        ${letters.map((l) => `<button type="button" class="${state.albumIndex === l ? 'active' : ''}" data-action="album-index" data-val="${l}">${l}</button>`).join('')}
        <button type="button" style="margin-left:6px" class="tag outline" data-action="toast" data-msg="已展开假名索引（あ か さ た な は ま や ら わ）">かな</button>
      </nav>

      ${selCount > 0 || state.bulkAlbums ? `
        <div class="bulk-bar">
          <div style="display:flex;align-items:center;gap:12px">
            <strong class="tnum">已选 ${selCount} 张专辑</strong>
            <span style="font-size:12.5px;color:var(--text-dim)">（勾选多张同名/分碟专辑可直接合并，源音频文件始终只读）</span>
          </div>
          <div style="display:flex;gap:8px;flex-wrap:wrap">
            <button type="button" class="btn btn-primary btn-sm" ${selCount < 2 ? 'disabled' : ''} data-action="toast" data-msg="已将 ${selCount} 张专辑合并为单张多碟发行（可在管理后台回退）">合并所选专辑</button>
            <button type="button" class="btn btn-danger-soft btn-sm" ${selCount < 1 ? 'disabled' : ''} data-action="toast" data-msg="打开删除确认对话框（仅删除库记录，不删源文件）">删除所选</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="clear-album-select">取消选择</button>
          </div>
        </div>
      ` : ''}

      <section class="album-grid" style="--cols: ${state.albumCols}">
        ${filtered.map((a) => renderAlbumCard(a, state)).join('')}
      </section>
    </div>
  `;
}

export function renderAlbumDetail(state) {
  const album = ALBUMS.find((a) => a.id === state.currentAlbumId) || ALBUMS[0];
  const currentTrackId = state.queue[state.queueIndex]?.id;

  return `
    <div class="page-wrap">
      <!-- Top Navigation + Preserved Web Management Entry Points -->
      <div class="detail-top-bar">
        <button type="button" class="btn btn-ghost btn-sm" data-nav="albums">${ic('i-arrow-left', 'sm')} 返回专辑列表</button>
        <div class="detail-mgmt-tools">
          <button type="button" class="btn btn-secondary btn-sm" data-drawer="edit-album" data-id="${album.id}">${ic('i-edit', 'xs')} 编辑专辑</button>
          <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已触发 Bangumi 条目搜索：《${album.title}》">${ic('i-globe', 'xs')} 从 Bangumi 查找</button>
          <button type="button" class="btn btn-ghost btn-sm" data-nav="admin/work-review">${ic('i-works', 'xs')} 作品审核队列</button>
        </div>
      </div>

      <!-- Neutral Editorial Hero (032player-rs alignment: NO blurred backdrop!) -->
      <section class="detail-hero">
        <img class="detail-cover" src="${album.cover}" alt="${album.title}">
        <div class="detail-heading">
          <h1>${album.title}</h1>
          <div class="detail-artist-line">
            <button type="button" data-nav="artist">${album.artist}</button>
            ${album.version ? `<span class="tag" style="margin-left:10px;vertical-align:middle">${album.version}</span>` : ''}
          </div>
          <div class="detail-meta-line tnum">
            <span>${album.kind} · ${album.year} · ${album.tracks} 首 · ${album.duration} · ${album.genre}</span>
          </div>
          <div class="work-capsules">
            ${album.works.map((w) => `
              <a href="#album" class="work-capsule" data-action="toast" data-msg="打开作品详情：《${w.title}》">
                <img src="${w.poster}" alt="">
                <span>${w.title}</span>
                <span class="tag accent">${w.role}</span>
              </a>
            `).join('')}
          </div>
          <div class="spotlight-actions" style="margin-top:18px">
            <button type="button" class="btn btn-primary" data-action="play-album" data-id="${album.id}">${ic('f-play', 'sm fill')} 播放</button>
            <button type="button" class="btn btn-secondary" data-action="toast" data-msg="已随机播放《${album.title}》">${ic('i-shuffle', 'sm')} 随机播放</button>
            <button type="button" class="btn btn-secondary" data-action="toast" data-msg="已添加到队列末尾">${ic('i-listplus', 'sm')} 加入队列</button>
            <button type="button" class="btn btn-secondary" data-action="toast" data-msg="请选择要加入的目标歌单">整张加入歌单</button>
            <button type="button" class="btn-icon-round ${album.favorite ? 'active' : ''}" data-action="toggle-album-fav" data-id="${album.id}" title="收藏专辑">${ic(album.favorite ? 'f-heart' : 'i-heart', album.favorite ? 'sm fill' : 'sm')}</button>
          </div>
        </div>

        <dl class="facts-list">
          <div class="fact-row"><dt>类型</dt><dd>${album.kind}</dd></div>
          <div class="fact-row"><dt>发行日期</dt><dd class="tnum">${album.date}</dd></div>
          <div class="fact-row"><dt>厂牌</dt><dd>${album.label}</dd></div>
          <div class="fact-row"><dt>碟数</dt><dd class="tnum">${album.discCount} 碟</dd></div>
          <div class="fact-row"><dt>最高音质</dt><dd class="tnum">${album.spec}</dd></div>
          <div class="fact-row"><dt>发行组身份</dt><dd><span class="tag ok">MBID 已绑定</span></dd></div>
        </dl>
      </section>

      <!-- Two-Column Body: Tracklist (with hover OP/ED popover + inline track edit triggers) | Inspector -->
      <div class="detail-columns">
        <section style="min-width:0">
          <div class="section-head">
            <h2>曲目列表</h2>
            <span class="section-sub tnum">${ALBUM_TRACKS.length} 首</span>
            <span class="section-rule"></span>
            <div class="section-tools">
              <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已开启曲目多选，可批量加入歌单">全选加入歌单</button>
            </div>
          </div>

          <div class="track-table-head">
            <span style="text-align:center">#</span>
            <span>标题 / 幕后署名</span>
            <span>关联作品</span>
            <span>歌手</span>
            <span>音质</span>
            <span style="text-align:right">时长</span>
            <span style="text-align:right">操作</span>
          </div>

          <div class="disc-sep tnum"><span>Disc 1</span><span>${ALBUM_TRACKS.length} 首 · 37:55</span></div>

          <div>
            ${ALBUM_TRACKS.map((t) => `
              <article class="track-row ${t.id === currentTrackId ? 'playing' : ''}" data-action="play-track" data-id="${t.id}">
                <span class="t-num tnum">${t.id === currentTrackId ? ic('i-wave', 'xs') : t.no}</span>
                <div class="t-main">
                  <strong class="t-title ell">${t.title}</strong>
                  <span class="t-credits ell">${t.credits}</span>
                </div>
                <div data-stop>
                  ${t.tieup ? `
                    <div class="tieup-wrap">
                      <button type="button" class="tieup-pill">◆ ${t.tieup.role}</button>
                      <div class="tieup-popover" role="dialog" aria-label="关联作品预览">
                        <div class="tieup-pop-item">
                          <img src="${t.tieup.poster}" alt="">
                          <div>
                            <strong>${t.tieup.work}</strong>
                            <span class="cn">${t.tieup.cn}</span>
                            <div class="meta">
                              <span class="tag accent">${t.tieup.role}</span>
                              <span>${t.tieup.type} · ${t.tieup.year}</span>
                            </div>
                            <button type="button" class="more-link" style="margin-top:10px;color:var(--accent-text)" data-action="toast" data-msg="打开作品页：《${t.tieup.work}》">查看作品 ${ic('i-right', 'xs')}</button>
                          </div>
                        </div>
                      </div>
                    </div>
                  ` : ''}
                </div>
                <span class="ell" style="font-size:13px;color:var(--text-dim)">${t.artist}</span>
                <span class="pill tnum">${t.spec}</span>
                <span class="tnum" style="font-size:13px;color:var(--text-faint);text-align:right">${t.dur}</span>
                <div class="t-actions" data-stop>
                  <button type="button" class="icon-btn" style="width:28px;height:28px" data-drawer="artist-correction" data-id="${t.id}" title="修正演唱者">${ic('i-users', 'xs')}</button>
                  <button type="button" class="icon-btn" style="width:28px;height:28px" data-drawer="track-tags" data-id="${t.id}" title="查看/修正 AI 标签">${ic('i-sparkle', 'xs')}</button>
                </div>
              </article>
            `).join('')}
          </div>
          <p style="margin-top:12px;font-size:12px;color:var(--text-faint)">提示：悬停任意带「◆ OP / 主题曲」标签的曲目可预览关联作品卡；点击行尾图标可直接「修正演唱者」或「修正 AI 标签」（Web 编辑能力完整保留）。</p>
        </section>

        <aside class="inspector-box">
          <section>
            <div class="section-head" style="margin-bottom:12px">
              <h2 style="font-size:16px">关联作品</h2>
              <span class="section-sub tnum">${album.works.length}</span>
              <span class="section-rule"></span>
              <button type="button" class="more-link" data-drawer="edit-album" data-id="${album.id}">管理</button>
            </div>
            <div style="display:flex;flex-direction:column;gap:10px">
              ${album.works.map((w) => `
                <div class="inspector-work-row">
                  <img src="${w.poster}" alt="${w.title}">
                  <div style="min-width:0;flex:1">
                    <strong style="font-size:14px;display:block">${w.title}</strong>
                    <small style="font-size:12px;color:var(--text-dim);display:block;margin-top:2px">${w.translated}</small>
                    <div style="display:flex;gap:6px;align-items:center;margin-top:8px">
                      <span class="tag accent">${w.role}</span>
                      <span style="font-size:12px;color:var(--text-faint)">${w.type} · ${w.year}</span>
                    </div>
                  </div>
                </div>
              `).join('')}
            </div>
          </section>

          <section>
            <div class="section-head" style="margin-bottom:12px">
              <h2 style="font-size:16px">参与人员</h2>
              <span class="section-rule"></span>
            </div>
            <div class="card" style="padding:14px 16px">
              <dl class="facts-list" style="padding-top:0">
                <div class="fact-row"><dt>主唱</dt><dd>suis (ヨルシカ)</dd></div>
                <div class="fact-row"><dt>作词 · 作曲 · 编曲</dt><dd>n-buna</dd></div>
                <div class="fact-row"><dt>吉他 / 贝斯</dt><dd>下鶴光康 · キタニタツヤ</dd></div>
              </dl>
            </div>
          </section>

          <section>
            <div class="section-head" style="margin-bottom:12px">
              <h2 style="font-size:16px">${album.artist} 的其他专辑</h2>
              <span class="section-rule"></span>
            </div>
            <div class="album-grid" style="--cols: 2; gap:14px">
              ${ALBUMS.slice(1, 3).map((a) => renderAlbumCard(a, state)).join('')}
            </div>
          </section>
        </aside>
      </div>
    </div>
  `;
}

export function renderArtistDetail(state) {
  const bios = {
    wiki: { label: 'Wikipedia · 简体中文', text: 'ヨルシカ（Yorushika）是由作曲家 n-buna 与主唱 suis 于 2017 年组成的日本摇滚双人组合。作品以文学意象、电影式叙事与清澈的吉他编曲著称，代表专辑包括《だから僕は音楽を辞めた》《盗作》《幻燈》。' },
    lastfm: { label: 'Last.fm · 日文原版', text: 'ヨルシカは、コンポーザーのn-bunaとボーカルのsuisによる日本のロックバンド。2017年結成。物語性の高いコンセプトアルバムと繊細な歌詞世界が支持を集めている。' },
    ai: { label: 'AI 联网概括 · gemini-2.5-flash', text: 'ヨルシカ（夜鹿）由 Vocaloid 出身的创作人 n-buna 与主唱 suis 搭档组成，注重“创作者退居作品背后”的理念；近年多首作品担任热门动画《大雪海的卡纳》《我心里危险的东西》《葬送的芙莉莲》主题曲。' },
  };
  const activeBio = bios[state.artistBioVer] || bios.wiki;

  return `
    <div class="page-wrap">
      <div class="detail-top-bar">
        <button type="button" class="btn btn-ghost btn-sm" data-nav="albums">${ic('i-arrow-left', 'sm')} 返回歌手列表</button>
        <!-- Preserved Web Artist Management Tools -->
        <div class="detail-mgmt-tools">
          <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已重新触发 MusicBrainz 资料匹配">${ic('i-refresh', 'xs')} 重新匹配资料</button>
          <button type="button" class="btn btn-secondary btn-sm" data-drawer="edit-artist">${ic('i-edit', 'xs')} 编辑歌手 / 别名 / 合并</button>
          <button type="button" class="btn btn-ghost btn-sm" data-nav="admin/artist-review">${ic('i-users', 'xs')} 身份审核</button>
        </div>
      </div>

      <section class="detail-hero artist-layout">
        <img class="artist-hero-portrait" src="${COVERS.gentou}" alt="ヨルシカ">
        <div class="detail-heading">
          <h1>ヨルシカ</h1>
          <div class="detail-meta-line">
            <span class="tag">组合 Group</span>
            <span class="tag">JP · 日本</span>
            <span class="tag accent">中文别名：夜鹿</span>
            <span class="tag ok">MBID 已确认</span>
          </div>
          <div class="spotlight-actions" style="margin-top:18px">
            <button type="button" class="btn btn-primary" data-action="play-track" data-id="3">${ic('f-play', 'sm fill')} 播放热门歌曲</button>
            <button type="button" class="btn btn-secondary" data-action="toast" data-msg="已收藏歌手 ヨルシカ">${ic('f-heart', 'sm fill')} 已收藏</button>
            <button type="button" class="btn btn-secondary" data-action="toast" data-msg="查看幕后作品（作词/作曲 12 首）">幕后 12 首</button>
          </div>
        </div>
        <dl class="facts-list">
          <div class="fact-row"><dt>关联专辑</dt><dd class="tnum">4 张</dd></div>
          <div class="fact-row"><dt>演唱曲目</dt><dd class="tnum">58 首</dd></div>
          <div class="fact-row"><dt>幕后参与</dt><dd class="tnum">12 首</dd></div>
          <div class="fact-row"><dt>搜索别名</dt><dd>Yorushika · 夜鹿</dd></div>
        </dl>
      </section>

      <div style="margin-top:28px;display:flex;flex-direction:column;gap:34px">
        <!-- Biography Section with Web Refresh / AI Generate / Version Switcher -->
        <section class="card">
          <div class="card-head" style="margin-bottom:10px">
            <div>
              <div class="card-kicker">歌手简介 · ${activeBio.label}</div>
              <h2 class="card-title">简介与多源版本管理</h2>
            </div>
            <div style="display:flex;gap:8px;flex-wrap:wrap">
              <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="正在从 Wikipedia / Last.fm 刷新简介…">${ic('i-refresh', 'xs')} 刷新简介</button>
              <button type="button" class="btn btn-secondary btn-xs" data-action="artist-bio-ver" data-val="ai">${ic('i-sparkle', 'xs')} AI 联网生成</button>
            </div>
          </div>
          <p style="font-size:14.5px;line-height:1.7;color:var(--text)">${activeBio.text}</p>
          <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:10px;margin-top:14px;padding-top:12px;border-top:1px solid var(--border)">
            <div class="chip-row">
              <span style="font-size:12px;color:var(--text-faint)">可用简介版本 (3)：</span>
              ${Object.entries(bios).map(([k, v]) => `
                <button type="button" class="chip ${state.artistBioVer === k ? 'active' : ''}" data-action="artist-bio-ver" data-val="${k}">${v.label}</button>
              `).join('')}
            </div>
            <button type="button" class="btn btn-ghost btn-xs" data-action="artist-bio-ver" data-val="wiki">恢复全局优先级</button>
          </div>
        </section>

        <!-- Discography -->
        <section>
          <div class="section-head">
            <h2>发行目录</h2>
            <span class="section-sub">最新优先 · ${ALBUMS.length} 张专辑</span>
            <span class="section-rule"></span>
            <label class="grid-slider" style="margin-left:0" title="调整歌手页每行显示的专辑数量">
              <span>每行</span>
              <input type="range" min="2" max="10" value="${state.artistCols}" data-input="artist-cols" aria-label="每行显示的专辑数量">
              <span class="tnum">${state.artistCols}</span>
            </label>
          </div>
          <div class="album-grid" data-artist-grid style="--cols: ${state.artistCols}">
            ${ALBUMS.map((a) => renderAlbumCard(a, state)).join('')}
          </div>
        </section>

        <!-- Backstage Credit Role Tabs -->
        <section>
          <div class="section-head">
            <h2>幕后参与曲目</h2>
            <span class="section-rule"></span>
            <div class="seg">
              ${['全部 (12)', '作词 (8)', '作曲 (10)', '编曲 (9)'].map((r, i) => `<button type="button" class="${i === 0 ? 'active' : ''}" data-action="toast" data-msg="筛选幕后角色：${r}">${r}</button>`).join('')}
            </div>
          </div>
          <div>
            ${ALBUM_TRACKS.slice(0, 4).map((t) => `
              <div class="editorial-row compact" data-action="play-track" data-id="${t.id}">
                <img class="row-art" src="${COVERS.gentou}" alt="">
                <div class="ell"><strong class="ell">${t.title}</strong><small class="ell">${t.credits}</small></div>
                <div class="row-end tnum"><span>${t.dur}</span></div>
              </div>
            `).join('')}
          </div>
        </section>
      </div>
    </div>
  `;
}

export function renderDiscover(state) {
  const items = DISCOVER_ITEMS.filter((it) => {
    if (it.tab !== state.discoverTab) return false;
    if (state.discoverFilter === 'all') return true;
    if (state.discoverFilter === 'mapping') return it.state === 'mapping';
    if (state.discoverFilter === 'possible') return it.state === 'possible';
    return it.source === state.discoverFilter;
  });

  return `
    <div class="page-wrap">
      <header class="list-header">
        <div>
          <div class="list-title-group"><h1>发现新音乐</h1><span class="count">基于曲库艺人与作品</span></div>
          <p style="color:var(--text-dim);font-size:13.5px;margin-top:6px">推荐发现与资源获取解耦：先判定“什么值得拥有”，点击获取时再通过 Jackett 检索 JPopsuki 并由你确认种子。</p>
        </div>
        <div style="display:flex;gap:8px;flex-wrap:wrap">
          <button type="button" class="btn btn-secondary" data-nav="admin/sources" data-subtab="acquisition">${ic('i-settings', 'sm')} 获取设置</button>
          <button type="button" class="btn btn-secondary" data-action="toggle-discover-sched">${ic('i-sliders', 'sm')} 定时与缓存</button>
          <button type="button" class="btn btn-primary" data-action="toast" data-msg="正在增量刷新发现目录（复用 7 天内缓存）…">${ic('i-refresh', 'sm')} 刷新目录</button>
        </div>
      </header>

      ${state.discoverSchedOpen ? `
        <section class="card" style="margin-bottom:20px">
          <div class="card-head">
            <div><div class="card-kicker">定时与缓存策略</div><h2 class="card-title">自动目录刷新配置</h2></div>
            <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已启动全量重新抓取（每 2 秒一个艺人）">全部重新抓取</button>
          </div>
          <div class="form-grid-3">
            <div class="field"><label>定时自动刷新</label><label class="switch-row" style="height:38px;padding:0 12px"><span>开启定时任务</span><input type="checkbox" class="toggle" checked></label></div>
            <div class="field"><label>刷新间隔</label><select class="select"><option>每 3 天</option><option selected>每周</option><option>每 2 周</option></select></div>
            <div class="field"><label>数据复用窗口</label><select class="select"><option>3 天</option><option selected>7 天</option><option>14 天</option></select></div>
          </div>
        </section>
      ` : ''}

      <div class="admin-tabs">
        ${[
          { id: 'recommended', label: '为你发现', cnt: 4 },
          { id: 'acquiring', label: '获取中', cnt: 1 },
          { id: 'ignored', label: '已忽略', cnt: 1 },
        ].map((t) => `<button type="button" class="admin-tab ${state.discoverTab === t.id ? 'active' : ''}" data-action="discover-tab" data-val="${t.id}">${t.label} <span class="tab-cnt tnum">${t.cnt}</span></button>`).join('')}
      </div>

      ${state.discoverTab === 'recommended' ? `
        <div class="chip-row" style="margin-bottom:22px">
          ${[
            ['all', '全部'], ['artist', '来自艺人'], ['work', '来自作品'],
            ['mapping', '待确认对应 · 1'], ['possible', '可能已有 · 1'],
          ].map(([k, l]) => `<button type="button" class="chip ${state.discoverFilter === k ? 'active' : ''}" data-action="discover-filter" data-val="${k}">${l}</button>`).join('')}
        </div>
      ` : ''}

      <div class="admin-grid-3">
        ${items.map((it) => {
          if (it.state === 'mapping') {
            return `
              <article class="card" style="grid-column: 1 / -1">
                <div class="card-head">
                  <div>
                    <span class="tag warn">待确认对应 · Bangumi → MusicBrainz</span>
                    <h3 class="card-title" style="margin-top:6px">${it.title}</h3>
                    <p class="card-desc">来自作品《${it.work}》· 确认对应 Release Group 后才能搜种获取</p>
                  </div>
                  <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已标记为“都不是”并隐藏">都不是</button>
                </div>
                <div class="admin-grid-2">
                  ${it.mappingOptions.map((o) => `
                    <div class="cand-item">
                      <div class="cand-item-main">
                        <img src="${it.cover}" alt="" style="width:52px;height:52px;object-fit:cover;flex:none">
                        <div class="ell">
                          <strong class="ell">${o.title}</strong>
                          <small class="ell">${o.artist} · ${o.year} · ${o.kind}</small>
                          <div style="margin-top:4px;font-size:11.5px;color:var(--text-faint)">${o.evidence.join(' · ')}</div>
                        </div>
                      </div>
                      <button type="button" class="btn btn-primary btn-xs" data-action="toast" data-msg="已确认对应《${o.title}》，现在可点击获取">是这张</button>
                    </div>
                  `).join('')}
                </div>
              </article>`;
          }
          return `
            <article class="card" style="padding:16px;display:flex;flex-direction:column;gap:12px">
              <div style="display:flex;gap:14px;align-items:flex-start">
                <img src="${it.cover}" alt="${it.title}" style="width:92px;height:92px;object-fit:cover;flex:none">
                <div style="min-width:0;flex:1">
                  <div style="display:flex;gap:6px;flex-wrap:wrap;margin-bottom:5px">
                    <span class="tag">${it.kind}</span>
                    ${it.state === 'auto-bangumi' ? '<span class="tag accent">Bangumi 自动对应</span>' : ''}
                    ${it.state === 'possible' ? '<span class="tag warn">可能已有</span>' : ''}
                    ${it.state === 'acquiring' ? '<span class="tag ok">下载中 68%</span>' : ''}
                  </div>
                  <strong style="font-size:15px;display:block;line-height:1.3">${it.title}</strong>
                  <span style="font-size:13px;color:var(--text-dim);display:block;margin-top:3px">${it.artist} · ${it.year}</span>
                </div>
              </div>
              <ul style="padding-left:16px;font-size:12.5px;color:var(--text-dim);display:grid;gap:3px">
                ${it.reasons.map((r) => `<li>${r}</li>`).join('')}
              </ul>
              ${it.state === 'acquiring' ? `
                <div>
                  <div class="progress-track"><div class="progress-fill ok" style="width:${it.progress}%"></div></div>
                  <small style="display:block;margin-top:6px;font-size:12px;color:var(--text-faint)">${it.step} · 完成后由 MAT 整理入库</small>
                </div>
              ` : ''}
              <div style="margin-top:auto;padding-top:10px;border-top:1px solid var(--border);display:flex;gap:8px;flex-wrap:wrap">
                ${it.state === 'ignored' ? `
                  <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已恢复推荐《${it.title}》">恢复推荐</button>
                ` : it.state === 'acquiring' ? `
                  <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="仅取消本地追踪，不删除 qBittorrent 任务">取消本地追踪</button>
                ` : it.state === 'possible' ? `
                  <button type="button" class="btn btn-primary btn-sm" data-drawer="discover-compare" data-id="${it.id}">对比本地专辑</button>
                  <button type="button" class="btn btn-secondary btn-sm" data-drawer="discover-acquire" data-id="${it.id}">仍然获取</button>
                ` : `
                  <button type="button" class="btn btn-primary btn-sm" data-drawer="discover-acquire" data-id="${it.id}">${ic('i-download', 'xs')} 获取</button>
                  <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已标记《${it.title}》为“我已有”">我已有</button>
                  <button type="button" class="btn btn-ghost btn-sm" data-action="toast" data-msg="已忽略《${it.title}》">忽略</button>
                `}
              </div>
            </article>`;
        }).join('')}
      </div>
    </div>
  `;
}

// ============================================================ NORMAL-PAGE MANAGEMENT DRAWERS
export function renderPlayerDrawer(type, id) {
  if (type === 'edit-album') {
    const album = ALBUMS.find((a) => a.id === Number(id)) || ALBUMS[0];
    return {
      title: `编辑专辑 · ${album.title}`,
      body: `
        <p style="font-size:12.5px;color:var(--text-dim);margin-bottom:16px">所有修改保存为独立覆盖层，源音频文件始终保持只读。</p>
        <div class="form-stack">
          <div style="display:flex;gap:14px;align-items:center;padding:12px;border-radius:10px;background:var(--surface-alt);border:1px solid var(--border)">
            <img src="${album.cover}" alt="" style="width:72px;height:72px;object-fit:cover">
            <div style="display:flex;flex-direction:column;gap:8px">
              <strong style="font-size:13.5px">自定义专辑封面</strong>
              <div style="display:flex;gap:8px">
                <button type="button" class="btn btn-secondary btn-xs" data-action="toast" data-msg="选择本地图片上传覆盖封面">上传封面</button>
                <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已恢复内嵌默认封面">恢复默认封面</button>
              </div>
            </div>
          </div>
          <div class="form-grid">
            <div class="field full"><label>专辑标题</label><input class="input" value="${album.title}"></div>
            <div class="field"><label>版本 / 副标题</label><input class="input" value="${album.version || ''}" placeholder="如：完全生产限定盘"></div>
            <div class="field"><label>专辑艺人</label><input class="input" value="${album.artist}"></div>
            <div class="field"><label>发行日期</label><input class="input" value="${album.date}"></div>
            <div class="field"><label>流派</label><input class="input" value="${album.genre}"></div>
            <div class="field"><label>唱片类型</label><select class="select"><option selected>${album.kind}</option><option>EP</option><option>单曲</option><option>原声集 OST</option></select></div>
            <div class="field"><label>厂牌</label><input class="input" value="${album.label}"></div>
          </div>
          <div style="padding-top:12px;border-top:1px solid var(--border)">
            <div class="card-kicker">专辑作品关联</div>
            <div style="display:flex;flex-direction:column;gap:8px;margin-top:8px">
              ${album.works.map((w) => `
                <div class="cand-item">
                  <span><strong>${w.title}</strong> <span class="tag accent" style="margin-left:6px">${w.role}</span></span>
                  <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="已解除与《${w.title}》的关联">解除关联</button>
                </div>
              `).join('')}
              <div style="display:flex;gap:8px;margin-top:4px">
                <input class="input" placeholder="输入作品名或 Bangumi ID 添加关联…">
                <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已添加专辑作品关联">添加</button>
              </div>
            </div>
          </div>
        </div>`,
      foot: `
        <button type="button" class="btn btn-ghost" data-action="close-drawer">取消</button>
        <button type="button" class="btn btn-primary" data-action="save-drawer" data-msg="已保存《${album.title}》覆盖信息">保存覆盖信息</button>`,
    };
  }

  if (type === 'artist-correction') {
    const track = ALBUM_TRACKS.find((t) => t.id === Number(id)) || ALBUM_TRACKS[0];
    return {
      title: `修正演唱者 · ${track.title}`,
      body: `
        <div class="form-stack">
          <div class="card" style="padding:14px;background:var(--surface-alt)">
            <div class="card-kicker">当前署名 · 音频标签原值</div>
            <strong>${track.artist}</strong>
            <small style="display:block;color:var(--text-faint);margin-top:4px">${track.credits}</small>
          </div>
          <div class="field">
            <label>选择或拆分演唱者（支持多值）</label>
            <div class="chip-row" style="margin-bottom:6px">
              <span class="chip active">ヨルシカ ×</span>
              <span class="chip">suis ×</span>
            </div>
            <div style="display:flex;gap:8px">
              <input class="input" placeholder="搜索已有歌手或输入新名称…">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已添加到待选演唱者列表">添加</button>
            </div>
          </div>
          <div style="padding-top:12px;border-top:1px solid var(--border);display:flex;gap:8px;flex-wrap:wrap">
            <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="预览：演唱者将从 1 位拆分为 2 位，不改动原音频文件">预览修改</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="toast" data-msg="已恢复为音频标签默认署名">恢复默认</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="toast" data-msg="已撤销最近一次人工修正">撤销最近操作</button>
          </div>
        </div>`,
      foot: `
        <button type="button" class="btn btn-ghost" data-action="close-drawer">取消</button>
        <button type="button" class="btn btn-primary" data-action="save-drawer" data-msg="已保存《${track.title}》演唱者修正">确认修改</button>`,
    };
  }

  if (type === 'track-tags') {
    const track = ALBUM_TRACKS.find((t) => t.id === Number(id)) || ALBUM_TRACKS[2];
    return {
      title: `AI 标签修正 · ${track.title}`,
      body: `
        <div class="form-stack">
          <div class="card" style="padding:14px;background:var(--surface-alt)">
            <div class="card-kicker">自动识别来源 · gemini-2.5-flash-lite + Essentia</div>
            <div class="chip-row" style="margin-top:8px">
              ${track.moods.map((m) => `<span class="tag accent">${m}</span>`).join('')}
              ${track.styles.map((s) => `<span class="tag">${s}</span>`).join('')}
              <span class="tag ok">人声：女声</span>
              <span class="tag">唤醒度 6.4 / 9</span>
            </div>
          </div>
          <div class="field"><label>情绪标签（逗号分隔，人工覆盖后自动重打标不再覆写）</label><input class="input" value="${track.moods.join(', ')}"></div>
          <div class="field"><label>风格标签</label><input class="input" value="${track.styles.join(', ')}"></div>
          <div class="form-grid">
            <div class="field"><label>能量等级 (1–9)</label><input class="input" type="number" min="1" max="9" value="6"></div>
            <div class="field"><label>语言 / 人声</label><select class="select"><option selected>日语 · 女声</option><option>日语 · 男声</option><option>纯音乐</option></select></div>
          </div>
          <div style="display:flex;gap:8px;padding-top:8px">
            <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已将该曲加入重新打标队列">重新打标</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="toast" data-msg="已恢复自动打标结果">恢复自动结果</button>
          </div>
        </div>`,
      foot: `
        <button type="button" class="btn btn-ghost" data-action="close-drawer">取消</button>
        <button type="button" class="btn btn-primary" data-action="save-drawer" data-msg="已保存《${track.title}》人工标签修正">保存修正</button>`,
    };
  }

  if (type === 'edit-artist') {
    return {
      title: '编辑歌手 · ヨルシカ',
      body: `
        <div class="form-stack">
          <div class="form-grid">
            <div class="field full"><label>显示名称</label><input class="input" value="ヨルシカ"></div>
            <div class="field"><label>类型</label><select class="select"><option selected>Group（组合）</option><option>Person（个人）</option></select></div>
            <div class="field"><label>国家 / 地区</label><input class="input" value="JP"></div>
          </div>
          <div style="display:flex;gap:8px">
            <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="选择本地图片上传歌手头像">上传头像</button>
            <button type="button" class="btn btn-ghost btn-sm" data-action="toast" data-msg="已恢复默认头像">恢复默认</button>
          </div>
          <div style="padding-top:12px;border-top:1px solid var(--border)">
            <div class="card-kicker">搜索别名（支持假名/中文检索）</div>
            <div class="chip-row" style="margin:8px 0">
              <span class="chip active">夜鹿 ×</span>
              <span class="chip">Yorushika ×</span>
            </div>
            <div style="display:flex;gap:8px">
              <input class="input" placeholder="新增搜索别名…">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已添加搜索别名">添加</button>
            </div>
          </div>
          <details class="disclosure">
            <summary>合并此歌手到其他歌手…</summary>
            <div class="form-stack">
              <input class="input" placeholder="搜索目标歌手名称…">
              <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="已核对并执行歌手合并（可在合并历史回退）">已核对是同一歌手，确认合并</button>
            </div>
          </details>
          <details class="disclosure danger-disclosure">
            <summary>解除错误的 MusicBrainz 身份…</summary>
            <p style="font-size:12.5px;margin-bottom:10px">解除后会清空当前 MBID 绑定并将该歌手重新放回待审队列。</p>
            <button type="button" class="btn btn-danger btn-sm" data-action="toast" data-msg="已解除身份并重新进入审核">确认解除，重新审核</button>
          </details>
        </div>`,
      foot: `
        <button type="button" class="btn btn-ghost" data-action="close-drawer">关闭</button>
        <button type="button" class="btn btn-primary" data-action="save-drawer" data-msg="已保存歌手覆盖信息">保存修改</button>`,
    };
  }

  if (type === 'discover-acquire') {
    const it = DISCOVER_ITEMS.find((x) => x.id === id) || DISCOVER_ITEMS[0];
    return {
      title: '通过 Jackett 检索并确认种子',
      body: `
        <div class="form-stack">
          <div style="display:flex;gap:12px;align-items:center">
            <img src="${it.cover}" alt="" style="width:60px;height:60px;object-fit:cover">
            <div><strong>${it.title}</strong><div style="font-size:12.5px;color:var(--text-dim)">${it.artist} · ${it.year} · ${it.kind}</div><code style="color:var(--text-faint)">RGID: ${it.rgid || '—'}</code></div>
          </div>
          <div style="display:flex;gap:8px">
            <input class="input" value="${it.artist} ${it.title}">
            <button type="button" class="btn btn-secondary btn-sm" data-action="toast" data-msg="正在强制刷新 Jackett 搜索结果…">强制刷新</button>
          </div>
          <div class="cand-list">
            <label class="cand-item ai-pick" style="cursor:pointer">
              <div style="min-width:0">
                <strong style="font-size:13px">[JPopsuki] ${it.artist} - ${it.title} [2020] [FLAC 24bit/96kHz WEB]</strong>
                <div style="display:flex;gap:6px;margin-top:6px"><span class="tag ok">Freeleech</span><span class="tag">FLAC 24bit</span><span class="tag outline tnum">842 MB · 38 做种</span></div>
              </div>
              <input type="radio" name="seed" checked style="accent-color:var(--accent)">
            </label>
            <label class="cand-item" style="cursor:pointer">
              <div style="min-width:0">
                <strong style="font-size:13px">[JPopsuki] ${it.artist} - ${it.title} [CD-Rip FLAC 16bit]</strong>
                <div style="display:flex;gap:6px;margin-top:6px"><span class="tag">FLAC 16bit</span><span class="tag outline tnum">412 MB · 61 做种</span></div>
              </div>
              <input type="radio" name="seed" style="accent-color:var(--accent)">
            </label>
          </div>
          <p style="font-size:12px;color:var(--text-faint)">请核对艺人、标题与版本。推送到 qBittorrent 后源文件将继续做种，正式曲库接收 MAT 整理后的副本。</p>
        </div>`,
      foot: `
        <button type="button" class="btn btn-ghost" data-action="close-drawer">取消</button>
        <button type="button" class="btn btn-primary" data-action="save-drawer" data-msg="已推送种子至 qBittorrent（分类：music-pt）">确认种子并获取</button>`,
    };
  }

  return {
    title: '对比推荐发行与本地专辑',
    body: `
      <div class="admin-grid-2">
        <div class="card" style="padding:14px"><div class="card-kicker">推荐的发行 (MusicBrainz)</div><strong>THE BOOK 3</strong><p class="card-desc">YOASOBI · 2023 · EP</p></div>
        <div class="card" style="padding:14px"><div class="card-kicker">本地相似专辑</div><strong>THE BOOK 2</strong><p class="card-desc">YOASOBI · 2021 · 8 首 · 已有发行组</p></div>
      </div>`,
    foot: `
      <button type="button" class="btn btn-secondary" data-action="save-drawer" data-msg="已标记为不同发行，保留推荐">不是同一张</button>
      <button type="button" class="btn btn-primary" data-action="save-drawer" data-msg="已关联本地专辑并标记为拥有">是同一发行</button>`,
  };
}
