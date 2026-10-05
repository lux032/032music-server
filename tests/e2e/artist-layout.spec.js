const { test, expect } = require('@playwright/test');
const os = require('node:os');
const fs = require('node:fs');
const path = require('node:path');

// All desktop shell pages fill available width; artist columns and bio retain
// their independent readability constraints. Uses isolated :45441.
test.use({ baseURL: 'http://127.0.0.1:45441' });
test.beforeEach(async ({ page }) => {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
});

const SHOTS = path.join(os.tmpdir(), 'artist-layout-screenshots');

async function albumArtistID(page) {
  const artists = await (await page.request.get('/admin/options/artists?role=album&q=E2E')).json();
  return artists[0].id;
}

// localStorage is origin-scoped, so set it while on any page of the fixture
// origin (beforeEach lands on /admin), then navigate: applyPanelState reads
// the value during page init. '1' = collapsed, '0' = expanded.
async function setPanelCollapsed(page, collapsed) {
  await page.evaluate(v => localStorage.setItem('032_np_collapsed', v), collapsed ? '1' : '0');
}

function geometry(page) {
  return page.evaluate(() => {
    const main = document.getElementById('app-main');
    const panel = document.getElementById('now-playing');
    const collapsed = document.body.classList.contains('np-collapsed');
    const mainRect = main.getBoundingClientRect();
    const panelLeft = collapsed ? window.innerWidth : panel.getBoundingClientRect().left;
    const aside = document.querySelector('.identity-columns > aside');
    const asideRect = aside ? aside.getBoundingClientRect() : null;
    const bio = document.querySelector('.artist-bio');
    const columns = document.querySelector('.identity-columns');
    const grid = document.querySelector('.detail-album-grid');
    const card = grid ? grid.querySelector('.library-album-card') : null;
    return {
      vw: window.innerWidth,
      maxWidth: getComputedStyle(main).maxWidth,
      mainClass: main.className,
      mainWidth: mainRect.width,
      mainRight: mainRect.right,
      panelLeft,
      asideWidth: asideRect ? asideRect.width : null,
      asideRight: asideRect ? asideRect.right : null,
      bioWidth: bio ? bio.getBoundingClientRect().width : null,
      columnTracks: columns ? getComputedStyle(columns).gridTemplateColumns.split(' ').length : null,
      gridTracks: grid ? getComputedStyle(grid).gridTemplateColumns.split(' ').length : null,
      cardWidth: card ? card.getBoundingClientRect().width : null,
      hOverflow: document.documentElement.scrollWidth - window.innerWidth
    };
  });
}

// Viewport sweep at and above the 1600px cap threshold. Expected widths come
// from vw - sidebar(232) - (expanded ? 340 : 0); the cap only matters above
// 1832 collapsed / 2172 expanded, so 1920-expanded is identical to baseline.
test('wide viewports: cap lifted, main fills up to the player bar', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', 'viewport sweep runs once on desktop-chromium');
  const id = await albumArtistID(page);
  const scenarios = [
    { vw: 3840, collapsed: false, mainWidth: 3268 },
    { vw: 3840, collapsed: true, mainWidth: 3608 },
    { vw: 2560, collapsed: false, mainWidth: 1988 }, // baseline 1600: gap 388 -> 0
    { vw: 2560, collapsed: true, mainWidth: 2328 },  // baseline 1600: gap 728 -> 0
    { vw: 1920, collapsed: false, mainWidth: 1348 }, // cap not reached: unchanged
    { vw: 1920, collapsed: true, mainWidth: 1688 },  // baseline 1600: gap 88 -> 0
    { vw: 1280, collapsed: true, mainWidth: 1048 }   // below the cap: unchanged
  ];
  fs.mkdirSync(SHOTS, { recursive: true });
  for (const s of scenarios) {
    await page.setViewportSize({ width: s.vw, height: 900 });
    await setPanelCollapsed(page, s.collapsed);
    await page.goto(`/admin/artists/${id}`);
    await expect(page.locator('.identity-columns')).toBeVisible();
    // Wait out any first-paint panel transition before measuring.
    await expect.poll(async () => Math.abs((await geometry(page)).mainWidth - s.mainWidth) <= 2).toBe(true);
    const g = await geometry(page);
    expect(g.maxWidth).toBe('none');
    expect(g.mainClass).toContain('artist-profile-main');
    expect(Math.abs(g.mainRight - g.panelLeft)).toBeLessThanOrEqual(1);
    expect(g.asideWidth).toBeGreaterThanOrEqual(339);
    expect(g.asideWidth).toBeLessThanOrEqual(341);
    // --content-gutter: clamp(22px, 3.4vw, 48px); the sidebar column sits one
    // gutter left of the player bar.
    const gutter = Math.min(48, Math.max(22, 0.034 * s.vw));
    expect(Math.abs(g.asideRight - (g.panelLeft - gutter))).toBeLessThanOrEqual(2);
    expect(g.columnTracks).toBe(2); // minmax(0,1fr) 340px unchanged
    expect(g.bioWidth).toBeLessThanOrEqual(850); // bio cap unchanged
    expect(g.hOverflow).toBeLessThanOrEqual(0);
    if (s.vw >= 2560) {
      await page.screenshot({
        path: path.join(SHOTS, `artist-${s.vw}-${s.collapsed ? 'collapsed' : 'expanded'}.png`),
        fullPage: true
      });
    }
  }
});

