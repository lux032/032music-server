// End-to-end tests for the playback session reporter wired into the real
// web player. Most tests intercept POST /api/v1/playback/events to capture
// and script responses; one test runs against the real fixture server to
// verify server-side counting. Every test also fails if any request hits
// the removed legacy endpoints (timeline/scrobble).
const { test, expect } = require('@playwright/test');

async function login(page) {
  await page.setViewportSize({ width: 1672, height: 900 });
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await page.waitForURL(/\/admin$/);
  await expect.poll(async () => Number(await page.locator('#tracks-count').textContent())).toBeGreaterThan(0);
  await page.goto('/admin/tracks');
  await expect(page.locator('.row-play-btn').first()).toBeVisible();
}

// Installs request capture for the events endpoint plus a legacy-endpoint
// watchdog. responder(body, events) may return {status, json} to override.
async function capturePlayback(page, responder) {
  const events = [];
  const legacy = [];
  page.on('request', (req) => {
    if (req.url().includes('/api/v1/playback/timeline') || req.url().includes('/api/v1/playback/scrobble')) {
      legacy.push(req.url());
    }
  });
  await page.route('**/api/v1/playback/events', async (route) => {
    const body = JSON.parse(route.request().postData() || '{}');
    events.push(body);
    const override = responder ? responder(body, events) : null;
    await route.fulfill({
      status: (override && override.status) || 200,
      contentType: 'application/json',
      body: JSON.stringify((override && override.json) || {
        applied: true, sessionId: body.sessionId, state: body.state || 'playing',
        positionMillis: body.positionMillis || 0, counted: false,
      }),
    });
  });
  return { events, legacy };
}

async function playFirstRow(page, cap) {
  await page.locator('.row-play-btn').first().click();
  await expect.poll(() => cap.events.filter((e) => e.type === 'start').length).toBe(1);
  await expect.poll(() => page.locator('#global-audio-element').evaluate((el) => el.paused)).toBe(false);
}

const byType = (cap, type) => cap.events.filter((e) => e.type === type);

test('real player: start, pause and resume share one session with monotonic seq', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);

  const start = byType(cap, 'start')[0];
  expect(start.seq).toBe(1);
  expect(start.clientKind).toBe('web');
  expect(start.clientId).toMatch(/^[A-Za-z0-9-]{1,64}$/);
  expect(['playing', 'buffering']).toContain(start.state);
  expect(start.trackId).toBeGreaterThan(0);

  // Only the real 'playing' event may produce the playing state.
  await expect.poll(() => byType(cap, 'resume').length + byType(cap, 'start').filter((e) => e.state === 'playing').length).toBeGreaterThan(0);

  await page.locator('#player-btn-play').click();
  await expect.poll(() => byType(cap, 'pause').length).toBe(1);
  await page.locator('#player-btn-play').click();
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0);

  const ids = new Set(cap.events.map((e) => e.sessionId));
  expect(ids.size).toBe(1);
  const seqs = cap.events.map((e) => e.seq);
  expect(seqs).toEqual([...seqs].sort((a, b) => a - b));
  expect(new Set(seqs).size).toBe(seqs.length);
  expect(cap.legacy).toEqual([]);
});

test('real player: buffering and seek are reported immediately with real positions', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0); // reached playing

  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => el.dispatchEvent(new Event('waiting')));
  await expect.poll(() => byType(cap, 'buffering').length).toBe(1);

  await audio.evaluate((el) => { el.currentTime = 20; });
  await expect.poll(() => byType(cap, 'seek').length).toBeGreaterThan(0);
  const seek = byType(cap, 'seek')[0];
  expect(Math.abs(seek.positionMillis - 20000)).toBeLessThan(1500);
  expect(cap.legacy).toEqual([]);
});

test('real player: next at a low position ends the old track as skipped with its real position', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => { el.currentTime = 5; });
  const firstTrackId = byType(cap, 'start')[0].trackId;

  await page.locator('#player-btn-next').click();
  await expect.poll(() => byType(cap, 'end').length).toBe(1);
  const end = byType(cap, 'end')[0];
  expect(end.trackId).toBe(firstTrackId);
  expect(end.endReason).toBe('skipped');
  expect(end.positionMillis).toBeGreaterThan(3000);
  expect(end.positionMillis).toBeLessThan(30000);
  await expect.poll(() => byType(cap, 'start').length).toBe(2);
  const next = byType(cap, 'start')[1];
  expect(next.trackId).not.toBe(firstTrackId);
  expect(next.sessionId).not.toBe(end.sessionId);
  expect(next.resumedFromSessionId).toBeUndefined();
  // P1-2: the new track's start is position 0 with the NEW track's own duration
  expect(next.positionMillis).toBe(0);
  const queueTrack = await page.evaluate(() => { const st = JSON.parse(sessionStorage.getItem('032_player_state')); return st.queue[st.currentIndex]; });
  expect(next.durationMillis).toBe(queueTrack.durationMs);
  expect(next.durationMillis).toBeGreaterThan(0);
  expect(cap.legacy).toEqual([]);
});

