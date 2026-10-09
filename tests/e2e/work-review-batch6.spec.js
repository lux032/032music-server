// 批次 6 UI e2e：系列建议 Tab（接受新建/加入、拒绝、合并选名）、系列管理页
// 全生命周期、/works 按类型筛选的系列行（D51）。
// 注意：截图、D51 与建议消费用例依赖本文件内的声明顺序（先截图/只读、后消费），
// 不可开启 retries（重试会在种子建议已被消费后空跑并误报失败）。
// 种子数据由 scripts/e2e-global-setup.mjs 写入**独立的 fixture 实例（45441）**，
// 与共享 fixture（45439）隔离；批次 6 的用例会真实接受/拒绝种子建议，必须排在
// work-review-batch3.spec.js 之后运行（文件名顺序保证），且本文件内截图与 D51
// 用例声明在最前（它们依赖尚未被消费的种子状态，与批次 3 的处理方式一致）。
// 仅桌面项目运行（UI 仅针对 PC）；截图用例只在 E2E_SCREENSHOTS=1 时执行。
const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const path = require('node:path');

const BATCH6_BASE_URL = 'http://127.0.0.1:45441';

test.use({ baseURL: BATCH6_BASE_URL });

const ROOT = path.resolve(__dirname, '..', '..');
const IMPL46_DIR = path.resolve(ROOT, '.local', 'mockups', 'impl46');
const SCREENSHOTS = process.env.E2E_SCREENSHOTS === '1';

// PJAX 切换时 #app-main 有 ~0.2s 的进入动画（shell.css .pjax-enter），直接截图
// 会拍到半透明（发灰）的页面。CSP（script-src 'self'）禁止 page-context eval，
// 不能用 waitForFunction 轮询动画，改用覆盖动画时长的固定等待。
async function settleMain(page) {
  await page.waitForTimeout(500);
}

async function login(page) {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
}

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', '批次 6 UI 仅针对 PC 桌面端');
  await login(page);
});

// ---- 截图（声明在最前：依赖尚未被接受的种子建议与未变动的鬼滅系列）----
test('captures batch 6 screenshots to .local/mockups/impl46/', async ({ page }, testInfo) => {
  test.skip(!SCREENSHOTS, '只在 E2E_SCREENSHOTS=1 时生成截图');
  test.skip(testInfo.project.name !== 'desktop-chromium');
  fs.mkdirSync(IMPL46_DIR, { recursive: true });
  await page.setViewportSize({ width: 1440, height: 900 });

  // 11: 审核页系列建议 Tab
  await page.goto('/admin/work-review?tab=series');
  await expect(page.locator('.series-suggestion-card').first()).toBeVisible();
  await settleMain(page);
  await page.screenshot({ path: path.join(IMPL46_DIR, '11-work-review-series.png'), fullPage: false });

  // 12: 需要选名字的合并建议（卡片局部）
  const mergeCard = page.locator('.series-suggestion-card', { hasText: '龍の国シリーズ甲' });
  await expect(mergeCard).toBeVisible();
  await expect(mergeCard).toContainText('两个系列都改过名');
  await mergeCard.scrollIntoViewIfNeeded();
  await settleMain(page);
  await mergeCard.screenshot({ path: path.join(IMPL46_DIR, '12-series-merge-naming.png') });

  // 13: 系列管理列表
  await page.goto('/admin/series');
  await expect(page.locator('.series-list-row').first()).toBeVisible();
  await settleMain(page);
  await page.screenshot({ path: path.join(IMPL46_DIR, '13-series-admin-list.png'), fullPage: false });

  // 14: 系列管理详情（鬼滅：动画×2 + 电影×1，多种类型分组）
  // 直接 goto 打开详情（整页加载没有 PJAX 进入动画，避免截图发灰）。
  await page.goto('/admin/series?q=' + encodeURIComponent('鬼滅'));
  const detailHref = await page.locator('.series-list-row a.secondary-button').first().getAttribute('href');
  await page.goto(detailHref);
  await expect(page).toHaveURL(/\/admin\/series\/\d+/);
  await expect(page.locator('.series-type-group-head').first()).toBeVisible();
  await settleMain(page);
  await page.screenshot({ path: path.join(IMPL46_DIR, '14-series-admin-detail.png'), fullPage: false });

  // 15: /works 按类型筛选后的作品网格
  await page.goto('/admin/works?type=movie');
  await expect(page.locator('.works-grid .work-card').first()).toBeVisible();
  await settleMain(page);
  await page.screenshot({ path: path.join(IMPL46_DIR, '15-works-type-filter.png'), fullPage: false });
});

