/* 032 Music Server - home.js
   主页（/admin/home）交互：聚焦页签切换、换一张、作品类型筛选、编年点年份、
   货架滚动、/ 聚焦搜索。播放与队列一律走 queue.js 的既有委托：
   播放 = 替换队列；加入队列 = 追加，空队列时立即开始。 */
(() => {
  'use strict';

  const main = () => document.querySelector('.home-main');

  function toast(msg) {
    let el = document.querySelector('.client-toast');
    if (!el) {
      el = document.createElement('div');
      el.className = 'client-toast';
      el.setAttribute('role', 'status');
      document.body.appendChild(el);
    }
    el.textContent = msg;
    el.hidden = false;
    clearTimeout(toast._t);
    toast._t = setTimeout(() => { el.hidden = true; }, 2400);
  }

  // redecorate asks queue.js (a module with player access) to re-scan freshly
  // swapped fragments for track rows and re-apply the now-playing marker.
  function redecorate(root) {
    document.dispatchEvent(new CustomEvent('032:decorate', { detail: { root } }));
  }

  /* ------------------------------------------------------------ hero tabs */
  function switchFocus(key) {
    const stack = document.querySelector('.h-hero-stack');
    if (!stack) return;
    stack.querySelectorAll('.h-hero').forEach((pane) => {
      pane.hidden = pane.dataset.focus !== key;
    });
    stack.querySelectorAll('[data-focus-tab]').forEach((tab) => {
      const on = tab.dataset.focusTab === key;
      tab.classList.toggle('on', on);
      tab.setAttribute('aria-selected', String(on));
    });
    const pane = stack.querySelector(`.h-hero[data-focus="${key}"]`);
    if (pane && !window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      pane.classList.remove('h-hero-swap');
      void pane.offsetWidth;
      pane.classList.add('h-hero-swap');
    }
  }

  /* ------------------------------------------------------------ reroll */
  let rerolling = false;
  async function reroll(button) {
    if (rerolling) return;
    rerolling = true;
    button.disabled = true;
    try {
      const res = await fetch(`/admin/home/spotlight/random?exclude=${encodeURIComponent(button.dataset.reroll || '0')}`, {
        credentials: 'same-origin', headers: { Accept: 'text/html' },
      });
      if (res.status === 401) { location.assign('/admin/login?next=' + encodeURIComponent(location.pathname)); return; }
      if (!res.ok) throw new Error('reroll failed');
      const html = await res.text();
      const pane = document.querySelector('.h-hero[data-focus="random"]');
      if (!pane || !button.isConnected) return;
      const doc = new DOMParser().parseFromString(`<section>${html}</section>`, 'text/html');
      const section = doc.querySelector('section');
      const albumLink = section.querySelector('.h-hero-detail');
      const albumID = albumLink ? albumLink.pathname.split('/').pop() : '';
      pane.innerHTML = section.innerHTML;
      if (albumID) pane.dataset.albumId = albumID;
      if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
        pane.classList.remove('h-hero-swap');
        void pane.offsetWidth;
        pane.classList.add('h-hero-swap');
      }
      redecorate(pane);
    } catch (_) {
      toast('换一张失败，请重试');
    } finally {
      rerolling = false;
      if (button.isConnected) button.disabled = false;
    }
  }

  /* -------------------------------------------------------- chronicle */
  async function selectYear(button) {
    const year = button.dataset.year;
    const chart = document.getElementById('era-chart');
    const result = document.getElementById('era-result');
    if (!chart || !result) return;
    chart.querySelectorAll('[data-year]').forEach((b) => b.setAttribute('aria-pressed', String(b === button)));
    document.querySelectorAll('#era-axis span').forEach((s) => {
      const y = Number(s.dataset.year);
      const on = s.dataset.year === year;
      s.classList.toggle('on', on);
      s.classList.toggle('dim', !on && y % 2 !== 0);
    });
    try {
      const res = await fetch(`/admin/home/chronicle?year=${encodeURIComponent(year)}`, {
        credentials: 'same-origin', headers: { Accept: 'text/html' },
      });
      if (res.status === 401) { location.assign('/admin/login?next=' + encodeURIComponent(location.pathname)); return; }
      if (!res.ok) throw new Error('year failed');
      result.innerHTML = await res.text();
      redecorate(result);
    } catch (_) {
      toast('年份内容加载失败，请重试');
    }
  }

  /* -------------------------------------------------------- works filter */
  function filterWorks(key) {
    const section = document.querySelector('[data-shelf="works"]');
    if (!section) return;
    let visible = 0;
    section.querySelectorAll('.h-work').forEach((card) => {
      const show = key === 'all' || card.dataset.workType === key;
      card.hidden = !show;
      if (show) visible += 1;
    });
    section.scrollTo({ left: 0 });
    const empty = document.querySelector('[data-works-empty]');
    if (empty) empty.hidden = visible > 0;
  }

  /* -------------------------------------------------- play work / series */
  // playEntries builds a transient hidden track list and clicks its first
  // row's play button, so queue replacement (with undo toast) stays entirely
  // inside queue.js semantics.
  function playEntries(tracks) {
    if (!tracks.length) { toast('没有可播放的歌曲'); return; }
    const holder = document.createElement('div');
    holder.hidden = true;
    holder.setAttribute('data-track-list', '');
    for (const t of tracks) {
      const row = document.createElement('article');
      row.setAttribute('data-track-id', String(t.id));
      row.setAttribute('data-track-title', t.title || '');
      row.setAttribute('data-track-artist', t.artist || '');
      row.setAttribute('data-track-album', t.album || '');
      row.setAttribute('data-track-artwork', t.artworkUrl || '');
      row.setAttribute('data-track-container', t.container || '');
      row.setAttribute('data-track-duration-ms', String(t.durationMillis || 0));
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'row-play-btn';
      btn.setAttribute('aria-label', `播放 ${t.title || ''}`);
      row.appendChild(btn);
      holder.appendChild(row);
    }
    document.body.appendChild(holder);
    holder.querySelector('.row-play-btn').click();
    holder.remove();
  }

  async function fetchWorkTracks(workID) {
    const res = await fetch(`/api/v1/works/${encodeURIComponent(workID)}/tracks`, { credentials: 'same-origin' });
    if (res.status === 401) { location.assign('/admin/login?next=' + encodeURIComponent(location.pathname)); return null; }
    if (!res.ok) throw new Error('work tracks failed');
    // The list endpoint wraps rows as { items: [...] } (apiResult).
    const body = await res.json();
    return Array.isArray(body?.items) ? body.items : [];
  }

  let workPlayBusy = false;
  async function playWorks(ids, label) {
    if (workPlayBusy) return;
    workPlayBusy = true;
    try {
      const tracks = [];
      const seen = new Set();
      for (const id of ids) {
        const list = await fetchWorkTracks(id);
        if (!list) return;
        // A song linked to several works of one series is queued once.
        for (const t of list) {
          if (seen.has(t.id)) continue;
          seen.add(t.id);
          tracks.push(t);
        }
      }
      playEntries(tracks);
      if (label && tracks.length) toast(`正在播放${label}（${tracks.length} 首）`);
    } catch (_) {
      toast('加载失败，请检查网络后重试');
    } finally {
      workPlayBusy = false;
    }
  }

  async function playPlaylist(id) {
    try {
      const res = await fetch(`/api/v1/playlists/${encodeURIComponent(id)}`, { credentials: 'same-origin' });
      if (res.status === 401) { location.assign('/admin/login?next=' + encodeURIComponent(location.pathname)); return; }
      if (!res.ok) throw new Error('playlist failed');
      const detail = await res.json();
      playEntries(detail.tracks || []);
    } catch (_) {
      toast('歌单加载失败，请重试');
    }
  }

  /* ------------------------------------------------------------ wiring */
  document.addEventListener('click', (e) => {
    if (!(e.target instanceof Element)) return;
    const tab = e.target.closest('[data-focus-tab]');
    if (tab) { switchFocus(tab.dataset.focusTab); return; }
    const rerollBtn = e.target.closest('[data-reroll]');
    if (rerollBtn) { reroll(rerollBtn); return; }
    const yearBtn = e.target.closest('.h-year[data-year]');
    if (yearBtn) { selectYear(yearBtn); return; }
    const filterBtn = e.target.closest('[data-work-filter]');
    if (filterBtn) {
      filterBtn.parentElement.querySelectorAll('[data-work-filter]').forEach((b) => {
        const on = b === filterBtn;
        b.classList.toggle('on', on);
        b.setAttribute('aria-pressed', String(on));
      });
      filterWorks(filterBtn.dataset.workFilter);
      return;
    }
    const nav = e.target.closest('[data-shelf-nav]');
    if (nav) {
      const shelf = document.querySelector(`[data-shelf="${nav.dataset.shelfNav}"]`);
      if (shelf) shelf.scrollBy({ left: (nav.dataset.dir === 'next' ? 1 : -1) * shelf.clientWidth * 0.8, behavior: 'smooth' });
      return;
    }
    const playWork = e.target.closest('[data-play-work]');
    if (playWork) {
      const card = playWork.closest('.h-work');
      const title = card ? card.querySelector('.h-work-title')?.textContent.trim() : '';
      playWorks([playWork.dataset.playWork], title ? `作品「${title}」` : '');
      return;
    }
    const playSeries = e.target.closest('[data-play-series]');
    if (playSeries) {
      const ids = playSeries.dataset.playSeries.split(',').filter(Boolean);
      playWorks(ids, '全部歌曲');
      return;
    }
    const playPl = e.target.closest('[data-play-playlist]');
    if (playPl) { playPlaylist(playPl.dataset.playPlaylist); return; }
  });

  // "/" focuses the home search box (mirrors the hint kbd).
  document.addEventListener('keydown', (e) => {
    if (e.key !== '/' || e.isComposing) return;
    if (!main()) return;
    const active = document.activeElement;
    if (active && /^(INPUT|TEXTAREA|SELECT)$/.test(active.tagName)) return;
    const input = document.querySelector('.h-search input');
    if (input) { e.preventDefault(); input.focus(); }
  });
})();