test('real player: removing the current track reports replaced, clearing the queue reports stopped', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);

  await page.locator('#np-queue-list li.current .q-remove').click({ force: true });
  await expect.poll(() => byType(cap, 'end').length).toBe(1);
  expect(byType(cap, 'end')[0].endReason).toBe('replaced');
  await expect.poll(() => byType(cap, 'start').length).toBe(2);

  await page.locator('#np-queue-clear').click();
  await expect.poll(() => byType(cap, 'end').length).toBe(2);
  expect(byType(cap, 'end')[1].endReason).toBe('stopped');
  expect(cap.legacy).toEqual([]);
});

test('real player: single-track loop replay ends completed and starts a brand-new session', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  const first = byType(cap, 'start')[0];

  // Loop mode off -> all -> one.
  await page.locator('#player-btn-loop').click();
  await page.locator('#player-btn-loop').click();
  await expect(page.locator('#player-btn-loop')).toHaveAttribute('title', /单曲循环/);

  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => { el.currentTime = Math.max(0, el.duration - 0.4); });
  await expect.poll(() => byType(cap, 'end').length, { timeout: 15_000 }).toBe(1);
  const end = byType(cap, 'end')[0];
  expect(end.endReason).toBe('completed');
  expect(end.positionMillis).toBeGreaterThan(55000); // real final position, never 0
  await expect.poll(() => byType(cap, 'start').length).toBe(2);
  const replay = byType(cap, 'start')[1];
  expect(replay.trackId).toBe(first.trackId);
  expect(replay.sessionId).not.toBe(first.sessionId);
  expect(replay.resumedFromSessionId).toBeUndefined();
  expect(replay.positionMillis).toBe(0); // loop replay starts at 0 with the track's own duration
  expect(replay.durationMillis).toBe(first.durationMillis);
  expect(replay.durationMillis).toBeGreaterThan(0);
  expect(cap.legacy).toEqual([]);
});

test('real player: page reload resumes the same track through the persisted chain', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  const first = byType(cap, 'start')[0];
  await page.waitForTimeout(300); // let saveState settle

  await page.reload();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  await expect.poll(() => byType(cap, 'start').length).toBe(2);
  const resumed = byType(cap, 'start')[1];
  expect(resumed.trackId).toBe(first.trackId);
  expect(resumed.resumedFromSessionId).toBe(first.sessionId);
  expect(resumed.sessionId).not.toBe(first.sessionId);
  expect(cap.legacy).toEqual([]);
});

test('real player: pagehide ends client_closed and a BFCache return rebuilds the resume chain', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  const first = byType(cap, 'start')[0];

  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true })));
  await expect.poll(() => byType(cap, 'end').length).toBe(1);
  expect(byType(cap, 'end')[0].endReason).toBe('client_closed');

  await page.evaluate(() => window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true })));
  await expect.poll(() => byType(cap, 'start').length).toBe(2);
  const rebuilt = byType(cap, 'start')[1];
  expect(rebuilt.trackId).toBe(first.trackId);
  expect(rebuilt.resumedFromSessionId).toBe(first.sessionId);
  expect(cap.legacy).toEqual([]);
});

test('real player: an audio error ends the session with reason error', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  await page.locator('#global-audio-element').evaluate((el) => el.dispatchEvent(new Event('error')));
  await expect.poll(() => byType(cap, 'end').length).toBe(1);
  expect(byType(cap, 'end')[0].endReason).toBe('error');
  expect(cap.legacy).toEqual([]);
});

