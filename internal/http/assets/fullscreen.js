import { s } from './state.js';
import { artworkForSize, formatTime, svgIcon, swapIcon } from './util.js';
import { togglePlay, playPrevious, playNext, playTrackAtIndex, cycleLoopMode, toggleShuffle } from './player-core.js';
import { lyrics } from './lyrics.js';
import { mediaTime, mediaDuration, mediaSeek } from './cast.js';
import { renderCompactArtists } from './artist-links.js';

let overlay = null;
let trigger = null;
let pendingFullscreen = false;
let previousOverflow = '';
let activeLine = null;
let seeking = false;
let lastFsQueueSig = '';

function button(className, label, icon, action) {
  const el = document.createElement('button');
  el.type = 'button'; el.className = className;
  el.setAttribute('aria-label', label);
  el.title = label;
  el.appendChild(svgIcon(icon));
  el.addEventListener('click', action);
  return el;
}

function syncFullscreenQueue() {
  if (!overlay) return;
  const list = overlay.querySelector('.fs-queue-list');
  const count = overlay.querySelector('.fs-queue-count');
  if (!list) return;
  const sig = `${s.currentIndex}:${s.queue.length}:${s.queue.map(t => t.id).join(',')}`;
  if (count) count.textContent = `${s.queue.length} 首`;
  if (sig === lastFsQueueSig) return;
  lastFsQueueSig = sig;
  list.replaceChildren();
  if (!s.queue.length) {
    const empty = document.createElement('li');
    empty.className = 'np-queue-empty';
    empty.textContent = '队列为空';
    list.appendChild(empty);
    return;
  }
  s.queue.forEach((track, index) => {
    const li = document.createElement('li');
    if (index === s.currentIndex) li.classList.add('is-current');
    const btn = document.createElement('button');
    btn.type = 'button';
    const num = document.createElement('span');
    num.className = 'fs-q-num';
    num.textContent = String(index + 1);
    const meta = document.createElement('span');
    const title = document.createElement('strong');
    title.textContent = track.title || '未知曲目';
    const artist = document.createElement('small');
    artist.textContent = track.artist || '';
    meta.append(title, artist);
    const dur = document.createElement('span');
    dur.className = 'fs-q-dur';
    dur.textContent = track.durationMs > 0 ? formatTime(track.durationMs / 1000) : '';
    btn.append(num, meta, dur);
    btn.addEventListener('click', () => {
      if (index === s.currentIndex) togglePlay();
      else playTrackAtIndex(index);
    });
    li.appendChild(btn);
    list.appendChild(li);
  });
}

export function syncFullscreen() {
  if (!overlay) return;
  const track = s.queue[s.currentIndex];
  overlay.querySelector('.fs-title').textContent = track?.title || '未在播放';
  renderCompactArtists(overlay.querySelector('.fs-artist'), track?.artists, track?.artist || '', { playerNav: true });
  overlay.querySelector('.fs-album').textContent = track?.album || '';
  const cover = overlay.querySelector('.fs-cover');
  if (track?.artwork) {
    const url = artworkForSize(track.artwork, 768);
    if (cover.getAttribute('src') !== url) cover.src = url;
    cover.hidden = false;
    overlay.classList.add('has-cover');
  } else {
    cover.removeAttribute('src'); cover.hidden = true;
    overlay.classList.remove('has-cover');
  }
  swapIcon(overlay.querySelector('.fs-play'), s.isPlaying ? 'icon-pause' : 'icon-play');
  overlay.querySelector('.fs-shuffle').classList.toggle('active', s.shuffleOn);
  overlay.querySelector('.fs-loop').classList.toggle('active', s.loopMode !== 'off');
  swapIcon(overlay.querySelector('.fs-loop'), s.loopMode === 'one' ? 'icon-repeat-1' : 'icon-repeat');
  const time = mediaTime(), duration = mediaDuration();
  const progress = overlay.querySelector('.fs-progress');
  progress.setAttribute('aria-valuemax', String(Math.floor(duration)));
  progress.setAttribute('aria-valuenow', String(Math.floor(time)));
  progress.setAttribute('aria-valuetext', `${formatTime(time)} / ${formatTime(duration)}`);
  overlay.querySelector('.fs-time').textContent = formatTime(Math.round(time));
  overlay.querySelector('.fs-duration').textContent = formatTime(duration);
  if (!seeking) progress.value = duration > 0 ? String(Math.min(100, time / duration * 100)) : '0';

  const content = overlay.querySelector('.fs-lyrics');
  if (content.dataset.track !== String(track?.id || '')) {
    content.dataset.track = String(track?.id || ''); content.textContent = '';
    activeLine = null;
  }
  const lines = lyrics();
  if (content.childElementCount !== lines.length || (!lines.length && !content.textContent)) {
    content.textContent = '';
    if (!lines.length) content.textContent = '暂无歌词';
    for (const line of lines) {
      const p = document.createElement('p');
      p.textContent = line.text;
      if (line.timeMs !== undefined) {
        p.addEventListener('click', () => {
          mediaSeek(line.timeMs / 1000);
          syncFullscreen();
        });
      }
      content.appendChild(p);
    }
  }
  const index = lines.findLastIndex(line => line.timeMs <= time * 1000);
  const next = content.children[index] || null;
  if (activeLine !== next) {
    activeLine?.classList.remove('active');
    next?.classList.add('active');
    activeLine = next;
    if (next) content.scrollTo({
      top: Math.max(0, next.offsetTop - content.offsetTop - content.clientHeight / 2 + next.offsetHeight / 2),
      behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth'
    });
  }
  syncFullscreenQueue();
}

