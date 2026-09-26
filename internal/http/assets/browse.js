/* Browser controls use document delegation so they survive main-only PJAX swaps. */
(() => {
  'use strict';
  let searchTimer, optionTimer, request, composing = false, searchDraft = null, pendingSearchURL = null, pendingFilter = null;
  function clearSearchDraft() {
    clearTimeout(searchTimer);
    searchDraft = null;
    pendingSearchURL = null;
  }
  function navigate(url, replace = false) {
    if (window.__032BrowseNavigate) return window.__032BrowseNavigate(url, replace);
    window.location.assign(url);
    return true;
  }
  function filteredURL(name, value) {
    const url = new URL(location.href);
    if (value) url.searchParams.set(name, value);
    else url.searchParams.delete(name);
    url.searchParams.delete('page');
    url.searchParams.delete('offset');
    url.searchParams.delete('notice');
    return url.href;
  }
  function submitSearch(input) {
    searchDraft = { path: location.pathname, value: input.value, start: input.selectionStart, end: input.selectionEnd };
    pendingSearchURL = filteredURL('q', input.value);
    if (navigate(pendingSearchURL, true) === false) clearSearchDraft();
  }
  function scheduleSearch(input) {
    clearTimeout(searchTimer);
    searchDraft = { path: location.pathname, value: input.value, start: input.selectionStart, end: input.selectionEnd };
    searchTimer = setTimeout(() => submitSearch(input), 300);
  }
  function selectOption(control, value) {
    const url = filteredURL(control.dataset.filter, value);
    pendingFilter = { path: new URL(url).pathname, filter: control.dataset.filter };
    if (navigate(url) === false) pendingFilter = null;
    close(control, true);
  }
  function close(control, restore = false) {
    if (!control) return;
    control.querySelector('.filter-popover').hidden = true;
    const trigger = control.querySelector('.filter-trigger');
    trigger.setAttribute('aria-expanded', 'false');
    const input = control.querySelector('[role="combobox"]');
    if (input) { input.setAttribute('aria-expanded', 'false'); input.removeAttribute('aria-activedescendant'); }
    if (restore) trigger.focus();
    if (request && control.dataset.optionsUrl) request.abort();
    clearTimeout(optionTimer);
  }
  function closeAll(except) {
    document.querySelectorAll('.filter-control').forEach(control => { if (control !== except) close(control); });
  }
  function setActive(control, option) {
    control.querySelectorAll('[role="option"]').forEach(item => item.setAttribute('aria-selected', String(item === option)));
    const input = control.querySelector('[role="combobox"]');
    if (input) {
      if (option?.id) input.setAttribute('aria-activedescendant', option.id);
      else input.removeAttribute('aria-activedescendant');
    }
    option?.focus();
  }
  function open(control) {
    closeAll(control);
    control.querySelector('.filter-popover').hidden = false;
    control.querySelector('.filter-trigger').setAttribute('aria-expanded', 'true');
    const input = control.querySelector('[role="combobox"]');
    if (input) { input.setAttribute('aria-expanded', 'true'); input.focus(); options(control); }
    else setActive(control, control.querySelector('[role="option"]'));
  }
  async function options(control) {
    const input = control.querySelector('[role="combobox"]');
    const list = control.querySelector('[role="listbox"]');
    if (request) request.abort();
    const current = new AbortController();
    request = current;
    list.replaceChildren();
    list.dataset.ready = 'false';
    const query = input.value;
    const url = new URL(control.dataset.optionsUrl, location.origin);
    url.searchParams.set('q', query);
    url.searchParams.set('limit', '30');
    try {
      const res = await fetch(url, { credentials: 'same-origin', signal: current.signal });
      if (res.status === 401) { location.assign('/admin/login'); return; }
      if (!res.ok) throw Error('筛选项加载失败');
      const items = await res.json();
      if (current.signal.aborted || !control.isConnected || input.value !== query) return;
      if (!items.length && query) {
        const empty = document.createElement('span');
        empty.textContent = '无匹配';
        list.appendChild(empty);
      }
      for (const item of [{ id: '', label: '全部' }, ...items]) {
        const button = document.createElement('button');
        button.type = 'button';
        button.id = `filter-option-${control.dataset.filter}-${item.id || 'all'}`;
        button.setAttribute('role', 'option');
        button.setAttribute('aria-selected', 'false');
        button.dataset.value = String(item.id);
        button.textContent = item.label;
        list.appendChild(button);
      }
      list.dataset.ready = 'true';
    } catch (err) {
      if (err.name !== 'AbortError' && !current.signal.aborted && control.isConnected) {
        const status = document.createElement('span');
        status.textContent = '筛选项加载失败';
        list.replaceChildren(status);
      }
    }
  }
  document.addEventListener('click', e => {
    if (!(e.target instanceof Element)) return;
    const link = e.target.closest('a[href]');
    if (!link || (link.target && link.target !== '_self') || link.hasAttribute('download') || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const destination = new URL(link.href, location.href);
    if (destination.origin === location.origin && destination.pathname.startsWith('/admin')) clearSearchDraft();
  }, true);
  window.addEventListener('popstate', () => { clearSearchDraft(); pendingFilter = null; });
  document.addEventListener('032:pjax-before-swap', e => {
    if (composing && searchDraft && searchDraft.path === location.pathname && pendingSearchURL === e.detail.url) {
      e.preventDefault();
      pendingSearchURL = null;
    }
  });
  document.addEventListener('click', e => {
    if (!(e.target instanceof Element)) return;
    const kana = e.target.closest('.kana-toggle');
    if (kana) {
      const links = kana.nextElementSibling;
      links.hidden = !links.hidden;
      kana.setAttribute('aria-expanded', String(!links.hidden));
      return;
    }
    const trigger = e.target.closest('.filter-trigger');
    if (trigger) {
      const control = trigger.closest('.filter-control');
      if (trigger.getAttribute('aria-expanded') === 'true') close(control, true);
      else open(control);
      return;
    }
    const option = e.target.closest('.filter-control [role="option"]');
    if (option) {
      const control = option.closest('.filter-control');
      selectOption(control, option.dataset.value);
      return;
    }
    if (!e.target.closest('.filter-control')) closeAll();
  });
  document.addEventListener('keydown', e => {
    if (e.isComposing || e.keyCode === 229 || !(e.target instanceof Element)) return;
    const control = e.target.closest('.filter-control');
    if (!control) return;
    const trigger = control.querySelector('.filter-trigger');
    if (e.target === trigger && e.key === 'ArrowDown' && trigger.getAttribute('aria-expanded') === 'false') {
      e.preventDefault(); open(control); return;
    }
    if (control.querySelector('.filter-popover').hidden) return;
    if (e.key === 'Escape') { e.preventDefault(); close(control, true); return; }
    const list = control.querySelector('[role="listbox"]');
    if (e.key === 'Enter' && e.target.matches('[role="combobox"]')) {
      e.preventDefault();
      if (list.dataset.ready === 'true') {
        const option = e.target.value.trim() ? (list.querySelector('[role="option"][aria-selected="true"]:not([data-value=""])') || list.querySelector('[role="option"]:not([data-value=""])')) : list.querySelector('[role="option"][data-value=""]');
        if (option) {
          selectOption(control, option.dataset.value);
        }
      }
      return;
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      const items = [...list.querySelectorAll('[role="option"]')];
      if (!items.length || (control.dataset.optionsUrl && list.dataset.ready !== 'true')) return;
      e.preventDefault();
      const index = items.indexOf(document.activeElement);
      const next = index < 0 ? (e.key === 'ArrowDown' ? 0 : items.length - 1) : (index + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
      setActive(control, items[next]);
    }
  });
  document.addEventListener('focusout', e => {
    const control = e.target.closest?.('.filter-control');
    if (control && !control.contains(e.relatedTarget)) close(control);
  });
  document.addEventListener('input', e => {
    if (!(e.target instanceof HTMLInputElement)) return;
    if (e.target.matches('.filter-control [role="combobox"]')) {
      clearTimeout(optionTimer);
      if (request) request.abort();
      const control = e.target.closest('.filter-control');
      control.querySelector('[role="listbox"]').dataset.ready = 'false';
      optionTimer = setTimeout(() => options(control), 200);
    } else if (e.target.matches('.instant-search input[name="q"]') && !composing && !e.isComposing) scheduleSearch(e.target);
  });
  document.addEventListener('compositionstart', e => { if (e.target.matches?.('.instant-search input, .filter-control [role="combobox"]')) { composing = true; clearTimeout(searchTimer); clearTimeout(optionTimer); } });
  document.addEventListener('compositionend', e => { if (e.target.matches?.('.instant-search input, .filter-control [role="combobox"]')) { composing = false; e.target.dispatchEvent(new Event('input', { bubbles: true })); } });
  document.addEventListener('submit', e => {
    if (!e.target.matches('.instant-search')) return;
    e.preventDefault();
    clearTimeout(searchTimer);
    submitSearch(e.target.querySelector('input[name="q"]'));
  }, true);
  document.addEventListener('toggle', e => {
    if (e.target.matches?.('.album-card-menu') && e.target.open) document.querySelectorAll('.album-card-menu[open]').forEach(menu => { if (menu !== e.target) menu.open = false; });
  }, true);
  document.addEventListener('click', e => {
    if (!e.target.closest?.('.album-card-menu')) document.querySelectorAll('.album-card-menu[open]').forEach(menu => { menu.open = false; });
  });
  document.addEventListener('keydown', e => {
    const menu = e.target.closest?.('.album-card-menu[open]');
    if (!menu) return;
    if (e.key === 'Escape') { e.preventDefault(); menu.open = false; menu.querySelector('summary').focus(); }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      const items = [...menu.querySelectorAll('.album-menu-items button, .album-menu-items a')];
      e.preventDefault();
      const index = items.indexOf(document.activeElement);
      items[(index + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus();
    }
  });

  // Slide-over edit drawers (F1). Row drawers are <details class="edit-drawer">
  // opened by their <summary>; page drawers are sections opened by a
  // [data-drawer-open] link to their id. Without this class every drawer
  // stays an inline form, so no-JS editing keeps working unchanged.
  document.documentElement.classList.add('drawer-ready');
  const drawerFocusable = 'a[href], button:not([disabled]), input:not([type="hidden"]):not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';
  const drawerFirstField = '.drawer-form input:not([type="hidden"]):not([disabled]), .drawer-form select:not([disabled]), .drawer-form textarea:not([disabled])';
  let activeDrawer = null, drawerTrigger = null, pendingDrawerClose = null;
  function drawerPanel(drawer) { return drawer.querySelector(':scope > .drawer-panel'); }
  function drawerIsOpen(drawer) { return drawer instanceof HTMLDetailsElement ? drawer.open : drawer.classList.contains('is-open'); }
  function drawerItems(panel) { return [...panel.querySelectorAll(drawerFocusable)].filter(el => el.getClientRects().length); }
  function flushDrawerClose() {
    if (!pendingDrawerClose) return;
    const { timer, finish } = pendingDrawerClose;
    pendingDrawerClose = null;
    clearTimeout(timer);
    finish();
  }
  function activateDrawer(drawer, trigger) {
    flushDrawerClose();
    if (activeDrawer && activeDrawer !== drawer) closeDrawer(activeDrawer, false, true);
    activeDrawer = drawer;
    drawerTrigger = trigger;
    const panel = drawerPanel(drawer);
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-modal', 'true');
    panel.tabIndex = -1;
    if (trigger && !(trigger instanceof HTMLElement && trigger.matches('summary'))) trigger.setAttribute('aria-expanded', 'true');
    document.documentElement.classList.add('drawer-open');
    (panel.querySelector(drawerFirstField) || panel).focus({ preventScroll: true });
  }
  function deactivateDrawer(drawer, restoreFocus) {
    const panel = drawerPanel(drawer);
    if (panel) { panel.removeAttribute('role'); panel.removeAttribute('aria-modal'); panel.removeAttribute('tabindex'); }
    drawer.classList.remove('is-closing');
    if (activeDrawer !== drawer) return;
    const trigger = drawerTrigger;
    activeDrawer = null;
    drawerTrigger = null;
    document.documentElement.classList.remove('drawer-open');
    if (trigger && trigger.hasAttribute('aria-expanded') && !trigger.matches('summary')) trigger.setAttribute('aria-expanded', 'false');
    if (restoreFocus && trigger?.isConnected) trigger.focus({ preventScroll: true });
  }
  function closeDrawer(drawer, restoreFocus = true, immediate = false) {
    if (!drawerIsOpen(drawer) || drawer.classList.contains('is-closing')) return;
    const finish = () => {
      deactivateDrawer(drawer, restoreFocus);
      if (drawer instanceof HTMLDetailsElement) drawer.open = false;
      else drawer.classList.remove('is-open');
    };
    if (immediate || window.matchMedia('(prefers-reduced-motion: reduce)').matches) { finish(); return; }
    drawer.classList.add('is-closing');
    pendingDrawerClose = { timer: setTimeout(flushDrawerClose, 280), finish };
  }
  function openPageDrawer(drawer, trigger) {
    if (drawerIsOpen(drawer)) return;
    flushDrawerClose();
    drawer.classList.add('is-open');
    activateDrawer(drawer, trigger);
  }
  function openDrawerFromHash() {
    if (!location.hash) return;
    let id;
    try { id = decodeURIComponent(location.hash.slice(1)); } catch (_) { return; }
    const drawer = document.getElementById(id);
    if (drawer?.matches('.edit-drawer[data-drawer]')) openPageDrawer(drawer, document.querySelector(`[data-drawer-open="${CSS.escape(id)}"]`));
  }
  document.addEventListener('toggle', e => {
    const drawer = e.target;
    if (!(drawer instanceof HTMLDetailsElement) || !drawer.matches('.edit-drawer')) return;
    if (drawer.open) { if (activeDrawer !== drawer) activateDrawer(drawer, drawer.querySelector(':scope > summary')); }
    else if (activeDrawer === drawer) deactivateDrawer(drawer, true);
  }, true);
  // Capture phase so the PJAX link handler sees defaultPrevented on 取消.
  document.addEventListener('click', e => {
    if (!(e.target instanceof Element)) return;
    const opener = e.target.closest('[data-drawer-open]');
    if (opener) {
      const drawer = document.getElementById(opener.dataset.drawerOpen);
      if (drawer?.matches('.edit-drawer')) { e.preventDefault(); openPageDrawer(drawer, opener); }
      return;
    }
    const closer = e.target.closest('[data-drawer-close]');
    if (closer && activeDrawer?.contains(closer)) { e.preventDefault(); closeDrawer(activeDrawer); }
  }, true);
  document.addEventListener('keydown', e => {
    if (!activeDrawer || e.isComposing || e.keyCode === 229) return;
    if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); closeDrawer(activeDrawer); return; }
    if (e.key !== 'Tab') return;
    const panel = drawerPanel(activeDrawer);
    const items = drawerItems(panel);
    if (!items.length) { e.preventDefault(); panel.focus(); return; }
    const first = items[0], last = items[items.length - 1], current = document.activeElement;
    if (!panel.contains(current)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
    else if (e.shiftKey && (current === first || current === panel)) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && current === last) { e.preventDefault(); first.focus(); }
  }, true);
  document.addEventListener('focusin', e => {
    if (!activeDrawer || pendingDrawerClose) return;
    const panel = drawerPanel(activeDrawer);
    if (panel && e.target instanceof Node && !panel.contains(e.target)) (drawerItems(panel)[0] || panel).focus({ preventScroll: true });
  });
  openDrawerFromHash();

  document.addEventListener('032:pjax-applied', () => {
    if (pendingDrawerClose) { clearTimeout(pendingDrawerClose.timer); pendingDrawerClose = null; }
    activeDrawer = null;
    drawerTrigger = null;
    document.documentElement.classList.remove('drawer-open');
    openDrawerFromHash();
    composing = false;
    closeAll();
    if (pendingFilter) {
      const { path, filter } = pendingFilter;
      pendingFilter = null;
      if (path === location.pathname) {
        const trigger = [...document.querySelectorAll('.filter-control')].find(control => control.dataset.filter === filter)?.querySelector('.filter-trigger');
        if (trigger) { trigger.setAttribute('aria-expanded', 'false'); trigger.focus({ preventScroll: true }); }
      }
    }
    if (!searchDraft || searchDraft.path !== location.pathname || !document.querySelector('.instant-search')) { clearSearchDraft(); return; }
    pendingSearchURL = null;
    const input = document.querySelector('.instant-search input[name="q"]');
    const draft = searchDraft;
    if (input.value !== draft.value) input.value = draft.value;
    input.focus({ preventScroll: true });
    input.setSelectionRange(draft.start, draft.end);
    if (new URL(location.href).searchParams.get('q') !== draft.value) scheduleSearch(input);
    else clearSearchDraft();
  });
})();
