// 032 Music Server - queue.js
import { s } from './state.js';
import { svgIcon, showToast, showUndoToast, artworkForSize, apiFetch, swapIcon } from './util.js';
import { playTrackAtIndex, togglePlay, saveState } from './player-core.js';
import { emitPlayerState } from './now-playing.js';
import { updateTrackRowsUI } from './player-bar.js';

  export function bindTrackListEvents() {
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
      const pageQueue = e.target.closest('.album-page-queue');
      if (pageQueue) {
        e.preventDefault();
        const pageTracks = extractAllTracksFromPage();
        if (pageTracks.length) queueTracks(pageTracks, pageQueue.dataset.mode === 'next' ? 'next' : 'append', albumLabel(pageTracks[0].album));
        const menu = pageQueue.closest('details');
        if (menu) { menu.removeAttribute('open'); menu.querySelector('summary')?.focus(); }
        return;
      }
      const albumPlayBtn = e.target.closest('.album-hero .primary-round, [data-play-all], [data-play-shuffle], [data-queue-all]');
      if (albumPlayBtn) {
        e.preventDefault();
        const pageTracks = extractAllTracksFromPage();
        if (pageTracks.length > 0) {
          const label = albumPlayBtn.closest('.album-hero') ? albumLabel(pageTracks[0].album) : listLabel();
          if (albumPlayBtn.hasAttribute('data-queue-all')) { queueTracks(pageTracks, 'append', label); return; }
          if (albumPlayBtn.hasAttribute('data-play-shuffle')) {
            for (let i = pageTracks.length - 1; i > 0; i--) { const j = Math.floor(Math.random() * (i + 1)); [pageTracks[i], pageTracks[j]] = [pageTracks[j], pageTracks[i]]; }
          }
          replaceQueue(pageTracks, 0, label);
        }
      }
    });

    document.addEventListener('dblclick', (e) => {
      if (!(e.target instanceof Element)) return;
      const row = e.target.closest('[data-track-id]');
      if (!row) return;
      if (e.target.closest('a, form, details, input, select, textarea, button, [role=menu], [role=dialog]')) return;
      playRowTrack(row);
    });

    // Favorite toggles (album card heart + menu item, artist hearts) switch
    // in place through the API instead of a PJAX POST that would reload the
    // page and jump to the top. Capture phase so this runs before the
    // router's submit handler; without JS the forms still post normally.
    document.addEventListener('submit', (e) => {
      const form = e.target;
      if (!(form instanceof HTMLFormElement) || !form.hasAttribute('data-favorite-toggle')) return;
      const match = /^\/admin\/favorites\/(albums|artists|tracks)\/(\d+)$/.exec(form.getAttribute('action') || '');
      if (!match) return;
      e.preventDefault();
      e.stopPropagation();
      const menu = form.closest('details');
      if (menu) menu.removeAttribute('open');
      toggleFavorite(match[1], match[2], form.querySelector('input[name="favorite"]')?.value === '1');
    }, true);

    decorateTrackRows(document);
    updateTrackRowsUI();
  }

  const favoriteBusy = new Set();
  const favoriteNouns = { albums: '专辑', artists: '艺术家', tracks: '歌曲' };
  function syncFavorite(kind, id, favorite) {
    document.querySelectorAll(`form[data-favorite-toggle][action="/admin/favorites/${kind}/${id}"]`).forEach((form) => {
      const value = form.querySelector('input[name="favorite"]');
      if (value) value.value = favorite ? '0' : '1';
      const button = form.querySelector('button');
      if (!button) return;
      if (!button.querySelector('use')) { button.textContent = favorite ? '取消收藏' : '加入收藏'; return; }
      const name = form.dataset.favoriteName || '';
      button.classList.toggle(form.dataset.favoriteClass || 'is-favorite', favorite);
      button.setAttribute('aria-pressed', String(favorite));
      button.title = favorite ? '取消收藏' : '加入收藏';
      button.setAttribute('aria-label', `${favorite ? '取消收藏' : '收藏'} ${name}`.trim());
      swapIcon(button, favorite ? 'icon-heart-fill' : 'icon-heart');
      const label = button.querySelector('.fav-label');
      if (label) label.textContent = favorite ? '已收藏' : '收藏';
    });
  }
  async function toggleFavorite(kind, id, favorite) {
    const key = `${kind}:${id}`;
    if (favoriteBusy.has(key)) return;
    favoriteBusy.add(key);
    syncFavorite(kind, id, favorite);
    const announce = (value) => document.dispatchEvent(new CustomEvent('032:favorite-changed', { detail: { kind, id: String(id), favorite: value } }));
    announce(favorite);
    try {
      const res = await apiFetch(`/api/v1/${kind}/${encodeURIComponent(id)}/favorite`, { method: favorite ? 'PUT' : 'DELETE' });
      if (!res.ok) throw new Error('favorite failed');
      showToast(favorite ? `已收藏${favoriteNouns[kind]}` : `已取消收藏${favoriteNouns[kind]}`);
    } catch (_) {
      syncFavorite(kind, id, !favorite);
      announce(!favorite);
      showToast('收藏操作失败，请重试');
    } finally {
      favoriteBusy.delete(key);
    }
  }

  function playRowTrack(row) {
    const trackId = row.getAttribute('data-track-id');
    if (!trackId) return;
    const pageTracks = extractAllTracksFromPage();
    const targetIndex = pageTracks.findIndex(t => String(t.id) === String(trackId));
    if (targetIndex !== -1) {
      if (s.currentIndex !== -1 && s.queue[s.currentIndex] && String(s.queue[s.currentIndex].id) === String(trackId)) {
        togglePlay();
      } else {
        replaceQueue(pageTracks, targetIndex, `“${pageTracks[targetIndex].title}”`);
      }
    }
  }

  // annotateTrackGaps notes missing track numbers on an album page so a
  // curator sees an incomplete rip without counting rows.
  function annotateTrackGaps(root) {
    const total = root.querySelector ? root.querySelector('[data-track-total]') : null;
    if (!total || total.dataset.gapsChecked) return;
    total.dataset.gapsChecked = '1';
    const discs = new Map();
    root.querySelectorAll('.num-text[data-disc][data-track-no]').forEach(el => {
      const disc = parseInt(el.dataset.disc, 10) || 1, no = parseInt(el.dataset.trackNo, 10);
      if (!no) return;
      if (!discs.has(disc)) discs.set(disc, []);
      discs.get(disc).push(no);
    });
    const ranges = [];
    discs.forEach((numbers, disc) => {
      numbers.sort((a, b) => a - b);
      let expected = 1;
      for (const n of numbers) {
        if (n > expected) ranges.push(n - 1 > expected ? `${disc}.${expected}–${disc}.${n - 1}` : `${disc}.${expected}`);
        expected = Math.max(expected, n + 1);
      }
    });
    if (!ranges.length) return;
    const note = document.createElement('span');
    note.className = 'track-gap-note';
    note.textContent = `缺 ${ranges.length > 3 ? ranges.slice(0, 3).join('、') + ' 等' : ranges.join('、')}`;
    note.title = `曲目编号不连续，缺少：${ranges.join('、')}`;
    total.append(' · ', note);
  }

  export function decorateTrackRows(root) {
    annotateTrackGaps(root);
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

  function albumLabel(title) { return title ? `《${title}》` : '这张专辑'; }
  function listLabel() { const h = document.querySelector('#app-main h1')?.textContent.trim(); return h ? `《${h}》` : '当前列表'; }

  function sameQueue(a, b) {
    return a.length === b.length && a.every((track, i) => String(track.id) === String(b[i].id));
  }

  // replaceQueue is the single path for "play this list": it swaps the queue
  // and, when that discards a different queue the user had built, offers the
  // same five-second undo as clearing the queue does.
  let dismissReplaceUndo = null;
  function replaceQueue(tracks, startIndex, label) {
    const previous = s.queue.slice();
    const snapshot = previous.length && !sameQueue(previous, tracks) ? {
      queue: previous, index: s.currentIndex, history: s.playHistory.slice(),
      position: s.audio?.currentTime || 0, wasPlaying: s.isPlaying
    } : null;
    s.queue = tracks;
    s.playHistory = []; // queue replaced: position history is meaningless
    playTrackAtIndex(startIndex);
    if (!snapshot) return;
    if (dismissReplaceUndo) dismissReplaceUndo();
    dismissReplaceUndo = showUndoToast(`正在播放${label}，原队列 ${snapshot.queue.length} 首已替换`, () => {
      dismissReplaceUndo = null;
      restoreQueue(snapshot);
    });
  }

  function restoreQueue(snapshot) {
    s.queue = snapshot.queue;
    s.playHistory = snapshot.history;
    const index = snapshot.index >= 0 && snapshot.index < s.queue.length ? snapshot.index : -1;
    if (index < 0) { s.currentIndex = -1; saveState(); emitPlayerState(); updateTrackRowsUI(); return; }
    playTrackAtIndex(index, true);
    const audio = s.audio;
    if (!audio) return;
    const resume = () => {
      if (snapshot.position > 0 && isFinite(audio.duration) && snapshot.position < audio.duration) audio.currentTime = snapshot.position;
      if (!snapshot.wasPlaying) audio.pause();
    };
    if (audio.readyState >= 1) resume(); else audio.addEventListener('loadedmetadata', resume, { once: true });
    showToast('已恢复原播放队列');
  }

  // queueTracks inserts tracks without replacing the queue: 'next' right
  // after the current track, 'append' at the end. With nothing queued the
  // tracks start playing at once (current behaviour), and the toast says so.
  function queueTracks(tracks, mode, label) {
    if (!tracks.length) return;
    const count = tracks.length > 1 ? `（${tracks.length} 首）` : '';
    if (!s.queue.length || s.currentIndex < 0) {
      s.queue = tracks.slice();
      s.playHistory = [];
      playTrackAtIndex(0);
      showToast(`队列为空，已开始播放${label}${count}`);
      return;
    }
    if (mode === 'next') s.queue.splice(s.currentIndex + 1, 0, ...tracks);
    else s.queue.push(...tracks);
    saveState();
    emitPlayerState();
    showToast(`${mode === 'next' ? '已加入下一首播放' : '已添加到队列末尾'}：${label}${count}`);
  }

  function extractAllTracksFromPage() {
    const tracks = [];
    const elements = document.querySelectorAll('[data-track-list] [data-track-id]');

    elements.forEach(el => {
      const id = el.getAttribute('data-track-id');
      if (!id) return;

      const title = el.getAttribute('data-track-title') || el.querySelector('strong')?.textContent.trim() || '未知歌曲';
      const artist = el.getAttribute('data-track-artist') || el.querySelector('.album-artist-links, small')?.textContent.trim() || '未知歌手';
      const album = el.getAttribute('data-track-album') || el.querySelector('a[href*="/admin/albums/"]')?.textContent.trim() || '';
      const artwork = el.getAttribute('data-track-artwork') || el.querySelector('img')?.src || '';
      const container = el.getAttribute('data-track-container') || el.querySelector('.format-pill')?.textContent.trim() || '';
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
    if (s.queue.length === 0 || s.currentIndex === -1) {
      s.queue.push(track);
      playTrackAtIndex(s.queue.length - 1);
      showToast(`队列为空，已开始播放“${track.title}”`);
      return;
    }
    if (mode === 'next') s.queue.splice(s.currentIndex + 1, 0, track);
    else s.queue.push(track);
    saveState();
    emitPlayerState();
    showToast(`${mode === 'next' ? '已加入下一首播放' : '已添加到队列末尾'}：“${track.title}”`);
  }

  const loadingAlbums = new Set();
  async function queueAlbumFromPage(albumId, mode) {
    if (loadingAlbums.has(albumId)) return;
    loadingAlbums.add(albumId);
    const card = document.querySelector(`[data-album-id="${CSS.escape(String(albumId))}"]`);
    if (card) { card.setAttribute('aria-busy', 'true'); card.classList.add('is-loading'); }
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
      const label = albumLabel(payload.album?.title);
      if (mode === 'play') replaceQueue(tracks, 0, label);
      else queueTracks(tracks, mode === 'next' ? 'next' : 'append', label);
    } catch (error) { showToast('专辑加载失败，请检查网络后重试'); }
    finally {
      loadingAlbums.delete(albumId);
      if (card) { card.removeAttribute('aria-busy'); card.classList.remove('is-loading'); }
    }
  }

