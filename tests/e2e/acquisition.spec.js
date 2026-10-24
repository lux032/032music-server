const { test, expect } = require('@playwright/test');
const fs = require('node:fs'), os = require('node:os'), path = require('node:path'), { spawnSync } = require('node:child_process');
test.use({ baseURL: 'http://127.0.0.1:45445' });
const rg = n => '11111111-1111-4111-8111-' + String(n).padStart(12, '0');
const card = (page, n) => page.locator(`[data-rgid="${rg(n)}"]`);
async function control(request, scenario = 'normal', reset = false) {
  return request.post('http://127.0.0.1:45447/control', { data: { scenario, reset } });
}
async function post(page, url, body = {}) {
  return page.evaluate(async ({ url, body }) => {
    const r = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': document.querySelector('meta[name="csrf-token"]').content }, body: JSON.stringify(body) });
    return { status: r.status, body: await r.json() };
  }, { url, body });
}
test.beforeEach(async ({ page, request }) => {
  const state = JSON.parse(fs.readFileSync(path.join(os.tmpdir(), '032music-e2e-discovery-state.json')));
  for (const name of ['reset', 'seed']) {
    const result = spawnSync(path.join(state.tempRoot, `${name}_discovery${process.platform === 'win32' ? '.exe' : ''}`), [path.join(state.tempRoot, 'data/music.db'), 'acquisition'], { encoding: 'utf8' });
    expect(result.status, result.stderr).toBe(0);
  }
  await control(request, 'normal', true);
  await page.goto('/admin/login'); await page.getByLabel('用户名', { exact: true }).fill('admin'); await page.getByLabel('密码', { exact: true }).fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click(); await page.waitForURL(/\/admin$/); await page.goto('/admin/discover');
});
test('搜索、明确选种、确认、hash 绑定和取消', async ({ page }) => {
  await card(page, 1).getByRole('button', { name: '获取', exact: true }).click();
  await expect(page.locator('#discovery-album-query')).toHaveValue('E2E Artist Discovery Missing');
  const confirm = page.getByRole('button', { name: '确认种子并获取' });
  await expect(confirm).toBeDisabled();
  await page.locator('dialog').getByRole('button', { name: '搜索', exact: true }).click();
  await expect(page.getByRole('radio')).toHaveCount(2);
  expect(await page.content()).not.toMatch(/e2e-key|e2e-password|45447\/dl|30128d2e/);
  await expect(page.locator('.acquisition-result').first()).toContainText('20 做种');
  await expect(page.locator('.acquisition-result').first()).toContainText('FREELEECH');
  await expect(confirm).toBeDisabled();
  await page.getByRole('radio').first().focus(); await page.keyboard.press('ArrowDown');
  await expect(confirm).toBeEnabled();
  let confirms = 0; page.on('request', r => { if (r.url().endsWith('/acquire')) confirms++; });
  await confirm.click();
  await expect(page.locator('dialog')).not.toBeVisible();
  await expect(page.locator('[data-discovery-live]')).toContainText('已交给 qBittorrent');
  expect(await page.content()).not.toMatch(/e2e-key|e2e-password|45447\/dl|30128d2e/);
  expect(confirms).toBe(1); await expect(card(page, 1)).toHaveCount(0);
  await card(page, 2).getByRole('button', { name: '获取', exact: true }).click();
  await page.locator('dialog').getByRole('button', { name: '搜索', exact: true }).click();
  await page.getByRole('radio').first().check(); await confirm.click();
  await expect(page.getByRole('link', { name: '查看绑定任务' })).toBeVisible();
  await page.getByRole('link', { name: '查看绑定任务' }).click();
  await expect(card(page, 1)).toContainText('Discovery Missing');
  await card(page, 1).getByRole('button', { name: '取消获取', exact: true }).click();
  await expect(page.locator('dialog')).toContainText('不会删除 qBittorrent 任务');
  await page.getByRole('button', { name: '确认取消获取' }).click();
  await expect(card(page, 1)).toHaveCount(0);
  await page.getByRole('button', { name: /为你发现/ }).click(); await expect(card(page, 1)).toBeVisible();
});
test('错误与空结果分离、magnet 不可选、冷却秒数', async ({ page, request }) => {
  await card(page, 1).getByRole('button', { name: '获取', exact: true }).click();
  await control(request, 'error');
  await page.locator('dialog').getByRole('button', { name: '搜索', exact: true }).click();
  await expect(page.locator('[data-discovery-dialog-message]')).toContainText('Jackett 认证失败');
  await expect(page.locator('dialog')).not.toContainText('secret must not reach UI');
  await control(request, 'empty'); await page.getByRole('button', { name: '强制刷新' }).click();
  await expect(page.locator('[data-discovery-dialog-message]')).toContainText('没有找到种子');
  await control(request, 'magnet-only'); await page.getByRole('button', { name: '强制刷新' }).click();
  await expect(page.getByRole('radio')).toBeDisabled(); await expect(page.locator('.acquisition-result')).toContainText('不支持磁力');
  await expect(page.getByRole('button', { name: '确认种子并获取' })).toBeDisabled();
  await control(request, 'cooldown'); await page.getByRole('button', { name: '强制刷新' }).click();
  await expect(page.locator('[data-discovery-dialog-message]')).toContainText(/请等待 [1-3] 秒/);
  await page.keyboard.press('Escape'); await expect(card(page, 1).getByRole('button', { name: '获取', exact: true })).toBeFocused();
});
test('任务原因、重试、手动关联与替换确认、入库文案、忽略冲突', async ({ page }) => {
  await page.getByRole('button', { name: /获取中/ }).click();
  await expect(page.locator('[data-discovery-grid]')).toContainText('专辑已入库 · 检测到 20 首');
  await expect(page.locator('[data-discovery-grid]')).toContainText('新文件缺少发行组身份，MAT 可能未识别');
  const timeout = page.locator('.acquisition-card').filter({ has: page.getByRole('heading', { name: 'Task library_timeoutATTENTION', exact: true }) });
  await expect(timeout.getByRole('button', { name: '重试', exact: true })).toHaveCount(0);
  const retry = page.locator('.acquisition-card').filter({ has: page.getByRole('heading', { name: 'Task qb_rejectedATTENTION', exact: true }) });
  await retry.getByRole('button', { name: '重试', exact: true }).click(); await expect(page.locator('[data-discovery-live]')).toContainText('已安排重试');
  // Bind the existing album first, so manual linking must explicitly replace its RG.
  const albums = await page.request.get('/admin/options/albums?q=E2E&discovery=1'); const album = (await albums.json())[0];
  expect((await post(page, '/admin/discover/rg/' + rg(2) + '/owned', { albumId: album.id })).status).toBe(200);
  await timeout.getByRole('button', { name: '手动关联专辑' }).click();
  await page.locator('#discovery-album-query').fill('E2E'); await page.locator('dialog').getByRole('button', { name: '搜索', exact: true }).click();
  await page.locator('[data-action="select-album"]').first().click();
  await expect(page.locator('[data-discovery-replace]')).toContainText('将把该专辑的发行组由');
  await page.getByRole('button', { name: '确认替换发行组' }).click(); await expect(page.locator('dialog')).not.toBeVisible();
  await expect(timeout).toContainText('专辑已入库');
  const active = page.locator('.acquisition-card').filter({ has: page.getByRole('heading', { name: 'Task qb_authATTENTION', exact: true }) });
  const activeRG = await active.getAttribute('data-rgid');
  const conflict = await post(page, '/admin/discover/rg/' + activeRG + '/ignore'); expect(conflict.status).toBe(409); expect(conflict.body.error.code).toBe('acquisition_active');
  await page.getByRole('button', { name: /为你发现/ }).click();
  await page.route('**/rg/*/ignore', route => route.fulfill({ status: 409, json: { error: { code: 'acquisition_active', message: 'conflict' } } }));
  await card(page, 1).getByRole('button', { name: '忽略', exact: true }).click(); await expect(page.locator('[data-discovery-live]')).toContainText('先取消获取任务');
  expect(await page.locator('body').evaluate(el => el.scrollWidth <= el.clientWidth)).toBe(true);
});
test('设置只写密钥、保存留空保留、测试连接和 PJAX', async ({ page }) => {
  await page.getByRole('link', { name: '获取设置', exact: true }).click();
  await expect(page.locator('input[name="jackett_api_key"]')).toHaveValue(''); await expect(page.locator('input[name="qb_password"]')).toHaveValue('');
  await expect(page.locator('input[name="jackett_api_key"]')).toHaveAttribute('placeholder', '已设置，留空保留');
  await page.getByRole('button', { name: '保存获取设置' }).click();
  await expect(page.locator('input[name="jackett_api_key"]')).toHaveValue('');
  await expect(page.locator('input[name="qb_password"]')).toHaveValue('');
  // The protocol fake rejects missing/wrong saved keys and passwords.
  await expect(page.locator('input[name="jackett_api_key"]')).toHaveAttribute('placeholder', '已设置，留空保留');
  await expect(page.locator('input[name="qb_password"]')).toHaveAttribute('placeholder', '已设置，留空保留');
  await page.getByRole('button', { name: '测试 Jackett', exact: true }).click(); await expect(page.locator('[data-test-status]')).toHaveText('连接测试成功');
  await page.getByRole('button', { name: '测试 qBittorrent 与分类' }).click(); await expect(page.locator('[data-test-status]')).toHaveText('连接测试成功');
});
test('获取分页、手动扫描提示、禁用引导与对话框焦点', async ({ page }) => {
  await page.route('**/admin/discover/items.json?*', async route => {
    const url = new URL(route.request().url());
    if (url.searchParams.get('tab') !== 'acquiring') return route.continue();
    const offset = Number(url.searchParams.get('offset'));
    const items = Array.from({ length: offset ? 1 : 60 }, (_, i) => ({ publicId: 'fixture-' + (offset + i), rgid: rg(500 + offset + i), title: '分页任务 ' + (offset + i), artist: 'E2E', torrentTitle: 'FLAC', state: 'AWAITING_LIBRARY' }));
    await route.fulfill({ json: { items, counts: { recommended: 2, possible: 2, ignored: 0, acquiring: 61 }, html: '', watchDisabled: true } });
  });
  await page.getByRole('button', { name: /获取中/ }).click();
  await expect(page.locator('.acquisition-card')).toHaveCount(60);
  await expect(page.locator('.acquisition-card').first()).toContainText('整理后需手动扫描');
  await page.getByRole('button', { name: '加载更多' }).click();
  await expect(page.locator('.acquisition-card')).toHaveCount(61);
  await expect(page.getByRole('button', { name: '加载更多' })).toBeHidden();
  await page.getByRole('button', { name: /为你发现/ }).click();
  await page.route('**/rg/*/search', route => route.fulfill({ status: 409, json: { error: { code: 'disabled', message: 'disabled' } } }));
  await card(page, 1).getByRole('button', { name: '获取', exact: true }).click();
  await page.locator('dialog').getByRole('button', { name: '搜索', exact: true }).click();
  await expect(page.locator('[data-discovery-dialog-message]')).toContainText('前往获取设置');
  await page.locator('dialog').getByRole('link', { name: '获取设置' }).focus();
  await page.keyboard.press('Tab'); await expect(page.locator('dialog').getByRole('button', { name: '关闭', exact: true })).toBeFocused();
  await page.keyboard.press('Shift+Tab'); await expect(page.locator('dialog').getByRole('link', { name: '获取设置' })).toBeFocused();
  await page.keyboard.press('Escape');
});

