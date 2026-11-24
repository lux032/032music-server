import { ALBUMS, ALBUM_TRACKS, QUEUE_ITEMS, LYRICS_LINES, PLAYLISTS } from './data.js';
import { renderHome, renderAlbums, renderAlbumDetail, renderArtistDetail, renderDiscover, renderPlayerDrawer } from './views-player.js';
import { renderAdminView, renderAdminDrawer } from './views-admin.js';

const ic = (name, cls = '') => `<svg class="ic ${cls}" aria-hidden="true"><use href="#${name}"/></svg>`;

const ADMIN_NAV_GROUPS = [
  {
    label: '总览与运维',
    items: [
      { route: 'admin/dashboard', icon: 'i-console', label: '控制台总览' },
      { route: 'admin/missing', icon: 'i-folder-x', label: '缺失文件与清理', badge: '3', badgeCls: 'warn' },
    ],
  },
  {
    label: '审核中心',
    items: [
      { route: 'admin/work-review', icon: 'i-works', label: '作品关联审核', badge: '24' },
      { route: 'admin/artist-review', icon: 'i-users', label: '艺术家与身份审核', badge: '18' },
      { route: 'admin/series', icon: 'i-series', label: '系列管理' },
    ],
  },
  {
    label: '内容与后台任务',
    items: [
      { route: 'admin/enrichment', icon: 'i-sparkle', label: '元数据自动增强' },
      { route: 'admin/analysis-ai', icon: 'i-wave', label: '声学分析与 AI 打标' },
      { route: 'admin/lyrics', icon: 'i-lyrics', label: '歌词与翻译' },
      { route: 'admin/daily-mixes', icon: 'i-sliders', label: '每日推荐调试' },
    ],
  },
  {
    label: '集成与模型配置',
    items: [
      { route: 'admin/sources', icon: 'i-globe', label: '数据来源与同步' },
      { route: 'admin/ai-settings', icon: 'i-cpu', label: 'AI 设置中心' },
    ],
  },
  {
    label: '系统与安全',
    items: [
      { route: 'admin/backups', icon: 'i-database', label: '数据库备份与恢复' },
      { route: 'admin/security', icon: 'i-shield', label: '账号与安全' },
    ],
  },
];

const PLAYER_NAV_ITEMS = [
  { route: 'home', icon: 'i-home', label: '主页' },
  { route: 'albums', icon: 'i-album', label: '专辑' },
  { route: 'artist', icon: 'i-user', label: '歌手' },
  { route: 'artist', icon: 'i-users', label: '幕后人员', toast: '幕后人员视图（按作词/作曲/编曲/制作筛选）' },
  { route: 'album', icon: 'i-music', label: '歌曲' },
  { route: 'home', icon: 'i-works', label: '作品', toast: '作品浏览视图（2:3 海报网格 + 筛选）' },
  { route: 'discover', icon: 'i-compass', label: '发现' },
  { route: 'albums', icon: 'i-heart', label: '收藏', toast: '收藏视图（专辑 / 歌曲 / 歌手三页签）' },
  { route: 'albums', icon: 'i-queue', label: '歌单', toast: '歌单管理视图' },
  { route: 'home', icon: 'i-history', label: '播放记录', toast: '播放记录视图（含清空历史危险确认）' },
];

const state = {
  route: 'home',
  theme: 'dark',
  npOpen: true,
  npOverlay: false,
  npTab: 'lyrics', // 'lyrics' | 'queue'
  fullOpen: false,
  playing: true,
  shuffle: false,
  repeat: 1, // 0 off, 1 all, 2 one
  volume: 82,
  posSec: 47,
  queue: [...QUEUE_ITEMS],
  queueIndex: 0,
  lyricsOffset: 0,
  lyricsTranslate: true,
  queueFilter: '',
  castLabel: '本机网页',

  // Player view state
  homeTab: 'recent',
  randomIdx: 2,
  workFilter: 'all',
  activeYear: 2023,
  albumCols: 5,
  artistCols: 6,
  albumQuery: '',
  albumIndex: '全部',
  bulkAlbums: false,
  selectedAlbums: new Set(),
  currentAlbumId: 101,
  artistBioVer: 'wiki',
  discoverTab: 'recommended',
  discoverFilter: 'all',
  discoverSchedOpen: false,

  // Admin view state
  scanning: false,
  workReviewTab: 'albums',
  artistReviewTab: 'identities',
  analysisTab: 'audio',
  lyricsTab: 'online',
  sourcesTab: 'metadata',
  resolvedItems: new Set(),
};

const $ = (id) => document.getElementById(id);
const isAdminRoute = (r) => r.startsWith('admin/');
const fmtTime = (sec) => `${Math.floor(sec / 60)}:${String(Math.floor(sec % 60)).padStart(2, '0')}`;

