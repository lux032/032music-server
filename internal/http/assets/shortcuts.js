// 032 Music Server - shortcuts.js
import { syncPanelA11y, getNowPlayingTab, setNowPlayingTab } from './now-playing.js';
import { togglePlay } from './player-core.js';

  export function bindGlobalShortcuts() {
    document.addEventListener('keydown', (e) => {
      const target = e.target instanceof Element ? e.target : document.activeElement;
      const interactive = target?.closest('input, textarea, select, button, a, summary, [contenteditable="true"], [role="slider"]');
      if (e.defaultPrevented) return;
      if (e.key === 'Escape') {
        const overlay = document.getElementById('lyrics-overlay');
        if (overlay && overlay.style.display !== 'none') {
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
        if (getNowPlayingTab() === 'lyrics' && !document.body.classList.contains('np-collapsed')) {
          e.preventDefault();
          setNowPlayingTab('queue');
          return;
        }
      }
      const search = (e.key === '/' && !e.ctrlKey && !e.metaKey && !e.shiftKey) ||
        (e.key.toLowerCase() === 'k' && (e.ctrlKey || e.metaKey));
      const editing = target?.closest('input, textarea, select, [contenteditable="true"]');
      if (search && !e.altKey && !e.isComposing && !editing &&
          !document.querySelector('dialog[open], #player-fullscreen, .drawer-panel[aria-modal="true"]') &&
          localStorage.getItem('032_shortcuts_disabled') !== '1') {
        e.preventDefault();
        const input = document.getElementById('library-search');
        if (input) input.focus();
        else document.getElementById('sidebar-search')?.click();
        return;
      }
      if (localStorage.getItem('032_shortcuts_disabled') === '1' || interactive || e.ctrlKey || e.metaKey || e.altKey || e.shiftKey || e.isComposing) return;
      if (e.code === 'Space') { e.preventDefault(); togglePlay(); }
      else if (e.key === 'l' || e.key === 'L') { e.preventDefault(); document.getElementById('player-btn-lyrics')?.click(); }
    });
  }

  // ------------------------------------------------------------- image fade
