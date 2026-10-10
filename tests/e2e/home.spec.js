// 主页（/admin/home）端到端测试。
// 使用 45441 独立实例（seed 含鬼滅系列、多部作品、角色关联与 4 张专辑，
// 其中仅 E2E Album 的 20 首是真实音频；播放断言只落在它上面）。
// 文件内顺序有意义：第一个测试清空播放记录，之后的“无播放记录”断言才成立。
const { test, expect } = require('@playwright/test');

test.use({ baseURL: 'http://127.0.0.1:45441' });

async function login(page, base) {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await page.waitForURL(/\/admin$/);
}

async function csrf(page) {
  return page.locator('meta[name="csrf-token"]').getAttribute('content');
}

async function home(page) {
  await page.goto('/admin/home');
  await expect(page.locator('.home-main h1')).toHaveText('主页');
}

// E2E Album（唯一有真实音频的专辑）在主页“最近添加”里的卡片。
function fixtureAlbumCard(page) {
  return page.locator('.h-card', { has: page.locator('a.h-card-title', { hasText: 'E2E Album' }) }).first();
}

async function queueState(page) {
  return page.evaluate(() => {
    const raw = sessionStorage.getItem('032_player_state');
    return raw ? JSON.parse(raw) : { queue: [], currentIndex: -1 };
  });
}

test('无播放记录：隐藏“最近在听”页签，接着听显示空态', async ({ page }) => {
  await login(page);
  await page.goto('/admin/playback');
  // 清掉其他 spec（或本文件之前的运行）留下的播放记录。
  await page.request.post('/admin/playback/clear', { form: { csrfToken: await csrf(page), confirm: 'clear' } });
  await home(page);
  await expect(page.locator('[data-focus-tab="listening"]')).toHaveCount(0);
  await expect(page.locator('.h-hero[data-focus="recent"] [data-focus-tab="recent"]')).toBeVisible();
  await expect(page.locator('.h-rows')).toHaveCount(0);
  await expect(page.locator('.h-empty-inline', { hasText: '还没有未听完的歌曲' })).toBeVisible();
});

test('渲染：页头、聚焦卡、各模块与页脚', async ({ page }) => {
  await login(page);
  await home(page);
  await expect(page.locator('.h-search input')).toBeVisible();
  await expect(page.locator('.h-hero[data-focus="recent"]')).toBeVisible();
  await expect(page.locator('.h-hero[data-focus="recent"] [data-focus-tab="series"]')).toBeVisible();
  await expect(page.locator('.h-hero[data-focus="recent"] [data-focus-tab="random"]')).toBeVisible();
  await expect(page.locator('#s-new')).toHaveText('最近添加');
  await expect(page.locator('#s-works')).toHaveText('按作品漫游');
  await expect(page.locator('#s-songs')).toHaveText('主题曲与插曲');
  await expect(page.locator('#s-era')).toHaveText('编年');
  await expect(page.locator('.h-foot a.h-foot-review')).toContainText('作品关联待审核');
  // 侧栏导航：主页选中，wordmark 指向主页。
  await expect(page.locator('.sidebar a[data-nav="home"]')).toHaveClass(/active/);
  await expect(page.locator('.sidebar a[data-nav="home"]')).toHaveAttribute('aria-current', 'page');
  await expect(page.locator('.wordmark')).toHaveAttribute('href', '/admin/home');
});

test('根路径重定向到主页', async ({ page }) => {
  await login(page);
  await page.goto('/');
  await expect(page).toHaveURL(/\/admin\/home$/);
});

test('聚焦页签切换：作品聚焦与随机重温', async ({ page }) => {
  await login(page);
  await home(page);
  await page.locator('.h-hero[data-focus="recent"] [data-focus-tab="series"]').click();
  await expect(page.locator('.h-hero[data-focus="series"]')).toBeVisible();
  await expect(page.locator('.h-hero[data-focus="recent"]')).toBeHidden();
  await expect(page.locator('.h-hero[data-focus="series"] [data-focus-tab="series"]')).toHaveAttribute('aria-selected', 'true');
  // 系列卡片：扇形封面、成员作品 chips、播放系列全部歌曲。
  await expect(page.locator('.h-hero[data-focus="series"] .h-fan')).toBeVisible();
  await expect(page.locator('.h-hero[data-focus="series"] .h-chip').first()).toBeVisible();
  await expect(page.locator('.h-hero[data-focus="series"] [data-play-series]')).toBeVisible();

  await page.locator('.h-hero[data-focus="series"] [data-focus-tab="random"]').click();
  await expect(page.locator('.h-hero[data-focus="random"]')).toBeVisible();
  await expect(page.locator('[data-reroll]')).toBeVisible();
});

