// 032 Music Server - shortcuts.js
import { syncPanelA11y } from './now-playing.js';
import { togglePlay } from './player-core.js';

  export function bindGlobalShortcuts() {
    document.addEventListener('keydown', (e) => {
      const target = e.target instanceof Element ? e.target : document.activeElement;
      const interactive = target?.closest('input, textarea, select, button, a, summary, [contenteditable="true"], [role="slider"]');
      if (e.key === 'Escape') {
        if (document.getElementById('lyrics-overlay')?.style.display !== 'none') {
          e.preventDefault(); document.getElementById('lyrics-close-btn')?.click(); return;
        }
        // [M2] Esc closes the mobile queue drawer and returns focus to its
        // trigger button.
        if (document.body.classList.contains('np-open')) {
          e.preventDefault();
          document.body.classList.remove('np-open');
          syncPanelA11y();
          const queueBtn = document.getElementById('player-btn-queue');
          if (queueBtn) { queueBtn.setAttribute('aria-expanded', 'false'); queueBtn.focus(); }
          return;
        }
      }
      if (localStorage.getItem('032_shortcuts_disabled') === '1' || interactive || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey || e.isComposing) return;
      if (e.code === 'Space') { e.preventDefault(); togglePlay(); }
      else if (e.key === 'l' || e.key === 'L') { e.preventDefault(); document.getElementById('player-btn-lyrics')?.click(); }
    });
  }

  // ------------------------------------------------------------- image fade
