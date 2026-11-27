// 待审头像工作台（/admin/matches?tab=avatars）浏览器端到端回归测试：
// 使用完全独立的临时实例（端口 45448），绝不污染 45439/45441 等既有夹具。
// 验证在真实 PJAX（router.js + admin.js）环境下：
//   1. 单卡「联网找新图」原地走 JSON 入队，不触发旧表单提交或 PJAX 整页刷新；
//   2. 队列 2 秒轮询刷新期间，若选中集合未变，「确认清空 N 位头像」武装态不被重置；选中变化时安全解除；
//   3. 多选「批量清空头像」经二次确认后携带非空 candidateIds 提交、落库并移除对应卡片；
//   4. 多选「批量保留原图」同样携带非空 candidateIds 提交、落库并移除对应卡片。
const { test, expect } = require('@playwright/test');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawn, spawnSync } = require('node:child_process');

const ROOT = path.resolve(__dirname, '../..');
const AVATAR_PORT = 45448;
const BIN_STATE_FILE = path.join(os.tmpdir(), '032music-e2e-bin.json');

let tempRoot = null;
let serverChild = null;

function findGo() {
  const candidates = [process.env.GO || 'go', 'C:\\Program Files\\Go\\bin\\go.exe'];
  for (const candidate of candidates) {
    const probe = spawnSync(candidate, ['version'], { encoding: 'utf8' });
    if (!probe.error && probe.status === 0) return candidate;
  }
  throw new Error('go binary not found');
}

async function startIsolatedServer() {
  const go = findGo();
  tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), '032music-e2e-avatar-'));
  const dataDir = path.join(tempRoot, 'data');
  const musicDir = path.join(tempRoot, 'music');
  fs.mkdirSync(dataDir, { recursive: true });
  fs.mkdirSync(musicDir, { recursive: true });

  let serverBin = null;
  try {
    if (fs.existsSync(BIN_STATE_FILE)) {
      const binState = JSON.parse(fs.readFileSync(BIN_STATE_FILE, 'utf8'));
      const candidate = path.join(binState.binDir, process.platform === 'win32' ? 'server.exe' : 'server');
      if (fs.existsSync(candidate)) serverBin = candidate;
    }
  } catch (_) {
    // Ignore inaccessible shared bin state on multi-user hosts and build locally.
  }
  if (!serverBin) {
    serverBin = path.join(tempRoot, process.platform === 'win32' ? 'server.exe' : 'server');
    const built = spawnSync(go, ['build', '-o', serverBin, './cmd/server'], { cwd: ROOT, encoding: 'utf8' });
    if (built.error || built.status !== 0) {
      throw new Error(`go build server failed: ${built.error ? built.error.message : built.stderr}`);
    }
  }

  const dbPath = path.join(dataDir, 'music.db');
  const logFd = fs.openSync(path.join(tempRoot, 'server.log'), 'a');
  serverChild = spawn(serverBin, [], {
    cwd: ROOT,
    stdio: ['ignore', logFd, logFd],
    env: {
      ...process.env,
      MUSIC_SERVER_ADDRESS: `127.0.0.1:${AVATAR_PORT}`,
      MUSIC_SERVER_DATA_DIR: dataDir,
      MUSIC_SERVER_DATABASE_PATH: dbPath,
      MUSIC_SERVER_MUSIC_DIR: musicDir,
      MUSIC_SERVER_LIBRARY_NAME: 'Avatar E2E Library',
      MUSIC_SERVER_ADMIN_USERNAME: 'admin',
      MUSIC_SERVER_ADMIN_PASSWORD: 'e2e-admin-password',
      MUSIC_SERVER_API_TOKEN: 'e2e-api-token-00000000000000000000',
      MUSIC_SERVER_MEDIA_TOKEN: 'e2e-media-token-000000000000000000',
      MUSIC_SERVER_COOKIE_SECURE: 'false',
      MUSIC_SERVER_LOG_LEVEL: 'error',
      MUSIC_SERVER_DEV_MODE: 'false',
      MUSIC_SERVER_WORK_POSTER_BACKFILL: 'false',
      MUSIC_SERVER_WATCH_INTERVAL: '0',
      MUSIC_SERVER_CAST_SSDP: 'false',
    },
  });
  fs.closeSync(logFd);

  const healthURL = `http://127.0.0.1:${AVATAR_PORT}/api/v1/health`;
  const deadline = Date.now() + 30_000;
  for (;;) {
    try {
      const res = await fetch(healthURL);
      if (res.ok) break;
    } catch (_) { /* starting */ }
    if (serverChild.exitCode !== null) {
      const log = fs.readFileSync(path.join(tempRoot, 'server.log'), 'utf8');
      throw new Error(`avatar e2e server exited with code ${serverChild.exitCode}:\n${log}`);
    }
    if (Date.now() > deadline) {
      throw new Error(`avatar e2e server did not become healthy on port ${AVATAR_PORT}`);
    }
    await new Promise((r) => setTimeout(r, 250));
  }

  const seedBin = path.join(tempRoot, process.platform === 'win32' ? 'seed_avatar_batch.exe' : 'seed_avatar_batch');
  const buildSeed = spawnSync(go, ['build', '-o', seedBin, './scripts/seed_avatar_batch_e2e.go'], { cwd: ROOT, encoding: 'utf8' });
  if (buildSeed.error || buildSeed.status !== 0) {
    throw new Error(`seed_avatar_batch_e2e build failed: ${buildSeed.error ? buildSeed.error.message : buildSeed.stderr}`);
  }
  const seeded = spawnSync(seedBin, [dbPath, dataDir], { cwd: ROOT, encoding: 'utf8' });
  if (seeded.error || seeded.status !== 0) {
    throw new Error(`seed_avatar_batch_e2e failed: ${seeded.error ? seeded.error.message : seeded.stderr}`);
  }
}

