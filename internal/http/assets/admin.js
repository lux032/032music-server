// 032 Music Server - Admin shell behaviours: adaptive scan-status polling
// for the sidebar and dashboard, mobile navigation, and settings forms.
(function () {
  'use strict';

  if (window.__032_admin_initialized) return;
  window.__032_admin_initialized = true;

  // Server and PJAX both mark the active sidebar mode.
  function syncWorkspaceMode() {
    document.body.classList.toggle('admin-workspace',
      !!document.getElementById('app-sidebar')?.classList.contains('is-admin-workspace'));
  }

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
  document.addEventListener('032:pjax-applied', syncWorkspaceMode);
  syncWorkspaceMode();

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

  // Select-all checkbox: toggles every input[name=id] in its form.
  document.addEventListener('change', (e) => {
    const all = e.target;
    if (!(all instanceof HTMLInputElement) || !all.hasAttribute('data-check-all') || !all.form) return;
    for (const box of all.form.querySelectorAll('input[name=id]')) box.checked = all.checked;
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

  function updateSidebarScanStatus(value) {
    const status = document.getElementById('sidebar-scan');
    if (!status) return;
    const title = document.getElementById('sidebar-scan-title');
    const detail = document.getElementById('sidebar-scan-detail');
    const scan = value.scan || {};
    const stats = value.statistics || {};
    const running = scan.status === 'running';
    const failed = scan.status === 'failed';
    status.dataset.scanState = running ? 'running' : failed ? 'failed' : 'idle';
    const total = scan.discoveredFiles || 0;
    const done = (scan.processedFiles || 0) + (scan.skippedFiles || 0) + (scan.failedFiles || 0);
    const label = running ? '正在扫描音乐库' : failed ? '扫描失败' :
      stats.tracks ? '音乐库已就绪' : '曲库暂无歌曲';
    const summary = running ? `${done.toLocaleString()} / ${total.toLocaleString()} · ${formatEta(scan)}`.replace(/ · $/, '') :
      failed ? scan.errorMessage || '请查看控制台日志' :
        `${(stats.tracks || 0).toLocaleString()} 首歌曲 · ${(stats.albums || 0).toLocaleString()} 张专辑`;
    if (title.textContent !== label) title.textContent = label;
    if (detail.textContent !== summary) detail.textContent = summary;
    status.title = summary;
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
          const healthy = !scan.status || ['completed', 'never', 'success'].includes(scan.status);
          progress.classList.toggle('ok', healthy);
          progress.style.width = `${healthy && !scan.discoveredFiles ? 100 : percent}%`;
        }
      }
      updateSidebarScanStatus(value);
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
    const status = button.closest('[data-security-flash], [data-totp-setup]')?.querySelector('[data-copy-status]');
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

  function syncSourceTabs() {
    const main = document.querySelector('.metadata-main');
    if (!main) return;
    const target = document.getElementById(window.location.hash.slice(1));
    const sync = target?.id === 'playback-sync' || !!target?.closest('.metadata-sync-cards');
    main.dataset.sourceView = sync ? 'sync' : 'sources';
    for (const tab of main.querySelectorAll('[data-source-tab]')) {
      if ((tab.dataset.sourceTab === 'sync') === sync) tab.setAttribute('aria-current', 'page');
      else tab.removeAttribute('aria-current');
    }
  }
  window.addEventListener('hashchange', syncSourceTabs);
  document.addEventListener('032:pjax-applied', syncSourceTabs);
  syncSourceTabs();

  // ------------------------------------------- durable run task polling
  // /admin/matches and /admin/enrichment pin their active run on top. While a
  // card is visible we poll its active.json every 2s; idle pages fall back to
  // a leisurely 30s check; hidden tabs never poll. All updates use
  // textContent only — no untrusted markup is ever built.
  const RUN_STATUS_LABELS = { running: '进行中', paused: '已暂停', completed: '已完成', failed: '已失败', cancelled: '已停止', queued: '排队中' };
  const artistTaskSources = [
    ['/admin/matches/runs/active.json', '身份匹配'],
    ['/admin/ai-review/runs/active.json?group=artists', 'AI 初审'],
    ['/admin/matches/images/runs/active.json', '头像补全'],
    ['/admin/matches/biographies/runs/active.json', '简介补全']
  ];
  let artistTasksTimer;
  let artistTasksRequest;
  function watchArtistTasks() {
    clearTimeout(artistTasksTimer);
    artistTasksRequest?.abort();
    const overview = document.querySelector('[data-artist-task-overview]');
    if (!overview || document.hidden) return;
    const request = new AbortController();
    artistTasksRequest = request;
    const refresh = async () => {
      const runs = await Promise.allSettled(artistTaskSources.map(async ([url, label]) => {
        const response = await fetch(url, { credentials: 'same-origin', signal: request.signal });
        if (!response.ok) return null;
        const { run } = await response.json();
        if (!run || !['running', 'paused', 'queued'].includes(run.status)) return null;
        const item = document.createElement('li');
        item.className = 'chip';
        item.textContent = `${label} #${run.id} · ${RUN_STATUS_LABELS[run.status]}`;
        return item;
      }));
      if (request.signal.aborted) return;
      const items = runs.flatMap(result => result.status === 'fulfilled' && result.value ? [result.value] : []);
      overview.querySelector('[data-artist-task-list]').replaceChildren(...items);
      overview.hidden = items.length === 0;
      artistTasksTimer = setTimeout(refresh, 15000);
    };
    refresh();
  }
  document.addEventListener('032:pjax-applied', watchArtistTasks);
  document.addEventListener('visibilitychange', watchArtistTasks);
  watchArtistTasks();

  const RUN_PAUSE_REASONS = { manual: '手动暂停', server_restart: '服务重启中断', shutdown: '服务关闭', rate_limit_count: '同一对象连续限流', rate_limit_wait_budget: '限流等待超出 30 分钟预算', rate_limit_exhausted: '自动重试已用尽', storage_or_runtime_error: '存储或运行错误' };
  let runPollTimer = null;
  let runPollController = null;
  let runCountdownTimer = null;

  function refreshCurrentPage() {
    if (typeof window.__032BrowseNavigate === 'function') {
      window.__032BrowseNavigate(window.location.href, true);
    } else {
      location.reload();
    }
  }

  function runWaitTotalText(ms) {
    const seconds = Math.floor((ms || 0) / 1000);
    if (seconds >= 90) return `${Math.round(seconds / 60)} 分钟`;
    return `${seconds} 秒`;
  }

  function runWaitLine(el) {
    const source = el.dataset.waitSource || '';
    const until = Date.parse(el.dataset.waitingUntil || '');
    const total = runWaitTotalText(Number(el.dataset.waitTotalMs || 0));
    // 只有限流原因的暂停才会到点自动继续；手动暂停绝不这样宣称。
    const autoResume = el.dataset.runStatus === 'paused' && ['rate_limit_count', 'rate_limit_wait_budget', 'server_restart'].includes(el.dataset.pauseReason || '');
    if (source && Number.isFinite(until)) {
      const remaining = Math.max(0, Math.round((until - Date.now()) / 1000));
      if (remaining > 0) {
        const mm = String(Math.floor(remaining / 60)).padStart(2, '0');
        const ss = String(remaining % 60).padStart(2, '0');
        el.textContent = autoResume
          ? `限流 ${source} · ${mm}:${ss} 后自动继续（累计等待 ${total}）`
          : `限流 ${source} · 等待 ${mm}:${ss}（累计等待 ${total}）`;
        return;
      }
    }
    if (source && (el.dataset.waitTotalMs || '0') !== '0') {
      el.textContent = `限流 ${source} · 累计等待 ${total}`;
      return;
    }
    el.textContent = '';
  }

  function hasActiveWaitCountdown() {
    for (const el of document.querySelectorAll('[data-run-field="wait"]')) {
      const until = Date.parse(el.dataset.waitingUntil || '');
      if (el.dataset.waitSource && Number.isFinite(until) && until > Date.now()) return true;
    }
    return false;
  }

  function stopRunCountdown() {
    if (runCountdownTimer) {
      clearInterval(runCountdownTimer);
      runCountdownTimer = null;
    }
  }

  function runCountdownTick() {
    if (document.hidden) { stopRunCountdown(); return; }
    const lines = document.querySelectorAll('[data-run-field="wait"]');
    lines.forEach(runWaitLine);
    if (!hasActiveWaitCountdown()) stopRunCountdown();
  }

  function ensureRunCountdown() {
    if (document.hidden || !hasActiveWaitCountdown()) { stopRunCountdown(); return; }
    if (!runCountdownTimer) {
      runCountdownTimer = setInterval(runCountdownTick, 1000);
    }
  }

  function runCountsText(kind, run) {
    if (kind === 'artist') {
      return `处理 ${run.processed} / ${run.total} · 匹配 ${run.matched} · 审核 ${run.review} · 无结果 ${run.noResult} · 跳过 ${run.skipped} · 失败 ${run.failed}`;
    }
    if (kind === 'artistImage') {
      const candPart = run.candidate > 0 ? ` · 送审 ${run.candidate}` : '';
      return `处理 ${run.processed} / ${run.total} · 已缓存 ${run.cached} · 无地址 ${run.noUrl} · 跳过 ${run.skipped} · 失败 ${run.failed}${candPart}`;
    }
    if (kind === 'artistBio') {
      return `处理 ${run.processed} / ${run.total} · 已补全 ${run.filled} · 无简介 ${run.missing} · 跳过 ${run.skipped} · 失败 ${run.failed}`;
    }
    if (kind === 'aiReview') {
      return `处理 ${run.processed} / ${run.total} · AI 自动处理 ${run.autoApplied} · 给建议 ${run.recommended} · 不确定 ${run.uncertain} · 跳过 ${run.skipped} · 失败 ${run.failed}`;
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

  function syncPipelineCard(kind, run) {
    if (!kind) return;
    const pipeline = document.querySelector(`[data-pipeline-kind="${kind}"]`);
    if (!pipeline) return;
    const badge = pipeline.querySelector('[data-pipeline-status]');
    const note = pipeline.querySelector('[data-pipeline-running-note]');
    if (!run) {
      if (badge) {
        badge.textContent = badge.dataset.idleLabel || '已结束';
        badge.className = 'run-status match-status-idle';
      }
      if (note) note.textContent = '任务已结束，刷新页面查看最新结果或再次启动。';
      return;
    }
    if (badge) {
      badge.textContent = RUN_STATUS_LABELS[run.status] || run.status;
      badge.className = `run-status status-${run.status}`;
    }
  }

  // updateRunCard returns 'reload' when the visible card no longer matches
  // the server state (new run id), true when an active card is shown.
  function updateRunCard(container, run, kind) {
    const card = container.querySelector('[data-run-card]');
    if (!run) {
      if (card) {
        // 任务已结束：不宣称具体结果，避免把未知结局误报为“完成”。
        const done = document.createElement('div');
        done.className = 'match-run-done-row';
        done.setAttribute('data-run-empty', '');
        const msg = document.createElement('p');
        msg.className = 'muted';
        msg.textContent = '任务已结束，可刷新页面查看最新结果。';
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'secondary-button compact-button';
        btn.textContent = '刷新查看结果';
        btn.addEventListener('click', refreshCurrentPage);
        done.append(msg, btn);
        card.replaceWith(done);
        syncPipelineCard(kind, null);
      }
      return false;
    }
    if (!card || card.dataset.runId !== String(run.id)) return 'reload';
    card.dataset.runStatus = run.status;
    card.classList.toggle('is-paused', run.status === 'paused');
    syncPipelineCard(kind, run);
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
    const currentText = run.current || run.currentLabel || '';
    setRunText(card, 'current', currentText ? `当前：${currentText}` : '');
    const wait = runField(card, 'wait');
    if (wait) {
      wait.dataset.waitSource = run.waitSource || '';
      wait.dataset.waitingUntil = run.waitingUntil || '';
      wait.dataset.waitTotalMs = String(run.waitTotalMs || 0);
      wait.dataset.runStatus = run.status || '';
      wait.dataset.pauseReason = run.pauseReason || '';
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

  function desiredRunPollDelay() {
    const anyActive = document.querySelector('[data-run-card]:not([data-run-status="paused"])');
    return anyActive ? 2000 : 30000;
  }

  async function pollRunTasks() {
    runPollController?.abort();
    const containers = Array.from(document.querySelectorAll('[data-run-tasks]'));
    if (!containers.length) return 'ok';
    const controller = new AbortController();
    runPollController = controller;
    let failed = false;
    let needsReload = false;
    await Promise.all(containers.map(async (container) => {
      try {
        const response = await fetch(container.getAttribute('data-active-url'), {
          credentials: 'same-origin',
          signal: controller.signal
        });
        if (!response.ok) throw new Error('fetch failed');
        const payload = await response.json();
        if (controller.signal.aborted || !container.isConnected) return;
        clearRunHint(container);
        const result = updateRunCard(container, payload.run, container.getAttribute('data-run-type') || container.getAttribute('data-run-kind'));
        if (result === 'reload') needsReload = true;
      } catch (err) {
        if (controller.signal.aborted || (err && err.name === 'AbortError')) return;
        failed = true;
        // 失败时保留现有卡片，绝不能把未知状态误报为完成。
        showRunHint(container, '状态更新中断，重试连接中…');
      }
    }));
    if (controller.signal.aborted) return 'aborted';
    if (needsReload) { refreshCurrentPage(); return 'reload'; }
    if (!document.querySelector('[data-run-card]')) {
      document.querySelectorAll('.match-tab-dot').forEach((dot) => { dot.hidden = true; });
    }
    ensureRunCountdown();
    return failed ? 'failed' : 'ok';
  }

  function scheduleRunPoll(delay) {
    if (runPollTimer) { clearTimeout(runPollTimer); runPollTimer = null; }
    if (document.hidden || !document.querySelector('[data-run-tasks]')) {
      runPollController?.abort();
      stopRunCountdown();
      return;
    }
    ensureRunCountdown();
    runPollTimer = setTimeout(runPollTick, delay);
  }

  async function runPollTick() {
    runPollTimer = null;
    const result = await pollRunTasks();
    if (result === 'reload' || result === 'aborted' || document.hidden || !document.querySelector('[data-run-tasks]')) return;
    if (result === 'failed') { scheduleRunPoll(8000); return; }
    // 任一卡片 running/waiting 即 2s 高频（页面可能同时有匹配与头像补全两张
    // 卡，不能只看第一张）；全部 paused 或没有卡片时 30s 低频；没有容器不轮询。
    // paused→running 的变化最迟 30s 被发现，随后回到 2s（reload 仅发生在 run id 变化时）。
    scheduleRunPoll(desiredRunPollDelay());
  }

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      if (runPollTimer) { clearTimeout(runPollTimer); runPollTimer = null; }
      runPollController?.abort();
      stopRunCountdown();
    } else {
      scheduleRunPoll(0);
    }
  });
  // 页面刚完成 SSR 或 PJAX 渲染时服务端已给出最新任务状态，按常规节奏排期即可，
  // 避免每次进入页面立刻并发轰炸 4 个 active.json。
  document.addEventListener('032:pjax-applied', () => {
    runPollController?.abort();
    scheduleRunPoll(desiredRunPollDelay());
  });
  scheduleRunPoll(desiredRunPollDelay());

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

// ---------------------------------------------- acoustic analysis status poller
// Polls /admin/audio-analysis/status every 3 s while the analysis page (or the
// dashboard card) is on screen and the tab is visible. All labels are built by
// the server (buildAudioAnalysisView); this only copies them with textContent.
(function () {
  'use strict';
  if (window.__032_audio_analysis_initialized) return;
  window.__032_audio_analysis_initialized = true;
  let timer = null;

  const setLine = (id, text) => {
    const el = document.getElementById(id);
    if (!el) return;
    el.textContent = text || '';
    el.hidden = !text;
  };

  async function refresh() {
    const root = document.getElementById('audio-analysis-status');
    const summary = document.getElementById('aa-summary');
    if (!root && !summary) return;
    try {
      const response = await fetch('/admin/audio-analysis/status', { credentials: 'same-origin' });
      if (!response.ok) return;
      const { view } = await response.json();
      if (summary) summary.textContent = view.summaryText || '';
      if (!root) return;
      root.dataset.tone = view.tone || 'off';
      const state = document.getElementById('aa-state');
      if (state) state.textContent = view.stateLabel || '';
      setLine('aa-state-detail', view.stateDetail);
      const bar = document.getElementById('aa-progress');
      if (bar) bar.style.width = `${Math.max(0, Math.min(100, Number(view.percent) || 0))}%`;
      const progress = document.getElementById('aa-progress-text');
      if (progress) progress.textContent = view.progressText || '';
      setLine('aa-current', view.currentText);
      setLine('aa-eta', view.etaText);
      setLine('aa-calibration', view.calibrationText);
      setLine('aa-failure', view.failureText);
      setLine('aa-last-error', view.lastErrorText);
      setLine('aa-analyzer', view.analyzerText);
    } catch (_) { /* keep the last rendered state; retry on the next tick */ }
  }

  function schedule() {
    if (timer) { clearTimeout(timer); timer = null; }
    if (document.hidden) return;
    if (document.getElementById('audio-analysis-status')) timer = setTimeout(tick, 3000);
    else if (document.getElementById('aa-summary')) timer = setTimeout(tick, 15000);
  }

  async function tick() {
    await refresh();
    schedule();
  }

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) { if (timer) { clearTimeout(timer); timer = null; } } else { tick(); }
  });
  document.addEventListener('032:pjax-applied', () => { tick(); });
  schedule();
})();

// ---------------------------------------------------- AI tagging status poller
// Same pattern as the acoustic analysis poller: 5 s on the AI tagging page,
// 15 s for the dashboard card; labels come from buildAITaggingView.
(function () {
  'use strict';
  if (window.__032_ai_tagging_initialized) return;
  window.__032_ai_tagging_initialized = true;
  let timer = null;

  const setLine = (id, text) => {
    const el = document.getElementById(id);
    if (!el) return;
    el.textContent = text || '';
    el.hidden = !text;
  };

  async function refresh() {
    const root = document.getElementById('ai-tagging-status');
    const summary = document.getElementById('at-summary');
    if (!root && !summary) return;
    try {
      const response = await fetch('/admin/ai-tagging/status', { credentials: 'same-origin' });
      if (!response.ok) return;
      const { view } = await response.json();
      if (summary) summary.textContent = view.summaryText || '';
      if (!root) return;
      root.dataset.tone = view.tone || 'off';
      const state = document.getElementById('at-state');
      if (state) state.textContent = view.stateLabel || '';
      setLine('at-state-detail', view.stateDetail);
      const bar = document.getElementById('at-progress');
      if (bar) bar.style.width = `${Math.max(0, Math.min(100, Number(view.percent) || 0))}%`;
      const progress = document.getElementById('at-progress-text');
      if (progress) progress.textContent = view.progressText || '';
      setLine('at-current', view.currentText);
      setLine('at-estimate', view.estimateText);
      setLine('at-review', view.reviewText);
      setLine('at-failure', view.failureText);
      setLine('at-sample', view.sampleText);
      setLine('at-last-error', view.lastErrorText);
    } catch (_) { /* keep the last rendered state; retry on the next tick */ }
  }

  function schedule() {
    if (timer) { clearTimeout(timer); timer = null; }
    if (document.hidden) return;
    if (document.getElementById('ai-tagging-status')) timer = setTimeout(tick, 5000);
    else if (document.getElementById('at-summary')) timer = setTimeout(tick, 15000);
  }

  async function tick() {
    await refresh();
    schedule();
  }

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) { if (timer) { clearTimeout(timer); timer = null; } } else { tick(); }
  });
  document.addEventListener('032:pjax-applied', () => { tick(); });
  schedule();
})();