test('随机重温：换一张换出不同的专辑', async ({ page }) => {
  await login(page);
  await home(page);
  await page.locator('.h-hero[data-focus="recent"] [data-focus-tab="random"]').click();
  const pane = page.locator('.h-hero[data-focus="random"]');
  const before = await pane.getAttribute('data-album-id');
  await pane.locator('[data-reroll]').click();
  await expect.poll(async () => pane.getAttribute('data-album-id')).not.toBe(before);
  await expect(pane.locator('.h-hero-inner')).toBeVisible();
});

test('聚焦卡播放替换队列；卡片加入队列是追加', async ({ page }) => {
  await login(page);
  await home(page);
  // 先播放真实音频的 E2E Album（最近添加卡片上的播放按钮）。
  const card = fixtureAlbumCard(page);
  await card.hover();
  await card.locator('.album-queue-action[data-mode="play"]').click();
  await expect.poll(async () => (await queueState(page)).queue.length).toBe(20);
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');

  // 聚焦卡的“加入队列”是追加：20 + 聚焦专辑曲目数。
  const hero = page.locator('.h-hero[data-focus="recent"]');
  const factTexts = await hero.locator('.h-facts li').allTextContents();
  const heroTracks = Number(factTexts.map((t) => t.match(/^(\d+) 首$/)).find(Boolean)?.[1] || 0);
  expect(heroTracks).toBeGreaterThan(0);
  await hero.locator('.album-queue-action[data-mode="append"]').click();
  await expect.poll(async () => (await queueState(page)).queue.length).toBe(20 + heroTracks);
});

test('宽内容时聚焦卡右侧曲目预览可直接播放', async ({ page }) => {
  await login(page);
  await page.addInitScript(() => localStorage.setItem('032_np_collapsed', '1'));
  await page.setViewportSize({ width: 1920, height: 900 });
  await home(page);
  const side = page.locator('.h-hero[data-focus="recent"] .h-hero-side');
  await expect(side).toBeVisible();
  const rows = side.locator('[data-track-id]');
  const count = await rows.count();
  expect(count).toBeGreaterThan(1);
  const third = rows.nth(2);
  const title = await third.getAttribute('data-track-title');
  await third.locator('.row-play-btn').click();
  const state = await queueState(page);
  expect(state.queue.length).toBe(count);
  expect(state.queue[state.currentIndex].title).toBe(title);
});

test('断点续播：接着听与最近在听页签', async ({ page }) => {
  await login(page);
  await home(page);
  // 用 E2E Album 的第一首造一条“听到 0:20”的记录。
  const albumHref = await fixtureAlbumCard(page).locator('a.h-card-title').getAttribute('href');
  const albumID = albumHref.split('/').pop();
  const album = await (await page.request.get(`/api/v1/albums/${albumID}`)).json();
  const track = album.tracks[0];
  const token = await csrf(page);
  const sessionId = `home-resume-${Date.now()}`;
  await page.request.post('/api/v1/playback/events', { headers: { 'X-CSRF-Token': token }, data: { clientId: 'home-e2e', clientKind: 'web', sessionId, seq: 1, type: 'start', trackId: track.id, state: 'playing', durationMillis: 60000, positionMillis: 0 } });
  await page.request.post('/api/v1/playback/events', { headers: { 'X-CSRF-Token': token }, data: { clientId: 'home-e2e', clientKind: 'web', sessionId, seq: 2, type: 'pause', trackId: track.id, positionMillis: 20000, durationMillis: 60000 } });

  await home(page);
  const row = page.locator('.h-rows .h-row', { hasText: track.title });
  await expect(row).toBeVisible();
  await expect(row.locator('.h-row-time')).toHaveText('0:20 / 1:00');

  // 最近在听页签出现，主按钮是“从 0:20 继续”。
  await page.locator('.h-hero[data-focus="recent"] [data-focus-tab="listening"]').click();
  const listening = page.locator('.h-hero[data-focus="listening"]');
  await expect(listening).toBeVisible();
  const resumeBtn = listening.locator('[data-resume-track-id]');
  await expect(resumeBtn).toContainText('从 0:20 继续');
  await expect(row.locator('.h-row-art')).toHaveAttribute('data-resume-ms', '20000');

  // 从断点继续：替换队列并 seek 到 20 秒附近。
  await resumeBtn.click();
  const audio = page.locator('#global-audio-element');
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  await expect.poll(() => audio.evaluate((el) => el.currentTime)).toBeGreaterThan(15);
  expect(await audio.evaluate((el) => el.currentTime)).toBeLessThan(26);
  await expect(page.locator('#player-title')).toHaveText(track.title);
});