test('real player: 409 session_expired triggers exactly one resume start then re-sends the event', async ({ page }) => {
  await login(page);
  let expiredOnce = false;
  const cap = await capturePlayback(page, (body) => {
    if (body.type === 'pause' && !expiredOnce) {
      expiredOnce = true;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return null;
  });
  await playFirstRow(page, cap);
  const first = byType(cap, 'start')[0];

  await page.locator('#player-btn-play').click(); // pause -> 409 -> resume -> pause
  await expect.poll(() => byType(cap, 'start').length).toBe(2);
  const resumed = byType(cap, 'start')[1];
  expect(resumed.resumedFromSessionId).toBe(first.sessionId);
  expect(resumed.trackId).toBe(first.trackId);
  await expect.poll(() => byType(cap, 'pause').length).toBe(2);
  const pauses = byType(cap, 'pause');
  expect(pauses[0].sessionId).toBe(first.sessionId);
  expect(pauses[1].sessionId).toBe(resumed.sessionId);
  expect(cap.legacy).toEqual([]);
});

test('real player: a failed start is retried with the same sessionId and seq', async ({ page }) => {
  await login(page);
  const events = [];
  let sabotage = true;
  await page.route('**/api/v1/playback/events', async (route) => {
    const body = JSON.parse(route.request().postData() || '{}');
    events.push(body);
    if (sabotage && body.type === 'start') {
      sabotage = false;
      await route.abort(); // network-level failure
      return;
    }
    await route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis || 0, counted: false }),
    });
  });
  await page.locator('.row-play-btn').first().click();
  await expect.poll(() => events.filter((e) => e.type === 'start').length, { timeout: 15_000 }).toBe(2);
  const starts = events.filter((e) => e.type === 'start');
  expect(starts[0].sessionId).toBe(starts[1].sessionId);
  expect(starts[0].seq).toBe(1);
  expect(starts[1].seq).toBe(1);
});

test('real player: a played track end hitting 409 recovers with a state playing resume start then the end', async ({ page }) => {
  await login(page);
  let end409 = true;
  const cap = await capturePlayback(page, (body) => {
    if (body.type === 'end' && end409) {
      end409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return null;
  });
  await playFirstRow(page, cap);
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0); // real playing evidence
  const first = byType(cap, 'start')[0];
  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => { el.currentTime = 40; });

  await page.locator('#player-btn-next').click();
  await expect.poll(() => byType(cap, 'start').length).toBe(3);
  const recovery = byType(cap, 'start')[1];
  expect(recovery.trackId).toBe(first.trackId);
  expect(recovery.resumedFromSessionId).toBe(first.sessionId);
  expect(recovery.state).toBe('playing'); // played track: evidence carried into the recovery start
  expect(recovery.positionMillis).toBeGreaterThan(37000);
  expect(recovery.positionMillis).toBeLessThan(45000);
  const ends = byType(cap, 'end');
  expect(ends.length).toBe(2);
  expect(ends[0]).toMatchObject({ sessionId: first.sessionId, endReason: 'skipped' });
  expect(ends[1]).toMatchObject({ sessionId: recovery.sessionId, endReason: 'skipped' });
  expect(ends[1].positionMillis).toBeGreaterThan(37000);
  expect(ends[1].positionMillis).toBeLessThan(45000);
  // the next track starts fresh without resume
  const next = byType(cap, 'start')[2];
  expect(next.trackId).not.toBe(first.trackId);
  expect(next.resumedFromSessionId).toBeUndefined();
  expect(cap.legacy).toEqual([]);
});

test('real player: a reload-resumed paused session recovers with the honest paused state (no fake playing)', async ({ page }) => {
  await login(page);
  let end409 = false; // armed only after the reload
  const cap = await capturePlayback(page, (body) => {
    if (body.type === 'end' && end409) {
      end409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return null;
  });
  await playFirstRow(page, cap);
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0); // played
  const first = byType(cap, 'start')[0];
  await page.locator('#player-btn-play').click(); // pause before reload
  await expect.poll(() => byType(cap, 'pause').length).toBe(1);
  await page.waitForTimeout(300); // let saveState settle

  await page.reload();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  await expect.poll(() => byType(cap, 'start').length).toBe(2);
  const resumed = byType(cap, 'start')[1];
  expect(resumed.trackId).toBe(first.trackId);
  expect(resumed.resumedFromSessionId).toBe(first.sessionId);
  expect(resumed.state).toBe('paused'); // honest restore state, no fake playing
  // No playing event after the reload: the evidence comes from the persisted snapshot.
  expect(byType(cap, 'resume').length).toBe(1);

  end409 = true;
  await page.locator('#player-btn-next').click();
  await expect.poll(() => byType(cap, 'start').length).toBe(4);
  const recovery = byType(cap, 'start')[2];
  expect(recovery.trackId).toBe(first.trackId);
  expect(recovery.resumedFromSessionId).toBe(resumed.sessionId);
  // P1-1: the session ended while paused — hasPlayed history must NOT
  // disguise the recovery start as playing (server priorState rule).
  expect(recovery.state).toBe('paused');
  const ends = byType(cap, 'end');
  expect(ends[ends.length - 1]).toMatchObject({ sessionId: recovery.sessionId, endReason: 'skipped' });
  expect(cap.legacy).toEqual([]);
});

