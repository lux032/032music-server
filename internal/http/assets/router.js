// 032 Music Server - router.js
import { showToast, markLoadedImages } from './util.js';
import { decorateTrackRows } from './queue.js';
import { updateTrackRowsUI } from './player-bar.js';

  let navigationAbortController = null;
  let currentDocUrl = window.location.href;
  const pendingForms = new Map();
  const draftStorageKey = '032_form_draft';

  export function setupPjaxNavigation() {
    if (!window.fetch || !window.history || !window.DOMParser) return;
    ensureCurrentHistoryState();
    restoreSafeDraft();
    document.addEventListener('click', (e) => {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      const link = e.target instanceof Element ? e.target.closest('a[href]') : null;
      if (!link || (link.target && link.target !== '_self') || link.hasAttribute('download')) return;
      let url;
      try { url = new URL(link.href, window.location.href); } catch (_) { return; }
      if (url.origin !== window.location.origin || !url.pathname.startsWith('/admin') || url.pathname.startsWith('/admin/assets/')) return;
      const current = new URL(window.location.href);
      if (url.pathname === current.pathname && url.search === current.search && url.hash) return;
      if (pendingForms.size && !window.confirm('更改仍在保存中。现在离开可能无法确认保存结果。是否仍要离开？')) { e.preventDefault(); return; }
      e.preventDefault();
      pjaxNavigate(url.href, true);
    });
    document.addEventListener('click', (e) => {
      const cancel = e.target instanceof Element ? e.target.closest('[data-cancel-confirm]') : null;
      if (cancel) cancel.closest('details')?.removeAttribute('open');
    });
    document.addEventListener('submit', (e) => {
      const form = e.target;
      // data-no-pjax forms need a real navigation (e.g. a redirect to an
      // external authorisation page that fetch() could not follow).
      if (!(form instanceof HTMLFormElement) || e.defaultPrevented || form.hasAttribute('data-no-pjax')) return;
      const method = (form.getAttribute('method') || 'get').toLowerCase();
      let action;
      try { action = new URL(form.getAttribute('action') || window.location.href, window.location.href); } catch (_) { return; }
      // M1: check form ownership (form.elements includes form=-associated
      // controls), not DOM containment — a file input living inside another
      // form's markup must not mark that form as a file form.
      if (action.origin !== window.location.origin || !action.pathname.startsWith('/admin') || action.pathname === '/admin/logout' || Array.from(form.elements).some((el) => el.type === 'file')) return;
      const submitter = e.submitter;
      const data = new FormData(form);
      if (submitter && submitter.name) data.append(submitter.name, submitter.value);
      const params = new URLSearchParams(data);
      e.preventDefault();
      if (method === 'post') {
        if (!pendingForms.has(form)) pjaxPost(action.href, params, form, submitter);
      } else {
        action.search = params.toString();
        pjaxNavigate(action.href, true);
      }
    });
    window.addEventListener('popstate', (e) => {
      // Chromium fires popstate for same-document fragment changes when history.state
      // is non-null; those must not trigger a PJAX fetch of the same page.
      const prev = new URL(currentDocUrl);
      const next = new URL(window.location.href);
      currentDocUrl = window.location.href;
      if (prev.pathname === next.pathname && prev.search === next.search) return;
      pjaxNavigate(window.location.href, false, e.state);
    });
  }

  function pageState() {
    const active = document.activeElement;
    let focus = '';
    if (active instanceof Element && active.id) focus = '#' + CSS.escape(active.id);
    else if (active instanceof Element && active.closest('[data-track-id]')) focus = `[data-track-id="${CSS.escape(active.closest('[data-track-id]').dataset.trackId || '')}"] .row-play-btn`;
    return { app: '032', url: window.location.href, scrollX: window.scrollX, scrollY: window.scrollY, focus };
  }
  function ensureCurrentHistoryState() { history.replaceState({ ...(history.state || {}), ...pageState() }, '', window.location.href); }
  function saveCurrentHistoryState() { history.replaceState({ ...(history.state || {}), ...pageState() }, '', window.location.href); }

  // ------------------------------------------------------------ progress bar
  let progressShowTimer = null;
  function startNavProgress() {
    // [M1] Rapid back-to-back navigations share one bar; a stale show-timer
    // from the previous navigation must never fire into the new one.
    if (progressShowTimer) { clearTimeout(progressShowTimer); progressShowTimer = null; }
    let bar = document.getElementById('pjax-progress');
    if (!bar) {
      bar = document.createElement('div');
      bar.id = 'pjax-progress';
      document.body.appendChild(bar);
    }
    bar.classList.remove('done', 'active');
    bar.style.width = '0';
    // Fast requests never show the bar; it appears only once navigation
    // has taken noticeably long.
    progressShowTimer = setTimeout(() => {
      bar.classList.add('active');
      bar.style.width = '35%';
    }, 150);
  }
  function finishNavProgress() {
    if (progressShowTimer) { clearTimeout(progressShowTimer); progressShowTimer = null; }
    const bar = document.getElementById('pjax-progress');
    if (!bar || !bar.classList.contains('active')) return;
    bar.classList.remove('active');
    bar.classList.add('done');
    bar.style.width = '100%';
    setTimeout(() => {
      bar.classList.remove('done');
      bar.style.width = '0';
    }, 500);
  }

  async function pjaxNavigate(url, push, restoreState) {
    // Declared outside try so finally can compare against the current
    // controller (const inside try would be out of scope in finally).
    let controller = null;
    try {
      saveCurrentHistoryState();
      if (navigationAbortController) navigationAbortController.abort();
      controller = new AbortController();
      navigationAbortController = controller;
      startNavProgress();
      const res = await fetch(url, { credentials: 'same-origin', headers: { Accept: 'text/html' }, signal: controller.signal });
      const html = await res.text();
      // applyPage returns true on success, 'reload' on a build mismatch and
      // false on structural failures; the latter two both need a full
      // navigation, which also picks up fresh assets on a new deployment.
      if (!res.ok) { window.location.href = res.url || url; return; }
      // fetch response URLs omit fragments: preserve the requested in-page target.
      const destinationURL = new URL(res.url || url, window.location.href);
      const requestedURL = new URL(url, window.location.href);
      if (!destinationURL.hash && requestedURL.hash) destinationURL.hash = requestedURL.hash;
      const destination = destinationURL.href;
      // A composing search must keep its live input; the browse controls
      // cancel only their own stale response, not unrelated navigation.
      if (!document.dispatchEvent(new CustomEvent('032:pjax-before-swap', { cancelable: true, detail: { url: destination } }))) return;
      if (applyPage(html, destination, push, restoreState) !== true) window.location.href = destination;
    } catch (err) {
      if (err && err.name === 'AbortError') return;
      window.location.href = url;
    } finally {
      // [M1] A superseded navigation must not finish the progress bar its
      // successor is still using.
      if (controller && navigationAbortController === controller) finishNavProgress();
    }
  }

  export function navigateFromPlayer(url) { return pjaxNavigate(url, true); }

  window.__032BrowseNavigate = (url, replace) => {
    if (pendingForms.size && !window.confirm('更改仍在保存中。现在离开可能无法确认保存结果。是否仍要离开？')) return false;
    return pjaxNavigate(url, !replace, replace ? { app: '032', url } : null);
  };

  function setFormPending(form, submitter, pending) {
    form.classList.toggle('is-pending', pending);
    form.setAttribute('aria-busy', String(pending));
    form.querySelectorAll('button[type="submit"], button:not([type]), input[type="submit"]').forEach((button) => {
      if (pending) { button.dataset.wasDisabled = button.disabled ? '1' : '0'; button.disabled = true; }
      else if (button.dataset.wasDisabled !== '1') button.disabled = false;
    });
    // Icon-only buttons keep their icon; replacing textContent would destroy the
    // <svg> for good (the label restore only puts back text).
    if (submitter instanceof HTMLButtonElement && !submitter.querySelector('svg')) {
      if (pending) { submitter.dataset.originalText = submitter.textContent; submitter.textContent = '保存中…'; }
      else if (submitter.dataset.originalText) { submitter.textContent = submitter.dataset.originalText; delete submitter.dataset.originalText; }
    }
  }

  async function pjaxPost(url, params, form, submitter) {
    setFormPending(form, submitter, true);
    pendingForms.set(form, true);
    try {
      const res = await fetch(url, { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/x-www-form-urlencoded', Accept: 'text/html' }, body: params.toString() });
      const html = await res.text();
      const doc = new DOMParser().parseFromString(html, 'text/html');
      if (doc.body && doc.body.classList.contains('login-page')) {
        storeSafeDraft(form);
        // res.url can still equal the POST target when the redirect is mocked or
        // not followed; never navigate back to the form action in that case.
        const target = res.url && res.url !== url ? res.url : '/admin/login';
        window.location.href = target;
        return;
      }
      if (!res.ok) {
        // Plain-text bodies come from http.Error and carry the actual reason
        // (e.g. "本地作品 #… 不存在"); HTML error pages are never shown raw.
        const plain = /^text\/plain/i.test(res.headers.get('Content-Type') || '');
        const detail = plain ? html.trim().replace(/[。.]$/, '') : '';
        const reason = detail && detail.length <= 200 && !detail.includes('\n') ? `：${detail}` : '';
        showFormError(form, res.status === 403 ? '操作被拒绝，请刷新页面后重试。输入内容已保留。' : `保存失败（${res.status}）${reason}。输入内容已保留。`);
        return;
      }
      sessionStorage.removeItem(draftStorageKey);
      const favoriteMatch = /^\/admin\/favorites\/(albums|artists|tracks)\/(\d+)$/.exec(new URL(url, window.location.href).pathname);
      if (favoriteMatch) document.dispatchEvent(new CustomEvent('032:favorite-changed', { detail: { kind: favoriteMatch[1], id: favoriteMatch[2], favorite: params.get('favorite') === '1' } }));
      // POST-redirect-GET back onto the same page (typically with ?notice=)
      // is a refresh, not a new history step: pushing it made "返回" need two
      // clicks (the first only went back to the pre-save copy of this page).
      const resultURL = new URL(res.url || window.location.href, window.location.href);
      const samePage = resultURL.pathname === window.location.pathname;
      const applied = applyPage(html, resultURL.href, !samePage, samePage ? { ...(history.state || {}), app: '032', url: resultURL.href, scrollX: window.scrollX, scrollY: window.scrollY, focus: '' } : null);
      if (applied === 'reload') {
        // The write succeeded but the response belongs to a newer build:
        // navigate to the result page directly instead of reporting a
        // failure (the form must not look retryable, it already committed).
        window.location.href = res.url || url;
        return;
      }
      if (!applied) showFormError(form, '服务器已响应，但页面无法更新。请刷新核对保存结果。');
    } catch (_) {
      showFormError(form, '无法确认保存结果。输入内容已保留；请核对后再决定是否重试。');
    } finally {
      pendingForms.delete(form);
      if (document.contains(form)) setFormPending(form, submitter, false);
    }
  }

  function showFormError(form, message) {
    // Controls bound with form="…" live inside another form's markup, so the
    // owning (often empty) form names where its error belongs.
    let host = form;
    try { host = (form.dataset.errorTarget && document.querySelector(form.dataset.errorTarget)) || form; } catch (_) { host = form; }
    let error = host.querySelector('.form-error');
    if (!error) { error = document.createElement('div'); error.className = 'form-error'; error.setAttribute('role', 'alert'); host.prepend(error); }
    error.textContent = message; error.tabIndex = -1; error.focus(); showToast(message, true);
  }
  function safeDraftFields(form) {
    return Array.from(new FormData(form).entries()).filter(([name]) => !/password|token|secret|key|csrf/i.test(name)).map(([name, value]) => [name, String(value)]);
  }
  function storeSafeDraft(form) {
    try { sessionStorage.setItem(draftStorageKey, JSON.stringify({ page: window.location.pathname + window.location.search, action: form.action, fields: safeDraftFields(form) })); } catch (_) {}
  }
  function restoreSafeDraft() {
    try {
      const raw = sessionStorage.getItem(draftStorageKey); if (!raw) return;
      const draft = JSON.parse(raw); if (draft.page !== window.location.pathname + window.location.search) return;
      const form = Array.from(document.forms).find((candidate) => candidate.action === draft.action); if (!form) return;
      draft.fields.forEach(([name, value]) => { const field = form.elements.namedItem(name); if (field && !/password|token|secret|key|csrf/i.test(name)) field.value = value; });
      showToast('已恢复登录前的非敏感输入，请核对后手动保存。', true); sessionStorage.removeItem(draftStorageKey);
    } catch (_) { sessionStorage.removeItem(draftStorageKey); }
  }

  const ADMIN_NAV_KEYS = ['console', 'matches', 'work-review', 'series', 'enrichment', 'merges', 'settings', 'security'];

  // syncChrome copies just the per-page bits of the persistent shell from
  // the freshly fetched document: the navigation highlight, the admin
  // group expansion and the logout form's CSRF field. The sidebar itself
  // is never rebuilt, so it does not flash on navigation.
  function syncChrome(doc) {
    const nav = (doc.body && doc.body.dataset && doc.body.dataset.nav) || '';
    document.body.dataset.nav = nav;
    document.querySelectorAll('.sidebar [data-nav]').forEach((link) => {
      const active = link.getAttribute('data-nav') === nav;
      link.classList.toggle('active', active);
      if (active) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
    const group = document.getElementById('sidebar-admin-group');
    if (group) {
      const toggle = document.getElementById('sidebar-group-toggle');
      if (ADMIN_NAV_KEYS.includes(nav)) {
        group.classList.add('open');
        if (toggle) toggle.setAttribute('aria-expanded', 'true');
      } else {
        let stored = null;
        try { stored = localStorage.getItem('032_admin_group'); } catch (_) {}
        const open = stored === 'open';
        group.classList.toggle('open', open);
        if (toggle) toggle.setAttribute('aria-expanded', String(open));
      }
    }
    // The logged-in username can change on the account & security page.
    const incomingUser = doc.querySelector('.sidebar-user > span');
    if (incomingUser) {
      document.querySelectorAll('.sidebar-user > span, .sheet-user > span').forEach((el) => { el.textContent = incomingUser.textContent; });
    }
    const incomingCsrfField = doc.querySelector('.sidebar input[name="csrfToken"]');
    if (incomingCsrfField) {
      document.querySelectorAll('.sidebar input[name="csrfToken"]').forEach((field) => { field.value = incomingCsrfField.value; });
    }
    const incomingBadge = doc.querySelector('.sidebar a[data-nav="work-review"] .nav-badge');
    document.querySelectorAll('.sidebar a[data-nav="work-review"], .sidebar-more-sheet a[data-nav="work-review"]').forEach((link) => {
      const existing = link.querySelector('.nav-badge');
      if (incomingBadge && incomingBadge.textContent.trim()) {
        if (existing) {
          existing.textContent = incomingBadge.textContent;
        } else {
          const span = document.createElement('span');
          span.className = 'nav-badge';
          span.textContent = incomingBadge.textContent;
          link.appendChild(span);
        }
      } else if (existing) {
        existing.remove();
      }
    });
  }

  function applyPage(html, url, push, restoreState) {
    currentDocUrl = url;
    const doc = new DOMParser().parseFromString(html, 'text/html');
    if (!doc.body || doc.body.classList.contains('login-page')) return false;
    // A new server build ships assets under a different content hash; the
    // running JS can no longer trust the freshly fetched DOM, so fall back
    // to a full navigation instead of stitching incompatible versions.
    const currentBuild = document.querySelector('meta[name="app-build"]')?.getAttribute('content');
    const incomingBuild = doc.querySelector('meta[name="app-build"]')?.getAttribute('content');
    if (currentBuild && incomingBuild && currentBuild !== incomingBuild) return 'reload';
    // <head> survives PJAX swaps, so the session CSRF token must be copied
    // over manually before any later apiFetch call reads it.
    const incomingCsrf = doc.querySelector('meta[name="csrf-token"]')?.getAttribute('content');
    if (incomingCsrf) {
      const currentCsrf = document.querySelector('meta[name="csrf-token"]');
      if (currentCsrf) currentCsrf.setAttribute('content', incomingCsrf);
    }
    const newMain = doc.querySelector('main#app-main'), curMain = document.querySelector('main#app-main');
    if (!newMain || !curMain) return false;
    doc.querySelectorAll('[autofocus]').forEach((el) => el.removeAttribute('autofocus'));
    document.title = doc.title || document.title;
    // Keep the shell state classes the page cannot know about.
    const keepShellClasses = ['has-global-player', 'np-collapsed', 'np-open', 'np-animated'];
    const kept = keepShellClasses.filter((cls) => document.body.classList.contains(cls));
    document.body.className = doc.body.className;
    kept.forEach((cls) => document.body.classList.add(cls));
    const adoptedMain = document.adoptNode(newMain);
    curMain.replaceWith(adoptedMain);
    if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      adoptedMain.classList.add('pjax-enter');
      adoptedMain.addEventListener('animationend', () => adoptedMain.classList.remove('pjax-enter'), { once: true });
    }
    syncChrome(doc);
    doc.querySelectorAll('script[src]').forEach((script) => { const src = script.getAttribute('src'); if (!src || document.querySelector(`script[src="${src}"]`)) return; const el = document.createElement('script'); if (script.type === 'module') el.type = 'module'; el.src = src; document.head.appendChild(el); });
    const destination = new URL(url, window.location.href);
    if (push && destination.href !== window.location.href) history.pushState({ app: '032', url: destination.href, previousURL: window.location.href, scrollX: 0, scrollY: 0, focus: '' }, '', destination.href);
    if (!push && restoreState && restoreState.app === '032' && destination.href !== window.location.href) history.replaceState({ app: '032', url: destination.href, previousURL: restoreState.previousURL, scrollX: 0, scrollY: 0, focus: '' }, '', destination.href);
    const state = !push && restoreState && restoreState.app === '032' ? restoreState : null;
    if (state && state.url === window.location.href) {
      window.scrollTo(state.scrollX || 0, state.scrollY || 0);
      requestAnimationFrame(() => { const target = state.focus && document.querySelector(state.focus); if (target) target.focus({ preventScroll: true }); });
    } else if (destination.hash) requestAnimationFrame(() => document.getElementById(destination.hash.slice(1))?.scrollIntoView());
    else window.scrollTo(0, 0);
    decorateTrackRows(document);
    updateTrackRowsUI();
    markLoadedImages(adoptedMain);
    restoreSafeDraft();
    document.dispatchEvent(new CustomEvent('032:pjax-applied'));
    return true;
  }

  // ------------------------------------------------------------------ chrome
