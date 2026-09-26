const { test, expect } = require('@playwright/test');

async function ready(page) {
  await page.setViewportSize({ width: 1672, height: 900 });
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await page.waitForURL(/\/admin$/);
  await expect.poll(async () => Number(await page.locator('#tracks-count').textContent())).toBeGreaterThan(0);
  await page.goto('/admin/tracks');
  await expect(page.locator('.row-play-btn').first()).toBeVisible();
  await page.locator('.row-play-btn').first().click();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  await page.locator('.track-table-row').nth(1).locator('summary').click();
  await page.locator('.track-table-row').nth(1).locator('.queue-append-btn').click();
  await expect(page.locator('#np-queue-count')).toHaveText('21');
}

test.beforeEach(async ({ page }) => ready(page));

test('remove a queued track and clear with undo restores its position', async ({ page }) => {
  await page.locator('.q-remove').nth(1).click({ force: true });
  await expect(page.locator('#np-queue-count')).toHaveText('20');
  await page.locator('.track-table-row').nth(1).locator('summary').click();
  await page.locator('.track-table-row').nth(1).locator('.queue-append-btn').click();
  await page.locator('#np-queue-clear').click();
  await expect(page.locator('#np-queue-count')).toHaveText('0');
  await page.getByRole('button', { name: '撤销' }).click();
  await expect(page.locator('#np-queue-count')).toHaveText('21');
  expect(await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue.length)).toBe(21);
});

test('Alt+Down reorders focused queue row and persists it', async ({ page }) => {
  const first = await page.locator('#np-queue-list li').first().locator('.q-title').textContent();
  await page.locator('#np-queue-list .q-row').first().focus();
  await page.keyboard.press('Alt+ArrowDown');
  await expect(page.locator('#np-queue-list li').nth(1).locator('.q-title')).toHaveText(first);
  await expect(page.locator('#np-queue-live')).toHaveText('已移到第 2 位');
  expect(await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(1);
});

test('favorite syncs server state and more menu navigates to album', async ({ page }) => {
  const trackId = await page.locator('#np-queue-list li').first().evaluate(el => JSON.parse(sessionStorage.getItem('032_player_state')).queue[Number(el.dataset.qindex)].id);
  const button = page.locator('#np-favorite');
  await expect(button).toBeEnabled();
  const before = await button.getAttribute('aria-pressed');
  await button.click();
  await expect(button).toHaveAttribute('aria-pressed', before === 'true' ? 'false' : 'true');
  await expect.poll(async () => (await (await page.request.get(`/api/v1/tracks/${trackId}`)).json()).isFavorite).toBe(before !== 'true');
  await page.locator('#np-more').click();
  await page.getByRole('menuitem', { name: '查看专辑' }).click();
  await expect(page).toHaveURL(/\/admin\/albums\/\d+$/);
});

test('fullscreen opens and Escape closes restoring focus', async ({ page }) => {
  await page.locator('#player-btn-fullscreen').click();
  await expect(page.locator('#player-fullscreen')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('#player-fullscreen')).toHaveCount(0);
  await expect(page.locator('#player-btn-fullscreen')).toBeFocused();
});

test('first row plays without hover', async ({ page }) => {
  await page.goto('/admin/tracks');
  await page.locator('.row-play-btn').first().click();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
});

test('slow pointer drag moves first queue row to third and persists order', async ({ page }) => {
  const before = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue.map(t => String(t.id)));
  const handle = page.locator('.q-drag').first();
  const target = page.locator('#np-queue-list li').nth(2);
  const sourceBox = await handle.boundingBox(), targetBox = await target.boundingBox();
  const x = sourceBox.x + sourceBox.width / 2;
  await page.mouse.move(x, sourceBox.y + sourceBox.height / 2);
  await page.mouse.down();
  // Deliberately small, separately dispatched pointer moves rather than one coalesced jump.
  for (let i = 1; i <= 30; i++) {
    await page.mouse.move(x, sourceBox.y + sourceBox.height / 2 +
      (targetBox.y + targetBox.height * .8 - sourceBox.y - sourceBox.height / 2) * i / 30);
    await page.waitForTimeout(12);
  }
  await page.mouse.up();
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(2);
  const after = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue.map(t => String(t.id)));
  expect(after).toEqual([before[1], before[2], before[0], ...before.slice(3)]);
  await expect(page.locator('#np-queue-list li.current')).toHaveAttribute('data-qindex', '2');
});

test('slow pointer drag moves third queue row to first and persists order', async ({ page }) => {
  const before = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue.map(t => String(t.id)));
  const handle = page.locator('.q-drag').nth(2);
  const first = page.locator('#np-queue-list li').first();
  const sourceBox = await handle.boundingBox(), firstBox = await first.boundingBox();
  const x = sourceBox.x + sourceBox.width / 2;
  const startY = sourceBox.y + sourceBox.height / 2;
  const endY = firstBox.y + firstBox.height * .2;
  await page.mouse.move(x, startY);
  await page.mouse.down();
  for (let i = 1; i <= 30; i++) {
    await page.mouse.move(x, startY + (endY - startY) * i / 30);
    await page.waitForTimeout(12);
  }
  await page.mouse.up();
  const expected = [before[2], before[0], before[1], ...before.slice(3)];
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue.map(t => String(t.id)))).toEqual(expected);
  const firstVisibleId = await page.locator('#np-queue-list li').first().evaluate(el => {
    const index = Number(el.dataset.qindex);
    return JSON.parse(sessionStorage.getItem('032_player_state')).queue[index].id;
  });
  expect(String(firstVisibleId)).toBe(before[2]);
});
