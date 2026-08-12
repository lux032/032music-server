const ids = {
  albums: document.querySelector('#albums-count'),
  artists: document.querySelector('#artists-count'),
  tracks: document.querySelector('#tracks-count'),
  audioFiles: document.querySelector('#files-count')
};
const title = document.querySelector('#scan-title');
if (title) {
  const refresh = async () => {
    try {
      const response = await fetch('/admin/status', {credentials: 'same-origin'});
      if (!response.ok) return;
      const value = await response.json();
      Object.entries(ids).forEach(([key, element]) => { if (element) element.textContent = value.statistics[key]; });
      const scan = value.scan;
      title.textContent = `扫描状态：${scan.status || 'never'}`;
      document.querySelector('#scan-path').textContent = scan.currentRelativePath || '';
      document.querySelector('#scan-detail').textContent = `发现 ${scan.discoveredFiles || 0} · 已处理 ${scan.processedFiles || 0} · 跳过 ${scan.skippedFiles || 0} · 失败 ${scan.failedFiles || 0} · 丢失 ${scan.missingFiles || 0}`;
      const percent = scan.discoveredFiles ? Math.min(100, ((scan.processedFiles + scan.skippedFiles + scan.failedFiles) / scan.discoveredFiles) * 100) : 0;
      document.querySelector('#scan-progress').style.width = `${percent}%`;
    } catch (_) { /* 下次轮询重试 */ }
  };
  refresh();
  window.setInterval(refresh, 1200);
}
