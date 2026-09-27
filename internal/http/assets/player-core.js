// 032 Music Server - player-core.js
import { s } from './state.js';
import { showToast, formatTime, artworkForSize, apiFetch, swapIcon } from './util.js';
import { updatePlayButtonUI, updateTrackRowsUI, updatePlayerMetaUI, updateVolumeUI } from './player-bar.js';
import { syncPanelProgress, emitPlayerState } from './now-playing.js';
import { loadLyrics, updateActiveLyric } from './lyrics.js';

  export function setupAudioElement() {
    if (!s.audio) s.audio = document.getElementById('global-audio-element');
    if (!s.audio) return;

    s.audio.addEventListener('play', () => {
      s.isPlaying = true;
      updatePlayButtonUI(true);
      updateTrackRowsUI();
      saveState();
    });

    s.audio.addEventListener('pause', () => {
      s.isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
    });

    s.audio.addEventListener('loadedmetadata', () => {
      const dur = s.audio.duration || 0;
      const totalElem = document.getElementById('player-time-total');
      if (totalElem && dur > 0) totalElem.textContent = formatTime(dur);
      syncPanelProgress();
    });

    s.audio.addEventListener('durationchange', () => {
      const dur = s.audio.duration || 0;
      const totalElem = document.getElementById('player-time-total');
      if (totalElem && dur > 0) totalElem.textContent = formatTime(dur);
    });

    s.audio.addEventListener('timeupdate', () => {
      onTimeUpdate();
    });

    s.audio.addEventListener('ended', () => {
      onTrackEnded();
    });

    s.audio.addEventListener('error', () => {
      const track = s.queue[s.currentIndex];
      const code = s.audio.error ? s.audio.error.code : 'UNKNOWN';
      const msg = s.audio.error ? s.audio.error.message : '';
      console.error('Audio playback error:', code, msg, s.audio.src);
      if (track) {
        showToast(`播放失败 (${code}): ${track.title || '未知曲目'}`);
      }
      s.isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
    });
  }

  // Queue edits preserve the current track by position, except removal of that track.
  export function moveQueueTrack(from, to) {
    if (from < 0 || from >= s.queue.length || to < 0 || to >= s.queue.length || from === to) return false;
    const [track] = s.queue.splice(from, 1);
    s.queue.splice(to, 0, track);
    if (s.currentIndex === from) s.currentIndex = to;
    else if (from < s.currentIndex && to >= s.currentIndex) s.currentIndex--;
    else if (from > s.currentIndex && to <= s.currentIndex) s.currentIndex++;
    saveState();
    emitPlayerState();
    return true;
  }

  export function removeQueueTrack(index) {
    if (index < 0 || index >= s.queue.length) return;
    const wasCurrent = index === s.currentIndex;
    s.queue.splice(index, 1);
    if (wasCurrent) {
      s.currentIndex = -1;
      if (s.queue.length) {
        let next = index;
        if (s.shuffleOn) next = Math.floor(Math.random() * s.queue.length);
        else if (next >= s.queue.length && s.loopMode === 'all') next = 0;
        if (next < s.queue.length) playTrackAtIndex(next, true);
        else stopQueuePlayback();
      } else stopQueuePlayback();
    } else if (index < s.currentIndex) s.currentIndex--;
    saveState();
    emitPlayerState();
  }

  export function stopQueuePlayback() {
    if (s.audio) {
      s.audio.pause();
      s.audio.removeAttribute('src');
      s.audio.load();
    }
    s.isPlaying = false;
    s.currentIndex = -1;
    updatePlayerMetaUI({ title: '未在播放', artist: '', album: '', container: '' });
    const title = document.getElementById('player-title'); if (title) title.title = '未在播放';
    const format = document.getElementById('player-format');
    if (format) format.style.display = 'none';
    const artist = document.getElementById('player-artist');
    if (artist) artist.textContent = '选择一首歌曲开始播放';
    for (const id of ['player-time-cur', 'player-time-total']) {
      const el = document.getElementById(id); if (el) el.textContent = '0:00';
    }
    for (const id of ['player-progress-fill', 'player-progress-thumb', 'np-progress-fill']) {
      const el = document.getElementById(id); if (el) el.style[id === 'player-progress-thumb' ? 'left' : 'width'] = '0';
    }
    for (const id of ['np-time-cur', 'np-time-total']) {
      const el = document.getElementById(id); if (el) el.textContent = '0:00';
    }
    for (const id of ['player-progress-bar', 'np-progress-bar']) {
      const el = document.getElementById(id);
      if (el) { el.setAttribute('aria-valuenow', '0'); el.setAttribute('aria-valuemax', '0'); }
    }
    updatePlayButtonUI(false);
    updateTrackRowsUI();
    saveState();
    emitPlayerState();
  }

  export function playTrackAtIndex(index, skipHistoryPush) {
    if (index < 0 || index >= s.queue.length) return;
    // Shuffle mode keeps a back-stack so 上一首 retraces what was heard
    // instead of walking the queue backwards.
    if (!skipHistoryPush && s.currentIndex !== -1 && index !== s.currentIndex && s.queue[s.currentIndex]) {
      s.playHistory.push(String(s.queue[s.currentIndex].id));
      if (s.playHistory.length > 100) s.playHistory.shift();
    }
    s.currentIndex = index;
    const track = s.queue[s.currentIndex];
    if (!track) return;
    // Every explicit play is a new listen that may be scrobbled again; the
    // server discards a repeated report of the same playback.
    s.lastScrobbledTrackId = null;

    updatePlayerMetaUI(track);
    loadLyrics(track.id);
    updateMediaSession(track);

    if (!s.audio) s.audio = document.getElementById('global-audio-element');
    if (!s.audio) return;

    const isSameSource = s.audio.src.endsWith(track.streamUrl) || s.audio.src === window.location.origin + track.streamUrl;
    if (!isSameSource) {
      s.audio.src = track.streamUrl;
      s.audio.load();
    }

    const playPromise = s.audio.play();
    if (playPromise !== undefined) {
      playPromise.then(() => {
        s.isPlaying = true;
        updatePlayButtonUI(true);
        updateTrackRowsUI();
        saveState();
      }).catch(err => {
        console.warn('Audio play request failed:', err);
        s.isPlaying = false;
        updatePlayButtonUI(false);
        updateTrackRowsUI();
      });
    }
  }

  export function togglePlay() {
    if (!s.audio) return;
    if (!s.audio.src && s.queue.length > 0) {
      playTrackAtIndex(s.currentIndex >= 0 ? s.currentIndex : 0);
      return;
    }

    if (s.audio.paused) {
      const playPromise = s.audio.play();
      if (playPromise !== undefined) {
        playPromise.then(() => {
          s.isPlaying = true;
          updatePlayButtonUI(true);
          updateTrackRowsUI();
          saveState();
        }).catch(err => {
          console.warn('Audio play failed on toggle:', err);
        });
      }
    } else {
      s.audio.pause();
      s.isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
    }
  }

  export function playPrevious() {
    if (!s.audio || s.queue.length === 0) return;
    if (s.audio.currentTime > 3) {
      s.audio.currentTime = 0;
      return;
    }
    if (s.shuffleOn && s.playHistory.length > 0) {
      // Pop ids until one still exists in the current queue; entries left
      // over from a replaced queue are skipped.
      while (s.playHistory.length > 0) {
        const id = s.playHistory.pop();
        const index = s.queue.findIndex((t) => String(t.id) === id);
        if (index !== -1 && index !== s.currentIndex) {
          playTrackAtIndex(index, true);
          return;
        }
      }
    }
    let prevIndex = s.currentIndex - 1;
    if (prevIndex < 0) {
      // Only list-loop wraps backwards; otherwise restart the first track.
      if (s.loopMode === 'all') prevIndex = s.queue.length - 1;
      else { s.audio.currentTime = 0; return; }
    }
    playTrackAtIndex(prevIndex);
  }

  export function playNext() {
    if (!s.audio || s.queue.length === 0) return;
    if (s.shuffleOn) { playRandomNext(); return; }
    let nextIndex = s.currentIndex + 1;
    if (nextIndex >= s.queue.length) {
      if (s.loopMode === 'all') nextIndex = 0;
      else return;
    }
    playTrackAtIndex(nextIndex);
  }

  function playRandomNext() {
    let nextIndex = s.currentIndex;
    if (s.queue.length > 1) {
      while (nextIndex === s.currentIndex) {
        nextIndex = Math.floor(Math.random() * s.queue.length);
      }
    } else {
      nextIndex = 0;
    }
    playTrackAtIndex(nextIndex);
  }

  function onTrackEnded() {
    const currentTrack = s.queue[s.currentIndex];
    if (currentTrack) scrobbleTrack(currentTrack.id);

    if (s.loopMode === 'one') {
      s.lastScrobbledTrackId = null;
      s.audio.currentTime = 0;
      s.audio.play().catch(console.warn);
    } else if (s.shuffleOn && s.queue.length > 1) {
      playRandomNext();
    } else if (s.currentIndex + 1 < s.queue.length) {
      playTrackAtIndex(s.currentIndex + 1);
    } else if (s.loopMode === 'all' && s.queue.length > 0) {
      // List loop: wrap to the start (a one-track queue simply replays).
      if (s.queue.length === 1) {
        s.audio.currentTime = 0;
        s.audio.play().catch(console.warn);
      } else {
        playTrackAtIndex(0);
      }
    } else {
      // Default sequential mode: the queue has been played to the end.
      s.isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
      emitPlayerState();
    }
  }

  // Loop modes: 'off' (play queue once, default) -> 'all' (list loop) -> 'one'.
  const LOOP_MODES = ['off', 'all', 'one'];
  const LOOP_TITLES = { off: '循环模式 (顺序播放)', all: '循环模式 (列表循环)', one: '循环模式 (单曲循环)' };

  export function syncLoopButton() {
    const loopBtn = document.getElementById('player-btn-loop');
    if (!loopBtn) return;
    const active = s.loopMode !== 'off';
    swapIcon(loopBtn, s.loopMode === 'one' ? 'icon-repeat-1' : 'icon-repeat');
    loopBtn.title = LOOP_TITLES[s.loopMode] || LOOP_TITLES.off;
    loopBtn.classList.toggle('active', active);
    loopBtn.setAttribute('aria-pressed', String(active));
  }

  export function cycleLoopMode() {
    const i = LOOP_MODES.indexOf(s.loopMode);
    s.loopMode = LOOP_MODES[(i + 1) % LOOP_MODES.length];
    syncLoopButton();
    saveState();
    emitPlayerState();
  }

  export function toggleShuffle() {
    s.shuffleOn = !s.shuffleOn;
    const shuffleBtn = document.getElementById('player-btn-shuffle');
    if (shuffleBtn) {
      shuffleBtn.classList.toggle('active', s.shuffleOn);
      shuffleBtn.setAttribute('aria-pressed', String(s.shuffleOn));
      shuffleBtn.title = s.shuffleOn ? '随机播放 (开)' : '随机播放';
    }
    saveState();
    emitPlayerState();
  }

  export function onTimeUpdate() {
    if (!s.audio) return;
    const curTime = s.audio.currentTime || 0;
    const durTime = s.audio.duration || 0;

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

    if (!s.isDraggingProgress && durTime > 0) {
      const percent = Math.min(100, Math.max(0, (curTime / durTime) * 100));
      if (fillElem) fillElem.style.width = `${percent}%`;
      if (thumbElem) thumbElem.style.left = `${percent}%`;
    }

    syncPanelProgress();
    updateActiveLyric(curTime * 1000);

    const currentTrack = s.queue[s.currentIndex];
    if (currentTrack && durTime > 10 && curTime / durTime >= 0.5 && s.lastScrobbledTrackId !== currentTrack.id) {
      scrobbleTrack(currentTrack.id);
    }
  }

  export function updateMediaSession(track) {
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
        if (details.seekTime !== undefined && s.audio && s.audio.duration) {
          s.audio.currentTime = details.seekTime;
        }
      });
    } catch (_) {}
  }

  // -------------------------------------------------------- now-playing panel
  export function reportTimelineProgress() {
    if (!s.audio || !s.isPlaying || s.currentIndex === -1 || !s.queue[s.currentIndex]) return;
    saveState();
    const track = s.queue[s.currentIndex];
    const positionMs = Math.floor((s.audio.currentTime || 0) * 1000);
    const durationMs = Math.floor((s.audio.duration || 0) * 1000) || 0;

    apiFetch('/api/v1/playback/timeline', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      keepalive: true,
      body: JSON.stringify({
        trackId: parseInt(track.id, 10),
        positionMillis: positionMs,
        durationMillis: durationMs,
        state: s.isPlaying ? 'playing' : 'paused'
      })
    }).catch(() => {});
  }

  function scrobbleTrack(trackId) {
    if (!trackId || s.lastScrobbledTrackId === trackId) return;
    s.lastScrobbledTrackId = trackId;

    const positionMs = s.audio ? Math.floor((s.audio.currentTime || 0) * 1000) : 0;
    const durationMs = s.audio ? Math.floor((s.audio.duration || 0) * 1000) : 0;

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
  export function saveState() {
    try {
      const state = {
        queue: s.queue,
        currentIndex: s.currentIndex,
        currentTime: s.audio ? s.audio.currentTime : 0,
        isPlaying: s.isPlaying,
        loopMode: s.loopMode,
        shuffle: s.shuffleOn,
        volume: s.audio ? s.audio.volume : 1
      };
      sessionStorage.setItem('032_player_state', JSON.stringify(state));
    } catch (_) {}
  }

  export function restoreState() {
    try {
      const raw = sessionStorage.getItem('032_player_state');
      if (!raw) return;
      const state = JSON.parse(raw);
      if (state.queue && state.queue.length > 0) {
        s.queue = state.queue;
        s.currentIndex = state.currentIndex === -1 ? -1 : (state.currentIndex >= 0 ? state.currentIndex : 0);
        // Legacy snapshots may still carry the combined loopMode 'shuffle'.
        if (state.loopMode === 'shuffle') {
          s.loopMode = 'off';
          s.shuffleOn = true;
        } else {
          s.loopMode = LOOP_MODES.includes(state.loopMode) ? state.loopMode : 'off';
          s.shuffleOn = !!state.shuffle;
        }

        // [P2-3] keep icon/title/aria-pressed in sync on restore.
        syncLoopButton();
        const shuffleBtn = document.getElementById('player-btn-shuffle');
        if (shuffleBtn) {
          shuffleBtn.classList.toggle('active', s.shuffleOn);
          shuffleBtn.setAttribute('aria-pressed', String(s.shuffleOn));
          shuffleBtn.title = s.shuffleOn ? '随机播放 (开)' : '随机播放';
        }

        const track = s.queue[s.currentIndex];
        if (track) {
          updatePlayerMetaUI(track);
          if (s.audio) {
            s.audio.src = track.streamUrl;
            s.audio.load();
            if (state.volume !== undefined) {
              s.audio.volume = state.volume;
              const slider = document.getElementById('player-volume-slider');
              if (slider) slider.value = state.volume;
              updateVolumeUI(state.volume);
            }

            if (state.currentTime > 0) {
              const applyTime = () => {
                try {
                  if (state.currentTime > 0) {
                    s.audio.currentTime = state.currentTime;
                  }
                } catch (_) {}
              };
              if (s.audio.readyState >= 1) {
                applyTime();
              } else {
                s.audio.addEventListener('loadedmetadata', applyTime, { once: true });
              }
            }

            if (state.isPlaying) {
              const playPromise = s.audio.play();
              if (playPromise !== undefined) {
                playPromise.then(() => {
                  s.isPlaying = true;
                  updatePlayButtonUI(true);
                  updateTrackRowsUI();
                }).catch(() => {
                  // Autoplay policy prevented immediate playback until user clicks
                  s.isPlaying = false;
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
