import { s } from './state.js';
import { artworkForSize, formatTime, hydrateIconSlots, svgIcon } from './util.js';
import { togglePlay, playTrackAtIndex } from './player-core.js';
import { currentTrackDetail } from './now-playing.js';
import { lyrics } from './lyrics.js';
import { castDevice, mediaTime, mediaSeek } from './cast.js';
import { renderCompactArtists } from './artist-links.js';

let stage, trigger, activeLine, previousOverflow;
let renderedLyrics, queueSignature;

function syncQueue() {
  const list = stage.querySelector('.fs-queue-list');
  const signature = `${s.currentIndex}:${s.queue.map(track => track.id).join(',')}`;
  if (signature === queueSignature) return;
  queueSignature = signature;
  stage.querySelector('.fs-queue-count').textContent = `${s.queue.length} 首`;
  list.replaceChildren();
  if (!s.queue.length) {
    const empty = document.createElement('li');
    empty.className = 'np-queue-empty';
    empty.textContent = '队列为空';
    list.append(empty);
    return;
  }
  s.queue.forEach((track, index) => {
    const row = document.createElement('li');
    const play = document.createElement('button');
    play.type = 'button';
    if (index === s.currentIndex) {
      row.classList.add('is-current');
      play.setAttribute('aria-current', 'true');
    }
    const cover = document.createElement(track.artwork ? 'img' : 'span');
    cover.className = 'queue-cover';
    if (track.artwork) {
      cover.src = artworkForSize(track.artwork, 128);
      cover.alt = '';
      cover.loading = 'lazy';
      cover.decoding = 'async';
    } else {
      cover.setAttribute('aria-hidden', 'true');
      cover.append(svgIcon('icon-tracks'));
    }
    const copy = document.createElement('span');
    const title = document.createElement('strong');
    title.textContent = track.title || '未知曲目';
    const artist = document.createElement('small');
    artist.textContent = track.artist || '';
    copy.append(title, artist);
    const duration = document.createElement('span');
    duration.className = 'fs-q-dur';
    duration.textContent = track.durationMs > 0 ? formatTime(track.durationMs / 1000) : '';
    play.append(cover, copy, duration);
    play.addEventListener('click', () => index === s.currentIndex ? togglePlay() : playTrackAtIndex(index));
    row.append(play);
    list.append(row);
  });
}

function syncLyrics() {
  const list = stage.querySelector('.fs-lyrics');
  const lines = lyrics();
  if (renderedLyrics !== lines) {
    renderedLyrics = lines;
    activeLine = null;
    list.textContent = lines.length ? '' : '暂无歌词';
    lines.forEach(line => {
      const row = document.createElement('p');
      if (line.timeMs == null) row.textContent = line.text;
      else {
        const seek = document.createElement('button');
        seek.type = 'button';
        seek.className = 'lyrics-seek';
        seek.textContent = line.text;
        seek.addEventListener('click', () => mediaSeek(line.timeMs / 1000));
        row.append(seek);
      }
      list.append(row);
    });
  }
  const index = lines.findLastIndex(line => line.timeMs <= mediaTime() * 1000);
  const next = list.children[index] || null;
  if (activeLine === next) return;
  activeLine?.classList.remove('active');
  next?.classList.add('active');
  activeLine = next;
  if (next) {
    const top = list.scrollTop + next.getBoundingClientRect().top - list.getBoundingClientRect().top;
    list.scrollTo({
      top: Math.max(0, top - (list.clientHeight - next.offsetHeight) / 2),
      behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth'
    });
  }
}

export function syncFullscreen() {
  if (!stage) return;
  const track = s.queue[s.currentIndex];
  const detail = currentTrackDetail();
  const cover = stage.querySelector('.fs-cover');
  const artwork = artworkForSize(track?.artwork, 768);
  stage.querySelector('.fs-title').textContent = track?.title || '未在播放';
  cover.hidden = !artwork;
  if (artwork && cover.getAttribute('src') !== artwork) cover.src = artwork;
  if (!artwork) cover.removeAttribute('src');
  stage.querySelector('.fs-cover-empty').hidden = !!artwork;
  renderCompactArtists(stage.querySelector('.fs-artist'), detail?.artists || track?.artists, track?.artist || '', { playerNav: true });
  const album = stage.querySelector('.fs-album');
  album.textContent = track?.album || '';
  if (detail?.albumId) album.href = `/admin/albums/${detail.albumId}`;
  else album.removeAttribute('href');
  const spec = stage.querySelector('.fs-spec');
  spec.textContent = track?.spec || track?.container?.toUpperCase() || '';
  spec.hidden = !spec.textContent;
  const cast = stage.querySelector('.fs-cast');
  const device = castDevice();
  cast.textContent = device ? `投放至 ${device.name}` : '';
  cast.hidden = !device;
  const tags = stage.querySelector('.fs-tags');
  tags.hidden = !track;
  if (track) tags.href = `/admin/tracks/${encodeURIComponent(track.id)}/tags`;
  stage.querySelector('.fs-lyrics-status').textContent = lyrics().length ? '歌词' : '暂无歌词';
  syncLyrics();
  syncQueue();
}

