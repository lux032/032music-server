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
  const composer = page.locator('a[href*="?credit=composer#credits"]').first();
  await expect(composer).toHaveText('E2E Composer');
  await composer.click();
  await expect(page).toHaveURL(/credit=composer#credits$/);
  await expect(page.locator('#credits')).toContainText('幕后作品');
  await expect(page.locator('#credits [data-track-id]')).toHaveCount(20);
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
  await expect(page).toHaveURL(/credit=composer#credits$/);
  await expect(page.locator('#credits')).toBeInViewport();
  await expect(page.locator('#credits')).toContainText('幕后作品');
});
