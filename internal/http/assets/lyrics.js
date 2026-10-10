// 032 Music Server - lyrics.js
import { s } from './state.js';
import { syncFullscreen } from './fullscreen.js';
import { isCasting, mediaSeek, castToggle } from './cast.js';

export function lyrics() { return s.lyrics; }

  export async function loadLyrics(trackId) {
    s.lyrics = [];
    s.activeLyricIndex = -1;
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
        s.lyrics = data.lines;
        renderLyrics();
        syncFullscreen();
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

    s.lyrics.forEach((line, index) => {
      const p = document.createElement('p');
      p.className = 'lyric-line';
      p.setAttribute('data-index', index);
      p.setAttribute('data-time', line.timeMs || 0);
      p.textContent = line.text;
      p.addEventListener('click', () => {
        if (isCasting() && line.timeMs !== undefined) {
          mediaSeek(line.timeMs / 1000);
          if (!s.isPlaying) castToggle();
        } else if (s.audio && line.timeMs !== undefined) {
          s.audio.currentTime = line.timeMs / 1000;
          if (s.audio.paused) s.audio.play().catch(console.warn);
        }
      });
      container.appendChild(p);
    });

    scrollLyricsToActive(true);
  }

  export function updateActiveLyric(timeMs) {
    if (!s.lyrics || s.lyrics.length === 0) return;

    let targetIndex = -1;
    for (let i = 0; i < s.lyrics.length; i++) {
      if (s.lyrics[i].timeMs <= timeMs) {
        targetIndex = i;
      } else {
        break;
      }
    }

    if (targetIndex !== s.activeLyricIndex) {
      s.activeLyricIndex = targetIndex;
      const lines = document.querySelectorAll('.lyric-line');
      lines.forEach((el, idx) => {
        el.classList.toggle('active', idx === s.activeLyricIndex);
      });
      scrollLyricsToActive(false);
    }
  }

  export function scrollLyricsToActive(instant) {
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
