const { test, expect } = require('@playwright/test');

for (const timezoneId of ['Asia/Shanghai', 'America/New_York', 'UTC']) {
  test.describe(timezoneId, () => {
    test.use({ timezoneId });

    test('时间跟随浏览器，覆盖跨日、夏令时、动态片段与局部导航', async ({ page }) => {
      await page.goto('/admin/login');
      await page.getByLabel('用户名').fill('admin');
      await page.getByLabel('密码').fill('e2e-admin-password');
      await page.getByRole('button', { name: /登录/ }).click();
      await page.waitForURL(/\/admin$/);
      await page.goto('/admin/home');
      await expect(page.locator('[data-local-today]')).toHaveAttribute('title', `浏览器时区：${timezoneId}`);
      const expectedToday = await page.evaluate(() => {
        const now = new Date();
        return `${now.getMonth() + 1}月${now.getDate()}日 星期${'日一二三四五六'[now.getDay()]}`;
      });
      await expect(page.locator('[data-local-today]')).toHaveText(expectedToday);
      const realTime = page.locator('.h-card-meta time[data-local-time]').first();
      await expect(realTime).toHaveAttribute('title', new RegExp(timezoneId));
      const expectedReal = await realTime.evaluate(element => {
        const d = new Date(element.getAttribute('datetime'));
        const pad = n => String(n).padStart(2, '0');
        return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
      });
      await expect(realTime).toHaveText(expectedReal);

      await page.evaluate(() => {
        const section = document.createElement('section');
        section.id = 'timezone-fixture';
        section.innerHTML = `
          <time id="cross-day" data-local-time datetime="2026-01-01T01:30:00Z">fallback</time>
          <time id="dst-before" data-local-time datetime="2026-03-08T06:59:00Z">fallback</time>
          <time id="dst-after" data-local-time datetime="2026-03-08T07:00:00Z">fallback</time>
          <time id="offset" data-local-time datetime="2026-01-01T09:30:00+08:00">fallback</time>
          <time id="invalid" data-local-time datetime="unexpected">unchanged</time>
          <time id="date-only" data-local-time datetime="2026-01-01">release date</time>
          <time id="no-zone" data-local-time datetime="2026-01-01T01:30:00">ambiguous</time>`;
        document.querySelector('#app-main').appendChild(section);
      });
      const expected = {
        'Asia/Shanghai': ['2026-01-01 09:30', '2026-03-08 14:59', '2026-03-08 15:00'],
        'America/New_York': ['2025-12-31 20:30', '2026-03-08 01:59', '2026-03-08 03:00'],
        'UTC': ['2026-01-01 01:30', '2026-03-08 06:59', '2026-03-08 07:00']
      }[timezoneId];
      await expect(page.locator('#cross-day')).toHaveText(expected[0]);
      await expect(page.locator('#offset')).toHaveText(expected[0]);
      await expect(page.locator('#dst-before')).toHaveText(expected[1]);
      await expect(page.locator('#dst-after')).toHaveText(expected[2]);
      await expect(page.locator('#invalid')).toHaveText('unchanged');
      await expect(page.locator('#date-only')).toHaveText('release date');
      await expect(page.locator('#no-zone')).toHaveText('ambiguous');

      await page.evaluate(() => { window.__timezoneNavigationMarker = true; });
      await page.locator('.sidebar a[data-nav="playback"]:visible').click();
      await expect(page).toHaveURL(/\/admin\/playback/);
      await page.locator('.sidebar a[data-nav="home"]:visible').click();
      await expect(page).toHaveURL(/\/admin\/home/);
      expect(await page.evaluate(() => window.__timezoneNavigationMarker)).toBe(true);
      await expect(page.locator('.h-card-meta time[data-local-time]').first()).toHaveAttribute('title', new RegExp(timezoneId));
    });
  });
}
