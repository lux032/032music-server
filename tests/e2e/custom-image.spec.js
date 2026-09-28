// 4.5.7 自定义专辑封面与歌手图片 e2e：
//   · 专辑编辑抽屉上传 PNG → 页面封面地址变化 → 恢复默认；
//   · 歌手编辑抽屉上传 PNG → 头像地址带版本号 → 恢复默认；
//   · M1 回归：主编辑表单（保存覆盖信息）必须继续走 PJAX，不做整页重载。
// 使用共享实例（45439）；所有用例 try/finally 保证结束时恢复默认，失败也不污染其他 spec。
const { test, expect } = require('@playwright/test');

// 8×8 PNG（与后端单测同源的极小样本），通过 buffer 注入，无需落地 fixture 文件。
const PNG_BASE64 = 'iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAHElEQVR4nGJhYDghx8CAiVhABDYwOCUAAQAA//+SmQKNqlOHfgAAAABJRU5ErkJggg==';
const PNG_FILE = { name: 'custom-cover.png', mimeType: 'image/png', buffer: Buffer.from(PNG_BASE64, 'base64') };

async function login(page) {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
}

// restoreAlbumCover 在自定义封面存在时通过 UI 恢复默认；没有自定义封面时无操作。
async function restoreAlbumCover(page, albumUrl) {
  await page.goto(albumUrl);
  const hasCustom = await page.locator('img.hero-cover').count();
  if (!hasCustom) return;
  await page.locator('a[data-drawer-open="edit"]').first().click();
  const drawer = page.locator('#edit[data-drawer]');
  await expect(drawer).toHaveClass(/is-open/);
  const reset = drawer.getByRole('button', { name: '恢复默认封面' });
  if (!(await reset.count())) return;
  await Promise.all([
    page.waitForURL(/\/admin\/albums\/\d+\?notice=/),
    reset.click()
  ]);
}

// restoreArtistImage 在自定义歌手图片（含继承）存在时通过 UI 恢复默认。
async function restoreArtistImage(page, artistUrl) {
  await page.goto(artistUrl);
  await page.locator('a[data-drawer-open="artist-edit"]').first().click();
  const drawer = page.locator('#artist-edit[data-drawer]');
  await expect(drawer).toHaveClass(/is-open/);
  const reset = drawer.getByRole('button', { name: '恢复默认图片' });
  if (!(await reset.count())) return;
  await Promise.all([
    page.waitForURL(/\/admin\/artists\/\d+\?notice=/),
    reset.click()
  ]);
}

test.beforeEach(async ({ page }) => { await login(page); });

test('album drawer uploads a custom cover and restores the default', async ({ page }, testInfo) => {
  // 桌面端专属用例（编辑抽屉只在 PC 布局验证）。
  test.skip(testInfo.project.name !== 'desktop-chromium');

  await page.goto('/admin/albums');
  await page.locator('a[href^="/admin/albums/"]').first().click();
  await expect(page).toHaveURL(/\/admin\/albums\/\d+/);
  const albumUrl = page.url();

  // fixture 专辑没有内嵌封面：上传前是占位符。
  await expect(page.locator('.hero-cover.cover-placeholder')).toBeVisible();

  try {
    await page.locator('a[data-drawer-open="edit"]').first().click();
    const drawer = page.locator('#edit[data-drawer]');
    // 抽屉 section 的子节点全是 position:fixed，本身高度为 0：用 is-open 类断言开态
    await expect(drawer).toHaveClass(/is-open/);

    // 带文件输入的上传表单走原生提交（data-no-pjax），提交后回到专辑页并显示提示。
    await drawer.locator('input[type="file"][name="image"]').setInputFiles(PNG_FILE);
    await Promise.all([
      page.waitForURL(/\/admin\/albums\/\d+\?notice=/),
      drawer.getByRole('button', { name: '上传封面' }).click()
    ]);
    await expect(page.locator('.toast')).toContainText('封面已更新');

    const cover = page.locator('img.hero-cover');
    await expect(cover).toBeVisible();
    const customSrc = await cover.getAttribute('src');
    expect(customSrc).toContain('/api/v1/artwork/');
    // 上传的封面真正可取。
    const response = await page.request.get(customSrc);
    expect(response.ok()).toBeTruthy();
  } finally {
    await restoreAlbumCover(page, albumUrl);
  }
  await expect(page.locator('.toast')).toContainText('已恢复默认封面');
  await expect(page.locator('.hero-cover.cover-placeholder')).toBeVisible();
});

test('artist drawer uploads a custom image and restores the default', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium');

  await page.goto('/admin/artists/album');
  await page.locator('.artist-row a[href^="/admin/artists/"]').first().click();
  await expect(page).toHaveURL(/\/admin\/artists\/\d+$/);
  const artistUrl = page.url();

  // fixture 歌手没有自动图片：上传前是占位符。
  await expect(page.locator('.artist-portrait').first()).not.toHaveJSProperty('tagName', 'IMG');

  try {
    await page.locator('a[data-drawer-open="artist-edit"]').first().click();
    const drawer = page.locator('#artist-edit[data-drawer]');
    await expect(drawer).toHaveClass(/is-open/);

    await drawer.locator('input[type="file"][name="image"]').setInputFiles(PNG_FILE);
    await Promise.all([
      page.waitForURL(/\/admin\/artists\/\d+\?notice=/),
      drawer.getByRole('button', { name: '上传图片' }).click()
    ]);
    await expect(page.locator('.toast')).toContainText('歌手图片已更新');

    const portrait = page.locator('img.artist-portrait');
    await expect(portrait).toBeVisible();
    const src = await portrait.getAttribute('src');
    expect(src).toContain('/image?v=');
    const response = await page.request.get(src);
    expect(response.ok()).toBeTruthy();
  } finally {
    await restoreArtistImage(page, artistUrl);
  }
  await expect(page.locator('.toast')).toContainText('已恢复默认歌手图片');
  await expect(page.locator('img.artist-portrait')).toHaveCount(0);
});

test('saving album metadata stays on PJAX without a full page reload (M1)', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium');

  await page.goto('/admin/albums');
  await page.locator('a[href^="/admin/albums/"]').first().click();
  await expect(page).toHaveURL(/\/admin\/albums\/\d+/);

  // 整页重载会清空 window 上的标记；PJAX 提交则保留。
  await page.evaluate(() => { window.__m1Marker = 'still-here'; });
  await page.locator('a[data-drawer-open="edit"]').first().click();
  const drawer = page.locator('#edit[data-drawer]');
  await expect(drawer).toHaveClass(/is-open/);
  // 不修改任何字段直接保存：对共享实例是数据 no-op。
  await drawer.getByRole('button', { name: '保存覆盖信息' }).click();
  await expect(page.locator('.toast')).toContainText('专辑信息已保存');
  expect(await page.evaluate(() => window.__m1Marker)).toBe('still-here');
});
