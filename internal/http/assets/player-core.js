// 032 Music Server - player-core.js
import { s } from './state.js';
import { showToast, showActionToast, formatTime, artworkForSize, apiFetch, swapIcon } from './util.js';
import { updatePlayButtonUI, updateTrackRowsUI, updatePlayerMetaUI, updateVolumeUI, setPlayerStatus } from './player-bar.js';
import { syncPanelProgress, emitPlayerState } from './now-playing.js';
import { loadLyrics, updateActiveLyric } from './lyrics.js';
import { createPlaybackReporter } from './playback-reporter.js';

  // ---------------------------------------------------- playback reporter
  // Event-driven session reporter (apiRevision 3). The reporter module is
  // pure logic; this glue feeds it the live audio element and the CSRF-aware
  // apiFetch transport.
  let reporter = null;
  export function playbackReporter() { return reporter; }

  function reporterTransport(body, { keepalive } = {}) {
    // Bounded wait: a half-open connection must not stall the serial event
    // queue forever; an abort surfaces as a network error and hits the
    // reporter's bounded retry. keepalive (pagehide) requests are left to
    // the browser.
    let signal;
    let timeoutId = null;
    if (!keepalive && typeof AbortController !== 'undefined') {
      const controller = new AbortController();
      timeoutId = setTimeout(() => controller.abort(), 10000);
      signal = controller.signal;
    }
    return apiFetch('/api/v1/playback/events', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      keepalive: !!keepalive,
      signal,
      body: JSON.stringify(body)
    }).then(async (res) => {
      let json = null;
      try { json = await res.json(); } catch (_) {}
      return { status: res.status, json };
    }).finally(() => { if (timeoutId !== null) clearTimeout(timeoutId); });
  }

  // L4: never fall back to s.queue[s.currentIndex] here — by the time an
  // old session's end is probed, currentIndex may already point at the NEW
  // track and would leak its duration into the old track's end.
  let lastAudioDurationMillis = 0;
  function currentTrackDurationMillis() {
    if (s.audio && isFinite(s.audio.duration) && s.audio.duration > 0) {
      lastAudioDurationMillis = s.audio.duration * 1000;
      return lastAudioDurationMillis;
    }
    return lastAudioDurationMillis;
  }

  export function setupPlaybackReporter() {
    if (reporter) return reporter;
    reporter = createPlaybackReporter({
      transport: reporterTransport,
      getPositionMillis: () => (s.audio ? s.audio.currentTime * 1000 : 0),
      getDurationMillis: currentTrackDurationMillis,
      onProtocolError: (info) => console.warn('playback reporter:', info.reason, info.sessionId)
    });
    // pagehide: best-effort end(client_closed); visibilitychange=hidden must
    // NOT end the session. pageshow persisted = BFCache return: the old
    // session was ended on pagehide, so rebuild it as a resume chain.
    window.addEventListener('pagehide', () => { reporter.suspend(); });
    window.addEventListener('pageshow', (e) => {
      if (!e.persisted) return;
      const track = s.queue[s.currentIndex];
      if (!track || !s.audio || !s.audio.getAttribute('src')) return;
      // 'playing' needs real evidence: unpaused AND enough data to actually
      // be audible. An unpaused-but-still-buffering element restores as
      // buffering so it cannot fabricate hasPlayed.
      const state = s.audio.paused ? 'paused' : (s.audio.readyState >= 3 ? 'playing' : 'buffering');
      reporter.restore(parseInt(track.id, 10), { initialState: state });
    });
    return reporter;
  }

  // 'playing' without an active session means playback started without an
  // explicit play path (e.g. undo-clear then play): self-heal — but never
  // while blocked on resume_invalid, which requires an explicit play action.
  function ensureReporterSession() {
    if (!reporter || reporter.hasActiveSession() || reporter.isBlocked()) return;
    const track = s.queue[s.currentIndex];
    // restore(), not play(): the same track resumes its persisted chain
    // (undo-clear / stopped sessions stay resumable) instead of opening a
    // silent no-resume chain at a high position that would count again.
    if (track) reporter.restore(parseInt(track.id, 10), { initialState: 'buffering' });
  }

  export function setupAudioElement() {
    if (!s.audio) s.audio = document.getElementById('global-audio-element');
    if (!s.audio) return;

    s.audio.addEventListener('play', () => {
      s.isPlaying = true;
      updatePlayButtonUI(true);
      updateTrackRowsUI();
      saveState();
    });

    // The reporter only trusts 'playing' (audible), never 'play'.
    s.audio.addEventListener('playing', () => {
      setPlayerStatus('');
      if (reporter) { ensureReporterSession(); reporter.notifyPlaying(); }
    });

    s.audio.addEventListener('waiting', () => {
      if (!s.audio.paused) setPlayerStatus('buffering');
      if (reporter) reporter.notifyBuffering();
    });
    s.audio.addEventListener('emptied', () => setPlayerStatus(''));
    // 'stalled' is only a network hint: playback may continue from the
    // buffer with no following 'playing' event. Report buffering only when
    // there is not enough data to be audible (M1).
    s.audio.addEventListener('stalled', () => {
      if (s.audio.readyState >= 3) return;
      if (!s.audio.paused) setPlayerStatus('buffering');
      if (reporter) reporter.notifyBuffering();
    });
    s.audio.addEventListener('seeked', () => { if (reporter) reporter.notifySeek(); });

    s.audio.addEventListener('pause', () => {
      if (!document.body.classList.contains('player-failed')) setPlayerStatus('');
      s.isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
      // Natural completion fires pause then ended in the same task: the
      // automatic pause must NOT erase the playing evidence, or a completed
      // session's end freezes priorState=paused and recovery under-counts
      // (P1). A real user pause (audio.ended === false) is unaffected.
      if (reporter && !s.audio.ended) reporter.notifyPaused();
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
      // Honest recovery evidence: the clock is advancing while audible, so
      // a session stuck in buffering (e.g. a stalled report) is really
      // playing — say so, or a real play would never count.
      if (reporter && !s.audio.paused && s.audio.readyState >= 3) {
        const dbg = reporter.getDebug();
        if (dbg.sessionId && !dbg.ended && dbg.state === 'buffering') reporter.notifyPlaying();
      }
      onTimeUpdate();
    });

    s.audio.addEventListener('ended', () => {
      onTrackEnded();
    });

    s.audio.addEventListener('error', () => {
      const track = s.queue[s.currentIndex];
      const code = s.audio.error ? s.audio.error.code : 0;
      const msg = s.audio.error ? s.audio.error.message : '';
      console.error('Audio playback error:', code, msg, s.audio.src);
      // MEDIA_ERR_ABORTED is the user (or a newer track) cancelling the load.
      if (code !== 1) {
        setPlayerStatus('error');
        s.failedTrackId = track ? String(track.id) : null;
        if (track) showPlaybackFailure(track, code);
      }
      if (reporter) reporter.fail();
      s.isPlaying = false;
      updatePlayButtonUI(false);
      updateTrackRowsUI();
      saveState();
    });
  }

  // showPlaybackFailure names the problem in plain words and offers the two
  // recoveries that keep the queue intact: retry this track or skip it.
  function showPlaybackFailure(track, code) {
    const title = `“${track.title || '未知曲目'}”`;
    const reason = code === 2 ? `网络中断，${title}没有加载完成`
      : code === 3 ? `${title}解码失败，文件可能已损坏`
      : `无法播放${title}：文件不可访问或格式不受支持`;
    const index = s.currentIndex;
    const actions = [{ label: '重试', run: () => retryTrack(index) }];
    if (index >= 0 && index < s.queue.length - 1) actions.push({ label: '跳过', run: () => playTrackAtIndex(index + 1) });
    showActionToast(reason, actions, 12000);
  }

  function retryTrack(index) {
    if (index < 0 || index >= s.queue.length || !s.audio) return;
    // Drop the failed source so playTrackAtIndex really reloads it.
    s.audio.removeAttribute('src');
    s.audio.load();
    playTrackAtIndex(index, true);
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
    // End the session before touching the audio element so the 'pause'
    // listener cannot double-report and the final position is still real.
    if (reporter) reporter.stopSession('stopped');
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

  export function playTrackAtIndex(index, skipHistoryPush, endReason) {
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
    s.failedTrackId = null;
    setPlayerStatus('');

    // Explicit new play: the reporter ends the previous session (real
    // position, given reason) and starts a fresh session for this track.
    // The new track's start carries an explicit position 0 and the NEW
    // track's own duration — never the old audio element's leftovers
    // (P1-2: the server derives initial position, Last.fm startedAt and a
    // library-unknown duration from this payload).
    if (reporter) reporter.play(parseInt(track.id, 10), { reason: endReason || 'replaced', positionMillis: 0, durationMillis: track.durationMs || 0 });

    updatePlayerMetaUI(track);
    loadLyrics(track.id);
    updateMediaSession(track);

    if (!s.audio) s.audio = document.getElementById('global-audio-element');
    if (!s.audio) return;

    const isSameSource = s.audio.src.endsWith(track.streamUrl) || s.audio.src === window.location.origin + track.streamUrl;
    if (!isSameSource) {
      s.audio.src = track.streamUrl;
      s.audio.load();
    } else {
      // Explicit re-play of the same source (album hero play, duplicate
      // queue entry): a real new play starts from position 0 whether the
      // element is paused or audible (P2-1; togglePlay — a different path —
      // still resumes in place). When already audible, play() fires no
      // 'playing' event, so say so honestly here; when paused, the real
      // 'playing' event follows on its own.
      s.audio.currentTime = 0;
      if (!s.audio.paused && reporter) reporter.notifyPlaying();
    }

    // A stale async play() callback from a previous track must never
    // overwrite the new track's state.
    const token = ++s.playToken;
    const playPromise = s.audio.play();
    // Requested is not audible: until 'playing' fires, say we are buffering.
    if (s.audio.readyState < 3) setPlayerStatus('buffering');
    if (playPromise !== undefined) {
      playPromise.then(() => {
        if (token !== s.playToken) return;
        s.isPlaying = true;
        updatePlayButtonUI(true);
        updateTrackRowsUI();
        saveState();
      }).catch(err => {
        if (token !== s.playToken) return;
        console.warn('Audio play request failed:', err);
        if (!document.body.classList.contains('player-failed')) setPlayerStatus('');
        if (reporter) reporter.notifyPaused();
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
      // An explicit user play is the only way out of a resume_invalid block.
      // It is a real NEW play: reset to 0 with the track's own duration
      // (unified with P1-2), never a silent high-position resume read.
      const track = s.queue[s.currentIndex];
      if (reporter && reporter.isBlocked() && track) {
        reporter.play(parseInt(track.id, 10), { positionMillis: 0, durationMillis: track.durationMs || 0 });
        s.audio.currentTime = 0;
      }
      const token = s.playToken;
      const playPromise = s.audio.play();
      if (playPromise !== undefined) {
        playPromise.then(() => {
          if (token !== s.playToken) return;
          s.isPlaying = true;
          updatePlayButtonUI(true);
          updateTrackRowsUI();
          saveState();
        }).catch(err => {
          if (token !== s.playToken) return;
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
          playTrackAtIndex(index, true, 'skipped');
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
    playTrackAtIndex(prevIndex, false, 'skipped');
  }

  export function playNext() {
    if (!s.audio || s.queue.length === 0) return;
    if (s.shuffleOn) { playRandomNext('skipped'); return; }
    let nextIndex = s.currentIndex + 1;
    if (nextIndex >= s.queue.length) {
      if (s.loopMode === 'all') nextIndex = 0;
      else return;
    }
    playTrackAtIndex(nextIndex, false, 'skipped');
  }

  function playRandomNext(endReason) {
    let nextIndex = s.currentIndex;
    if (s.queue.length > 1) {
      while (nextIndex === s.currentIndex) {
        nextIndex = Math.floor(Math.random() * s.queue.length);
      }
    } else {
      nextIndex = 0;
    }
    playTrackAtIndex(nextIndex, false, endReason);
  }

  function onTrackEnded() {
    const currentTrack = s.queue[s.currentIndex];
    // Natural end: end(completed) with the real final position.
    if (reporter) reporter.complete();

    if (s.loopMode === 'one') {
      // Same-track loop replay is a new play: fresh session, position 0.
      s.audio.currentTime = 0;
      if (reporter && currentTrack) reporter.play(parseInt(currentTrack.id, 10), { positionMillis: 0, durationMillis: currentTrack.durationMs || 0 });
      s.audio.play().catch(console.warn);
    } else if (s.shuffleOn && s.queue.length > 1) {
      playRandomNext();
    } else if (s.currentIndex + 1 < s.queue.length) {
      playTrackAtIndex(s.currentIndex + 1);
    } else if (s.loopMode === 'all' && s.queue.length > 0) {
      // List loop: wrap to the start (a one-track queue simply replays).
      if (s.queue.length === 1) {
        s.audio.currentTime = 0;
        if (reporter && currentTrack) reporter.play(parseInt(currentTrack.id, 10), { positionMillis: 0, durationMillis: currentTrack.durationMs || 0 });
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
  // (The legacy /api/v1/playback/timeline and /api/v1/playback/scrobble
  // endpoints were removed server-side; all playback reporting goes through
  // the session reporter above. Counting/scrobbling is server-derived.)

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
            // Refresh/restoreState of the same track resumes the persisted
            // session chain (resumedFromSessionId) instead of opening a new
            // play; the restored position is the resume position.
            if (reporter) {
              reporter.restore(parseInt(track.id, 10), {
                initialState: state.isPlaying ? 'buffering' : 'paused',
                positionMillis: Math.max(0, Math.floor((state.currentTime || 0) * 1000)),
                durationMillis: track.durationMs || 0
              });
            }
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
              const token = s.playToken;
              const playPromise = s.audio.play();
              if (playPromise !== undefined) {
                playPromise.then(() => {
                  if (token !== s.playToken) return;
                  s.isPlaying = true;
                  updatePlayButtonUI(true);
                  updateTrackRowsUI();
                }).catch(() => {
                  if (token !== s.playToken) return;
                  // Autoplay policy prevented immediate playback until user clicks
                  if (reporter) reporter.notifyPaused();
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
