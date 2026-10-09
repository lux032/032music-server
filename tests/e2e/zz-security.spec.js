// Account & security page. The file name sorts after every other spec so it
// runs last within the desktop project; later projects (mobile-375,
// boundary-820) must still see the fixture's environment credentials, so
// every state-changing test restores them in `finally` from a fresh browser
// context, and afterAll unconditionally resets all four items as a safety
// net. Exactly three wrong-password attempts are made (API token form,
// old-password login, media-token reveal), far below the 10-failure login
// limit; each successful password check resets the counter.
const { test, expect } = require('@playwright/test');

// The fixture's environment username; MUSIC_SERVER_ADMIN_USERNAME wins when
// the runner is pointed at a server configured with a different account.
const ADMIN = process.env.MUSIC_SERVER_ADMIN_USERNAME || 'admin';
const PASSWORD = 'e2e-admin-password';
const NEW_PASSWORD = 'e2e-rotated-password-2';
const ENV_API_TOKEN = 'e2e-api-token-00000000000000000000';
const ENV_MEDIA_TOKEN = 'e2e-media-token-000000000000000000';
const SECURITY = '/admin/settings/security';
// Reset order: tokens first, the password last (it rotates the session).
const ALL_ITEMS = ['api-token', 'media-token', 'username', 'password'];

test.describe.configure({ mode: 'serial' });

// Username most recently submitted on the login form. A test that renames
// the account logs in with the new name, so restoreToEnv must try it too.
let lastUsername = ADMIN;

async function login(page, password = PASSWORD, username = ADMIN) {
  await page.goto('/admin/login');
  const input = page.getByLabel('用户名');
  await input.fill(username);
  lastUsername = await input.inputValue();
  await page.getByLabel('密码').fill(password);
  await page.getByRole('button', { name: /登录/ }).click();
}

async function loginOK(page, password = PASSWORD, username = ADMIN) {
  await login(page, password, username);
  await expect(page).toHaveURL(/\/admin$/);
}

// Usernames that may currently own the account: the last one typed into
// the login form, the configured environment username, and the fixture default.
function candidateUsernames() {
  return [...new Set([lastUsername, process.env.MUSIC_SERVER_ADMIN_USERNAME, ADMIN, 'admin'].filter(Boolean))];
}

// loginAny logs in with the first working username/password pair and
// returns it, or null when none works.
async function loginAny(page, usernames, passwords) {
  for (const username of usernames) {
    for (const password of passwords) {
      await login(page, password, username);
      await page.waitForLoadState();
      if (/\/admin$/.test(new URL(page.url()).pathname)) return { username, password };
    }
  }
  return null;
}

function item(page, name) { return page.locator(`[data-security-item="${name}"]`); }

async function apiStatus(request, token) {
  return (await request.get('/api/v1/status', { headers: { Authorization: `Bearer ${token}` } })).status();
}

// restoreToEnv resets the given items to their environment values from a
// brand-new browser context, independent of any page the test may have
// lost. It logs in with the first candidate username (see
// candidateUsernames) and password in `passwords` that works, skips items
// already on the environment value, and finally checks that the original
// fixture password logs in.
async function restoreToEnv(browser, baseURL, passwords, items = ALL_ITEMS) {
  const usernames = candidateUsernames();
  const context = await browser.newContext({ baseURL });
  try {
    const page = await context.newPage();
    const account = await loginAny(page, usernames, passwords);
    if (!account) throw new Error(`restoreToEnv: no candidate username (${usernames.join(', ')}) and password can log in`);
    let current = account.password;
    for (const key of items) {
      await page.goto(SECURITY);
      if (await item(page, key).locator('[data-source="override"]').count() === 0) continue;
      const csrfToken = await page.locator('meta[name="csrf-token"]').getAttribute('content');
      const res = await page.request.post(`${SECURITY}/reset/${key}`, { form: { csrfToken, current_password: current } });
      expect(res.ok()).toBeTruthy();
      await page.goto(SECURITY);
      await expect(item(page, key).locator('[data-source="env"]')).toBeVisible();
      if (key === 'password') current = PASSWORD;
    }
  } finally {
    await context.close();
  }
  const verify = await browser.newContext({ baseURL });
  try {
    const verifyPage = await verify.newPage();
    // After a username reset the environment name is expected; try it first.
    if (!await loginAny(verifyPage, [...new Set([ADMIN, ...usernames])], [PASSWORD])) throw new Error('restoreToEnv: the environment password no longer logs in');
  } finally {
    await verify.close();
  }
}

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', 'security flows run once, on desktop only');
  await loginOK(page);
});

test.afterAll(async ({ browser }, testInfo) => {
  if (testInfo.project.name !== 'desktop-chromium') return;
  await restoreToEnv(browser, testInfo.project.use.baseURL, [PASSWORD, NEW_PASSWORD]);
});

test('security page is reachable from the admin sidebar group and highlighted', async ({ page }) => {
  await page.goto('/admin');
  const link = page.locator('#sidebar-admin-items a[data-nav="security"]');
  if (!(await link.isVisible())) await page.locator('#sidebar-group-toggle').click();
  await link.click();
  await expect(page).toHaveURL(new RegExp(`${SECURITY}$`));
  await expect(page.getByRole('heading', { level: 1, name: '账号与安全' })).toBeVisible();
  await expect(link).toHaveAttribute('aria-current', 'page');
  await expect(link).toHaveClass(/active/);
  await expect(page.locator('#sidebar-admin-group')).toHaveClass(/open/);
  for (const name of ['username', 'password', 'api-token', 'media-token']) {
    await expect(item(page, name).locator('[data-source="env"]')).toBeVisible();
  }
});

