// Issue #3：作品关联审核页的批量勾选栏。只验证勾选、全选、Shift 连选与取消
// 确认，不真正提交，避免改动独立 fixture（45441）上被 batch6 spec 依赖的种子。
const { test, expect } = require('@playwright/test');

test.use({ baseURL: 'http://127.0.0.1:45441' });

test.beforeEach(async ({ page }, testInfo) => {
  test.skip(testInfo.project.name !== 'desktop-chromium', '审核页 UI 仅针对 PC 桌面端');
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await expect(page).toHaveURL(/\/admin$/);
});

test('series tab: select all, shift range and cancelled confirm keep data', async ({ page }) => {
  await page.goto('/admin/work-review?tab=series');
  const bar = page.locator('[data-review-batch]');
  const boxes = page.locator('input.review-select');
  const total = await boxes.count();
  expect(total).toBeGreaterThan(2);
  const count = bar.locator('[data-review-selected-count]');
  await expect(count).toHaveText(`已选 0 / ${total} 条`);
  await expect(bar.getByRole('button', { name: '批量接受' })).toBeDisabled();

  await bar.getByLabel('全选本页候选').check();
  await expect(count).toHaveText(`已选 ${total} / ${total} 条`);
  await expect(boxes.first()).toBeChecked();
  await expect(page.locator('.review-candidate-body.is-selected')).toHaveCount(total);
  await bar.getByLabel('全选本页候选').uncheck();
  await expect(count).toHaveText(`已选 0 / ${total} 条`);

  await boxes.nth(0).click();
  await boxes.nth(2).click({ modifiers: ['Shift'] });
  await expect(count).toHaveText(`已选 3 / ${total} 条`);
  await expect(bar.getByLabel('全选本页候选')).toHaveJSProperty('indeterminate', total > 3);
  await expect(bar.getByRole('button', { name: '批量拒绝' })).toBeEnabled();

  let message = '';
  page.once('dialog', dialog => { message = dialog.message(); dialog.dismiss(); });
  await bar.getByRole('button', { name: '批量拒绝' }).click();
  expect(message).toContain('拒绝选中的 3 条');
  await expect(page).toHaveURL(/\/admin\/work-review\?tab=series$/);
  await expect(boxes).toHaveCount(total);
});

test('albums and tracks tabs offer per-card selection; works tab does not', async ({ page }) => {
  await page.goto('/admin/work-review?tab=albums');
  await expect(page.locator('input.review-select')).toHaveCount(1);
  await expect(page.locator('[data-review-batch]')).toContainText('批量拒绝并抑制');
  await page.goto('/admin/work-review?tab=tracks');
  await expect(page.locator('input.review-select')).toHaveCount(1);
  await page.goto('/admin/work-review?tab=works');
  await expect(page.locator('[data-review-batch]')).toHaveCount(0);
});
