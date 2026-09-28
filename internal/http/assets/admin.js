// 032 Music Server - Admin shell behaviours: adaptive scan-status polling
// (drives both the sidebar scan card and the dashboard page), collapsible
// sidebar admin group, unsaved-change highlighting on source cards.
(function () {
  'use strict';

  if (window.__032_admin_initialized) return;
  window.__032_admin_initialized = true;

  // ------------------------------------------------- sidebar admin group
  const GROUP_KEY = '032_admin_group';
  const ADMIN_NAV_KEYS = ['console', 'matches', 'work-review', 'enrichment', 'merges', 'settings', 'security'];

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
})();
