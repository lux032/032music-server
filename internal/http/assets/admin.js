// Dashboard scan-status polling. PJAX-safe: elements are re-queried on every
// tick and polling is skipped whenever the dashboard is not the current page.
(function () {
  'use strict';

  if (window.__032_admin_initialized) return;
  window.__032_admin_initialized = true;

  const statusMap = {
    never: '尚未扫描',
    running: '正在扫描',
    completed: '扫描完成',
    failed: '扫描失败'
  };

  const refresh = async () => {
    const title = document.querySelector('#scan-title');
    if (!title) return;
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
      const statusText = statusMap[scan.status] || scan.status || 'never';
      title.textContent = `扫描状态：${statusText}`;
      if (lastUpdated) {
        const now = new Date();
        lastUpdated.textContent = `最后更新：${now.getHours().toString().padStart(2,'0')}:${now.getMinutes().toString().padStart(2,'0')}:${now.getSeconds().toString().padStart(2,'0')}`;
      }
      const path = document.querySelector('#scan-path');
      if (path) path.textContent = scan.currentRelativePath || '';
      const detail = document.querySelector('#scan-detail');
      if (detail) detail.textContent = `发现 ${scan.discoveredFiles || 0} · 已处理 ${scan.processedFiles || 0} · 跳过 ${scan.skippedFiles || 0} · 失败 ${scan.failedFiles || 0} · 丢失 ${scan.missingFiles || 0}`;
      const progress = document.querySelector('#scan-progress');
      if (progress) {
        const percent = scan.discoveredFiles ? Math.min(100, ((scan.processedFiles + scan.skippedFiles + scan.failedFiles) / scan.discoveredFiles) * 100) : 0;
        progress.style.width = `${percent}%`;
      }
    } catch (_) {
      if (lastUpdated) lastUpdated.textContent = '状态更新中断，重试连接中…';
    }
  };

  refresh();
  window.setInterval(refresh, 1200);
})();

// 来源卡片未保存更改高亮
document.addEventListener('input', (e) => {
  const form = e.target.closest ? e.target.closest('form[data-scope-form]') : null;
  if (!form) return;
  const btn = form.querySelector('button[type="submit"]');
  if (btn && !btn.classList.contains('is-unsaved')) {
    btn.classList.add('is-unsaved');
    btn.style.borderColor = 'var(--accent)';
    btn.style.boxShadow = '0 0 0 1px var(--accent)';
  }
});
