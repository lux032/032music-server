const { test, expect } = require('@playwright/test');

async function login(page) {
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
}

// 详情页左上角的“返回”回到来时的页面，而不是写死的列表页；
// 直接打开（新标签页、书签）时才兜底回对应列表。
test.beforeEach(async ({ page }) => { await login(page); });

async function firstAlbumID(page) {
  await page.goto('/admin/albums');
  const href = await page.locator('.library-album-card .album-card-title a').first().getAttribute('href');
  return href.split('/').pop();
}

async function setAlbumFavorite(page, id, favorite) {
  await page.goto(`/admin/albums/${id}`);
  const heart = page.locator('.album-actions .favorite-button');
  if ((await heart.getAttribute('aria-pressed')) !== String(favorite)) {
    await heart.click();
    await expect(heart).toHaveAttribute('aria-pressed', String(favorite));
  }
}

test('album opened from favorites returns to favorites', async ({ page }) => {
  const id = await firstAlbumID(page);
  await setAlbumFavorite(page, id, true);
  try {
    await page.goto('/admin/favorites?sort=title');
    await page.locator(`main a[href="/admin/albums/${id}"]`).first().click();
    await expect(page).toHaveURL(new RegExp(`/admin/albums/${id}$`));
    const back = page.locator('.back-button');
    await expect(back).toHaveText('返回我的收藏');
    await back.click();
    await expect(page).toHaveURL(/\/admin\/favorites\?sort=title$/);
  } finally {
    await setAlbumFavorite(page, id, false);
  }
});

test('back keeps the specific label when coming from the fallback list', async ({ page }) => {
  await firstAlbumID(page);
  await page.locator('.library-album-card .album-card-title a').first().click();
  await expect(page.locator('.back-button')).toHaveText('返回专辑列表');
});

test('chained detail pages unwind one step at a time', async ({ page }) => {
  const id = await firstAlbumID(page);
  await page.goto(`/admin/albums/${id}`);
  const artistLink = page.locator('main a[href^="/admin/artists/"]').first();
  test.skip(!(await artistLink.count()), 'fixture album has no linked artist');
  const albumTitle = (await page.title()).split(' · ')[0];
  await artistLink.click();
  await expect(page).toHaveURL(/\/admin\/artists\/\d+/);
  const back = page.locator('.back-button');
  await expect(back).toHaveText(/^[\x00-\x7f]/.test(albumTitle) ? `返回 ${albumTitle}` : `返回${albumTitle}`);
  await back.click();
  await expect(page).toHaveURL(new RegExp(`/admin/albums/${id}$`));
  await expect(page.locator('.back-button')).toHaveText('返回专辑列表');
  await page.locator('.back-button').click();
  await expect(page).toHaveURL(/\/admin\/albums$/);
});

test('directly opened detail page falls back to its list', async ({ page }) => {
  const id = await firstAlbumID(page);
  await page.goto(`/admin/albums/${id}`);
  const back = page.locator('.back-button');
  await expect(back).toHaveText('返回专辑列表');
  await back.click();
  await expect(page).toHaveURL(/\/admin\/albums$/);
});