test('real player: rapid track switches serialize one end per replaced track with real positions', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  await page.locator('#player-btn-next').click();
  await page.locator('#player-btn-next').click();
  await expect.poll(() => byType(cap, 'start').length).toBe(3);
  await expect.poll(() => byType(cap, 'end').length).toBe(2);
  const starts = byType(cap, 'start');
  const ends = byType(cap, 'end');
  expect(ends[0]).toMatchObject({ trackId: starts[0].trackId, sessionId: starts[0].sessionId, endReason: 'skipped' });
  expect(ends[1]).toMatchObject({ trackId: starts[1].trackId, sessionId: starts[1].sessionId, endReason: 'skipped' });
  for (const e of ends) {
    expect(e.positionMillis).toBeGreaterThanOrEqual(0);
    expect(e.positionMillis).toBeLessThan(30000); // real position snapshot, never the next track's
  }
  expect(new Set(starts.map((s) => s.sessionId)).size).toBe(3);
  expect(cap.legacy).toEqual([]);
});

test('real player: a resume_invalid block survives reload and never silently restarts', async ({ page }) => {
  await login(page);
  let invalid = false;
  const cap = await capturePlayback(page, (body) => {
    if (invalid && body.type === 'start' && body.resumedFromSessionId) {
      invalid = false;
      return { status: 409, json: { error: { code: 'resume_invalid', message: 'bad predecessor' } } };
    }
    return null;
  });
  await playFirstRow(page, cap);
  const first = byType(cap, 'start')[0];
  await page.waitForTimeout(300); // snapshot persisted via applied responses

  invalid = true;
  await page.reload();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  await expect.poll(() => byType(cap, 'start').length).toBe(2); // the resume attempt
  expect(byType(cap, 'start')[1].resumedFromSessionId).toBe(first.sessionId);
  await page.waitForTimeout(600);
  expect(byType(cap, 'start').length).toBe(2); // no silent fallback fresh start

  // H1: the persisted marker blocks any new start even after another reload.
  await page.reload();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  await page.waitForTimeout(1500);
  expect(byType(cap, 'start').length).toBe(2);
  expect(byType(cap, 'heartbeat').length).toBe(0);
  expect(cap.legacy).toEqual([]);
});

test('real player: replaying the currently audible track is an honest new play from position 0', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0);
  const first = byType(cap, 'start')[0];
  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => { el.currentTime = 30; });
  await page.waitForTimeout(300);

  // Album hero play rebuilds the queue and re-plays the same source while
  // it is audible (H2 path: play() on a playing element fires no events).
  // The full navigation itself resumes the chain on the album page first.
  await page.goto('/admin/albums');
  await page.locator('a[href^="/admin/albums/"]').first().click();
  await expect.poll(() => byType(cap, 'start').length).toBe(2);
  expect(byType(cap, 'start')[1].resumedFromSessionId).toBe(first.sessionId);
  if (await audio.evaluate((el) => el.paused)) await page.locator('#player-btn-play').click();
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  await audio.evaluate((el) => { el.currentTime = 30; });
  await page.waitForTimeout(300);
  await page.locator('.album-hero .primary-round').click();

  await expect.poll(() => byType(cap, 'start').length).toBe(3);
  const replay = byType(cap, 'start')[2];
  expect(replay.trackId).toBe(first.trackId);
  expect(replay.sessionId).not.toBe(first.sessionId);
  expect(replay.resumedFromSessionId).toBeUndefined();
  expect(replay.positionMillis).toBe(0); // P1-2: no old-track leftovers
  expect(replay.durationMillis).toBeGreaterThan(0);
  // the old session ends replaced with its real high position
  const replacedEnd = byType(cap, 'end').find((e) => e.endReason === 'replaced');
  expect(replacedEnd).toBeTruthy();
  expect(replacedEnd.positionMillis).toBeGreaterThan(25000);
  // the new session must reach playing honestly, near position 0 — never
  // stuck in buffering for the whole track
  await expect.poll(() => byType(cap, 'resume').some((e) => e.sessionId === replay.sessionId)).toBe(true);
  const resume = byType(cap, 'resume').find((e) => e.sessionId === replay.sessionId);
  expect(resume.positionMillis).toBeLessThan(3000);
  expect(cap.legacy).toEqual([]);
});

