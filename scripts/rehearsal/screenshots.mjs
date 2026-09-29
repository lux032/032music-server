// screenshots.mjs — 演练截图：登录副本服务,截取 /admin/work-review 与
// /admin/works,保存到 .local/mockups/impl47/rehearsal-*.png。
// 环境变量: REHEARSAL_BASE(必填, 如 http://127.0.0.1:45490)、REHEARSAL_USER、
// REHEARSAL_PASS。仅供 rehearsal.sh 调用,不访问外网。
import fs from 'node:fs';
import path from 'node:path';
import { chromium } from '@playwright/test';

const ROOT = path.resolve(import.meta.dirname, '..', '..');
const OUT = path.join(ROOT, '.local', 'mockups', 'impl47');

async function main() {
  const base = process.env.REHEARSAL_BASE;
  if (!base) throw new Error('REHEARSAL_BASE is required');
  const user = process.env.REHEARSAL_USER || 'admin';
  const pass = process.env.REHEARSAL_PASS || '';
  fs.mkdirSync(OUT, { recursive: true });
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.goto(base + '/admin/login');
  await page.getByLabel('用户名').fill(user);
  await page.getByLabel('密码').fill(pass);
  await page.getByRole('button', { name: /登录/ }).click();
  await page.waitForURL(/\/admin$/);
  for (const [name, url] of [
    ['rehearsal-work-review.png', '/admin/work-review'],
    ['rehearsal-works.png', '/admin/works'],
  ]) {
    await page.goto(base + url);
    await page.waitForTimeout(600); // PJAX 进入动画 ~0.2s,余量等待
    await page.screenshot({ path: path.join(OUT, name), fullPage: false });
    console.log('saved', path.join(OUT, name));
  }
  await browser.close();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
