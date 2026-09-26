// 032 Music Server - queue.js
import { s } from './state.js';
import { svgIcon, showToast, artworkForSize } from './util.js';
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
      const albumPlayBtn = e.target.closest('.album-hero .primary-round');
      if (albumPlayBtn) {
        e.preventDefault();
        const pageTracks = extractAllTracksFromPage();
        if (pageTracks.length > 0) {
          s.queue = pageTracks;
          s.playHistory = [];
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
      if (s.currentIndex !== -1 && s.queue[s.currentIndex] && String(s.queue[s.currentIndex].id) === String(trackId)) {
        togglePlay();
      } else {
        s.queue = pageTracks;
        s.playHistory = []; // queue replaced: position history is meaningless
        playTrackAtIndex(targetIndex);
      }
    }
  }

  export function decorateTrackRows(root) {
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
    if (s.queue.length === 0 || s.currentIndex === -1) {
      s.queue.push(track);
      playTrackAtIndex(s.queue.length - 1);
      showToast(`开始播放：${track.title}`);
      return;
    }
    if (mode === 'next') s.queue.splice(s.currentIndex + 1, 0, track);
    else s.queue.push(track);
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
        s.queue = tracks;
        s.playHistory = [];
        playTrackAtIndex(0);
      } else if (!s.queue.length || s.currentIndex < 0) {
        s.queue = tracks;
        s.playHistory = [];
        playTrackAtIndex(0);
        showToast(`开始播放：${payload.album?.title || '专辑'}`);
      } else {
        if (mode === 'next') s.queue.splice(s.currentIndex + 1, 0, ...tracks);
        else s.queue.push(...tracks);
        saveState();
        emitPlayerState();
        showToast(mode === 'next' ? '已加入下一首播放' : '已添加到队列末尾');
      }
    } catch (error) { showToast('专辑加载失败，请重试'); }
    finally { loadingAlbums.delete(albumId); }
  }

