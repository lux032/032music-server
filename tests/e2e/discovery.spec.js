const { test, expect } = require('@playwright/test');
test.use({ baseURL: 'http://127.0.0.1:45445' });
const id = n => '11111111-1111-4111-8111-' + String(n).padStart(12, '0');
async function expectFocus(page) {
  await expect.poll(() => page.evaluate(() => document.activeElement !== document.body)).toBe(true);
}
async function login(page) {
  await page.goto('/admin/login'); await page.getByLabel('用户名').fill('admin'); await page.getByLabel('密码').fill('e2e-admin-password'); await page.getByRole('button', { name: /登录/ }).click(); await page.waitForURL(/\/admin$/);
}
function card(page, n) { return page.locator(`[data-rgid="${id(n)}"]`); }
// Reset feedback with direct fixture seed before each viewport (not the shared instance).
test('发现推荐、反馈、对比、刷新及 PJAX 键盘交互', async ({ page }) => {
  // Re-seed feedback and candidates before each viewport without touching shared fixtures.
  const fs = require('node:fs'), os = require('node:os'), path = require('node:path'), { spawnSync } = require('node:child_process');
  const state = JSON.parse(fs.readFileSync(path.join(os.tmpdir(), '032music-e2e-discovery-state.json')));
  const reset = spawnSync(path.join(state.tempRoot, process.platform === 'win32' ? 'reset_discovery.exe' : 'reset_discovery'), [path.join(state.tempRoot, 'data/music.db')], { encoding: 'utf8' });
  expect(reset.status, reset.stderr).toBe(0);
  const seed = spawnSync(path.join(state.tempRoot, process.platform === 'win32' ? 'seed_discovery.exe' : 'seed_discovery'), [path.join(state.tempRoot, 'data/music.db')], { encoding: 'utf8' });
  expect(seed.status, seed.stderr).toBe(0);
  await login(page);
  await page.goto('/admin/albums');
  if (test.info().project.name !== 'desktop-chromium') await page.getByRole('button', { name: '更多导航' }).click();
  await page.locator('a[data-nav="discover"]:visible').click();
  await expect(page.locator('.discovery-main h1')).toHaveText('发现新音乐');
  await expect(card(page, 1)).toContainText('你收藏了 E2E Artist');
  // Re-enter via PJAX after the external script has already loaded.
  await page.locator('a[data-nav="albums"]:visible').click();
  if (test.info().project.name !== 'desktop-chromium') await page.getByRole('button', { name: '更多导航' }).click();
  await page.locator('a[data-nav="discover"]:visible').click();
  await expect(page.locator('[data-discovery-grid]')).toHaveCSS('display', 'grid');
  let ignoreRequests = 0;
  page.on('request', req => { if (req.method() === 'POST' && req.url().endsWith('/ignore')) ignoreRequests++; });
  if (process.env.E2E_SCREENSHOTS) {
    fs.mkdirSync('.local/mockups/discovery', { recursive: true });
    await page.screenshot({ path: '.local/mockups/discovery/' + test.info().project.name + '.png', fullPage: true });
  }
  await expect(page.locator('[data-count="recommended"]')).toHaveText('2');
  await card(page, 1).getByRole('button', { name: '忽略', exact: true }).focus();
  await page.keyboard.press('Enter');
  await expect(card(page, 1)).toHaveCount(0);
  expect(ignoreRequests).toBe(1);
  await expectFocus(page);
  await expect(card(page, 2).getByRole('button', { name: '我已有' })).toBeFocused();
  await page.getByRole('button', { name: /已忽略/ }).click();
  await expect(page.locator('.discovery-chips')).not.toBeVisible();
  await card(page, 1).getByRole('button', { name: '恢复推荐' }).focus();
  await page.keyboard.press('Enter');
  await expect(card(page, 1)).toHaveCount(0);
  await expectFocus(page);
  await page.getByRole('button', { name: /为你发现/ }).click();
  await card(page, 1).getByRole('button', { name: '我已有' }).focus(); await page.keyboard.press('Enter');
  await expect(page.locator('dialog')).toBeVisible(); await page.keyboard.press('Escape');
  await expect(card(page, 1).getByRole('button', { name: '我已有' })).toBeFocused();
  await page.getByRole('button', { name: /可能已有/ }).click();
  await card(page, 4).getByRole('button', { name: '对比本地专辑' }).click();
  await expect(page.locator('dialog')).toContainText('20 首');
  await page.locator('dialog').getByRole('button', { name: '不是', exact: true }).focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('dialog [data-discovery-search]')).toBeVisible(); await expectFocus(page); await page.keyboard.press('Escape');
  await expectFocus(page);
  await card(page, 3).getByRole('button', { name: '对比本地专辑' }).click();
  await page.locator('dialog').getByRole('button', { name: '是同一发行' }).focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('dialog')).not.toBeVisible();
  await expectFocus(page);
  await page.getByRole('button', { name: '全部', exact: true }).click();
  await card(page, 2).getByRole('button', { name: '我已有' }).click();
  await page.locator('#discovery-album-query').fill('E2E Album');
  await page.locator('dialog').getByRole('button', { name: '搜索', exact: true }).click();
  await expect(page.locator('[data-action="select-album"]').first()).toContainText('20 首');
  await page.locator('[data-action="select-album"]').first().click();
  await expect(page.locator('[data-discovery-replace]')).toContainText('将把该专辑的发行组由 ' + id(3) + ' 改为 ' + id(2));
  await page.getByRole('button', { name: '确认替换发行组' }).click();
  await expect(page.locator('dialog')).not.toBeVisible();
  await expectFocus(page);
  await page.getByRole('button', { name: /获取中/ }).click();
  await expect(page.locator('[data-discovery-grid]')).toHaveText('获取功能将在后续版本提供');
  await page.getByRole('button', { name: /为你发现/ }).click();
  await page.getByRole('button', { name: '刷新目录' }).click();
  await expect(page.locator('[data-discovery-status]')).toContainText('刷新中');
  await expect(page.getByRole('button', { name: '取消刷新' })).toBeVisible();
  await expect(page.locator('[data-discovery-grid]')).toContainText('Refreshed Discovery', { timeout: 15000 });
  await expect(page.locator('[data-discovery-live]')).toContainText('目录刷新已结束');
  await page.getByRole('button', { name: '刷新目录' }).click();
  await page.getByRole('button', { name: '取消刷新' }).click();
  await expect(page.locator('[data-discovery-status]')).toContainText('已取消');
  await expect(page.locator('[data-discovery-status] time')).toHaveAttribute('data-local-time', '');
  // A zero-seed/instant refresh can complete before the first status poll.
  await page.route('**/admin/discover/status.json', route => route.fulfill({ json: { status: 'completed', finishedAt: '2026-01-01T00:00:00Z', processed: 0, total: 0 } }));
  await page.route('**/admin/discover/refresh', route => route.fulfill({ status: 202, json: { generation: 99 } }));
  let reloads = 0;
  page.on('request', req => { if (req.url().includes('/admin/discover/items.json')) reloads++; });
  await page.getByRole('button', { name: '刷新目录' }).click();
  await expect.poll(() => reloads).toBe(1);
  await expect(page.locator('[data-discovery-live]')).toContainText('目录刷新已结束');
  await expect(page.locator('body')).toHaveJSProperty('scrollWidth', await page.locator('body').evaluate(el => el.clientWidth));
});
