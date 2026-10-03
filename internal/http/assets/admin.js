// 032 Music Server - Admin shell behaviours: adaptive scan-status polling
// (drives both the sidebar scan card and the dashboard page), collapsible
// sidebar admin group, unsaved-change highlighting on source cards.
(function () {
  'use strict';

  if (window.__032_admin_initialized) return;
  window.__032_admin_initialized = true;

  // ------------------------------------------------- sidebar admin group
  const GROUP_KEY = '032_admin_group';
  const ADMIN_NAV_KEYS = ['console', 'matches', 'work-review', 'series', 'enrichment', 'merges', 'settings', 'security'];

  function applyGroupState() {
    const group = document.getElementById('sidebar-admin-group');
    if (!group) return;
    const toggle = document.getElementById('sidebar-group-toggle');
    const nav = document.body.dataset.nav || '';
    let open;
    if (ADMIN_NAV_KEYS.includes(nav)) {
      // The active page lives inside the group: force it open.
      open = true;
    } else {
      let stored = null;
      try { stored = localStorage.getItem(GROUP_KEY); } catch (_) {}
      open = stored === 'open';
    }
    group.classList.toggle('open', open);
    if (toggle) toggle.setAttribute('aria-expanded', String(open));
  }

  document.addEventListener('click', (e) => {
    if (!(e.target instanceof Element)) return;
    const toggle = e.target.closest('#sidebar-group-toggle');
    if (!toggle) return;
    const group = document.getElementById('sidebar-admin-group');
    if (!group) return;
    const open = !group.classList.contains('open');
    group.classList.toggle('open', open);
    toggle.setAttribute('aria-expanded', String(open));
    try { localStorage.setItem(GROUP_KEY, open ? 'open' : 'closed'); } catch (_) {}
  });

  // ----------------------------------------- mobile “更多” sheet (M5/V7)
  // The bottom bar only fits the primary destinations; everything else,
  // including the admin group and logout, lives in this sheet.
  function moreSheet() { return document.getElementById('sidebar-more-sheet'); }
  function setMoreSheet(open) {
    const sheet = moreSheet();
    const btn = document.getElementById('sidebar-more');
    if (!sheet || !btn) return;
    sheet.hidden = !open;
    document.body.classList.toggle('more-sheet-open', open);
    btn.setAttribute('aria-expanded', String(open));
    if (open) sheet.querySelector('a')?.focus();
  }
  document.addEventListener('click', (e) => {
    if (!(e.target instanceof Element)) return;
    const btn = e.target.closest('#sidebar-more');
    if (btn) {
      const sheet = moreSheet();
      setMoreSheet(!!(sheet && sheet.hidden));
      return;
    }
    const sheet = moreSheet();
    if (sheet && !sheet.hidden && !e.target.closest('#sidebar-more-sheet')) setMoreSheet(false);
  });
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    const sheet = moreSheet();
    if (sheet && !sheet.hidden) {
      setMoreSheet(false);
      document.getElementById('sidebar-more')?.focus();
    }
  });
  // [P1] A link inside the sheet navigates via PJAX; close the sheet both
  // immediately and after the swap so focus never stays trapped inside.
  document.addEventListener('click', (e) => {
    if (!(e.target instanceof Element)) return;
    if (e.target.closest('#sidebar-more-sheet a[href]')) setMoreSheet(false);
  });
  document.addEventListener('032:pjax-applied', () => setMoreSheet(false));
  document.addEventListener('032:pjax-applied', applyGroupState);
  applyGroupState();

  // Native dialogs used by the work detail editor and destructive confirm.
  document.addEventListener('click', (e) => {
    if (!(e.target instanceof Element)) return;
    const opener = e.target.closest('[data-dialog-open]');
    if (opener) {
      const dialog = document.getElementById(opener.getAttribute('data-dialog-open') || '');
      if (dialog instanceof HTMLDialogElement) dialog.showModal();
      return;
    }
    const closer = e.target.closest('[data-dialog-close]');
    if (closer) closer.closest('dialog')?.close();
  });

  // ------------------------------------------- adaptive scan/status polling
  const statusMap = {
    never: '尚未扫描',
    running: '正在扫描',
    completed: '扫描完成',
    failed: '扫描失败'
  };

  let lastScanStatus = '';
  let pollTimer = null;

  function formatClock(date) {
    return `${date.getHours().toString().padStart(2, '0')}:${date.getMinutes().toString().padStart(2, '0')}:${date.getSeconds().toString().padStart(2, '0')}`;
  }

  // formatEta renders a coarse remaining-time estimate; returns '' when the
  // estimate would be meaningless.
  function formatEta(scan) {
    if (!scan.startedAt) return '';
    const started = Date.parse(scan.startedAt);
    if (Number.isNaN(started)) return '';
    const done = (scan.processedFiles || 0) + (scan.skippedFiles || 0) + (scan.failedFiles || 0);
    const total = scan.discoveredFiles || 0;
    if (done <= 0 || total <= done) return '';
    const elapsedSec = (Date.now() - started) / 1000;
    if (elapsedSec < 3) return '';
    const rate = done / elapsedSec;
    if (rate <= 0) return '';
    const remainingSec = (total - done) / rate;
    if (!Number.isFinite(remainingSec) || remainingSec < 2 || remainingSec > 3600 * 4) return '';
    if (remainingSec >= 90) return `预计剩余 ${Math.round(remainingSec / 60)} 分钟`;
    return `预计剩余 ${Math.round(remainingSec)} 秒`;
  }

  function updateSidebarScanCard(value) {
    const card = document.getElementById('sidebar-scan');
    if (!card) return;
    const ring = document.getElementById('sidebar-scan-ring');
    const title = document.getElementById('sidebar-scan-title');
    const detail = document.getElementById('sidebar-scan-detail');
    const scan = value.scan || {};
    const stats = value.statistics || {};

    if (scan.status === 'running') {
      card.classList.remove('idle');
      const total = scan.discoveredFiles || 0;
      const done = (scan.processedFiles || 0) + (scan.skippedFiles || 0) + (scan.failedFiles || 0);
      const percent = total ? Math.min(100, (done / total) * 100) : 0;
      if (ring) ring.style.strokeDashoffset = String(100 - percent);
      if (title) title.textContent = '正在扫描音乐库';
      if (detail) {
        const parts = [`已扫描 ${done.toLocaleString()} / ${total.toLocaleString()}`];
        const eta = formatEta(scan);
        if (eta) parts.push(eta);
        detail.textContent = parts.join(' · ');
      }
      return;
    }

    card.classList.add('idle');
    if (scan.status === 'failed') {
      if (title) title.textContent = '扫描失败';
      if (detail) detail.textContent = scan.errorMessage || '请查看控制台日志';
      return;
    }
    if (title) title.textContent = '音乐库已就绪';
    if (detail) {
      const tracks = stats.tracks || 0;
      const albums = stats.albums || 0;
      detail.textContent = `${tracks.toLocaleString()} 首歌曲 · ${albums.toLocaleString()} 张专辑`;
    }
  }

  const refresh = async () => {
    const dashboardTitle = document.querySelector('#scan-title');
    const sidebarCard = document.getElementById('sidebar-scan');
    if (!dashboardTitle && !sidebarCard) return;
    const lastUpdated = document.querySelector('#scan-last-updated');
    try {
      const response = await fetch('/admin/status', { credentials: 'same-origin' });
      if (!response.ok) throw new Error('fetch failed');
      const value = await response.json();
      const ids = {
        albums: document.querySelector('#albums-count'),
        artists: document.querySelector('#artists-count'),
        tracks: document.querySelector('#tracks-count'),
        audioFiles: document.querySelector('#files-count')
      };
      Object.entries(ids).forEach(([key, element]) => { if (element) element.textContent = value.statistics[key]; });
      const emptyHint = document.querySelector('#empty-library-hint');
      if (emptyHint && value.statistics.audioFiles > 0) emptyHint.hidden = true;
      const scan = value.scan;
      lastScanStatus = scan.status || '';
      if (dashboardTitle) {
        const statusText = statusMap[scan.status] || scan.status || 'never';
        dashboardTitle.textContent = `扫描状态：${statusText}`;
        if (lastUpdated) lastUpdated.textContent = `最后更新：${formatClock(new Date())}`;
        const path = document.querySelector('#scan-path');
        if (path) path.textContent = scan.currentRelativePath || '';
        const detail = document.querySelector('#scan-detail');
        if (detail) detail.textContent = `发现 ${scan.discoveredFiles || 0} · 已处理 ${scan.processedFiles || 0} · 跳过 ${scan.skippedFiles || 0} · 失败 ${scan.failedFiles || 0} · 丢失 ${scan.missingFiles || 0}`;
        const progress = document.querySelector('#scan-progress');
        if (progress) {
          const percent = scan.discoveredFiles ? Math.min(100, ((scan.processedFiles + scan.skippedFiles + scan.failedFiles) / scan.discoveredFiles) * 100) : 0;
          progress.style.width = `${percent}%`;
        }
      }
      updateSidebarScanCard(value);
    } catch (_) {
      if (lastUpdated) lastUpdated.textContent = '状态更新中断，重试连接中…';
    }
  };

  // Poll fast while a scan is running or the dashboard is on screen,
  // leisurely otherwise, and never while the tab is hidden.
  function pollDelay() {
    if (lastScanStatus === 'running') return 1500;
    if (document.getElementById('scan-title')) return 1500;
    return 30000;
  }

  function scheduleNext() {
    if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
    if (document.hidden) return;
    pollTimer = setTimeout(tick, pollDelay());
  }

  async function tick() {
    await refresh();
    scheduleNext();
  }

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      if (pollTimer) { clearTimeout(pollTimer); pollTimer = null; }
    } else {
      tick();
    }
  });
  // After a PJAX swap the dashboard elements may have appeared or vanished;
  // re-evaluate the cadence and refresh immediately.
  document.addEventListener('032:pjax-applied', () => { tick(); });

  tick();

  // ------------------------------------- source-card unsaved-change highlight
  document.addEventListener('input', (e) => {
    const form = e.target.closest ? e.target.closest('form[data-scope-form]') : null;
    if (!form) return;
    const btn = form.querySelector('button[type="submit"]');
    if (btn && !btn.classList.contains('is-unsaved')) {
      btn.classList.add('is-unsaved');
    }
  });

  // ----------------------------------------- security page: copy one-shot value
  // The flashed token lives only in a readonly input; copying never builds
  // markup from its value (textContent only for the status line).
  document.addEventListener('click', async (e) => {
    const button = e.target instanceof Element ? e.target.closest('[data-copy-target]') : null;
    if (!button) return;
    const input = document.getElementById(button.getAttribute('data-copy-target') || '');
    if (!(input instanceof HTMLInputElement)) return;
    const status = button.closest('[data-security-flash]')?.querySelector('[data-copy-status]');
    const report = (message) => { if (status) status.textContent = message; };
    try {
      if (!navigator.clipboard || !window.isSecureContext) throw new Error('clipboard unavailable');
      await navigator.clipboard.writeText(input.value);
      report('已复制到剪贴板。');
    } catch (_) {
      input.focus();
      input.select();
      report('无法自动复制，已选中内容，请按 Ctrl+C（Mac 为 ⌘+C）复制。');
    }
  });

  // One-shot security values must never come back from the back/forward
  // cache: drop them when the page is hidden and again if a persisted page
  // is restored (the HTML itself is served with Cache-Control: no-store).
  function removeSecurityFlashes() {
    document.querySelectorAll('[data-security-flash]').forEach((el) => el.remove());
  }
  window.addEventListener('pagehide', removeSecurityFlashes);
  window.addEventListener('pageshow', (e) => { if (e.persisted) removeSecurityFlashes(); });

  // ------------------------------------------- durable run task polling
  // /admin/matches and /admin/enrichment pin their active run on top. While a
  // card is visible we poll its active.json every 2s; idle pages fall back to
  // a leisurely 30s check; hidden tabs never poll. All updates use
  // textContent only — no untrusted markup is ever built.
  const RUN_STATUS_LABELS = { running: '进行中', paused: '已暂停', completed: '已完成', failed: '已失败', cancelled: '已停止', queued: '排队中' };
  const RUN_PAUSE_REASONS = { manual: '手动暂停', server_restart: '服务重启中断', shutdown: '服务关闭', rate_limit_count: '同一对象连续限流', rate_limit_wait_budget: '限流等待超出 30 分钟预算', storage_or_runtime_error: '存储或运行错误' };
  let runPollTimer = null;
  let runCountdownTimer = null;

  function runWaitTotalText(ms) {
    const seconds = Math.floor((ms || 0) / 1000);
    if (seconds >= 90) return `${Math.round(seconds / 60)} 分钟`;
    return `${seconds} 秒`;
  }

  function runWaitLine(el) {
    const source = el.dataset.waitSource || '';
    const until = Date.parse(el.dataset.waitingUntil || '');
    const total = runWaitTotalText(Number(el.dataset.waitTotalMs || 0));
    if (source && Number.isFinite(until)) {
      const remaining = Math.max(0, Math.round((until - Date.now()) / 1000));
      if (remaining > 0) {
        const mm = String(Math.floor(remaining / 60)).padStart(2, '0');
        const ss = String(remaining % 60).padStart(2, '0');
        el.textContent = `限流 ${source} · 等待 ${mm}:${ss}（累计等待 ${total}）`;
        return;
      }
    }
    if (source && (el.dataset.waitTotalMs || '0') !== '0') {
      el.textContent = `限流 ${source} · 累计等待 ${total}`;
      return;
    }
    el.textContent = '';
  }

  function runCountdownTick() {
    const lines = document.querySelectorAll('[data-run-field="wait"]');
    lines.forEach(runWaitLine);
    if (!document.querySelector('[data-run-card]') && runCountdownTimer) {
      clearInterval(runCountdownTimer);
      runCountdownTimer = null;
    }
  }

  function ensureRunCountdown() {
    if (!runCountdownTimer && document.querySelector('[data-run-card]')) {
      runCountdownTimer = setInterval(runCountdownTick, 1000);
    }
  }

  function runCountsText(kind, run) {
    if (kind === 'artist') {
      return `处理 ${run.processed} / ${run.total} · 匹配 ${run.matched} · 审核 ${run.review} · 无结果 ${run.noResult} · 跳过 ${run.skipped} · 失败 ${run.failed}`;
    }
    return `处理 ${run.processed} / ${run.total} · 成功 ${run.succeeded} · 跳过 ${run.skipped} · 审核 ${run.review} · 失败 ${run.failed}`;
  }

  function runField(card, name) { return card.querySelector(`[data-run-field="${name}"]`); }
  function setRunText(card, name, text) { const el = runField(card, name); if (el) el.textContent = text; }

  function showRunHint(container, text) {
    let hint = container.querySelector('.run-update-hint');
    if (!hint) {
      hint = document.createElement('small');
      hint.className = 'run-update-hint';
      container.append(hint);
    }
    hint.textContent = text;
  }
  function clearRunHint(container) { container.querySelector('.run-update-hint')?.remove(); }

  // updateRunCard returns 'reload' when the visible card no longer matches
  // the server state (new run id), true when an active card is shown.
  function updateRunCard(container, run, kind) {
    const card = container.querySelector('[data-run-card]');
    if (!run) {
      if (card) {
        // 任务已结束：不宣称具体结果，避免把未知结局误报为“完成”。
        const done = document.createElement('p');
        done.className = 'muted run-empty';
        done.setAttribute('data-run-empty', '');
        done.textContent = '任务已结束，刷新页面查看最新结果。';
        card.replaceWith(done);
      }
      return false;
    }
    if (!card || card.dataset.runId !== String(run.id)) return 'reload';
    card.dataset.runStatus = run.status;
    const statusEl = runField(card, 'status');
    if (statusEl) {
      statusEl.textContent = RUN_STATUS_LABELS[run.status] || run.status;
      statusEl.className = `run-status status-${run.status}`;
    }
    const bar = runField(card, 'progressBar');
    if (bar) {
      const percent = run.total > 0 ? Math.min(100, Math.round((run.processed * 100) / run.total)) : 0;
      bar.style.width = `${percent}%`;
    }
    setRunText(card, 'counts', runCountsText(kind, run));
    setRunText(card, 'current', run.current ? `当前：${run.current}` : '');
    const wait = runField(card, 'wait');
    if (wait) {
      wait.dataset.waitSource = run.waitSource || '';
      wait.dataset.waitingUntil = run.waitingUntil || '';
      wait.dataset.waitTotalMs = String(run.waitTotalMs || 0);
      runWaitLine(wait);
    }
    setRunText(card, 'pauseReason', run.status === 'paused' && run.pauseReason ? `暂停原因：${RUN_PAUSE_REASONS[run.pauseReason] || run.pauseReason}` : '');
    setRunText(card, 'error', run.errorMessage || '');
    card.querySelectorAll('[data-run-action]').forEach((form) => {
      const action = form.getAttribute('data-run-action');
      form.hidden = (action === 'pause' && run.status !== 'running') || (action === 'resume' && run.status !== 'paused');
    });
    return true;
  }

  async function pollRunTasks() {
    const containers = Array.from(document.querySelectorAll('[data-run-tasks]'));
    let failed = false;
    for (const container of containers) {
      try {
        const response = await fetch(container.getAttribute('data-active-url'), { credentials: 'same-origin' });
        if (!response.ok) throw new Error('fetch failed');
        const payload = await response.json();
        clearRunHint(container);
        const result = updateRunCard(container, payload.run, container.getAttribute('data-run-kind'));
        if (result === 'reload') { location.reload(); return 'reload'; }
      } catch (_) {
        failed = true;
        // 失败时保留现有卡片，绝不能把未知状态误报为完成。
        showRunHint(container, '状态更新中断，重试连接中…');
      }
    }
    ensureRunCountdown();
    return failed ? 'failed' : 'ok';
  }

  function scheduleRunPoll(delay) {
    if (runPollTimer) { clearTimeout(runPollTimer); runPollTimer = null; }
    if (document.hidden || !document.querySelector('[data-run-tasks]')) return;
    runPollTimer = setTimeout(runPollTick, delay);
  }

  async function runPollTick() {
    const result = await pollRunTasks();
    if (result === 'reload' || document.hidden || !document.querySelector('[data-run-tasks]')) return;
    if (result === 'failed') { scheduleRunPoll(8000); return; }
    // running/waiting 卡片 2s 高频；paused 卡片与空闲页面 30s 低频；没有容器不轮询。
    // paused→running 的变化最迟 30s 被发现，随后回到 2s（reload 仅发生在 run id 变化时）。
    const card = document.querySelector('[data-run-card]');
    scheduleRunPoll(card && card.dataset.runStatus !== 'paused' ? 2000 : 30000);
  }

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      if (runPollTimer) { clearTimeout(runPollTimer); runPollTimer = null; }
    } else {
      scheduleRunPoll(0);
    }
  });
  // PJAX 换页后容器可能新增/消失：重新评估并重查一次，不重复计时器。
  document.addEventListener('032:pjax-applied', () => scheduleRunPoll(0));
  scheduleRunPoll(0);

  // Password input masks custom tokens even when CSS masking is unsupported.
  document.addEventListener('click', (e) => {
    const button = e.target instanceof Element ? e.target.closest('[data-mask-toggle]') : null;
    if (!button) return;
    const input = document.getElementById(button.getAttribute('data-mask-toggle') || '');
    if (!(input instanceof HTMLInputElement)) return;
    const reveal = input.type === 'password';
    input.type = reveal ? 'text' : 'password';
    button.textContent = reveal ? '隐藏' : '显示';
    button.setAttribute('aria-pressed', String(reveal));
  });

  // ------------------------------------------------- custom image upload guard (L6)
  // Multipart upload forms (custom album covers / artist images) reject
  // files over 10MB on the server; stop the submit early with a clear
  // message instead of uploading the whole file first. form.elements
  // includes form=-associated controls, matching the router's ownership rule.
  const CUSTOM_IMAGE_MAX_BYTES = 10 * 1024 * 1024;
  document.addEventListener('submit', (e) => {
    const form = e.target;
    if (!(form instanceof HTMLFormElement) || (form.enctype || '').toLowerCase() !== 'multipart/form-data') return;
    for (const el of Array.from(form.elements)) {
      if (!(el instanceof HTMLInputElement) || el.type !== 'file' || !el.files || !el.files.length) continue;
      if (el.files[0].size <= CUSTOM_IMAGE_MAX_BYTES) continue;
      e.preventDefault();
      const fieldset = el.closest('fieldset');
      let error = fieldset ? fieldset.querySelector('.form-error') : null;
      if (!error) {
        error = document.createElement('p');
        error.className = 'form-error';
        (fieldset || form).append(error);
      }
      error.textContent = '图片不能超过 10MB，请选择更小的文件。';
      el.focus();
      return;
    }
  }, true);
})();

