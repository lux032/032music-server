// 032 Music Server - now-playing.js
import { s } from './state.js';
import { formatTime, svgIcon, showUndoToast, showToast, apiFetch, swapIcon } from './util.js';
import { navigateFromPlayer } from './router.js';
import { bindSeekBar, updateTrackRowsUI, updatePlayerMetaUI, updatePlayButtonUI } from './player-bar.js';
import { loadLyrics } from './lyrics.js';
import { togglePlay, playTrackAtIndex, moveQueueTrack, removeQueueTrack, stopQueuePlayback, saveState, updateMediaSession } from './player-core.js';

  const trackDetails = new Map();
  let detailId = null;
  let favoriteBusy = false;
  let menu = null;
  let menuTrigger = null;
  let drag = null;
  let dismissUndo = null;
  let restorePosition = null;
  const announce = (message) => { const live = document.getElementById('np-queue-live'); if (live) live.textContent = message; };

  function renderArtistLinks(container, detail, fallback) {
    if (!container) return;
    container.replaceChildren();
    if (detail?.artists?.length) {
      detail.artists.forEach((artist, index) => {
        if (index) container.append(document.createTextNode('、'));
        const link = document.createElement('a');
        link.href = `/admin/artists/${artist.id}`;
        link.textContent = artist.name;
        link.dataset.playerNav = '1';
        container.append(link);
      });
    } else container.textContent = fallback || '—';
  }

  function renderAlbumLink(container, detail, name, suffix = '') {
    if (!container) return;
    container.replaceChildren();
    if (detail?.albumId) {
      const link = document.createElement('a');
      link.href = `/admin/albums/${detail.albumId}`;
      link.textContent = name || detail.album || '';
      link.dataset.playerNav = '1';
      container.append(link);
      if (suffix) container.append(document.createTextNode(suffix));
    } else container.textContent = (name || '') + suffix;
  }

  function syncFavorite() {
    const track = s.queue[s.currentIndex];
    const detail = track && trackDetails.get(String(track.id));
    const favored = !!detail?.isFavorite;
    for (const id of ['np-favorite', 'player-btn-favorite']) {
      const button = document.getElementById(id);
      if (!button) continue;
      button.disabled = !track || !detail || favoriteBusy;
      if (!detail && track) button.title = '歌曲信息加载中';
      button.setAttribute('aria-pressed', String(favored));
      button.setAttribute('aria-label', track ? `${favored ? '取消收藏' : '收藏'} ${track.title}` : '收藏');
      button.title = favored ? '取消收藏' : '收藏';
      swapIcon(button, favored ? 'icon-heart-fill' : 'icon-heart');
    }
    if (detail && track) {
      document.querySelectorAll('.inline-favorite, .compact-action').forEach(form => {
        if (form.getAttribute('action') !== `/admin/favorites/tracks/${encodeURIComponent(track.id)}`) return;
        const value = form.querySelector('input[name="favorite"]');
        if (value) value.value = detail.isFavorite ? '0' : '1';
        const button = form.querySelector('button');
        if (!button) return;
        button.classList.toggle('selected', favored);
        if (form.classList.contains('inline-favorite')) {
          button.title = button.getAttribute('aria-label') = `${favored ? '取消收藏' : '收藏'} ${track.title}`;
          swapIcon(button, favored ? 'icon-heart-fill' : 'icon-heart');
        } else button.textContent = favored ? '取消收藏' : '加入收藏';
      });
    }
  }

  async function loadTrackDetail(track) {
    if (!track) { detailId = null; syncFavorite(); return; }
    const id = String(track.id);
    if (detailId === id) return;
    detailId = id;
    syncFavorite();
    if (!trackDetails.has(id)) {
      try {
        const res = await fetch(`/api/v1/tracks/${encodeURIComponent(id)}`, { credentials: 'same-origin' });
        if (!res.ok) throw new Error('track detail');
        trackDetails.set(id, await res.json());
      } catch (_) {
        if (detailId === id) {
          showToast('无法获取歌曲信息，请重试', true);
          const retry = document.createElement('button'); retry.type = 'button'; retry.textContent = '重试';
          document.querySelector('.client-toast').appendChild(retry);
          retry.addEventListener('click', () => { retry.remove(); detailId = null; loadTrackDetail(track); });
          syncFavorite();
        }
        return;
      }
    }
    if (detailId === id) {
      lastQueueSignature = null;
      const toast = document.querySelector('.client-toast');
      if (toast?.style.display !== 'none' && toast?.textContent.includes('无法获取歌曲信息，请重试')) toast.style.display = 'none';
      syncFavorite(); syncNowPlayingPanel();
    }
  }

  async function toggleFavorite() {
    const track = s.queue[s.currentIndex];
    if (!track || favoriteBusy) return;
    const id = String(track.id);
    const detail = trackDetails.get(id);
    if (!detail) { detailId = null; loadTrackDetail(track); return; }
    favoriteBusy = true;
    detail.isFavorite = !detail.isFavorite;
    syncFavorite();
    try {
      const res = await apiFetch(`/api/v1/tracks/${encodeURIComponent(id)}/favorite`, { method: detail.isFavorite ? 'PUT' : 'DELETE' });
      if (!res.ok) throw new Error('favorite failed');
    } catch (_) {
      detail.isFavorite = !detail.isFavorite;
      showToast('收藏操作失败，请重试');
    } finally { favoriteBusy = false; syncFavorite(); }
  }

  function closeMenu(restore = true) {
    if (!menu) return;
    menu.remove(); menu = null;
    menuTrigger?.setAttribute('aria-expanded', 'false');
    if (restore) menuTrigger?.focus();
    menuTrigger = null;
  }

  function openMenu(trigger) {
    if (menu) { closeMenu(); return; }
    menuTrigger = trigger;
    menu = document.createElement('div');
    menu.className = 'np-more-menu';
    menu.setAttribute('role', 'menu');
    const track = s.queue[s.currentIndex];
    const detail = track && trackDetails.get(String(track.id));
    for (const [label, enabled, action] of [
      ['查看专辑', !!detail?.albumId, () => navigateFromPlayer(`/admin/albums/${detail.albumId}`)],
      ['从队列移除', !!track, () => removeQueueTrack(s.currentIndex)],
      ['清空队列', s.queue.length > 0, clearQueue],
      ['快捷键说明', true, () => document.getElementById('player-btn-shortcuts')?.click()]
    ]) {
      const button = document.createElement('button');
      button.type = 'button'; button.setAttribute('role', 'menuitem');
      button.textContent = label; button.disabled = !enabled;
      button.addEventListener('click', () => { closeMenu(false); action(); });
      menu.appendChild(button);
    }
    document.body.appendChild(menu);
    const rect = trigger.getBoundingClientRect();
    menu.style.left = `${Math.max(8, Math.min(rect.left, window.innerWidth - menu.offsetWidth - 8))}px`;
    menu.style.top = `${rect.bottom + menu.offsetHeight + 8 <= window.innerHeight ? rect.bottom : Math.max(8, rect.top - menu.offsetHeight)}px`;
    menu.addEventListener('keydown', e => {
      if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
      e.preventDefault();
      const buttons = Array.from(menu.querySelectorAll('button:not(:disabled)'));
      const index = buttons.indexOf(document.activeElement);
      buttons[(index + (e.key === 'ArrowDown' ? 1 : buttons.length - 1)) % buttons.length].focus();
    });
    trigger.setAttribute('aria-expanded', 'true');
    menu.querySelector('button:not(:disabled)')?.focus();
  }

  function clearQueue() {
    if (!s.queue.length) return;
    if (dismissUndo) dismissUndo();
    if (restorePosition && s.audio) s.audio.removeEventListener('loadedmetadata', restorePosition);
    restorePosition = null;
    const snapshot = s.queue.slice(), index = s.currentIndex, position = s.audio?.currentTime || 0;
    s.queue = [];
    stopQueuePlayback();
    dismissUndo = showUndoToast('已清空播放队列', () => {
      dismissUndo = null;
      if (s.queue.length || s.currentIndex !== -1 || s.audio?.getAttribute('src')) return;
      if (s.audio) s.audio.pause();
      s.isPlaying = false; updatePlayButtonUI(false);
      s.queue = snapshot; s.currentIndex = index;
      const track = s.queue[index];
      if (track && s.audio) {
        s.audio.src = track.streamUrl; s.audio.load();
        restorePosition = () => { s.audio.currentTime = position; restorePosition = null; };
        s.audio.addEventListener('loadedmetadata', restorePosition, { once: true });
        updatePlayerMetaUI(track);
        loadLyrics(track.id); updateMediaSession(track);
      }
      updateTrackRowsUI(); saveState(); emitPlayerState();
    });
  }

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
  export function syncPanelA11y() {
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

  export function toggleNowPlayingPanel() {
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

  export function setupNowPlayingPanel() {
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

    document.getElementById('np-queue-clear')?.addEventListener('click', clearQueue);
    for (const id of ['np-favorite', 'player-btn-favorite']) document.getElementById(id)?.addEventListener('click', toggleFavorite);
    for (const id of ['np-more', 'player-btn-more']) {
      const button = document.getElementById(id);
      if (button) button.addEventListener('click', () => openMenu(button));
    }
    document.addEventListener('pointerdown', e => { if (menu && !menu.contains(e.target) && !menuTrigger.contains(e.target)) closeMenu(false); });
    document.addEventListener('focusin', e => { if (menu && !menu.contains(e.target) && !menuTrigger.contains(e.target)) closeMenu(false); });
    document.addEventListener('032:player-state', () => {
      if (restorePosition && s.audio?.getAttribute('src') !== s.queue[s.currentIndex]?.streamUrl) {
        s.audio.removeEventListener('loadedmetadata', restorePosition); restorePosition = null;
      }
      if (dismissUndo && (s.queue.length || s.currentIndex !== -1)) { dismissUndo(); dismissUndo = null; }
    });
    for (const id of ['np-more', 'player-btn-more']) document.getElementById(id)?.setAttribute('aria-haspopup', 'menu');
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && menu) { e.preventDefault(); e.stopImmediatePropagation(); closeMenu(); } }, true);
    document.addEventListener('032:pjax-applied', () => { closeMenu(false); syncFavorite(); });
    const live = document.createElement('div'); live.id = 'np-queue-live'; live.className = 'sr-only'; live.setAttribute('aria-live', 'polite');
    document.body.appendChild(live);
    const list = document.getElementById('np-queue-list');
    if (list) {
      list.addEventListener('keydown', e => {
        if (!e.altKey || !['ArrowUp', 'ArrowDown'].includes(e.key)) return;
        const item = e.target.closest('li[data-qindex]'); if (!item) return;
        e.preventDefault();
        const from = Number(item.dataset.qindex), to = from + (e.key === 'ArrowDown' ? 1 : -1);
        if (moveQueueTrack(from, to)) { lastQueueSignature = null; renderQueueList(); list.querySelector(`li[data-qindex="${to}"] .q-row`)?.focus(); announce(`已移到第 ${to + 1} 位`); }
      });
      list.addEventListener('pointerdown', e => {
        const handle = e.target.closest('.q-drag'); if (!handle || drag) return;
        const item = handle.closest('li[data-qindex]'); if (!item) return;
        e.preventDefault(); list.setPointerCapture(e.pointerId);
        drag = { item, handle, pointerId: e.pointerId, from: Number(item.dataset.qindex), to: Number(item.dataset.qindex), y: e.clientY, lastX: e.clientX, lastY: e.clientY, scroll0: list.scrollTop, maxScroll: Math.max(0, list.scrollHeight - list.clientHeight), scrollTimer: null };
        drag.scrollTimer = setInterval(() => {
          if (!drag) return;
          const box = list.getBoundingClientRect();
          const edge = 45;
          const delta = drag.lastY < box.top + edge ? -12 : drag.lastY > box.bottom - edge ? 12 : 0;
          // Clamp to the pre-drag extent: the translated drag row itself adds
          // scrollable overflow, which would otherwise let auto-scroll run forever.
          const next = Math.min(drag.maxScroll, Math.max(0, list.scrollTop + delta));
          if (next !== list.scrollTop) { list.scrollTop = next; updateDrag(drag.lastX, drag.lastY); }
        }, 30);
        item.classList.add('dragging');
        list.addEventListener('pointermove', onDragMove);
        list.addEventListener('pointerup', endDrag, { once: true });
        list.addEventListener('pointercancel', cancelDrag, { once: true });
        list.addEventListener('lostpointercapture', cancelDrag, { once: true });
      });
      function updateDrag(x, y) {
        if (!drag) return;
        const { item } = drag;
        const box = list.getBoundingClientRect();
        // offsetTop is relative to the list's offset parent, not the viewport.
        const parentTop = list.offsetParent?.getBoundingClientRect().top || 0;
        const pointerY = y - parentTop + list.scrollTop;
        const rows = Array.from(list.children).filter(row => row !== item && row.hasAttribute('data-qindex'));
        // The captured pointer still hits the dragged row. Inspect the stack
        // below it, while using layout offsets rather than animated rectangles.
        const hit = document.elementsFromPoint(x, y).find(el => list.contains(el) && !item.contains(el));
        const overList = hit && x >= box.left && x <= box.right && y >= box.top && y <= box.bottom;
        if (overList && rows.length) {
          const insertion = rows.find(row => pointerY < row.offsetTop + row.offsetHeight / 2);
          const next = insertion || null;
          if (next !== item.nextSibling) {
            const previous = new Map(Array.from(list.children, child => [child, child.offsetTop]));
            list.insertBefore(item, next);
            drag.to = Array.from(list.children).indexOf(item);
            drag.y += item.offsetTop - previous.get(item);
            if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
              for (const child of list.children) {
                if (child === item) continue;
                const delta = previous.get(child) - child.offsetTop;
                if (delta) child.animate([{ transform: `translateY(${delta}px)` }, { transform: 'translateY(0)' }], { duration: 150, easing: 'ease-out' });
              }
            }
          }
        }
        item.style.transform = `translateY(${y - drag.y + list.scrollTop - drag.scroll0}px)`;
      }
      function onDragMove(e) {
        if (!drag || e.pointerId !== drag.pointerId) return;
        drag.lastX = e.clientX; drag.lastY = e.clientY;
        updateDrag(e.clientX, e.clientY);
      }
      function cleanupDrag() {
        const previous = drag;
        clearInterval(previous.scrollTimer);
        list.removeEventListener('pointermove', onDragMove);
        list.removeEventListener('pointerup', endDrag);
        list.removeEventListener('pointercancel', cancelDrag);
        list.removeEventListener('lostpointercapture', cancelDrag);
        previous.item.style.transform = ''; previous.item.classList.remove('dragging');
        drag = null;
        return previous;
      }
      function cancelDrag(e) {
        if (!drag || (e && e.pointerId !== drag.pointerId)) return;
        const { item, from } = cleanupDrag();
        const sibling = Array.from(list.children).filter(child => child !== item)[from] || null;
        list.insertBefore(item, sibling);
      }
      function endDrag(e) {
        if (!drag || e.pointerId !== drag.pointerId) return;
        const { from, to } = cleanupDrag();
        if (moveQueueTrack(from, to)) { lastQueueSignature = null; renderQueueList(); announce(`已移到第 ${to + 1} 位`); }
      }
      list.addEventListener('click', e => {
        const remove = e.target.closest('.q-remove'); if (!remove) return;
        e.stopPropagation(); removeQueueTrack(Number(remove.closest('li').dataset.qindex));
      });
      list.addEventListener('click', (e) => {
        if (e.target.closest('[data-player-nav]')) return;
        if (e.target.closest('.q-remove, .q-drag, .q-artist')) return;
        const item = e.target instanceof Element ? e.target.closest('li[data-qindex]') : null;
        if (!item) return;
        const index = parseInt(item.getAttribute('data-qindex'), 10);
        if (Number.isNaN(index)) return;
        if (index === s.currentIndex) togglePlay();
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
  export function emitPlayerState() {
    if (emitScheduled) return;
    emitScheduled = true;
    requestAnimationFrame(() => {
      emitScheduled = false;
      const track = s.queue[s.currentIndex] || null;
      document.dispatchEvent(new CustomEvent('032:player-state', {
        detail: {
          track,
          queueLength: s.queue.length,
          currentIndex: s.currentIndex,
          isPlaying: s.isPlaying,
          loopMode: s.loopMode,
          shuffle: s.shuffleOn
        }
      }));
    });
  }

  function syncNowPlayingPanel() {
    const panel = document.getElementById('now-playing');
    if (!panel) return;
    const track = s.queue[s.currentIndex] || null;

    const titleEl = document.getElementById('np-title');
    const artistEl = document.getElementById('np-artist');
    const albumEl = document.getElementById('np-album');
    const cover = document.getElementById('np-cover');
    const countEl = document.getElementById('np-queue-count');

    if (titleEl) titleEl.textContent = track ? (track.title || '未知曲目') : '未在播放';
    const detail = track && trackDetails.get(String(track.id));
    renderArtistLinks(artistEl, detail, track ? track.artist : '—');
    let creditsEl = document.getElementById('np-credits');
    if (!creditsEl && artistEl) { creditsEl = document.createElement('div'); creditsEl.id = 'np-credits'; artistEl.after(creditsEl); }
    if (creditsEl) {
      creditsEl.replaceChildren();
      const labels = {lyricist:'作词',composer:'作曲',arranger:'编曲'};
      for (const credit of detail?.credits || []) {
        if (!labels[credit.role]) continue;
        creditsEl.append(document.createTextNode(`${labels[credit.role]}：`));
        credit.artists.forEach((artist,index) => {
          if (index) creditsEl.append(document.createTextNode('、'));
          const link = document.createElement('a');link.href = `/admin/artists/${artist.id}?credit=${credit.role}#credits`;link.textContent = artist.name;link.dataset.playerNav='1';creditsEl.append(link);
        });
        creditsEl.append(document.createElement('br'));
      }
    }
    renderAlbumLink(albumEl, detail, track?.album, detail?.year ? ` · ${detail.year}` : '');
    const playerArtist = document.getElementById('player-artist');
    if (playerArtist && track) {
      renderArtistLinks(playerArtist, detail, track.artist);
      if (track.album) {
        playerArtist.append(document.createTextNode(' · '));
        const albumPart = document.createElement('span');
        renderAlbumLink(albumPart, detail, track.album);
        playerArtist.append(albumPart);
      }
    }
    loadTrackDetail(track);
    syncFavorite();
    const clear = document.getElementById('np-queue-clear'); if (clear) clear.disabled = !s.queue.length;
    for (const id of ['np-more', 'player-btn-more']) { const button = document.getElementById(id); if (button) button.disabled = !track; }
    if (countEl) countEl.textContent = String(s.queue.length);

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
    return s.queue.length + ':' + s.queue.map((t) => t.id).join(',');
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
    if (s.queue.length === 0) {
      const empty = document.createElement('li');
      empty.className = 'np-queue-empty';
      empty.textContent = '队列为空，从歌曲列表开始播放。';
      list.appendChild(empty);
      return;
    }
    s.queue.forEach((track, index) => {
      const item = document.createElement('li');
      item.setAttribute('data-qindex', String(index));
      if (index === s.currentIndex) {
        item.classList.add('current');
        if (!s.isPlaying) item.classList.add('paused');
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
      if (index === s.currentIndex) {
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
      // Queue rows are buttons; keep their artist label plain text rather than
      // nesting an interactive link inside the button.
      artist.textContent = track.artist || '';
      meta.appendChild(title);
      meta.appendChild(artist);
      rowBtn.appendChild(meta);

      const duration = document.createElement('span');
      duration.className = 'q-duration';
      duration.textContent = track.durationMs > 0 ? formatTime(track.durationMs / 1000) : '';
      rowBtn.appendChild(duration);

      const remove = document.createElement('button');
      remove.type = 'button'; remove.className = 'q-remove';
      remove.setAttribute('aria-label', `从队列移除 ${track.title || '未知曲目'}`);
      remove.appendChild(svgIcon('icon-close'));
      const handle = document.createElement('button'); handle.type = 'button'; handle.className = 'q-drag';
      handle.setAttribute('aria-label', `拖动排序 ${track.title || '未知曲目'}`);
      handle.appendChild(svgIcon('icon-drag'));
      item.append(rowBtn, remove, handle);
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
  export function syncPanelQueueCurrent() {
    const list = document.getElementById('np-queue-list');
    if (!list) return;
    list.querySelectorAll('li[data-qindex]').forEach((item) => {
      const index = parseInt(item.getAttribute('data-qindex'), 10);
      const isCurrent = index === s.currentIndex;
      item.classList.toggle('current', isCurrent);
      item.classList.toggle('paused', isCurrent && !s.isPlaying);
    });
  }

  export function syncPanelProgress() {
    if (!s.audio) return;
    const curTime = s.audio.currentTime || 0;
    const durTime = s.audio.duration || 0;
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
    if (fillElem && !s.isDraggingProgress && durTime > 0) {
      fillElem.style.width = `${Math.min(100, Math.max(0, (curTime / durTime) * 100))}%`;
    }
  }

  // ------------------------------------------------------------------ lyrics