test('real player: stalled while audible is not reported as buffering, waiting still is', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0);
  const audio = page.locator('#global-audio-element');
  const readyState = await audio.evaluate((el) => el.readyState);
  expect(readyState).toBeGreaterThanOrEqual(3); // precondition: enough data to be audible

  await audio.evaluate((el) => el.dispatchEvent(new Event('stalled')));
  await page.waitForTimeout(400);
  expect(byType(cap, 'buffering').length).toBe(0); // network hint only, playback continues

  await audio.evaluate((el) => el.dispatchEvent(new Event('waiting')));
  await expect.poll(() => byType(cap, 'buffering').length).toBe(1); // real buffer underrun
  expect(cap.legacy).toEqual([]);
});

test('real server: reload mid-play counts exactly once across the resume chain (no interception)', async ({ page }) => {
  test.skip(process.env.PLAYWRIGHT_BASE_URL !== undefined, 'fixture server only');
  await login(page);
  const audio = page.locator('#global-audio-element');
  await page.locator('.row-play-btn').first().click();
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  const trackA = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue[0].id);
  const baseA = (await (await page.request.get(`/api/v1/tracks/${trackA}`)).json()).viewCount || 0;

  // Past the 50% count threshold, then reload: the chain resumes.
  await audio.evaluate((el) => { el.currentTime = 35; });
  await page.waitForTimeout(500); // let saveState settle
  await page.reload();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  await page.waitForTimeout(1500); // resumed session settles (play or autoplay-block)
  await page.locator('#player-btn-next').click(); // end the resumed session

  // Exactly +1 across the whole chain — the resume must never double count.
  await expect.poll(async () => (await (await page.request.get(`/api/v1/tracks/${trackA}`)).json()).viewCount, { timeout: 15_000 }).toBe(baseA + 1);
  await page.waitForTimeout(2500);
  expect((await (await page.request.get(`/api/v1/tracks/${trackA}`)).json()).viewCount || 0).toBe(baseA + 1);
});

test('real server: a real resume_invalid blocks the client — no silent fresh start (no interception)', async ({ page }) => {
  test.skip(process.env.PLAYWRIGHT_BASE_URL !== undefined, 'fixture server only');
  await login(page);
  const events = [];
  const statuses = [];
  page.on('request', (req) => {
    if (req.url().includes('/api/v1/playback/events')) {
      try { events.push(JSON.parse(req.postData() || '{}')); } catch (_) {}
    }
  });
  page.on('response', (res) => {
    if (res.url().includes('/api/v1/playback/events')) statuses.push(res.status());
  });
  const audio = page.locator('#global-audio-element');
  await page.locator('.row-play-btn').first().click();
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  await expect.poll(() => events.filter((e) => e.type === 'start').length).toBe(1);
  const first = events.find((e) => e.type === 'start');

  // Complete the track for real: the predecessor ends 'completed'.
  await audio.evaluate((el) => { el.currentTime = Math.max(0, el.duration - 0.3); });
  await expect.poll(() => events.filter((e) => e.type === 'end' && e.endReason === 'completed').length, { timeout: 15_000 }).toBe(1);
  // The queue advanced to track 2; pause it so the saved state is stable.
  await page.locator('#player-btn-play').click();
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(true);

  // Forge a reload whose persisted snapshot points at the COMPLETED
  // session. Must run as an init script: pagehide's saveState/suspend
  // would otherwise clobber the forged sessionStorage on the way out.
  await page.addInitScript(({ sessionId, trackId }) => {
    try {
      sessionStorage.setItem('032_playback_session_v1', JSON.stringify({ sessionId, trackId, hasPlayed: true }));
      const raw = sessionStorage.getItem('032_player_state');
      if (!raw) return;
      const state = JSON.parse(raw);
      const index = state.queue.findIndex((t) => Number(t.id) === Number(trackId));
      if (index < 0) return;
      state.currentIndex = index;
      state.currentTime = 55;
      state.isPlaying = false;
      sessionStorage.setItem('032_player_state', JSON.stringify(state));
    } catch (_) {}
  }, { sessionId: first.sessionId, trackId: first.trackId });

  const startsBefore = events.filter((e) => e.type === 'start').length;
  await page.reload();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  // the resume start goes out and the REAL server rejects it...
  await expect.poll(() => events.filter((e) => e.type === 'start').length).toBe(startsBefore + 1);
  await expect.poll(() => statuses.includes(409)).toBe(true);
  const resumed = events.filter((e) => e.type === 'start').pop();
  expect(resumed.resumedFromSessionId).toBe(first.sessionId);
  // ...and the client must NOT silently follow up with a fresh start.
  await page.waitForTimeout(2500);
  expect(events.filter((e) => e.type === 'start').length).toBe(startsBefore + 1);
  expect(events.filter((e) => e.type === 'heartbeat').every((e) => e.sessionId !== resumed.sessionId)).toBe(true);
});

