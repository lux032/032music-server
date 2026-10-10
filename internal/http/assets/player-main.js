// 032 Music Server - player entry point
import { createPlayerDOM } from './player-bar.js';
import { setupAudioElement, setupPlaybackReporter, restoreState, saveState } from './player-core.js';
import { setupNowPlayingPanel } from './now-playing.js';
import { bindTrackListEvents } from './queue.js';
import { bindGlobalShortcuts } from './shortcuts.js';
import { setupPjaxNavigation } from './router.js';
import { setupImageFadeIn } from './util.js';
import { setupPlaylists } from './playlist-picker.js';
import { setupFullscreen } from './fullscreen.js';
import { setupCast, restoreCast } from './cast.js';

if (!window.__032_player_initialized) {
  window.__032_player_initialized = true;
  function init() {
    if (!document.body || document.body.classList.contains('login-page')) return;
    createPlayerDOM();
    setupAudioElement();
    setupPlaybackReporter();
    setupNowPlayingPanel();
    setupPlaylists();
    setupFullscreen();
    setupCast();
    // Before restoreState: a restored cast session must not start local audio.
    restoreCast();
    restoreState();
    bindTrackListEvents();
    bindGlobalShortcuts();
    setupPjaxNavigation();
    setupImageFadeIn();
    requestAnimationFrame(() => requestAnimationFrame(() => {
      document.body.classList.add('np-animated');
    }));
    window.addEventListener('pagehide', saveState);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
}