test('轮询保持焦点和滚动、在飞期间不并发、离开后不重挂', async ({ page }) => {
  await page.clock.install();
  await page.goto('/admin/discover');
  await page.getByRole('button', { name: /获取中/ }).click();
  await expect(page.locator('.acquisition-card')).toHaveCount(14);
  await page.evaluate(() => { document.activeElement.blur(); window.scrollTo(0, 300); });
  const before = await page.evaluate(() => ({ y: scrollY, tag: document.activeElement.tagName }));
  let polls = 0, release;
  await page.route('**/admin/discover/items.json?*', async route => {
    if (!route.request().url().includes('tab=acquiring')) return route.continue();
    polls++;
    await new Promise(resolve => { release = resolve; });
    await route.continue();
  });
  await page.clock.fastForward(20000);
  await expect.poll(() => polls).toBe(1);
  await page.evaluate(() => { document.dispatchEvent(new Event('visibilitychange')); document.dispatchEvent(new Event('visibilitychange')); });
  await page.clock.fastForward(20000);
  expect(polls).toBe(1);
  const loaded = page.waitForResponse(r => r.url().includes('items.json?tab=acquiring'));
  release(); await loaded;
  await expect.poll(() => page.evaluate(() => ({ y: scrollY, tag: document.activeElement.tagName }))).toEqual(before);
  // The same rule preserves an explicit focus outside the grid.
  const tab = page.getByRole('button', { name: /获取中/ }); await tab.focus();
  await page.unroute('**/admin/discover/items.json?*');
  const refreshed = page.waitForResponse(r => r.url().includes('items.json?tab=acquiring'));
  const y = await page.evaluate(() => scrollY);
  await page.clock.fastForward(20000); await refreshed;
  await expect(tab).toBeFocused(); expect(await page.evaluate(() => scrollY)).toBe(y);
  // Navigate through PJAX while another poll is awaiting its response.
  await page.route('**/admin/discover/items.json?*', async route => {
    polls++; await new Promise(resolve => { release = resolve; }); await route.continue().catch(() => {});
  });
  await page.clock.fastForward(20000); await expect.poll(() => polls).toBe(2);
  await page.locator('.discovery-main header a').click();
  await expect(page.locator('[data-acquisition-tests]')).toBeVisible();
  release(); await page.clock.fastForward(60000);
  expect(polls).toBe(2);
});
test('确认冷却复用倒计时，归零可以重新搜索', async ({ page }) => {
  await page.clock.install();
  await card(page, 1).getByRole('button', { name: '获取', exact: true }).click();
  await page.locator('dialog').getByRole('button', { name: '搜索', exact: true }).click();
  await page.getByRole('radio').first().check();
  await page.route('**/rg/*/acquire', route => route.fulfill({ status: 429, headers: { 'Retry-After': '3' }, json: { error: { code: 'jackett_cooldown', message: 'cooldown' } } }));
  await page.getByRole('button', { name: '确认种子并获取' }).click();
  await expect(page.locator('[data-discovery-dialog-message]')).toContainText(/请等待 [1-3] 秒/);
  await page.clock.fastForward(3000);
  await expect(page.locator('[data-discovery-dialog-message]')).toContainText('可以重新搜索');
});
