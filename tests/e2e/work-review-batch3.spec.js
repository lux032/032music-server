// 批次 3 UI e2e：集中审核页、专辑页胶囊/悬停卡片、作品页、作品列表系列展开。
// 种子数据由 scripts/e2e-global-setup.mjs 写入**独立的 fixture 实例（45441）**，
// 与共享 fixture（45439，20 轨 1 专辑）隔离，避免污染其他 spec。
// 仅桌面项目运行（UI 仅针对 PC）；截图用例只在 E2E_SCREENSHOTS=1 时执行。
const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');

const BATCH3_BASE_URL = 'http://127.0.0.1:45441';

test.use({ baseURL: BATCH3_BASE_URL });

const ROOT = path.resolve(__dirname, '..', '..');
const IMPL_DIR = path.resolve(ROOT, '.local', 'mockups', 'impl');
const SCREENSHOTS = process.env.E2E_SCREENSHOTS === '1';

// 种子数据创建的示例专辑/作品（scripts/seed_e2e.go）
const SINGLE_ALBUM = '紅蓮華'; // 单曲专辑：紅蓮華 OP + c/w 两首 + Instrumental
const COMPILATION_ALBUM = 'E2E Album'; // 精选集：多部作品的曲目级关联
const SERIES_WORK = '鬼滅の刃 竈門炭治郎 立志編';

async function login(page) {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
}

async function gotoAlbum(page, title) {
  await page.goto('/admin/albums?q=' + encodeURIComponent(title));
  await page.locator('.album-card-title a', { hasText: title }).first().click();
  await expect(page).toHaveURL(/\/admin\/albums\/\d+/);
}

async function gotoWork(page, title) {
  await page.goto('/admin/works?q=' + encodeURIComponent(title));
  const card = page.locator('.work-card h2', { hasText: title }).first();
  if (await card.isVisible()) {
    await card.click();
  } else {
    // 系列成员在折叠行内：先展开再点成员卡
    const seriesRow = page.locator('details.works-series-row').first();
    await expect(seriesRow).toBeVisible();
    await seriesRow.locator('summary.works-series-bar').click();
    await page.locator('.works-member-card', { hasText: title }).first().click();
  }
  await expect(page).toHaveURL(/\/admin\/works\/\d+/);
}

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', '批次 3 UI 仅针对 PC 桌面端');
  await login(page);
});

test('work review page tabs switch cleanly via server links', async ({ page }) => {
  await page.goto('/admin/work-review');
  await expect(page.locator('h1')).toContainText('作品关联审核');
  await expect(page.locator('.review-tab.active')).toContainText('专辑候选');
  await expect(page.locator('.review-group-card').first()).toBeVisible();

  // Tab 2: tracks
  await page.getByRole('link', { name: /曲目候选/ }).click();
  await expect(page).toHaveURL(/tab=tracks/);
  await expect(page.locator('.review-tab.active')).toContainText('曲目候选');
  await expect(page.locator('.match-kind-badge').first()).toBeVisible();

  // Tab 3: works
  await page.getByRole('link', { name: /作品对齐候选/ }).click();
  await expect(page).toHaveURL(/tab=works/);
  await expect(page.locator('.review-tab.active')).toContainText('作品对齐候选');
  await expect(page.locator('.work-align-candidates')).toBeVisible();
});

test('work review grouping toggles between album and work groups', async ({ page }) => {
  await page.goto('/admin/work-review?tab=albums&group=album');
  await expect(page.locator('.group-segmented a.active')).toContainText('按专辑分组');
  await expect(page.locator('.review-group-head').first()).toBeVisible();

  await page.locator('.group-segmented a', { hasText: '按作品分组' }).click();
  await expect(page).toHaveURL(/group=work/);
  await expect(page.locator('.group-segmented a.active')).toContainText('按作品分组');
  await expect(page.locator('.review-group-head').first()).toBeVisible();
});

