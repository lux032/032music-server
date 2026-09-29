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
  // Precondition: the first queued track is still the current one.
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(0);
  const first = await page.locator('#np-queue-list li').first().locator('.q-title').textContent();
  await page.locator('#np-queue-list .q-row').first().focus();
  await page.keyboard.press('Alt+ArrowDown');
  await expect(page.locator('#np-queue-list li').nth(1).locator('.q-title')).toHaveText(first);
  await expect(page.locator('#np-queue-live')).toHaveText('已移到第 2 位');
  expect(await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(1);
});

test('current-playing and bottom-bar links navigate without interrupting audio', async ({ page }) => {
  const audio = page.locator('#global-audio-element');
  await expect.poll(() => audio.evaluate(el => el.paused)).toBe(false);

  for (const selector of ['#np-album a', '#np-artist a:first-child', '#player-artist span a', '#player-artist > a:first-child']) {
    const link = page.locator(selector);
    await expect(link).toBeVisible();
    await expect(link).toHaveAttribute('href', /\/admin\/(albums|artists)\/\d+$/);
    const href = await link.getAttribute('href');
    const before = await audio.evaluate(el => el.currentTime);
    await link.click();
    await expect(page).toHaveURL(new RegExp(`${href}$`));
    await expect.poll(() => audio.evaluate(el => el.paused)).toBe(false);
    await expect.poll(() => audio.evaluate(el => el.currentTime)).toBeGreaterThan(before);
  }
});

test('clicking queue artist text keeps the current track playing', async ({ page }) => {
  const audio = page.locator('#global-audio-element');
  const currentID = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue[JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex].id);
  const artistText = page.locator('#np-queue-list li.current .q-artist');
  await expect(artistText).toBeVisible();
  await expect(artistText.locator('a')).toHaveCount(0);
  await artistText.click();
  await expect.poll(() => audio.evaluate(el => el.paused)).toBe(false);
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue[JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex].id)).toBe(currentID);
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
  // Precondition: the first queued track is still the current one.
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(0);
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
  // Precondition: the first queued track is still the current one.
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(0);
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

test('dragging to the queue bottom edge auto-scrolls and drops past the initial viewport', async ({ page }) => {
  // Precondition: the first queued track is still the current one.
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(0);
  const list = page.locator('#np-queue-list');
  const before = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue.map(t => String(t.id)));
  expect(before.length).toBeGreaterThanOrEqual(15);
  // The queue must overflow the panel, otherwise there is nothing to auto-scroll.
  const metrics = await list.evaluate(el => ({ scrollTop: el.scrollTop, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight }));
  expect(metrics.scrollHeight).toBeGreaterThan(metrics.clientHeight + 100);
  expect(metrics.scrollTop).toBe(0);
  // Highest queue index whose row is fully visible before the drag.
  const lastVisible = await list.evaluate(el => {
    const box = el.getBoundingClientRect();
    return Math.max(...Array.from(el.children).filter(row => row.getBoundingClientRect().bottom <= box.bottom).map(row => Number(row.dataset.qindex)));
  });
  expect(lastVisible).toBeLessThan(before.length - 1);

  const listBox = await list.boundingBox();
  const handleBox = await page.locator('.q-drag').first().boundingBox();
  const x = handleBox.x + handleBox.width / 2;
  const startY = handleBox.y + handleBox.height / 2;
  // Inside the 45px auto-scroll band at the bottom of the list.
  const edgeY = listBox.y + listBox.height - 12;
  await page.mouse.move(x, startY);
  await page.mouse.down();
  for (let i = 1; i <= 30; i++) {
    await page.mouse.move(x, startY + (edgeY - startY) * i / 30);
    await page.waitForTimeout(12);
  }
  // Hold at the edge: the auto-scroll timer keeps scrolling without pointer moves.
  await expect.poll(() => list.evaluate(el => el.scrollTop), { timeout: 5_000 }).toBeGreaterThan(0);
  const maxScroll = metrics.scrollHeight - metrics.clientHeight;
  await expect.poll(() => list.evaluate(el => el.scrollTop), { timeout: 8_000 }).toBeGreaterThanOrEqual(maxScroll - 1);
  // Keep holding: auto-scroll must stop at the queue end rather than chase
  // the overflow created by the translated drag row.
  await page.waitForTimeout(600);
  expect(await list.evaluate(el => el.scrollTop)).toBeLessThanOrEqual(maxScroll + 1);
  await page.mouse.move(x, edgeY - 1);
  await page.mouse.up();

  const moved = before[0];
  await expect.poll(() => page.evaluate(id => JSON.parse(sessionStorage.getItem('032_player_state')).queue.map(t => String(t.id)).indexOf(id), moved)).toBeGreaterThan(lastVisible);
  const after = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue.map(t => String(t.id)));
  const to = after.indexOf(moved);
  const expected = before.slice(1);
  expected.splice(to, 0, moved);
  expect(after).toEqual(expected);
  // The playing track moved with the drop, so the current index follows it.
  expect(await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(to);
  await expect(page.locator('#np-queue-list li.current')).toHaveAttribute('data-qindex', String(to));
});
