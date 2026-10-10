// 032 Music Server - cast.js
// Pushes the web player's queue to a Sonos / DLNA renderer through the
// server (/api/v1/cast). Sonos plays its own device queue, so playback
// continues with this tab closed; the player UI only mirrors its status.
import { s } from './state.js';
import { apiFetch, showToast, artworkForSize } from './util.js';
import { updatePlayButtonUI, updatePlayerMetaUI, updateTrackRowsUI, setPlayerStatus, updateVolumeUI } from './player-bar.js';
import { emitPlayerState } from './now-playing.js';
import { loadLyrics } from './lyrics.js';
import { onTimeUpdate, updateMediaSession, saveState, playbackReporter } from './player-core.js';
import { syncFullscreen } from './fullscreen.js';

const STORAGE_KEY = '032_cast_device';
const c = {
  device: null,     // { id, name, kind }
  status: null,     // last /status payload
  statusAt: 0,      // performance.now() of status
  pushed: [],       // track ids the renderer queue holds, in order
  chain: Promise.resolve(),
  pending: 0,       // queued commands; polling does not override optimistic UI meanwhile
  timer: null,
  syncScheduled: false,
  adoptedAt: 0,
  volumeTimer: null,
};

export function isCasting() { return !!c.device; }
export function castDevice() { return c.device; }

const api = (id, path) => `/api/v1/cast/devices/${encodeURIComponent(id)}${path}`;

async function request(url, body) {
  const res = await apiFetch(url, body === undefined ? {} : {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
  });
  let json = null;
  try { json = await res.json(); } catch (_) {}
  if (!res.ok) {
    const err = new Error(json?.error?.message || `HTTP ${res.status}`);
    err.status = res.status; err.code = json?.error?.code;
    throw err;
  }
  return json;
}

// run serializes renderer commands: queue edits must reach the speaker in
// the order the user made them.
function run(task) {
  c.pending++;
  const next = c.chain.then(task).catch((err) => {
    if (err && err.code !== 'unsupported') showToast(`投送失败：${err.message || err}`);
  }).finally(() => { c.pending--; });
  c.chain = next;
  return next;
}

// ------------------------------------------------------------ media clock
// Position helpers used by the player UI in both modes.
export function mediaTime() {
  if (!c.device) return s.audio ? s.audio.currentTime || 0 : 0;
  const st = c.status;
  if (!st) return 0;
  let ms = st.positionMs || 0;
  if (st.state === 'playing') ms += performance.now() - c.statusAt;
  const dur = mediaDuration() * 1000;
  return Math.max(0, dur > 0 ? Math.min(ms, dur) : ms) / 1000;
}

export function mediaDuration() {
  if (!c.device) return s.audio && isFinite(s.audio.duration) ? s.audio.duration || 0 : 0;
  const st = c.status;
  const track = s.queue[s.currentIndex];
  return ((st && st.durationMs) || (track && track.durationMs) || 0) / 1000;
}

export function mediaSeek(seconds) {
  if (!c.device) { if (s.audio) s.audio.currentTime = seconds; return; }
  const ms = Math.max(0, Math.round(seconds * 1000));
  if (c.status) { c.status = { ...c.status, positionMs: ms }; c.statusAt = performance.now(); }
  onTimeUpdate();
  run(() => control({ action: 'seek', positionMs: ms }));
}

// ------------------------------------------------------------ queue sync
const ids = () => s.queue.map((t) => Number(t.id));
const same = (a, b) => a.length === b.length && a.every((v, i) => v === b[i]);

// diffQueue reduces a queue change to one renderer edit when possible.
export function diffQueue(old, next) {
  if (same(old, next)) return { type: 'equal' };
  let i = 0;
  while (i < old.length && i < next.length && old[i] === next[i]) i++;
  const grow = next.length - old.length;
  if (grow > 0 && same(next.slice(i + grow), old.slice(i))) return { type: 'insert', at: i, ids: next.slice(i, i + grow) };
  if (grow === -1 && same(next.slice(i), old.slice(i + 1))) return { type: 'remove', index: i };
  if (grow === 0) {
    let j = old.length - 1;
    while (j > i && old[j] === next[j]) j--;
    if (same(next.slice(i, j + 1), [...old.slice(i + 1, j + 1), old[i]])) return { type: 'move', from: i, to: j };
    if (same(next.slice(i, j + 1), [old[j], ...old.slice(i, j)])) return { type: 'move', from: j, to: i };
  }
  return { type: 'replace' };
}