test('wrong current password is rejected with a fixed notice and changes nothing', async ({ page, request }) => {
  await page.goto(SECURITY);
  const card = item(page, 'api-token');
  await card.locator('.security-inline-form input[name="current_password"]').fill('not-the-password');
  await card.getByRole('button', { name: '生成新 Token' }).click();
  await expect(page.locator('[data-security-notice]')).toHaveText('当前密码不正确。');
  await expect(page).not.toHaveURL(/notice=/);
  await expect(page.locator('[data-security-flash]')).toHaveCount(0);
  await expect(item(page, 'api-token').locator('[data-source="env"]')).toBeVisible();
  expect(await apiStatus(request, ENV_API_TOKEN)).toBe(200);
});

test('generated API token is shown once, works, and replaces the old token', async ({ page, request, context, browser, baseURL }) => {
  let changed = false;
  try {
    await page.goto(SECURITY);
    const card = item(page, 'api-token');
    await card.locator('.security-inline-form input[name="current_password"]').fill(PASSWORD);
    changed = true; // mark before submitting: the response may never be observed
    await card.getByRole('button', { name: '生成新 Token' }).click();
    const flash = page.locator('[data-security-flash]');
    await expect(flash).toBeVisible();
    await expect(flash).toContainText('新的 API Token');
    await expect(flash).toContainText('只显示这一次');
    const token = await flash.locator('[data-security-flash-value]').inputValue();
    expect(token.length).toBeGreaterThanOrEqual(32);
    expect(page.url()).not.toContain(token);
    await expect(item(page, 'api-token').locator('[data-source="override"]')).toBeVisible();

    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await flash.getByRole('button', { name: '复制' }).click();
    await expect(flash.locator('[data-copy-status]')).toHaveText('已复制到剪贴板。');
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(token);

    await page.reload();
    await expect(page.locator('[data-security-flash]')).toHaveCount(0);
    await expect(page.locator('body')).not.toContainText(token);

    expect(await apiStatus(request, token)).toBe(200);
    expect(await apiStatus(request, ENV_API_TOKEN)).toBe(401);
  } finally {
    if (changed) await restoreToEnv(browser, baseURL, [PASSWORD], ['api-token']);
    expect(await apiStatus(request, ENV_API_TOKEN)).toBe(200);
  }
});

test('password change logs out other sessions and keeps the current one', async ({ page, browser, baseURL }) => {
  const newPassword = NEW_PASSWORD;
  let submitted = false;
  const other = await browser.newContext({ baseURL });
  try {
    const otherPage = await other.newPage();
    await loginOK(otherPage);

    await page.goto(SECURITY);
    const card = item(page, 'password');
    await card.locator('input[name="current_password"]').fill(PASSWORD);
    await card.locator('input[name="new_password"]').fill(newPassword);
    await card.locator('input[name="confirm_password"]').fill(newPassword);
    // From here on the new password may be in effect even if the response
    // is never observed; cleanup tries it first.
    submitted = true;
    await card.getByRole('button', { name: '修改密码' }).click();
    await expect(page.locator('[data-security-notice]')).toHaveText('密码已修改，其他登录会话已退出。');
    await expect(item(page, 'password').locator('[data-source="override"]')).toBeVisible();

    await otherPage.goto('/admin');
    await expect(otherPage).toHaveURL(/\/admin\/login$/);

    await page.goto('/admin');
    await expect(page).toHaveURL(/\/admin$/);

    const probe = await browser.newContext({ baseURL });
    try {
      const probePage = await probe.newPage();
      await login(probePage, PASSWORD);
      await expect(probePage).toHaveURL(/\/admin\/login$/);
      await expect(probePage.getByRole('alert')).toHaveText('用户名或密码不正确。');
      await loginOK(probePage, newPassword);
    } finally {
      await probe.close();
    }
  } finally {
    await other.close();
    // Fresh context: works even if `page` was closed or its session lost.
    await restoreToEnv(browser, baseURL, submitted ? [newPassword, PASSWORD] : [PASSWORD], ['password']);
  }
});

test('revealing the media token requires the current password', async ({ page }) => {
  await page.goto(SECURITY);
  const reveal = item(page, 'media-token').locator('[data-security-reveal]');
  await reveal.locator('summary').click();
  await reveal.locator('input[name="current_password"]').fill('not-the-password');
  await reveal.getByRole('button', { name: '查看' }).click();
  await expect(page.locator('[data-security-notice]')).toHaveText('当前密码不正确。');
  await expect(page.locator('[data-security-flash]')).toHaveCount(0);

  const again = item(page, 'media-token').locator('[data-security-reveal]');
  await again.locator('summary').click();
  await again.locator('input[name="current_password"]').fill(PASSWORD);
  await again.getByRole('button', { name: '查看' }).click();
  const flash = page.locator('[data-security-flash]');
  await expect(flash).toContainText('当前媒体 Token');
  await expect(flash.locator('[data-security-flash-value]')).toHaveValue(ENV_MEDIA_TOKEN);
  await page.reload();
  await expect(page.locator('[data-security-flash]')).toHaveCount(0);
});