function toast(msg) {
  const el = document.createElement('div');
  el.className = 'toast';
  el.innerHTML = `${ic('i-check', 'xs')} <span>${msg}</span>`;
  $('toast-region').appendChild(el);
  setTimeout(() => el.remove(), 2600);
}

function renderSidebar() {
  const admin = isAdminRoute(state.route);
  const el = $('app-sidebar');

  if (admin) {
    el.innerHTML = `
      <div class="brand-row">
        <a class="wordmark" href="#home" data-nav="home">
          <img src="../../docs/images/icon-v2.png" alt="032">
          <span>032</span><small>ADMIN</small>
        </a>
        <span class="mode-pill">工作区</span>
      </div>
      <button type="button" class="back-to-lib-btn" data-nav="home">
        ${ic('i-arrow-left', 'sm')}<span>返回音乐库</span>
      </button>
      <div class="sidebar-scroll">
        ${ADMIN_NAV_GROUPS.map((g) => `
          <div class="nav-section-label"><span>${g.label}</span></div>
          <nav class="nav-list" aria-label="${g.label}">
            ${g.items.map((it) => `
              <button type="button" class="nav-item ${state.route === it.route ? 'active' : ''}" data-nav="${it.route}">
                ${ic(it.icon)}
                <span class="nav-label">${it.label}</span>
                ${it.badge ? `<span class="nav-badge ${it.badgeCls || ''}">${it.badge}</span>` : ''}
              </button>
            `).join('')}
          </nav>
        `).join('')}
      </div>
      <div class="sidebar-footer">
        <button type="button" class="health-line" data-nav="admin/dashboard" title="服务正常">
          <span class="status-dot ${state.scanning ? 'busy' : ''}"></span>
          <span class="ell">SQLite · v0.18.4</span>
        </button>
        <button type="button" class="icon-btn" data-action="toggle-theme" title="切换深浅主题">${ic(state.theme === 'dark' ? 'i-sun' : 'i-moon', 'sm')}</button>
        <button type="button" class="icon-btn" data-action="toast" data-msg="已退出管理会话（演示）" title="退出登录">${ic('i-logout', 'sm')}</button>
      </div>
    `;
  } else {
    el.innerHTML = `
      <div class="brand-row">
        <a class="wordmark" href="#home" data-nav="home">
          <img src="../../docs/images/icon-v2.png" alt="032">
          <span>032</span><small>MUSIC</small>
        </a>
      </div>
      <button type="button" class="sidebar-search-btn" data-action="open-search">
        ${ic('i-search', 'sm')}<span>搜索曲库</span><kbd>Ctrl K</kbd>
      </button>
      <div class="sidebar-scroll">
        <div class="nav-section-label"><span>我的媒体库</span></div>
        <nav class="nav-list" aria-label="我的媒体库">
          ${PLAYER_NAV_ITEMS.map((it) => `
            <button type="button" class="nav-item ${!it.toast && state.route === it.route ? 'active' : ''}"
              ${it.toast ? `data-action="toast" data-msg="${it.toast}"` : `data-nav="${it.route}"`}>
              ${ic(it.icon)}
              <span class="nav-label">${it.label}</span>
            </button>
          `).join('')}
        </nav>

        <div class="nav-section-label">
          <span>播放列表</span>
          <span class="nav-section-actions">
            <button type="button" class="icon-btn" style="width:24px;height:24px" data-action="toast" data-msg="新建播放列表">${ic('i-plus', 'xs')}</button>
          </span>
        </div>
        <div class="nav-list">
          ${PLAYLISTS.map((p) => `
            <button type="button" class="side-playlist" data-action="toast" data-msg="打开歌单：${p.title}">
              <img src="${p.cover}" alt="">
              <div class="ell"><strong class="ell">${p.title}</strong><small class="ell">${p.sub}</small></div>
            </button>
          `).join('')}
        </div>
      </div>

      <button type="button" class="admin-entry-btn" data-nav="admin/dashboard">
        ${ic('i-settings', 'sm')}
        <span style="flex:1;text-align:left">管理后台</span>
        <span class="nav-badge">42</span>
      </button>

      <div class="sidebar-footer">
        <button type="button" class="health-line" data-nav="admin/dashboard" title="music.032 · 17,595 首 · 服务正常">
          <span class="status-dot ${state.scanning ? 'busy' : ''}"></span>
          <span class="ell tnum">17,595 首</span>
        </button>
        <button type="button" class="icon-btn" data-action="toggle-theme" title="切换深浅主题">${ic(state.theme === 'dark' ? 'i-sun' : 'i-moon', 'sm')}</button>
        <button type="button" class="icon-btn" data-action="toast" data-msg="当前用户：lux032" title="退出登录">${ic('i-logout', 'sm')}</button>
      </div>
    `;
  }
}