test('credits and matches share the desktop shell boundary contract', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium');
  await page.setViewportSize({ width: 2560, height: 900 });
  const composers = await (await page.request.get('/admin/options/artists?role=composer&q=E2E')).json();
  for (const collapsed of [false, true]) {
    await setPanelCollapsed(page, collapsed);
    for (const route of [`/admin/credits/${composers[0].id}?role=composer`, '/admin/matches']) {
      await page.goto(route);
      await expect.poll(async () => { const g = await geometry(page); return Math.abs(g.mainRight - g.panelLeft); }).toBeLessThanOrEqual(1);
      expect((await geometry(page)).maxWidth).toBe('none');
    }
  }
});

// The PJAX router swaps main#app-main wholesale, so the profile class must not
// survive navigation away, and must be back after navigating in again.
test('PJAX round trip does not leak or lose artist-profile-main', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', 'PJAX check runs once on desktop-chromium');
  await page.setViewportSize({ width: 2560, height: 900 });
  await setPanelCollapsed(page, true);
  const id = await albumArtistID(page);
  await page.goto(`/admin/artists/${id}`);
  await expect(page.locator('.identity-columns')).toBeVisible();
  expect((await geometry(page)).maxWidth).toBe('none');

  await page.locator('.back-button').click();
  await expect(page).toHaveURL(/\/admin\/artists\/album$/);
  const list = await geometry(page);
  expect(list.mainClass).not.toContain('artist-profile-main');

  await page.locator(`a[href="/admin/artists/${id}"]`).first().click();
  await expect(page).toHaveURL(new RegExp(`/admin/artists/${id}$`));
  await expect(page.locator('.identity-columns')).toBeVisible();
  const back = await geometry(page);
  expect(back.mainClass).toContain('artist-profile-main');
  expect(back.maxWidth).toBe('none');
  expect(Math.abs(back.mainWidth - 2328)).toBeLessThanOrEqual(2);
});

// Toggling the player bar animates #app-main margin-right (0.32s); after the
// transition the main right edge must sit exactly on the panel edge.
test('player bar toggle: main right edge tracks the panel edge', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', 'toggle check runs once on desktop-chromium');
  await page.setViewportSize({ width: 2560, height: 900 });
  await setPanelCollapsed(page, true);
  const id = await albumArtistID(page);
  await page.goto(`/admin/artists/${id}`);
  await expect(page.locator('.identity-columns')).toBeVisible();
  const near = async target => Math.abs((await geometry(page)).mainWidth - target) <= 2;
  await expect.poll(() => near(2328)).toBe(true);

  await page.locator('#np-expand').click();
  await expect.poll(() => near(1988), { timeout: 5000 }).toBe(true);
  let g = await geometry(page);
  expect(Math.abs(g.mainRight - g.panelLeft)).toBeLessThanOrEqual(1);

  await page.locator('#np-collapse').click();
  await expect.poll(() => near(2328), { timeout: 5000 }).toBe(true);
  g = await geometry(page);
  expect(g.panelLeft).toBe(g.vw);
  expect(Math.abs(g.mainRight - g.vw)).toBeLessThanOrEqual(1);
});

