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
  document.addEventListener('032:pjax-applied', () => {
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