function renderNowPlaying() {
  const track = state.queue[state.queueIndex] || QUEUE_ITEMS[0];
  const activeLyricIdx = Math.max(0, LYRICS_LINES.findLastIndex((l) => l.time <= state.posSec));
  const qFilter = state.queueFilter.trim().toLowerCase();
  const qList = state.queue.filter((t) => !qFilter || t.title.toLowerCase().includes(qFilter) || t.artist.toLowerCase().includes(qFilter));

  $('np-panel').innerHTML = `
    <div class="np-top">
      <div class="np-cover-wrap" data-action="toggle-fullscreen" title="进入全屏播放">
        <img src="${track.cover}" alt="${track.title}">
        <div class="np-cover-overlay">${ic('i-expand', 'lg')}</div>
      </div>
      <div class="np-meta-row">
        <div class="ell" style="flex:1">
          <div class="np-title ell">${track.title}</div>
          <div class="np-sub ell">
            <button type="button" data-nav="artist">${track.artist}</button> · <button type="button" data-nav="album" data-id="101">${track.album}</button>
          </div>
        </div>
        <div style="display:flex;gap:6px;flex:none">
          <button type="button" class="btn-icon-round ${track.fav ? 'active' : ''}" style="width:34px;height:34px" data-action="toggle-np-fav" title="收藏">${ic(track.fav ? 'f-heart' : 'i-heart', track.fav ? 'xs fill' : 'xs')}</button>
          <button type="button" class="btn-icon-round" style="width:34px;height:34px" data-action="close-np" title="收起面板">${ic('i-panel', 'xs')}</button>
        </div>
      </div>
      <div class="np-badges">
        <span class="pill tnum">${track.spec}</span>
        <span class="np-status-ok">${ic('i-cast', 'xs')} ${state.castLabel}</span>
      </div>
    </div>

    <div class="np-tabs-bar">
      <button type="button" class="pill-tab ${state.npTab === 'lyrics' ? 'active' : ''}" data-action="np-tab" data-val="lyrics">歌词</button>
      <button type="button" class="pill-tab ${state.npTab === 'queue' ? 'active' : ''}" data-action="np-tab" data-val="queue">队列 <small class="tnum">${state.queue.length}</small></button>
      ${state.npTab === 'lyrics' ? '<span class="tag outline source-pill">LRCLIB</span>' : `<button type="button" class="btn btn-ghost btn-xs source-pill" data-action="toast" data-msg="已将当前队列另存为新歌单">存为歌单</button>`}
    </div>

    <div class="np-pane">
      ${state.npTab === 'lyrics' ? `
        <div class="np-lyrics-scroll">
          ${LYRICS_LINES.map((l, idx) => `
            <div class="lyric-line ${idx === activeLyricIdx ? 'active' : ''}" data-action="seek-sec" data-val="${l.time}">
              <div class="l-orig">${l.orig}</div>
              ${state.lyricsTranslate ? `<div class="l-tr">${l.tr}</div>` : ''}
            </div>
          `).join('')}
        </div>
        <div class="np-lyrics-toolbar tnum">
          <button type="button" class="ltool-btn" data-nav="admin/lyrics" title="在线歌词诊断与搜索">${ic('i-search', 'xs')}</button>
          <button type="button" class="ltool-btn" data-action="lyrics-offset" data-val="-150">${ic('i-minus', 'xs')}</button>
          <span style="min-width:52px;text-align:center">${state.lyricsOffset >= 0 ? '+' : ''}${state.lyricsOffset} ms</span>
          <button type="button" class="ltool-btn" data-action="lyrics-offset" data-val="150">${ic('i-plus', 'xs')}</button>
          <button type="button" class="ltool-btn ${state.lyricsTranslate ? 'active' : ''}" data-action="toggle-translate">${ic('i-lang', 'xs')} 翻译</button>
        </div>
      ` : `
        <div class="np-queue-head">
          <input class="input" type="search" value="${state.queueFilter}" placeholder="搜索队列…" data-input="queue-filter">
          <button type="button" class="btn btn-ghost btn-xs" data-action="toast" data-msg="队列已清空（演示保留数据）">清空</button>
        </div>
        <ol class="np-queue-list">
          ${qList.map((t, idx) => `
            <li class="q-row ${t.id === track.id ? 'playing' : ''}" data-action="play-queue-idx" data-val="${state.queue.indexOf(t)}">
              <span class="q-num tnum">${t.id === track.id ? ic('i-wave', 'xs') : idx + 1}</span>
              <img src="${t.cover}" alt="">
              <div class="ell"><strong class="ell">${t.title}</strong><small class="ell">${t.artist}</small></div>
              <span class="q-dur tnum">${t.dur}</span>
            </li>
          `).join('')}
        </ol>
      `}
    </div>
  `;
}

