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
    // A debounced search must not fire while this navigation is in flight:
    // it would build its URL from the old location and abort the filter/sort
    // request. The draft is kept, so the search is re-scheduled on top of the
    // new URL once it has been applied.
    clearTimeout(searchTimer);
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
      if (res.status === 401) { location.assign('/admin/login?next=' + encodeURIComponent(location.pathname + location.search)); return; }
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
  // Album grid density: applied live and remembered in a cookie the server
  // reads to render the chosen column count without a layout flash.
  // The chosen count is a preference, not a promise: the effective count is
  // capped by the grid's own width (the now-playing panel and sidebar take
  // their share), so covers never shrink below a recognisable size. Without
  // a saved preference the count follows the available width.
  const MIN_COVER = 150, AUTO_COVER = 190, FAVORITES_MIN_COVER = 88;
  function effectiveCols(grid) {
    const width = grid.clientWidth;
    if (!width) return null;
    const gap = parseFloat(getComputedStyle(grid).columnGap) || 24;
    // The favorites grid defaults to 8 columns, so its covers may shrink
    // further than the library browser's before the width cap applies.
    const minCover = grid.classList.contains('favorite-albums') ? FAVORITES_MIN_COVER : MIN_COVER;
    const fit = (cover) => Math.max(2, Math.floor((width + gap) / (cover + gap)));
    const chosen = parseInt(grid.style.getPropertyValue('--album-cols'), 10);
    if (!chosen) return { chosen: null, cols: Math.min(8, fit(AUTO_COVER)) };
    return { chosen, cols: Math.min(chosen, fit(minCover)) };
  }
  function syncGridOutput(state) {
    const input = document.querySelector('[data-grid-cols]');
    const output = input?.parentElement.querySelector('output');
    if (!input || !output || !state) return;
    // The output mirrors the slider; a separate note explains a width cap.
    let note = output.parentElement.querySelector('.grid-cap-note');
    if (!note) { note = document.createElement('small'); note.className = 'grid-cap-note'; output.after(note); }
    if (!state.chosen) input.value = String(state.cols);
    output.textContent = String(state.chosen || state.cols);
    const capped = !!state.chosen && state.cols < state.chosen;
    note.textContent = capped ? `当前 ${state.cols} 列` : '';
    note.title = capped ? `当前宽度最多容纳 ${state.cols} 列；加宽窗口或收起播放面板后按 ${state.chosen} 列显示` : '';
  }
  function fitGrid(grid) {
    if (grid.classList.contains('detail-album-grid')) return;
    const state = effectiveCols(grid);
    if (!state) return;
    grid.style.setProperty('--album-cols-eff', String(state.cols));
    syncGridOutput(state);
  }
  const gridObserver = typeof ResizeObserver === 'function' ? new ResizeObserver(entries => entries.forEach(entry => fitGrid(entry.target))) : null;
  function observeGrids() {
    document.querySelectorAll('.album-browser:not(.detail-album-grid)').forEach(grid => {
      if (gridObserver) gridObserver.observe(grid); else fitGrid(grid);
    });
  }
  observeGrids();
  document.addEventListener('032:pjax-applied', observeGrids);
  function applyGridCols(input) {
    const cols = Math.min(10, Math.max(2, parseInt(input.value, 10) || 4));
    document.querySelectorAll('.album-browser:not(.detail-album-grid)').forEach(grid => { grid.style.setProperty('--album-cols', String(cols)); fitGrid(grid); });
    return cols;
  }
  document.addEventListener('input', e => {
    if (e.target instanceof HTMLInputElement && e.target.matches('[data-grid-cols]')) applyGridCols(e.target);
  });
  document.addEventListener('change', e => {
    if (!(e.target instanceof HTMLInputElement) || !e.target.matches('[data-grid-cols]')) return;
    const cols = applyGridCols(e.target);
    document.cookie = `032_album_cols=${cols}; path=/admin; max-age=31536000; samesite=lax`;
  });
  // Selects marked data-autosubmit (e.g. favorites sort/page-size) submit
  // their GET form immediately; the router turns that into a PJAX navigation.
  document.addEventListener('change', e => {
    if (!(e.target instanceof HTMLSelectElement) || !e.target.matches('[data-autosubmit]')) return;
    e.target.form?.requestSubmit();
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
    if (e.target.matches?.('.album-card-menu, .queue-menu') && e.target.open) document.querySelectorAll('.album-card-menu[open], .queue-menu[open]').forEach(menu => { if (menu !== e.target) menu.open = false; });
  }, true);
  document.addEventListener('click', e => {
    const inside = e.target.closest?.('.album-card-menu, .queue-menu');
    document.querySelectorAll('.album-card-menu[open], .queue-menu[open]').forEach(menu => { if (menu !== inside) menu.open = false; });
  });
  document.addEventListener('keydown', e => {
    const menu = e.target.closest?.('.album-card-menu[open], .queue-menu[open]');
    if (!menu) return;
    if (e.key === 'Escape') { e.preventDefault(); menu.open = false; menu.querySelector('summary').focus(); }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      const items = [...menu.querySelectorAll('.album-menu-items button, .album-menu-items a')];
      e.preventDefault();
      const index = items.indexOf(document.activeElement);
      items[(index + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length]?.focus();
    }
  });

  // Album multi-select. Order matters: the first ticked album is the main
  // album that every later pick is merged into. State lives only for the
  // current page; any PJAX swap (including the POST result) resets it.
  let albumSelection = [];
  function albumCards() { return [...document.querySelectorAll('.album-browser .library-album-card[data-album-id]')]; }
  function renderAlbumSelection() {
    const bar = document.querySelector('.album-selection-bar');
    const present = new Set(albumCards().map(card => card.dataset.albumId));
    albumSelection = albumSelection.filter(item => present.has(item.id));
    const order = new Map(albumSelection.map((item, index) => [item.id, index]));
    albumCards().forEach(card => {
      const index = order.get(card.dataset.albumId);
      const selected = index !== undefined;
      card.classList.toggle('is-selected', selected);
      const toggle = card.querySelector('.album-select');
      if (toggle) {
        toggle.setAttribute('aria-pressed', String(selected));
        toggle.title = selected ? (index === 0 ? '主专辑（点击取消选择）' : `第 ${index + 1} 张（点击取消选择）`) : '选择';
      }
      const badge = card.querySelector('.album-main-badge');
      if (badge) badge.hidden = !(index === 0 && albumSelection.length > 1);
    });
    // Detail pages have several grids (release groups / collapsed "more");
    // every grid must show the selecting state, not just the first one.
    document.querySelectorAll('.album-browser').forEach(g => g.classList.toggle('is-selecting', albumSelection.length > 0));
    if (!bar) return;
    bar.hidden = albumSelection.length === 0;
    bar.querySelector('[data-selection-count]').textContent = `已选择 ${albumSelection.length} 张专辑`;
    bar.querySelector('[data-selection-main]').textContent = albumSelection.length > 1 ? `合并时并入主专辑《${albumSelection[0].title}》` : '再选择一张即可合并';
    const merge = bar.querySelector('[data-album-bulk="merge"]');
    merge.disabled = albumSelection.length < 2;
  }
  function toggleAlbum(card) {
    const id = card.dataset.albumId;
    const index = albumSelection.findIndex(item => item.id === id);
    if (index >= 0) albumSelection.splice(index, 1);
    else albumSelection.push({ id, title: card.dataset.albumTitle || '' });
    renderAlbumSelection();
  }
  function clearAlbumSelection() {
    albumSelection = [];
    renderAlbumSelection();
  }
  function submitAlbumBulk(action) {
    const form = document.querySelector('[data-album-bulk-form]');
    if (!form || !albumSelection.length || form.classList.contains('is-pending')) return;
    const count = albumSelection.length;
    if (action === 'merge') {
      if (count < 2) return;
      if (!window.confirm(`将其余 ${count - 1} 张专辑的歌曲全部合并到主专辑《${albumSelection[0].title}》？\n合并后其余专辑会消失，重新扫描也会保持合并。`)) return;
    } else if (!window.confirm(`从曲库删除选中的 ${count} 张专辑及其全部歌曲？\n磁盘上的音乐文件不会被删除，但之后重新扫描也不会再导入它们。`)) return;
    form.querySelectorAll('input[name="album"]').forEach(input => input.remove());
    for (const item of albumSelection) {
      const input = document.createElement('input');
      input.type = 'hidden';
      input.name = 'album';
      input.value = item.id;
      form.appendChild(input);
    }
    form.setAttribute('action', action === 'merge' ? '/admin/albums/merge' : '/admin/albums/delete');
    form.requestSubmit();
  }
  // Capture phase: in selection mode a cover click must toggle instead of
  // reaching the PJAX link handler.
  document.addEventListener('click', e => {
    if (!(e.target instanceof Element)) return;
    const toggle = e.target.closest('.album-select');
    const card = e.target.closest('.album-browser .library-album-card[data-album-id]');
    if (toggle && card) {
      e.preventDefault();
      e.stopPropagation();
      toggleAlbum(card);
      return;
    }
    if (card && albumSelection.length && e.target.closest('.album-tile-cover') && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      e.stopPropagation();
      toggleAlbum(card);
      return;
    }
    const bulk = e.target.closest('[data-album-bulk]');
    if (bulk) {
      e.preventDefault();
      if (bulk.dataset.albumBulk === 'cancel') clearAlbumSelection();
      else submitAlbumBulk(bulk.dataset.albumBulk);
    }
  }, true);
  document.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || !albumSelection.length || activeDrawer || e.target.closest?.('.album-card-menu[open], .queue-menu[open], .filter-control')) return;
    e.preventDefault();
    clearAlbumSelection();
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
  // An open drawer owns a same-URL history entry, so the browser's back
  // button (or a phone's back gesture) closes it instead of leaving the
  // page. The entry is keyed by the drawer's id, or by its position for
  // the id-less row drawers, so it can be reopened when the user comes
  // back to it.
  let switchingDrawer = false;
  function drawerKey(drawer) {
    if (drawer.id) return drawer.id;
    const index = [...document.querySelectorAll('main .edit-drawer')].indexOf(drawer);
    return index < 0 ? '' : `row:${index}`;
  }
  function drawerByKey(key) {
    if (!key) return null;
    const drawer = key.startsWith('row:') ? document.querySelectorAll('main .edit-drawer')[Number(key.slice(4))] : document.getElementById(key);
    return drawer?.matches('.edit-drawer') ? drawer : null;
  }
  function recordDrawerEntry(drawer) {
    const key = drawerKey(drawer);
    const state = history.state || {};
    if (!key || state.app !== '032' || state.drawer === key) return;
    if (state.drawer) { history.replaceState({ ...state, drawer: key }, '', location.href); return; }
    const entry = { ...state, drawer: key };
    // The page's own "返回" must skip this entry too.
    if (state.previousURL) entry.backSteps = (state.backSteps || 1) + 1;
    history.pushState(entry, '', location.href);
  }
  function releaseDrawerEntry(drawer) {
    if (switchingDrawer) return;
    const key = drawerKey(drawer);
    if (key && history.state?.drawer === key) history.back();
  }
  function openDrawerFromState() {
    const drawer = drawerByKey(history.state?.drawer);
    if (!drawer || drawerIsOpen(drawer)) return;
    if (drawer instanceof HTMLDetailsElement) { activateDrawer(drawer, drawer.querySelector(':scope > summary'), true); drawer.open = true; }
    else openPageDrawer(drawer, document.querySelector(`[data-drawer-open="${CSS.escape(drawer.id)}"]`), true);
  }
  // popstate also fires for cross-page history steps, before the router has
  // swapped the page; those are handled by 032:pjax-applied on the new page.
  const pageKey = url => { const u = new URL(url, location.href); u.searchParams.delete('notice'); return u.pathname + u.search; };
  let shownPage = pageKey(location.href);
  document.addEventListener('032:pjax-applied', () => { shownPage = pageKey(location.href); });
  window.addEventListener('popstate', () => {
    if (pageKey(location.href) !== shownPage) return;
    const key = history.state?.drawer || '';
    if (activeDrawer && drawerKey(activeDrawer) !== key) closeDrawer(activeDrawer);
    if (key) openDrawerFromState();
  });
  function activateDrawer(drawer, trigger, fromHistory = false) {
    flushDrawerClose();
    if (activeDrawer && activeDrawer !== drawer) {
      switchingDrawer = true;
      try { closeDrawer(activeDrawer, false, true); } finally { switchingDrawer = false; }
    }
    if (!fromHistory) recordDrawerEntry(drawer);
    activeDrawer = drawer;
    drawerTrigger = trigger;
    const panel = drawerPanel(drawer);
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-modal', 'true');
    panel.tabIndex = -1;
    if (trigger && !(trigger instanceof HTMLElement && trigger.matches('summary'))) trigger.setAttribute('aria-expanded', 'true');
    document.documentElement.classList.add('drawer-open');
    (panel.querySelector('[data-drawer-autofocus]') || panel.querySelector(drawerFirstField) || panel).focus({ preventScroll: true });
  }
  function deactivateDrawer(drawer, restoreFocus) {
    const panel = drawerPanel(drawer);
    if (panel) { panel.removeAttribute('role'); panel.removeAttribute('aria-modal'); panel.removeAttribute('tabindex'); }
    drawer.classList.remove('is-closing');
    if (activeDrawer !== drawer) return;
    releaseDrawerEntry(drawer);
    const trigger = drawerTrigger;
    activeDrawer = null;
    drawerTrigger = null;
    document.documentElement.classList.remove('drawer-open');
    if (trigger && trigger.hasAttribute('aria-expanded') && !trigger.matches('summary')) trigger.setAttribute('aria-expanded', 'false');
    if (restoreFocus && trigger?.isConnected) trigger.focus({ preventScroll: true });
  }
  function closeDrawer(drawer, restoreFocus = true, immediate = false) {
    if (!drawerIsOpen(drawer) || drawer.classList.contains('is-closing')) return;
    // Release the history entry now, not after the close animation: a link
    // clicked meanwhile must not have its navigation undone by a late back().
    if (activeDrawer === drawer) releaseDrawerEntry(drawer);
    const finish = () => {
      deactivateDrawer(drawer, restoreFocus);
      if (drawer instanceof HTMLDetailsElement) drawer.open = false;
      else drawer.classList.remove('is-open');
    };
    if (immediate || window.matchMedia('(prefers-reduced-motion: reduce)').matches) { finish(); return; }
    drawer.classList.add('is-closing');
    pendingDrawerClose = { timer: setTimeout(flushDrawerClose, 280), finish };
  }
  function openPageDrawer(drawer, trigger, fromHistory = false) {
    if (drawerIsOpen(drawer)) return;
    flushDrawerClose();
    drawer.classList.add('is-open');
    activateDrawer(drawer, trigger, fromHistory);
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
  openDrawerFromState();
  // Forms the PJAX router leaves to the browser (file uploads, data-no-pjax)
  // would stack their result page on top of the drawer's entry; drop that
  // entry first, then submit for real.
  document.addEventListener('submit', e => {
    const form = e.target;
    if (!(form instanceof HTMLFormElement) || e.defaultPrevented || !history.state?.drawer || !activeDrawer?.contains(form)) return;
    if (!form.hasAttribute('data-no-pjax') && ![...form.elements].some(el => el.type === 'file')) return;
    e.preventDefault();
    const submitter = e.submitter;
    window.addEventListener('popstate', () => form.requestSubmit(submitter && submitter.form === form ? submitter : undefined), { once: true });
    history.back();
  }, true);

  // ------------------------------------------------ Work Hover Popover (4.5.5)
  let hoverOpenTimer = null;
  let hoverCloseTimer = null;
  let activeHoverCell = null;
  let suppressFocusOpen = false;

  function clearHoverTimers() {
    if (hoverOpenTimer) { clearTimeout(hoverOpenTimer); hoverOpenTimer = null; }
    if (hoverCloseTimer) { clearTimeout(hoverCloseTimer); hoverCloseTimer = null; }
  }

  function closeHoverPopover(restoreFocus = false) {
    clearHoverTimers();
    if (!activeHoverCell) return;
    const cell = activeHoverCell;
    activeHoverCell = null;
    const trigger = cell.querySelector('.track-tieup-trigger');
    const popover = cell.querySelector('.work-hover-popover');
    if (trigger) {
      trigger.classList.remove('is-active');
      trigger.setAttribute('aria-expanded', 'false');
    }
    if (popover) {
      popover.hidden = true;
      popover.classList.remove('flip-up');
      popover.style.removeProperty('right');
      popover.style.removeProperty('left');
    }
    if (restoreFocus && trigger?.isConnected) {
      // Esc 关闭后把焦点还给触发器，但不能因此重新打开卡片。
      suppressFocusOpen = true;
      trigger.focus({ preventScroll: true });
      setTimeout(() => { suppressFocusOpen = false; }, 0);
    }
  }

  function positionHoverPopover(cell, popover) {
    popover.classList.remove('flip-up');
    popover.style.removeProperty('right');
    popover.style.removeProperty('left');
    const main = document.getElementById('app-main');
    const player = document.querySelector('.global-player');
    const mainRect = main ? main.getBoundingClientRect() : { left: 0, right: window.innerWidth, top: 0, bottom: window.innerHeight };
    const bottomLimit = player ? player.getBoundingClientRect().top - 10 : window.innerHeight - 16;
    const popRect = popover.getBoundingClientRect();
    if (popRect.bottom > bottomLimit && cell.getBoundingClientRect().top - popRect.height - 12 > mainRect.top) {
      popover.classList.add('flip-up');
    }
    const updatedRect = popover.getBoundingClientRect();
    if (updatedRect.left < mainRect.left + 12) {
      const shift = (mainRect.left + 12) - updatedRect.left;
      popover.style.right = `${-shift}px`;
    } else if (updatedRect.right > mainRect.right - 12) {
      const shift = updatedRect.right - (mainRect.right - 12);
      popover.style.right = `${shift}px`;
    }
  }

  function openHoverPopover(cell) {
    if (!cell || !cell.isConnected) return;
    clearHoverTimers();
    if (activeHoverCell && activeHoverCell !== cell) {
      closeHoverPopover(false);
    }
    const trigger = cell.querySelector('.track-tieup-trigger');
    const popover = cell.querySelector('.work-hover-popover');
    if (!trigger || !popover) return;
    activeHoverCell = cell;
    trigger.classList.add('is-active');
    trigger.setAttribute('aria-expanded', 'true');
    popover.hidden = false;
    positionHoverPopover(cell, popover);
  }

  document.addEventListener('mouseover', e => {
    if (!(e.target instanceof Element)) return;
    const cell = e.target.closest('.track-tieup-cell');
    if (!cell) return;
    if (activeHoverCell === cell) {
      if (hoverCloseTimer) { clearTimeout(hoverCloseTimer); hoverCloseTimer = null; }
      return;
    }
    clearHoverTimers();
    hoverOpenTimer = setTimeout(() => openHoverPopover(cell), 150);
  });

  document.addEventListener('mouseout', e => {
    if (!(e.target instanceof Element)) return;
    const cell = e.target.closest('.track-tieup-cell');
    if (!cell) return;
    if (e.relatedTarget instanceof Node && cell.contains(e.relatedTarget)) return;
    if (hoverOpenTimer) { clearTimeout(hoverOpenTimer); hoverOpenTimer = null; }
    if (activeHoverCell === cell) {
      if (hoverCloseTimer) clearTimeout(hoverCloseTimer);
      hoverCloseTimer = setTimeout(() => closeHoverPopover(false), 300);
    }
  });

  document.addEventListener('focusin', e => {
    if (!(e.target instanceof Element)) return;
    const trigger = e.target.closest('.track-tieup-trigger');
    if (trigger) {
      if (suppressFocusOpen) return;
      const cell = trigger.closest('.track-tieup-cell');
      if (cell) openHoverPopover(cell);
      return;
    }
    if (activeHoverCell && !activeHoverCell.contains(e.target)) {
      closeHoverPopover(false);
    }
  });

  document.addEventListener('focusout', e => {
    if (!activeHoverCell) return;
    if (e.relatedTarget instanceof Node && activeHoverCell.contains(e.relatedTarget)) return;
    closeHoverPopover(false);
  });

  document.addEventListener('keydown', e => {
    if (e.key === 'Escape' && activeHoverCell) {
      e.preventDefault();
      e.stopPropagation();
      closeHoverPopover(true);
    }
  });

  // -------------------------------------- Album Capsule "+N" Expand & Drawer Picker
  document.addEventListener('click', e => {
    if (!(e.target instanceof Element)) return;
    const expandBtn = e.target.closest('[data-capsule-expand]');
    if (expandBtn) {
      const container = expandBtn.closest('.album-work-capsules');
      if (container) {
        const extras = container.querySelectorAll('.work-capsule.is-extra[hidden]');
        if (extras.length > 0) {
          e.preventDefault();
          extras.forEach(el => { el.hidden = false; });
          expandBtn.hidden = true;
          return;
        }
      }
    }
    const peopleBtn = e.target.closest('[data-people-expand]');
    if (peopleBtn) {
      const section = peopleBtn.closest('.album-people');
      if (section) section.querySelectorAll('.is-extra[hidden]').forEach(el => { el.hidden = false; });
      peopleBtn.hidden = true;
      return;
    }
    const workOpt = e.target.closest('[data-work-option-id]');
    if (workOpt) {
      const picker = workOpt.closest('[data-work-picker]');
      if (picker) {
        const idInput = picker.querySelector('[data-work-id-input]');
        const searchInput = picker.querySelector('[data-work-search]');
        const results = picker.querySelector('[data-work-results]');
        if (idInput) idInput.value = workOpt.dataset.workOptionId;
        if (searchInput) searchInput.value = workOpt.textContent || '';
        if (results) results.hidden = true;
      }
    }
  });

  let workPickerTimer = null;
  let workPickerAbort = null;
  // M5：搜索框内按 Enter 不提交外层“保存覆盖信息”表单。
  document.addEventListener('keydown', e => {
    if (e.key === 'Enter' && e.target instanceof HTMLInputElement && e.target.matches('[data-work-search]')) {
      e.preventDefault();
    }
  });
  document.addEventListener('submit', e => {
    const form = e.target;
    if (!(form instanceof HTMLFormElement)) return;
    const picker = form.querySelector('[data-work-picker="/admin/options/albums"], [data-work-picker-required]');
    if (!picker) return;
    const idInput = picker.querySelector('[data-work-id-input]');
    if (idInput?.value) return;
    e.preventDefault();
    const searchInput = picker.querySelector('[data-work-search]');
    if (searchInput) {
      searchInput.setCustomValidity(picker.dataset.workPickerRequired || '请从搜索结果中选择专辑');
      searchInput.reportValidity();
      searchInput.addEventListener('input', () => searchInput.setCustomValidity(''), { once: true });
    }
  });
  document.addEventListener('input', e => {
    if (!(e.target instanceof HTMLInputElement) || !e.target.matches('[data-work-search]')) return;
    const picker = e.target.closest('[data-work-picker]');
    if (!picker) return;
    // M5：搜索内容变化时清空已选作品 ID，避免提交错误的作品。
    const idInput = picker.querySelector('[data-work-id-input]');
    if (idInput) idInput.value = '';
    const results = picker.querySelector('[data-work-results]');
    if (!results) return;
    clearTimeout(workPickerTimer);
    if (workPickerAbort) workPickerAbort.abort();
    const q = e.target.value.trim();
    if (!q) {
      results.replaceChildren();
      results.hidden = true;
      return;
    }
    workPickerTimer = setTimeout(async () => {
      const ctrl = new AbortController();
      workPickerAbort = ctrl;
      try {
        const url = new URL(picker.dataset.workPicker || '/admin/options/works', location.origin);
        url.searchParams.set('q', q);
        url.searchParams.set('limit', picker.dataset.workLimit || '10');
        const res = await fetch(url, { credentials: 'same-origin', signal: ctrl.signal });
        if (!res.ok) return;
        const exclude = picker.dataset.workExclude;
        const items = (await res.json()).filter(item => !exclude || String(item.id) !== exclude);
        if (ctrl.signal.aborted) return;
        results.replaceChildren();
        if (!items.length) {
          if (picker.dataset.workEmpty) {
            const empty = document.createElement('p');
            empty.className = 'work-picker-empty';
            empty.textContent = picker.dataset.workEmpty;
            results.appendChild(empty);
            results.hidden = false;
          } else {
            results.hidden = true;
          }
          return;
        }
        for (const item of items) {
          const btn = document.createElement('button');
          btn.type = 'button';
          btn.dataset.workOptionId = String(item.id);
          btn.textContent = `#${item.id} · ${item.label}`;
          results.appendChild(btn);
        }
        results.hidden = false;
      } catch (_) {}
    }, 200);
  });

  // -------------------------------------- 审核页“只接受勾选作品 (已选/总数)”计数
  function updateSelectedCounts() {
    document.querySelectorAll('[data-selected-count-for]').forEach(btn => {
      const form = document.getElementById(btn.dataset.selectedCountFor);
      const span = btn.querySelector('.selected-count');
      if (!form || !span) return;
      const boxes = form.querySelectorAll('input[name="workSubjectIds"]');
      const checked = form.querySelectorAll('input[name="workSubjectIds"]:checked');
      span.textContent = `${checked.length}/${boxes.length}`;
    });
  }
  document.addEventListener('change', e => {
    if (e.target instanceof HTMLInputElement && e.target.name === 'workSubjectIds') updateSelectedCounts();
  });
  updateSelectedCounts();

  document.addEventListener('032:pjax-applied', () => {
    closeHoverPopover(false);
    updateSelectedCounts();
    clearAlbumSelection();
    if (pendingDrawerClose) { clearTimeout(pendingDrawerClose.timer); pendingDrawerClose = null; }
    activeDrawer = null;
    drawerTrigger = null;
    document.documentElement.classList.remove('drawer-open');
    openDrawerFromHash();
    openDrawerFromState();
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
    // An empty keyword is removed from the URL (get() returns null), so compare
    // against '' or clearing the box would re-submit the same search forever.
    if ((new URL(location.href).searchParams.get('q') ?? '') !== draft.value) scheduleSearch(input);
    else clearSearchDraft();
  });
})();
