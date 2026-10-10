// Playwright globalTeardown：逐个关闭所有 fixture 实例（共享 45439 + 批次 3
// 45441）并清理共用的 server 二进制目录。任何一个清理失败都不影响其他的清理；
// global setup 中途失败时，已启动的实例也按各自 state file 关闭。
import fs from 'node:fs';
import { teardown as fixtureTeardown } from './e2e-fixture.mjs';
import { SHARED_STATE_FILE, BATCH3_STATE_FILE, EMPTY_STATE_FILE, BIN_STATE_FILE } from './e2e-global-setup.mjs';

export default async function teardown() {
  const errors = [];
  for (const stateFile of [SHARED_STATE_FILE, BATCH3_STATE_FILE, EMPTY_STATE_FILE]) {
    try {
      await fixtureTeardown({ stateFile });
    } catch (err) {
      errors.push(err);
    }
  }
  // binDir / BIN_STATE_FILE 清理失败也要上报，不静默吞掉（文件不存在除外）。
  if (fs.existsSync(BIN_STATE_FILE)) {
    try {
      const bin = JSON.parse(fs.readFileSync(BIN_STATE_FILE, 'utf8'));
      fs.rmSync(bin.binDir, { recursive: true, force: true, maxRetries: 20, retryDelay: 250 });
      fs.rmSync(BIN_STATE_FILE, { force: true });
    } catch (err) {
      errors.push(err);
    }
  }
  if (errors.length > 0) {
    throw new Error(`fixture teardown errors: ${errors.map((e) => e.message).join('; ')}`);
  }
}
