const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const SHOTS = path.join(os.tmpdir(), 'wide-layout-screenshots');
test.use({ baseURL: 'http://127.0.0.1:45441' });
test.beforeEach(async ({ page }) => {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
});
async function geometry(page) {
  return page.evaluate(() => {
    const main = document.querySelector('#app-main').getBoundingClientRect();
    const mobile = innerWidth <= 900;
    const edge = mobile || document.body.classList.contains('np-collapsed') ? innerWidth : document.querySelector('#now-playing').getBoundingClientRect().left;
    return { gap: Math.abs(main.right - edge), overflow: document.documentElement.scrollWidth - innerWidth, margin: getComputedStyle(document.querySelector('#app-main')).marginRight };
  });
}
async function boundary(page) {
  await page.locator('#app-main').evaluate(async n => { await Promise.all(n.getAnimations().map(a => a.finished.catch(() => {}))); });
  await expect.poll(async () => (await geometry(page)).gap).toBeLessThanOrEqual(1);
  await expect.poll(async () => (await geometry(page)).overflow, { message: page.url() }).toBeLessThanOrEqual(0);
}
async function routes(page) {
  const artist = (await (await page.request.get('/admin/options/artists?role=album&q=E2E')).json())[0].id;
  const credit = (await (await page.request.get('/admin/options/artists?role=composer&q=E2E')).json())[0].id;
  await page.goto('/admin/series');
  const series = await page.locator('.series-list-row a[href^="/admin/series/"]').first().getAttribute('href');
  await page.goto('/admin/playlists');
  const create = page.locator('form[action="/admin/playlists"]').first();
  await create.getByLabel('名称').fill('Wide layout fixture');
  await create.getByRole('button').click();
  await expect(page).toHaveURL(/\/admin\/playlists\/\d+/);
  const playlist = new URL(page.url()).pathname;
  // Write only to this disposable :45441 fixture through existing endpoints.
  await page.goto('/admin/tracks');
  const ids = await page.locator('[data-track-id]').evaluateAll(nodes => [...new Set(nodes.map(n => Number(n.dataset.trackId)))].slice(0, 3));
  expect(ids.length).toBe(3);
  const csrfToken = await page.locator('input[name="csrfToken"]').first().inputValue();
  for (const [i, trackId] of ids.entries()) {
    const sessionId = `wide-${Date.now()}-${i}`;
    expect((await page.request.post(`${playlist}/tracks`, { form: { csrfToken, trackId: String(trackId) } })).ok()).toBe(true);
    expect((await page.request.post(`/admin/favorites/tracks/${trackId}`, { form: { csrfToken, favorite: '1' } })).ok()).toBe(true);
    expect((await page.request.post('/api/v1/playback/events', { headers: { 'X-CSRF-Token': csrfToken }, data: { clientId: 'wide-layout-fixture', clientKind: 'web', sessionId, seq: 1, type: 'start', trackId, state: 'playing', durationMillis: 30000, positionMillis: 0 } })).ok()).toBe(true);
    expect((await page.request.post('/api/v1/playback/events', { headers: { 'X-CSRF-Token': csrfToken }, data: { clientId: 'wide-layout-fixture', clientKind: 'web', sessionId, seq: 2, type: 'end', trackId, endReason: 'stopped', durationMillis: 30000, positionMillis: 0 } })).ok()).toBe(true);
  }

  await page.goto('/admin/works');
  const work = await page.locator('a[href^="/admin/works/"]').first().getAttribute('href');
  await page.goto('/admin/albums');
  const album = await page.locator('a[href^="/admin/albums/"]').first().getAttribute('href');
  return ['/admin/matches', '/admin/work-review', '/admin/work-review?tab=tracks', '/admin/work-review?tab=works', '/admin/work-review?tab=series', '/admin/series', series, '/admin/enrichment', '/admin/merges', '/admin/settings/metadata', '/admin/settings/security', `/admin/credits/${credit}?role=composer`, '/admin/works', '/admin/favorites?kind=tracks', '/admin/playlists', playlist, '/admin/playback', work, album, `/admin/artists/${artist}`, '/admin/albums', '/admin/tracks', '/admin'];
}
async function density(page) {
  // These selectors are populated by the isolated fixture. Fail rather than
  // silently accepting an empty list for the key seeded categories.
  const route = new URL(page.url()).pathname;
  for (const [pattern, selector] of [[/^\/admin\/favorites$/, '.feature-track-list > article'], [/^\/admin\/playlists\/\d+$/, '.playlist-track-list > article'], [/^\/admin\/playback$/, '.playback-list > article'], [/^\/admin\/credits\/\d+$/, '.artist-track-list article']]) {
    if (pattern.test(route)) expect(await page.locator(selector).count(), selector).toBeGreaterThan(0);
  }
  const actual = page.locator('.series-list-row, .review-group-card');
  if (/\/series$|\/work-review/.test(new URL(page.url()).pathname)) expect(await actual.count()).toBeGreaterThan(0);
  for (const selector of ['.series-list-row', '.review-list section', '.work-align-option', '.review-group-card', '.review-list > article']) {
    const widths = await page.locator(selector).evaluateAll(nodes => nodes.map(n => n.getBoundingClientRect().width));
    for (const width of widths) expect(width).toBeLessThanOrEqual(['.series-list-row', '.review-group-card', '.review-list > article'].includes(selector) ? 720 : 400);
  }
  if (route === '/admin/work-review' && page.viewportSize().width === 3840) {
    expect(await page.locator('.review-group-list').count()).toBeGreaterThan(0);
    const columns = await page.locator('.review-group-list').evaluateAll(nodes => nodes.map(n => getComputedStyle(n).gridTemplateColumns.split(' ').length));
    columns.forEach(n => expect(n).toBeGreaterThan(1));
  }
  const fields = await page.locator('.form-grid input:not([type=checkbox]):not([type=radio]):visible, .form-grid select:visible, .create-work-form input:visible, .create-playlist input:visible').evaluateAll(nodes => nodes.map(n => n.getBoundingClientRect().width));
  for (const width of fields) expect(width).toBeLessThanOrEqual(480);
  const localWidths = await page.locator('.source-card-form, .security-card, .series-admin-main > .feature-card, .create-work-form, .create-playlist form').evaluateAll(nodes => nodes.map(n => n.getBoundingClientRect().width));
  for (const width of localWidths) expect(width).toBeLessThanOrEqual(1100);
  const fieldWidths = await page.locator('.security-inline-form input:visible, .series-create-form input:visible, .inline-rename input:visible, .create-work-form select:visible').evaluateAll(nodes => nodes.map(n => n.getBoundingClientRect().width));
  fieldWidths.forEach(width => expect(width).toBeLessThanOrEqual(480));
  const controls = await page.locator('.review-group-card button:visible, .review-group-card select:visible').evaluateAll(nodes => nodes.map(n => { const r = n.getBoundingClientRect(), group = n.closest('.review-group-card').getBoundingClientRect(); return { width: r.width, height: r.height, inside: r.left >= group.left && r.right <= group.right + 1 }; }));
  if (route === '/admin/work-review') expect(controls.length).toBeGreaterThan(0);
  controls.forEach(c => { expect(c.width).toBeGreaterThanOrEqual(30); expect(c.height).toBeGreaterThanOrEqual(28); expect(c.inside).toBe(true); });
  if (route === '/admin/series') expect(await page.locator('.series-list').evaluate(n => getComputedStyle(n).gridTemplateColumns.split(' ').length)).toBeGreaterThan(1);
  // Text Range endpoint, not the full row's left edge: measures the rendered
  // title's visible end (clamped to an ellipsis box) to the primary action.
  const distances = await page.evaluate(() => {
    const pairs = [['.series-list-row', 'h2', 'a:last-child'], ['.pending-review-banner', 'span', 'a, button'], ['.feature-track-list > article', 'strong', '.row-play-btn'], ['.artist-track-list article', 'strong', '.row-play-btn'], ['.playlist-track-list > article', 'strong', '.track-actions'], ['.playback-list > article', 'strong', '.row-play-btn'], ['.merge-list article', 'strong', 'button']];
    return pairs.flatMap(([row, copy, action]) => [...document.querySelectorAll(row)].flatMap(n => {
      const title = n.querySelector(copy), button = n.querySelector(action);
      if (!title || !button) return [];
      const range = document.createRange(); range.selectNodeContents(title);
      const end = Math.min(range.getBoundingClientRect().right, title.getBoundingClientRect().right);
      return [Math.abs(button.getBoundingClientRect().left - end)];
    }));
  });
  for (const distance of distances) expect(distance).toBeLessThanOrEqual(800);
}
for (const width of [1920, 2560, 3840]) {
  for (const collapsed of [false, true]) {
    test(`desktop pages ${width} ${collapsed ? 'collapsed' : 'expanded'}`, async ({ page }, info) => {
      test.skip(info.project.name !== 'desktop-chromium');
      test.setTimeout(120000);
      await page.setViewportSize({ width, height: 900 });
      const pages = await routes(page);
      await page.evaluate(v => localStorage.setItem('032_np_collapsed', v), collapsed ? '1' : '0');
      for (const route of pages) {
        const response = await page.goto(route); expect(response.status(), route).toBe(200);
        await boundary(page); await density(page);
        if (width >= 2560 && ['/admin/matches', '/admin/series', '/admin/enrichment', '/admin/settings/metadata', '/admin/playback'].includes(route)) {
          fs.mkdirSync(SHOTS, { recursive: true });
          await page.screenshot({ path: path.join(SHOTS, `${route.split('/').pop()}-${width}-${collapsed ? 'collapsed' : 'expanded'}.png`), fullPage: true });
        }
      }
    });
  }
}
// Artist-match candidates and merge history have no seed in this fixture.
// Self-contained DOM probes below use shipped CSS and template-shaped markup;
// they are component evidence, NOT seeded actual-page evidence.
test('nonempty CSS probes: sparse candidates and ordered rows', async ({ page }, info) => {
  test.skip(info.project.name !== 'desktop-chromium');
  for (const width of [1920, 2560, 3840]) for (const collapsed of [false, true]) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate(v => localStorage.setItem('032_np_collapsed', v), collapsed ? '1' : '0');
    await page.goto('/admin/matches');
    await page.evaluate(() => {
      const main = document.querySelector('#app-main');
      main.innerHTML = `<div class="review-list"><article><header>歌手候选</header><div><section><strong>${'長い候補名'.repeat(30)}</strong><div class="candidate-actions"><button>确认</button><button>拒绝</button></div></section></div></article></div>`;
      for (const cls of ['feature-track-list', 'artist-track-list', 'playlist-track-list', 'playback-list']) {
        const list = document.createElement('div'); list.className = cls;
        for (let i = 0; i < 3; i++) list.innerHTML += `<article><span>${i}</span><div class="feature-track-copy artist-track-copy playback-copy"><strong>${i === 1 ? '超长曲目标题'.repeat(40) : '短标题'}</strong><small>歌手 · 专辑</small></div><button class="row-play-btn">播放</button><span>03:40</span><div class="track-actions"><button>移除</button></div></article>`;
        main.append(list);
      }
      main.insertAdjacentHTML('beforeend', '<section class="feature-section run-tasks"><div class="section-title"><h2>头像补全</h2></div><p class="muted">只补全缺失头像，不覆盖自定义图片。</p><form><button type="button">补全缺失头像</button></form><article class="run-card"><header><strong>补全任务</strong><span>暂停</span></header><p>处理 12 / 40</p><div class="run-actions"><button>继续</button><button>停止</button></div></article><details class="run-history"><summary>任务记录</summary><div class="enrichment-runs"><article>已完成任务</article></div></details></section>');
      main.insertAdjacentHTML('beforeend', '<div class="merge-list"><article><div><span>已合并</span><strong>源歌手 → 目标歌手</strong><small>时间</small></div><button>撤销</button></article></div>');
    });
    await boundary(page); await density(page);
    expect(await page.locator('.review-list section').count()).toBe(1);
    await page.locator('.review-list section strong').evaluate(n => n.textContent = '短候选');
    await density(page); // Short and very long text both constrain the candidate.
    await page.locator('.review-list > article > div').evaluate(n => { const candidate = n.querySelector('section'); for (let i = 0; i < 3; i++) n.append(candidate.cloneNode(true)); });
    expect(await page.locator('.review-list section').count()).toBe(4);
    await density(page);
    const taskWidths = await page.locator('.run-card').evaluateAll(nodes => nodes.map(n => n.getBoundingClientRect().width));
    expect(taskWidths.length).toBe(1); taskWidths.forEach(width => expect(width).toBeLessThanOrEqual(640));
    const taskFlow = await page.locator('.run-tasks').evaluate(n => {
      const title = n.querySelector('.section-title').getBoundingClientRect(), section = n.getBoundingClientRect();
      const children = [...n.children].map(c => c.getBoundingClientRect());
      return { fullTitle: Math.abs(title.width - section.width) <= 1, ordered: children.every((r, i) => !i || r.top >= children[i - 1].bottom - 1), display: getComputedStyle(n).display };
    });
    expect(taskFlow.fullTitle).toBe(true); expect(taskFlow.ordered).toBe(true); expect(taskFlow.display).toBe('block');
    expect(await page.locator('.merge-list article').count()).toBe(1);
    const order = await page.locator('.feature-track-list > article > span:first-child').allTextContents(); expect(order).toEqual(['0', '1', '2']);
  }
});
test('breakpoints and mobile pages', async ({ page }, info) => {
  test.setTimeout(120000);
  const pages = await routes(page);
  const widths = info.project.name === 'desktop-chromium' ? [900, 901, 1439, 1440] : [info.project.name === 'mobile-375' ? 375 : 820];
  for (const width of widths) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate(() => localStorage.removeItem('032_np_collapsed'));
    for (const route of pages) {
      await page.goto(route); await boundary(page);
      if (width <= 900) expect((await geometry(page)).margin).toBe('0px');
    }
    if (width <= 900) {
      await page.evaluate(() => document.body.insertAdjacentHTML('beforeend', '<div class="client-toast">提示</div><div class="album-selection-bar">已选</div>'));
      const offsets = await page.locator('.client-toast:visible, .album-selection-bar:visible').evaluateAll(nodes => nodes.map(n => { const r = n.getBoundingClientRect(); return Math.abs((r.left+r.right)/2-innerWidth/2); }));
      expect(offsets.length).toBe(2); offsets.forEach(x => expect(x).toBeLessThanOrEqual(2));
      await page.locator('.client-toast, .album-selection-bar').evaluateAll(nodes => nodes.forEach(n => n.remove()));
    }
    if (width === 1439 || width === 1440) expect(await page.locator('body').evaluate(n => n.classList.contains('np-collapsed'))).toBe(width < 1440);
  }
});
test('toggle animation, floating centers and PJAX round trip', async ({ page }, info) => {
  test.skip(info.project.name !== 'desktop-chromium');
  await page.setViewportSize({ width: 2560, height: 900 });
  const pages = await routes(page); const artist = pages.find(p => p.startsWith('/admin/artists/'));
  await page.evaluate(() => localStorage.setItem('032_np_collapsed', '1'));
  await page.goto(artist);
  await page.evaluate(() => { window.__wideShell = document.querySelector('#now-playing'); window.__wideMargins = []; document.querySelector('#app-main').addEventListener('transitionrun', () => { let i = 0; const sample = () => { window.__wideMargins.push(parseFloat(getComputedStyle(document.querySelector('#app-main')).marginRight)); if (++i < 30) requestAnimationFrame(sample); }; requestAnimationFrame(sample); }); });
  await page.locator('#np-expand').click(); await expect.poll(async () => (await geometry(page)).margin).toBe('340px'); await boundary(page);
  await expect.poll(() => page.evaluate(() => window.__wideMargins.some(x => x > 0 && x < 340))).toBe(true);
  for (const collapsed of [false, true]) {
    if (collapsed) { await page.locator('#np-collapse').click(); await expect.poll(async () => (await geometry(page)).margin).toBe('0px'); await boundary(page); }
    await page.evaluate(() => { document.body.insertAdjacentHTML('beforeend', '<div class="client-toast">提示</div><div class="album-selection-bar">已选专辑</div>'); });
    const centers = await page.locator('.client-toast:visible, .album-selection-bar:visible').evaluateAll(nodes => nodes.map(n => { const r = n.getBoundingClientRect(); const m = document.querySelector('#app-main').getBoundingClientRect(); return Math.abs((r.left+r.right)/2 - (m.left+m.right)/2); }));
    expect(centers.length).toBe(2); centers.forEach(x => expect(x).toBeLessThanOrEqual(2));
    await page.locator('.client-toast, .album-selection-bar').evaluateAll(nodes => nodes.forEach(n => n.remove()));
  }
  for (const route of ['/admin/matches', '/admin/series', artist]) {
    // Real delegated router link; artist isn't always present in the sidebar.
    await page.evaluate(route => { const a = document.createElement('a'); a.href = route; a.id = 'wide-pjax'; a.textContent = 'navigate'; document.querySelector('#app-main').append(a); }, route);
    await page.locator('#wide-pjax').click(); await expect(page).toHaveURL(new RegExp(route + '$')); await boundary(page);
    expect(await page.evaluate(() => window.__wideShell === document.querySelector('#now-playing'))).toBe(true);
  }
  // Actual selection controls, not only a CSS probe: no merge/delete submission.
  for (const collapsed of [false, true]) {
    await page.evaluate(v => localStorage.setItem('032_np_collapsed', v), collapsed ? '1' : '0');
    await page.goto('/admin/albums'); await boundary(page);
    await page.locator('.album-select').first().click({ force: true });
    const bar = page.locator('.album-selection-bar'); await expect(bar).toBeVisible();
    const offset = await bar.evaluate(n => { const r = n.getBoundingClientRect(), m = document.querySelector('#app-main').getBoundingClientRect(); return Math.abs((r.left+r.right-m.left-m.right)/2); });
    expect(offset).toBeLessThanOrEqual(2);
    await page.locator('.album-selection-cancel').click(); await expect(bar).toBeHidden();
  }
});