async function stopIsolatedServer() {
  if (serverChild && serverChild.exitCode === null) {
    try {
      serverChild.kill('SIGTERM');
    } catch (_) { /* already stopped */ }
    const deadline = Date.now() + 10_000;
    while (serverChild.exitCode === null && Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 100));
    }
  }
  serverChild = null;
  if (tempRoot) {
    fs.rmSync(tempRoot, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
    tempRoot = null;
  }
}

test.use({ baseURL: `http://127.0.0.1:${AVATAR_PORT}` });

test.describe('pending avatars batch actions and queue interaction under PJAX', () => {
  test.beforeAll(async ({}, testInfo) => {
    if (testInfo.project.name !== 'desktop-chromium') return;
    testInfo.setTimeout(90_000);
    await startIsolatedServer();
  });

  test.afterAll(async ({}, testInfo) => {
    if (testInfo.project.name !== 'desktop-chromium') return;
    await stopIsolatedServer();
  });

  test('single-card search, poll-safe inline confirm, batch clear, and batch keep all work under PJAX', async ({ page }, testInfo) => {
    test.skip(testInfo.project.name !== 'desktop-chromium', '待审头像工作台仅针对 PC 桌面端验收');
    test.setTimeout(60_000);

    await page.goto('/admin/login');
    await page.getByLabel('用户名').fill('admin');
    await page.getByLabel('密码').fill('e2e-admin-password');
    await page.getByRole('button', { name: /登录/ }).click();
    await expect(page).toHaveURL(/\/admin$/);

    await page.goto('/admin/matches?tab=avatars');
    const root = page.locator('[data-avatar-workbench]');
    await expect(root).toBeVisible();
    await expect(root.locator('[data-avatar-card]')).toHaveCount(6);

    const cardByName = (name) => root.locator(`[data-avatar-card][data-artist-name="${name}"]`);
    const idOf = async (name) => {
      const id = await cardByName(name).getAttribute('data-candidate-id');
      expect(id).toBeTruthy();
      return id;
    };

    const clearId1 = await idOf('AvatarClearOne');
    const clearId2 = await idOf('AvatarClearTwo');
    const keepId1 = await idOf('AvatarKeepOne');
    const keepId2 = await idOf('AvatarKeepTwo');
    const searchId = await idOf('AvatarSearchOne');

    // 1) 单卡「联网找新图」原地走 JSON 入队 + 队列 2 秒轮询期间不重置二次确认武装态
    let legacySearchPosts = 0;
    let jsonEnqueuePosts = 0;
    let queuePollCount = 0;
    const runningSnapshot = {
      status: 'running',
      total: 1,
      completed: 0,
      waiting: 0,
      found: 0,
      notFound: 0,
      failed: 0,
      currentId: Number(searchId),
      currentArtist: 'AvatarSearchOne',
      queuedPositions: {},
      completedResults: [],
    };
    const onRequest = (req) => {
      if (req.method() === 'POST' && /\/admin\/matches\/images\/candidates\/\d+\/search$/.test(new URL(req.url()).pathname)) {
        legacySearchPosts += 1;
      }
      if (req.method() === 'POST' && new URL(req.url()).pathname === '/admin/matches/images/search-queue/enqueue') {
        jsonEnqueuePosts += 1;
      }
    };
    page.on('request', onRequest);

    await page.route('**/admin/matches/images/search-queue/enqueue', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ added: 1, queue: runningSnapshot }),
      });
    });
    await page.route('**/admin/matches/images/search-queue.json', async (route) => {
      queuePollCount += 1;
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ queue: runningSnapshot }),
      });
    });

    // 先勾选待清空两张卡并标记页面未发生整页刷新
    await page.evaluate(() => { window.__avatarNoReloadMarker = 'alive'; });
    await cardByName('AvatarClearOne').locator('[data-avatar-select]').check();
    await cardByName('AvatarClearTwo').locator('[data-avatar-select]').check();
    await expect(root.locator('[data-avatar-selected-label]')).toHaveText('已选 2 项');
    const syncedClearIds = await root.locator('form[data-avatar-batch-form="clear"] input[name="candidateIds"]').inputValue();
    expect(syncedClearIds.split(',').sort()).toEqual([clearId1, clearId2].sort());

    // 点击单卡「联网找新图」：只走一次 JSON 入队，不发旧表单 POST，不刷新页面，不丢勾选
    await cardByName('AvatarSearchOne').locator('[data-avatar-search-btn]').click();
    await expect(cardByName('AvatarSearchOne')).toHaveAttribute('data-search-state', 'running');
    await expect.poll(() => jsonEnqueuePosts).toBe(1);
    expect(legacySearchPosts).toBe(0);
    expect(await page.evaluate(() => window.__avatarNoReloadMarker)).toBe('alive');
    await expect(cardByName('AvatarClearOne').locator('[data-avatar-select]')).toBeChecked();
    await expect(cardByName('AvatarClearTwo').locator('[data-avatar-select]')).toBeChecked();

    // 武装「批量清空头像」二次确认，等待至少 2 次真实 2 秒队列轮询，确认武装态与「取消」按钮稳住不被冲掉
    const batchClearBtn = root.locator('[data-avatar-batch-clear]');
    await batchClearBtn.click();
    await expect(batchClearBtn).toHaveAttribute('data-confirm-armed', '1');
    await expect(batchClearBtn).toHaveText('确认清空 2 位头像');
    await expect(root.locator('form[data-avatar-batch-form="clear"] .match-inline-confirm-cancel')).toBeVisible();

    const pollsAtArm = queuePollCount;
    await expect.poll(() => queuePollCount, { timeout: 10_000 }).toBeGreaterThanOrEqual(pollsAtArm + 2);
    await expect(batchClearBtn).toHaveAttribute('data-confirm-armed', '1');
    await expect(batchClearBtn).toHaveText('确认清空 2 位头像');
    await expect(root.locator('form[data-avatar-batch-form="clear"] .match-inline-confirm-cancel')).toBeVisible();

    // 当勾选集合实际变化时，二次确认态应安全解除并更新计数
    await cardByName('AvatarPollExtra').locator('[data-avatar-select]').check();
    await expect(batchClearBtn).not.toHaveAttribute('data-confirm-armed', '1');
    await expect(batchClearBtn).toHaveText('批量清空头像 (3)');
    await expect(root.locator('form[data-avatar-batch-form="clear"] .match-inline-confirm-cancel')).toHaveCount(0);
    await cardByName('AvatarPollExtra').locator('[data-avatar-select]').uncheck();
    await expect(batchClearBtn).toHaveText('批量清空头像 (2)');

    page.off('request', onRequest);
    await page.unroute('**/admin/matches/images/search-queue/enqueue');
    await page.unroute('**/admin/matches/images/search-queue.json');

    // 2) 真实 PJAX 提交「批量清空头像」：验证请求体携带非空 candidateIds、后端落库并刷新移除已清卡片
    const clearPostPromise = page.waitForRequest((req) =>
      req.method() === 'POST' && new URL(req.url()).pathname === '/admin/matches/images/candidates/batch-clear'
    );
    await batchClearBtn.click();
    await expect(batchClearBtn).toHaveText('确认清空 2 位头像');
    await batchClearBtn.click();
    const clearReq = await clearPostPromise;
    const clearParams = new URLSearchParams(clearReq.postData() || '');
    expect((clearParams.get('candidateIds') || '').split(',').sort()).toEqual([clearId1, clearId2].sort());

    await expect(page.locator('.toast')).toContainText('已批量清空 2 位艺术家缓存头像');
    await expect(cardByName('AvatarClearOne')).toHaveCount(0);
    await expect(cardByName('AvatarClearTwo')).toHaveCount(0);
    await expect(root.locator('[data-avatar-card]')).toHaveCount(4);

    // 刷新页面确认数据库已持久化清空结果
    await page.reload();
    await expect(root.locator('[data-avatar-card]')).toHaveCount(4);
    await expect(cardByName('AvatarClearOne')).toHaveCount(0);
    await expect(cardByName('AvatarClearTwo')).toHaveCount(0);

    // 3) 真实 PJAX 提交「批量保留原图」：同样验证非空 candidateIds、落库与移除卡片
    await cardByName('AvatarKeepOne').locator('[data-avatar-select]').check();
    await cardByName('AvatarKeepTwo').locator('[data-avatar-select]').check();
    const keepPostPromise = page.waitForRequest((req) =>
      req.method() === 'POST' && new URL(req.url()).pathname === '/admin/matches/images/candidates/batch-keep'
    );
    const batchKeepBtn = root.locator('[data-avatar-batch-keep]');
    await expect(batchKeepBtn).toHaveText('批量保留原图 (2)');
    await batchKeepBtn.click();
    const keepReq = await keepPostPromise;
    const keepParams = new URLSearchParams(keepReq.postData() || '');
    expect((keepParams.get('candidateIds') || '').split(',').sort()).toEqual([keepId1, keepId2].sort());

    await expect(page.locator('.toast')).toContainText('已批量确认保留 2 位艺术家原图');
    await expect(cardByName('AvatarKeepOne')).toHaveCount(0);
    await expect(cardByName('AvatarKeepTwo')).toHaveCount(0);
    await expect(root.locator('[data-avatar-card]')).toHaveCount(2);

    await page.reload();
    await expect(root.locator('[data-avatar-card]')).toHaveCount(2);
  });
});