// syncQueue pushes the difference between the renderer queue and s.queue.
// Returns true when the whole queue was replaced (and playback restarted
// at opts.startIndex).
async function syncQueue(opts = {}) {
  if (!c.device) return false;
  const next = ids();
  const d = diffQueue(c.pushed, next);
  const id = c.device.id;
  if (d.type === 'equal') return false;
  if (d.type === 'replace' || c.pushed.length === 0) {
    const start = opts.startIndex ?? Math.max(0, s.currentIndex);
    const st = await request(api(id, '/queue'), {
      mode: 'replace', trackIds: next, startIndex: start,
      positionMs: Math.round((opts.positionSeconds || 0) * 1000),
      play: opts.play ?? true
    });
    c.pushed = next;
    applyStatus(st, true);
    return true;
  }
  if (d.type === 'insert') {
    await request(api(id, '/queue'), d.at >= c.pushed.length
      ? { mode: 'append', trackIds: d.ids }
      : { mode: 'insert', insertAt: d.at, trackIds: d.ids });
  } else if (d.type === 'remove') {
    await request(api(id, '/queue/remove'), { index: d.index });
  } else if (d.type === 'move') {
    await request(api(id, '/queue/move'), { from: d.from, to: d.to });
  }
  c.pushed = next;
  return false;
}

// castQueueChanged is called whenever the player persists its state; the
// diff makes it a no-op unless the queue really changed.
export function castQueueChanged() {
  if (!c.device || c.syncScheduled) return;
  c.syncScheduled = true;
  setTimeout(() => {
    c.syncScheduled = false;
    if (c.device && !same(c.pushed, ids())) run(() => syncQueue({ play: s.isPlaying }));
  }, 0);
}

async function control(body) {
  if (!c.device) return null;
  const st = await request(api(c.device.id, '/control'), body);
  applyStatus(st, true);
  return st;
}

// ------------------------------------------------------------ commands
export function castPlayIndex(index) {
  const track = s.queue[index];
  if (!track) return;
  // Optimistic UI: the renderer confirms through the next status.
  s.currentIndex = index;
  s.isPlaying = true;
  c.status = { ...(c.status || {}), index, trackId: Number(track.id), positionMs: 0, durationMs: track.durationMs || 0, state: 'buffering' };
  c.statusAt = performance.now();
  showTrack(track);
  updatePlayButtonUI(true);
  setPlayerStatus('buffering');
  run(async () => {
    const replaced = await syncQueue({ startIndex: index, play: true });
    if (!replaced) await control({ action: 'playIndex', index });
  });
}

export function castToggle() {
  const playing = c.status && (c.status.state === 'playing' || c.status.state === 'buffering');
  if (!playing && (!c.status || c.status.index < 0 || !c.status.queueActive)) {
    if (s.queue.length) castPlayIndex(Math.max(0, s.currentIndex));
    return;
  }
  s.isPlaying = !playing;
  if (c.status) { c.status = { ...c.status, positionMs: mediaTime() * 1000, state: playing ? 'paused' : 'playing' }; c.statusAt = performance.now(); }
  updatePlayButtonUI(s.isPlaying);
  updateTrackRowsUI();
  run(() => control({ action: playing ? 'pause' : 'play' }));
}

export function castNext() {
  if (!s.queue.length) return;
  if (s.shuffleOn || c.device?.kind !== 'sonos') { run(() => control({ action: 'next' })); return; }
  let next = s.currentIndex + 1;
  if (next >= s.queue.length) { if (s.loopMode !== 'all') return; next = 0; }
  castPlayIndex(next);
}

export function castPrevious() {
  if (!s.queue.length) return;
  if (mediaTime() > 3) { mediaSeek(0); return; }
  if (s.shuffleOn) { run(() => control({ action: 'previous' })); return; }
  let prev = s.currentIndex - 1;
  if (prev < 0) { if (s.loopMode !== 'all') { mediaSeek(0); return; } prev = s.queue.length - 1; }
  castPlayIndex(prev);
}

export function castStop() {
  run(() => control({ action: 'stop' }));
}

export function castPlayMode() {
  if (!c.device) return;
  run(() => control({ action: 'playMode', loop: s.loopMode, shuffle: s.shuffleOn }));
}

export function castVolume(value) {
  if (!c.device) return;
  clearTimeout(c.volumeTimer);
  c.volumeTimer = setTimeout(() => run(() => control({ action: 'volume', volume: Math.round(value * 100) })), 200);
}

// ------------------------------------------------------------ status
function showTrack(track) {
  updatePlayerMetaUI(track);
  loadLyrics(track.id);
  updateMediaSession(track);
  updateTrackRowsUI();
  emitPlayerState();
}

