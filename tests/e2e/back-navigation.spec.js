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

test('one-shot notice is dropped from the URL and not repeated on return', async ({ page }) => {
  await page.goto('/admin?notice=' + encodeURIComponent('扫描已启动'));
  await expect(page.locator('.toast')).toContainText('扫描已启动');
  await expect(page).toHaveURL(/\/admin$/);
  await page.locator('.sidebar a[data-nav="albums"]').click();
  await expect(page).toHaveURL(/\/admin\/albums$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/admin$/);
  await expect(page.locator('main .toast')).toHaveCount(0);
});

test('login returns to the page that asked for it', async ({ page }) => {
  await page.context().clearCookies();
  await page.goto('/admin/favorites?kind=tracks');
  await expect(page).toHaveURL(/\/admin\/login\?next=/);
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin\/favorites\?kind=tracks$/);
});

test('browser back closes an open drawer before leaving the page', async ({ page }) => {
  const id = await firstAlbumID(page);
  await page.locator('.library-album-card .album-card-title a').first().click();
  await expect(page).toHaveURL(new RegExp(`/admin/albums/${id}$`));
  const drawer = page.locator('#edit[data-drawer]');

  await page.locator('a[data-drawer-open="edit"]').first().click();
  await expect(drawer).toHaveClass(/is-open/);
  await page.goBack();
  await expect(drawer).not.toHaveClass(/is-open/);
  await expect(page).toHaveURL(new RegExp(`/admin/albums/${id}$`));

  // Closing it in the page (Esc) leaves no extra history entry behind.
  await page.locator('a[data-drawer-open="edit"]').first().click();
  await expect(drawer).toHaveClass(/is-open/);
  await page.keyboard.press('Escape');
  await expect(drawer).not.toHaveClass(/is-open/);
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/albums$/);
});

test('saving from a drawer and deleting a page keep history clean', async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', 'creates and deletes data; runs once');
  await page.goto('/admin/albums');
  await page.goto('/admin/works');
  await page.locator('.browser-header [data-drawer-open="create-work"]').click();
  const form = page.locator('#create-work form[action="/admin/works"]');
  await form.locator('input[name=title]').fill(`E2E 返回导航 ${Date.now()}`);
  await form.locator('select[name=type]').selectOption('anime');
  await form.getByRole('button', { name: '创建作品' }).click();
  await expect(page.locator('.toast')).toContainText('作品已创建');
  await expect(page).toHaveURL(/\/admin\/works\/\d+$/);
  const workURL = page.url();

  // Back skips the drawer's entry: the list, with the drawer closed.
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/works$/);
  await expect(page.locator('#create-work')).not.toHaveClass(/is-open/);
  await page.goForward();
  await expect(page).toHaveURL(workURL);

  // Deleting replaces the removed page's entry instead of stacking on it.
  await page.locator('[data-dialog-open="work-delete-dialog"]').click();
  await page.locator('#work-delete-dialog').getByRole('button', { name: '确认删除' }).click();
  await expect(page.locator('.toast')).toContainText('作品已删除');
  await expect(page).toHaveURL(/\/admin\/works$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/works$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/admin\/albums$/);
});

test('player bar title opens the playing album', async ({ page }) => {
  const id = await firstAlbumID(page);
  await page.goto(`/admin/albums/${id}`);
  await page.locator('.album-hero .primary-round').click();
  await page.goto('/admin/tracks');
  const title = page.locator('#player-title a');
  await expect(title).toHaveAttribute('href', new RegExp(`/admin/albums/${id}$`));
  await title.click();
  await expect(page).toHaveURL(new RegExp(`/admin/albums/${id}$`));
});