// The fixture ships one album and a placeholder bio, so density and long-text
// behavior are simulated client-side only (DOM clones, no DB writes): auto-fill
// must keep ~150px tracks when dense and must not stretch cards when sparse;
// the bio keeps its 850px cap and never runs under the 340px sidebar column.
test('dense/sparse discography and long bio stay inside the layout', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', 'density check runs once on desktop-chromium');
  await page.setViewportSize({ width: 3840, height: 900 });
  await setPanelCollapsed(page, true);
  const id = await albumArtistID(page);
  await page.goto(`/admin/artists/${id}`);
  await expect(page.locator('.detail-album-grid .library-album-card').first()).toBeVisible();

  await page.evaluate(() => {
    const grid = document.querySelector('.detail-album-grid');
    const card = grid.querySelector('.library-album-card');
    for (let i = 0; i < 79; i++) grid.appendChild(card.cloneNode(true));
    document.querySelector('.artist-bio').textContent = '可换行简介内容，'.repeat(400);
  });
  const dense = await geometry(page);
  expect(dense.gridTracks).toBeGreaterThanOrEqual(12);
  expect(dense.cardWidth).toBeGreaterThanOrEqual(140);
  expect(dense.bioWidth).toBeLessThanOrEqual(850);
  expect(dense.hOverflow).toBeLessThanOrEqual(0);
  const bioUnderAside = await page.evaluate(() => {
    const bio = document.querySelector('.artist-bio').getBoundingClientRect();
    const aside = document.querySelector('.identity-columns > aside').getBoundingClientRect();
    return bio.right > aside.left;
  });
  expect(bioUnderAside).toBe(false);

  await page.evaluate(() => {
    document.querySelectorAll('.detail-album-grid .library-album-card').forEach((card, i) => {
      if (i >= 2) card.remove();
    });
  });
  const sparse = await geometry(page);
  expect(sparse.gridTracks).toBeGreaterThanOrEqual(12); // auto-fill keeps empty tracks
  expect(sparse.cardWidth).toBeLessThanOrEqual(180); // cards never stretch
  expect(sparse.hOverflow).toBeLessThanOrEqual(0);
  await page.screenshot({ path: path.join(SHOTS, 'artist-3840-sparse.png'), fullPage: true });
});

// <=900px: single column, aside stacked under the content column. At 375 the
// discography grid is the fixed 2-column mobile rule.
// Known pre-existing exceptions (not caused and not fixed by option A, so not
// asserted as clean): 641-900px detail-toolbar horizontal overflow (~10px at
// 820), and 1280-expanded track-row overflow.
test('single column below 900px; two-column discography at 375', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name === 'desktop-chromium', 'runs on mobile-375 and boundary-820');
  const id = await albumArtistID(page);
  await page.goto(`/admin/artists/${id}`);
  await expect(page.locator('.identity-columns')).toBeVisible();
  const g = await geometry(page);
  expect(g.columnTracks).toBe(1);
  const stacked = await page.evaluate(() => {
    const section = document.querySelector('.identity-columns > section').getBoundingClientRect();
    const aside = document.querySelector('.identity-columns > aside').getBoundingClientRect();
    return aside.top >= section.bottom - 1 && Math.abs(aside.width - section.width) <= 1;
  });
  expect(stacked).toBe(true);
  if (testInfo.project.name === 'mobile-375') {
    expect(g.gridTracks).toBe(2);
    expect(g.hOverflow).toBeLessThanOrEqual(0);
  }
});
