const { test, expect } = require('@playwright/test');

// Uses the existing batch3/batch6 fixture on 45441, whose 20 scanned tracks
// also carry composer tags. The separate shared fixture on 45439 is unchanged.
test.use({ baseURL: 'http://127.0.0.1:45441' });
test.beforeEach(async ({ page }) => {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
});

test('album composer links open paginated backstage works', async ({ page }) => {
  const artists = await (await page.request.get('/admin/options/artists?role=composer&q=E2E')).json();
  const credits = await (await page.request.get(`/api/v1/artists/${artists[0].id}/credits?role=composer`)).json();
  await page.goto(`/admin/albums/${credits.items[0].albumId}`);
  const composer = page.locator('a[href*="?role=composer"]').first();
  await expect(composer).toHaveText('E2E Composer');
  await composer.click();
  await expect(page).toHaveURL(/\/admin\/credits\/\d+[?]role=composer$/);
  await expect(page.locator('#credits')).toContainText('幕后作品');
  await expect(page.locator('#credits [data-track-id]')).toHaveCount(20);
  await expect(page.locator('body')).toHaveAttribute('data-nav', 'credits');
  await expect(page.locator('.sidebar a[data-nav="credits"]').first()).toHaveClass(/active/);
  await expect(page.locator('#credit-collaborators')).toContainText('E2E Artist');
  await expect(page.locator('#credit-albums')).toContainText('E2E Album');
  await page.locator('#credits .row-play-btn').first().click();
  const audio = page.locator('#global-audio-element');
  await expect.poll(() => audio.evaluate(el => el.paused)).toBe(false);
  await page.locator('#credits').getByRole('link', { name: '在歌曲列表中筛选' }).click();
  await expect(page.locator('.filter-tags')).toContainText('作曲：E2E Composer');
});

test('focus keeps multiple values and removes only one condition', async ({ page }) => {
  await page.goto('/admin/tracks');
  await page.locator('.focus-more summary').click();
  await page.locator('select[name="trackType"]').selectOption(['regular']);
  await page.locator('select[name="format"]').selectOption(['mp3', 'flac']);
  await page.locator('select[name="quality"]').selectOption('lossy');
  await page.locator('.apply-focus').click();
  await expect(page.locator('.filter-tags')).toContainText('有损');
  await expect(page.locator('.filter-tags')).toContainText('格式：mp3');
  await expect(page.locator('[data-track-id]')).toHaveCount(20);
  await page.locator('.filter-tag').filter({ hasText: '格式：flac' }).click();
  await expect(page.locator('.filter-tags')).not.toContainText('格式：flac');
  await expect(page.locator('.filter-tags')).toContainText('格式：mp3');
  await expect(page.locator('.filter-tags')).toContainText('普通曲目');
  await expect(page.locator('[data-track-id]')).toHaveCount(20);
});

test('backstage navigation and role directory lead to credits detail', async ({ page }) => {
  await page.goto('/admin/tracks');
  const backstage = page.locator('.sidebar a[data-nav="credits"]').first();
  if (!(await backstage.isVisible())) {
    await page.getByRole('button', { name: '更多导航', exact: true }).click();
    await page.locator('#sidebar-more-sheet a[data-nav="credits"]').click();
  } else {
    await backstage.click();
  }
  await expect(page).toHaveURL(/\/admin\/credits/);
  await page.getByRole('link', { name: '作曲', exact: true }).click();
  await expect(page).toHaveURL(/credit=composer/);
  await expect(page.locator('.result-count')).toContainText('位幕后人员');
  await page.locator('.artist-row h2').filter({ hasText: 'E2E Composer' }).click();
  await expect(page).toHaveURL(/\/admin\/credits\/\d+[?]role=composer$/);
  await page.locator('#credits').scrollIntoViewIfNeeded();
  await expect(page.locator('#credits')).toBeInViewport();
  await expect(page.locator('#credits')).toContainText('幕后作品');
});


