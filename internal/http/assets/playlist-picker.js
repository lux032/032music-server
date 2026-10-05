// Shared playlist selection; candidates never use data-track-id.
import { showToast } from './util.js';
import { s } from './state.js';
let dialog, controller, origin, previewURL, undoTimer, undoBar, dragging;
const pending = new WeakSet();
let pageController = new AbortController();
const csrf = () => document.querySelector('meta[name="csrf-token"]')?.content || '';
export async function playlistRequest(url, method = 'GET', body, signal) {
  const headers = { 'X-CSRF-Token': csrf() };
  if (body && !(body instanceof FormData)) headers['Content-Type'] = 'application/json';
  const response = await fetch(url, { method, credentials: 'same-origin', headers, signal,
    body: body instanceof FormData ? body : body ? JSON.stringify(body) : undefined });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) { const error = new Error(payload.error?.message || '请求失败，请重试'); error.status = response.status; error.payload = payload; throw error; }
  return payload;
}
function statsText(result) { return `已加入 ${result.added || 0} 首 · 重复 ${result.skippedDuplicate || 0} 首 · 无效 ${result.skippedInvalid || 0} 首 · 共 ${result.total || 0} 首`; }
function closePicker() { controller?.abort(); controller = null; dialog?.close(); dialog?.remove(); dialog = null; if (origin?.isConnected) origin.focus(); }
export async function openPlaylistPicker(input) {
  const trigger = document.activeElement; closePicker(); origin = trigger; controller = new AbortController();
  const signal = controller.signal, snapshot = input.trackIds ? { trackIds: [...input.trackIds] } : { albumId: input.albumId };
  const current = document.createElement('dialog'); dialog = current; current.className = 'playlist-picker';
  current.innerHTML = `<header><h2>加入歌单</h2><button type="button" data-picker-close aria-label="关闭歌单选择">×</button></header>
    <label>搜索歌单<input type="search" data-picker-search placeholder="歌单名称"></label>
    <p role="status" data-picker-status>正在加载歌单…</p><div class="playlist-picker-results"></div>
    <form data-picker-create><h3>新建并加入</h3><label>名称<input name="name" required maxlength="160"></label><button>创建并加入</button></form>`;
  document.body.append(current); current.showModal(); current.querySelector('input').focus();
  const status = current.querySelector('[data-picker-status]'), list = current.querySelector('.playlist-picker-results');
  const all = [];
  function render() {
    list.replaceChildren(); const query = current.querySelector('[data-picker-search]').value.toLocaleLowerCase();
    for (const playlist of all.filter(p => p.name.toLocaleLowerCase().includes(query))) {
      const button = document.createElement('button'); button.type = 'button'; button.textContent = `${playlist.name} · ${playlist.itemCount} 首`;
      button.addEventListener('click', async () => {
        if (pending.has(current)) return; pending.add(current); current.querySelectorAll('button:not([data-picker-close]),input').forEach(b => { b.disabled = true; }); status.textContent = '正在加入…';
        try { const result = await playlistRequest(`/api/v1/playlists/${playlist.id}/items/append`, 'POST', snapshot, signal); if (dialog !== current) return; showToast(statsText(result)); closePicker(); }
        catch (error) { if (!signal.aborted) status.textContent = error.message; }
        finally { pending.delete(current); current.querySelectorAll('button,input').forEach(b => { b.disabled = false; }); }
      }); list.append(button);
    }
    if (!list.children.length) { const empty = document.createElement('p'); empty.textContent = '没有匹配歌单，可以在下方新建'; list.append(empty); }
  }
  current.querySelector('[data-picker-close]').onclick = closePicker;
  current.addEventListener('cancel', e => { e.preventDefault(); closePicker(); });
  current.querySelector('[data-picker-search]').addEventListener('input', render);
  current.querySelector('form').addEventListener('submit', async e => {
    e.preventDefault(); if (pending.has(current)) return; pending.add(current); const button = e.target.querySelector('button'); current.querySelectorAll('button:not([data-picker-close]),input').forEach(el => { el.disabled = true; });
    try {
      let ids = snapshot.trackIds;
      if (!ids) { const album = await playlistRequest(`/api/v1/albums/${snapshot.albumId}`, 'GET', undefined, signal); ids = (album.tracks || []).map(t => t.id); }
      const result = await playlistRequest('/api/v1/playlists', 'POST', { name: e.target.elements.name.value, trackIds: ids, invalidTracks: 'skip' }, signal);
      if (dialog !== current) return; showToast(statsText(result.stats)); closePicker();
    } catch (error) { if (!signal.aborted) status.textContent = error.message; }
    finally { pending.delete(current); current.querySelectorAll('button,input').forEach(el => { el.disabled = false; }); }
  });
  try {
    // Exhaust pagination before client-side search; no truncated target list.
    for (let offset = 0; ; offset += 500) { const page = await playlistRequest(`/api/v1/playlists?limit=500&offset=${offset}`, 'GET', undefined, signal); all.push(...page.items); if (offset + page.items.length >= page.total || !page.items.length) break; }
    if (dialog !== current) return; status.textContent = `${all.length} 个歌单`; render();
  } catch (error) { if (!signal.aborted) status.textContent = error.message; }
}
function playlistRoot() { return document.querySelector('[data-playlist-id]'); }
function rows() { return [...document.querySelectorAll('[data-track-list] [data-track-id]')]; }
function selectedIDs() { return rows().filter(row => row.querySelector('.playlist-select')?.checked).map(row => Number(row.dataset.trackId)); }
function updateSelection() { document.querySelectorAll('[data-selected-count]').forEach(el => { el.textContent = `${selectedIDs().length} 首已选`; }); }
function enhance() {
  rows().forEach(row => {
    if (row.closest('[data-track-select]') && !row.querySelector('.playlist-select')) {
      const checkbox = document.createElement('input'); checkbox.type = 'checkbox'; checkbox.className = 'playlist-select'; checkbox.setAttribute('aria-label', `选择 ${row.dataset.trackTitle}`); row.prepend(checkbox);
    }
    if (!row.querySelector('[data-playlist-track]')) {
      const button = document.createElement('button'); button.type = 'button'; button.className = 'playlist-add-track'; button.dataset.playlistTrack = row.dataset.trackId; button.textContent = '加入歌单'; button.setAttribute('aria-label', `加入歌单 ${row.dataset.trackTitle}`);
      const cell = row.querySelector('.track-actions') || [...row.children].find(child => child.querySelector('strong'));
      if (cell) { cell.classList.add('playlist-action-cell'); cell.append(button); }
    }
  }); updateSelection();
}
async function refreshPlaylist(root, signal = pageController.signal, active = document.activeElement) {
  const track = active?.closest?.("[data-track-id]")?.dataset.trackId;
  const selector = active?.hasAttribute?.("data-playlist-move") ? `[data-playlist-move][value="${active.value}"]` : active?.classList.contains("playlist-drag") ? ".playlist-drag" : ".row-play-btn";
  const response = await fetch(location.href, { credentials: 'same-origin', signal }); if (!response.ok) throw new Error('刷新歌单失败');
  const doc = new DOMParser().parseFromString(await response.text(), 'text/html'), fresh = doc.querySelector('[data-playlist-id]');
  if (root.isConnected && fresh && fresh.dataset.playlistId === root.dataset.playlistId) { root.replaceWith(fresh); enhance(); if (track) fresh.querySelector(`[data-track-id="${track}"] ${selector}`)?.focus({ preventScroll: true }); }
}
function clearUndo() { clearTimeout(undoTimer); undoBar?.remove(); undoBar = null; }
function offerUndo(id, removed) {
  clearUndo(); const bar = document.createElement('div'); undoBar = bar; bar.className = 'playlist-undo'; bar.setAttribute('role', 'status');
  const text = document.createElement('span'); text.textContent = `已移除 ${removed.length} 首`; const button = document.createElement('button'); button.textContent = '撤销'; button.type = 'button'; bar.append(text, button); document.body.append(bar);
  undoTimer = setTimeout(clearUndo, 8000);
  button.onclick = async () => {
    if (button.disabled) return; const signal = pageController.signal; button.disabled = true; clearTimeout(undoTimer); let restored = 0, invalid = 0;
    try { for (const item of removed) { const result = await playlistRequest(`/api/v1/playlists/${id}/items/insert`, 'POST', { trackId: item.id, index: item.index }, signal); restored += result.added; invalid += result.skippedInvalid; } const root = playlistRoot(); if (root?.dataset.playlistId === id) await refreshPlaylist(root); showToast(`已恢复 ${restored} 首 · 已删除 ${invalid} 首`); clearUndo(); }
    catch (error) { if (signal.aborted) return; text.textContent = error.message; button.disabled = false; undoTimer = setTimeout(clearUndo, 8000); }
  };
}
async function mutate(root, operation, input, removed, rollbackOrder) {
  if (pending.has(root)) return; const signal = pageController.signal, focus = document.activeElement; pending.add(root); const enabled = [...root.querySelectorAll('[data-playlist-move],[data-playlist-remove],[data-playlist-remove-selected],.playlist-drag')].filter(b => !b.disabled); enabled.forEach(b => { b.disabled = true; });
  try { const id = root.dataset.playlistId; await playlistRequest(`/api/v1/playlists/${id}/${operation}`, operation === 'order' ? 'PUT' : 'POST', { ...input, expectedRevision: Number(root.dataset.playlistRevision) }, signal); await refreshPlaylist(root, signal, focus); if (!signal.aborted && removed?.length) offerUndo(id, removed); }
  catch (error) {
    if (rollbackOrder && root.isConnected) { const list = root.querySelector('[data-track-list]'); for (const id of rollbackOrder) { const row = list.querySelector(`[data-track-id="${id}"]`); if (row) list.append(row); } }
    if (signal.aborted) return; showToast(error.status === 409 ? '歌单已在别处修改，已刷新列表' : error.message); if (error.status === 409 || rollbackOrder) await refreshPlaylist(root, signal, focus).catch(() => {}); }
  finally { pending.delete(root); enabled.forEach(b => { b.disabled = false; }); }
}
function removeTracks(root, ids) { const removed = rows().map((row, index) => ({ id: Number(row.dataset.trackId), index })).filter(item => ids.includes(item.id)); return mutate(root, 'items/remove', { trackIds: ids }, removed); }
function moveTrack(root, row, delta) { const ids = rows().map(row => Number(row.dataset.trackId)), i = ids.indexOf(Number(row.dataset.trackId)), to = i + delta; if (to < 0 || to >= ids.length) return; [ids[i], ids[to]] = [ids[to], ids[i]]; return mutate(root, 'order', { trackIds: ids }); }
async function artwork(root, form, reset) {
  if (pending.has(form)) return; const signal = pageController.signal; pending.add(form); const status = form.querySelector('[data-artwork-status]'); form.querySelectorAll('button').forEach(b => { b.disabled = true; });
  try {
    const file = form.elements.image.files[0]; if (!reset && file?.size > 10 * 1024 * 1024) throw new Error('图片不能超过 10MB');
    status.textContent = '正在保存…'; await playlistRequest(`/api/v1/playlists/${root.dataset.playlistId}/artwork`, reset ? 'DELETE' : 'PUT', reset ? undefined : new FormData(form), signal);
    if (!root.isConnected || signal.aborted) return;
    // A revision is meaningful only together with the ordered list read in
    // the same snapshot. The artwork response can include another tab's edit.
    await refreshPlaylist(root, signal);
    const fresh = playlistRoot();
    if (!signal.aborted && fresh?.dataset.playlistId === root.dataset.playlistId) {
      fresh.querySelector('[data-playlist-editor]').open = true;
      fresh.querySelector('[data-artwork-status]').textContent = reset ? '已恢复自动封面' : '封面已更新';
    }
  } catch (error) { if (!signal.aborted) status.textContent = error.message; }
  finally { pending.delete(form); form.querySelectorAll('button').forEach(b => { b.disabled = false; }); }
}
export function setupPlaylists() {
  enhance();
  document.addEventListener('032:pjax-before-swap', () => { pageController.abort(); pageController = new AbortController(); closePicker(); clearUndo(); if (previewURL) URL.revokeObjectURL(previewURL); dragging = null; });
  document.addEventListener('032:pjax-applied', enhance);
  document.addEventListener('click', e => {
    if (!(e.target instanceof Element)) return; const button = e.target.closest('button'); if (!button) return;
    if (button.hasAttribute('data-playlist-track')) { e.preventDefault(); openPlaylistPicker({ trackIds: [Number(button.dataset.playlistTrack)] }); }
    if (button.hasAttribute('data-playlist-album')) { e.preventDefault(); openPlaylistPicker({ albumId: Number(button.dataset.playlistAlbum) }); }
    if (button.hasAttribute('data-playlist-selected')) { e.preventDefault(); const ids = selectedIDs(); if (ids.length) openPlaylistPicker({ trackIds: ids }); else showToast('请先选择歌曲'); }
    if (button.hasAttribute('data-select-tracks')) { const boxes = [...document.querySelectorAll('.playlist-select')], all = boxes.every(c => c.checked); boxes.forEach(c => { c.checked = !all; }); updateSelection(); }
    const root = playlistRoot(); if (!root) return;
    if (button.hasAttribute('data-playlist-edit')) { const editor = root.querySelector('[data-playlist-editor]'); editor.open = true; editor.scrollIntoView({ block: 'nearest' }); editor.querySelector('input').focus(); }
    if (button.hasAttribute('data-playlist-remove-selected')) { e.preventDefault(); removeTracks(root, selectedIDs()); }
    if (button.hasAttribute('data-playlist-remove')) { e.preventDefault(); removeTracks(root, [Number(button.closest('[data-track-id]').dataset.trackId)]); }
    if (button.hasAttribute('data-playlist-move')) { e.preventDefault(); moveTrack(root, button.closest('[data-track-id]'), button.value === 'up' ? -1 : 1); }
    if (button.hasAttribute('data-playlist-artwork-reset')) { e.preventDefault(); artwork(root, button.closest('form'), true); }
  });
  document.addEventListener('submit', e => {
    const form = e.target; if (!(form instanceof HTMLFormElement)) return;
    if (form.hasAttribute('data-playlist-artwork')) { e.preventDefault(); artwork(playlistRoot(), form, false); }
    if (form.querySelector('[data-playlist-move],[data-playlist-remove]')) e.preventDefault();
  }, true);
  document.addEventListener('change', e => {
    if (!(e.target instanceof Element)) return;
    if (e.target.matches('.playlist-select')) updateSelection();
    if (e.target.matches('[data-playlist-artwork] input[type=file]')) { if (previewURL) URL.revokeObjectURL(previewURL); const image = e.target.closest('form').querySelector('[data-artwork-preview]'), file = e.target.files[0]; image.hidden = !file; if (file) { previewURL = URL.createObjectURL(file); image.src = previewURL; } }
  });
  document.addEventListener('keydown', e => {
    if (dialog) {
      if (e.key === 'Escape') { e.preventDefault(); e.stopImmediatePropagation(); closePicker(); return; }
      if (e.key === 'Tab') { const focusable = [...dialog.querySelectorAll('button:not(:disabled),input')].filter(el => el.offsetParent); const first = focusable[0], last = focusable.at(-1); if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last?.focus(); } else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first?.focus(); } }
      e.stopPropagation();
    }
    if (e.altKey && ['ArrowUp', 'ArrowDown'].includes(e.key) && e.target.closest('[data-playlist-id] [data-track-id]')) { e.preventDefault(); moveTrack(playlistRoot(), e.target.closest('[data-track-id]'), e.key === 'ArrowUp' ? -1 : 1); }
  }, true);
  document.addEventListener('pointerdown', e => {
    const handle = e.target.closest?.('.playlist-drag'), root = playlistRoot(); if (!handle || !root || pending.has(root)) return;
    e.preventDefault(); const row = handle.closest('[data-track-id]'), list = row.parentElement; list.setPointerCapture(e.pointerId); dragging = { handle, row, list, root, pointer: e.pointerId, old: rows().map(row => Number(row.dataset.trackId)) }; row.classList.add('dragging');
  });
  document.addEventListener('pointermove', e => {
    if (!dragging || e.pointerId !== dragging.pointer) return; e.preventDefault(); const { row, list } = dragging;
    const target = [...list.children].find(el => el !== row && el.hasAttribute('data-track-id') && e.clientY < el.getBoundingClientRect().top + el.getBoundingClientRect().height / 2); list.insertBefore(row, target || null);
  });
  function endDrag(e) { if (!dragging || e.pointerId !== dragging.pointer) return; const drag = dragging; dragging = null; drag.row.classList.remove('dragging'); if (e.type === 'pointercancel') { for (const id of drag.old) drag.list.append([...drag.list.children].find(row => Number(row.dataset.trackId) === id)); return; } const ids = rows().map(row => Number(row.dataset.trackId)); if (ids.join() !== drag.old.join()) mutate(drag.root, 'order', { trackIds: ids }, undefined, drag.old); }
  document.addEventListener('pointerup', endDrag); document.addEventListener('pointercancel', endDrag); document.addEventListener('lostpointercapture', e => { if (dragging) endDrag({ pointerId: e.pointerId, type: 'pointercancel' }); });
  const queue = document.getElementById('np-queue-list'); if (queue) { const save = document.createElement('button'); save.type = 'button'; save.className = 'playlist-save-queue'; save.textContent = '保存队列到歌单'; save.onclick = () => { const ids = s.queue.map(track => Number(track.id)); if (ids.length) openPlaylistPicker({ trackIds: ids }); else showToast('播放队列为空'); }; queue.before(save); }
}
