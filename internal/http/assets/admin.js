// Dashboard scan-status polling. PJAX-safe: elements are re-queried on every
// tick and polling is skipped whenever the dashboard is not the current page.
(function () {
  'use strict';

  if (window.__032_admin_initialized) return;
  window.__032_admin_initialized = true;

  const refresh = async () => {
    const title = document.querySelector('#scan-title');
    if (!title) return;
    try {
      const response = await fetch('/admin/status', { credentials: 'same-origin' });
      if (!response.ok) return;
      const value = await response.json();
      const ids = {
        albums: document.querySelector('#albums-count'),
        artists: document.querySelector('#artists-count'),
        tracks: document.querySelector('#tracks-count'),
        audioFiles: document.querySelector('#files-count')
      };
      Object.entries(ids).forEach(([key, element]) => { if (element) element.textContent = value.statistics[key]; });
      const scan = value.scan;
      title.textContent = `扫描状态：${scan.status || 'never'}`;
      const path = document.querySelector('#scan-path');
      if (path) path.textContent = scan.currentRelativePath || '';
      const detail = document.querySelector('#scan-detail');
      if (detail) detail.textContent = `发现 ${scan.discoveredFiles || 0} · 已处理 ${scan.processedFiles || 0} · 跳过 ${scan.skippedFiles || 0} · 失败 ${scan.failedFiles || 0} · 丢失 ${scan.missingFiles || 0}`;
      const progress = document.querySelector('#scan-progress');
      if (progress) {
        const percent = scan.discoveredFiles ? Math.min(100, ((scan.processedFiles + scan.skippedFiles + scan.failedFiles) / scan.discoveredFiles) * 100) : 0;
        progress.style.width = `${percent}%`;
      }
    } catch (_) { /* 下次轮询重试 */ }
  };

  refresh();
  window.setInterval(refresh, 1200);
})();
