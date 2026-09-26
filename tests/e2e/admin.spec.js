const { test, expect } = require('@playwright/test');

async function login(page) {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
  await expect.poll(async () => (await page.locator('#tracks-count').textContent()) || '0', { timeout: 15000 }).not.toBe('0');
}

async function trackPage(page) {
  await page.goto('/admin/tracks');
  await expect(page.locator('[data-track-id]').first()).toBeVisible();
}

test.beforeEach(async ({ page }) => { await login(page); });

test('keeps one audio element across navigation and restores history context', async ({ page }) => {
  await trackPage(page);
  await page.locator('.row-play-btn').nth(12).focus();
  await page.evaluate(() => window.scrollTo(0, 300));
  const audioMarker = await page.evaluate(() => { document.querySelector('#global-audio-element').dataset.marker = 'same'; return true; });
  expect(audioMarker).toBeTruthy();
  await page.getByRole('link', { name: '专辑' }).first().click();
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/tracks/);
  await expect.poll(() => page.evaluate(() => window.scrollY)).toBeGreaterThan(100);
  expect(await page.evaluate(() => document.querySelector('#global-audio-element').dataset.marker)).toBe('same');
});

test('same path/query hash uses native navigation without a fetch', async ({ page }) => {
  await page.goto('/admin#target');
  await page.evaluate(() => { const el = document.createElement('div'); el.id = 'native-anchor'; el.style.marginTop = '1200px'; document.querySelector('main').append(el); });
  const requestedUrls = [];
  const origin = new URL(page.url()).origin;
  page.on('request', req => {
    const u = new URL(req.url());
    // 排除静态资源与仪表盘定时轮询；若 PJAX 错误拦截 hash 链接发出文档 fetch 则此处必定记录
    if (u.origin === origin && u.pathname.startsWith('/admin') && !u.pathname.startsWith('/admin/assets/') && u.pathname !== '/admin/status') requestedUrls.push(`${req.resourceType()} ${req.url()}`);
  });
  await page.evaluate(() => { const a = document.createElement('a'); a.href = '#native-anchor'; a.textContent = 'anchor'; document.querySelector('main').prepend(a); });
  await page.getByText('anchor').click();
  await expect(page).toHaveURL(/#native-anchor$/);
  expect(requestedUrls).toEqual([]);
});

test('POST pending prevents duplicate submit and failures preserve input', async ({ page }) => {
  await page.goto('/admin/playlists');
  const form = page.locator('form[action="/admin/playlists"]').first();
  await form.getByLabel('名称').fill('Keep this draft');
  let posts = 0;
  await page.route('**/admin/playlists', async route => {
    if (route.request().method() !== 'POST') return route.continue();
    posts++;
    await new Promise(r => setTimeout(r, 350));
    await route.fulfill({ status: 500, contentType: 'text/plain', body: 'failed' });
  });
  const save = form.getByRole('button');
  await save.dblclick();
  await expect(form.locator('.form-error')).toContainText('保存失败');
  await expect(form.getByLabel('名称')).toHaveValue('Keep this draft');
  expect(posts).toBe(1);
  await page.unroute('**/admin/playlists');

  await page.route('**/admin/playlists', route => route.abort('failed'));
  await save.click();
  await expect(form.locator('.form-error')).toContainText('无法确认保存结果');
  await expect(form.getByLabel('名称')).toHaveValue('Keep this draft');
  expect(posts).toBe(1);
});

test('expired login stores only non-sensitive draft and never replays POST', async ({ page }) => {
  await page.goto('/admin/settings/metadata');
  const form = page.locator('form.source-card-form').first();
  const ordinary = form.locator('input[name="musicbrainz_application_name"]');
  await ordinary.fill('safe draft');
  let posts = 0;
  const loginHtml = '<!doctype html><html><body class="login-page"><main>login</main></body></html>';
  await page.route('**/admin/settings/metadata', async route => {
    if (route.request().method() === 'POST') { posts++; await route.fulfill({ status: 200, contentType: 'text/html', body: loginHtml }); }
    else await route.continue();
  });
  await form.getByRole('button', { name: /保存/ }).click();
  // Session in this fixture is still valid, so the server bounces /admin/login back to /admin;
  // what matters is leaving the form page without replaying the POST.
  await expect(page).toHaveURL(/\/admin(\/login)?\/?$/);
  expect(posts).toBe(1);
  const draft = await page.evaluate(() => sessionStorage.getItem('032_form_draft'));
  expect(draft).toContain('safe draft');
  expect(draft).not.toContain('SECRET-VALUE');
});

test('danger actions require explicit expansion; cancel sends nothing', async ({ page }) => {
  await page.goto('/admin/playlists');
  const create = page.locator('form[action="/admin/playlists"]').first();
  await create.getByLabel('名称').fill('Disposable');
  await create.getByRole('button').click();
  await expect(page).toHaveURL(/\/admin\/playlists\/\d+/);
  let deletes = 0;
  page.on('request', r => { if (r.method() === 'POST' && /\/delete$/.test(r.url())) deletes++; });
  await page.getByText('删除歌单…').click();
  await page.getByRole('button', { name: '取消' }).click();
  expect(deletes).toBe(0);
  await page.getByText('删除歌单…').click();
  await page.getByRole('button', { name: /永久删除歌单/ }).click();
  await expect(page).toHaveURL(/\/admin\/playlists/);
  expect(deletes).toBe(1);
});

test('keyboard interaction does not steal Space and shortcuts can be disabled', async ({ page }, testInfo) => {
  // 快捷键说明按钮在 ≤820px 视口按设计隐藏，键盘交互语义在桌面项目验证
  test.skip(testInfo.project.name !== 'desktop-chromium');
  await trackPage(page);
  const rowPlay = page.locator('.row-play-btn').first();
  await rowPlay.focus();
  await expect(rowPlay).toBeVisible();
  await page.keyboard.press('Space');
  await expect(page.locator('#player-title')).toContainText('E2E Track');
  await page.locator('#player-btn-shortcuts').click();
  await page.getByRole('button', { name: '关闭快捷键' }).click();
  await expect(page.getByRole('button', { name: '开启快捷键' })).toBeVisible();
  await page.locator('main h1').click();
  await page.keyboard.press('KeyL');
  await expect(page.locator('#lyrics-overlay')).toBeHidden();
  await page.getByRole('button', { name: '开启快捷键' }).click();
  await expect(page.getByRole('button', { name: '关闭快捷键' })).toBeVisible();
  await page.locator('main h1').click();
  await page.keyboard.press('KeyL');
  await expect(page.locator('#lyrics-overlay')).toBeVisible();
});

test('dashboard renders notice, Chinese scan status and offline hint', async ({ page }) => {
  await page.goto('/admin?notice=扫描已启动');
  await expect(page.locator('.toast')).toContainText('扫描已启动');
  await expect(page.locator('#scan-title')).toContainText(/扫描状态：(尚未扫描|正在扫描|扫描完成|扫描失败)/);
  await expect(page.locator('#scan-last-updated')).toContainText('最后更新：');

  // Once the status poll cannot reach the server, the dashboard must say so
  // instead of silently showing stale data.
  await page.route('**/admin/status', route => route.abort('failed'));
  await expect(page.locator('#scan-last-updated')).toContainText('状态更新中断，重试连接中…');
  await page.unroute('**/admin/status');
});

test('metadata source card saves independently', async ({ page }) => {
  await page.goto('/admin/settings/metadata');
  const form = page.locator('form[data-scope-form]', { has: page.locator('input[name="scope"][value="musicbrainz"]') });
  await form.locator('input[name="musicbrainz_application_name"]').fill('E2E App');
  await expect(form.getByRole('button', { name: '保存 MusicBrainz 设置' })).toHaveClass(/is-unsaved/);
  let posts = 0;
  page.on('request', r => { if (r.method() === 'POST' && r.url().includes('/admin/settings/metadata')) posts++; });
  await form.getByRole('button', { name: '保存 MusicBrainz 设置' }).click();
  // Brand-named success notice, not the raw scope key.
  await expect(page.locator('.toast')).toContainText('MusicBrainz 设置已保存');
  await expect(page).toHaveURL(/notice=MusicBrainz/);
  expect(posts).toBe(1);

  // Saving the MusicBrainz card must not touch the other source cards.
  const lastfm = page.locator('form[data-scope-form]', { has: page.locator('input[name="scope"][value="lastfm"]') });
  await expect(lastfm.locator('input[name="lastfm_api_key"]')).toHaveValue('');
  const saved = page.locator('form[data-scope-form]', { has: page.locator('input[name="scope"][value="musicbrainz"]') });
  await expect(saved.locator('input[name="musicbrainz_application_name"]')).toHaveValue('E2E App');
});

test('mobile keeps lyrics entry and progress slider keyboard-accessible', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name === 'desktop-chromium');
  await trackPage(page);
  await expect(page.locator('#player-btn-lyrics')).toBeVisible();
  await expect(page.locator('#player-progress-bar')).toHaveAttribute('role', 'slider');
  await page.locator('#player-progress-bar').focus();
  await page.keyboard.press('Home');
  await page.locator('#player-btn-lyrics').click();
  await expect(page.locator('#lyrics-overlay')).toBeVisible();
  await expect(page.locator('#lyrics-close-btn')).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(page.locator('#player-btn-lyrics')).toBeFocused();
});