// -------------------------------------------------- Last.fm & ListenBrainz love sync poller
// The backfill is a long, explicitly started run, so the settings card refreshes
// its progress bar while a run is active (3 s) and falls back to a slow check
// otherwise so a run started in another tab still shows up.
(function () {
  'use strict';
  if (window.__032_lastfm_love_initialized) return;
  window.__032_lastfm_love_initialized = true;

  const setLine = (id, text) => {
    const el = document.getElementById(id);
    if (!el) return;
    el.textContent = text || '';
    el.hidden = !text;
  };

  function setupLovePoller(cfg) {
    let timer = null;

    function render(view) {
      const root = document.getElementById(cfg.rootId);
      if (!root) return;
      root.dataset.loveStatus = String(!!view.enabled);
      const pending = Number(view.pending) || 0;
      const failed = Number(view.failed) || 0;
      const queue = document.getElementById(cfg.prefix + 'love-queue-line');
      if (queue) {
        queue.textContent = pending
          ? `待同步 ${pending} 首${failed ? `（其中 ${failed} 首多次失败，正在放慢重试）` : ''}`
          : '';
        queue.hidden = pending === 0;
      }
      setLine(cfg.prefix + 'love-error-line', view.lastError ? `最近错误${view.lastErrorAt ? `（${view.lastErrorAt}）` : ''}：${view.lastError}` : '');

      const run = view.backfill || {};
      const active = run.state === 'running' || run.state === 'done' || run.state === 'cancelled';
      const bar = document.getElementById(cfg.prefix + 'love-backfill-bar');
      if (bar) bar.style.width = `${Math.max(0, Math.min(100, Number(run.percent) || 0))}%`;
      const wrapper = document.getElementById(cfg.prefix + 'love-backfill-progress');
      if (wrapper) wrapper.hidden = !active;
      const text = document.getElementById(cfg.prefix + 'love-backfill-text');
      if (text) {
        text.hidden = !active;
        if (active) {
          const state = run.state === 'running' ? ' · 进行中' : run.state === 'done' ? ' · 已完成' : ' · 已停止';
          const skippedNoId = Number(run.skippedNoId) || 0;
          const runFailed = Number(run.failed) || 0;
          if (cfg.hasSkippedNoID) {
            text.textContent = `已处理 ${Number(run.processed) || 0} / ${Number(run.total) || 0} 首（${(Number(run.percent) || 0).toFixed(1)}%）`
              + `${skippedNoId ? ` · 无 ID 跳过 ${skippedNoId} 首` : ''}`
              + `${runFailed ? ` · 失败 ${runFailed} 首` : ''}${state}`;
          } else {
            text.textContent = `已同步 ${Number(run.processed) || 0} / ${Number(run.total) || 0} 首（${(Number(run.percent) || 0).toFixed(1)}%）`
              + `${runFailed ? ` · 跳过 ${runFailed} 首` : ''}${state}`;
          }
        }
      }
      setLine(cfg.prefix + 'love-backfill-error', run.lastError);
      if (root.dataset.loveRunning === 'true' && run.state !== 'running') {
        window.location.reload();
        return;
      }
      root.dataset.loveRunning = String(run.state === 'running');
    }

    async function refresh() {
      const root = document.getElementById(cfg.rootId);
      if (!root) return;
      try {
        const response = await fetch(cfg.endpoint, { credentials: 'same-origin' });
        if (!response.ok) return;
        const body = await response.json();
        body.enabled = root.dataset.loveStatus === 'true';
        render(body);
      } catch (_) { /* keep the last rendered state; retry on the next tick */ }
    }

    function schedule() {
      if (timer) { clearTimeout(timer); timer = null; }
      if (document.hidden) return;
      const root = document.getElementById(cfg.rootId);
      if (!root) return;
      timer = setTimeout(tick, root.dataset.loveRunning === 'true' ? 3000 : 20000);
    }

    async function tick() {
      await refresh();
      schedule();
    }

    document.addEventListener('visibilitychange', () => {
      if (document.hidden) { if (timer) { clearTimeout(timer); timer = null; } } else { tick(); }
    });
    document.addEventListener('032:pjax-applied', () => { tick(); });
    schedule();
  }

  setupLovePoller({ rootId: 'lastfm-love', endpoint: '/admin/settings/lastfm-scrobble/love-status', prefix: '', hasSkippedNoID: false });
  setupLovePoller({ rootId: 'listenbrainz-love', endpoint: '/admin/settings/listenbrainz/love-status', prefix: 'lb-', hasSkippedNoID: true });
})();