// Reviewer P1: scrollWidth alone cannot detect content hidden by a fixed panel.
test('narrow desktop saved expanded panel keeps new card grids inside content', async ({ page }, info) => {
  test.skip(info.project.name !== 'desktop-chromium');
  test.setTimeout(120000);
  for (const width of [901, 950, 1000, 1064]) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate(() => localStorage.setItem('032_np_collapsed', '0'));
    for (const route of ['/admin/series', '/admin/work-review', '/admin/work-review?tab=tracks', '/admin/work-review?tab=works', '/admin/work-review?tab=series', '/admin/matches', '/admin/enrichment']) {
      await page.goto(route); await boundary(page);
      expect(await page.locator('body').evaluate(n => n.classList.contains('np-collapsed'))).toBe(false);
      if (route === '/admin/series') expect(await page.locator('.series-list-row').count()).toBeGreaterThan(0);
      const bounds = await page.locator('.series-list-row, .series-list-row > a:last-child, .review-group-card, .work-align-option, .run-card, .enrichment-runs > article').evaluateAll(nodes => {
        const main = document.querySelector('#app-main'), rect = main.getBoundingClientRect(), css = getComputedStyle(main), panel = document.querySelector('#now-playing').getBoundingClientRect();
        const left = rect.left + parseFloat(css.paddingLeft), right = rect.right - parseFloat(css.paddingRight);
        return nodes.filter(n => n.getBoundingClientRect().width > 0).map(n => { const r = n.getBoundingClientRect(); return r.left >= left - 1 && r.right <= right + 1 && r.right <= panel.left + 1; });
      });
      bounds.forEach(inside => expect(inside, `${width} ${route}`).toBe(true));
      if (route === '/admin/series') {
        fs.mkdirSync(SHOTS, { recursive: true });
        await page.screenshot({ path: path.join(SHOTS, `series-${width}-expanded.png`), fullPage: true });
      }
    }
  }
});