test('profile escape and player backstage navigation preserve playback', async ({ page }) => {
  const artists = await (await page.request.get('/admin/options/artists?role=composer&q=E2E')).json();
  const id = artists[0].id;
  await page.goto(`/admin/artists/${id}`);
  await expect(page).toHaveURL(new RegExp(`/admin/credits/${id}$`));
  await page.getByRole('link', { name: '资料与身份管理' }).click();
  await expect(page).toHaveURL(new RegExp(`/admin/artists/${id}[?]view=profile$`));
  await expect(page.locator('#credits')).toHaveCount(0);
  await page.locator('.credit-fact-link').click();
  await page.locator('#credits .row-play-btn').first().click();
  const audio = page.locator('#global-audio-element');
  await expect.poll(() => audio.evaluate(el => el.paused)).toBe(false);
  if (await page.locator('#now-playing').getAttribute('aria-hidden') === 'true') await page.locator('#player-btn-queue').click();
  const link = page.locator('#np-credits a[data-player-nav]').first();
  await expect(link).toHaveAttribute('href', `/admin/credits/${id}?role=composer`);
  const before = await audio.evaluate(el => el.currentTime);
  await link.click();
  await expect(page).toHaveURL(new RegExp(`/admin/credits/${id}[?]role=composer$`));
  await expect.poll(() => audio.evaluate(el => el.paused)).toBe(false);
  await expect.poll(() => audio.evaluate(el => el.currentTime)).toBeGreaterThan(before);
});

test('archive mixed artist and backstage detail at 1280', async ({ page }, testInfo) => {
  test.skip(!process.env.ARCHIVE_MOCKUPS || testInfo.project.name !== 'desktop-chromium', 'opt-in 1280 archive');
  const fs = require('node:fs');
  fs.mkdirSync('.local/mockups/credit-detail', { recursive: true });
  const artists = await (await page.request.get('/admin/options/artists?role=composer&q=LiSA')).json();
  expect(artists).toHaveLength(1);
  await page.goto(`/admin/artists/${artists[0].id}`);
  await expect(page.locator('.credit-fact-link')).toContainText('幕后');
  await page.locator('#app-main').evaluate(async (main) => {
    await Promise.all(Array.from(main.querySelectorAll('img')).map(async (image) => {
      image.loading = 'eager';
      if (!image.complete) await new Promise(resolve => { image.addEventListener('load', resolve, { once: true }); image.addEventListener('error', resolve, { once: true }); });
      if (image.naturalWidth) await image.decode();
    }));
    await document.fonts.ready;
  });
  await page.screenshot({ path: '.local/mockups/credit-detail/mixed-artist-1280.png', fullPage: true, animations: 'disabled' });
  await page.locator('.credit-fact-link').click();
  await expect(page.locator('#credits')).toBeVisible();
  await page.locator('#app-main').evaluate(async (main) => {
    await Promise.all(Array.from(main.querySelectorAll('img')).map(async (image) => {
      image.loading = 'eager';
      if (!image.complete) await new Promise(resolve => { image.addEventListener('load', resolve, { once: true }); image.addEventListener('error', resolve, { once: true }); });
      if (image.naturalWidth) await image.decode();
    }));
    await document.fonts.ready;
  });
  await page.screenshot({ path: '.local/mockups/credit-detail/credit-detail-1280.png', fullPage: true, animations: 'disabled' });
  await page.locator('#credits .filter-tag').filter({ hasText: '作曲' }).click();
  await expect(page.locator('#credits .filter-tag[aria-current="page"]')).toContainText('作曲 4');
  await expect(page.locator('.artist-hero .album-facts')).toContainText('全部 4 首');
  await page.screenshot({ path: '.local/mockups/credit-detail/credit-detail-multirole-1280.png', fullPage: true, animations: 'disabled' });
});