test('real player: natural auto-advance starts the next track at position 0 with its own duration', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  const first = byType(cap, 'start')[0];
  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => { el.currentTime = Math.max(0, el.duration - 0.4); });
  await expect.poll(() => byType(cap, 'start').length, { timeout: 15_000 }).toBe(2);
  const next = byType(cap, 'start')[1];
  expect(next.trackId).not.toBe(first.trackId);
  expect(next.resumedFromSessionId).toBeUndefined();
  // P1-2: auto-advance too — position 0, the NEW track's own duration
  expect(next.positionMillis).toBe(0);
  const track2 = await page.evaluate(() => { const st = JSON.parse(sessionStorage.getItem('032_player_state')); return st.queue[st.currentIndex]; });
  expect(next.durationMillis).toBe(track2.durationMs);
  expect(next.durationMillis).toBeGreaterThan(0);
  const end = byType(cap, 'end')[0];
  expect(end.endReason).toBe('completed');
  expect(end.positionMillis).toBeGreaterThan(55000); // real final position of the old track
  expect(cap.legacy).toEqual([]);
});

test('real player: replaying the same source while paused also restarts from 0 (P2-1)', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page);
  await playFirstRow(page, cap);
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0);
  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => { el.currentTime = 30; });
  await page.locator('#player-btn-play').click(); // pause at ~30s
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(true);

  await page.goto('/admin/albums');
  await page.locator('a[href^="/admin/albums/"]').first().click();
  await expect.poll(() => byType(cap, 'start').length).toBe(2); // chain resume after navigation
  if (!(await audio.evaluate((el) => el.paused))) {
    await page.locator('#player-btn-play').click(); // ensure paused before the replay
    await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(true);
  }
  await page.locator('.album-hero .primary-round').click();
  await expect.poll(() => byType(cap, 'start').length).toBe(3);
  const replay = byType(cap, 'start')[2];
  expect(replay.resumedFromSessionId).toBeUndefined();
  expect(replay.positionMillis).toBe(0);
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  expect(await audio.evaluate((el) => el.currentTime)).toBeLessThan(3); // restarted from 0, not 30s
  expect(cap.legacy).toEqual([]);
});

test('real server: paused seek to 80% then expired recovery never counts (real counting, one scripted 409)', async ({ page }) => {
  test.skip(process.env.PLAYWRIGHT_BASE_URL !== undefined, 'fixture server only');
  await login(page);
  const events = [];
  let forceExpired = true;
  await page.route('**/api/v1/playback/events', async (route) => {
    const body = JSON.parse(route.request().postData() || '{}');
    events.push(body);
    if (forceExpired && body.type === 'seek') {
      forceExpired = false;
      // Only the trigger is scripted: every other event (including the
      // recovery start and the final end) goes to the REAL fixture server,
      // so counting is real.
      await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: { code: 'session_expired', message: 'expired' } }) });
      return;
    }
    await route.continue();
  });
  const audio = page.locator('#global-audio-element');
  await page.locator('.row-play-btn').first().click();
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  const trackA = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue[0].id);
  const base = await (await page.request.get(`/api/v1/tracks/${trackA}`)).json();
  const baseView = base.viewCount || 0;
  const baseSkip = base.skipCount || 0;

  // Really play a bit, pause, then drag to 80% while paused.
  await page.waitForTimeout(1200);
  await page.locator('#player-btn-play').click();
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(true);
  await audio.evaluate((el) => { el.currentTime = el.duration * 0.8; });

  // the seeked event hits the scripted 409 -> the recovery start must be honest
  await expect.poll(() => events.filter((e) => e.type === 'start').length).toBe(2);
  const starts = events.filter((e) => e.type === 'start');
  const recovery = starts[1];
  expect(recovery.resumedFromSessionId).toBe(starts[0].sessionId);
  expect(recovery.state).toBe('paused'); // never disguised as playing
  expect(recovery.positionMillis).toBeGreaterThan(40000);

  // End the chain for real: skip to the next track.
  await page.locator('#player-btn-next').click();
  await expect.poll(() => events.filter((e) => e.type === 'end' && e.sessionId === recovery.sessionId).length, { timeout: 15_000 }).toBe(1);
  await page.waitForTimeout(1500);
  const after = await (await page.request.get(`/api/v1/tracks/${trackA}`)).json();
  expect(after.viewCount || 0).toBe(baseView); // a paused-seek play never counts
  expect(after.skipCount || 0).toBe(baseSkip); // 80% is not a skip either
});

