import { s } from './state.js';
import { artworkForSize, formatTime, svgIcon, swapIcon } from './util.js';
import { togglePlay, playPrevious, playNext, cycleLoopMode, toggleShuffle } from './player-core.js';
import { lyrics } from './lyrics.js';

let overlay = null;
let trigger = null;
let pendingFullscreen = false;
let previousOverflow = '';
let activeLine = null;
let seeking = false;

function button(className, label, icon, action) {
  const el = document.createElement('button');
  el.type = 'button'; el.className = className;
  el.setAttribute('aria-label', label);
  el.title = label;
  el.appendChild(svgIcon(icon));
  el.addEventListener('click', action);
  return el;
}

// CSS url() needs both string and CSS escapes; never interpolate a URL into markup.
function coverCSSURL(url) {
  return `url("${url.replace(/[\\"\n\r\f]/g, char => `\\${char.charCodeAt(0).toString(16)} `)}")`;
}

export function syncFullscreen() {
  if (!overlay) return;
  const track = s.queue[s.currentIndex];
  overlay.querySelector('.fs-title').textContent = track?.title || '未在播放';
  overlay.querySelector('.fs-artist').textContent = track?.artist || '';
  overlay.querySelector('.fs-album').textContent = track?.album || '';
  const cover = overlay.querySelector('.fs-cover');
  if (track?.artwork) {
    const url = artworkForSize(track.artwork, 768);
    if (cover.getAttribute('src') !== url) cover.src = url;
    cover.hidden = false;
    overlay.style.setProperty('--fs-artwork', coverCSSURL(url));
    overlay.classList.add('has-cover');
  } else {
    cover.removeAttribute('src'); cover.hidden = true;
    overlay.style.removeProperty('--fs-artwork'); overlay.classList.remove('has-cover');
  }
  swapIcon(overlay.querySelector('.fs-play'), s.isPlaying ? 'icon-pause' : 'icon-play');
  overlay.querySelector('.fs-shuffle').classList.toggle('active', s.shuffleOn);
  overlay.querySelector('.fs-loop').classList.toggle('active', s.loopMode === 'one');
  swapIcon(overlay.querySelector('.fs-loop'), s.loopMode === 'one' ? 'icon-repeat-1' : 'icon-repeat');
  const time = s.audio?.currentTime || 0, duration = s.audio?.duration || 0;
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
    for (const line of lines) { const p = document.createElement('p'); p.textContent = line.text; content.appendChild(p); }
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
}

function closeFullscreen() {
  if (!overlay) return;
  overlay.remove(); overlay = null; activeLine = null; seeking = false;
  document.body.style.overflow = previousOverflow;
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {});
  trigger?.focus();
}

export function setupFullscreen() {
  document.getElementById('player-btn-fullscreen')?.addEventListener('click', async e => {
    trigger = e.currentTarget;
    overlay = document.createElement('div'); overlay.id = 'player-fullscreen';
    overlay.setAttribute('role', 'dialog'); overlay.setAttribute('aria-modal', 'true'); overlay.setAttribute('aria-label', '全屏播放');
    const close = button('fs-close np-icon-btn', '退出全屏', 'icon-close', closeFullscreen);
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
      if (!s.audio?.duration) return;
      s.audio.currentTime = Number(progress.value) * s.audio.duration / 100;
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
    const content = document.createElement('div'); content.className = 'fs-lyrics';
    overlay.append(close, cover, meta, progressRow, controls, content);
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