function applyStatus(st, fromCommand) {
  if (!st || !c.device) return;
  // While commands are in flight a poll may predate them: keep optimistic UI.
  if (!fromCommand && c.pending > 0) return;
  c.status = st;
  c.statusAt = performance.now();
  let index = -1;
  if (st.trackId) {
    const atIndex = s.queue[st.index];
    index = atIndex && Number(atIndex.id) === st.trackId ? st.index : s.queue.findIndex((t) => Number(t.id) === st.trackId);
  }
  if (index !== -1 && index !== s.currentIndex) {
    s.currentIndex = index;
    showTrack(s.queue[index]);
    saveState();
  }
  const playing = st.state === 'playing' || st.state === 'buffering';
  if (playing !== s.isPlaying) {
    s.isPlaying = playing;
    updatePlayButtonUI(playing);
    updateTrackRowsUI();
  }
  setPlayerStatus(st.state === 'buffering' ? 'buffering' : '');
  if (typeof st.volume === 'number') {
    const slider = document.getElementById('player-volume-slider');
    if (slider) slider.value = String(st.volume / 100);
    updateVolumeUI(st.volume / 100);
  }
  onTimeUpdate();
  // Someone else (another client, the Sonos app) rebuilt the queue.
  if (!fromCommand && st.queueActive && st.queueLength !== c.pushed.length && performance.now() - c.adoptedAt > 10000) {
    c.adoptedAt = performance.now();
    run(adoptDeviceQueue);
  }
}

async function poll() {
  if (!c.device) return;
  try {
    const st = await request(api(c.device.id, '/status'));
    applyStatus(st, false);
  } catch (err) {
    if (err.status === 404) { showToast(`找不到投送设备“${c.device.name}”`); detach(false); return; }
  }
  if (c.device) c.timer = setTimeout(poll, document.hidden ? 5000 : 1000);
}

// A clock tick between polls keeps the progress bar moving smoothly.
setInterval(() => { if (c.device && c.status && c.status.state === 'playing') { onTimeUpdate(); syncFullscreen(); } }, 250);

async function adoptDeviceQueue() {
  if (!c.device) return;
  const payload = await request(api(c.device.id, '/queue'));
  const items = (payload.items || []).filter((it) => it.trackId > 0);
  if (!items.length) return;
  s.queue = items.map((it) => ({
    id: String(it.trackId), title: it.title, artist: it.artist || '', album: it.album || '',
    artwork: it.artworkUrl ? artworkForSize(it.artworkUrl, 256) : '', container: it.container || '',
    durationMs: it.durationMs || 0, streamUrl: `/api/v1/tracks/${it.trackId}/stream`
  }));
  s.playHistory = [];
  // Foreign items (radio) were dropped, so pushed mirrors only when complete.
  c.pushed = items.length === payload.items.length ? ids() : [];
  s.currentIndex = -1;
  applyStatus(payload.status, true);
  if (s.currentIndex === -1 && s.queue.length) { s.currentIndex = 0; showTrack(s.queue[0]); }
  saveState();
  emitPlayerState();
}

// ------------------------------------------------------------ attach / detach
function setCastUI() {
  document.body.classList.toggle('is-casting', !!c.device);
  const btn = document.getElementById('player-btn-cast');
  const label = document.getElementById('player-cast-label');
  if (btn) {
    btn.classList.toggle('active', !!c.device);
    btn.title = c.device ? `正在投送到 ${c.device.name}` : '投送到 Sonos / DLNA 设备';
  }
  if (label) label.textContent = c.device ? c.device.name : '';
}

// attach switches output to a renderer. With a local queue the current
// track continues there at the same position; otherwise the renderer's own
// queue is adopted.
export async function attach(device, { push = true } = {}) {
  if (c.device && c.device.id === device.id) return;
  if (c.device) await detach(true);
  const position = s.audio ? s.audio.currentTime || 0 : 0;
  if (s.audio && !s.audio.paused) s.audio.pause();
  const reporter = playbackReporter();
  if (reporter && push) reporter.stopSession('stopped');
  c.device = { id: device.id, name: device.name, kind: device.kind };
  c.pushed = [];
  c.status = null;
  sessionStorage.setItem(STORAGE_KEY, JSON.stringify(c.device));
  setCastUI();
  if (push && s.queue.length && s.currentIndex >= 0) {
    s.isPlaying = true;
    run(async () => {
      await syncQueue({ startIndex: s.currentIndex, positionSeconds: position, play: true });
      await control({ action: 'playMode', loop: s.loopMode, shuffle: s.shuffleOn });
    });
    showToast(`已投送到 ${device.name}`);
  } else {
    run(adoptDeviceQueue);
  }
  run(async () => applyStatus(await request(api(device.id, '/status?volume=1')), true));
  clearTimeout(c.timer);
  c.timer = setTimeout(poll, 1000);
}