test('search, index and focus filters stack and tags remove one condition', async ({ page }) => {
  await page.goto('/admin/albums');
  const search = page.locator('.instant-search input[name="q"]');
  await search.fill('E2E');
  await search.press('Enter');
  await expect(page).toHaveURL(/q=E2E/);

  // Index bar refines the current view instead of discarding the keyword.
  await page.locator('.index-bar a', { hasText: /^E$/ }).click();
  await expect(page).toHaveURL(/index=E/);
  await expect(page).toHaveURL(/q=E2E/);

  // Active conditions are visible as removable chips.
  await expect(page.locator('.filter-tag', { hasText: '关键词' })).toBeVisible();
  await expect(page.locator('.filter-tag', { hasText: '首字母' })).toBeVisible();

  // Selecting a sort order keeps the keyword and the index letter.
  await page.locator('.filter-control[data-filter=sort] .filter-trigger').click();
  await page.locator('.filter-control[data-filter=sort] [role=option][data-value=year]').click();
  await expect(page).toHaveURL(/index=E/);
  await expect(page).toHaveURL(/q=E2E/);
  await expect(page).toHaveURL(/sort=year/);

  // Removing one chip keeps the remaining conditions.
  await page.locator('.filter-tag', { hasText: '首字母' }).click();
  await expect(page).toHaveURL(/q=E2E/);
  await expect(page).not.toHaveURL(/index=/);

  // Searching again preserves nothing stale and resets paging implicitly.
  await page.locator('.instant-search input[name="q"]').fill('不存在的关键词');
  await page.locator('.instant-search input[name="q"]').press('Enter');
  await expect(page).toHaveURL(/q=%E4%B8%8D%E5%AD%98%E5%9C%A8/);
  await expect(page.locator('.zero-state')).toBeVisible();
});

