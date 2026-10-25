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
    let mode = 'owned', snapshot, selected, idempotencyKey, acquisitionTimer, cooldownTimer, acquisitionPolling = false;
    const controls = root.querySelector('[data-acquisition-controls]');
    const cancelPanel = root.querySelector('[data-task-cancel]');
    const acquireButton = root.querySelector('[data-action="confirm-acquire"]');
    const dialog = root.querySelector('dialog');
    const grid = root.querySelector('[data-discovery-grid]');
    const message = root.querySelector('[data-discovery-live]');
    const dialogMessage = root.querySelector('[data-discovery-dialog-message]');
    const results = root.querySelector('[data-discovery-results]');
    const compare = root.querySelector('[data-discovery-compare]');
    const search = root.querySelector('[data-discovery-search]');
    const confirm = root.querySelector('[data-discovery-confirm]');
    cleanup = () => { clearTimeout(timer); clearTimeout(acquisitionTimer); clearInterval(cooldownTimer); controller.abort(); if (dialog.open) dialog.close(); };
    async function request(path, method = 'GET', body) {
      const response = await fetch(path.startsWith('/') ? path : '/admin/discover/' + path, { method, signal, headers: { 'X-CSRF-Token': document.querySelector('meta[name="csrf-token"]').content, 'Content-Type': 'application/json' }, body: body ? JSON.stringify(body) : undefined });
      const data = await response.json();
      if (!response.ok) { const e = new Error(data.error?.message || data.message || '请求失败，请重试'); e.data = data; e.status = response.status; e.retryAfter = Number(response.headers.get('Retry-After')) || 0; e.message = errors[data.error?.code] || e.message; throw e; }
      return data;
    }
    const errors = {
      disabled: '获取尚未启用，请前往获取设置完成配置', unavailable: '获取服务尚未配置，请前往获取设置',
      acquisition_active: '已有活跃获取任务，请先取消获取任务再忽略',
      jackett_auth: 'Jackett 认证失败，请检查获取设置', jackett_unreachable: '无法连接 Jackett，请检查获取设置',
      jackett_failed: 'Jackett 搜索失败，请稍后重试', jackett_cooldown: 'Jackett 请求冷却中',
      invalid_settings: '请前往获取设置完成 Jackett 与 qBittorrent 配置',
      qb_auth: 'qBittorrent 认证失败，请修改获取设置', qb_unreachable: '无法连接 qBittorrent，请检查连接后重试',
      magnet_only: '不支持磁力', pure_v2: '不支持纯 v2 种子', not_retryable: '此原因不能重试，请手动关联专辑',
      not_found: '搜索已过期或任务不存在，请重新搜索', hash_bound: '该种子已绑定其他获取任务',
      stale: '任务状态已变化，请刷新后重试', already_owned: '该发行已在曲库中', ignored: '请先恢复该发行',
      idempotency_conflict: '确认请求已变化，请重新搜索'
    };
    const reasons = {
      qb_unreachable: '无法连接 qBittorrent，请检查连接后重试', qb_auth: 'qBittorrent 认证失败，请修改获取设置',
      qb_rejected: 'qBittorrent 未接受种子，请重试', qb_missing: 'qBittorrent 中未找到任务，请检查后重试或关联专辑',
      torrent_unavailable: '种子暂不可用，请重试', library_timeout: '等待入库超时，请检查 MAT 整理及扫描',
      rg_conflict: '文件发行组身份冲突，请检查后关联专辑', preexisting_only: '仅检测到任务创建前的文件，请手动关联专辑',
      no_rg_files: '新文件缺少发行组身份，MAT 可能未识别', rule_suppressed: '可能受到专辑删除规则抑制，请检查后关联专辑'
    };
    function element(tag, text, className) {
      const el = document.createElement(tag); el.textContent = text; if (className) el.className = className; return el;
    }
    function taskCards(data) {
      const fragment = document.createDocumentFragment();
      const states = { QB_SUBMITTING: '提交中', DOWNLOADING: '下载中', AWAITING_LIBRARY: '等待入库', IN_LIBRARY: '已入库', ATTENTION: '需处理', CANCELLED: '已取消' };
      data.items.forEach(task => {
        const card = element('article', '', 'discovery-card acquisition-card');
        Object.assign(card.dataset, { rgid: task.rgid, title: task.title, artist: task.artist, publicId: task.publicId });
        card.id = 'acquisition-' + task.publicId;
        const albumHref = task.state === 'IN_LIBRARY' && task.albumId > 0 ? '/admin/albums/' + task.albumId : 'https://musicbrainz.org/release-group/' + task.rgid;
        const cover = element('a', '', 'discovery-cover');
        cover.href = albumHref; cover.tabIndex = -1; cover.setAttribute('aria-hidden', 'true');
        if (!albumHref.startsWith('/')) { cover.target = '_blank'; cover.rel = 'noopener noreferrer'; }
        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); svg.setAttribute('class', 'icon'); svg.setAttribute('aria-hidden', 'true');
        const use = document.createElementNS('http://www.w3.org/2000/svg', 'use'); use.setAttribute('href', (root.dataset.icons || '') + '#icon-albums'); svg.append(use); cover.append(svg);
        if (task.coverUrl) { const img = document.createElement('img'); img.src = task.coverUrl; img.alt = ''; img.loading = 'lazy'; cover.append(img); }
        cover.append(element('span', states[task.state] || task.state, 'acquisition-state' + (task.state === 'ATTENTION' ? ' is-attention' : task.state === 'IN_LIBRARY' ? ' is-done' : '')));
        const title = element('h2', ''); const link = element('a', task.title); link.href = albumHref;
        if (!albumHref.startsWith('/')) { link.target = '_blank'; link.rel = 'noopener noreferrer'; }
        title.append(link);
        const torrent = element('p', task.torrentTitle, 'discovery-meta acquisition-torrent'); torrent.title = task.torrentTitle;
        card.append(cover, title, element('p', task.artist, 'discovery-artist'), torrent);
        if (task.state === 'IN_LIBRARY') card.append(element('p', '专辑已入库 · 检测到 ' + task.detectedCount + ' 首', 'acquisition-note is-done'));
        if (task.state === 'AWAITING_LIBRARY' && data.watchDisabled) card.append(element('p', '目录监控已关闭，整理后需手动扫描入库。', 'acquisition-note'));
        const actions = element('div', '', 'discovery-card-actions');
        const button = (action, label, primary = false) => { const b = element('button', label, primary ? '' : 'secondary'); b.type = 'button'; b.dataset.action = action; actions.append(b); };
        if (task.state === 'ATTENTION') {
          card.append(element('p', reasons[task.attentionReason] || '请检查任务并手动关联专辑', 'acquisition-note is-attention'));
          if (/^(qb_|torrent_)/.test(task.attentionReason)) button('task-retry', '重试', true);
          button('task-link', '手动关联专辑', !/^(qb_|torrent_)/.test(task.attentionReason));
          const a = element('a', '获取设置', 'acquisition-note-link'); a.href = '/admin/settings/acquisition'; card.append(a);
        }
        if (!['IN_LIBRARY', 'CANCELLED'].includes(task.state)) button('task-cancel', '取消获取');
        if (actions.childElementCount) card.append(actions);
        fragment.append(card);
      });
      if (!data.items.length) fragment.append(element('p', '暂无获取任务', 'discovery-empty'));
      return fragment;
    }
    async function searchTorrents(force = false) {
      const sequence = ++searchSequence;
      snapshot = undefined; selected = undefined; idempotencyKey = undefined; acquireButton.disabled = true;
      results.replaceChildren(); clearInterval(cooldownTimer); dialogMessage.textContent = '正在搜索…';
      try {
        const data = await request('rg/' + target.rgid + '/search', 'POST', { q: search.elements.q.value, force });
        if (sequence !== searchSequence || !dialog.open) return;
        snapshot = data.searchId;
        const group = element('div', '', 'acquisition-results'); group.setAttribute('role', 'radiogroup'); group.setAttribute('aria-label', '种子候选');
        data.results.map((result, index) => ({ result, index })).sort((a, b) => (b.result.seeders ?? -1) - (a.result.seeders ?? -1)).forEach(({ result: r, index }) => {
          const label = element('label', '', 'acquisition-result'); const radio = document.createElement('input');
          radio.type = 'radio'; radio.name = 'torrent'; radio.value = index; radio.disabled = r.magnetOnly;
          radio.addEventListener('change', () => { selected = index; acquireButton.disabled = false; });
          label.append(radio, element('strong', r.title));
          label.append(element('span', [r.category || '分类未知', r.size == null ? '体积未知' : (r.size / 1048576).toFixed(1) + ' MB', (r.seeders ?? '未知') + ' 做种', (r.peers ?? '未知') + ' 下载', (r.grabs ?? '未知') + ' 完成', r.publishedAt || '日期未知'].join(' · ')));
          label.append(element('span', [...(r.badges || []), ...(r.downloadFactor === 0 ? ['FREELEECH'] : []), ...(r.magnetOnly ? ['不支持磁力'] : [])].join(' · ')));
          group.append(label);
        });
        results.append(group); dialogMessage.textContent = data.results.length ? '请选择一个种子，不会自动选中。' : '没有找到种子，请更换关键词。';
      } catch (e) {
        if (e.name === 'AbortError' || sequence !== searchSequence) return;
        showDialogError(e);
      }
    }
    function showDialogError(error) {
      clearInterval(cooldownTimer);
      dialogMessage.textContent = error.message;
      if (!error.retryAfter) return;
      const until = Date.now() + error.retryAfter * 1000;
      const tick = () => {
        const seconds = Math.max(0, Math.ceil((until - Date.now()) / 1000));
        dialogMessage.textContent = seconds ? error.message + '，请等待 ' + seconds + ' 秒' : error.message + '，可以重新搜索';
        if (!seconds) clearInterval(cooldownTimer);
      };
      tick(); cooldownTimer = setInterval(tick, 1000);
    }
    function newKey() {
      if (globalThis.crypto?.randomUUID) return crypto.randomUUID();
      return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, c => { const n = crypto.getRandomValues(new Uint8Array(1))[0] % 16; return (c === 'x' ? n : (n & 3) | 8).toString(16); });
    }
    function announce(text) { message.textContent = text; }
    function cardIndex(card = trigger?.closest('article[data-rgid], article[data-subject-id]')) {
      return [...grid.querySelectorAll('article[data-rgid], article[data-subject-id]')].indexOf(card);
    }
    function restoreFocus(index) {
      const active = document.activeElement;
      if (active?.isConnected && active !== document.body && (dialog.open || !dialog.contains(active))) return;
      const cards = [...grid.querySelectorAll('article[data-rgid], article[data-subject-id]')];
      const fallback = dialog.open ? dialog.querySelector('button:not(:disabled)') :
        [...(cards[Math.min(Math.max(index, 0), cards.length - 1)]?.querySelectorAll('button:not(:disabled)') || [])].find(button => button.getClientRects().length) || root.querySelector('[data-tab="' + tab + '"]');
      fallback?.focus();
    }
    async function load(append = false, focusIndex = cardIndex(document.activeElement?.closest('article[data-rgid], article[data-subject-id]')), { restore = true } = {}) {
      const sequence = ++listSequence;
      root.querySelector('.discovery-chips').hidden = tab !== 'recommended';
      root.querySelectorAll('[data-tab]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.tab === tab)));
      root.querySelectorAll('[data-filter]').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.filter === filter)));

      const data = await request('items.json?tab=' + (tab === 'recommended' && ['possible', 'mapping'].includes(filter) ? filter : tab) + '&source=' + (tab === 'recommended' && ['artist','work'].includes(filter) ? filter : 'all') + '&limit=60&offset=' + (append ? offset : 0));
      if (sequence !== listSequence) return;
      // HTML is rendered by the same escaped Go card template as the first screen.
      let content;
      if (tab === 'acquiring') content = taskCards(data);
      else { const fragment = document.createElement('template'); fragment.innerHTML = data.html; content = fragment.content; }
      if (!append) grid.replaceChildren(); grid.append(content);
      // Work-sourced picks only appear after a Bangumi→MusicBrainz match is confirmed.
      const empty = grid.querySelector('.discovery-empty p');
      if (empty && tab === 'recommended' && filter === 'work') empty.textContent = Number(data.counts.mapping) > 0 ? '作品来源的推荐需要先确认对应关系：在“待确认对应”中点“是这张”后会出现在这里。' : '暂无来自作品的推荐。作品需绑定 Bangumi 条目，并在刷新目录后找到对应的发行。';
      offset = (append ? offset : 0) + data.items.length;
      root.querySelector('[data-action="more"]').hidden = data.items.length < 60;
      Object.entries(data.counts).forEach(([key, value]) => { root.querySelectorAll('[data-count="' + key + '"]').forEach(el => { el.textContent = value; }); });
      if (restore) restoreFocus(focusIndex);
    }
    function close() { dialog.close(); }
    dialog.addEventListener('close', () => { ++searchSequence; clearInterval(cooldownTimer); if (trigger?.isConnected) trigger.focus(); else restoreFocus(target?.cardIndex ?? 0); }, { signal });
    function open(card, button, comparing, acquiring = false) {
      mode = acquiring ? 'acquire' : button.dataset.action === 'task-link' ? 'link' : 'owned';
      controls.hidden = !acquiring; cancelPanel.hidden = true; acquireButton.disabled = true;
      target = { rgid: card.dataset.rgid, title: card.dataset.title, publicId: card.dataset.publicId, cardIndex: cardIndex(card) }; trigger = button;
      results.replaceChildren(); compare.replaceChildren(); confirm.hidden = true; dialogMessage.textContent = '';
      root.querySelector('[data-discovery-target]').textContent = target.title;
      root.querySelector('#discovery-dialog-title').textContent = comparing ? '核对可能已有的发行' : '关联本地专辑';
      search.hidden = comparing;
      if (comparing) compare.append(card.querySelector('[data-possible]').cloneNode(true));
      compare.querySelector('[data-possible]')?.removeAttribute('hidden');
      search.elements.q.value = acquiring ? [card.dataset.artist, target.title].filter(Boolean).join(' ') : target.title;
      search.querySelector('label').textContent = acquiring ? 'JPopsuki 搜索关键词' : '搜索本地专辑';
      root.querySelector('#discovery-dialog-title').textContent = acquiring ? '获取发行' : comparing ? '核对可能已有的发行' : '关联本地专辑';
      dialog.showModal();
      (comparing ? dialog.querySelector('[data-action="same"]') : search.elements.q)?.focus();
    }
    async function owned(albumId, confirmReplace = false) {
      try { await request(mode === 'link' ? '/admin/acquisitions/' + target.publicId + '/link' : 'rg/' + target.rgid + '/owned', 'POST', { albumId, confirmReplace }); close(); await load(false, target.cardIndex); announce('已关联本地专辑，推荐和计数已更新。'); }
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
      if (mode === 'acquire') { await searchTorrents(); return; }
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
        const action = b.dataset.action, card = b.closest('article[data-rgid], article[data-subject-id]'), focusIndex = cardIndex(card);
        if (!action) return;
        if (action === 'close') { close(); return; }
        if (action === 'acquire') { open(card, b, false, true); return; }
        if (action === 'force-search') { await searchTorrents(true); return; }
        if (action === 'task-cancel') { open(card, b, false); search.hidden = true; cancelPanel.hidden = false; root.querySelector('#discovery-dialog-title').textContent = '取消获取任务'; cancelPanel.querySelector('button').focus(); return; }
        if (action === 'owned' || action === 'compare' || action === 'task-link') { open(card, b, action === 'compare'); return; }
        busy = true; b.disabled = true;
        if (action.startsWith('mapping-')) {
          const group = b.closest('[data-subject-id]');
          const subject = group.dataset.subjectId;
          const options = action === 'mapping-reject-all' ? [...group.querySelectorAll('[data-option-rgid]')].map(el => el.dataset.optionRgid) : [b.closest('[data-option-rgid]')?.dataset.optionRgid || card?.dataset.rgid];
          let reranked = true, processed = 0, failure;
          // Show progress on the card: the request also re-ranks the catalog.
          (group || card)?.setAttribute('aria-busy', 'true');
          announce('正在保存对应并更新推荐…');
          try {
            for (const rgid of options) {
              const decision = action === 'mapping-confirm' ? 'confirm' : 'reject';
              const data = await request('mapping/' + subject + '/' + rgid + '/' + decision, action === 'mapping-restore' ? 'DELETE' : 'POST');
              reranked &&= data.reranked;
              processed++;
            }
          } catch (error) { failure = error; }
          finally { await load(false, focusIndex); }
          if (failure) announce('已处理 ' + processed + '/' + options.length + ' 项 · ' + failure.message);
          else announce(reranked ? '对应已更新。' : '对应已更新，排名将在下次刷新时更新');
          return;
        }
        if (action === 'confirm-acquire') {
          if (selected === undefined || !snapshot) return;
          idempotencyKey ||= newKey();
          try { await request('rg/' + target.rgid + '/acquire', 'POST', { searchId: snapshot, resultIdx: selected, idempotencyKey }); close(); await load(); announce('已交给 qBittorrent'); }
          catch (e) {
            if (e.data?.error?.code === 'hash_bound') {
              dialogMessage.textContent = e.message;
              if (e.data.publicId) { const a = element('a', '查看绑定任务'); a.href = '#acquisition-' + e.data.publicId; a.addEventListener('click', () => { (async () => { close(); tab = 'acquiring'; await load(); document.getElementById('acquisition-' + e.data.publicId)?.scrollIntoView(); })().catch(e => announce(e.message)); }); dialogMessage.append(' · ', a); }
              else await load();
            } else throw e;
          }
        }
        if (action === 'task-retry') { await request('/admin/acquisitions/' + card.dataset.publicId + '/retry', 'POST'); await load(); announce('已安排重试'); }
        if (action === 'confirm-task-cancel') { await request('/admin/acquisitions/' + target.publicId + '/cancel', 'POST'); close(); await load(); announce('已取消本地追踪，未删除 qBittorrent 任务'); }
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
      } catch (e) { if (e.name !== 'AbortError') { if (dialog.open) showDialogError(e); else announce(e.message); } }
      finally { busy = false; b.disabled = b === acquireButton ? selected === undefined || !snapshot : b.dataset.action === 'refresh' && previousStatus === 'running'; }
    }, { signal });
    // Native modal dialog provides inert background, Escape and focus containment.
    dialog.addEventListener('keydown', e => {
      if (e.key !== 'Tab') return;
      const focusable = [...dialog.querySelectorAll('button,input,a[href]')].filter(el => !el.disabled && el.getClientRects().length);
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
          if (s.errorMessage && s.errorMessage !== s.bangumi) status.append(' · ' + s.errorMessage);
          if (s.status === 'cancelled') status.append(' · 已取消');
        }
        status.append(' · MusicBrainz · ' + (s.bangumi || 'Bangumi'));
        root.querySelector('[data-action="cancel"]').hidden = s.status !== 'running';
        root.querySelector('[data-action="refresh"]').disabled = s.status === 'running';
        if (previousStatus === 'running' && s.status !== 'running') { await load(); announce(s.errorMessage || '目录刷新已结束。'); }
        previousStatus = s.status;
        if (!signal.aborted && s.status === 'running') timer = setTimeout(poll, 1000);
      } catch (e) { if (e.name !== 'AbortError') announce(e.message); }
    }
    root.addEventListener('error', event => { if (event.target.matches('.discovery-cover img')) { event.target.remove(); } }, { signal, capture: true });
    async function pollAcquisitions() {
      clearTimeout(acquisitionTimer);
      if (signal.aborted || acquisitionPolling || document.hidden) return;
      acquisitionPolling = true;
      const pollingTab = tab;
      try {
        // Do not replace a card while a keyboard user is interacting with it.
        if (!dialog.open && !busy && !grid.contains(document.activeElement)) {
          if (pollingTab === 'acquiring' && grid.querySelector('[data-action="task-cancel"]')) {
            const pages = Math.max(1, Math.ceil(offset / 60));
            await load(false, undefined, { restore: false });
            for (let page = 1; page < pages && tab === pollingTab && !signal.aborted; page++) {
              await load(true, undefined, { restore: false });
            }
          } else if (pollingTab !== 'acquiring') {
            const data = await request('acquisitions.json');
            root.querySelector('[data-count="acquiring"]').textContent = data.items.length;
          }
        }
      } catch (e) { if (e.name !== 'AbortError') announce(e.message); }
      finally {
        acquisitionPolling = false;
        if (!signal.aborted && !document.hidden) acquisitionTimer = setTimeout(pollAcquisitions, 20000);
      }
    }
    document.addEventListener('visibilitychange', () => {
      clearTimeout(acquisitionTimer);
      if (!document.hidden) pollAcquisitions();
    }, { signal });
    acquisitionTimer = setTimeout(pollAcquisitions, 20000);
    poll();
  }
  document.addEventListener('DOMContentLoaded', init);
  document.addEventListener('032:pjax-applied', init);
  if (document.readyState !== 'loading') init();
})();