test('real player: natural completion keeps playing evidence — a completed end hitting 409 recovers with state playing', async ({ page }) => {
  await login(page);
  let end409 = true;
  const cap = await capturePlayback(page, (body) => {
    if (body.type === 'end' && end409) {
      end409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return null;
  });
  await playFirstRow(page, cap);
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0);
  const first = byType(cap, 'start')[0];
  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => { el.currentTime = Math.max(0, el.duration - 1); });

  // Natural completion: the browser fires pause then ended in one task. The
  // automatic pause must NOT be reported (P1) — the end freezes playing.
  // Poll the settled chain: starts==3 implies the serial queue delivered
  // end -> recovery start -> recovery end -> next-track start.
  await expect.poll(() => byType(cap, 'start').length, { timeout: 15_000 }).toBe(3);
  const ends = byType(cap, 'end');
  expect(ends.length).toBe(2);
  expect(ends[0]).toMatchObject({ sessionId: first.sessionId, endReason: 'completed' });
  expect(byType(cap, 'pause').length).toBe(0);

  const recovery = byType(cap, 'start')[1];
  expect(recovery.trackId).toBe(first.trackId);
  expect(recovery.resumedFromSessionId).toBe(first.sessionId);
  expect(recovery.state).toBe('playing');
  expect(recovery.positionMillis).toBeGreaterThan(55000); // real final position
  expect(ends[1]).toMatchObject({ sessionId: recovery.sessionId, endReason: 'completed' });
  expect(ends[1].positionMillis).toBeGreaterThan(55000);
  expect(byType(cap, 'start')[2].resumedFromSessionId).toBeUndefined(); // next track fresh
  expect(cap.legacy).toEqual([]);
});

test('real player: a natural completion end hitting 404 restores start(playing)+end at the real final position', async ({ page }) => {
  await login(page);
  const cap = await capturePlayback(page, (body) => {
    if (body.type === 'end') {
      return { status: 404, json: { error: { code: 'session_not_found', message: 'cleared' } } };
    }
    return null;
  });
  await playFirstRow(page, cap);
  await expect.poll(() => byType(cap, 'resume').length).toBeGreaterThan(0);
  const first = byType(cap, 'start')[0];
  const audio = page.locator('#global-audio-element');
  await audio.evaluate((el) => { el.currentTime = Math.max(0, el.duration - 1); });

  await expect.poll(() => byType(cap, 'start').length, { timeout: 15_000 }).toBe(3); // restore + next track
  expect(byType(cap, 'pause').length).toBe(0);
  const restore = byType(cap, 'start')[1];
  expect(restore.trackId).toBe(first.trackId);
  expect(restore.resumedFromSessionId).toBeUndefined(); // 404: fresh session, no resume
  expect(restore.state).toBe('playing');
  expect(restore.positionMillis).toBeGreaterThan(55000);
  const ends = byType(cap, 'end');
  expect(ends.length).toBe(2);
  expect(ends[1]).toMatchObject({ sessionId: restore.sessionId, endReason: 'completed' });
  expect(ends[1].positionMillis).toBeGreaterThan(55000);
  expect(cap.legacy).toEqual([]);
});

test('real player: an explicit play out of a resume_invalid block restarts from 0 with the track duration', async ({ page }) => {
  await login(page);
  let invalid = false;
  const cap = await capturePlayback(page, (body) => {
    if (invalid && body.type === 'start' && body.resumedFromSessionId) {
      invalid = false;
      return { status: 409, json: { error: { code: 'resume_invalid', message: 'bad predecessor' } } };
    }
    return null;
  });
  await playFirstRow(page, cap);
  await page.waitForTimeout(300);

  invalid = true;
  await page.reload();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  await expect.poll(() => byType(cap, 'start').length).toBe(2); // resume attempt -> 409 -> blocked
  const audio = page.locator('#global-audio-element');
  if (!(await audio.evaluate((el) => el.paused))) {
    await page.locator('#player-btn-play').click(); // ensure paused before the explicit play
    await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(true);
  }
  await audio.evaluate((el) => { el.currentTime = 20; }); // high position while blocked

  await page.locator('#player-btn-play').click(); // explicit new play out of the block
  await expect.poll(() => byType(cap, 'start').length).toBe(3);
  const fresh = byType(cap, 'start')[2];
  expect(fresh.resumedFromSessionId).toBeUndefined();
  expect(fresh.positionMillis).toBe(0); // real new play, not a high-position read
  const queueTrack = await page.evaluate(() => { const st = JSON.parse(sessionStorage.getItem('032_player_state')); return st.queue[st.currentIndex]; });
  expect(fresh.durationMillis).toBe(queueTrack.durationMs);
  expect(fresh.durationMillis).toBeGreaterThan(0);
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  expect(await audio.evaluate((el) => el.currentTime)).toBeLessThan(3);
  expect(cap.legacy).toEqual([]);
});