function closeFullscreen() {
  if (!stage) return;
  stage.remove();
  stage = null;
  renderedLyrics = activeLine = queueSignature = null;
  document.body.style.overflow = previousOverflow;
  if (document.fullscreenElement === document.documentElement) document.exitFullscreen().catch(() => {});
  if (trigger?.isConnected && !trigger.closest('[inert]')) trigger.focus();
  else document.getElementById('player-btn-fullscreen')?.focus();
}

export function setupFullscreen() {
  let requesting = false;
  async function openFullscreen(event) {
    if (stage) { closeFullscreen(); return; }
    trigger = event.currentTarget;
    stage = document.createElement('section');
    stage.id = 'player-fullscreen';
    stage.setAttribute('role', 'dialog');
    stage.setAttribute('aria-label', '全屏播放');
    stage.innerHTML = `
      <header class="fs-topbar">
        <button type="button" class="fs-close secondary-button"><span data-icon="icon-arrow-left"></span>退出全屏</button>
        <strong class="fs-lyrics-status"></strong>
        <a class="fs-tags secondary-button" data-player-nav="1"><span data-icon="icon-ai"></span>曲目 AI 标签</a>
      </header>
      <div class="fs-stage-left">
        <img class="fs-cover" alt="" hidden>
        <div class="fs-cover-empty cover-placeholder" aria-hidden="true">032</div>
        <div class="fs-meta">
          <h2 class="fs-title"></h2>
          <span class="fs-artist"></span>
          <a class="fs-album" data-player-nav="1"></a>
          <div class="fs-badges"><span class="format-pill fs-spec" hidden></span><span class="cast-badge fs-cast" hidden></span></div>
        </div>
      </div>
      <div class="fs-stage-center"><div class="fs-lyrics" aria-label="歌词"></div></div>
      <aside class="fs-queue-card">
        <div class="fs-queue-head"><strong>接下来播放</strong><small class="fs-queue-count"></small></div>
        <ol class="fs-queue-list"></ol>
      </aside>`;
    stage.querySelectorAll('[data-icon]').forEach(node => node.classList.add('icon-slot'));
    hydrateIconSlots(stage);
    const close = stage.querySelector('.fs-close');
    close.addEventListener('click', closeFullscreen);
    previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    document.body.append(stage);
    syncFullscreen();
    close.focus();
    requesting = true;
    try {
      await document.documentElement.requestFullscreen?.();
      if (!stage && document.fullscreenElement) await document.exitFullscreen();
    } catch (_) {
      // The in-page stage is also available without browser fullscreen.
    } finally {
      requesting = false;
    }
  }
  for (const id of ['player-btn-fullscreen', 'np-fullscreen']) {
    document.getElementById(id)?.addEventListener('click', openFullscreen);
  }
  document.addEventListener('keydown', event => {
    if (!stage || event.target.closest?.('dialog, .np-more-menu, #cast-menu')) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopImmediatePropagation();
      closeFullscreen();
      return;
    }
    if (event.key !== 'Tab') return;
    const focusable = [...document.querySelectorAll('#player-fullscreen :is(button, a[href]), #global-player :is(button, input, a[href], [role="slider"])')]
      .filter(element => !element.disabled && element.getClientRects().length);
    const index = focusable.indexOf(document.activeElement);
    const next = index < 0 ? (event.shiftKey ? focusable.at(-1) : focusable[0]) :
      focusable[(index + (event.shiftKey ? focusable.length - 1 : 1)) % focusable.length];
    event.preventDefault();
    next?.focus();
  }, true);
  document.addEventListener('fullscreenchange', () => {
    if (!document.fullscreenElement && !requesting) closeFullscreen();
  });
  document.addEventListener('032:pjax-applied', closeFullscreen);
  document.addEventListener('032:player-state', syncFullscreen);
  s.audio?.addEventListener('timeupdate', syncFullscreen);
}