function renderPlayerBar() {
  const track = state.queue[state.queueIndex] || QUEUE_ITEMS[0];
  const pct = Math.min(100, Math.max(0, (state.posSec / track.sec) * 100));
  const panelVisible = isAdminRoute(state.route) ? state.npOverlay : (state.npOpen || state.npOverlay);

  $('player-bar').innerHTML = `
    <div class="bar-left">
      <div class="bar-cover" data-action="toggle-fullscreen" title="全屏播放">
        <img src="${track.cover}" alt="${track.title}">
      </div>
      <div class="bar-meta">
        <div class="bar-title-row">
          <strong class="ell">${track.title}</strong>
          <button type="button" class="icon-btn ${track.fav ? 'active' : ''}" style="width:26px;height:26px" data-action="toggle-np-fav" title="收藏">${ic(track.fav ? 'f-heart' : 'i-heart', track.fav ? 'xs fill' : 'xs')}</button>
        </div>
        <div class="bar-sub-row">
          <span class="pill tnum">${track.spec}</span>
          <span class="ell"><button type="button" data-nav="artist">${track.artist}</button> · <button type="button" data-nav="album" data-id="101">${track.album}</button></span>
        </div>
      </div>
    </div>

    <div class="bar-center">
      <div class="transport-row">
        <button type="button" class="t-btn ${state.shuffle ? 'active' : ''}" data-action="toggle-shuffle" title="随机播放">${ic('i-shuffle', 'sm')}</button>
        <button type="button" class="t-btn skip" data-action="prev-track" title="上一首">${ic('f-prev', 'fill')}</button>
        <button type="button" class="play-main-btn" data-action="toggle-play" title="播放 / 暂停 (Space)">${ic(state.playing ? 'f-pause' : 'f-play', 'fill')}</button>
        <button type="button" class="t-btn skip" data-action="next-track" title="下一首">${ic('f-next', 'fill')}</button>
        <button type="button" class="t-btn ${state.repeat > 0 ? 'active' : ''}" data-action="cycle-repeat" title="循环模式">${ic('i-repeat', 'sm')}</button>
      </div>
      <div class="seek-row tnum">
        <span>${fmtTime(state.posSec)}</span>
        <div class="range-rail" data-action="seek-bar" role="slider" aria-label="播放进度">
          <div class="range-track"><div class="range-fill" style="width:${pct.toFixed(1)}%"></div></div>
        </div>
        <span>${track.dur}</span>
      </div>
    </div>

    <div class="bar-right">
      <button type="button" class="bar-chip ${state.castLabel !== '本机网页' ? 'on' : ''}" data-action="cycle-cast" title="投送到 Sonos / DLNA">
        ${ic('i-cast', 'sm')}<span>${state.castLabel === '本机网页' ? '投放' : state.castLabel}</span>
      </button>
      <span class="bar-sep"></span>
      <button type="button" class="icon-btn ${panelVisible && state.npTab === 'lyrics' ? 'active' : ''}" data-action="bar-panel-tab" data-val="lyrics" title="歌词 (L)">${ic('i-lyrics', 'sm')}</button>
      <button type="button" class="icon-btn ${panelVisible && state.npTab === 'queue' ? 'active' : ''}" data-action="bar-panel-tab" data-val="queue" title="播放队列">${ic('i-queue', 'sm')}</button>
      <button type="button" class="icon-btn ${state.fullOpen ? 'active' : ''}" data-action="toggle-fullscreen" title="全屏播放">${ic('i-expand', 'sm')}</button>
      <span class="bar-sep"></span>
      <div class="vol-wrap">
        ${ic('i-volume', 'sm')}
        <div class="range-rail" data-action="volume-bar" role="slider" aria-label="音量">
          <div class="range-track"><div class="range-fill" style="width:${state.volume}%"></div></div>
        </div>
      </div>
    </div>
  `;
}