test('real server: natural completion with one scripted 409 counts exactly once (real counting)', async ({ page }) => {
  test.skip(process.env.PLAYWRIGHT_BASE_URL !== undefined, 'fixture server only');
  await login(page);
  const events = [];
  let forceExpired = true;
  await page.route('**/api/v1/playback/events', async (route) => {
    const body = JSON.parse(route.request().postData() || '{}');
    events.push(body);
    if (forceExpired && body.type === 'end' && body.endReason === 'completed') {
      forceExpired = false;
      // Only the trigger is scripted; the recovery start+end and every other
      // event go to the REAL fixture server, so counting is real.
      await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: { code: 'session_expired', message: 'expired' } }) });
      return;
    }
    await route.continue();
  });
  const audio = page.locator('#global-audio-element');
  await page.locator('.row-play-btn').first().click();
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  const trackA = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue[0].id);
  const baseView = (await (await page.request.get(`/api/v1/tracks/${trackA}`)).json()).viewCount || 0;

  await audio.evaluate((el) => { el.currentTime = Math.max(0, el.duration - 1); }); // natural completion
  await expect.poll(() => events.filter((e) => e.type === 'start').length, { timeout: 15_000 }).toBe(3); // orig + recovery + next track
  const starts = events.filter((e) => e.type === 'start');
  const recovery = starts[1];
  expect(recovery.resumedFromSessionId).toBe(starts[0].sessionId);
  expect(recovery.state).toBe('playing'); // P1: natural end keeps playing evidence
  expect(recovery.positionMillis).toBeGreaterThan(55000);
  expect(events.filter((e) => e.type === 'pause').length).toBe(0);

  await expect.poll(async () => (await (await page.request.get(`/api/v1/tracks/${trackA}`)).json()).viewCount, { timeout: 15_000 }).toBe(baseView + 1);
  await page.waitForTimeout(2500);
  expect((await (await page.request.get(`/api/v1/tracks/${trackA}`)).json()).viewCount || 0).toBe(baseView + 1); // never +2
});

test('real server: counting, skip threshold and legacy 410 (no interception)', async ({ page }) => {
  test.skip(process.env.PLAYWRIGHT_BASE_URL !== undefined, 'fixture server only');
  await login(page);
  // Legacy endpoints are gone server-side.
  for (const url of ['/api/v1/playback/timeline', '/api/v1/playback/scrobble']) {
    const res = await page.request.post(url, { data: {} });
    expect(res.status()).toBe(410);
  }

  const audio = page.locator('#global-audio-element');
  await page.locator('.row-play-btn').first().click();
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  const trackA = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue[0].id);
  // The fixture database is shared across projects, so assert increments
  // from a baseline rather than absolute counts.
  const baseA = (await (await page.request.get(`/api/v1/tracks/${trackA}`)).json()).viewCount || 0;

  // Past the 50% count threshold, then next: a real play count, no skip.
  await audio.evaluate((el) => { el.currentTime = 35; });
  await page.locator('#player-btn-next').click();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/tracks/${trackA}`)).json()).viewCount, { timeout: 15_000 }).toBe(baseA + 1);

  // Below the skip threshold on the next track: a skip, no play count.
  const trackB = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue[JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex].id);
  const baseB = (await (await page.request.get(`/api/v1/tracks/${trackB}`)).json()).skipCount || 0;
  await audio.evaluate((el) => { el.currentTime = 5; });
  await page.locator('#player-btn-next').click();
  await expect.poll(async () => (await (await page.request.get(`/api/v1/tracks/${trackB}`)).json()).skipCount, { timeout: 15_000 }).toBe(baseB + 1);
  const b = await (await page.request.get(`/api/v1/tracks/${trackB}`)).json();
  expect(b.viewCount || 0).toBe(0);
});