test('queue buttons add next/append without replacing the current queue', async ({ page }) => {
  await trackPage(page);
  await page.locator('.track-table-row').first().locator('.row-play-btn').focus();
  await page.keyboard.press('Enter');
  await expect(page.locator('#player-title')).toContainText('E2E Track');
  const before = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue.length);

  const second = page.locator('.track-table-row').nth(1);
  await second.locator('summary').click();
  await second.locator('.queue-next-btn').click();
  await expect(page.locator('.client-toast')).toContainText('下一首播放');
  let state = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')));
  expect(state.queue.length).toBe(before + 1);

  const third = page.locator('.track-table-row').nth(2);
  await third.locator('summary').click();
  await third.locator('.queue-append-btn').click();
  await expect(page.locator('.client-toast')).toContainText('队列末尾');
  state = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')));
  expect(state.queue.length).toBe(before + 2);

  // Queue buttons must not trigger row playback.
  await expect(page.locator('#player-title')).toContainText('E2E Track');
});

test('track quick edit keeps the filtered list context after save', async ({ page }) => {
  await page.goto('/admin/tracks?q=E2E&sort=year');
  const row = page.locator('.track-table-row').first();
  await row.locator('summary').click();
  const editForm = row.locator('form[action^="/admin/tracks/"]');
  await editForm.locator('input[name="composer"]').fill('E2E Composer');
  await editForm.getByRole('button', { name: '保存' }).click();
  await expect(page).toHaveURL(/q=E2E/);
  await expect(page).toHaveURL(/sort=year/);
  await expect(page.locator('.toast')).toContainText('已保存');
  await expect(editForm.locator('input[name="composer"]')).toHaveValue('E2E Composer');
});