// Delegated events survive PJAX replacement; option names remain plain text.
let creditSearchTimer;
document.addEventListener('input', (event) => {
 const input = event.target.closest('[data-credit-search]'); if (!input) return;
 clearTimeout(creditSearchTimer);
 const picker = input.closest('[data-credit-picker]');
 creditSearchTimer = setTimeout(async () => {
  const role = picker.querySelector('[data-credit-role]').value;
  const query = input.value;
  try {
   const response = await fetch(`/admin/options/artists?role=${role === 'any' ? 'credit' : role}&q=${encodeURIComponent(query)}`);
   const data = await response.json();
   if (!picker.isConnected || input.value !== query || picker.querySelector('[data-credit-role]').value !== role) return;
   const select = picker.querySelector('[data-credit-results]'); select.replaceChildren();
   for (const artist of data || []) { const option = document.createElement('option'); option.value = artist.id; option.textContent = artist.label; select.append(option); }
  } catch (_) { picker.querySelector('[data-credit-results]').replaceChildren(); }
 }, 200);
});
document.addEventListener('change', (event) => {if (event.target.matches('[data-credit-role]')) {const input = event.target.closest('[data-credit-picker]').querySelector('[data-credit-search]');input.dispatchEvent(new Event('input', {bubbles:true}));}});
document.addEventListener('click', (event) => {
 const remove = event.target.closest('[data-credit-remove]'); if (remove) {remove.closest('label').remove();return;}
 const add = event.target.closest('[data-credit-add]'); if (!add) return;
 const picker = add.closest('[data-credit-picker]'); const option = picker.querySelector('[data-credit-results]').selectedOptions[0]; if (!option) return;
 const role = picker.querySelector('[data-credit-role]'); const value = `${role.value}:${option.value}`;
 if (Array.from(picker.querySelectorAll('input[name="credit"]')).some(input => input.value === value)) return;
 const label = document.createElement('label');label.textContent = `${role.selectedOptions[0].textContent}：${option.textContent}`;
 const hidden = document.createElement('input');hidden.type='hidden';hidden.name='credit';hidden.value=value;
 const button = document.createElement('button');button.type='button';button.dataset.creditRemove='';button.textContent='移除';label.append(hidden,button);picker.querySelector('[data-credit-selected]').append(label);
});
