// 032 Music Server - player-bar.js
import { s } from './state.js';
import { hydrateIconSlots, formatTime, swapIcon, showToast } from './util.js';
import { togglePlay, playPrevious, playNext, cycleLoopMode, toggleShuffle, saveState, onTimeUpdate } from './player-core.js';
import { toggleNowPlayingPanel, emitPlayerState, syncPanelQueueCurrent } from './now-playing.js';
import { scrollLyricsToActive } from './lyrics.js';
import { isCasting, castVolume, mediaDuration, mediaTime, mediaSeek } from './cast.js';

  export function createPlayerDOM() {
    if (document.getElementById('global-player')) {
      s.audio = document.getElementById('global-audio-element');
      return;
    }

    const playerBar = document.createElement('div');
    playerBar.id = 'global-player';
    playerBar.className = 'global-player';
    playerBar.setAttribute('role', 'region');
    playerBar.setAttribute('aria-label', '播放控制');
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
            <span id="player-status" class="player-status" role="status" aria-live="polite"></span>
          </div>
          <div class="player-sub-row">
            <span id="player-format" class="format-pill" style="display:none;"></span>
            <small id="player-artist">选择一首歌曲开始播放</small>
          </div>
        </div>
        <button type="button" id="player-btn-favorite" class="np-icon-btn" title="收藏" aria-label="收藏" aria-pressed="false" disabled><span class="icon-slot" data-icon="icon-heart"></span></button>
        <button type="button" id="player-btn-more" class="np-icon-btn" title="更多" aria-label="更多" disabled><span class="icon-slot" data-icon="icon-dots"></span></button>
      </div>

      <div class="player-center">
        <div class="player-controls">
          <button id="player-btn-shuffle" class="player-ctrl-btn" title="随机播放" aria-label="随机播放" aria-pressed="false"><span class="icon-slot" data-icon="icon-shuffle"></span></button>
          <button id="player-btn-prev" class="player-ctrl-btn" title="上一首 (Alt+Left)" aria-label="上一首"><span class="icon-slot" data-icon="icon-prev"></span></button>
          <button id="player-btn-play" class="player-ctrl-btn play-main-btn" title="播放 (Space)" aria-label="播放"><span class="icon-slot" data-icon="icon-play"></span></button>
          <button id="player-btn-next" class="player-ctrl-btn" title="下一首 (Alt+Right)" aria-label="下一首"><span class="icon-slot" data-icon="icon-next"></span></button>
          <button id="player-btn-loop" class="player-ctrl-btn loop-btn" title="循环模式 (顺序播放)" aria-label="循环模式" aria-pressed="false"><span class="icon-slot" data-icon="icon-repeat"></span></button>
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
        <span id="player-cast-label" class="player-cast-label" aria-hidden="true"></span>
        <button id="player-btn-cast" class="player-tool-btn" title="投送到 Sonos / DLNA 设备" aria-label="投送到其他设备" aria-haspopup="menu" aria-expanded="false" aria-controls="cast-menu"><span class="icon-slot" data-icon="icon-cast"></span></button>
        <button id="player-btn-lyrics" class="player-tool-btn" title="歌词 (L)" aria-label="歌词" aria-expanded="false" aria-controls="lyrics-overlay"><span class="icon-slot" data-icon="icon-lyrics"></span></button>
        <button id="player-btn-queue" class="player-tool-btn" title="播放队列" aria-label="播放队列" aria-expanded="false" aria-controls="now-playing"><span class="icon-slot" data-icon="icon-queue"></span></button>
        <button id="player-btn-fullscreen" class="player-tool-btn" title="全屏" aria-label="全屏"><span class="icon-slot" data-icon="icon-fullscreen"></span></button>
        <button id="player-btn-shortcuts" class="player-tool-btn" title="快捷键说明" aria-label="快捷键说明" aria-expanded="false"><span class="icon-slot" data-icon="icon-dots"></span></button>
        <div id="player-shortcuts-help" class="player-shortcuts-help" hidden><span>Space 播放/暂停 · L 歌词 · Esc 关闭歌词 · 队列中 Alt+↑/↓ 调整顺序</span><button type="button" id="player-shortcuts-disable">关闭快捷键</button></div>
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

    s.audio = document.getElementById('global-audio-element');
    bindPlayerUIEvents();
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
        if (isCasting()) { castVolume(val); updateVolumeUI(val); return; }
        if (s.audio) {
          s.audio.volume = val;
          s.audio.muted = false;
        }
        updateVolumeUI(val);
        saveState();
      });
    }

    if (muteBtn) {
      muteBtn.addEventListener('click', () => {
        if (!s.audio) return;
        if (s.audio.muted) {
          s.audio.muted = false;
          if (s.audio.volume === 0) {
            s.audio.volume = 0.8;
            if (volumeSlider) volumeSlider.value = '0.8';
          }
        } else {
          s.audio.muted = true;
        }
        updateVolumeUI(s.audio.muted ? 0 : s.audio.volume);
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
  export function bindSeekBar(bar, applyPreview) {
    const seekToPosition = (clientX) => {
      const duration = mediaDuration();
      if (!duration) return;
      const rect = bar.getBoundingClientRect();
      const ratio = Math.max(0, Math.min(1, (clientX - rect.left) / rect.width));
      const targetTime = ratio * duration;
      // A cast seek is one renderer command, sent when the drag ends.
      if (!isCasting()) s.audio.currentTime = targetTime;
      lastTarget = targetTime;

      const curElem = document.getElementById('player-time-cur');
      if (curElem) curElem.textContent = formatTime(targetTime);
      applyPreview(`${ratio * 100}%`, `${ratio * 100}%`);
    };

    let lastTarget = null;
    const commitCast = () => { if (isCasting() && lastTarget !== null) mediaSeek(lastTarget); lastTarget = null; };
    bar.addEventListener('mousedown', (e) => {
      if (!mediaDuration()) return;
      s.isDraggingProgress = true;
      seekToPosition(e.clientX);

      const onMouseMove = (moveEvent) => {
        if (s.isDraggingProgress) {
          seekToPosition(moveEvent.clientX);
        }
      };

      const onMouseUp = () => {
        s.isDraggingProgress = false;
        commitCast();
        window.removeEventListener('mousemove', onMouseMove);
        window.removeEventListener('mouseup', onMouseUp);
      };

      window.addEventListener('mousemove', onMouseMove);
      window.addEventListener('mouseup', onMouseUp);
    });

    // Touch support for mobile/tablets
    bar.addEventListener('touchstart', (e) => {
      if (!mediaDuration() || !e.touches[0]) return;
      s.isDraggingProgress = true;
      seekToPosition(e.touches[0].clientX);

      const onTouchMove = (moveEvent) => {
        if (s.isDraggingProgress && moveEvent.touches[0]) {
          seekToPosition(moveEvent.touches[0].clientX);
        }
      };

      const onTouchEnd = () => {
        s.isDraggingProgress = false;
        commitCast();
        window.removeEventListener('touchmove', onTouchMove);
        window.removeEventListener('touchend', onTouchEnd);
      };

      window.addEventListener('touchmove', onTouchMove);
      window.addEventListener('touchend', onTouchEnd);
    }, { passive: true });
    bar.addEventListener('keydown', (e) => {
      const duration = mediaDuration();
      if (!duration) return;
      let next = mediaTime();
      if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') next -= 5;
      else if (e.key === 'ArrowRight' || e.key === 'ArrowUp') next += 5;
      else if (e.key === 'Home') next = 0;
      else if (e.key === 'End') next = duration;
      else return;
      e.preventDefault(); mediaSeek(Math.max(0, Math.min(duration, next))); onTimeUpdate();
    });
  }

  // ------------------------------------------------------------ track lists
  // Track-row interactions are delegated at the document level so PJAX
  // swaps never need re-binding; decorateTrackRows only fills in the
  // hover-play markup and accessible labels for freshly inserted content.
  export function updatePlayButtonUI(playing) {
    const playBtn = document.getElementById('player-btn-play');
    if (playBtn) {
      swapIcon(playBtn, playing ? 'icon-pause' : 'icon-play');
      playBtn.classList.toggle('is-playing', playing);
      if (!playBtn.classList.contains('is-buffering')) {
        playBtn.setAttribute('aria-label', playing ? '暂停' : '播放');
        playBtn.title = `${playing ? '暂停' : '播放'} (Space)`;
      }
    }
    const currentRowBtn = document.querySelector('.np-queue-list li.current');
    if (currentRowBtn) currentRowBtn.classList.toggle('paused', !playing);
    emitPlayerState();
  }

  // setPlayerStatus reflects what the audio element is actually doing, not
  // what was requested: 'buffering' while a started track has no audible
  // data yet, 'error' after a failed load, '' otherwise.
  export function setPlayerStatus(state) {
    const buffering = state === 'buffering';
    const playBtn = document.getElementById('player-btn-play');
    if (playBtn) {
      playBtn.classList.toggle('is-buffering', buffering);
      playBtn.setAttribute('aria-busy', String(buffering));
      if (buffering) { playBtn.setAttribute('aria-label', '正在缓冲，点按暂停'); playBtn.title = '正在缓冲…'; }
      else { playBtn.setAttribute('aria-label', s.isPlaying ? '暂停' : '播放'); playBtn.title = `${s.isPlaying ? '暂停' : '播放'} (Space)`; }
    }
    const status = document.getElementById('player-status');
    if (status) {
      const text = buffering ? '缓冲中' : state === 'error' ? '无法播放' : '';
      if (status.textContent !== text) status.textContent = text;
      status.dataset.state = state || '';
    }
    document.body.classList.toggle('player-buffering', buffering);
    document.body.classList.toggle('player-failed', state === 'error');
  }

  export function updatePlayerMetaUI(track) {
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
      // Full spec ("FLAC / 24bit / 48kHz") when known; queues saved before
      // specs existed, or cast-adopted items, fall back to the container.
      const label = (track.spec || '').trim() || (track.container || '').trim().toUpperCase();
      formatPill.textContent = label;
      formatPill.title = label;
      formatPill.style.display = label ? 'inline-block' : 'none';
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

  export function updateTrackRowsUI() {
    const currentTrack = s.queue[s.currentIndex];
    const currentId = currentTrack ? String(currentTrack.id) : null;

    document.querySelectorAll('[data-track-id]').forEach(el => {
      const trackId = el.getAttribute('data-track-id');
      const isCurrent = trackId === currentId;
      el.classList.toggle('now-playing-row', isCurrent);

      const btn = el.querySelector('.row-play-btn');
      if (btn) {
        swapIcon(btn, (isCurrent && s.isPlaying) ? 'icon-pause' : 'icon-play');
        btn.classList.toggle('playing', isCurrent && s.isPlaying);
      }
    });
    syncPanelQueueCurrent();
  }

  export function updateVolumeUI(val) {
    const muteBtn = document.getElementById('player-btn-mute');
    if (muteBtn) swapIcon(muteBtn, val === 0 ? 'icon-mute' : 'icon-volume');
  }