function renderFullscreen() {
  const stage = $('fullscreen-stage');
  stage.hidden = !state.fullOpen;
  if (!state.fullOpen) return;

  const track = state.queue[state.queueIndex] || QUEUE_ITEMS[0];
  const activeLyricIdx = Math.max(0, LYRICS_LINES.findLastIndex((l) => l.time <= state.posSec));

  stage.innerHTML = `
    <div class="fs-top">
      <button type="button" class="btn-icon-round" data-action="toggle-fullscreen" title="退出全屏 (Esc)">${ic('i-down', 'sm')}</button>
      <div style="display:flex;align-items:center;gap:10px">
        <span style="font-weight:700;font-size:14px">歌词</span>
        <span class="tag outline">LRCLIB</span>
        <button type="button" class="ltool-btn ${state.lyricsTranslate ? 'active' : ''}" data-action="toggle-translate">${ic('i-lang', 'xs')} 双语翻译</button>
      </div>
      <button type="button" class="btn btn-secondary btn-sm" data-drawer="track-tags" data-id="${track.id}">${ic('i-sparkle', 'xs')} 曲目 AI 标签</button>
    </div>
    <div class="fs-grid">
      <div class="fs-art-col">
        <img src="${track.cover}" alt="${track.title}">
        <h2>${track.title}</h2>
        <p>${track.artist} · ${track.album}</p>
        <div style="display:flex;gap:8px;margin-top:14px;align-items:center">
          <span class="pill tnum">${track.spec}</span>
          <span class="np-status-ok">${ic('i-cast', 'xs')} ${state.castLabel}</span>
        </div>
      </div>
      <div class="fs-lyrics-col">
        ${LYRICS_LINES.map((l, idx) => `
          <div class="lyric-line ${idx === activeLyricIdx ? 'active' : ''}" data-action="seek-sec" data-val="${l.time}">
            <div class="l-orig">${l.orig}</div>
            ${state.lyricsTranslate ? `<div class="l-tr">${l.tr}</div>` : ''}
          </div>
        `).join('')}
      </div>
      <div class="fs-queue-card">
        <div style="padding:14px 16px;border-bottom:1px solid var(--border);display:flex;justify-content:space-between;align-items:center">
          <strong style="font-size:14px">接下来播放</strong>
          <span class="tag outline tnum">${state.queue.length} 首</span>
        </div>
        <ol class="np-queue-list" style="padding-top:8px">
          ${state.queue.map((t, idx) => `
            <li class="q-row ${idx === state.queueIndex ? 'playing' : ''}" data-action="play-queue-idx" data-val="${idx}">
              <span class="q-num tnum">${idx + 1}</span>
              <img src="${t.cover}" alt="">
              <div class="ell"><strong class="ell">${t.title}</strong><small class="ell">${t.artist}</small></div>
              <span class="q-dur tnum">${t.dur}</span>
            </li>
          `).join('')}
        </ol>
      </div>
    </div>
  `;
}

function renderMain() {
  const main = $('app-main');
  if (isAdminRoute(state.route)) {
    main.innerHTML = renderAdminView(state.route, state);
    const activeItem = ADMIN_NAV_GROUPS.flatMap((g) => g.items).find((i) => i.route === state.route);
    $('mobile-admin-title').textContent = activeItem ? activeItem.label : '管理后台';
  } else {
    if (state.route === 'home') main.innerHTML = renderHome(state);
    else if (state.route === 'albums') main.innerHTML = renderAlbums(state);
    else if (state.route === 'album') main.innerHTML = renderAlbumDetail(state);
    else if (state.route === 'artist') main.innerHTML = renderArtistDetail(state);
    else if (state.route === 'discover') main.innerHTML = renderDiscover(state);
    else main.innerHTML = renderHome(state);
  }
  main.scrollTop = 0;
}

function syncShellClasses() {
  const admin = isAdminRoute(state.route);
  document.documentElement.dataset.theme = state.theme;
  document.body.classList.toggle('mode-admin', admin);
  document.body.classList.toggle('mode-player', !admin);
  document.body.classList.toggle('np-closed', !admin && !state.npOpen && !state.npOverlay);
  document.body.classList.toggle('np-overlay', state.npOverlay);
  $('np-scrim').hidden = !state.npOverlay;
  document.querySelectorAll('#mobile-bottom-nav button').forEach((b) => {
    b.classList.toggle('active', b.dataset.nav === state.route);
  });
}

function renderAll() {
  syncShellClasses();
  renderSidebar();
  renderMain();
  renderNowPlaying();
  renderPlayerBar();
  renderFullscreen();
}

function navigate(route, opts = {}) {
  const prevAdmin = isAdminRoute(state.route);
  const nextAdmin = isAdminRoute(route);
  state.route = route;
  if (opts.id) state.currentAlbumId = Number(opts.id);
  if (opts.subtab && route === 'admin/sources') state.sourcesTab = opts.subtab;
  if (nextAdmin && !prevAdmin) state.npOverlay = false;
  document.body.classList.remove('mobile-drawer-open');
  $('sidebar-scrim').hidden = true;
  if (location.hash !== `#${route}`) history.replaceState(null, '', `#${route}`);
  renderAll();
}

function openDrawer(type, id) {
  const isAdminD = type === 'avatar-strategy' || type === 'translation-detail';
  const cfg = isAdminD ? renderAdminDrawer(type, id) : renderPlayerDrawer(type, id);
  const drawer = $('app-drawer');
  drawer.innerHTML = `
    <div class="drawer-head">
      <h2 id="drawer-title">${cfg.title}</h2>
      <button type="button" class="icon-btn" data-action="close-drawer" aria-label="关闭">${ic('i-x', 'sm')}</button>
    </div>
    <div class="drawer-body">${cfg.body}</div>
    <div class="drawer-foot">${cfg.foot}</div>
  `;
  drawer.hidden = false;
  $('drawer-scrim').hidden = false;
}

