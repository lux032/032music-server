(() => {
  function init() {
    const root = document.querySelector('[data-acquisition-tests]');
    if (!root || root.dataset.initialized) return;
    root.dataset.initialized = 'true';
    const status = root.querySelector('[data-test-status]');
    root.addEventListener('click', async (event) => {
      const button = event.target.closest('[data-test]');
      if (!button || button.disabled) return;
      button.disabled = true;
      status.textContent = '正在测试已保存的设置…';
      try {
        const response = await fetch('/admin/settings/acquisition/' + button.dataset.test, {
          method: 'POST', credentials: 'same-origin',
          headers: { 'X-CSRF-Token': document.querySelector('input[name="csrfToken"]').value }
        });
        const body = await response.json();
        status.textContent = response.ok ? '连接测试成功' : (body.error?.message || '连接测试失败');
      } catch (_) {
        status.textContent = '连接测试失败，请稍后重试';
      } finally {
        button.disabled = false;
      }
    });
  }
  document.addEventListener('DOMContentLoaded', init);
  document.addEventListener('032:pjax-applied', init);
  if (document.readyState !== 'loading') init();
})();
