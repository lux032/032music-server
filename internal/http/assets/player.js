// 032 Music Server - Global Audio Player, Now-Playing Panel & PJAX Navigation
(function() {
  'use strict';

  if (window.__032_player_initialized) return;
  window.__032_player_initialized = true;

  // Global state
  let audio = null;
  let queue = [];
  let currentIndex = -1;
  let isPlaying = false;
  let loopMode = 'all'; // 'all', 'one'
  let shuffleOn = false;
  let lyrics = [];
  let activeLyricIndex = -1;
  let lastScrobbledTrackId = null;
  let timelineReportTimer = null;
  let isDraggingProgress = false;
  // [P2-2] Shuffle history stores track ids, not queue positions, so it
  // survives splice insertions; a full queue replacement clears it.
  let playHistory = [];

  // ------------------------------------------------------------------ helpers
  // The CSRF meta tag lives in <head>, which PJAX never replaces, so the
  // token is re-read on every request AND re-synced in applyPage() after
  // each navigation (the server may have rotated the session token).
  function csrfToken() {
    const meta = document.querySelector('meta[name="csrf-token"]');
    return meta ? (meta.getAttribute('content') || '') : '';
  }

  // apiFetch wraps fetch for /api/v1 calls: session-authenticated state
  // changing requests must echo the session CSRF token in a header.
  function apiFetch(url, options) {
    const opts = Object.assign({ credentials: 'same-origin' }, options);
    const method = (opts.method || 'GET').toUpperCase();
    const headers = Object.assign({}, opts.headers);
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
      const token = csrfToken();
      if (token) headers['X-CSRF-Token'] = token;
    }
    opts.headers = headers;
    return fetch(url, opts);
  }

  function spriteURL() {
    const meta = document.querySelector('meta[name="icon-sprite"]');
    return meta ? (meta.getAttribute('content') || '') : '/admin/assets/icons.svg';
  }

  // svgIcon builds an icon node via DOM APIs; icon names are internal
  // constants, never user data.
  function svgIcon(name) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'icon');
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', spriteURL() + '#' + name);
    svg.appendChild(use);
    return svg;
  }

  // swapIcon replaces the glyph of an icon button in place.
  function swapIcon(btn, name) {
    const use = btn ? btn.querySelector('use') : null;
    if (use) use.setAttribute('href', spriteURL() + '#' + name);
  }

  // hydrateIconSlots replaces static <span class="icon-slot" data-icon>
  // placeholders inside freshly built chrome with real SVG nodes.
  function hydrateIconSlots(root) {
    root.querySelectorAll('.icon-slot[data-icon]').forEach((slot) => {
      slot.replaceWith(svgIcon(slot.dataset.icon));
    });
  }

  function init() {
    if (!document.body || document.body.classList.contains('login-page')) return;

    createPlayerDOM();
    setupAudioElement();
    setupNowPlayingPanel();
    restoreState();
    bindTrackListEvents();
    bindGlobalShortcuts();
    setupPjaxNavigation();
    setupImageFadeIn();

    // [V3] Enable shell transitions only after the first painted frame so a
    // below-1440px first load does not animate the panel sliding away.
    requestAnimationFrame(() => requestAnimationFrame(() => {
      document.body.classList.add('np-animated');
    }));

    if (timelineReportTimer) clearInterval(timelineReportTimer);
    timelineReportTimer = setInterval(reportTimelineProgress, 5000);
    window.addEventListener('pagehide', saveState);
  }

  // ---------------------------------------------------------------------
  // PJAX navigation: full page loads destroy the <audio> element and the
  // browser autoplay policy blocks resuming without a user gesture, so
  // in-app navigation swaps <main id="app-main"> via fetch and keeps
  // playback alive. The sidebar, now-playing panel and player bar are
  // persistent chrome; only the navigation highlight is synced.
  // ---------------------------------------------------------------------
  let navigationAbortController = null;
  let currentDocUrl = window.location.href;
  const pendingForms = new Map();
  const draftStorageKey = '032_form_draft';

  function setupPjaxNavigation() {
    if (!window.fetch || !window.history || !window.DOMParser) return;
    ensureCurrentHistoryState();
    restoreSafeDraft();
    document.addEventListener('click', (e) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const link = e.target instanceof Element ? e.target.closest('a[href]') : null;
      if (!link || (link.target && link.target !== '_self') || link.hasAttribute('download')) return;
      let url;
      try { url = new URL(link.href, window.location.href); } catch (_) { return; }
      if (url.origin !== window.location.origin || !url.pathname.startsWith('/admin') || url.pathname.startsWith('/admin/assets/')) return;
      const current = new URL(window.location.href);
      if (url.pathname === current.pathname && url.search === current.search && url.hash) return;
      if (pendingForms.size && !window.confirm('更改仍在保存中。现在离开可能无法确认保存结果。是否仍要离开？')) { e.preventDefault(); return; }
      e.preventDefault();
      pjaxNavigate(url.href, true);
    });
    document.addEventListener('click', (e) => {
      const cancel = e.target instanceof Element ? e.target.closest('[data-cancel-confirm]') : null;
      if (cancel) cancel.closest('details')?.removeAttribute('open');
    });
    document.addEventListener('submit', (e) => {
      const form = e.target;
      if (!(form instanceof HTMLFormElement) || e.defaultPrevented) return;
      const method = (form.getAttribute('method') || 'get').toLowerCase();
      let action;
      try { action = new URL(form.getAttribute('action') || window.location.href, window.location.href); } catch (_) { return; }
      if (action.origin !== window.location.origin || !action.pathname.startsWith('/admin') || action.pathname === '/admin/logout' || form.querySelector('input[type="file"]')) return;
      const submitter = e.submitter;
      const data = new FormData(form);
      if (submitter && submitter.name) data.append(submitter.name, submitter.value);
      const params = new URLSearchParams(data);
      e.preventDefault();
      if (method === 'post') {
        if (!pendingForms.has(form)) pjaxPost(action.href, params, form, submitter);
      } else {
        action.search = params.toString();
        pjaxNavigate(action.href, true);
      }
    });
    window.addEventListener('popstate', (e) => {
      // Chromium fires popstate for same-document fragment changes when history.state
      // is non-null; those must not trigger a PJAX fetch of the same page.
      const prev = new URL(currentDocUrl);
      const next = new URL(window.location.href);
      currentDocUrl = window.location.href;
      if (prev.pathname === next.pathname && prev.search === next.search) return;
      pjaxNavigate(window.location.href, false, e.state);
    });
  }

  function pageState() {
    const active = document.activeElement;
    let focus = '';
    if (active instanceof Element && active.id) focus = '#' + CSS.escape(active.id);
    else if (active instanceof Element && active.closest('[data-track-id]')) focus = `[data-track-id="${CSS.escape(active.closest('[data-track-id]').dataset.trackId || '')}"] .row-play-btn`;
    return { app: '032', url: window.location.href, scrollX: window.scrollX, scrollY: window.scrollY, focus };
  }
  function ensureCurrentHistoryState() { history.replaceState({ ...(history.state || {}), ...pageState() }, '', window.location.href); }
  function saveCurrentHistoryState() { history.replaceState({ ...(history.state || {}), ...pageState() }, '', window.location.href); }

  // ------------------------------------------------------------ progress bar
  let progressShowTimer = null;
  function startNavProgress() {
    // [M1] Rapid back-to-back navigations share one bar; a stale show-timer
    // from the previous navigation must never fire into the new one.
    if (progressShowTimer) { clearTimeout(progressShowTimer); progressShowTimer = null; }
    let bar = document.getElementById('pjax-progress');
    if (!bar) {
      bar = document.createElement('div');
      bar.id = 'pjax-progress';
      document.body.appendChild(bar);
    }
    bar.classList.remove('done', 'active');
    bar.style.width = '0';
    // Fast requests never show the bar; it appears only once navigation
    // has taken noticeably long.
    progressShowTimer = setTimeout(() => {
      bar.classList.add('active');
      bar.style.width = '35%';
    }, 150);
  }
  function finishNavProgress() {
    if (progressShowTimer) { clearTimeout(progressShowTimer); progressShowTimer = null; }
    const bar = document.getElementById('pjax-progress');
    if (!bar || !bar.classList.contains('active')) return;
    bar.classList.remove('active');
    bar.classList.add('done');
    bar.style.width = '100%';
    setTimeout(() => {
      bar.classList.remove('done');
      bar.style.width = '0';
    }, 500);
  }

  async function pjaxNavigate(url, push, restoreState) {
    // Declared outside try so finally can compare against the current
    // controller (const inside try would be out of scope in finally).
    let controller = null;
    try {
      saveCurrentHistoryState();
      if (navigationAbortController) navigationAbortController.abort();
      controller = new AbortController();
      navigationAbortController = controller;
      startNavProgress();
      const res = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/html' }, signal: controller.signal });
      const html = await res.text();
      // applyPage returns true on success, 'reload' on a build mismatch and
      // false on structural failures; the latter two both need a full
      // navigation, which also picks up fresh assets on a new deployment.
      if (!res.ok) { window.location.href = res.url || url; return; }
      const destination = res.url || url;
      // A composing search must keep its live input; the browse controls
      // cancel only their own stale response, not unrelated navigation.
      if (!document.dispatchEvent(new CustomEvent('032:pjax-before-swap', { cancelable: true, detail: { url: destination } }))) return;
      if (applyPage(html, destination, push, restoreState) !== true) window.location.href = destination;
    } catch (err) {
      if (err && err.name === 'AbortError') return;
      window.location.href = url;
    } finally {
      // [M1] A superseded navigation must not finish the progress bar its
      // successor is still using.
      if (controller && navigationAbortController === controller) finishNavProgress();
    }
  }

  window.__032BrowseNavigate = (url, replace) => {
    if (pendingForms.size && !window.confirm('更改仍在保存中。现在离开可能无法确认保存结果。是否仍要离开？')) return false;
    return pjaxNavigate(url, !replace, replace ? { app: '032', url } : null);
  };

  function setFormPending(form, submitter, pending) {
    form.classList.toggle('is-pending', pending);
    form.setAttribute('aria-busy', String(pending));
    form.querySelectorAll('button[type="submit"], button:not([type]), input[type="submit"]').forEach((button) => {
      if (pending) { button.dataset.wasDisabled = button.disabled ? '1' : '0'; button.disabled = true; }
      else if (button.dataset.wasDisabled !== '1') button.disabled = false;
    });
    if (submitter instanceof HTMLButtonElement) {
      if (pending) { submitter.dataset.originalText = submitter.textContent; submitter.textContent = '保存中…'; }
      else if (submitter.dataset.originalText) { submitter.textContent = submitter.dataset.originalText; delete submitter.dataset.originalText; }
    }
  }

  async function pjaxPost(url, params, form, submitter) {
    setFormPending(form, submitter, true);
    pendingForms.set(form, true);
    try {
      const res = await fetch(url, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/x-www-form-urlencoded', Accept: 'text/html' }, body: params.toString() });
      const html = await res.text();
      const doc = new DOMParser().parseFromString(html, 'text/html');
      if (doc.body && doc.body.classList.contains('login-page')) {
        storeSafeDraft(form);
        // res.url can still equal the POST target when the redirect is mocked or
        // not followed; never navigate back to the form action in that case.
        const target = res.url && res.url !== url ? res.url : '/admin/login';
        window.location.href = target;
        return;
      }
      if (!res.ok) {
        showFormError(form, res.status === 403 ? '操作被拒绝，请刷新页面后重试。输入内容已保留。' : `保存失败（${res.status}），输入内容已保留。`);
        return;
      }
      sessionStorage.removeItem(draftStorageKey);
      const applied = applyPage(html, res.url || window.location.href, true);
      if (applied === 'reload') {
        // The write succeeded but the response belongs to a newer build:
        // navigate to the result page directly instead of reporting a
        // failure (the form must not look retryable, it already committed).
        window.location.href = res.url || url;
        return;
      }
      if (!applied) showFormError(form, '服务器已响应，但页面无法更新。请刷新核对保存结果。');
    } catch (_) {
      showFormError(form, '无法确认保存结果。输入内容已保留；请核对后再决定是否重试。');
    } finally {
      pendingForms.delete(form);
      if (document.contains(form)) setFormPending(form, submitter, false);
    }
  }

  function showFormError(form, message) {
    let error = form.querySelector('.form-error');
    if (!error) { error = document.createElement('div'); error.className = 'form-error'; error.setAttribute('role', 'alert'); form.prepend(error); }
    error.textContent = message; error.tabIndex = -1; error.focus(); showToast(message, true);
  }
  function safeDraftFields(form) {
    return Array.from(new FormData(form).entries()).filter(([name]) => !/password|token|secret|key|csrf/i.test(name)).map(([name, value]) => [name, String(value)]);
  }
  function storeSafeDraft(form) {
    try { sessionStorage.setItem(draftStorageKey, JSON.stringify({ page: window.location.pathname + window.location.search, action: form.action, fields: safeDraftFields(form) })); } catch (_) {}
  }
  function restoreSafeDraft() {
    try {
      const raw = sessionStorage.getItem(draftStorageKey); if (!raw) return;
      const draft = JSON.parse(raw); if (draft.page !== window.location.pathname + window.location.search) return;
      const form = Array.from(document.forms).find((candidate) => candidate.action === draft.action); if (!form) return;
      draft.fields.forEach(([name, value]) => { const field = form.elements.namedItem(name); if (field && !/password|token|secret|key|csrf/i.test(name)) field.value = value; });
      showToast('已恢复登录前的非敏感输入，请核对后手动保存。', true); sessionStorage.removeItem(draftStorageKey);
    } catch (_) { sessionStorage.removeItem(draftStorageKey); }
  }

  const ADMIN_NAV_KEYS = ['console', 'matches', 'enrichment', 'merges', 'settings'];

  // syncChrome copies just the per-page bits of the persistent shell from
  // the freshly fetched document: the navigation highlight, the admin
  // group expansion and the logout form's CSRF field. The sidebar itself
  // is never rebuilt, so it does not flash on navigation.
  function syncChrome(doc) {
    const nav = (doc.body && doc.body.dataset && doc.body.dataset.nav) || '';
    document.body.dataset.nav = nav;
    document.querySelectorAll('.sidebar [data-nav]').forEach((link) => {
      const active = link.getAttribute('data-nav') === nav;
      link.classList.toggle('active', active);
      if (active) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    const group = document.getElementById('sidebar-admin-group');
    if (group) {
      const toggle = document.getElementById('sidebar-group-toggle');
      if (ADMIN_NAV_KEYS.includes(nav)) {
        group.classList.add('open');
        if (toggle) toggle.setAttribute('aria-expanded', 'true');
      } else {
        let stored = null;
        try { stored = localStorage.getItem('032_admin_group'); } catch (_) {}
        const open = stored === 'open';
        group.classList.toggle('open', open);
        if (toggle) toggle.setAttribute('aria-expanded', String(open));
      }
    }
    const incomingCsrfField = doc.querySelector('.sidebar input[name="csrfToken"]');
    if (incomingCsrfField) {
      document.querySelectorAll('.sidebar input[name="csrfToken"]').forEach((field) => { field.value = incomingCsrfField.value; });
    }
  }

  function applyPage(html, url, push, restoreState) {
    currentDocUrl = url;
    const doc = new DOMParser().parseFromString(html, 'text/html');
    if (!doc.body || doc.body.classList.contains('login-page')) return false;
    // A new server build ships assets under a different content hash; the
    // running JS can no longer trust the freshly fetched DOM, so fall back
    // to a full navigation instead of stitching incompatible versions.
    const currentBuild = document.querySelector('meta[name="app-build"]')?.getAttribute('content');
    const incomingBuild = doc.querySelector('meta[name="app-build"]')?.getAttribute('content');
    if (currentBuild && incomingBuild && currentBuild !== incomingBuild) return 'reload';
    // <head> survives PJAX swaps, so the session CSRF token must be copied
    // over manually before any later apiFetch call reads it.
    const incomingCsrf = doc.querySelector('meta[name="csrf-token"]')?.getAttribute('content');
    if (incomingCsrf) {
      const currentCsrf = document.querySelector('meta[name="csrf-token"]');
      if (currentCsrf) currentCsrf.setAttribute('content', incomingCsrf);
    }
    const newMain = doc.querySelector('main#app-main'), curMain = document.querySelector('main#app-main');
    if (!newMain || !curMain) return false;
    doc.querySelectorAll('[autofocus]').forEach((el) => el.removeAttribute('autofocus'));
    document.title = doc.title || document.title;
    // Keep the shell state classes the page cannot know about.
    const keepShellClasses = ['has-global-player', 'np-collapsed', 'np-open', 'np-animated'];
    const kept = keepShellClasses.filter((cls) => document.body.classList.contains(cls));
    document.body.className = doc.body.className;
    kept.forEach((cls) => document.body.classList.add(cls));
    const adoptedMain = document.adoptNode(newMain);
    curMain.replaceWith(adoptedMain);
    if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      adoptedMain.classList.add('pjax-enter');
      adoptedMain.addEventListener('animationend', () => adoptedMain.classList.remove('pjax-enter'), { once: true });
    }
    syncChrome(doc);
    doc.querySelectorAll('script[src]').forEach((script) => { const src = script.getAttribute('src'); if (!src || document.querySelector(`script[src="${src}"]`)) return; const el = document.createElement('script'); el.src = src; document.head.appendChild(el); });
    const destination = new URL(url, window.location.href);
    if (push && destination.href !== window.location.href) history.pushState({ app: '032', url: destination.href, previousURL: window.location.href, scrollX: 0, scrollY: 0, focus: '' }, '', destination.href);
    if (!push && restoreState && restoreState.app === '032' && destination.href !== window.location.href) history.replaceState({ app: '032', url: destination.href, scrollX: 0, scrollY: 0, focus: '' }, '', destination.href);
    const state = !push && restoreState && restoreState.app === '032' ? restoreState : null;
    if (state && state.url === window.location.href) {
      window.scrollTo(state.scrollX || 0, state.scrollY || 0);
      requestAnimationFrame(() => { const target = state.focus && document.querySelector(state.focus); if (target) target.focus({ preventScroll: true }); });
    } else if (destination.hash) requestAnimationFrame(() => document.getElementById(destination.hash.slice(1))?.scrollIntoView());
    else window.scrollTo(0, 0);
    decorateTrackRows(document);
    updateTrackRowsUI();
    markLoadedImages(adoptedMain);
    restoreSafeDraft();
    document.dispatchEvent(new CustomEvent('032:pjax-applied'));
    return true;
  }

  // ------------------------------------------------------------------ chrome
  function createPlayerDOM() {
    if (document.getElementById('global-player')) {
      audio = document.getElementById('global-audio-element');
      return;
    }

    const playerBar = document.createElement('div');
    playerBar.id = 'global-player';
    playerBar.className = 'global-player';
    playerBar.innerHTML = `
      <audio id="global-audio-element" preload="metadata"></audio>
      <div class="player-left">
        <div class="player-cover-wrap">
          <img id="player-cover" src="" alt="" style="display:none;">
          <div id="player-cover-placeholder" class="player-cover-placeholder">032</div>
        </div>
        <div class="player-meta">
          <div class="player-title-row">
            <strong id="player-title" title="未在播放">未在播放</strong>
            <span id="player-format" class="format-pill" style="display:none;">FLAC</span>
          </div>
          <small id="player-artist">选择一首歌曲开始播放</small>
        </div>
        <button type="button" id="player-btn-favorite" class="np-icon-btn" title="收藏" aria-label="收藏" disabled><span class="icon-slot" data-icon="icon-heart"></span></button>
        <button type="button" id="player-btn-more" class="np-icon-btn" title="更多" aria-label="更多" disabled><span class="icon-slot" data-icon="icon-dots"></span></button>
      </div>

      <div class="player-center">
        <div class="player-controls">
          <button id="player-btn-shuffle" class="player-ctrl-btn" title="随机播放" aria-label="随机播放" aria-pressed="false"><span class="icon-slot" data-icon="icon-shuffle"></span></button>
          <button id="player-btn-prev" class="player-ctrl-btn" title="上一首 (Alt+Left)" aria-label="上一首"><span class="icon-slot" data-icon="icon-prev"></span></button>
          <button id="player-btn-play" class="player-ctrl-btn play-main-btn" title="播放 / 暂停 (Space)" aria-label="播放 / 暂停"><span class="icon-slot" data-icon="icon-play"></span></button>
          <button id="player-btn-next" class="player-ctrl-btn" title="下一首 (Alt+Right)" aria-label="下一首"><span class="icon-slot" data-icon="icon-next"></span></button>
          <button id="player-btn-loop" class="player-ctrl-btn loop-btn" title="循环模式 (全部循环)" aria-label="循环模式" aria-pressed="false"><span class="icon-slot" data-icon="icon-repeat"></span></button>
        </div>
        <div class="player-progress-row">
          <span id="player-time-cur" class="player-time">0:00</span>
          <div id="player-progress-bar" class="player-progress-bar" role="slider" tabindex="0" aria-label="播放进度" aria-valuemin="0" aria-valuemax="0" aria-valuenow="0" aria-valuetext="0:00 / 0:00">
            <div id="player-progress-fill" class="player-progress-fill"></div>
            <div id="player-progress-thumb" class="player-progress-thumb"></div>
          </div>
          <span id="player-time-total" class="player-time">0:00</span>
        </div>
      </div>

      <div class="player-right">
        <div class="player-volume-wrap">
          <button id="player-btn-mute" class="player-tool-btn" title="静音" aria-label="静音"><span class="icon-slot" data-icon="icon-volume"></span></button>
          <input id="player-volume-slider" type="range" min="0" max="1" step="0.01" value="1" title="音量">
        </div>
        <button id="player-btn-lyrics" class="player-tool-btn" title="歌词 (L)" aria-label="歌词" aria-expanded="false" aria-controls="lyrics-overlay"><span class="icon-slot" data-icon="icon-lyrics"></span></button>
        <button id="player-btn-queue" class="player-tool-btn" title="播放队列" aria-label="播放队列" aria-expanded="false" aria-controls="now-playing"><span class="icon-slot" data-icon="icon-queue"></span></button>
        <button id="player-btn-fullscreen" class="player-tool-btn" title="全屏" aria-label="全屏" disabled><span class="icon-slot" data-icon="icon-fullscreen"></span></button>
        <button id="player-btn-shortcuts" class="player-tool-btn" title="快捷键说明" aria-label="快捷键说明" aria-expanded="false"><span class="icon-slot" data-icon="icon-dots"></span></button>
        <div id="player-shortcuts-help" class="player-shortcuts-help" hidden><span>Space 播放/暂停 · L 歌词 · Esc 关闭歌词</span><button type="button" id="player-shortcuts-disable">关闭快捷键</button></div>
      </div>
    `;
    hydrateIconSlots(playerBar);

    // Lyrics overlay
    const lyricsOverlay = document.createElement('div');
    lyricsOverlay.id = 'lyrics-overlay';
    lyricsOverlay.className = 'lyrics-overlay';
    lyricsOverlay.style.display = 'none';
    lyricsOverlay.innerHTML = `
      <div class="lyrics-header">
        <div class="lyrics-track-info">
          <strong id="lyrics-title"></strong>
          <small id="lyrics-artist"></small>
        </div>
        <button id="lyrics-close-btn" class="lyrics-close-btn" title="关闭歌词" aria-label="关闭歌词"><span class="icon-slot" data-icon="icon-close"></span></button>
      </div>
      <div id="lyrics-scroll-container" class="lyrics-content">
        <div class="lyrics-zero-state">暂无歌词</div>
      </div>
    `;
    hydrateIconSlots(lyricsOverlay);

    document.body.appendChild(playerBar);
    document.body.appendChild(lyricsOverlay);
    document.body.classList.add('has-global-player');

    audio = document.getElementById('global-audio-element');
    bindPlayerUIEvents();
  }

  function setupAudioElement() {
    if (!audio) audio = document.getElementById('global-audio-element');
    if (!audio) return;

    audio.addEventListener('play', () => {
      isPlaying = true;
      updatePlayButtonUI(true);
      updateTrackRowsUI();
      saveState();
    });

    audio.addEventListener('pause', () => {
      isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
    });

    audio.addEventListener('loadedmetadata', () => {
      const dur = audio.duration || 0;
      const totalElem = document.getElementById('player-time-total');
      if (totalElem && dur > 0) totalElem.textContent = formatTime(dur);
      syncPanelProgress();
    });

    audio.addEventListener('durationchange', () => {
      const dur = audio.duration || 0;
      const totalElem = document.getElementById('player-time-total');
      if (totalElem && dur > 0) totalElem.textContent = formatTime(dur);
    });

    audio.addEventListener('timeupdate', () => {
      onTimeUpdate();
    });

    audio.addEventListener('ended', () => {
      onTrackEnded();
    });

    audio.addEventListener('error', () => {
      const track = queue[currentIndex];
      const code = audio.error ? audio.error.code : 'UNKNOWN';
      const msg = audio.error ? audio.error.message : '';
      console.error('Audio playback error:', code, msg, audio.src);
      if (track) {
        showToast(`播放失败 (${code}): ${track.title || '未知曲目'}`);
      }
      isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
    });
  }

  function bindPlayerUIEvents() {
    const playBtn = document.getElementById('player-btn-play');
    const prevBtn = document.getElementById('player-btn-prev');
    const nextBtn = document.getElementById('player-btn-next');
    const loopBtn = document.getElementById('player-btn-loop');
    const shuffleBtn = document.getElementById('player-btn-shuffle');
    const progressBar = document.getElementById('player-progress-bar');
    const volumeSlider = document.getElementById('player-volume-slider');
    const muteBtn = document.getElementById('player-btn-mute');
    const lyricsBtn = document.getElementById('player-btn-lyrics');
    const lyricsCloseBtn = document.getElementById('lyrics-close-btn');
    const shortcutsBtn = document.getElementById('player-btn-shortcuts');
    const shortcutsDisableBtn = document.getElementById('player-shortcuts-disable');
    const queueBtn = document.getElementById('player-btn-queue');

    if (playBtn) playBtn.addEventListener('click', togglePlay);
    if (prevBtn) prevBtn.addEventListener('click', playPrevious);
    if (nextBtn) nextBtn.addEventListener('click', playNext);
    if (loopBtn) loopBtn.addEventListener('click', cycleLoopMode);
    if (shuffleBtn) shuffleBtn.addEventListener('click', toggleShuffle);
    if (queueBtn) queueBtn.addEventListener('click', toggleNowPlayingPanel);

    if (progressBar) {
      bindSeekBar(progressBar, (fill, thumb) => {
        const fillElem = document.getElementById('player-progress-fill');
        const thumbElem = document.getElementById('player-progress-thumb');
        if (fillElem && fill) fillElem.style.width = fill;
        if (thumbElem && thumb) thumbElem.style.left = thumb;
      });
    }

    if (volumeSlider) {
      volumeSlider.addEventListener('input', (e) => {
        const val = parseFloat(e.target.value);
        if (audio) {
          audio.volume = val;
          audio.muted = false;
        }
        updateVolumeUI(val);
        saveState();
      });
    }

    if (muteBtn) {
      muteBtn.addEventListener('click', () => {
        if (!audio) return;
        if (audio.muted) {
          audio.muted = false;
          if (audio.volume === 0) {
            audio.volume = 0.8;
            if (volumeSlider) volumeSlider.value = '0.8';
          }
        } else {
          audio.muted = true;
        }
        updateVolumeUI(audio.muted ? 0 : audio.volume);
        saveState();
      });
    }

    if (lyricsBtn) {
      lyricsBtn.addEventListener('click', () => {
        const overlay = document.getElementById('lyrics-overlay');
        if (!overlay) return;
        const isVisible = overlay.style.display !== 'none';
        overlay.style.display = isVisible ? 'none' : 'flex';
        lyricsBtn.classList.toggle('active', !isVisible);
        lyricsBtn.setAttribute('aria-expanded', String(!isVisible));
        if (!isVisible) { scrollLyricsToActive(true); lyricsCloseBtn?.focus(); } else lyricsBtn.focus();
      });
    }

    if (lyricsCloseBtn) {
      lyricsCloseBtn.addEventListener('click', () => {
        const overlay = document.getElementById('lyrics-overlay');
        const lyricsBtn = document.getElementById('player-btn-lyrics');
        if (overlay) overlay.style.display = 'none';
        if (lyricsBtn) { lyricsBtn.classList.remove('active'); lyricsBtn.setAttribute('aria-expanded', 'false'); lyricsBtn.focus(); }
      });
    }
    if (shortcutsBtn) shortcutsBtn.addEventListener('click', () => {
      const help = document.getElementById('player-shortcuts-help'); if (!help) return;
      help.hidden = !help.hidden; shortcutsBtn.setAttribute('aria-expanded', String(!help.hidden));
    });
    const syncShortcutsToggleLabel = () => {
      if (!shortcutsDisableBtn) return;
      shortcutsDisableBtn.textContent = localStorage.getItem('032_shortcuts_disabled') === '1' ? '开启快捷键' : '关闭快捷键';
    };
    syncShortcutsToggleLabel();
    if (shortcutsDisableBtn) shortcutsDisableBtn.addEventListener('click', () => {
      const disabled = localStorage.getItem('032_shortcuts_disabled') === '1';
      if (disabled) localStorage.removeItem('032_shortcuts_disabled');
      else localStorage.setItem('032_shortcuts_disabled', '1');
      syncShortcutsToggleLabel();
      showToast(disabled ? '播放器快捷键已开启' : '播放器快捷键已关闭');
    });
  }

  // bindSeekBar wires click/drag/touch/keyboard seeking on a progress bar
  // element. applyPreview receives the width/left percentages while
  // dragging so the bar can preview the target position.
  function bindSeekBar(bar, applyPreview) {
    const seekToPosition = (clientX) => {
      if (!audio || !audio.duration) return;
      const rect = bar.getBoundingClientRect();
      const ratio = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
      const targetTime = ratio * audio.duration;
      audio.currentTime = targetTime;

      const curElem = document.getElementById('player-time-cur');
      if (curElem) curElem.textContent = formatTime(targetTime);
      applyPreview(`${ratio * 100}%`, `${ratio * 100}%`);
    };

    bar.addEventListener('mousedown', (e) => {
      if (!audio || !audio.duration) return;
      isDraggingProgress = true;
      seekToPosition(e.clientX);

      const onMouseMove = (moveEvent) => {
        if (isDraggingProgress) {
          seekToPosition(moveEvent.clientX);
        }
      };

      const onMouseUp = () => {
        isDraggingProgress = false;
        window.removeEventListener('mousemove', onMouseMove);
        window.removeEventListener('mouseup', onMouseUp);
      };

      window.addEventListener('mousemove', onMouseMove);
      window.addEventListener('mouseup', onMouseUp);
    });

    // Touch support for mobile/tablets
    bar.addEventListener('touchstart', (e) => {
      if (!audio || !audio.duration || !e.touches[0]) return;
      isDraggingProgress = true;
      seekToPosition(e.touches[0].clientX);

      const onTouchMove = (moveEvent) => {
        if (isDraggingProgress && moveEvent.touches[0]) {
          seekToPosition(moveEvent.touches[0].clientX);
        }
      };

      const onTouchEnd = () => {
        isDraggingProgress = false;
        window.removeEventListener('touchmove', onTouchMove);
        window.removeEventListener('touchend', onTouchEnd);
      };

      window.addEventListener('touchmove', onTouchMove);
      window.addEventListener('touchend', onTouchEnd);
    }, { passive: true });
    bar.addEventListener('keydown', (e) => {
      if (!audio || !audio.duration) return;
      let next = audio.currentTime || 0;
      if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') next -= 5;
      else if (e.key === 'ArrowRight' || e.key === 'ArrowUp') next += 5;
      else if (e.key === 'Home') next = 0;
      else if (e.key === 'End') next = audio.duration;
      else return;
      e.preventDefault(); audio.currentTime = Math.max(0, Math.min(audio.duration, next)); onTimeUpdate();
    });
  }

  // ------------------------------------------------------------ track lists
  // Track-row interactions are delegated at the document level so PJAX
  // swaps never need re-binding; decorateTrackRows only fills in the
  // hover-play markup and accessible labels for freshly inserted content.
  function bindTrackListEvents() {
    document.addEventListener('click', (e) => {
      if (!(e.target instanceof Element)) return;
      const playBtn = e.target.closest('.row-play-btn');
      if (playBtn) {
        const row = playBtn.closest('[data-track-id]');
        if (row) {
          e.preventDefault();
          e.stopPropagation();
          playRowTrack(row);
        }
        return;
      }
      const queueBtn = e.target.closest('.queue-next-btn, .queue-append-btn');
      if (queueBtn) {
        const row = queueBtn.closest('[data-track-id]');
        if (!row) return;
        e.preventDefault();
        e.stopPropagation();
        queueTrackFromPage(row.getAttribute('data-track-id'), queueBtn.classList.contains('queue-next-btn') ? 'next' : 'append');
        const menu = queueBtn.closest('details');
        if (menu) menu.removeAttribute('open');
        return;
      }
      const albumAction = e.target.closest('.album-queue-action');
      if (albumAction) {
        e.preventDefault();
        const card = albumAction.closest('[data-album-id]');
        if (card) queueAlbumFromPage(card.dataset.albumId, albumAction.dataset.mode);
        const menu = albumAction.closest('details');
        if (menu) { menu.removeAttribute('open'); menu.querySelector('summary')?.focus(); }
        return;
      }
      const albumPlayBtn = e.target.closest('.album-hero .primary-round');
      if (albumPlayBtn) {
        e.preventDefault();
        const pageTracks = extractAllTracksFromPage();
        if (pageTracks.length > 0) {
          queue = pageTracks;
          playHistory = [];
          playTrackAtIndex(0);
        }
      }
    });

    document.addEventListener('dblclick', (e) => {
      if (!(e.target instanceof Element)) return;
      const row = e.target.closest('[data-track-id]');
      if (!row) return;
      if (e.target.closest('a, form, details, input, select, textarea, .row-play-btn')) return;
      playRowTrack(row);
    });

    decorateTrackRows(document);
    updateTrackRowsUI();
  }

  function playRowTrack(row) {
    const trackId = row.getAttribute('data-track-id');
    if (!trackId) return;
    const pageTracks = extractAllTracksFromPage();
    const targetIndex = pageTracks.findIndex(t => String(t.id) === String(trackId));
    if (targetIndex !== -1) {
      if (currentIndex !== -1 && queue[currentIndex] && String(queue[currentIndex].id) === String(trackId)) {
        togglePlay();
      } else {
        queue = pageTracks;
        playHistory = []; // queue replaced: position history is meaningless
        playTrackAtIndex(targetIndex);
      }
    }
  }

  function decorateTrackRows(root) {
    root.querySelectorAll('[data-track-id]').forEach((el) => {
      let playBtn = el.querySelector('.row-play-btn');
      const trackId = el.getAttribute('data-track-id');
      if (!trackId) return;

      if (!playBtn) {
        const numElem = el.querySelector('.track-number, span:first-child, .drag-position, .tieup-role');
        if (numElem && !numElem.querySelector('.row-play-btn')) {
          // Build nodes instead of innerHTML: the original text comes from
          // file tags and must never be re-parsed as HTML.
          const originalText = numElem.textContent.trim();
          numElem.classList.add('playable-number');
          numElem.textContent = '';
          const numText = document.createElement('span');
          numText.className = 'num-text';
          numText.textContent = originalText;
          const rowPlayButton = document.createElement('button');
          rowPlayButton.type = 'button';
          rowPlayButton.className = 'row-play-btn';
          rowPlayButton.title = '播放';
          rowPlayButton.appendChild(svgIcon('icon-play'));
          numElem.appendChild(numText);
          numElem.appendChild(rowPlayButton);
          playBtn = rowPlayButton;
        }
      }

      if (playBtn && !playBtn.getAttribute('aria-label')) {
        const accessibleTitle = el.getAttribute('data-track-title') || el.querySelector('strong')?.textContent.trim() || '歌曲';
        playBtn.setAttribute('aria-label', `播放 ${accessibleTitle}`);
      }
    });
  }

  function extractAllTracksFromPage() {
    const tracks = [];
    const elements = document.querySelectorAll('[data-track-id]');

    elements.forEach(el => {
      const id = el.getAttribute('data-track-id');
      if (!id) return;

      const title = el.getAttribute('data-track-title') || el.querySelector('strong')?.textContent.trim() || '未知歌曲';
      const artist = el.getAttribute('data-track-artist') || el.querySelector('.album-artist-links, small')?.textContent.trim() || '未知歌手';
      const album = el.getAttribute('data-track-album') || el.querySelector('a[href*="/admin/albums/"]')?.textContent.trim() || '';
      const artwork = el.getAttribute('data-track-artwork') || el.querySelector('img')?.src || '';
      const container = el.getAttribute('data-track-container') || el.querySelector('.format-pill')?.textContent.trim() || 'FLAC';
      const durationMs = parseInt(el.getAttribute('data-track-duration-ms') || '0', 10) || 0;

      tracks.push({
        id: id,
        title: title,
        artist: artist,
        album: album,
        artwork: artwork,
        container: container,
        durationMs: durationMs,
        streamUrl: `/api/v1/tracks/${id}/stream`
      });
    });

    return tracks;
  }

  // queueTrackFromPage inserts a track from the current page into the
  // playback queue without replacing it: mode 'next' puts it right after the
  // current track, 'append' puts it at the end. When nothing is queued yet,
  // the track starts playing immediately so the button never feels dead.
  function queueTrackFromPage(trackId, mode) {
    const track = extractAllTracksFromPage().find(t => String(t.id) === String(trackId));
    if (!track) return;
    if (queue.length === 0 || currentIndex === -1) {
      queue.push(track);
      playTrackAtIndex(queue.length - 1);
      showToast(`开始播放：${track.title}`);
      return;
    }
    if (mode === 'next') queue.splice(currentIndex + 1, 0, track);
    else queue.push(track);
    saveState();
    emitPlayerState();
    showToast(mode === 'next' ? `已加入下一首播放：${track.title}` : `已添加到队列末尾：${track.title}`);
  }

  const loadingAlbums = new Set();
  async function queueAlbumFromPage(albumId, mode) {
    if (loadingAlbums.has(albumId)) return;
    loadingAlbums.add(albumId);
    try {
      const response = await fetch(`/api/v1/albums/${encodeURIComponent(albumId)}`, { credentials: 'same-origin' });
      if (response.status === 401) { window.location.assign('/admin/login'); return; }
      if (!response.ok) throw new Error('专辑加载失败');
      const payload = await response.json();
      const tracks = (payload.tracks || []).map(track => ({
        id: String(track.id), title: track.title, artist: track.artist,
        album: track.album || payload.album?.title || '',
        artwork: artworkForSize(track.artworkUrl || payload.album?.artworkUrl || '', 256),
        container: track.container, durationMs: track.durationMillis,
        streamUrl: `/api/v1/tracks/${track.id}/stream`
      }));
      if (!tracks.length) { showToast('这张专辑没有可播放的歌曲'); return; }
      if (mode === 'play') {
        queue = tracks;
        playHistory = [];
        playTrackAtIndex(0);
      } else if (!queue.length || currentIndex < 0) {
        queue = tracks;
        playHistory = [];
        playTrackAtIndex(0);
        showToast(`开始播放：${payload.album?.title || '专辑'}`);
      } else {
        if (mode === 'next') queue.splice(currentIndex + 1, 0, ...tracks);
        else queue.push(...tracks);
        saveState();
        emitPlayerState();
        showToast(mode === 'next' ? '已加入下一首播放' : '已添加到队列末尾');
      }
    } catch (error) { showToast('专辑加载失败，请重试'); }
    finally { loadingAlbums.delete(albumId); }
  }

  function playTrackAtIndex(index, skipHistoryPush) {
    if (index < 0 || index >= queue.length) return;
    // Shuffle mode keeps a back-stack so 上一首 retraces what was heard
    // instead of walking the queue backwards.
    if (!skipHistoryPush && currentIndex !== -1 && index !== currentIndex && queue[currentIndex]) {
      playHistory.push(String(queue[currentIndex].id));
      if (playHistory.length > 100) playHistory.shift();
    }
    currentIndex = index;
    const track = queue[currentIndex];
    if (!track) return;

    updatePlayerMetaUI(track);
    loadLyrics(track.id);
    updateMediaSession(track);

    if (!audio) audio = document.getElementById('global-audio-element');
    if (!audio) return;

    const isSameSource = audio.src.endsWith(track.streamUrl) || audio.src === window.location.origin + track.streamUrl;
    if (!isSameSource) {
      audio.src = track.streamUrl;
      audio.load();
    }

    const playPromise = audio.play();
    if (playPromise !== undefined) {
      playPromise.then(() => {
        isPlaying = true;
        updatePlayButtonUI(true);
        updateTrackRowsUI();
        saveState();
      }).catch(err => {
        console.warn('Audio play request failed:', err);
        isPlaying = false;
        updatePlayButtonUI(false);
        updateTrackRowsUI();
      });
    }
  }

  function togglePlay() {
    if (!audio) return;
    if (!audio.src && queue.length > 0) {
      playTrackAtIndex(currentIndex >= 0 ? currentIndex : 0);
      return;
    }

    if (audio.paused) {
      const playPromise = audio.play();
      if (playPromise !== undefined) {
        playPromise.then(() => {
          isPlaying = true;
          updatePlayButtonUI(true);
          updateTrackRowsUI();
          saveState();
        }).catch(err => {
          console.warn('Audio play failed on toggle:', err);
        });
      }
    } else {
      audio.pause();
      isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
    }
  }

  function playPrevious() {
    if (!audio || queue.length === 0) return;
    if (audio.currentTime > 3) {
      audio.currentTime = 0;
      return;
    }
    if (shuffleOn && playHistory.length > 0) {
      // Pop ids until one still exists in the current queue; entries left
      // over from a replaced queue are skipped.
      while (playHistory.length > 0) {
        const id = playHistory.pop();
        const index = queue.findIndex((t) => String(t.id) === id);
        if (index !== -1 && index !== currentIndex) {
          playTrackAtIndex(index, true);
          return;
        }
      }
    }
    let prevIndex = currentIndex - 1;
    if (prevIndex < 0) prevIndex = queue.length - 1;
    playTrackAtIndex(prevIndex);
  }

  function playNext() {
    if (!audio || queue.length === 0) return;
    if (shuffleOn) { playRandomNext(); return; }
    let nextIndex = currentIndex + 1;
    if (nextIndex >= queue.length) {
      if (loopMode === 'all') nextIndex = 0;
      else return;
    }
    playTrackAtIndex(nextIndex);
  }

  function playRandomNext() {
    let nextIndex = currentIndex;
    if (queue.length > 1) {
      while (nextIndex === currentIndex) {
        nextIndex = Math.floor(Math.random() * queue.length);
      }
    } else {
      nextIndex = 0;
    }
    playTrackAtIndex(nextIndex);
  }

  function onTrackEnded() {
    const currentTrack = queue[currentIndex];
    if (currentTrack) scrobbleTrack(currentTrack.id);

    if (loopMode === 'one') {
      audio.currentTime = 0;
      audio.play().catch(console.warn);
    } else if (shuffleOn && queue.length > 1) {
      playRandomNext();
    } else if (queue.length <= 1) {
      // A one-track queue never infinite-loops on its own: loop mode 'all'
      // replays it, otherwise playback stops at the end.
      if (loopMode === 'all' && queue.length === 1) {
        audio.currentTime = 0;
        audio.play().catch(console.warn);
      } else {
        isPlaying = false;
        updatePlayButtonUI(false);
        updateTrackRowsUI();
        saveState();
      }
    } else {
      playNext();
    }
  }

  function cycleLoopMode() {
    const loopBtn = document.getElementById('player-btn-loop');
    if (loopMode === 'all') {
      loopMode = 'one';
      if (loopBtn) {
        swapIcon(loopBtn, 'icon-repeat-1');
        loopBtn.title = '循环模式 (单曲循环)';
        loopBtn.classList.add('active');
        loopBtn.setAttribute('aria-pressed', 'true');
      }
    } else {
      loopMode = 'all';
      if (loopBtn) {
        swapIcon(loopBtn, 'icon-repeat');
        loopBtn.title = '循环模式 (全部循环)';
        loopBtn.classList.remove('active');
        loopBtn.setAttribute('aria-pressed', 'false');
      }
    }
    saveState();
    emitPlayerState();
  }

  function toggleShuffle() {
    shuffleOn = !shuffleOn;
    const shuffleBtn = document.getElementById('player-btn-shuffle');
    if (shuffleBtn) {
      shuffleBtn.classList.toggle('active', shuffleOn);
      shuffleBtn.setAttribute('aria-pressed', String(shuffleOn));
      shuffleBtn.title = shuffleOn ? '随机播放 (开)' : '随机播放';
    }
    saveState();
    emitPlayerState();
  }

  function onTimeUpdate() {
    if (!audio) return;
    const curTime = audio.currentTime || 0;
    const durTime = audio.duration || 0;

    const curElem = document.getElementById('player-time-cur');
    const totalElem = document.getElementById('player-time-total');
    const fillElem = document.getElementById('player-progress-fill');
    const thumbElem = document.getElementById('player-progress-thumb');
    const progressElem = document.getElementById('player-progress-bar');

    if (curElem) curElem.textContent = formatTime(curTime);
    if (totalElem && durTime > 0) totalElem.textContent = formatTime(durTime);
    if (progressElem) {
      progressElem.setAttribute('aria-valuemax', String(Math.floor(durTime)));
      progressElem.setAttribute('aria-valuenow', String(Math.floor(curTime)));
      progressElem.setAttribute('aria-valuetext', `${formatTime(curTime)} / ${formatTime(durTime)}`);
    }

    if (!isDraggingProgress && durTime > 0) {
      const percent = Math.min(100, Math.max(0, (curTime / durTime) * 100));
      if (fillElem) fillElem.style.width = `${percent}%`;
      if (thumbElem) thumbElem.style.left = `${percent}%`;
    }

    syncPanelProgress();
    updateActiveLyric(curTime * 1000);

    const currentTrack = queue[currentIndex];
    if (currentTrack && durTime > 10 && curTime / durTime >= 0.5 && lastScrobbledTrackId !== currentTrack.id) {
      scrobbleTrack(currentTrack.id);
    }
  }

  function updatePlayButtonUI(playing) {
    const playBtn = document.getElementById('player-btn-play');
    if (playBtn) {
      swapIcon(playBtn, playing ? 'icon-pause' : 'icon-play');
      playBtn.classList.toggle('is-playing', playing);
    }
    const currentRowBtn = document.querySelector('.np-queue-list li.current');
    if (currentRowBtn) currentRowBtn.classList.toggle('paused', !playing);
    emitPlayerState();
  }

  function updatePlayerMetaUI(track) {
    const titleEl = document.getElementById('player-title');
    const artistEl = document.getElementById('player-artist');
    const coverImg = document.getElementById('player-cover');
    const coverPlaceholder = document.getElementById('player-cover-placeholder');
    const formatPill = document.getElementById('player-format');
    const lyricsTitle = document.getElementById('lyrics-title');
    const lyricsArtist = document.getElementById('lyrics-artist');

    if (titleEl) {
      titleEl.textContent = track.title || '未知曲目';
      titleEl.title = track.title || '';
    }
    if (artistEl) {
      artistEl.textContent = track.artist ? `${track.artist} · ${track.album || ''}` : '';
    }
    if (formatPill) {
      formatPill.textContent = (track.container || 'FLAC').toUpperCase();
      formatPill.style.display = 'inline-block';
    }

    if (coverImg && coverPlaceholder) {
      if (track.artwork) {
        coverImg.src = track.artwork;
        coverImg.style.display = 'block';
        coverPlaceholder.style.display = 'none';
      } else {
        coverImg.style.display = 'none';
        coverPlaceholder.style.display = 'grid';
      }
    }

    if (lyricsTitle) lyricsTitle.textContent = track.title || '';
    if (lyricsArtist) lyricsArtist.textContent = track.artist || '';

    emitPlayerState();
  }

  function updateTrackRowsUI() {
    const currentTrack = queue[currentIndex];
    const currentId = currentTrack ? String(currentTrack.id) : null;

    document.querySelectorAll('[data-track-id]').forEach(el => {
      const trackId = el.getAttribute('data-track-id');
      const isCurrent = trackId === currentId;
      el.classList.toggle('now-playing-row', isCurrent);

      const btn = el.querySelector('.row-play-btn');
      if (btn) {
        swapIcon(btn, (isCurrent && isPlaying) ? 'icon-pause' : 'icon-play');
        btn.classList.toggle('playing', isCurrent && isPlaying);
      }
    });
    syncPanelQueueCurrent();
  }

  function updateVolumeUI(val) {
    const muteBtn = document.getElementById('player-btn-mute');
    if (muteBtn) swapIcon(muteBtn, val === 0 ? 'icon-mute' : 'icon-volume');
  }

  function artworkForSize(artwork, size) {
    if (!artwork) return '';
    if (/([?&])size=\d+/.test(artwork)) return artwork.replace(/([?&])size=\d+/, `$1size=${size}`);
    return artwork + (artwork.includes('?') ? '&' : '?') + 'size=' + size;
  }

  function updateMediaSession(track) {
    if (!('mediaSession' in navigator)) return;
    try {
      navigator.mediaSession.metadata = new MediaMetadata({
        title: track.title || '未知歌曲',
        artist: track.artist || '',
        album: track.album || '',
        // data-track-artwork carries a 256px thumbnail; the lock-screen
        // artwork is requested at 512px so it stays sharp on dense screens.
        artwork: track.artwork ? [{ src: artworkForSize(track.artwork, 512), sizes: '512x512', type: 'image/jpeg' }] : []
      });

      navigator.mediaSession.setActionHandler('play', togglePlay);
      navigator.mediaSession.setActionHandler('pause', togglePlay);
      navigator.mediaSession.setActionHandler('previoustrack', playPrevious);
      navigator.mediaSession.setActionHandler('nexttrack', playNext);
      navigator.mediaSession.setActionHandler('seekto', (details) => {
        if (details.seekTime !== undefined && audio && audio.duration) {
          audio.currentTime = details.seekTime;
        }
      });
    } catch (_) {}
  }

  // -------------------------------------------------------- now-playing panel
  const PANEL_KEY = '032_np_collapsed';

  function panelCollapsedDefault() {
    return window.innerWidth < 1440;
  }

  function isPanelCollapsed() {
    let stored = null;
    try { stored = localStorage.getItem(PANEL_KEY); } catch (_) {}
    if (stored === null) return panelCollapsedDefault();
    return stored === '1';
  }

  function isPanelOpen() {
    if (window.innerWidth <= 900) return document.body.classList.contains('np-open');
    return !document.body.classList.contains('np-collapsed');
  }

  // [M2] syncPanelA11y keeps focusability in line with visibility: a hidden
  // panel must never receive Tab focus.
  function syncPanelA11y() {
    const panel = document.getElementById('now-playing');
    if (!panel) return;
    const open = isPanelOpen();
    panel.inert = !open;
    panel.setAttribute('aria-hidden', String(!open));
  }

  function applyPanelState() {
    if (window.innerWidth <= 900) {
      document.body.classList.remove('np-collapsed');
    } else {
      document.body.classList.toggle('np-collapsed', isPanelCollapsed());
      document.body.classList.remove('np-open');
    }
    const queueBtn = document.getElementById('player-btn-queue');
    if (queueBtn) queueBtn.setAttribute('aria-expanded', String(isPanelOpen()));
    syncPanelA11y();
  }

  // [M5] persist=false on the mobile drawer so a phone never overwrites the
  // desktop collapse preference.
  function setPanelCollapsed(collapsed, persist) {
    if (persist && window.innerWidth > 900) {
      try { localStorage.setItem(PANEL_KEY, collapsed ? '1' : '0'); } catch (_) {}
    }
    if (window.innerWidth <= 900) {
      document.body.classList.toggle('np-open', !collapsed);
      document.body.classList.remove('np-collapsed');
    } else {
      document.body.classList.toggle('np-collapsed', collapsed);
      document.body.classList.remove('np-open');
    }
    const queueBtn = document.getElementById('player-btn-queue');
    if (queueBtn) queueBtn.setAttribute('aria-expanded', String(!collapsed));
    syncPanelA11y();
  }

  function toggleNowPlayingPanel() {
    if (window.innerWidth <= 900) {
      const open = !document.body.classList.contains('np-open');
      document.body.classList.toggle('np-open', open);
      const queueBtn = document.getElementById('player-btn-queue');
      if (queueBtn) queueBtn.setAttribute('aria-expanded', String(open));
      syncPanelA11y();
      if (open) document.getElementById('np-collapse')?.focus();
      return;
    }
    setPanelCollapsed(!document.body.classList.contains('np-collapsed'), true);
  }

  function setupNowPlayingPanel() {
    applyPanelState();

    const collapseBtn = document.getElementById('np-collapse');
    if (collapseBtn) collapseBtn.addEventListener('click', () => {
      const mobile = window.innerWidth <= 900;
      setPanelCollapsed(true, !mobile);
      if (mobile) document.getElementById('player-btn-queue')?.focus();
    });
    const expandBtn = document.getElementById('np-expand');
    if (expandBtn) expandBtn.addEventListener('click', () => setPanelCollapsed(false, true));

    const panelBar = document.getElementById('np-progress-bar');
    if (panelBar) {
      bindSeekBar(panelBar, (fill) => {
        const fillElem = document.getElementById('np-progress-fill');
        if (fillElem && fill) fillElem.style.width = fill;
      });
    }

    const list = document.getElementById('np-queue-list');
    if (list) {
      list.addEventListener('click', (e) => {
        const item = e.target instanceof Element ? e.target.closest('li[data-qindex]') : null;
        if (!item) return;
        const index = parseInt(item.getAttribute('data-qindex'), 10);
        if (Number.isNaN(index)) return;
        if (index === currentIndex) togglePlay();
        else playTrackAtIndex(index);
      });
    }

    window.addEventListener('resize', () => {
      // Only reconcile the shell classes; the user's explicit choice (or
      // the width-derived default) is re-evaluated, not overridden live.
      if (window.innerWidth <= 900) {
        document.body.classList.remove('np-collapsed');
      } else {
        document.body.classList.remove('np-open');
      }
      applyPanelState();
    });

    // [M4] the panel renders from the debounced state event; direct renders
    // only happen at setup so the first paint is correct.
    document.addEventListener('032:player-state', () => syncNowPlayingPanel());
    syncNowPlayingPanel();
  }

  // [M4] emitPlayerState broadcasts a snapshot for any listener, coalesced
  // to one dispatch per animation frame.
  let emitScheduled = false;
  function emitPlayerState() {
    if (emitScheduled) return;
    emitScheduled = true;
    requestAnimationFrame(() => {
      emitScheduled = false;
      const track = queue[currentIndex] || null;
      document.dispatchEvent(new CustomEvent('032:player-state', {
        detail: {
          track,
          queueLength: queue.length,
          currentIndex,
          isPlaying,
          loopMode,
          shuffle: shuffleOn
        }
      }));
    });
  }

  function syncNowPlayingPanel() {
    const panel = document.getElementById('now-playing');
    if (!panel) return;
    const track = queue[currentIndex] || null;

    const titleEl = document.getElementById('np-title');
    const artistEl = document.getElementById('np-artist');
    const albumEl = document.getElementById('np-album');
    const cover = document.getElementById('np-cover');
    const countEl = document.getElementById('np-queue-count');

    if (titleEl) titleEl.textContent = track ? (track.title || '未知曲目') : '未在播放';
    if (artistEl) artistEl.textContent = track ? (track.artist || '—') : '—';
    if (albumEl) albumEl.textContent = track && track.album ? track.album : '';
    if (countEl) countEl.textContent = String(queue.length);

    if (cover) {
      // [M4] Rebuild the cover only when the artwork actually changes so
      // queue-only updates do not flicker the image.
      const src = track && track.artwork ? track.artwork : '';
      if (cover.dataset.src !== src) {
        cover.dataset.src = src;
        cover.textContent = '';
        if (src) {
          const img = document.createElement('img');
          img.src = src;
          img.alt = '';
          img.decoding = 'async';
          cover.appendChild(img);
        } else {
          const empty = document.createElement('div');
          empty.className = 'np-cover-empty';
          empty.textContent = '032';
          cover.appendChild(empty);
        }
      }
    }

    renderQueueList();
    syncPanelProgress();
  }

  // [P2-1] The list is rebuilt only when the queue contents actually
  // change; play/pause state flips just re-mark the current row so keyboard
  // focus is never destroyed.
  let lastQueueSignature = null;
  function queueSignature() {
    return queue.length + ':' + queue.map((t) => t.id).join(',');
  }

  function renderQueueList() {
    const list = document.getElementById('np-queue-list');
    if (!list) return;
    const signature = queueSignature();
    if (signature === lastQueueSignature && list.childElementCount > 0) {
      syncPanelQueueCurrent();
      return;
    }
    // If the rebuild would destroy a focused queue row, remember which one
    // and restore focus to it afterwards.
    let refocusIndex = null;
    const active = document.activeElement;
    if (active && list.contains(active)) {
      const item = active.closest('li[data-qindex]');
      if (item) refocusIndex = parseInt(item.getAttribute('data-qindex'), 10);
    }
    lastQueueSignature = signature;
    list.textContent = '';
    if (queue.length === 0) {
      const empty = document.createElement('li');
      empty.className = 'np-queue-empty';
      empty.textContent = '队列为空，从歌曲列表开始播放。';
      list.appendChild(empty);
      return;
    }
    queue.forEach((track, index) => {
      const item = document.createElement('li');
      item.setAttribute('data-qindex', String(index));
      if (index === currentIndex) {
        item.classList.add('current');
        if (!isPlaying) item.classList.add('paused');
      }

      // [M3] the whole row is a real button so queue entries are keyboard
      // reachable and announced by screen readers.
      const rowBtn = document.createElement('button');
      rowBtn.type = 'button';
      rowBtn.className = 'q-row';
      rowBtn.setAttribute('aria-label', `播放 ${track.title || '未知曲目'}`);

      const indexCell = document.createElement('span');
      indexCell.className = 'q-index';
      const num = document.createElement('span');
      num.className = 'q-num';
      num.textContent = String(index + 1);
      indexCell.appendChild(num);
      if (index === currentIndex) {
        const bars = document.createElement('span');
        bars.className = 'eq-bars';
        bars.setAttribute('aria-hidden', 'true');
        for (let i = 0; i < 3; i++) bars.appendChild(document.createElement('i'));
        indexCell.appendChild(bars);
      }
      rowBtn.appendChild(indexCell);

      const meta = document.createElement('span');
      meta.className = 'q-meta';
      const title = document.createElement('span');
      title.className = 'q-title';
      title.textContent = track.title || '未知曲目';
      const artist = document.createElement('span');
      artist.className = 'q-artist';
      artist.textContent = track.artist || '';
      meta.appendChild(title);
      meta.appendChild(artist);
      rowBtn.appendChild(meta);

      const duration = document.createElement('span');
      duration.className = 'q-duration';
      duration.textContent = track.durationMs > 0 ? formatTime(track.durationMs / 1000) : '';
      rowBtn.appendChild(duration);

      item.appendChild(rowBtn);
      list.appendChild(item);
    });
    if (refocusIndex !== null) {
      const target = list.querySelector(`li[data-qindex="${refocusIndex}"] .q-row`)
        || list.querySelector('.q-row');
      if (target) target.focus({ preventScroll: true });
    }
  }

  // syncPanelQueueCurrent refreshes only the current-row markers (cheap
  // enough to run on every play/pause toggle).
  function syncPanelQueueCurrent() {
    const list = document.getElementById('np-queue-list');
    if (!list) return;
    list.querySelectorAll('li[data-qindex]').forEach((item) => {
      const index = parseInt(item.getAttribute('data-qindex'), 10);
      const isCurrent = index === currentIndex;
      item.classList.toggle('current', isCurrent);
      item.classList.toggle('paused', isCurrent && !isPlaying);
    });
  }

  function syncPanelProgress() {
    if (!audio) return;
    const curTime = audio.currentTime || 0;
    const durTime = audio.duration || 0;
    const curElem = document.getElementById('np-time-cur');
    const totalElem = document.getElementById('np-time-total');
    const fillElem = document.getElementById('np-progress-fill');
    const bar = document.getElementById('np-progress-bar');
    if (curElem) curElem.textContent = formatTime(curTime);
    if (totalElem) totalElem.textContent = durTime > 0 ? formatTime(durTime) : '0:00';
    if (bar) {
      bar.setAttribute('aria-valuemax', String(Math.floor(durTime)));
      bar.setAttribute('aria-valuenow', String(Math.floor(curTime)));
    }
    if (fillElem && !isDraggingProgress && durTime > 0) {
      fillElem.style.width = `${Math.min(100, Math.max(0, (curTime / durTime) * 100))}%`;
    }
  }

  // ------------------------------------------------------------------ lyrics
  async function loadLyrics(trackId) {
    lyrics = [];
    activeLyricIndex = -1;
    const container = document.getElementById('lyrics-scroll-container');
    if (container) {
      container.innerHTML = '<div class="lyrics-loading">歌词加载中…</div>';
    }

    try {
      const res = await fetch(`/api/v1/tracks/${trackId}/lyrics`, { credentials: 'same-origin' });
      if (!res.ok) {
        if (container) container.innerHTML = '<div class="lyrics-zero-state">暂无歌词</div>';
        return;
      }
      const data = await res.json();
      if (data && data.lines && data.lines.length > 0) {
        lyrics = data.lines;
        renderLyrics();
      } else {
        if (container) container.innerHTML = '<div class="lyrics-zero-state">暂无歌词</div>';
      }
    } catch (e) {
      if (container) container.innerHTML = '<div class="lyrics-zero-state">歌词加载失败</div>';
    }
  }

  function renderLyrics() {
    const container = document.getElementById('lyrics-scroll-container');
    if (!container) return;
    container.innerHTML = '';

    lyrics.forEach((line, index) => {
      const p = document.createElement('p');
      p.className = 'lyric-line';
      p.setAttribute('data-index', index);
      p.setAttribute('data-time', line.timeMs || 0);
      p.textContent = line.text;
      p.addEventListener('click', () => {
        if (audio && line.timeMs !== undefined) {
          audio.currentTime = line.timeMs / 1000;
          if (audio.paused) audio.play().catch(console.warn);
        }
      });
      container.appendChild(p);
    });

    scrollLyricsToActive(true);
  }

  function updateActiveLyric(timeMs) {
    if (!lyrics || lyrics.length === 0) return;

    let targetIndex = -1;
    for (let i = 0; i < lyrics.length; i++) {
      if (lyrics[i].timeMs <= timeMs) {
        targetIndex = i;
      } else {
        break;
      }
    }

    if (targetIndex !== activeLyricIndex) {
      activeLyricIndex = targetIndex;
      const lines = document.querySelectorAll('.lyric-line');
      lines.forEach((el, idx) => {
        el.classList.toggle('active', idx === activeLyricIndex);
      });
      scrollLyricsToActive(false);
    }
  }

  function scrollLyricsToActive(instant) {
    const container = document.getElementById('lyrics-scroll-container');
    const activeLine = container?.querySelector('.lyric-line.active');
    if (container && activeLine) {
      const targetTop = activeLine.offsetTop - container.offsetHeight / 2 + activeLine.offsetHeight / 2;
      container.scrollTo({
        top: Math.max(0, targetTop),
        behavior: instant ? 'auto' : 'smooth'
      });
    }
  }

  // --------------------------------------------------------------- reporting
  function reportTimelineProgress() {
    if (!audio || !isPlaying || currentIndex === -1 || !queue[currentIndex]) return;
    saveState();
    const track = queue[currentIndex];
    const positionMs = Math.floor((audio.currentTime || 0) * 1000);
    const durationMs = Math.floor((audio.duration || 0) * 1000) || 0;

    apiFetch('/api/v1/playback/timeline', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      keepalive: true,
      body: JSON.stringify({
        trackId: parseInt(track.id, 10),
        positionMillis: positionMs,
        durationMillis: durationMs,
        state: isPlaying ? 'playing' : 'paused'
      })
    }).catch(() => {});
  }

  function scrobbleTrack(trackId) {
    if (!trackId || lastScrobbledTrackId === trackId) return;
    lastScrobbledTrackId = trackId;

    const positionMs = audio ? Math.floor((audio.currentTime || 0) * 1000) : 0;
    const durationMs = audio ? Math.floor((audio.duration || 0) * 1000) : 0;

    apiFetch('/api/v1/playback/scrobble', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      keepalive: true,
      body: JSON.stringify({
        trackId: parseInt(trackId, 10),
        positionMillis: positionMs,
        durationMillis: durationMs,
        timestamp: new Date().toISOString()
      })
    }).catch(() => {});
  }

  // ------------------------------------------------------------------- state
  function saveState() {
    try {
      const state = {
        queue: queue,
        currentIndex: currentIndex,
        currentTime: audio ? audio.currentTime : 0,
        isPlaying: isPlaying,
        loopMode: loopMode,
        shuffle: shuffleOn,
        volume: audio ? audio.volume : 1
      };
      sessionStorage.setItem('032_player_state', JSON.stringify(state));
    } catch (_) {}
  }

  function restoreState() {
    try {
      const raw = sessionStorage.getItem('032_player_state');
      if (!raw) return;
      const state = JSON.parse(raw);
      if (state.queue && state.queue.length > 0) {
        queue = state.queue;
        currentIndex = state.currentIndex >= 0 ? state.currentIndex : 0;
        // Legacy snapshots may still carry the combined loopMode 'shuffle'.
        if (state.loopMode === 'shuffle') {
          loopMode = 'all';
          shuffleOn = true;
        } else {
          loopMode = state.loopMode || 'all';
          shuffleOn = !!state.shuffle;
        }

        const loopBtn = document.getElementById('player-btn-loop');
        if (loopBtn) {
          if (loopMode === 'one') {
            swapIcon(loopBtn, 'icon-repeat-1');
            loopBtn.classList.add('active');
            loopBtn.title = '循环模式 (单曲循环)';
          } else {
            swapIcon(loopBtn, 'icon-repeat');
            loopBtn.classList.remove('active');
            loopBtn.title = '循环模式 (全部循环)';
          }
          // [P2-3] keep aria-pressed in sync on restore.
          loopBtn.setAttribute('aria-pressed', String(loopMode === 'one'));
        }
        const shuffleBtn = document.getElementById('player-btn-shuffle');
        if (shuffleBtn) {
          shuffleBtn.classList.toggle('active', shuffleOn);
          shuffleBtn.setAttribute('aria-pressed', String(shuffleOn));
          shuffleBtn.title = shuffleOn ? '随机播放 (开)' : '随机播放';
        }

        const track = queue[currentIndex];
        if (track) {
          updatePlayerMetaUI(track);
          if (audio) {
            audio.src = track.streamUrl;
            audio.load();
            if (state.volume !== undefined) {
              audio.volume = state.volume;
              const slider = document.getElementById('player-volume-slider');
              if (slider) slider.value = state.volume;
              updateVolumeUI(state.volume);
            }

            if (state.currentTime > 0) {
              const applyTime = () => {
                try {
                  if (state.currentTime > 0) {
                    audio.currentTime = state.currentTime;
                  }
                } catch (_) {}
              };
              if (audio.readyState >= 1) {
                applyTime();
              } else {
                audio.addEventListener('loadedmetadata', applyTime, { once: true });
              }
            }

            if (state.isPlaying) {
              const playPromise = audio.play();
              if (playPromise !== undefined) {
                playPromise.then(() => {
                  isPlaying = true;
                  updatePlayButtonUI(true);
                  updateTrackRowsUI();
                }).catch(() => {
                  // Autoplay policy prevented immediate playback until user clicks
                  isPlaying = false;
                  updatePlayButtonUI(false);
                  updateTrackRowsUI();
                });
              }
            }
          }
        }
      }
    } catch (_) {}
  }

  // --------------------------------------------------------------- shortcuts
  function bindGlobalShortcuts() {
    document.addEventListener('keydown', (e) => {
      const target = e.target instanceof Element ? e.target : document.activeElement;
      const interactive = target?.closest('input, textarea, select, button, a, summary, [contenteditable="true"], [role="slider"]');
      if (e.key === 'Escape') {
        if (document.getElementById('lyrics-overlay')?.style.display !== 'none') {
          e.preventDefault(); document.getElementById('lyrics-close-btn')?.click(); return;
        }
        // [M2] Esc closes the mobile queue drawer and returns focus to its
        // trigger button.
        if (document.body.classList.contains('np-open')) {
          e.preventDefault();
          document.body.classList.remove('np-open');
          syncPanelA11y();
          const queueBtn = document.getElementById('player-btn-queue');
          if (queueBtn) { queueBtn.setAttribute('aria-expanded', 'false'); queueBtn.focus(); }
          return;
        }
      }
      if (localStorage.getItem('032_shortcuts_disabled') === '1' || interactive || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey || e.isComposing) return;
      if (e.code === 'Space') { e.preventDefault(); togglePlay(); }
      else if (e.key === 'l' || e.key === 'L') { e.preventDefault(); document.getElementById('player-btn-lyrics')?.click(); }
    });
  }

  // ------------------------------------------------------------- image fade
  function setupImageFadeIn() {
    // load/error do not bubble; listen in the capture phase at the root.
    document.addEventListener('load', (e) => {
      if (e.target instanceof HTMLImageElement) e.target.classList.add('is-loaded');
    }, true);
    document.addEventListener('error', (e) => {
      if (e.target instanceof HTMLImageElement) e.target.classList.add('is-loaded');
    }, true);
    markLoadedImages(document);
  }

  function markLoadedImages(root) {
    root.querySelectorAll('img.fade-img').forEach((img) => {
      if (img.complete) img.classList.add('is-loaded');
    });
  }

  // ------------------------------------------------------------------- misc
  // [P3] m:ss everywhere (h:mm:ss past one hour), matching the Go-side
  // formatDurationMillis used by album/work detail pages.
  function formatTime(seconds) {
    if (isNaN(seconds) || seconds < 0) return '0:00';
    const totalSeconds = Math.floor(seconds);
    const hours = Math.floor(totalSeconds / 3600);
    const mins = Math.floor((totalSeconds % 3600) / 60);
    const secs = totalSeconds % 60;
    if (hours > 0) return `${hours}:${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
    return `${mins}:${String(secs).padStart(2, '0')}`;
  }

  let toastTimer = null;

  function showToast(msg, persistent) {
    let toast = document.querySelector('.client-toast');
    if (!toast) {
      toast = document.createElement('div');
      toast.className = 'toast client-toast';
      document.body.appendChild(toast);
    }
    toast.setAttribute('aria-live', persistent ? 'assertive' : 'polite');
    toast.setAttribute('role', persistent ? 'alert' : 'status');
    toast.textContent = msg;
    toast.style.display = 'block';
    if (toastTimer) { clearTimeout(toastTimer); toastTimer = null; }
    if (!persistent) {
      // Guard by message so a stale transient timer can never hide a newer
      // (possibly persistent) notification.
      toastTimer = setTimeout(() => {
        toastTimer = null;
        if (toast.textContent === msg) toast.style.display = 'none';
      }, 3500);
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