function closeDrawer() {
  $('app-drawer').hidden = true;
  $('drawer-scrim').hidden = true;
}

function openSearchModal() {
  const m = $('search-modal');
  m.innerHTML = `
    <div class="search-modal-input-row">
      ${ic('i-search', 'sm')}
      <input id="cmd-search-input" type="search" placeholder="搜索专辑、歌手、作品，或直达管理页面…" autofocus>
      <button type="button" class="icon-btn" data-action="close-search">${ic('i-x', 'sm')}</button>
    </div>
    <div class="search-modal-results" id="cmd-search-results"></div>
  `;
  m.hidden = false;
  $('search-modal-scrim').hidden = false;
  renderSearchHits('');
  setTimeout(() => $('cmd-search-input')?.focus(), 10);
}

function renderSearchHits(q) {
  const term = q.trim().toLowerCase();
  const hits = [
    ...ALBUMS.slice(0, 4).map((a) => ({ label: a.title, sub: `专辑 · ${a.artist}`, img: a.cover, route: 'album', id: a.id })),
    ...ADMIN_NAV_GROUPS.flatMap((g) => g.items.map((i) => ({ label: i.label, sub: `管理 · ${g.label}`, icon: i.icon, route: i.route }))),
  ].filter((h) => !term || h.label.toLowerCase().includes(term) || h.sub.toLowerCase().includes(term)).slice(0, 8);

  const box = $('cmd-search-results');
  if (!box) return;
  box.innerHTML = hits.map((h) => `
    <button type="button" class="search-hit" data-action="cmd-go" data-nav="${h.route}" ${h.id ? `data-id="${h.id}"` : ''}>
      ${h.img ? `<img src="${h.img}" alt="">` : `<span class="icon-btn">${ic(h.icon, 'sm')}</span>`}
      <div class="ell"><strong class="ell" style="display:block;font-size:14px">${h.label}</strong><small style="color:var(--text-faint)">${h.sub}</small></div>
    </button>
  `).join('') || '<p style="padding:18px;text-align:center;color:var(--text-faint)">没有匹配结果</p>';
}

function closeSearchModal() {
  $('search-modal').hidden = true;
  $('search-modal-scrim').hidden = true;
}