// ---- 批次 7：新建作品抽屉截图（1280 / 1920）----
test('captures batch 7 works create-card screenshots to .local/mockups/impl47/', async ({ page }, testInfo) => {
  test.skip(!SCREENSHOTS, '只在 E2E_SCREENSHOTS=1 时生成截图');
  test.skip(testInfo.project.name !== 'desktop-chromium');
  const IMPL47_DIR = path.resolve(ROOT, '.local', 'mockups', 'impl47');
  fs.mkdirSync(IMPL47_DIR, { recursive: true });
  for (const width of [1280, 1920]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto('/admin/works?type=movie');
    await page.locator('.browser-header [data-drawer-open="create-work"]').click();
    await expect(page.locator('#create-work')).toHaveClass(/is-open/);
    await expect(page.locator('#create-work input[name=bangumiSubject]')).toBeFocused();
    await settleMain(page);
    await page.screenshot({ path: path.join(IMPL47_DIR, `works-create-${width}.png`), fullPage: false });
  }
});

// ---- /works 只平铺作品，不穿插系列行（声明在接受建议的用例之前）----
test('works list shows plain work cards without series rows', async ({ page }) => {
  await page.goto('/admin/works?type=movie');
  const grid = page.locator('.works-grid');
  await expect(grid.locator('details.works-series-row')).toHaveCount(0);
  await expect(grid.locator('.work-card', { hasText: '無限列車編' })).toBeVisible();
  await expect(grid).not.toContainText('遊郭編');

  await page.goto('/admin/works');
  await expect(page.locator('.works-grid details.works-series-row')).toHaveCount(0);
  await expect(page.locator('.works-grid .work-card', { hasText: '遊郭編' })).toBeVisible();
});

// ---- 系列建议 Tab ----
test('series suggestion tab: accept a create-series suggestion', async ({ page }) => {
  await page.goto('/admin/work-review?tab=series');
  const card = page.locator('.series-suggestion-card', { hasText: 'Fate/stay night [Unlimited Blade Works]' });
  await expect(card).toBeVisible();
  await expect(card).toContainText('不同演绎 ↔ 不同演绎');
  await expect(card).toContainText('互为不同演绎');
  await expect(card).toContainText('接受后：新建系列');
  await card.getByRole('button', { name: '接受建议' }).click();
  await expect(page.locator('.toast')).toContainText('建议已接受');
  await expect(page).toHaveURL(/\/admin\/work-review\?tab=series$/);
  // 系列已创建：系列管理页可搜到，含两名成员。
  await page.goto('/admin/series?q=' + encodeURIComponent('Fate/stay night'));
  await expect(page.locator('.series-list-row')).toHaveCount(1);
  await expect(page.locator('.series-list-row')).toContainText('共 2 部作品');
  // 建议消失。
  await page.goto('/admin/work-review?tab=series');
  await expect(page.locator('.series-suggestion-card', { hasText: 'Unlimited Blade Works' })).toHaveCount(0);
});

test('series suggestion tab: accept an add-to-series suggestion', async ({ page }) => {
  await page.goto('/admin/work-review?tab=series');
  const card = page.locator('.series-suggestion-card', { hasText: 'ヒノカミ血風譚' });
  await expect(card).toBeVisible();
  await expect(card).toContainText('动画 ↔ 游戏');
  await expect(card).toContainText('《鬼滅の刃 ヒノカミ血風譚》是《鬼滅の刃 竈門炭治郎 立志編》的游戏');
  await expect(card).toContainText('加入《鬼滅の刃 竈門炭治郎 立志編》');
  await card.getByRole('button', { name: '接受建议' }).click();
  await expect(page.locator('.toast')).toContainText('建议已接受');
  await expect(page).toHaveURL(/\/admin\/work-review\?tab=series$/);
  // 鬼滅 系列变为 4 名成员（游戏被手动加入）。
  await page.goto('/admin/series?q=' + encodeURIComponent('鬼滅'));
  await expect(page.locator('.series-list-row')).toContainText('共 4 部作品');
  await expect(page.locator('.series-list-row')).toContainText('游戏 1');
});