// No seeded matches candidates: template-shaped CSS probe, not actual data.
test('matches candidate mobile columns and separators at 560 through 641', async ({ page }, info) => {
  test.skip(info.project.name !== 'desktop-chromium');
  for (const width of [560, 600, 640, 641]) {
    await page.setViewportSize({ width, height: 900 }); await page.goto('/admin/matches');
    await page.evaluate(() => {
      document.querySelector('#app-main').insertAdjacentHTML('beforeend', '<section class="review-list" id="candidate-breakpoint-probe"><article><header><a href="#"><strong>歌手</strong></a><span>1 张专辑</span></header><div>' + Array.from({ length: 3 }, (_, i) => `<section><b>候选 ${i}</b><span class="status-badge warning">待审核</span><span>MusicBrainz · 80</span><small>国家 · 类型</small><div class="candidate-actions"><form><button type="button">确认</button></form><form><button type="button" class="secondary-button">拒绝</button></form></div></section>`).join('') + '</div></article></section>');
    });
    await boundary(page);
    const columns = await page.locator('#candidate-breakpoint-probe > article > div').evaluate(n => getComputedStyle(n).gridTemplateColumns.split(' ').length);
    expect(columns).toBe(1);
    const candidateWidth = await page.locator('#candidate-breakpoint-probe section').first().evaluate(n => n.getBoundingClientRect().width);
    if (width === 641) expect(candidateWidth).toBeLessThanOrEqual(360);
    const borders = await page.locator('#candidate-breakpoint-probe section').evaluateAll(nodes => nodes.map(n => ({ right: getComputedStyle(n).borderRightWidth, bottom: getComputedStyle(n).borderBottomWidth })));
    expect(borders.length).toBe(3);
    borders.forEach(b => { expect(b.right).toBe(width <= 640 ? '0px' : '1px'); expect(b.bottom).toBe(width <= 640 ? '1px' : '0px'); });
  }
});