// Global Click Delegation
document.addEventListener('click', (e) => {
  const drawerTrigger = e.target.closest('[data-drawer]');
  if (drawerTrigger) {
    e.preventDefault();
    e.stopPropagation();
    openDrawer(drawerTrigger.dataset.drawer, drawerTrigger.dataset.id);
    return;
  }

  const actionEl = e.target.closest('[data-action]');
  if (actionEl) {
    const act = actionEl.dataset.action;
    if (actionEl.hasAttribute('data-stop')) e.stopPropagation();

    if (act === 'toast') { toast(actionEl.dataset.msg || '操作已完成'); return; }
    if (act === 'toggle-theme') { state.theme = state.theme === 'dark' ? 'light' : 'dark'; renderAll(); return; }
    if (act === 'open-search') { openSearchModal(); return; }
    if (act === 'close-search') { closeSearchModal(); return; }
    if (act === 'cmd-go') { closeSearchModal(); navigate(actionEl.dataset.nav, { id: actionEl.dataset.id }); return; }
    if (act === 'close-drawer') { closeDrawer(); return; }
    if (act === 'save-drawer') { closeDrawer(); toast(actionEl.dataset.msg || '已保存'); return; }

    if (act === 'home-tab') { state.homeTab = actionEl.dataset.val; renderMain(); return; }
    if (act === 'home-reroll') { state.randomIdx = (state.randomIdx + 1) % ALBUMS.length; renderMain(); return; }
    if (act === 'work-filter') { state.workFilter = actionEl.dataset.val; renderMain(); return; }
    if (act === 'select-year') { state.activeYear = Number(actionEl.dataset.val); renderMain(); return; }
    if (act === 'album-index') { state.albumIndex = actionEl.dataset.val; renderMain(); return; }
    if (act === 'toggle-bulk-albums') { state.bulkAlbums = !state.bulkAlbums; if (!state.bulkAlbums) state.selectedAlbums.clear(); renderMain(); return; }
    if (act === 'toggle-album-select') {
      e.stopPropagation();
      const id = Number(actionEl.dataset.id);
      state.selectedAlbums.has(id) ? state.selectedAlbums.delete(id) : state.selectedAlbums.add(id);
      renderMain(); return;
    }
    if (act === 'clear-album-select') { state.selectedAlbums.clear(); state.bulkAlbums = false; renderMain(); return; }
    if (act === 'toggle-album-fav') {
      e.stopPropagation();
      const a = ALBUMS.find((x) => x.id === Number(actionEl.dataset.id));
      if (a) { a.favorite = !a.favorite; toast(a.favorite ? `已收藏《${a.title}》` : `已取消收藏《${a.title}》`); renderMain(); }
      return;
    }
    if (act === 'artist-bio-ver') { state.artistBioVer = actionEl.dataset.val; renderMain(); return; }
    if (act === 'discover-tab') { state.discoverTab = actionEl.dataset.val; renderMain(); return; }
    if (act === 'discover-filter') { state.discoverFilter = actionEl.dataset.val; renderMain(); return; }
    if (act === 'toggle-discover-sched') { state.discoverSchedOpen = !state.discoverSchedOpen; renderMain(); return; }

    if (act === 'work-review-tab') { state.workReviewTab = actionEl.dataset.val; renderMain(); return; }
    if (act === 'artist-review-tab') { state.artistReviewTab = actionEl.dataset.val; renderMain(); return; }
    if (act === 'analysis-tab') { state.analysisTab = actionEl.dataset.val; renderMain(); return; }
    if (act === 'lyrics-tab') { state.lyricsTab = actionEl.dataset.val; renderMain(); return; }
    if (act === 'sources-tab') { state.sourcesTab = actionEl.dataset.val; renderMain(); return; }
    if (act === 'resolve-item') { state.resolvedItems.add(actionEl.dataset.id); toast(actionEl.dataset.msg || '已处理'); renderMain(); return; }
    if (act === 'trigger-scan') {
      state.scanning = !state.scanning;
      toast(state.scanning ? `已启动${actionEl.dataset.val === 'full' ? '完整重扫' : '增量扫描'}` : '扫描已完成');
      renderSidebar(); renderMain(); return;
    }

    // Player & Right Panel controls
    if (act === 'play-album') {
      e.stopPropagation();
      const a = ALBUMS.find((x) => x.id === Number(actionEl.dataset.id));
      state.playing = true; state.posSec = 18;
      toast(`正在播放专辑《${a ? a.title : '幻燈'}》`);
      renderPlayerBar(); renderNowPlaying(); return;
    }
    if (act === 'play-track') {
      const id = Number(actionEl.dataset.id);
      const qIdx = state.queue.findIndex((t) => t.id === id);
      if (qIdx >= 0) state.queueIndex = qIdx;
      else {
        const t = ALBUM_TRACKS.find((x) => x.id === id);
        if (t) {
          state.queue.unshift({ id: t.id, title: t.title, artist: t.artist, album: '幻燈', spec: 'FLAC 24bit / 96kHz', dur: t.dur, sec: t.sec, cover: ALBUMS[0].cover, fav: t.fav });
          state.queueIndex = 0;
        }
      }
      state.playing = true; state.posSec = 36;
      renderNowPlaying(); renderPlayerBar(); renderFullscreen();
      if (state.route === 'album') renderMain();
      return;
    }
    if (act === 'play-queue-idx') {
      state.queueIndex = Number(actionEl.dataset.val); state.playing = true; state.posSec = 26;
      renderNowPlaying(); renderPlayerBar(); renderFullscreen(); return;
    }
    if (act === 'toggle-play') { state.playing = !state.playing; renderPlayerBar(); return; }
    if (act === 'prev-track') { state.queueIndex = (state.queueIndex - 1 + state.queue.length) % state.queue.length; state.posSec = 26; renderNowPlaying(); renderPlayerBar(); renderFullscreen(); return; }
    if (act === 'next-track') { state.queueIndex = (state.queueIndex + 1) % state.queue.length; state.posSec = 26; renderNowPlaying(); renderPlayerBar(); renderFullscreen(); return; }
    if (act === 'toggle-shuffle') { state.shuffle = !state.shuffle; renderPlayerBar(); return; }
    if (act === 'cycle-repeat') { state.repeat = (state.repeat + 1) % 3; renderPlayerBar(); return; }
    if (act === 'toggle-np-fav') {
      const t = state.queue[state.queueIndex];
      if (t) { t.fav = !t.fav; toast(t.fav ? `已收藏《${t.title}》` : `已取消收藏《${t.title}》`); renderNowPlaying(); renderPlayerBar(); }
      return;
    }
    if (act === 'np-tab') { state.npTab = actionEl.dataset.val; renderNowPlaying(); renderPlayerBar(); return; }
    if (act === 'close-np') { state.npOpen = false; state.npOverlay = false; syncShellClasses(); renderPlayerBar(); return; }
    if (act === 'bar-panel-tab') {
      const tab = actionEl.dataset.val;
      const overlayMode = isAdminRoute(state.route) || window.innerWidth <= 1180;
      if (overlayMode) {
        if (state.npOverlay && state.npTab === tab) state.npOverlay = false;
        else { state.npTab = tab; state.npOverlay = true; }
      } else {
        if (state.npOpen && state.npTab === tab) state.npOpen = false;
        else { state.npTab = tab; state.npOpen = true; }
      }
      syncShellClasses(); renderNowPlaying(); renderPlayerBar(); return;
    }
    if (act === 'toggle-fullscreen') { state.fullOpen = !state.fullOpen; renderFullscreen(); renderPlayerBar(); return; }
    if (act === 'lyrics-offset') { state.lyricsOffset += Number(actionEl.dataset.val); renderNowPlaying(); return; }
    if (act === 'toggle-translate') { state.lyricsTranslate = !state.lyricsTranslate; renderNowPlaying(); renderFullscreen(); return; }
    if (act === 'seek-sec') { state.posSec = Number(actionEl.dataset.val); renderNowPlaying(); renderPlayerBar(); renderFullscreen(); return; }
    if (act === 'cycle-cast') {
      const opts = ['本机网页', 'Sonos · 客厅', 'Sonos · 书房'];
      state.castLabel = opts[(opts.indexOf(state.castLabel) + 1) % opts.length];
      toast(`输出目标已切换：${state.castLabel}`);
      renderNowPlaying(); renderPlayerBar(); return;
    }
    if (act === 'seek-bar') {
      const rect = actionEl.getBoundingClientRect();
      const ratio = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
      const track = state.queue[state.queueIndex] || QUEUE_ITEMS[0];
      state.posSec = Math.round(ratio * track.sec);
      renderNowPlaying(); renderPlayerBar(); renderFullscreen(); return;
    }
    if (act === 'volume-bar') {
      const rect = actionEl.getBoundingClientRect();
      state.volume = Math.round(Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width)) * 100);
      renderPlayerBar(); return;
    }
  }

  const navEl = e.target.closest('[data-nav]');
  if (navEl && !e.target.closest('[data-stop]')) {
    e.preventDefault();
    navigate(navEl.dataset.nav, { id: navEl.dataset.id, subtab: navEl.dataset.subtab });
  }
});

