// 032 Music Server - util.js


  function csrfToken() {
    const meta = document.querySelector('meta[name="csrf-token"]');
    return meta ? (meta.getAttribute('content') || '') : '';
  }

  // apiFetch wraps fetch for /api/v1 calls: session-authenticated state
  // changing requests must echo the session CSRF token in a header.
  export function apiFetch(url, options) {
    const opts = Object.assign({ credentials: 'same-origin' }, options);
    const method = (opts.method || 'GET').toUpperCase();
    const headers = Object.assign({}, opts.headers);
    if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) {
      const token = csrfToken();
      if (token) headers['X-CSRF-Token'] = token;
    }
    opts.headers = headers;
    return fetch(url, opts);
  }

  function spriteURL() {
    const meta = document.querySelector('meta[name="icon-sprite"]');
    return meta ? (meta.getAttribute('content') || '') : '/admin/assets/icons.svg';
  }

  // svgIcon builds an icon node via DOM APIs; icon names are internal
  // constants, never user data.
  export function svgIcon(name) {
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('class', 'icon');
    svg.setAttribute('aria-hidden', 'true');
    const use = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    use.setAttribute('href', spriteURL() + '#' + name);
    svg.appendChild(use);
    return svg;
  }

  // swapIcon replaces the glyph of an icon button in place.
  export function swapIcon(btn, name) {
    const use = btn ? btn.querySelector('use') : null;
    if (use) use.setAttribute('href', spriteURL() + '#' + name);
  }

  // hydrateIconSlots replaces static <span class="icon-slot" data-icon>
  // placeholders inside freshly built chrome with real SVG nodes.
  export function hydrateIconSlots(root) {
    root.querySelectorAll('.icon-slot[data-icon]').forEach((slot) => {
      slot.replaceWith(svgIcon(slot.dataset.icon));
    });
  }

  export function artworkForSize(artwork, size) {
    if (!artwork) return '';
    if (/([?&])size=\d+/.test(artwork)) return artwork.replace(/([?&])size=\d+/, `$1size=${size}`);
    return artwork + (artwork.includes('?') ? '&' : '?') + 'size=' + size;
  }

  export function setupImageFadeIn() {
    // load/error do not bubble; listen in the capture phase at the root.
    document.addEventListener('load', (e) => {
      if (e.target instanceof HTMLImageElement) e.target.classList.add('is-loaded');
    }, true);
    document.addEventListener('error', (e) => {
      if (e.target instanceof HTMLImageElement) e.target.classList.add('is-loaded');
    }, true);
    markLoadedImages(document);
  }

  export function markLoadedImages(root) {
    root.querySelectorAll('img.fade-img').forEach((img) => {
      if (img.complete) img.classList.add('is-loaded');
    });
  }

  // ------------------------------------------------------------------- misc
  // [P3] m:ss everywhere (h:mm:ss past one hour), matching the Go-side
  // formatDurationMillis used by album/work detail pages.
  export function formatTime(seconds) {
    if (isNaN(seconds) || seconds < 0) return '0:00';
    const totalSeconds = Math.floor(seconds);
    const hours = Math.floor(totalSeconds / 3600);
    const mins = Math.floor((totalSeconds % 3600) / 60);
    const secs = totalSeconds % 60;
    if (hours > 0) return `${hours}:${String(mins).padStart(2, '0')}:${String(secs).padStart(2, '0')}`;
    return `${mins}:${String(secs).padStart(2, '0')}`;
  }

  let toastTimer = null;

  // showActionToast shows a persistent message with one or more recovery
  // actions; it hides itself after timeoutMs unless replaced first. The
  // returned function dismisses it early.
  export function showActionToast(msg, actions, timeoutMs) {
    showToast(msg, true);
    const toast = document.querySelector('.client-toast');
    const group = document.createElement('span');
    group.className = 'toast-actions';
    const dismiss = () => {
      clearTimeout(timer);
      const active = group.isConnected;
      group.remove();
      if (active && toast.textContent === msg) { toast.style.display = 'none'; toast.textContent = ''; }
    };
    for (const action of actions) {
      const button = document.createElement('button');
      button.type = 'button';
      button.textContent = action.label;
      button.addEventListener('click', () => { if (group.isConnected) { dismiss(); action.run(); } });
      group.appendChild(button);
    }
    toast.appendChild(group);
    const timer = setTimeout(dismiss, timeoutMs || 5000);
    return dismiss;
  }

  export function showUndoToast(msg, undo) {
    return showActionToast(msg, [{ label: '撤销', run: undo }], 5000);
  }

  export function showToast(msg, persistent) {
    let toast = document.querySelector('.client-toast');
    if (!toast) {
      toast = document.createElement('div');
      toast.className = 'toast client-toast';
      document.body.appendChild(toast);
    }
    toast.setAttribute('aria-live', persistent ? 'assertive' : 'polite');
    toast.setAttribute('role', persistent ? 'alert' : 'status');
    toast.textContent = msg;
    toast.style.display = 'block';
    if (toastTimer) { clearTimeout(toastTimer); toastTimer = null; }
    if (!persistent) {
      // Guard by message so a stale transient timer can never hide a newer
      // (possibly persistent) notification.
      toastTimer = setTimeout(() => {
        toastTimer = null;
        // Clear the text too: a hidden toast must not keep announcing or
        // exposing a stale message.
        if (toast.textContent === msg) { toast.style.display = 'none'; toast.textContent = ''; }
      }, 3500);

  }
  }