test('instant search preserves the focused complete input across PJAX', async ({ page }) => {
  await page.goto('/admin/albums');
  const search = page.locator('.instant-search input[name="q"]');
  await search.focus();
  await search.pressSequentially('E2E', { delay: 180 });
  await expect(page).toHaveURL(/q=E2E/);
  await expect(search).toBeFocused();
  await expect(search).toHaveValue('E2E');
});

test('unfinished album search does not follow sidebar navigation', async ({ page }) => {
  await page.goto('/admin/albums');
  await page.locator('.instant-search input[name="q"]').fill('E2E');
  await page.locator('.sidebar a[href="/admin/tracks"]').click();
  await expect(page).toHaveURL(/\/admin\/tracks$/);
  await page.waitForTimeout(400);
  await expect(page).toHaveURL(/\/admin\/tracks$/);
});

test('in-flight search does not interrupt IME composition', async ({ page }) => {
  await page.goto('/admin/albums');
  let releaseResponse;
  const heldResponse = new Promise(resolve => { releaseResponse = resolve; });
  let requestStarted;
  const started = new Promise(resolve => { requestStarted = resolve; });
  await page.route('**/admin/albums?q=E2E', async route => {
    requestStarted();
    await heldResponse;
    await route.continue();
  });
  const search = page.locator('.instant-search input[name="q"]');
  await search.fill('E2E');
  await started;
  await search.evaluate(input => { input.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })); input.value = '新标题'; });
  releaseResponse();
  // The stale response must be discarded while composition owns the input.
  await page.waitForTimeout(350);
  await expect(search).toHaveValue('新标题');
  await expect(page).toHaveURL(/\/admin\/albums$/);
  await search.evaluate(input => input.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true })));
  await expect(page).toHaveURL(/q=%E6%96%B0%E6%A0%87%E9%A2%98/);
  await expect(search).toHaveValue('新标题');
});

test('works filters remain visible after library focus fallback is hidden', async ({ page }) => {
  await page.goto('/admin/works');
  await expect(page.locator('form.focus-bar select[name="type"]')).toBeVisible();
  await expect(page.locator('form.focus-bar button', { hasText: '应用' })).toBeVisible();
});

test('artist typeahead Enter selects a matching artist rather than All', async ({ page }) => {
  await page.goto('/admin/albums');
  const trigger = page.locator('.filter-control[data-filter="artist"] .filter-trigger');
  await trigger.click();
  const input = page.getByRole('combobox', { name: '搜索歌手' });
  await input.fill('E2E');
  await expect(page.locator('#filter-list-artist [role="option"]:not([data-value=""])')).toBeVisible();
  await expect(page.locator('#filter-list-artist')).toHaveAttribute('data-ready', 'true');
  await input.press('Enter');
  await expect(page).toHaveURL(/artist=\d+/);
  await expect(trigger).toContainText('E2E Artist');
});