test('按作品漫游：类型筛选', async ({ page }) => {
  await login(page);
  await home(page);
  const rail = page.locator('.h-works');
  const total = await rail.locator('.h-work').count();
  expect(total).toBeGreaterThan(2);
  await page.locator('[data-work-filter="movie"]').click();
  await expect(rail.locator('.h-work:visible')).toHaveCount(1);
  await expect(rail.locator('.h-work:visible .h-work-title')).toHaveText('劇場版「鬼滅の刃」無限列車編');
  await page.locator('[data-work-filter="game"]').click();
  await expect(rail.locator('.h-work:visible')).toHaveCount(1);
  await expect(rail.locator('.h-work:visible .h-work-title')).toHaveText('鬼滅の刃 ヒノカミ血風譚');
  await page.locator('[data-work-filter="all"]').click();
  await expect(rail.locator('.h-work:visible')).toHaveCount(total);
});

test('编年：点击柱子切换年份，链接到专辑页年份筛选', async ({ page }) => {
  await login(page);
  await home(page);
  await page.locator('.h-year[data-year="2019"]').click();
  await expect(page.locator('#era-result h3')).toHaveText('2019 年');
  await expect(page.locator('#era-result .h-card-title').first()).toHaveText('紅蓮華');
  const link = page.locator('#era-result a.h-link', { hasText: '在专辑页查看全部' });
  await expect(link).toHaveAttribute('href', '/admin/albums?year=2019');
  await expect(page.locator('.h-year[data-year="2019"]')).toHaveAttribute('aria-pressed', 'true');
  await link.click();
  await expect(page).toHaveURL(/\/admin\/albums\?year=2019/);
  await expect(page.locator('.browser-header h1')).toHaveText('专辑');
});

test('主题曲与插曲：加入队列追加到末尾', async ({ page }) => {
  await login(page);
  await home(page);
  const card = fixtureAlbumCard(page);
  await card.hover();
  await card.locator('.album-queue-action[data-mode="play"]').click();
  await expect.poll(async () => (await queueState(page)).queue.length).toBe(20);
  const row = page.locator('.h-tlist .h-trow').first();
  const title = await row.getAttribute('data-track-title');
  await row.locator('.queue-append-btn').click();
  await expect.poll(async () => (await queueState(page)).queue.length).toBe(21);
  const state = await queueState(page);
  expect(state.queue[20].title).toBe(title);
});

test('PJAX 进出主页播放不中断', async ({ page }) => {
  await login(page);
  await home(page);
  const card = fixtureAlbumCard(page);
  await card.hover();
  await card.locator('.album-queue-action[data-mode="play"]').click();
  const audio = page.locator('#global-audio-element');
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);

  await page.locator('.sidebar a[data-nav="albums"]').click();
  await expect(page).toHaveURL(/\/admin\/albums$/);
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);

  await page.locator('.sidebar a[data-nav="home"]').click();
  await expect(page).toHaveURL(/\/admin\/home$/);
  await expect(page.locator('.home-main h1')).toHaveText('主页');
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);

  // 浏览器返回：恢复专辑页，播放仍继续。
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/albums$/);
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
});

test('歌单货架：播放歌单', async ({ page }) => {
  await login(page);
  await home(page);
  const token = await csrf(page);
  const name = `Home E2E ${Date.now()}`;
  await page.request.post('/admin/playlists', { form: { csrfToken: token, name } });
  const albums = await (await page.request.get('/api/v1/albums?limit=50')).json();
  const fixture = albums.items.find((a) => a.title === 'E2E Album');
  const album = await (await page.request.get(`/api/v1/albums/${fixture.id}`)).json();
  const playlists = await (await page.request.get('/api/v1/playlists')).json();
  const pl = playlists.items.find((p) => p.name === name);
  await page.request.post(`/admin/playlists/${pl.id}/tracks`, { form: { csrfToken: token, trackId: String(album.tracks[0].id) } });

  await home(page);
  const card = page.locator('.h-card-pl', { hasText: name });
  await expect(card).toBeVisible();
  await card.hover();
  await card.locator('[data-play-playlist]').click();
  await expect.poll(async () => (await queueState(page)).queue.length).toBe(1);
  await expect(page.locator('#player-title')).toHaveText(album.tracks[0].title);
});

test.describe('空曲库', () => {
  test.use({ baseURL: 'http://127.0.0.1:45443' });
  test('首次安装引导指向控制台扫描', async ({ page }) => {
    await login(page);
    await page.goto('/admin/home');
    await expect(page.locator('.h-first-run h2')).toHaveText('曲库还是空的');
    await page.locator('.h-first-run a', { hasText: '前往控制台扫描' }).click();
    await expect(page).toHaveURL(/\/admin$/);
    await expect(page.locator('.sidebar a[data-nav="home"]')).not.toHaveClass(/active/);
  });
});