test('album page capsules show roles and +N collapse expands', async ({ page }) => {
  await gotoAlbum(page, COMPILATION_ALBUM);
  const capsules = page.locator('.album-work-capsules');
  await expect(capsules).toBeVisible();
  // 精选集有 6 部作品，默认显示 2 个胶囊 + “+4”
  const more = capsules.locator('.work-capsule-more');
  await expect(more).toHaveText('+4');
  await more.click();
  await expect(capsules.locator('.work-capsule')).toHaveCount(6);
  await expect(more).toBeHidden();
});

test('hover popover opens in ~150ms, stays open when mouse enters, closes in ~300ms', async ({ page }) => {
  await gotoAlbum(page, SINGLE_ALBUM);
  await expect(page.locator('.album-work-capsules')).toBeVisible();

  const trigger = page.locator('.track-tieup-trigger').first();
  await expect(trigger).toBeVisible();
  const popover = page.locator('.work-hover-popover').first();
  await expect(popover).toBeHidden();

  // Hover on trigger -> wait ~200ms -> popover visible
  await trigger.hover();
  await page.waitForTimeout(200);
  await expect(popover).toBeVisible();

  // Move mouse into popover -> stays visible
  await popover.hover();
  await page.waitForTimeout(350);
  await expect(popover).toBeVisible();

  // Move mouse away -> closes in ~300ms
  await page.locator('h1').hover();
  await page.waitForTimeout(400);
  await expect(popover).toBeHidden();
});

test('hover popover opens on Tab focus and closes on Escape', async ({ page }) => {
  await gotoAlbum(page, SINGLE_ALBUM);
  const trigger = page.locator('.track-tieup-trigger').first();
  await expect(trigger).toBeVisible();
  const popover = page.locator('.work-hover-popover').first();
  await expect(popover).toBeHidden();

  // Focus trigger via keyboard
  await trigger.focus();
  await expect(popover).toBeVisible();
  await expect(trigger).toHaveAttribute('aria-expanded', 'true');

  // Press Escape -> popover closes, trigger remains focused
  await page.keyboard.press('Escape');
  await expect(popover).toBeHidden();
  await expect(trigger).toBeFocused();
});

test('hover popover remains functional after PJAX page swap', async ({ page }) => {
  await gotoAlbum(page, SINGLE_ALBUM);
  await expect(page.locator('.album-work-capsules')).toBeVisible();
  const albumUrl = page.url();

  // Navigate away via sidebar PJAX link
  await page.locator('.sidebar a[href="/admin/tracks"]').click();
  await expect(page).toHaveURL(/\/admin\/tracks$/);

  // Navigate back via back button
  await page.goBack();
  await expect(page).toHaveURL(albumUrl);

  const trigger = page.locator('.track-tieup-trigger').first();
  await expect(trigger).toBeVisible();
  const popover = page.locator('.work-hover-popover').first();

  await trigger.hover();
  await page.waitForTimeout(200);
  await expect(popover).toBeVisible();

  await page.keyboard.press('Escape');
  await expect(popover).toBeHidden();
});

test('album drawer work picker: Enter in search does not submit the edit form', async ({ page }) => {
  await gotoAlbum(page, SINGLE_ALBUM);
  await page.locator('a[data-drawer-open="edit"]').first().click();
  const drawer = page.locator('.edit-drawer');
  // 抽屉 section 的子节点全是 position:fixed，本身高度为 0：用 is-open 类断言开态
  await expect(drawer).toHaveClass(/is-open/);

  const search = drawer.locator('[data-work-search]');
  await expect(search).toBeVisible();
  await search.fill('鬼滅');
  await expect(drawer.locator('[data-work-id-input]')).toHaveValue('');
  await search.press('Enter');
  // 没有提交“保存覆盖信息”表单：仍在同一页且抽屉仍开着
  await expect(page).toHaveURL(/\/admin\/albums\/\d+/);
  await expect(drawer).toHaveClass(/is-open/);
});

test('works list expands series drawer in place beneath the row', async ({ page }) => {
  await page.goto('/admin/works');
  const seriesRow = page.locator('details.works-series-row').first();
  await expect(seriesRow).toBeVisible();
  const drawer = seriesRow.locator('.works-series-expanded-drawer');
  await expect(drawer).toBeHidden();

  // Click summary to expand
  await seriesRow.locator('summary.works-series-bar').click();
  await expect(seriesRow).toHaveAttribute('open', '');
  await expect(drawer).toBeVisible();
  await expect(drawer.locator('.works-member-card')).toHaveCount(3);

  // Click summary again to collapse
  await seriesRow.locator('summary.works-series-bar').click();
  await expect(seriesRow).not.toHaveAttribute('open', '');
  await expect(drawer).toBeHidden();
});