// detach returns to the local player at the renderer's current position.
// The renderer is paused unless keepPlaying (switching between renderers
// leaves the old one alone only when asked).
export async function detach(pauseDevice = true) {
  if (!c.device) return;
  const device = c.device;
  const position = mediaTime();
  clearTimeout(c.timer);
  if (pauseDevice) {
    try { await c.chain; await request(api(device.id, '/control'), { action: 'pause' }); } catch (_) {}
  }
  c.device = null;
  c.status = null;
  c.pushed = [];
  sessionStorage.removeItem(STORAGE_KEY);
  setCastUI();
  s.isPlaying = false;
  updatePlayButtonUI(false);
  setPlayerStatus('');
  const track = s.queue[s.currentIndex];
  if (track && s.audio) {
    s.audio.src = track.streamUrl;
    s.audio.load();
    const seek = () => { try { s.audio.currentTime = position; } catch (_) {} };
    if (s.audio.readyState >= 1) seek(); else s.audio.addEventListener('loadedmetadata', seek, { once: true });
  }
  updateTrackRowsUI();
  saveState();
  showToast('已切换回本机播放');
}

// restoreCast resumes mirroring after a reload / PJAX full load.
export function restoreCast() {
  try {
    const saved = JSON.parse(sessionStorage.getItem(STORAGE_KEY) || 'null');
    if (!saved || !saved.id) return false;
    c.device = saved;
    setCastUI();
    run(async () => {
      const payload = await request(api(saved.id, '/queue'));
      const known = (payload.items || []).map((it) => it.trackId);
      if (known.length && same(known, ids())) {
        c.pushed = known;
        applyStatus(payload.status, true);
      } else {
        await adoptDeviceQueue();
      }
    });
    c.timer = setTimeout(poll, 1000);
    return true;
  } catch (_) {
    return false;
  }
}

// ------------------------------------------------------------ device menu
let menuBusy = false;
async function renderMenu(menu, refresh) {
  if (menuBusy) return;
  menuBusy = true;
  const list = menu.querySelector('ul');
  const hint = menu.querySelector('.cast-menu-empty');
  hint.hidden = false;
  hint.textContent = '正在搜索局域网设备…';
  try {
    const res = await request(`/api/v1/cast/devices${refresh ? '?refresh=1' : ''}`);
    list.textContent = '';
    const entries = [{ id: '', name: '本机（浏览器）', kind: 'local' }, ...(res.devices || [])];
    for (const d of entries) {
      const li = document.createElement('li');
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.setAttribute('role', 'menuitemradio');
      const active = d.id ? c.device?.id === d.id : !c.device;
      btn.setAttribute('aria-checked', String(active));
      const name = document.createElement('span');
      name.textContent = d.name || d.id;
      const meta = document.createElement('small');
      meta.textContent = d.kind === 'sonos' ? `Sonos${d.memberCount > 1 ? ` · ${d.memberCount} 个房间` : ''}` : d.kind === 'dlna' ? (d.model || 'DLNA') : '';
      btn.append(name, meta);
      btn.addEventListener('click', () => {
        menu.hidden = true;
        document.getElementById('player-btn-cast')?.setAttribute('aria-expanded', 'false');
        if (d.id) attach(d); else detach(true);
      });
      li.appendChild(btn);
      list.appendChild(li);
    }
    hint.hidden = (res.devices || []).length > 0;
    hint.textContent = res.available === false ? '服务器未启用投送' : '未发现 Sonos / DLNA 设备，请确认服务器与音箱在同一局域网';
  } catch (err) {
    hint.hidden = false;
    hint.textContent = `设备搜索失败：${err.message}`;
  } finally {
    menuBusy = false;
  }
}

export function setupCast() {
  const btn = document.getElementById('player-btn-cast');
  const player = document.getElementById('global-player');
  if (!btn || !player || document.getElementById('cast-menu')) return;
  const menu = document.createElement('div');
  menu.id = 'cast-menu';
  menu.className = 'cast-menu';
  menu.hidden = true;
  menu.setAttribute('role', 'menu');
  menu.setAttribute('aria-label', '选择播放设备');
  menu.innerHTML = '<div class="cast-menu-head"><span>播放到</span><button type="button" class="cast-refresh">重新搜索</button></div><ul></ul><div class="cast-menu-empty"></div>';
  player.appendChild(menu);
  btn.addEventListener('click', (e) => {
    e.stopPropagation();
    menu.hidden = !menu.hidden;
    btn.setAttribute('aria-expanded', String(!menu.hidden));
    if (!menu.hidden) renderMenu(menu, false);
  });
  menu.querySelector('.cast-refresh').addEventListener('click', (e) => { e.stopPropagation(); renderMenu(menu, true); });
  document.addEventListener('click', (e) => {
    if (!menu.hidden && !menu.contains(e.target)) { menu.hidden = true; btn.setAttribute('aria-expanded', 'false'); }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !menu.hidden) { menu.hidden = true; btn.setAttribute('aria-expanded', 'false'); btn.focus(); }
  });
}
