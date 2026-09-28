// Isolated E2E fixture server lifecycle for Playwright globalSetup/globalTeardown.
// Uses only synthetic audio in a fresh temp dir; never touches the real library.
//
// setup/teardown 接受参数以支持多个隔离实例并存：共享实例保持默认的 45439 与
// 原 state file；批次 3 的 spec 用独立实例（45441、独立 state file），种子数据
// 只写进独立实例，共享 fixture 保持 20 首曲目、1 张专辑的原始假设。
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const ROOT = path.resolve(import.meta.dirname, '..');
const DEFAULT_STATE_FILE = path.join(os.tmpdir(), '032music-e2e-state.json');

// 优先用环境变量（GO / FFMPEG）或 PATH（让 Node 直接解析命令名，不依赖
// where/which），保留 Windows 安装路径作为最后兜底。
//
// 已知问题：在某些 MSYS bash + npx 启动链下，Playwright globalSetup 进程里
// spawnSync('go', ...) 按 PATH 解析会返回 ENOENT，而同一个 shell 里直接跑
// `node -e` 却能找到 go（MSYS 只在启动原生进程时转换 PATH，经过 npx 的
// shell 脚本中转后 Playwright 进程拿到的 PATH 可能仍是 POSIX 形式）。因此
// 兜底路径是必须保留的，global setup 与 fixture 复用同一个 findTool（导出
// 供 e2e-global-setup.mjs 使用），保证两处解析结果一致。
export function findTool(name, fallback) {
  const envKey = name.toUpperCase();
  const candidate = process.env[envKey] || name;
  const probeArgs = name === 'go' ? ['version'] : ['-version'];
  const probe = spawnSync(candidate, probeArgs, { encoding: 'utf8' });
  if (!probe.error && probe.status === 0) return candidate;
  if (fallback && fs.existsSync(fallback)) {
    const fb = spawnSync(fallback, probeArgs, { encoding: 'utf8' });
    if (!fb.error && fb.status === 0) return fallback;
  }
  const detail = probe.error ? probe.error.message : (probe.stderr || '').trim();
  const pathSeen = process.env.PATH || process.env.Path || '';
  throw new Error(`required tool not found: ${name} ('${candidate}' on PATH failed: ${detail}; PATH=${pathSeen.slice(0, 200)}...; set the ${envKey} env var)`);
}

function run(cmd, args, label) {
  const res = spawnSync(cmd, args, { cwd: ROOT, stdio: 'inherit' });
  if (res.status !== 0) throw new Error(`${label} failed with exit code ${res.status}`);
}

// buildServer 单独导出：多个 fixture 实例共用同一个 server 二进制，只 build 一次。
export function buildServer(go, outPath) {
  run(go, ['build', '-o', outPath, './cmd/server'], 'go build');
}