// 大文件快照必须走 fetch + CSRF 头，不能由服务器预解析 multipart 表单。
document.addEventListener('submit', async (event) => {
  const form = event.target.closest('[data-backup-upload]');
  if (!form) return;
  event.preventDefault();
  const button = form.querySelector('button');
  const status = form.querySelector('[data-backup-upload-status]');
  button.disabled = true;
  status.textContent = '正在上传并校验，请勿关闭页面…';
  try {
    const response = await fetch(form.action, {
      method: 'POST', body: new FormData(form),
      headers: { 'X-CSRF-Token': document.querySelector('meta[name="csrf-token"]').content },
    });
    const body = await response.text();
    if (!response.ok) throw new Error(body);
    location.assign(JSON.parse(body).location);
  } catch (error) {
    status.textContent = `上传失败：${error.message}`;
    button.disabled = false;
  }
});

// 等待优雅停机后轮询健康接口；无守护进程时页面保留手动启动提示。
(() => {
  function pollRestart() {
    if (!document.querySelector('[data-backup-restarting]')) return;
    setTimeout(async function check() {
      try {
        const response = await fetch('/api/v1/health', { cache: 'no-store' });
        if (response.ok) { location.href = '/admin/backups'; return; }
        if (response.status === 503 && (await response.text()).trim() === 'manual recovery required') { location.href = '/'; return; }
      } catch (_) { /* 重启期间连接中断是预期行为。 */ }
      setTimeout(check, 1500);
    }, 3000);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', pollRestart);
  else pollRestart();
  document.addEventListener('032:pjax-applied', pollRestart);
})();

// Chinese artist alias completion: update state without replacing review forms
// or discarding an in-progress manual edit. Full results refresh explicitly.
(function () {
  'use strict';
  if (window.__032_artist_aliases_initialized) return;
  window.__032_artist_aliases_initialized = true;
  let timer = null;
  let busy = false;
  async function tick() {
    if (timer) clearTimeout(timer);
    timer = null;
    const state = document.getElementById('alias-worker-state');
    if (!state || document.hidden || busy) return;
    busy = true;
    try {
      const response = await fetch('/admin/artist-aliases/status', { credentials: 'same-origin' });
      if (response.ok && document.getElementById('alias-worker-state') === state) {
        const { settings, stats, detail } = await response.json();
        state.textContent = !settings.enabled ? '尚未开启' : (settings.paused ? '已暂停 · ' : '') + (detail || '等待后台任务');
        document.getElementById('alias-worker-progress').textContent = `待处理 ${stats.Queued} · 处理中 ${stats.Running} · 已处理中文名 ${stats.Filled} · 待审核 ${stats.Pending} · 未找到 ${stats.Missing} · 失败 ${stats.Failed}`;
        document.getElementById('alias-worker-usage').textContent = `累计搜索 ${stats.Searches} 次 · 网页读取 ${stats.Fetches} 次 · 模型调用 ${stats.Models} 次（含失败尝试）`;
      }
    } catch (_) { /* Preserve rendered data; the refresh control remains usable. */ }
    finally { busy = false; }
    if (!document.hidden && document.getElementById('alias-worker-state')) timer = setTimeout(tick, 5000);
  }
  document.addEventListener('032:pjax-applied', tick);
  document.addEventListener('visibilitychange', tick);
  tick();

  document.addEventListener('click', async (event) => {
    const btn = event.target.closest('[data-artist-image-web-search]');
    if (!btn) return;
    const artistId = btn.dataset.artistId;
    const form = document.getElementById('artist-image-approve-form');
    const csrf = form?.elements.namedItem('csrfToken')?.value;
    const box = btn.closest('fieldset')?.querySelector('[data-artist-image-web-result]');
    if (!artistId || !box || !form || !csrf) return;
    btn.disabled = true;
    box.textContent = '正在联网检索并核验候选头像...';
    try {
      const body = new URLSearchParams({ csrfToken: csrf });
      const resp = await fetch(`/admin/artists/${encodeURIComponent(artistId)}/image/web-search`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body,
      });
      if (!resp.ok) {
        box.textContent = '联网搜索失败，请检查 AI 与 DeepSeek 联网配置。';
        return;
      }
      const data = await resp.json();
      if (!data.found || !data.candidate || !data.candidate.ID) {
        box.textContent = '未找到符合身份锚点与画质要求的候选头像。';
        return;
      }
      const c = data.candidate;
      box.replaceChildren();
      const wrap = document.createElement('div');
      wrap.className = 'artist-image-candidate-summary';
      const img = document.createElement('img');
      const candidatePath = `/admin/matches/images/candidates/${encodeURIComponent(c.ID)}`;
      img.src = `${candidatePath}/preview`;
      img.alt = '候选头像';
      img.className = 'artist-image-candidate-preview';
      const info = document.createElement('span');
      info.textContent = `${c.Confidence || 'medium'} · ${c.Width || 0}×${c.Height || 0}${c.Reason ? ' · ' + c.Reason : ''}`;
      const approveBtn = document.createElement('button');
      approveBtn.type = 'submit';
      approveBtn.setAttribute('form', form.id);
      form.action = `${candidatePath}/approve`;
      approveBtn.textContent = '采纳为头像';
      wrap.append(img, info, approveBtn);
      box.append(wrap);
    } catch (_) {
      box.textContent = '联网搜索请求出错，请稍后重试。';
    } finally {
      btn.disabled = false;
    }
  });

})();

