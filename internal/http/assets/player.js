// 032 Music Server - Global Audio Player & Lyrics Engine
(function() {
  'use strict';

  if (window.__032_player_initialized) return;
  window.__032_player_initialized = true;

  // Global state
  let audio = null;
  let queue = [];
  let currentIndex = -1;
  let isPlaying = false;
  let loopMode = 'all'; // 'all', 'one', 'shuffle'
  let lyrics = [];
  let activeLyricIndex = -1;
  let lastScrobbledTrackId = null;
  let timelineReportTimer = null;
  let isDraggingProgress = false;

  function init() {
    if (!document.body || document.body.classList.contains('login-page')) return;

    createPlayerDOM();
    setupAudioElement();
    restoreState();
    bindTrackListEvents();
    bindGlobalShortcuts();
    setupPjaxNavigation();

    if (timelineReportTimer) clearInterval(timelineReportTimer);
    timelineReportTimer = setInterval(reportTimelineProgress, 5000);
    window.addEventListener('pagehide', saveState);
  }

  // ---------------------------------------------------------------------
  // PJAX navigation: full page loads destroy the <audio> element and the
  // browser autoplay policy blocks resuming without a user gesture, so
  // in-app navigation swaps <main> via fetch and keeps playback alive.
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

  async function pjaxNavigate(url, push, restoreState) {
    try {
      saveCurrentHistoryState();
      if (navigationAbortController) navigationAbortController.abort();
      navigationAbortController = new AbortController();
      const res = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/html' }, signal: navigationAbortController.signal });
      const html = await res.text();
      if (!res.ok || !applyPage(html, res.url || url, push, restoreState)) window.location.href = url;
    } catch (err) {
      if (err && err.name === 'AbortError') return;
      window.location.href = url;
    }
  }

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
      if (!applyPage(html, res.url || window.location.href, true)) showFormError(form, '服务器已响应，但页面无法更新。请刷新核对保存结果。');
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

  function applyPage(html, url, push, restoreState) {
    currentDocUrl = url;
    const doc = new DOMParser().parseFromString(html, 'text/html');
    if (!doc.body || doc.body.classList.contains('login-page')) return false;
    const newMain = doc.querySelector('main'), curMain = document.querySelector('main');
    if (!newMain || !curMain) return false;
    doc.querySelectorAll('[autofocus]').forEach((el) => el.removeAttribute('autofocus'));
    document.title = doc.title || document.title; document.body.className = doc.body.className; document.body.classList.add('has-global-player');
    curMain.replaceWith(document.adoptNode(newMain));
    const newSidebar = doc.querySelector('aside.sidebar'), curSidebar = document.querySelector('aside.sidebar');
    if (newSidebar && curSidebar) curSidebar.replaceWith(document.adoptNode(newSidebar));
    doc.querySelectorAll('script[src]').forEach((script) => { const src = script.getAttribute('src'); if (!src || document.querySelector(`script[src="${src}"]`)) return; const el = document.createElement('script'); el.src = src; document.head.appendChild(el); });
    const destination = new URL(url, window.location.href);
    if (push && destination.href !== window.location.href) history.pushState({ app: '032', url: destination.href, previousURL: window.location.href, scrollX: 0, scrollY: 0, focus: '' }, '', destination.href);
    const state = !push && restoreState && restoreState.app === '032' ? restoreState : null;
    if (state && state.url === window.location.href) {
      window.scrollTo(state.scrollX || 0, state.scrollY || 0);
      requestAnimationFrame(() => { const target = state.focus && document.querySelector(state.focus); if (target) target.focus({ preventScroll: true }); });
    } else if (destination.hash) requestAnimationFrame(() => document.getElementById(destination.hash.slice(1))?.scrollIntoView());
    else window.scrollTo(0, 0);
    bindTrackListEvents(); updateTrackRowsUI(); restoreSafeDraft(); return true;
  }

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
      </div>

      <div class="player-center">
        <div class="player-controls">
          <button id="player-btn-prev" class="player-ctrl-btn" title="上一首 (Alt+Left)">⏮</button>
          <button id="player-btn-play" class="player-ctrl-btn play-main-btn" title="播放 / 暂停 (Space)">▶</button>
          <button id="player-btn-next" class="player-ctrl-btn" title="下一首 (Alt+Right)">⏭</button>
          <button id="player-btn-loop" class="player-ctrl-btn loop-btn" title="循环模式 (全部循环)">🔁</button>
        </div>
        <div class="player-progress-row">
          <span id="player-time-cur" class="player-time">00:00</span>
          <div id="player-progress-bar" class="player-progress-bar" role="slider" tabindex="0" aria-label="播放进度" aria-valuemin="0" aria-valuemax="0" aria-valuenow="0" aria-valuetext="00:00 / 00:00">
            <div id="player-progress-fill" class="player-progress-fill"></div>
            <div id="player-progress-thumb" class="player-progress-thumb"></div>
          </div>
          <span id="player-time-total" class="player-time">00:00</span>
        </div>
      </div>

      <div class="player-right">
        <button id="player-btn-lyrics" class="player-tool-btn" title="歌词 (L)" aria-expanded="false" aria-controls="lyrics-overlay">词</button>
        <button id="player-btn-shortcuts" class="player-tool-btn" title="快捷键说明" aria-expanded="false">?</button>
        <div id="player-shortcuts-help" class="player-shortcuts-help" hidden><span>Space 播放/暂停 · L 歌词 · Esc 关闭歌词</span><button type="button" id="player-shortcuts-disable">关闭快捷键</button></div>
        <div class="player-volume-wrap">
          <button id="player-btn-mute" class="player-tool-btn" title="静音">🔊</button>
          <input id="player-volume-slider" type="range" min="0" max="1" step="0.01" value="1" title="音量">
        </div>
      </div>
    `;

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
        <button id="lyrics-close-btn" class="lyrics-close-btn" title="关闭歌词">✕</button>
      </div>
      <div id="lyrics-scroll-container" class="lyrics-content">
        <div class="lyrics-zero-state">暂无歌词</div>
      </div>
    `;

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
    const progressBar = document.getElementById('player-progress-bar');
    const volumeSlider = document.getElementById('player-volume-slider');
    const muteBtn = document.getElementById('player-btn-mute');
    const lyricsBtn = document.getElementById('player-btn-lyrics');
    const lyricsCloseBtn = document.getElementById('lyrics-close-btn');
    const shortcutsBtn = document.getElementById('player-btn-shortcuts');
    const shortcutsDisableBtn = document.getElementById('player-shortcuts-disable');

    if (playBtn) playBtn.addEventListener('click', togglePlay);
    if (prevBtn) prevBtn.addEventListener('click', playPrevious);
    if (nextBtn) nextBtn.addEventListener('click', playNext);
    if (loopBtn) loopBtn.addEventListener('click', cycleLoopMode);

    if (progressBar) {
      const seekToPosition = (clientX) => {
        if (!audio || !audio.duration) return;
        const rect = progressBar.getBoundingClientRect();
        const ratio = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
        const targetTime = ratio * audio.duration;
        audio.currentTime = targetTime;

        const curElem = document.getElementById('player-time-cur');
        const fillElem = document.getElementById('player-progress-fill');
        const thumbElem = document.getElementById('player-progress-thumb');
        if (curElem) curElem.textContent = formatTime(targetTime);
        if (fillElem) fillElem.style.width = `${ratio * 100}%`;
        if (thumbElem) thumbElem.style.left = `${ratio * 100}%`;
      };

      progressBar.addEventListener('mousedown', (e) => {
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
      progressBar.addEventListener('touchstart', (e) => {
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
      progressBar.addEventListener('keydown', (e) => {
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

  function bindTrackListEvents() {
    const trackElements = document.querySelectorAll('[data-track-id], .track-table-row, .detail-tracks article, .feature-track-list article, .playlist-track-list article, .artist-track-list article, .playback-list article');

    trackElements.forEach((el) => {
      let playBtn = el.querySelector('.row-play-btn');
      const trackId = el.getAttribute('data-track-id') || parseTrackIdFromElement(el);
      if (!trackId) return;

      el.setAttribute('data-track-id', trackId);

      if (!playBtn) {
        const numElem = el.querySelector('.track-number, span:first-child, .drag-position, .tieup-role');
        if (numElem && !numElem.querySelector('.row-play-btn')) {
          const originalText = numElem.textContent.trim();
          numElem.classList.add('playable-number');
          numElem.innerHTML = `<span class="num-text">${originalText}</span><button type="button" class="row-play-btn" title="播放">▶</button>`;
          playBtn = numElem.querySelector('.row-play-btn');
        }
      }

      const clickHandler = (e) => {
        if (e && e.target && e.target.closest('a, form, details, input, select, textarea') && !e.target.closest('.row-play-btn')) {
          return;
        }

        const pageTracks = extractAllTracksFromPage();
        const targetIndex = pageTracks.findIndex(t => String(t.id) === String(trackId));
        if (targetIndex !== -1) {
          if (currentIndex !== -1 && queue[currentIndex] && String(queue[currentIndex].id) === String(trackId)) {
            togglePlay();
          } else {
            queue = pageTracks;
            playTrackAtIndex(targetIndex);
          }
        }
      };

      if (playBtn) {
        const accessibleTitle = el.getAttribute('data-track-title') || el.querySelector('strong')?.textContent.trim() || '歌曲';
        playBtn.setAttribute('aria-label', `播放 ${accessibleTitle}`);
        playBtn.addEventListener('click', (e) => {
          e.preventDefault();
          e.stopPropagation();
          clickHandler(e);
        });
      }

      el.querySelectorAll('.queue-next-btn, .queue-append-btn').forEach((btn) => {
        btn.addEventListener('click', (e) => {
          e.preventDefault();
          e.stopPropagation();
          queueTrackFromPage(trackId, btn.classList.contains('queue-next-btn') ? 'next' : 'append');
          const menu = btn.closest('details');
          if (menu) menu.removeAttribute('open');
        });
      });

      el.addEventListener('dblclick', clickHandler);
    });

    const albumPlayBtn = document.querySelector('.album-hero .primary-round');
    if (albumPlayBtn && albumPlayBtn.textContent.includes('播放')) {
      albumPlayBtn.addEventListener('click', (e) => {
        e.preventDefault();
        const pageTracks = extractAllTracksFromPage();
        if (pageTracks.length > 0) {
          queue = pageTracks;
          playTrackAtIndex(0);
        }
      });
    }

    updateTrackRowsUI();
  }

  function parseTrackIdFromElement(el) {
    const streamAudio = el.querySelector('audio[src*="/tracks/"]');
    if (streamAudio) {
      const match = streamAudio.src.match(/\/tracks\/(\d+)\/stream/);
      if (match) return match[1];
    }
    const formAction = el.querySelector('form[action*="/tracks/"]');
    if (formAction) {
      const match = formAction.action.match(/\/tracks\/(\d+)/);
      if (match) return match[1];
    }
    const inlineInput = el.querySelector('input[name="trackId"]');
    if (inlineInput) return inlineInput.value;

    return null;
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

      tracks.push({
        id: id,
        title: title,
        artist: artist,
        album: album,
        artwork: artwork,
        container: container,
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
    showToast(mode === 'next' ? `已加入下一首播放：${track.title}` : `已添加到队列末尾：${track.title}`);
  }

  function playTrackAtIndex(index) {
    if (index < 0 || index >= queue.length) return;
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
    let prevIndex = currentIndex - 1;
    if (prevIndex < 0) prevIndex = queue.length - 1;
    playTrackAtIndex(prevIndex);
  }

  function playNext() {
    if (!audio || queue.length === 0) return;
    let nextIndex = currentIndex + 1;
    if (nextIndex >= queue.length) {
      if (loopMode === 'all') nextIndex = 0;
      else return;
    }
    playTrackAtIndex(nextIndex);
  }

  function onTrackEnded() {
    const currentTrack = queue[currentIndex];
    if (currentTrack) scrobbleTrack(currentTrack.id);

    if (loopMode === 'one') {
      audio.currentTime = 0;
      audio.play().catch(console.warn);
    } else if (loopMode === 'shuffle') {
      let nextIndex = currentIndex;
      if (queue.length > 1) {
        while (nextIndex === currentIndex) {
          nextIndex = Math.floor(Math.random() * queue.length);
        }
      } else {
        nextIndex = 0;
      }
      playTrackAtIndex(nextIndex);
    } else {
      playNext();
    }
  }

  function cycleLoopMode() {
    const loopBtn = document.getElementById('player-btn-loop');
    if (loopMode === 'all') {
      loopMode = 'one';
      if (loopBtn) {
        loopBtn.textContent = '🔂';
        loopBtn.title = '循环模式 (单曲循环)';
        loopBtn.classList.add('active');
      }
    } else if (loopMode === 'one') {
      loopMode = 'shuffle';
      if (loopBtn) {
        loopBtn.textContent = '🔀';
        loopBtn.title = '循环模式 (随机播放)';
        loopBtn.classList.add('active');
      }
    } else {
      loopMode = 'all';
      if (loopBtn) {
        loopBtn.textContent = '🔁';
        loopBtn.title = '循环模式 (全部循环)';
        loopBtn.classList.remove('active');
      }
    }
    saveState();
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

    updateActiveLyric(curTime * 1000);

    const currentTrack = queue[currentIndex];
    if (currentTrack && durTime > 10 && curTime / durTime >= 0.5 && lastScrobbledTrackId !== currentTrack.id) {
      scrobbleTrack(currentTrack.id);
    }
  }

  function updatePlayButtonUI(playing) {
    const playBtn = document.getElementById('player-btn-play');
    if (playBtn) {
      playBtn.textContent = playing ? '⏸' : '▶';
      playBtn.classList.toggle('is-playing', playing);
    }
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
        btn.textContent = (isCurrent && isPlaying) ? '⏸' : '▶';
        btn.classList.toggle('playing', isCurrent && isPlaying);
      }
    });
  }

  function updateVolumeUI(val) {
    const muteBtn = document.getElementById('player-btn-mute');
    if (muteBtn) {
      muteBtn.textContent = val === 0 ? '🔇' : (val < 0.5 ? '🔉' : '🔊');
    }
  }

  function updateMediaSession(track) {
    if (!('mediaSession' in navigator)) return;
    try {
      navigator.mediaSession.metadata = new MediaMetadata({
        title: track.title || '未知歌曲',
        artist: track.artist || '',
        album: track.album || '',
        artwork: track.artwork ? [{ src: track.artwork, sizes: '512x512', type: 'image/jpeg' }] : []
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

  function reportTimelineProgress() {
    if (!audio || !isPlaying || currentIndex === -1 || !queue[currentIndex]) return;
    saveState();
    const track = queue[currentIndex];
    const positionMs = Math.floor((audio.currentTime || 0) * 1000);
    const durationMs = Math.floor((audio.duration || 0) * 1000) || 0;

    fetch('/api/v1/playback/timeline', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
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

    fetch('/api/v1/playback/scrobble', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({
        trackId: parseInt(trackId, 10),
        positionMillis: positionMs,
        durationMillis: durationMs,
        timestamp: new Date().toISOString()
      })
    }).catch(() => {});
  }

  function saveState() {
    try {
      const state = {
        queue: queue,
        currentIndex: currentIndex,
        currentTime: audio ? audio.currentTime : 0,
        isPlaying: isPlaying,
        loopMode: loopMode,
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
        loopMode = state.loopMode || 'all';

        const loopBtn = document.getElementById('player-btn-loop');
        if (loopBtn) {
          if (loopMode === 'one') {
            loopBtn.textContent = '🔂';
            loopBtn.classList.add('active');
          } else if (loopMode === 'shuffle') {
            loopBtn.textContent = '🔀';
            loopBtn.classList.add('active');
          } else {
            loopBtn.textContent = '🔁';
            loopBtn.classList.remove('active');
          }
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

  function bindGlobalShortcuts() {
    document.addEventListener('keydown', (e) => {
      const target = e.target instanceof Element ? e.target : document.activeElement;
      const interactive = target?.closest('input, textarea, select, button, a, summary, [contenteditable="true"], [role="slider"]');
      if (e.key === 'Escape' && document.getElementById('lyrics-overlay')?.style.display !== 'none') {
        e.preventDefault(); document.getElementById('lyrics-close-btn')?.click(); return;
      }
      if (localStorage.getItem('032_shortcuts_disabled') === '1' || interactive || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey || e.isComposing) return;
      if (e.code === 'Space') { e.preventDefault(); togglePlay(); }
      else if (e.key === 'l' || e.key === 'L') { e.preventDefault(); document.getElementById('player-btn-lyrics')?.click(); }
    });
  }

  function formatTime(seconds) {
    if (isNaN(seconds) || seconds < 0) return '00:00';
    const mins = Math.floor(seconds / 60);
    const secs = Math.floor(seconds % 60);
    return `${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
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