test('captures real page screenshots to .local/mockups/impl/', async ({ page }, testInfo) => {
  test.skip(!SCREENSHOTS, '只在 E2E_SCREENSHOTS=1 时生成截图');
  test.skip(testInfo.project.name !== 'desktop-chromium');
  fs.mkdirSync(IMPL_DIR, { recursive: true });
  await page.setViewportSize({ width: 1440, height: 900 });

  // 01: work review albums
  await page.goto('/admin/work-review?tab=albums');
  await page.screenshot({ path: path.join(IMPL_DIR, '01-work-review-albums.png'), fullPage: false });

  // 02: work review tracks
  await page.goto('/admin/work-review?tab=tracks');
  await page.screenshot({ path: path.join(IMPL_DIR, '02-work-review-tracks.png'), fullPage: false });

  // 03: work review works
  await page.goto('/admin/work-review?tab=works');
  await page.screenshot({ path: path.join(IMPL_DIR, '03-work-review-works.png'), fullPage: false });

  // 04: 单曲专辑（紅蓮華）
  await gotoAlbum(page, SINGLE_ALBUM);
  await page.screenshot({ path: path.join(IMPL_DIR, '04-album-single-ost.png'), fullPage: false });

  // 05: 精选集（另一张专辑）
  await gotoAlbum(page, COMPILATION_ALBUM);
  await expect(page.locator('.album-work-capsules')).toBeVisible();
  await page.screenshot({ path: path.join(IMPL_DIR, '05-album-compilation.png'), fullPage: false });

  // 06: album popover open
  const trigger = page.locator('.track-tieup-trigger').first();
  if (await trigger.isVisible()) {
    await trigger.hover();
    await page.waitForTimeout(250);
  }
  await page.screenshot({ path: path.join(IMPL_DIR, '06-album-compilation-popover.png'), fullPage: false });
  await page.keyboard.press('Escape');

  // 07: work detail collapsed
  await gotoWork(page, SERIES_WORK);
  await page.screenshot({ path: path.join(IMPL_DIR, '07-work-detail-collapsed.png'), fullPage: false });

  // 08: work detail expanded
  const seriesFold = page.locator('details.series-fold-card summary');
  if (await seriesFold.isVisible()) {
    await seriesFold.click();
    await page.waitForTimeout(100);
  }
  await page.screenshot({ path: path.join(IMPL_DIR, '08-work-detail-expanded.png'), fullPage: false });

  // 09: works list collapsed
  await page.goto('/admin/works');
  await page.screenshot({ path: path.join(IMPL_DIR, '09-works-list-collapsed.png'), fullPage: false });

  // 10: works list expanded
  const seriesRow = page.locator('details.works-series-row summary').first();
  if (await seriesRow.isVisible()) {
    await seriesRow.click();
    await page.waitForTimeout(100);
  }
  await page.screenshot({ path: path.join(IMPL_DIR, '10-works-list-expanded.png'), fullPage: false });
});

// 放在截图用例之后：会真实解除种子里的曲目关联（写抑制），不影响 07 截图。
test('work page track usages can be removed via the existing remove route', async ({ page }) => {
  await gotoWork(page, SERIES_WORK);
  const usages = page.locator('.work-track-usages').first();
  await expect(usages).toBeVisible();
  const row = usages.locator('.work-track-usage-row', { hasText: '紅蓮華' });
  await expect(row).toContainText('OP');
  await expect(row).toContainText('Bangumi');

  await row.getByRole('button', { name: '解除' }).click();
  // 解除后回到作品页 #albums 锚点，唯一的曲目用途消失，整个曲目用途区不再渲染
  await expect(page).toHaveURL(/\/admin\/works\/\d+/);
  await expect(page.locator('.work-track-usages')).toHaveCount(0);
});
