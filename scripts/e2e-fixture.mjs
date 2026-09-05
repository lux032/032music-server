// Isolated E2E fixture server lifecycle for Playwright globalSetup/globalTeardown.
// Uses only synthetic audio in a fresh temp dir; never touches the real library.
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const ROOT = path.resolve(import.meta.dirname, '..');
const STATE_FILE = path.join(os.tmpdir(), '032music-e2e-state.json');
const HEALTH_URL = 'http://127.0.0.1:45439/api/v1/health';

function findTool(name, fallback) {
  const probe = spawnSync('where', [name], { shell: true, encoding: 'utf8' });
  const found = probe.status === 0 ? probe.stdout.split(/\r?\n/)[0].trim() : '';
  if (found && fs.existsSync(found)) return found;
  if (fallback && fs.existsSync(fallback)) return fallback;
  throw new Error(`required tool not found: ${name}`);
}

function run(cmd, args, label) {
  const res = spawnSync(cmd, args, { cwd: ROOT, stdio: 'inherit' });
  if (res.status !== 0) throw new Error(`${label} failed with exit code ${res.status}`);
}

export async function setup() {
  const ffmpeg = findTool('ffmpeg', 'C:\\ffmpeg\\bin\\ffmpeg.exe');
  const go = findTool('go', 'C:\\Program Files\\Go\\bin\\go.exe');

  const tempRoot = fs.mkdtempSync(path.join(os.tmpdir(), '032music-e2e-'));
  const data = path.join(tempRoot, 'data');
  const music = path.join(tempRoot, 'music');
  fs.mkdirSync(data, { recursive: true });
  fs.mkdirSync(music, { recursive: true });

  const fixture = path.join(music, 'fixture-01.mp3');
  run(ffmpeg, ['-loglevel', 'error', '-f', 'lavfi', '-i', 'sine=frequency=440:duration=2',
    '-metadata', 'title=E2E Track 01', '-metadata', 'artist=E2E Artist', '-metadata', 'album=E2E Album',
    '-y', fixture], 'ffmpeg fixture generation');
  for (let i = 2; i <= 20; i++) {
    fs.copyFileSync(fixture, path.join(music, `fixture-${String(i).padStart(2, '0')}.mp3`));
  }

  const exe = path.join(tempRoot, process.platform === 'win32' ? 'server.exe' : 'server');
  run(go, ['build', '-o', exe, './cmd/server'], 'go build');

  const logFd = fs.openSync(path.join(tempRoot, 'server.log'), 'a');
  const child = spawn(exe, [], {
    cwd: ROOT,
    stdio: ['ignore', logFd, logFd],
    env: {
      ...process.env,
      MUSIC_SERVER_ADDRESS: '127.0.0.1:45439',
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
  fs.writeFileSync(STATE_FILE, JSON.stringify({ pid: child.pid, tempRoot }));

  const deadline = Date.now() + 120_000;
  for (;;) {
    try {
      const res = await fetch(HEALTH_URL);
      if (res.ok) return;
    } catch (_) { /* server not up yet */ }
    if (child.exitCode !== null) throw new Error(`fixture server exited early with code ${child.exitCode}; see ${tempRoot}/server.log`);
    if (Date.now() > deadline) throw new Error(`fixture server did not become healthy in time; see ${tempRoot}/server.log`);
    await new Promise((r) => setTimeout(r, 500));
  }
}

export async function teardown() {
  let state = null;
  try { state = JSON.parse(fs.readFileSync(STATE_FILE, 'utf8')); } catch (_) { /* nothing to clean */ }
  if (state) {
    if (process.platform === 'win32') {
      spawnSync('taskkill', ['/pid', String(state.pid), '/T', '/F'], { stdio: 'ignore' });
    } else {
      try { process.kill(state.pid, 'SIGTERM'); } catch (_) { /* already gone */ }
    }
    fs.rmSync(state.tempRoot, { recursive: true, force: true });
    fs.rmSync(STATE_FILE, { force: true });
  }
}