// ----------------------------------------- Artist Match Workbench (/admin/matches)
// Tab switching (client-side instant switch with URL ?tab= sync), AI/conflict
// filter chips on the current review page, inline 2-step confirmation for high-
// impact actions (reject-all / identity-conflict merge), and dialog backdrop close.
(function () {
  'use strict';
  if (window.__032_match_workbench_initialized) return;
  window.__032_match_workbench_initialized = true;

  function setMatchTab(root, tab, syncUrl) {
    if (!root || !tab) return;
    root.dataset.activeTab = tab;
    root.querySelectorAll('.match-tabs [data-match-tab]').forEach((btn) => {
      const on = btn.getAttribute('data-match-tab') === tab;
      btn.classList.toggle('active', on);
      btn.setAttribute('aria-selected', String(on));
    });
    root.querySelectorAll('[data-match-panel]').forEach((panel) => {
      panel.hidden = panel.getAttribute('data-match-panel') !== tab;
    });
    const baseUrl = new URL(window.location.href);
    baseUrl.searchParams.delete('notice');
    root.querySelectorAll('[data-match-return-to]').forEach((input) => {
      const fixedTab = input.getAttribute('data-match-return-to') || tab;
      const retUrl = new URL(baseUrl.href);
      if (fixedTab === 'identity' && !retUrl.searchParams.has('runsPage') && !retUrl.searchParams.has('creditsPage')) {
        retUrl.searchParams.delete('tab');
      } else {
        retUrl.searchParams.set('tab', fixedTab);
      }
      input.value = retUrl.pathname + retUrl.search;
    });
    if (syncUrl && window.history && window.history.replaceState) {
      const url = new URL(window.location.href);
      url.searchParams.delete('notice');
      if (tab === 'identity' && !url.searchParams.has('runsPage') && !url.searchParams.has('creditsPage')) {
        url.searchParams.delete('tab');
      } else {
        url.searchParams.set('tab', tab);
      }
      window.history.replaceState(window.history.state, '', url.href);
    }
  }

  function applyMatchFilter(root, filter) {
    if (!root) return;
    const cards = Array.from(root.querySelectorAll('[data-match-card]'));
    let visible = 0;
    cards.forEach((card) => {
      const bucket = card.getAttribute('data-ai-bucket') || 'uncertain';
      const conflict = card.getAttribute('data-has-conflict') === '1';
      const match = filter === 'all' || (filter === 'conflict' ? conflict : bucket === filter);
      card.hidden = !match;
      if (match) visible += 1;
    });
    root.querySelectorAll('[data-match-ai-filter]').forEach((chip) => {
      chip.classList.toggle('active', chip.getAttribute('data-match-ai-filter') === filter);
    });
    const clearBtn = root.querySelector('[data-match-filter-clear]');
    if (clearBtn) clearBtn.hidden = filter === 'all';
    const empty = root.querySelector('[data-match-filter-empty]');
    if (empty) empty.hidden = cards.length === 0 || visible > 0;
    const summary = root.querySelector('[data-match-filter-summary]');
    if (summary) {
      if (!summary.dataset.defaultText) summary.dataset.defaultText = summary.textContent;
      summary.textContent = filter === 'all'
        ? summary.dataset.defaultText
        : `当前筛选显示 ${visible} / 本页 ${cards.length} 位`;
    }
  }

  function disarmInlineConfirm(form) {
    if (!form) return;
    const btn = form.querySelector('button[data-confirm-armed="1"]');
    if (btn) {
      btn.removeAttribute('data-confirm-armed');
      if (btn.dataset.originalLabel) btn.textContent = btn.dataset.originalLabel;
    }
    form.querySelector('.match-inline-confirm-cancel')?.remove();
  }

  document.addEventListener('click', (e) => {
    if (!(e.target instanceof Element)) return;
    const root = document.querySelector('[data-match-workbench]');

    // Tab switch (both tab buttons and cross-tab jump buttons)
    const tabTrigger = e.target.closest('[data-match-tab]');
    if (tabTrigger && root && root.contains(tabTrigger)) {
      const targetTab = tabTrigger.getAttribute('data-match-tab');
      if (targetTab) setMatchTab(root, targetTab, true);
      return;
    }

    // AI / conflict filter chips
    const chip = e.target.closest('[data-match-ai-filter]');
    if (chip && root && root.contains(chip)) {
      applyMatchFilter(root, chip.getAttribute('data-match-ai-filter') || 'all');
      return;
    }
    if (e.target.closest('[data-match-filter-clear]') && root) {
      applyMatchFilter(root, 'all');
      return;
    }

    // Inline 2-step confirmation cancel
    const cancelBtn = e.target.closest('.match-inline-confirm-cancel');
    if (cancelBtn) {
      disarmInlineConfirm(cancelBtn.closest('form'));
      return;
    }

    // Inline 2-step confirmation first click arms; second click lets form submit
    const confirmBtn = e.target.closest('button[data-inline-confirm]');
    if (confirmBtn) {
      const form = confirmBtn.closest('form');
      if (confirmBtn.getAttribute('data-confirm-armed') !== '1') {
        e.preventDefault();
        // Disarm any other open confirmations on the page
        document.querySelectorAll('.match-inline-confirm-form').forEach((other) => {
          if (other !== form) disarmInlineConfirm(other);
        });
        confirmBtn.dataset.originalLabel = confirmBtn.textContent;
        confirmBtn.textContent = confirmBtn.getAttribute('data-inline-confirm') || '确认操作';
        confirmBtn.setAttribute('data-confirm-armed', '1');
        const cancel = document.createElement('button');
        cancel.type = 'button';
        cancel.className = 'match-inline-confirm-cancel compact-button';
        cancel.textContent = '取消';
        confirmBtn.after(cancel);
        return;
      }
    }

    // Credit split inline editor toggle / cancel
    const editToggle = e.target.closest('[data-credit-edit-toggle]');
    if (editToggle && root && root.contains(editToggle)) {
      const id = editToggle.getAttribute('data-credit-edit-toggle');
      const form = root.querySelector(`[data-credit-edit-form="${id}"]`);
      if (form) {
        form.hidden = !form.hidden;
        if (!form.hidden) form.querySelector('input[name="parts"]')?.focus();
      }
      return;
    }
    const editCancel = e.target.closest('[data-credit-edit-cancel]');
    if (editCancel && root && root.contains(editCancel)) {
      const id = editCancel.getAttribute('data-credit-edit-cancel');
      const form = root.querySelector(`[data-credit-edit-form="${id}"]`);
      if (form) form.hidden = true;
      return;
    }

    // Close settings dialog when clicking the backdrop
    if (e.target instanceof HTMLDialogElement && e.target.classList.contains('match-settings-dialog')) {
      e.target.close();
    }
  });

  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      document.querySelectorAll('.match-inline-confirm-form').forEach(disarmInlineConfirm);
    }
  });
})();