document.addEventListener('input', (e) => {
  const el = e.target;
  if (el.dataset.input === 'album-cols') {
    state.albumCols = Number(el.value);
    const grid = document.querySelector('.album-grid');
    if (grid) grid.style.setProperty('--cols', String(state.albumCols));
    el.nextElementSibling.textContent = String(state.albumCols);
  } else if (el.dataset.input === 'artist-cols') {
    state.artistCols = Number(el.value);
    const grid = document.querySelector('[data-artist-grid]');
    if (grid) grid.style.setProperty('--cols', String(state.artistCols));
    el.nextElementSibling.textContent = String(state.artistCols);
  } else if (el.dataset.input === 'album-query') {
    state.albumQuery = el.value;
    renderMain();
  } else if (el.dataset.input === 'queue-filter') {
    state.queueFilter = el.value;
    renderNowPlaying();
  } else if (el.id === 'cmd-search-input') {
    renderSearchHits(el.value);
  }
});

// Scrims & Mobile Menu
$('drawer-scrim').addEventListener('click', closeDrawer);
$('search-modal-scrim').addEventListener('click', closeSearchModal);
$('np-scrim').addEventListener('click', () => { state.npOverlay = false; syncShellClasses(); renderPlayerBar(); });
$('sidebar-scrim').addEventListener('click', () => { document.body.classList.remove('mobile-drawer-open'); $('sidebar-scrim').hidden = true; });
$('mobile-admin-menu-btn').addEventListener('click', () => {
  const open = document.body.classList.toggle('mobile-drawer-open');
  $('sidebar-scrim').hidden = !open;
});

// Keyboard shortcuts
document.addEventListener('keydown', (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); openSearchModal(); return; }
  if (e.key === 'Escape') {
    if (!$('search-modal').hidden) { closeSearchModal(); return; }
    if (!$('app-drawer').hidden) { closeDrawer(); return; }
    if (state.fullOpen) { state.fullOpen = false; renderFullscreen(); renderPlayerBar(); return; }
    if (state.npOverlay) { state.npOverlay = false; syncShellClasses(); renderPlayerBar(); return; }
  }
  if (['INPUT', 'TEXTAREA', 'SELECT'].includes(document.activeElement?.tagName)) return;
  if (e.key === ' ') { e.preventDefault(); state.playing = !state.playing; renderPlayerBar(); }
});

// Sync route from URL hash
function loadHash() {
  const h = location.hash.replace(/^#/, '');
  const valid = new Set([
    'home', 'albums', 'album', 'artist', 'discover',
    ...ADMIN_NAV_GROUPS.flatMap((g) => g.items.map((i) => i.route)),
  ]);
  if (valid.has(h)) state.route = h;
}
window.addEventListener('hashchange', () => { loadHash(); renderAll(); });

// Subtle live playback clock
setInterval(() => {
  if (!state.playing) return;
  const track = state.queue[state.queueIndex] || QUEUE_ITEMS[0];
  state.posSec = (state.posSec + 1) % track.sec;
  renderPlayerBar();
}, 1000);

loadHash();
renderAll();