// options:
//   port       监听端口（默认 45439，共享实例）
//   stateFile  实例状态文件路径（teardown 据此关闭对应实例）
//   tempPrefix 临时目录前缀
//   go/ffmpeg  预先解析好的工具路径（缺省时用 findTool 解析）
//   serverBin  预先 build 好的 server 二进制（缺省时 build 到本实例 tempRoot）
export async function setup(options = {}) {
  const port = options.port || 45439;
  const stateFile = options.stateFile || DEFAULT_STATE_FILE;
  const tempPrefix = options.tempPrefix || '032music-e2e-';
  const ffmpeg = options.ffmpeg || findTool('ffmpeg', 'C:\\ffmpeg\\bin\\ffmpeg.exe');
  const go = options.go || findTool('go', 'C:\\Program Files\\Go\\bin\\go.exe');

  // state file 已存在说明上次运行留下了残留实例（异常退出等），先清理再启动。
  if (fs.existsSync(stateFile)) {
    await teardown({ stateFile });
  }

  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), tempPrefix));
  const data = path.join(tempRoot, 'data');
  const music = path.join(tempRoot, 'music');
  fs.mkdirSync(data, { recursive: true });
  fs.mkdirSync(music, { recursive: true });

  // 60-second tracks: long enough that playback never advances to the
  // next queue entry while a test inspects or reorders the queue.
  const fixture = path.join(music, 'fixture-01.mp3');
  run(ffmpeg, ['-loglevel', 'error', '-f', 'lavfi', '-i', 'sine=frequency=440:duration=60',
    '-metadata', 'title=E2E Track 01', '-metadata', 'artist=E2E Artist', '-metadata', 'album=E2E Album',
    '-y', fixture], 'ffmpeg fixture generation');
  for (let i = 2; i <= 20; i++) {
    fs.copyFileSync(fixture, path.join(music, `fixture-${String(i).padStart(2, '0')}.mp3`));
  }

  const exe = options.serverBin || path.join(tempRoot, process.platform === 'win32' ? 'server.exe' : 'server');
  if (!options.serverBin) buildServer(go, exe);

  const logFd = fs.openSync(path.join(tempRoot, 'server.log'), 'a');
  const child = spawn(exe, [], {
    cwd: ROOT,
    stdio: ['ignore', logFd, logFd],
    env: {
      ...process.env,
      MUSIC_SERVER_ADDRESS: `127.0.0.1:${port}`,
      MUSIC_SERVER_DATA_DIR: data,
      MUSIC_SERVER_DATABASE_PATH: path.join(data, 'music.db'),
      MUSIC_SERVER_MUSIC_DIR: music,
      MUSIC_SERVER_LIBRARY_NAME: 'E2E Library',
      MUSIC_SERVER_ADMIN_USERNAME: 'admin',
      MUSIC_SERVER_ADMIN_PASSWORD: 'e2e-admin-password',
      MUSIC_SERVER_API_TOKEN: 'e2e-api-token-00000000000000000000',
      MUSIC_SERVER_MEDIA_TOKEN: 'e2e-media-token-000000000000000000',
      MUSIC_SERVER_COOKIE_SECURE: 'false',
      MUSIC_SERVER_LOG_LEVEL: 'error',
      MUSIC_SERVER_DEV_MODE: 'false'
    }
  });
  fs.closeSync(logFd);
  // 先写状态文件再等健康检查：即使实例没能起来，teardown 也能按状态文件清理。
  fs.writeFileSync(stateFile, JSON.stringify({ pid: child.pid, tempRoot, port }));

  const healthURL = `http://127.0.0.1:${port}/api/v1/health`;
  const deadline = Date.now() + 120_000;
  for (;;) {
    try {
      const res = await fetch(healthURL);
      if (res.ok) {
        // 健康检查通过不代表是我们的进程在应答（端口可能被占用，对方先起来了）。
        // 给一个短宽限：占用场景下我们的 child 会绑定失败并很快退出。
        await new Promise((r) => setTimeout(r, 1200));
        if (child.exitCode !== null) {
          throw new Error(`port ${port} is served by another process (our fixture server exited with code ${child.exitCode})`);
        }
        return;
      }
    } catch (_) { /* server not up yet */ }
    if (child.exitCode !== null) throw new Error(`fixture server (port ${port}) exited early with code ${child.exitCode}; see ${tempRoot}/server.log`);
    if (Date.now() > deadline) throw new Error(`fixture server (port ${port}) did not become healthy in time; see ${tempRoot}/server.log`);
    await new Promise((r) => setTimeout(r, 500));
  }
}

// teardown 只关闭 options.stateFile 对应的实例；状态文件不存在时是无操作，
// 这样 global teardown 可以对每个实例独立调用、互不影响。
export async function teardown(options = {}) {
  const stateFile = options.stateFile || DEFAULT_STATE_FILE;
  let state = null;
  try { state = JSON.parse(fs.readFileSync(stateFile, 'utf8')); } catch (_) { /* nothing to clean */ }
  if (state) {
    const alive = () => { try { process.kill(state.pid, 0); return true; } catch (_) { return false; } };
    if (process.platform === 'win32') {
      // Absolute path: a bare 'taskkill' can fail with ENOENT depending on
      // how the runner's PATH was inherited, which silently left the server
      // (and its locked music.db) running.
      const taskkill = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'taskkill.exe');
      const res = spawnSync(taskkill, ['/pid', String(state.pid), '/T', '/F'], { stdio: 'ignore' });
      if (res.error && alive()) { try { process.kill(state.pid); } catch (_) { /* already gone */ } }
    } else {
      try { process.kill(state.pid, 'SIGTERM'); } catch (_) { /* already gone */ }
    }
    // The server holds music.db open until it has actually exited.
    for (const deadline = Date.now() + 15_000; alive() && Date.now() < deadline;) {
      await new Promise((r) => setTimeout(r, 100));
    }
    // File handles can outlive the process briefly on Windows; retry EBUSY.
    fs.rmSync(state.tempRoot, { recursive: true, force: true, maxRetries: 20, retryDelay: 250 });
    fs.rmSync(stateFile, { force: true });
  }
}
