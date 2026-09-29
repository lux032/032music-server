// Playwright globalSetup：启动两个隔离的 fixture 实例——
//   · 共享实例 45439：保持 20 首曲目、1 张专辑的原始假设，供既有 spec 使用；
//   · 独立实例 45441：播种批次 3 与批次 6 的示例数据（seed_e2e.go 一次写入，
//     含系列建议与改名系列），供 work-review-batch3/batch6 两个 spec 使用，
//     避免污染共享 fixture（player-queue 等 spec 依赖全局曲目数）。
// 两个实例共用一个 findTool 解析结果，server 二进制只 build 一次。
// 种子失败会直接抛错中止整个 e2e 运行，不吞错误；每个实例启动后立刻写各自的
// state file，global teardown 按 state file 逐个关闭，中途失败也能清理。
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setup as fixtureSetup, findTool, buildServer } from './e2e-fixture.mjs';

const ROOT = path.resolve(import.meta.dirname, '..');
export const SHARED_STATE_FILE = path.join(os.tmpdir(), '032music-e2e-state.json');
export const BATCH3_STATE_FILE = path.join(os.tmpdir(), '032music-e2e-batch3-state.json');
export const BIN_STATE_FILE = path.join(os.tmpdir(), '032music-e2e-bin.json');
export const BATCH3_PORT = 45441;

// 与 fixture 复用同一个 findTool（env GO → PATH → 平台兜底路径），保证两处解析
// 结果一致；Playwright globalSetup 进程里按 PATH 解析 go 可能 ENOENT（MSYS bash
// 的 PATH 转换问题，见 e2e-fixture.mjs 注释），兜底路径不能省。
function findGo() {
  return findTool('go', 'C:\\Program Files\\Go\\bin\\go.exe');
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

// seed 依赖 fixture 首次扫描产生的曲目；扫描还没完成时 seed 以退出码 2 退出，
// 只对退出码 2 重试；其他非零退出码（编译错误、数据错误等）立即失败。
// 注意：`go run` 会把子进程退出码压成 1，所以先 build 再直接运行二进制。
async function seedBatch3(go) {
  const state = JSON.parse(fs.readFileSync(BATCH3_STATE_FILE, 'utf8'));
  const dbPath = path.join(state.tempRoot, 'data', 'music.db');
  const seedBin = path.join(state.tempRoot, process.platform === 'win32' ? 'seed_e2e.exe' : 'seed_e2e');
  const build = spawnSync(go, ['build', '-o', seedBin, './scripts/seed_e2e.go'], { cwd: ROOT, encoding: 'utf8' });
  if (build.error || build.status !== 0) {
    throw new Error(`seed_e2e build failed: ${build.error ? build.error.message : (build.stderr || '').trim()}`);
  }
  const deadline = Date.now() + 180_000;
  for (;;) {
    const res = spawnSync(seedBin, [dbPath], { cwd: ROOT, encoding: 'utf8' });
    if (res.status === 0) {
      console.log('[e2e-setup] batch3+batch6 seed data ready (isolated instance :45441)');
      return;
    }
    if (res.error) throw new Error(`seed_e2e failed to launch: ${res.error}`);
    const output = `${res.stdout || ''}${res.stderr || ''}`.trim();
    if (res.status !== 2) {
      throw new Error(`seed_e2e failed with exit code ${res.status}:\n${output}`);
    }
    if (Date.now() > deadline) {
      throw new Error(`seed_e2e still waiting for the initial scan after the deadline:\n${output}`);
    }
    await sleep(2000);
  }
}

export default async function setup() {
  const go = findGo();
  const ffmpeg = findTool('ffmpeg', 'C:\\ffmpeg\\bin\\ffmpeg.exe');

  // server 二进制只 build 一次，两个实例共用；记录在 BIN_STATE_FILE 供 teardown 清理。
  const binDir = fs.mkdtempSync(path.join(os.tmpdir(), '032music-e2e-bin-'));
  const serverBin = path.join(binDir, process.platform === 'win32' ? 'server.exe' : 'server');
  buildServer(go, serverBin);
  fs.writeFileSync(BIN_STATE_FILE, JSON.stringify({ binDir }));

  await fixtureSetup({ port: 45439, stateFile: SHARED_STATE_FILE, go, ffmpeg, serverBin });
  await fixtureSetup({
    port: BATCH3_PORT,
    stateFile: BATCH3_STATE_FILE,
    tempPrefix: '032music-e2e-batch3-',
    go,
    ffmpeg,
    serverBin
  });
  await seedBatch3(go);
}