function closeFullscreen() {
  if (!overlay) return;
  overlay.remove(); overlay = null; activeLine = null; seeking = false; lastFsQueueSig = '';
  document.body.style.overflow = previousOverflow;
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  trigger?.focus();
}

export function setupFullscreen() {
  document.getElementById('player-btn-fullscreen')?.addEventListener('click', async e => {
    trigger = e.currentTarget;
    overlay = document.createElement('div'); overlay.id = 'player-fullscreen';
    overlay.setAttribute('role', 'dialog'); overlay.setAttribute('aria-modal', 'true'); overlay.setAttribute('aria-label', '全屏播放');

    const topbar = document.createElement('div'); topbar.className = 'fs-topbar';
    const brand = document.createElement('span'); brand.className = 'fs-brand'; brand.textContent = '032 · 正在播放';
    const close = button('fs-close np-icon-btn', '退出全屏', 'icon-close', closeFullscreen);
    topbar.append(brand, close);

    const left = document.createElement('div'); left.className = 'fs-stage-left';
    const cover = document.createElement('img'); cover.className = 'fs-cover'; cover.alt = ''; cover.hidden = true;
    const meta = document.createElement('div'); meta.className = 'fs-meta';
    for (const cls of ['fs-title', 'fs-artist', 'fs-album']) {
      const el = document.createElement(cls === 'fs-title' ? 'strong' : 'span'); el.className = cls; meta.appendChild(el);
    }
    const progressRow = document.createElement('div'); progressRow.className = 'fs-progress-row';
    const time = document.createElement('span'); time.className = 'fs-time';
    const progress = document.createElement('input'); progress.type = 'range'; progress.className = 'fs-progress';
    progress.min = '0'; progress.max = '100'; progress.step = '0.1'; progress.setAttribute('aria-label', '播放进度');
    progress.addEventListener('pointerdown', () => { seeking = true; });
    progress.addEventListener('pointerup', () => { seeking = false; syncFullscreen(); });
    progress.addEventListener('pointercancel', () => { seeking = false; syncFullscreen(); });
    progress.addEventListener('lostpointercapture', () => { seeking = false; syncFullscreen(); });
    progress.addEventListener('change', () => { seeking = false; syncFullscreen(); });
    progress.addEventListener('input', () => {
      if (!mediaDuration()) return;
      mediaSeek(Number(progress.value) * mediaDuration() / 100);
      syncFullscreen();
    });
    const duration = document.createElement('span'); duration.className = 'fs-duration';
    progressRow.append(time, progress, duration);
    const controls = document.createElement('div'); controls.className = 'fs-controls';
    for (const [cls, label, icon, action] of [
      ['fs-shuffle player-ctrl-btn', '随机播放', 'icon-shuffle', toggleShuffle],
      ['fs-prev player-ctrl-btn', '上一首', 'icon-prev', playPrevious],
      ['fs-play player-ctrl-btn play-main-btn', '播放 / 暂停', 'icon-play', togglePlay],
      ['fs-next player-ctrl-btn', '下一首', 'icon-next', playNext],
      ['fs-loop player-ctrl-btn', '循环模式', 'icon-repeat', cycleLoopMode]
    ]) controls.appendChild(button(cls, label, icon, () => { action(); syncFullscreen(); }));
    left.append(cover, meta, progressRow, controls);

    const center = document.createElement('div'); center.className = 'fs-stage-center';
    const content = document.createElement('div'); content.className = 'fs-lyrics';
    center.appendChild(content);

    const right = document.createElement('aside'); right.className = 'fs-stage-right';
    const qHead = document.createElement('div'); qHead.className = 'fs-queue-head';
    const qTitle = document.createElement('span'); qTitle.textContent = '接下来播放';
    const qCount = document.createElement('span'); qCount.className = 'fs-queue-count';
    qHead.append(qTitle, qCount);
    const qList = document.createElement('ol'); qList.className = 'fs-queue-list';
    right.append(qHead, qList);

    overlay.append(topbar, left, center, right);
    previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    document.body.appendChild(overlay); syncFullscreen(); close.focus();
    pendingFullscreen = true;
    try {
      await document.documentElement.requestFullscreen();
      if (!overlay && document.fullscreenElement) await document.exitFullscreen();
    } catch (_) { /* Keep the overlay when the Fullscreen API is unavailable. */ }
    finally { pendingFullscreen = false; }
  });
  document.addEventListener('keydown', e => {
    if (!overlay) return;
    if (e.key === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); closeFullscreen(); return; }
    if (e.key !== 'Tab') return;
    const focusable = Array.from(overlay.querySelectorAll('button, input'));
    const index = focusable.indexOf(document.activeElement);
    if (e.shiftKey && index <= 0) { e.preventDefault(); focusable.at(-1).focus(); }
    else if (!e.shiftKey && (index === -1 || index === focusable.length - 1)) { e.preventDefault(); focusable[0].focus(); }
  }, true);
  document.addEventListener('fullscreenchange', () => { if (!document.fullscreenElement && overlay && !pendingFullscreen) closeFullscreen(); });
  document.addEventListener('032:player-state', syncFullscreen);
  if (s.audio) s.audio.addEventListener('timeupdate', syncFullscreen);
}