test('series suggestion tab: reject a suggestion', async ({ page }) => {
  await page.goto('/admin/work-review?tab=series');
  const card = page.locator('.series-suggestion-card', { hasText: 'とある科学の超電磁砲' });
  await expect(card).toBeVisible();
  await expect(card).toContainText('主线故事 ↔ 衍生');
  await expect(card).toContainText('《とある科学の超電磁砲》是《とある魔術の禁書目録》的衍生');
  await card.getByRole('button', { name: '拒绝' }).click();
  await expect(page.locator('.toast')).toContainText('建议已拒绝');
  await expect(page).toHaveURL(/\/admin\/work-review\?tab=series$/);
  await expect(page.locator('.series-suggestion-card', { hasText: '超電磁砲' })).toHaveCount(0);
  // 没有产生任何新系列。
  await page.goto('/admin/series?q=' + encodeURIComponent('超電磁砲'));
  await expect(page.locator('.series-list-row')).toHaveCount(0);
});

test('series suggestion tab: merge suggestion requires and honors a chosen name', async ({ page }) => {
  await page.goto('/admin/work-review?tab=series');
  const card = page.locator('.series-suggestion-card', { hasText: '龍の国 第二部' });
  await expect(card).toBeVisible();
  await expect(card).toContainText('前传 ↔ 续集');
  await expect(card).toContainText('《龍の国 第二部》是《龍の国 第一部》的续集');
  await expect(card).toContainText('合并《龍の国シリーズ甲》与《龍の国シリーズ乙》');
  await expect(card).toContainText('两个系列都改过名，请选择合并后的名字');
  // 选择新名字后接受成功。
  await card.getByRole('radio', { name: /新名字/ }).check();
  await card.locator('input[name=title]').fill('龍の国全集');
  await card.getByRole('button', { name: '接受建议' }).click();
  await expect(page.locator('.toast')).toContainText('建议已接受');
  await expect(page).toHaveURL(/\/admin\/work-review\?tab=series$/);
  // 两个系列合并为一个，名字为所选新名字，共 2 名成员。
  await page.goto('/admin/series?q=' + encodeURIComponent('龍の国'));
  await expect(page.locator('.series-list-row')).toHaveCount(1);
  await expect(page.locator('.series-list-row')).toContainText('龍の国全集');
  await expect(page.locator('.series-list-row')).toContainText('共 2 部作品');
  await expect(page.locator('.series-list-row')).toContainText('已改名');
});