// Detail-albums batch: credit detail albums reuse the shared card, so play,
// multi-select and bulk merge behave like the album list (option A).
test('credit detail albums play, select across grids and merge in pick order', async ({ page }) => {
  const artists = await (await page.request.get('/admin/options/artists?role=composer&q=E2E')).json();
  const id = artists[0].id;
  await page.goto(`/admin/credits/${id}?role=composer`);
  await expect(page.locator('.album-selection-bar')).toHaveCount(1);
  await expect(page.locator('.album-selection-bar')).toBeHidden();
  const card = page.locator('#credit-albums .library-album-card').first();
  await expect(card).toBeVisible();
  const realID = await card.getAttribute('data-album-id');

  // Play acts on the full album without navigating (Medium-3).
  await card.hover();
  await card.locator('.play-disc').click();
  const audio = page.locator('#global-audio-element');
  await expect.poll(() => audio.evaluate(el => el.paused)).toBe(false);
  await expect(page).toHaveURL(new RegExp(`/admin/credits/${id}[?]role=composer$`));

  // Validation gap: the 45441 fixture has a single album in a single group,
  // so a cloned card/grid simulates the second release group; real multi-group
  // markup is covered by the Go render tests.
  await page.evaluate(() => {
    const grid = document.querySelector('#credit-albums .album-browser');
    const second = grid.cloneNode(true);
    const cloneCard = second.querySelector('.library-album-card');
    cloneCard.dataset.albumId = '999999';
    cloneCard.dataset.albumTitle = 'Clone Album';
    grid.after(second);
  });
  await card.locator('.album-select').click();
  // In selection mode a cover click toggles instead of navigating.
  await page.locator('.library-album-card[data-album-id="999999"] .album-tile-cover').click();
  await expect(page).toHaveURL(new RegExp(`/admin/credits/${id}[?]role=composer$`));
  // High-1: every grid on the page carries is-selecting, not just the first.
  await expect(page.locator('.album-browser.is-selecting')).toHaveCount(2);
  const cloneToggle = page.locator('.library-album-card[data-album-id="999999"] .album-select');
  await expect(cloneToggle).toHaveAttribute('aria-pressed', 'true');
  await expect.poll(() => cloneToggle.evaluate(el => getComputedStyle(el).opacity)).toBe('1');
  const bar = page.locator('.album-selection-bar');
  await expect(bar).toContainText('已选择 2 张专辑');
  await expect(bar).toContainText('主专辑《E2E Album》');
  await expect(card.locator('.album-main-badge')).toBeVisible();

  let body = null;
  await page.route('**/admin/albums/merge', async route => {
    body = route.request().postData();
    await route.fulfill({ status: 500, contentType: 'text/plain', body: 'blocked in test' });
  });
  // Medium-2: the destructive confirm names the main album.
  let confirmMessage = '';
  page.once('dialog', dialog => {
    confirmMessage = dialog.message();
    return dialog.accept();
  });
  await bar.getByRole('button', { name: '合并' }).click();
  await expect.poll(() => body).not.toBeNull();
  const params = new URLSearchParams(body);
  expect(params.getAll('album')).toEqual([realID, '999999']);
  expect(params.get('returnTo')).toBe(`/admin/credits/${id}?role=composer`);
  expect(confirmMessage).toContain('主专辑《E2E Album》');

  await page.keyboard.press('Escape');
  await expect(bar).toBeHidden();
  await expect(page.locator('.library-album-card.is-selected')).toHaveCount(0);
});

// High-3: the detail grid sizes to the container at every project viewport
// (1280 / 820 / 375), controls never overlap, and reduced motion is reused.
test('credit detail album grid keeps controls usable at every viewport', async ({ page }) => {
  const artists = await (await page.request.get('/admin/options/artists?role=composer&q=E2E')).json();
  await page.goto(`/admin/credits/${artists[0].id}?role=composer`);
  const card = page.locator('#credit-albums .library-album-card').first();
  await expect(card).toBeVisible();
  const width = (await card.boundingBox()).width;
  expect(width).toBeGreaterThanOrEqual(140);
  const boxes = await card.evaluate(el => {
    const rect = sel => el.querySelector(sel).getBoundingClientRect();
    return { select: rect('.album-select'), play: rect('.play-disc') };
  });
  const overlap = boxes.select.left < boxes.play.right && boxes.play.left < boxes.select.right && boxes.select.top < boxes.play.bottom && boxes.play.top < boxes.select.bottom;
  expect(overlap).toBe(false);

  await page.emulateMedia({ reducedMotion: 'reduce' });
  const duration = await card.locator('.cover-wrap').evaluate(el => getComputedStyle(el).transitionDuration);
  // base.css collapses every transition to 0.01ms (serialized as 1e-05s) and
  // pages.css turns the card transition off entirely; both mean no motion.
  expect(['0s', '0.01ms', '1e-05s']).toContain(duration);
});

// Artist detail: release groups share the same card and bulk bar, with the
// detail address as the safe return target.
test('artist detail release groups share selection UI and return address', async ({ page }) => {
  const artists = await (await page.request.get('/admin/options/artists?role=album&q=E2E')).json();
  const id = artists[0].id;
  await page.goto(`/admin/artists/${id}`);
  await expect(page.locator('.artist-discography .album-browser.detail-album-grid').first()).toBeVisible();
  await expect(page.locator('.album-selection-bar')).toHaveCount(1);
  const card = page.locator('.artist-discography .library-album-card').first();
  const realID = await card.getAttribute('data-album-id');
  await card.hover();
  await card.locator('.album-select').click();
  const bar = page.locator('.album-selection-bar');
  await expect(bar).toBeVisible();
  await expect(bar).toContainText('已选择 1 张专辑');
  // A single selection disables merge; cancelling restores the idle state.
  await expect(bar.getByRole('button', { name: '合并' })).toBeDisabled();
  expect(await page.locator('[data-album-bulk-form] input[name="returnTo"]').getAttribute('value')).toBe(`/admin/artists/${id}`);
  await bar.getByRole('button', { name: '取消选择' }).click();
  await expect(bar).toBeHidden();
  await expect(page.locator(`.library-album-card[data-album-id="${realID}"].is-selected`)).toHaveCount(0);
});
