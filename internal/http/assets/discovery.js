(() => {
  'use strict';
  let cleanup = () => {};
  function init() {
    const root = document.querySelector('[data-discovery]');
    if (root?.dataset.initialized) return;
    cleanup();
    if (!root) return;
    root.dataset.initialized = 'true';
    const controller = new AbortController();
    const { signal } = controller;
    let timer, tab = 'recommended', filter = 'all', target, trigger, replaceAlbum, busy = false, offset = root.querySelectorAll('[data-rgid]').length, searchSequence = 0, listSequence = 0;
    const dialog = root.querySelector('dialog');
    const grid = root.querySelector('[data-discovery-grid]');
    const message = root.querySelector('[data-discovery-live]');
    const dialogMessage = root.querySelector('[data-discovery-dialog-message]');
    const results = root.querySelector('[data-discovery-results]');
    const compare = root.querySelector('[data-discovery-compare]');
    const search = root.querySelector('[data-discovery-search]');
    const confirm = root.querySelector('[data-discovery-confirm]');
    cleanup = () => { clearTimeout(timer); controller.abort(); if (dialog.open) dialog.close(); };
    async function request(path, method = 'GET', body) {
      const response = await fetch('/admin/discover/' + path, { method, signal, headers: { 'X-CSRF-Token': document.querySelector('meta[name="csrf-token"]').content, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
      const data = await response.json();
      if (!response.ok) { const e = new Error(data.error?.message || data.message || '请求失败，请重试'); e.data = data; e.status = response.status; throw e; }
      return data;
    }
    function announce(text) { message.textContent = text; }
    function cardIndex(card = trigger?.closest('[data-rgid]')) {
      return [...grid.querySelectorAll('[data-rgid]')].indexOf(card);
    }
    function restoreFocus(index) {
      const active = document.activeElement;
      if (active?.isConnected && active !== document.body && (dialog.open || !dialog.contains(active))) return;
      const cards = [...grid.querySelectorAll('[data-rgid]')];
      const fallback = dialog.open ? dialog.querySelector('button:not(:disabled)') :
        cards[Math.min(Math.max(index, 0), cards.length - 1)]?.querySelector('button') || root.querySelector('[data-tab="' + tab + '"]');
      fallback?.focus();
    }
    async function load(append = false, focusIndex = cardIndex(document.activeElement?.closest('[data-rgid]'))) {
      const sequence = ++listSequence;
      root.querySelector('.discovery-chips').hidden = tab !== 'recommended';
      root.querySelectorAll('[data-tab]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.tab === tab)));
      root.querySelectorAll('[data-filter]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.filter === filter)));
      if (tab === 'acquiring') { grid.replaceChildren(); const p = document.createElement('p'); p.className = 'discovery-empty'; p.textContent = '获取功能将在后续版本提供'; grid.append(p); root.querySelector('[data-action="more"]').hidden = true; return; }
      // S1 has only artist seeds: all and artist are equivalent until S3 adds work sources.
      const data = await request('items.json?tab=' + (tab === 'recommended' && filter === 'possible' ? 'possible' : tab) + '&limit=60&offset=' + (append ? offset : 0));
      if (sequence !== listSequence) return;
      // HTML is rendered by the same escaped Go card template as the first screen.
      const fragment = document.createElement('template'); fragment.innerHTML = data.html;
      if (!append) grid.replaceChildren(); grid.append(fragment.content);
      offset = (append ? offset : 0) + data.items.length;
      root.querySelector('[data-action="more"]').hidden = data.items.length < 60;
      Object.entries(data.counts).forEach(([key, value]) => { root.querySelectorAll('[data-count="' + key + '"]').forEach(el => { el.textContent = value; }); });
      restoreFocus(focusIndex);
    }
    function close() { dialog.close(); }
    dialog.addEventListener('close', () => { ++searchSequence; if (trigger?.isConnected) trigger.focus(); else restoreFocus(target?.cardIndex ?? 0); }, { signal });
    function open(card, button, comparing) {
      target = { rgid: card.dataset.rgid, title: card.dataset.title, cardIndex: cardIndex(card) }; trigger = button;
      results.replaceChildren(); compare.replaceChildren(); confirm.hidden = true; dialogMessage.textContent = '';
      root.querySelector('[data-discovery-target]').textContent = target.title;
      root.querySelector('#discovery-dialog-title').textContent = comparing ? '核对可能已有的发行' : '关联本地专辑';
      search.hidden = comparing;
      if (comparing) compare.append(card.querySelector('[data-possible]').cloneNode(true));
      compare.querySelector('[data-possible]')?.removeAttribute('hidden');
      search.elements.q.value = target.title;
      dialog.showModal();
      (comparing ? dialog.querySelector('[data-action="same"]') : search.elements.q)?.focus();
    }
    async function owned(albumId, confirmReplace = false) {
      try { await request('rg/' + target.rgid + '/owned', 'POST', { albumId, confirmReplace }); close(); await load(false, target.cardIndex); announce('已关联本地专辑，推荐和计数已更新。'); }
      catch (e) {
        if (e.status === 409 && e.data.error?.code === 'replaces_rg') {
          replaceAlbum = albumId; confirm.hidden = false;
          root.querySelector('[data-discovery-replace]').textContent = '将把该专辑的发行组由 ' + e.data.currentRg + ' 改为 ' + target.rgid;
          confirm.querySelector('button').focus();
        } else throw e;
      }
    }
    root.addEventListener('submit', async event => {
      if (event.target !== search) return; event.preventDefault();
      const sequence = ++searchSequence;
      dialogMessage.textContent = '正在搜索…'; results.replaceChildren(); confirm.hidden = true;
      try {
        const response = await fetch('/admin/options/albums?q=' + encodeURIComponent(search.elements.q.value) + '&discovery=1', { signal });
        if (!response.ok) throw new Error('搜索失败，请重试');
        const albums = await response.json(); if (sequence !== searchSequence || !dialog.open) return;
        albums.forEach(album => { const b = document.createElement('button'); b.type = 'button'; b.dataset.action = 'select-album'; b.dataset.albumId = album.id; b.textContent = album.label + ' · ' + (album.year || '年份未知') + ' · ' + album.trackCount + ' 首' + (album.currentRg ? ' · 已关联发行组 ' + album.currentRg : ' · 尚无发行组'); results.append(b); });
        dialogMessage.textContent = albums.length ? '选择与该发行一致的本地专辑。' : '没有找到本地专辑，请更换关键词。';
      } catch (e) { if (e.name !== 'AbortError') dialogMessage.textContent = e.message; }
    }, { signal });
    root.addEventListener('click', async event => {
      const b = event.target.closest('button'); if (!b || busy) return;
      try {
        if (b.dataset.tab) { tab = b.dataset.tab; await load(); return; }
        if (b.dataset.filter) { filter = b.dataset.filter; await load(); return; }
        const action = b.dataset.action, card = b.closest('[data-rgid]'), focusIndex = cardIndex(card);
        if (!action) return;
        if (action === 'close') { close(); return; }
        if (action === 'owned' || action === 'compare') { open(card, b, action === 'compare'); return; }
        busy = true; b.disabled = true;
        if (action === 'ignore' || action === 'restore') { await request('rg/' + card.dataset.rgid + '/ignore', action === 'ignore' ? 'POST' : 'DELETE'); await load(false, focusIndex); announce(action === 'ignore' ? '已忽略该发行，可在已忽略中恢复。' : '已恢复推荐。'); }
        if (action === 'same' || action === 'select-album') await owned(Number(b.dataset.albumId));
        if (action === 'confirm-replace') await owned(replaceAlbum, true);
        if (action === 'not-same') {
          await request('rg/' + target.rgid + '/not-same', 'POST', { albumId: Number(b.dataset.albumId) });
          b.closest('[data-current-rg]').remove(); await load(false, target.cardIndex); announce('已记住：不是同一发行。');
          if (!compare.querySelector('[data-action="same"]')) { compare.replaceChildren(); search.hidden = false; dialogMessage.textContent = '已完成对比。可以搜索其他本地专辑，或关闭后查看推荐。'; search.elements.q.focus(); }
        }
        if (action === 'more') await load(true);
        if (action === 'refresh' || action === 'cancel') { await request(action === 'refresh' ? 'refresh' : 'refresh/cancel', 'POST'); if (action === 'refresh') previousStatus = 'running'; await poll(); }
      } catch (e) { if (e.name !== 'AbortError') { if (dialog.open) dialogMessage.textContent = e.message; else announce(e.message); } }
      finally { busy = false; b.disabled = b.dataset.action === 'refresh' && previousStatus === 'running'; }
    }, { signal });
    // Native modal dialog provides inert background, Escape and focus containment.
    dialog.addEventListener('keydown', e => {
      if (e.key !== 'Tab') return;
      const focusable = [...dialog.querySelectorAll('button,input')].filter(el => !el.disabled && el.getClientRects().length);
      const first = focusable[0], last = focusable.at(-1);
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    }, { signal });
    let previousStatus;
    async function poll() {
      clearTimeout(timer);
      try {
        const s = await request('status.json');
        const status = root.querySelector('[data-discovery-status]');
        status.replaceChildren();
        if (s.status === 'running') status.textContent = '刷新中：' + s.processed + ' / ' + s.total;
        else {
          status.append('目录更新时间：');
          if (s.finishedAt) {
            const time = document.createElement('time');
            time.dateTime = s.finishedAt; time.dataset.localTime = ''; time.textContent = s.finishedAt;
            status.append(time);
          } else status.append('尚未刷新');
          if (s.errorMessage) status.append(' · ' + s.errorMessage);
          if (s.status === 'cancelled') status.append(' · 已取消');
        }
        root.querySelector('[data-action="cancel"]').hidden = s.status !== 'running';
        root.querySelector('[data-action="refresh"]').disabled = s.status === 'running';
        if (previousStatus === 'running' && s.status !== 'running') { await load(); announce(s.errorMessage || '目录刷新已结束。'); }
        previousStatus = s.status;
        if (s.status === 'running') timer = setTimeout(poll, 1000);
      } catch (e) { if (e.name !== 'AbortError') announce(e.message); }
    }
    root.addEventListener('error', event => { if (event.target.matches('.discovery-cover img')) { event.target.remove(); } }, { signal, capture: true });
    poll();
  }
  document.addEventListener('DOMContentLoaded', init);
  document.addEventListener('032:pjax-applied', init);
  if (document.readyState !== 'loading') init();
})();