// ---- 系列管理页全生命周期（数据在用例内自建，与种子无关）----
test('series management: create, rename, add, remove, merge, dissolve', async ({ page }) => {
  // 通过作品页新建两部作品。
  for (const title of ['E2E 管理作品甲', 'E2E 管理作品乙']) {
    await page.goto('/admin/works');
    await page.locator('.browser-header [data-drawer-open="create-work"]').click();
    const form = page.locator('#create-work form[action="/admin/works"]');
    await form.locator('input[name=title]').fill(title);
    await form.locator('select[name=type]').selectOption('anime');
    await form.getByRole('button', { name: '创建作品' }).click();
    await expect(page).toHaveURL(/\/admin\/works\/\d+$/);
    await expect(page.locator('.toast')).toContainText('作品已创建');
  }

  // 新建系列（作品甲）。
  await page.goto('/admin/series');
  const createForm = page.locator('.series-create-form');
  await createForm.locator('input[name=title]').fill('E2E 管理系列');
  const createPicker = createForm.locator('.work-picker').first();
  await createPicker.locator('[data-work-search]').fill('E2E 管理作品甲');
  await createPicker.locator('[data-work-results] button', { hasText: 'E2E 管理作品甲' }).first().click();
  await createForm.getByRole('button', { name: '创建系列' }).click();
  await expect(page).toHaveURL(/\/admin\/series\/\d+$/);
  await expect(page.locator('.toast')).toContainText('系列已创建');
  await expect(page.locator('h1')).toContainText('E2E 管理系列');
  await expect(page.locator('.series-member-row')).toHaveCount(1);

  // 重命名。
  const renameForm = page.locator('.inline-rename');
  await renameForm.locator('input[name=title]').fill('E2E 管理系列改');
  await renameForm.getByRole('button', { name: '重命名' }).click();
  await expect(page.locator('h1')).toContainText('E2E 管理系列改');
  await expect(page.locator('.series-detail-header')).toContainText('名字由用户修改过');

  // 加入作品乙。
  const addForm = page.locator('.series-add-form');
  await addForm.locator('[data-work-search]').fill('E2E 管理作品乙');
  await addForm.locator('[data-work-results] button', { hasText: 'E2E 管理作品乙' }).first().click();
  await addForm.getByRole('button', { name: '加入系列' }).click();
  await expect(page.locator('.toast')).toContainText('已加入系列');
  await expect(page).toHaveURL(/\/admin\/series\/\d+$/);
  await expect(page.locator('.series-member-row')).toHaveCount(2);

  // 移出作品乙（提示不再自动归组）。
  await page.locator('.series-member-row', { hasText: 'E2E 管理作品乙' }).getByRole('button', { name: '移出' }).click();
  await expect(page.locator('.toast')).toContainText('已移出本系列');
  await expect(page.locator('.series-member-row')).toHaveCount(1);

  // 用作品乙另建一个系列，随后并入主系列。
  await page.goto('/admin/series');
  const createForm2 = page.locator('.series-create-form');
  await createForm2.locator('input[name=title]').fill('E2E 被合并系列');
  const picker2 = createForm2.locator('.work-picker').first();
  await picker2.locator('[data-work-search]').fill('E2E 管理作品乙');
  await picker2.locator('[data-work-results] button', { hasText: 'E2E 管理作品乙' }).first().click();
  await createForm2.getByRole('button', { name: '创建系列' }).click();
  await expect(page).toHaveURL(/\/admin\/series\/\d+$/);
  await expect(page.locator('.toast')).toContainText('系列已创建');

  await page.goto('/admin/series?q=' + encodeURIComponent('E2E 管理系列改'));
  await page.locator('.series-list-row a.secondary-button').first().click();
  await expect(page).toHaveURL(/\/admin\/series\/\d+/);
  const mergeForm = page.locator('.series-merge-form');
  await mergeForm.locator('[data-work-search]').fill('E2E 被合并');
  await mergeForm.locator('[data-work-results] button', { hasText: 'E2E 被合并系列' }).first().click();
  // 当前系列已改名，默认“保留《E2E 管理系列改》”。
  await expect(mergeForm.getByRole('radio', { name: /保留《E2E 管理系列改》/ })).toBeChecked();
  await mergeForm.getByRole('button', { name: '合并到本系列' }).click();
  await expect(page.locator('.toast')).toContainText('系列已合并');
  await expect(page.locator('.series-member-row')).toHaveCount(2);
  await expect(page.locator('.series-member-row', { hasText: 'E2E 管理作品乙' })).toContainText('手动加入');

  // 解散：回到列表页，系列消失。
  await page.getByRole('button', { name: '解散系列' }).click();
  await expect(page).toHaveURL(/\/admin\/series$/);
  await expect(page.locator('.series-list-row', { hasText: 'E2E 管理系列改' })).toHaveCount(0);
});

// ---- 导航与作品详情页入口 ----
test('series management is reachable from nav and work detail series bar', async ({ page }) => {
  await page.goto('/admin/work-review');
  // 导航“管理”组在组内页面上自动展开；系列管理属于该组（ADMIN_NAV_KEYS）。
  await expect(page.locator('#sidebar-admin-group')).toHaveClass(/open/);
  const navLink = page.locator('.sidebar a[href="/admin/series"]').first();
  await expect(navLink).toBeVisible();
  await navLink.click();
  // PJAX 导航后组保持展开且当前项高亮（router.js 的 ADMIN_NAV_KEYS 同步）。
  await expect(page).toHaveURL(/\/admin\/series$/);
  await expect(page.locator('#sidebar-admin-group')).toHaveClass(/open/);
  await expect(page.locator('.sidebar a[href="/admin/series"]').first()).toHaveClass(/active/);
  await expect(page.locator('h1')).toContainText('系列管理');

  // 作品详情页的系列条带“在系列管理页打开”链接（鬼滅：多类型分组）。
  await page.goto('/admin/works?q=' + encodeURIComponent('遊郭編'));
  await expect(page.locator('.works-grid details.works-series-row')).toHaveCount(0);
  await page.locator('.works-grid .work-card', { hasText: '遊郭編' }).first().click();
  await expect(page).toHaveURL(/\/admin\/works\/\d+/);
  const fold = page.locator('details.series-fold-card');
  await fold.locator('summary').click();
  await expect(fold.locator('.series-type-group-head').first()).toBeVisible();
  await fold.getByRole('link', { name: '在系列管理页打开' }).click();
  await expect(page).toHaveURL(/\/admin\/series\/\d+/);
  await expect(page.locator('h1')).toContainText('鬼滅の刃');
});
