// Unit tests for the event-driven playback session reporter.
// The reporter is a pure-logic ES module with injected transport/clock/
// timers/storage/uuid, so these run in Node without a browser or network.
const { test, expect } = require('@playwright/test');
const fs = require('fs');
const os = require('os');
const path = require('path');

let createPlaybackReporter;

test.beforeAll(() => {
  // The reporter is a pure-logic ES module; Playwright transpiles specs to
  // CJS, so load it through a tiny CommonJS shim (no DOM involved).
  const src = fs.readFileSync(path.resolve(__dirname, '../../internal/http/assets/playback-reporter.js'), 'utf8');
  const shim = src.replace(/export function createPlaybackReporter/, 'function createPlaybackReporter')
    + '\nmodule.exports = { createPlaybackReporter };\n';
  const shimPath = path.join(os.tmpdir(), `032-playback-reporter-${process.pid}.cjs`);
  fs.writeFileSync(shimPath, shim);
  ({ createPlaybackReporter } = require(shimPath));
});

function memoryStorage() {
  const map = new Map();
  return {
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => map.set(k, String(v)),
    removeItem: (k) => map.delete(k),
    _map: map,
  };
}

function makeHarness(options = {}) {
  const sent = [];
  const errors = [];
  const timers = [];
  let position = 0;
  let duration = 60000;
  let uuidCounter = 0;
  let gate = null; // when set, transport waits for this promise first
  let responder = options.responder || ((body) => ({
    status: 200,
    json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false },
  }));

  const local = memoryStorage();
  const sessionStore = memoryStorage();
  local.setItem('032_playback_client_id', 'client-test-1'); // keep uuid allocation deterministic for sessions
  if (options.snapshot) sessionStore.setItem('032_playback_session_v1', JSON.stringify(options.snapshot));
  if (options.blockMarker) sessionStore.setItem('032_playback_block_v1', JSON.stringify(options.blockMarker));

  const reporter = createPlaybackReporter({
    transport: async (body, opts) => {
      const entry = { body: { ...body }, keepalive: !!(opts && opts.keepalive) };
      sent.push(entry); // call order == send order; keepalive bypasses the gate
      if (gate && !entry.keepalive) await gate;
      return responder(body, opts);
    },
    getPositionMillis: () => position,
    getDurationMillis: () => duration,
    setTimeout: (fn, ms) => { const t = { fn, ms, cancelled: false }; timers.push(t); return t; },
    clearTimeout: (t) => { t.cancelled = true; },
    uuid: () => `sid-${++uuidCounter}`,
    localStorage: local,
    sessionStorage: sessionStore,
    retryDelaysMillis: [5, 10],
    onProtocolError: (e) => errors.push(e),
  });

  return {
    reporter, sent, errors, timers, local, sessionStore,
    setPosition(v) { position = v; },
    setDuration(v) { duration = v; },
    setResponder(r) { responder = r; },
    hold() { let release; gate = new Promise((r) => { release = r; }); return () => { gate = null; release(); }; },
    async flush(rounds = 60) { for (let i = 0; i < rounds; i++) await Promise.resolve(); },
    fireTimers(pred) {
      const due = timers.splice(0);
      for (const t of due) {
        if (t.cancelled) continue;
        if (pred && !pred(t)) { timers.push(t); continue; }
        t.fn();
      }
    },
    byType(type) { return sent.filter((s) => s.body.type === type).map((s) => s.body); },
  };
}

test('start precedes heartbeat, seq strictly monotonic, clientId persisted', async () => {
  const h = makeHarness();
  h.reporter.play(12);
  await h.flush();
  expect(h.sent.length).toBe(1);
  const start = h.sent[0].body;
  expect(start).toMatchObject({ type: 'start', seq: 1, sessionId: 'sid-1', trackId: 12, clientKind: 'web', state: 'buffering' });
  expect(start.clientId).toBe('client-test-1');
  expect(h.local.getItem('032_playback_client_id')).toBe(start.clientId);
  expect(start.resumedFromSessionId).toBeUndefined();

  h.fireTimers();
  await h.flush();
  const hb = h.byType('heartbeat');
  expect(hb.length).toBe(1);
  expect(hb[0].seq).toBe(2);
  expect(hb[0].state).toBe('buffering');

  h.reporter.notifyPlaying();
  h.reporter.notifyPaused();
  await h.flush();
  const seqs = h.sent.map((s) => s.body.seq);
  expect(seqs).toEqual([...seqs].sort((a, b) => a - b));
  expect(new Set(seqs).size).toBe(seqs.length);
  expect(h.byType('resume')[0].seq).toBeGreaterThan(2);
  expect(h.byType('pause')[0].seq).toBeGreaterThan(h.byType('resume')[0].seq);
});

test('heartbeat is never sent before the session start lands', async () => {
  const h = makeHarness();
  const release = h.hold();
  h.reporter.play(12);
  await h.flush();
  h.fireTimers(); // heartbeat tick while start is still in flight
  await h.flush();
  expect(h.sent.length).toBe(1);
  expect(h.sent[0].body.type).toBe('start');
  release();
  await h.flush();
  expect(h.sent[0].body.type).toBe('start');
  // At most one coalesced heartbeat follows; no backlog burst.
  expect(h.byType('heartbeat').length).toBeLessThanOrEqual(1);
});

test('start is retried with the same sessionId and seq after network failures', async () => {
  const h = makeHarness();
  let calls = 0;
  h.setResponder((body) => {
    calls++;
    if (body.type === 'start' && calls <= 2) throw new Error('network down');
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(12);
  await h.flush();      // attempt 1 fails, sleep timer pending
  h.fireTimers((t) => t.ms <= 10);  // backoff 1 (leave the 15s heartbeat timer alone)
  await h.flush();      // attempt 2 fails
  h.fireTimers((t) => t.ms <= 10);  // backoff 2
  await h.flush();      // attempt 3 succeeds
  const starts = h.byType('start');
  expect(starts.length).toBe(3);
  for (const s of starts) {
    expect(s.sessionId).toBe('sid-1');
    expect(s.seq).toBe(1);
  }
  h.reporter.notifyPlaying();
  await h.flush();
  expect(h.byType('resume')[0].seq).toBe(2);
});

test('heartbeat cadence is 15s active and 60s paused', async () => {
  const h = makeHarness();
  h.reporter.play(12);
  await h.flush();
  expect(h.timers[h.timers.length - 1].ms).toBe(15000);
  h.reporter.notifyPaused();
  await h.flush();
  expect(h.timers[h.timers.length - 1].ms).toBe(60000);
  h.reporter.notifyPlaying();
  await h.flush();
  expect(h.timers[h.timers.length - 1].ms).toBe(15000);
});

test('heartbeat ticks coalesce while the queue is stuck (no unbounded backlog)', async () => {
  const h = makeHarness();
  const release = h.hold();
  h.reporter.play(12);
  await h.flush();
  for (let i = 0; i < 5; i++) { h.fireTimers(); await h.flush(); }
  expect(h.sent.length).toBe(1); // still just the held start
  release();
  await h.flush();
  const types = h.sent.map((s) => s.body.type);
  expect(types[0]).toBe('start');
  expect(types.filter((t) => t === 'heartbeat').length).toBeLessThanOrEqual(1);
});

test('track switch ends the old track with its real position before the new start', async () => {
  const h = makeHarness();
  h.reporter.play(1);
  await h.flush();
  h.setPosition(7000);
  h.reporter.play(2, { reason: 'skipped' });
  h.setPosition(99999); // must not leak into the already-enqueued end
  await h.flush();
  const end = h.byType('end')[0];
  expect(end).toMatchObject({ trackId: 1, endReason: 'skipped', positionMillis: 7000, sessionId: 'sid-1', seq: 2 });
  const starts = h.byType('start');
  expect(starts[1]).toMatchObject({ trackId: 2, sessionId: 'sid-2', seq: 1 });
  expect(h.sent.map((s) => s.body.type)).toEqual(['start', 'end', 'start']);
});

test('complete reports the real final position, never 0', async () => {
  const h = makeHarness();
  h.reporter.play(1);
  await h.flush();
  h.setPosition(59800);
  h.reporter.complete();
  await h.flush();
  const end = h.byType('end')[0];
  expect(end).toMatchObject({ endReason: 'completed', positionMillis: 59800 });
  // terminal: no further heartbeats
  h.fireTimers();
  await h.flush();
  expect(h.byType('heartbeat').length).toBe(0);
});

test('409 session_expired: exactly one serial resume start, then the current event is re-sent', async () => {
  const h = makeHarness();
  h.setResponder((body) => {
    if (body.type === 'pause' && body.sessionId === 'sid-1') {
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(7);
  h.reporter.notifyPlaying();
  await h.flush();
  h.setPosition(12345);
  h.reporter.notifyPaused();
  await h.flush();
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  // The session was PAUSED when the pause hit the 409: the recovery start
  // carries the honest current state (server priorState rule) — hasPlayed
  // history never disguises a paused session as playing (P1-1).
  expect(starts[1]).toMatchObject({ sessionId: 'sid-2', resumedFromSessionId: 'sid-1', trackId: 7, state: 'paused', positionMillis: 12345 });
  // the pause event is re-sent against the recovery session with a fresh seq
  const pauses = h.byType('pause');
  expect(pauses.length).toBe(2);
  expect(pauses[1]).toMatchObject({ sessionId: 'sid-2', seq: 2 });
  // subsequent events continue on the recovery session
  h.reporter.notifyPlaying();
  await h.flush();
  const resumes = h.byType('resume');
  expect(resumes[resumes.length - 1].sessionId).toBe('sid-2');
  // persisted snapshot now points at the recovery session
  expect(JSON.parse(h.sessionStore.getItem('032_playback_session_v1'))).toMatchObject({ sessionId: 'sid-2', trackId: 7 });
});

test('409 session_expired on end: resume start at the final position, then end with the original reason', async () => {
  const h = makeHarness();
  let end409 = true;
  h.setResponder((body) => {
    if (body.type === 'end' && end409) {
      end409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(7);
  h.reporter.notifyPlaying();
  await h.flush();
  h.setPosition(30000);
  h.reporter.stopSession('stopped');
  await h.flush();
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  // Played track: the recovery start uses state playing at the final position.
  expect(starts[1]).toMatchObject({ sessionId: 'sid-2', resumedFromSessionId: 'sid-1', state: 'playing', positionMillis: 30000 });
  const ends = h.byType('end');
  expect(ends.length).toBe(2);
  expect(ends[1]).toMatchObject({ sessionId: 'sid-2', endReason: 'stopped', positionMillis: 30000 });
  // L1: an end recovery must not become the active session nor be persisted
  // as resumable; the snapshot still points at the old (stopped, resumable)
  // session, never at the about-to-end recovery session.
  expect(h.reporter.hasActiveSession()).toBe(false);
  expect(h.reporter.getDebug().sessionId).not.toBe('sid-2');
  const snap = JSON.parse(h.sessionStore.getItem('032_playback_session_v1'));
  expect(snap.sessionId).toBe('sid-1');
});

test('404 session_not_found on heartbeat: rebuild with a fresh start (same id, no resume)', async () => {
  const h = makeHarness();
  let missing = true;
  h.setResponder((body) => {
    if (body.type === 'heartbeat' && missing) {
      missing = false;
      return { status: 404, json: { error: { code: 'session_not_found', message: 'unknown' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(7);
  await h.flush();
  h.fireTimers();
  await h.flush();
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  expect(starts[1].sessionId).toBe('sid-1');
  expect(starts[1].resumedFromSessionId).toBeUndefined();
  expect(starts[1].seq).toBe(3);
});

test('404 session_not_found on end: played past threshold restores start+end, anything else is discarded', async () => {
  // played AND past threshold: restored with state playing at the final position
  const h1 = makeHarness();
  h1.setResponder((body) => {
    if (body.type === 'end') return { status: 404, json: { error: { code: 'session_not_found', message: 'cleared' } } };
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h1.reporter.play(7);
  h1.reporter.notifyPlaying(); // real play evidence
  await h1.flush();
  h1.setPosition(40000); // >= 30000 of 60000
  h1.reporter.stopSession('stopped');
  await h1.flush();
  const starts = h1.byType('start');
  expect(starts.length).toBe(2);
  expect(starts[1]).toMatchObject({ sessionId: 'sid-2', state: 'playing', positionMillis: 40000 });
  expect(starts[1].resumedFromSessionId).toBeUndefined();
  expect(h1.byType('end')[1]).toMatchObject({ sessionId: 'sid-2', endReason: 'stopped', positionMillis: 40000 });

  // never played, past threshold: discarded — a paused high-position session
  // must never be disguised as playing
  const h4 = makeHarness();
  h4.setResponder((body) => {
    if (body.type === 'end') return { status: 404, json: { error: { code: 'session_not_found', message: 'cleared' } } };
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h4.reporter.play(7);
  await h4.flush();
  h4.setPosition(40000); // e.g. seek while paused, never a playing event
  h4.reporter.stopSession('stopped');
  await h4.flush();
  expect(h4.byType('start').length).toBe(1);
  expect(h4.byType('end').length).toBe(1);
  expect(h4.sent.every((s) => s.body.state !== 'playing')).toBe(true);

  // played but below threshold: discarded
  const h2 = makeHarness();
  h2.setResponder((body) => {
    if (body.type === 'end') return { status: 404, json: { error: { code: 'session_not_found', message: 'cleared' } } };
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h2.reporter.play(7);
  h2.reporter.notifyPlaying();
  await h2.flush();
  h2.setPosition(10000);
  h2.reporter.stopSession('stopped');
  await h2.flush();
  expect(h2.byType('start').length).toBe(1);
  expect(h2.byType('end').length).toBe(1);

  // played but unknown duration: never past threshold
  const h3 = makeHarness();
  h3.setDuration(0);
  h3.setResponder((body) => {
    if (body.type === 'end') return { status: 404, json: { error: { code: 'session_not_found', message: 'cleared' } } };
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h3.reporter.play(7);
  h3.reporter.notifyPlaying();
  await h3.flush();
  h3.setPosition(40000);
  h3.reporter.stopSession('stopped');
  await h3.flush();
  expect(h3.byType('start').length).toBe(1);
});

test('409 resume_invalid: blocks reporting, clears the snapshot, never silently restarts', async () => {
  const h = makeHarness({ snapshot: { sessionId: 'old-1', trackId: 5 } });
  h.setResponder((body) => {
    if (body.type === 'start' && body.resumedFromSessionId) {
      return { status: 409, json: { error: { code: 'resume_invalid', message: 'bad predecessor' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.restore(5, { initialState: 'paused', positionMillis: 40000 });
  await h.flush();
  expect(h.byType('start').length).toBe(1);
  expect(h.byType('start')[0].resumedFromSessionId).toBe('old-1');
  expect(h.reporter.isBlocked()).toBe(true);
  expect(h.errors.map((e) => e.reason)).toContain('resume_invalid');
  expect(h.sessionStore.getItem('032_playback_session_v1')).toBeNull();
  // H1: the block is persisted so a refresh of the same track stays blocked
  expect(JSON.parse(h.sessionStore.getItem('032_playback_block_v1'))).toMatchObject({ blocked: true, trackId: 5 });
  // no silent fallback new start, no heartbeats
  h.reporter.notifyPlaying();
  h.fireTimers();
  await h.flush();
  expect(h.byType('start').length).toBe(1);
  expect(h.byType('heartbeat').length).toBe(0);
  // restore stays blocked (the persisted marker matches the track)
  h.reporter.restore(5, { initialState: 'playing', positionMillis: 41000 });
  await h.flush();
  expect(h.byType('start').length).toBe(1);
  // an explicit new play is the only way out
  h.reporter.play(5);
  await h.flush();
  expect(h.reporter.isBlocked()).toBe(false);
  expect(h.sessionStore.getItem('032_playback_block_v1')).toBeNull();
  expect(h.byType('start').length).toBe(2);
  expect(h.byType('start')[1].resumedFromSessionId).toBeUndefined();
});

test('resume_invalid block survives a simulated refresh via the persisted marker', async () => {
  const h1 = makeHarness({ snapshot: { sessionId: 'old-1', trackId: 5, hasPlayed: true } });
  h1.setResponder((body) => {
    if (body.type === 'start' && body.resumedFromSessionId) {
      return { status: 409, json: { error: { code: 'resume_invalid', message: 'bad predecessor' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h1.reporter.restore(5, { initialState: 'paused', positionMillis: 40000 });
  await h1.flush();
  const marker = JSON.parse(h1.sessionStore.getItem('032_playback_block_v1'));
  expect(marker).toMatchObject({ blocked: true, trackId: 5 });

  // Simulated refresh: a brand-new reporter with the persisted marker must
  // NOT silently open a new high-position chain for the same track.
  const h2 = makeHarness({ blockMarker: marker });
  h2.reporter.restore(5, { initialState: 'paused', positionMillis: 41000 });
  await h2.flush();
  expect(h2.byType('start').length).toBe(0);
  expect(h2.reporter.isBlocked()).toBe(true);

  // A different track drops the stale marker and restores normally.
  h2.reporter.restore(9, { initialState: 'paused', positionMillis: 1000 });
  await h2.flush();
  expect(h2.byType('start').length).toBe(1);
  expect(h2.sessionStore.getItem('032_playback_block_v1')).toBeNull();

  // Only an explicit play on the blocked track lifts the persisted block.
  const h3 = makeHarness({ blockMarker: { blocked: true, trackId: 5 } });
  h3.reporter.restore(5, { initialState: 'paused', positionMillis: 40000 });
  await h3.flush();
  expect(h3.byType('start').length).toBe(0);
  h3.reporter.play(5);
  await h3.flush();
  expect(h3.byType('start').length).toBe(1);
  expect(h3.sessionStore.getItem('032_playback_block_v1')).toBeNull();
});

test('applied:false with terminal state stops the local session from faking activity', async () => {
  const h = makeHarness();
  h.setResponder((body) => {
    if (body.type === 'heartbeat') {
      return { status: 200, json: { applied: false, sessionId: body.sessionId, state: 'ended', positionMillis: body.positionMillis, counted: false } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(7);
  await h.flush();
  h.fireTimers();
  await h.flush();
  expect(h.byType('heartbeat').length).toBe(1);
  expect(h.reporter.getDebug().sessionId).toBeNull();
  h.fireTimers();
  await h.flush();
  expect(h.byType('heartbeat').length).toBe(1); // no more faked activity
  // M3: the finalized session latches — no silent self-heal, and a restore
  // does not lift the latch (no high-position fresh chain mid-track).
  expect(h.reporter.isBlocked()).toBe(true);
  h.reporter.notifyPlaying();
  h.reporter.restore(7, { initialState: 'buffering', positionMillis: 30000 });
  await h.flush();
  expect(h.byType('start').length).toBe(1);
  // only an explicit new play recovers
  h.reporter.play(7);
  await h.flush();
  expect(h.reporter.isBlocked()).toBe(false);
  expect(h.byType('start').length).toBe(2);
});

test('applied:false with a live state is ignored and the session continues', async () => {
  const h = makeHarness();
  h.setResponder((body) => {
    if (body.type === 'pause') {
      return { status: 200, json: { applied: false, sessionId: body.sessionId, state: 'paused', positionMillis: body.positionMillis, counted: false } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(7);
  h.reporter.notifyPaused();
  await h.flush();
  expect(h.reporter.getDebug().sessionId).toBe('sid-1');
  h.fireTimers();
  await h.flush();
  expect(h.byType('heartbeat').length).toBe(1);
});

test('restore resumes the persisted chain only when the track matches', async () => {
  const h1 = makeHarness({ snapshot: { sessionId: 'old-9', trackId: 7 } });
  h1.reporter.restore(7, { initialState: 'buffering', positionMillis: 22000 });
  await h1.flush();
  expect(h1.byType('start')[0]).toMatchObject({ resumedFromSessionId: 'old-9', trackId: 7, positionMillis: 22000 });

  const h2 = makeHarness({ snapshot: { sessionId: 'old-9', trackId: 7 } });
  h2.reporter.restore(8, { initialState: 'paused', positionMillis: 5000 });
  await h2.flush();
  expect(h2.byType('start')[0].resumedFromSessionId).toBeUndefined();
});

test('suspend sends end(client_closed) with keepalive, bypassing the queue', async () => {
  // queue stuck behind a held start: the keepalive end must not wait.
  // The start has NOT landed, so the predecessor snapshot (a resumable
  // client_closed end) must be KEPT — a quick second refresh still has a
  // chain to resume and the server chain-fork handling counts at most once.
  const h1 = makeHarness({ snapshot: { sessionId: 'sid-0', trackId: 7, hasPlayed: true } });
  const release = h1.hold();
  h1.reporter.restore(7, { initialState: 'buffering', positionMillis: 2000 });
  await h1.flush();
  h1.setPosition(3000);
  h1.reporter.suspend();
  await h1.flush();
  expect(h1.sent.length).toBe(2);
  expect(h1.sent[1]).toMatchObject({ keepalive: true });
  expect(h1.sent[1].body).toMatchObject({ type: 'end', endReason: 'client_closed', positionMillis: 3000, seq: 2 });
  expect(JSON.parse(h1.sessionStore.getItem('032_playback_session_v1'))).toMatchObject({ sessionId: 'sid-0', trackId: 7 });
  release();

  // confirmed session: snapshot persisted for the resume chain
  const h2 = makeHarness();
  h2.reporter.play(7);
  await h2.flush();
  h2.setPosition(42000);
  h2.reporter.suspend();
  await h2.flush();
  expect(JSON.parse(h2.sessionStore.getItem('032_playback_session_v1'))).toMatchObject({ sessionId: 'sid-1', trackId: 7 });
  expect(h2.byType('end')[0]).toMatchObject({ endReason: 'client_closed', positionMillis: 42000 });
  // a reload-style restore resumes the chain
  h2.reporter.restore(7, { initialState: 'paused', positionMillis: 42000 });
  await h2.flush();
  const starts = h2.byType('start');
  expect(starts[1]).toMatchObject({ resumedFromSessionId: 'sid-1', trackId: 7 });
});

test('a late 409 on a replaced session recovers the old track without touching the new session', async () => {
  const h = makeHarness();
  h.setResponder((body) => {
    if (body.type === 'end' && body.trackId === 1) {
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(1);
  h.reporter.notifyPlaying();
  await h.flush();
  h.setPosition(15000);
  h.reporter.play(2, { reason: 'skipped' });
  await h.flush();
  // recovery start targets the OLD track, chains to the old session and
  // carries the play evidence (the old track actually played)
  const starts = h.byType('start');
  expect(starts.length).toBe(3);
  expect(starts[1]).toMatchObject({ trackId: 1, resumedFromSessionId: 'sid-1', state: 'playing', positionMillis: 15000 });
  expect(starts[2]).toMatchObject({ trackId: 2, sessionId: 'sid-2' });
  expect(starts[2].resumedFromSessionId).toBeUndefined();
  const ends = h.byType('end');
  expect(ends.length).toBe(2);
  expect(ends[1]).toMatchObject({ trackId: 1, endReason: 'skipped', positionMillis: 15000 });
  // the current session is still track 2 and keeps heartbeating
  expect(h.reporter.getDebug().sessionId).toBe('sid-2');
  h.fireTimers();
  await h.flush();
  const hbs = h.byType('heartbeat');
  expect(hbs.length).toBeGreaterThan(0);
  expect(hbs[0].sessionId).toBe('sid-2');
});

test('seek events report immediately and keep the session state', async () => {
  const h = makeHarness();
  h.reporter.play(7);
  h.reporter.notifyPlaying();
  await h.flush();
  h.setPosition(25000);
  h.reporter.notifySeek();
  await h.flush();
  const seeks = h.byType('seek');
  expect(seeks.length).toBe(1);
  expect(seeks[0]).toMatchObject({ positionMillis: 25000, sessionId: 'sid-1' });
  expect(seeks[0].state).toBeUndefined(); // state stays server-side
  h.fireTimers();
  await h.flush();
  expect(h.byType('heartbeat')[0].state).toBe('playing');
});

test('buffering transitions only from playing, and error ends the session', async () => {
  const h = makeHarness();
  h.reporter.play(7);
  await h.flush();
  h.reporter.notifyBuffering(); // initial state is already buffering: no-op
  await h.flush();
  expect(h.byType('buffering').length).toBe(0);
  h.reporter.notifyPlaying();
  await h.flush();
  h.reporter.notifyBuffering();
  await h.flush();
  expect(h.byType('buffering').length).toBe(1);
  h.reporter.fail();
  await h.flush();
  expect(h.byType('end')[0].endReason).toBe('error');
});

test('unplayed paused session: 409 on end recovers with the honest paused state, never fakes playing', async () => {
  const h = makeHarness();
  let end409 = true;
  h.setResponder((body) => {
    if (body.type === 'end' && end409) {
      end409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(7);
  await h.flush();
  h.reporter.notifyPaused(); // paused without ever playing
  await h.flush();
  h.setPosition(40000); // high position reached by seeking while paused
  h.reporter.stopSession('stopped');
  await h.flush();
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  // The end is still restored (start resume at the final position + end),
  // but the recovery start keeps the honest paused state.
  expect(starts[1]).toMatchObject({ sessionId: 'sid-2', resumedFromSessionId: 'sid-1', state: 'paused', positionMillis: 40000 });
  const ends = h.byType('end');
  expect(ends.length).toBe(2);
  expect(ends[1]).toMatchObject({ sessionId: 'sid-2', endReason: 'stopped', positionMillis: 40000 });
  // No event ever claimed playing for this track.
  expect(h.sent.every((s) => s.body.state !== 'playing')).toBe(true);
});

test('persisted hasPlayed is NOT sufficient: a restored paused chain recovers with the honest paused state', async () => {
  const h1 = makeHarness();
  h1.reporter.play(7);
  h1.reporter.notifyPlaying();
  await h1.flush();
  const snapshot = JSON.parse(h1.sessionStore.getItem('032_playback_session_v1'));
  // hasPlayed stays persisted as a historical record...
  expect(snapshot).toMatchObject({ sessionId: 'sid-1', trackId: 7, hasPlayed: true });

  // ...but a reload that restores the chain PAUSED (e.g. after a paused
  // high-position seek) must not let recovery disguise it as playing.
  const h2 = makeHarness({ snapshot });
  let end409 = true;
  h2.setResponder((body) => {
    if (body.type === 'end' && end409) {
      end409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h2.reporter.restore(7, { initialState: 'paused', positionMillis: 40000 });
  await h2.flush();
  expect(h2.byType('start')[0]).toMatchObject({ resumedFromSessionId: 'sid-1', state: 'paused' });
  h2.setPosition(42000);
  h2.reporter.stopSession('stopped');
  await h2.flush();
  const starts = h2.byType('start');
  expect(starts.length).toBe(2);
  // the end's frozen priorState is paused, so the recovery start is paused
  expect(starts[1]).toMatchObject({ sessionId: 'sid-2', resumedFromSessionId: 'sid-1', state: 'paused', positionMillis: 42000 });
  expect(h2.byType('end')[1]).toMatchObject({ sessionId: 'sid-2', endReason: 'stopped' });
  expect(h2.sent.every((s) => s.body.state !== 'playing')).toBe(true);
});

test('legacy snapshots without hasPlayed are treated as not played (no invented evidence)', async () => {
  const h = makeHarness({ snapshot: { sessionId: 'old-1', trackId: 7 } }); // no hasPlayed field
  let end409 = true;
  h.setResponder((body) => {
    if (body.type === 'end' && end409) {
      end409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.restore(7, { initialState: 'paused', positionMillis: 40000 });
  await h.flush();
  h.setPosition(42000);
  h.reporter.stopSession('stopped');
  await h.flush();
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  expect(starts[1].state).toBe('paused');
  expect(h.sent.every((s) => s.body.state !== 'playing')).toBe(true);
});

test('a mid-session 404 rebuild uses the honest current state (hasPlayed history is not enough)', async () => {
  const h = makeHarness();
  let missing = true;
  h.setResponder((body) => {
    if (body.type === 'heartbeat' && missing) {
      missing = false;
      return { status: 404, json: { error: { code: 'session_not_found', message: 'cleared' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(7);
  h.reporter.notifyPlaying();
  h.reporter.notifyPaused(); // played earlier, paused now
  await h.flush();
  h.fireTimers();
  await h.flush();
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  // honest: paused now, so the rebuild start is paused — never playing
  expect(starts[1]).toMatchObject({ sessionId: 'sid-1', state: 'paused' });
  h.fireTimers();
  await h.flush();
  const hbs = h.byType('heartbeat');
  expect(hbs[hbs.length - 1].state).toBe('paused');
});

test('rapid track switch behind a stuck transport serializes start/end/start and heartbeats stay on the live session', async () => {
  const h = makeHarness();
  const release = h.hold();
  h.reporter.play(1);
  await h.flush();
  h.setPosition(8000);
  h.reporter.play(2, { reason: 'skipped' }); // async switch while start 1 is in flight
  await h.flush();
  release();
  await h.flush();
  expect(h.sent.map((s) => s.body.type)).toEqual(['start', 'end', 'start']);
  const starts = h.byType('start');
  expect(starts[0]).toMatchObject({ trackId: 1, sessionId: 'sid-1' });
  expect(starts[1]).toMatchObject({ trackId: 2, sessionId: 'sid-2' });
  expect(h.byType('end')[0]).toMatchObject({ trackId: 1, sessionId: 'sid-1', endReason: 'skipped', positionMillis: 8000 });
  h.fireTimers();
  await h.flush();
  const hbs = h.byType('heartbeat');
  expect(hbs.length).toBeGreaterThan(0);
  expect(hbs.every((b) => b.sessionId === 'sid-2')).toBe(true);
});

test('stopped keeps the resumable snapshot and restore resumes the chain (undo-clear path)', async () => {
  const h = makeHarness();
  h.reporter.play(7);
  h.reporter.notifyPlaying();
  await h.flush();
  h.setPosition(25000);
  h.reporter.stopSession('stopped');
  await h.flush();
  // stopped is a resumable endReason: the snapshot is kept, not cleared
  expect(JSON.parse(h.sessionStore.getItem('032_playback_session_v1'))).toMatchObject({ sessionId: 'sid-1', trackId: 7, hasPlayed: true });
  // self-heal / undo-clear path: restore resumes the SAME chain instead of
  // opening a silent no-resume chain at a high position
  h.reporter.restore(7, { initialState: 'buffering', positionMillis: 25000 });
  await h.flush();
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  expect(starts[1]).toMatchObject({ resumedFromSessionId: 'sid-1', trackId: 7, positionMillis: 25000 });
});

test('a late 409 for an in-flight non-end event of a replaced session does not revive the old track', async () => {
  const h = makeHarness();
  let hb409 = true;
  h.setResponder((body) => {
    if (body.type === 'heartbeat' && hb409) {
      hb409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(1);
  h.reporter.notifyPlaying();
  await h.flush();
  const release = h.hold();
  h.fireTimers(); // heartbeat of sid-1 goes in flight (held)
  await h.flush();
  h.reporter.play(2, { reason: 'skipped' }); // switch before the 409 arrives
  await h.flush();
  release();
  await h.flush();
  // no recovery start for the old track — the queued end closes the chain
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  expect(starts.every((s) => !s.resumedFromSessionId)).toBe(true);
  expect(h.byType('end')[0]).toMatchObject({ trackId: 1, sessionId: 'sid-1', endReason: 'skipped' });
  // the live session is track 2 and keeps heartbeating
  expect(h.reporter.getDebug().sessionId).toBe('sid-2');
  h.fireTimers();
  await h.flush();
  const hbs = h.byType('heartbeat');
  expect(hbs.length).toBeGreaterThan(1);
  expect(hbs[0].sessionId).toBe('sid-1'); // the in-flight one that earned the 409
  expect(hbs.slice(1).every((b) => b.sessionId === 'sid-2')).toBe(true);
});

test('pending seeks coalesce per session while the queue is stuck', async () => {
  const h = makeHarness();
  const release = h.hold();
  h.reporter.play(7);
  await h.flush();
  h.setPosition(1000);
  h.reporter.notifySeek();
  h.setPosition(2000);
  h.reporter.notifySeek();
  h.setPosition(3000);
  h.reporter.notifySeek();
  await h.flush();
  release();
  await h.flush();
  const seeks = h.byType('seek');
  expect(seeks.length).toBe(1); // only the latest pending seek is delivered
  expect(seeks[0].positionMillis).toBe(3000);
});

test('play 5s, pause, seek to 80%: 409/404 recovery never disguises the session as playing', async () => {
  // 409 on the heartbeat after a paused high-position seek
  const h1 = makeHarness();
  let hb409 = true;
  h1.setResponder((body) => {
    if (body.type === 'heartbeat' && hb409) {
      hb409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h1.reporter.play(7);
  h1.reporter.notifyPlaying(); // really played ~5s
  h1.reporter.notifyPaused();
  await h1.flush();
  h1.setPosition(48000); // dragged to 80% of 60000 while paused
  h1.fireTimers();
  await h1.flush();
  const starts1 = h1.byType('start');
  expect(starts1.length).toBe(2);
  expect(starts1[1]).toMatchObject({ sessionId: 'sid-2', resumedFromSessionId: 'sid-1', state: 'paused', positionMillis: 48000 });
  // no start or heartbeat ever claims playing for this paused session
  expect(starts1.every((s) => s.state !== 'playing')).toBe(true);
  expect(h1.byType('heartbeat').every((b) => b.state !== 'playing')).toBe(true);

  // 404 on the end after a paused high-position seek: discarded, not restored
  const h2 = makeHarness();
  h2.setResponder((body) => {
    if (body.type === 'end') return { status: 404, json: { error: { code: 'session_not_found', message: 'cleared' } } };
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h2.reporter.play(7);
  h2.reporter.notifyPlaying();
  h2.reporter.notifyPaused();
  await h2.flush();
  h2.setPosition(48000);
  h2.reporter.stopSession('stopped');
  await h2.flush();
  expect(h2.byType('start').length).toBe(1); // no start+end restore
  expect(h2.byType('end').length).toBe(1);

  // contrast: still PLAYING at the end past the threshold -> 404 restores start+end with state playing
  const h3 = makeHarness();
  h3.setResponder((body) => {
    if (body.type === 'end') return { status: 404, json: { error: { code: 'session_not_found', message: 'cleared' } } };
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h3.reporter.play(7);
  h3.reporter.notifyPlaying();
  await h3.flush();
  h3.setPosition(48000);
  h3.reporter.stopSession('stopped'); // ends while playing: priorState playing
  await h3.flush();
  const starts3 = h3.byType('start');
  expect(starts3.length).toBe(2);
  expect(starts3[1]).toMatchObject({ sessionId: 'sid-2', state: 'playing', positionMillis: 48000 });
  expect(h3.byType('end')[1]).toMatchObject({ sessionId: 'sid-2', endReason: 'stopped', positionMillis: 48000 });
});

test('play() carries an explicit start position and the new track duration', async () => {
  const h = makeHarness();
  h.setPosition(55555); // old track's leftovers must not leak into the new start
  h.setDuration(60000);
  h.reporter.play(9, { positionMillis: 0, durationMillis: 123456 });
  await h.flush();
  expect(h.byType('start')[0]).toMatchObject({ trackId: 9, positionMillis: 0, durationMillis: 123456 });
});

test('409 with multiple queued events: exactly one recovery session, queued jobs retargeted with frozen identities', async () => {
  const h = makeHarness();
  let hb409 = true;
  h.setResponder((body) => {
    if (body.type === 'heartbeat' && hb409) {
      hb409 = false;
      return { status: 409, json: { error: { code: 'session_expired', message: 'expired' } } };
    }
    return { status: 200, json: { applied: true, sessionId: body.sessionId, state: body.state || 'playing', positionMillis: body.positionMillis, counted: false } };
  });
  h.reporter.play(7);
  h.reporter.notifyPlaying();
  await h.flush();
  const release = h.hold();
  h.fireTimers(); // heartbeat of sid-1 in flight (held)
  await h.flush();
  h.setPosition(12000);
  h.reporter.notifySeek(); // queued behind, position frozen at 12000
  h.setPosition(12500);
  h.reporter.notifyPaused(); // queued behind
  await h.flush();
  release();
  await h.flush();
  // exactly ONE recovery session — the queued jobs must not fork a second
  const starts = h.byType('start');
  expect(starts.length).toBe(2);
  expect(starts[1]).toMatchObject({ sessionId: 'sid-2', resumedFromSessionId: 'sid-1', state: 'paused' });
  // queued jobs retargeted onto the single recovery session, frozen identity kept
  const seeks = h.byType('seek');
  expect(seeks.length).toBe(1);
  expect(seeks[0]).toMatchObject({ sessionId: 'sid-2', positionMillis: 12000 });
  const pauses = h.byType('pause');
  expect(pauses.length).toBe(1);
  expect(pauses[0].sessionId).toBe('sid-2');
  // the in-flight old heartbeat was the only one on sid-1; no second recovery follows
  h.fireTimers();
  await h.flush();
  expect(h.byType('start').length).toBe(2);
  const hbs = h.byType('heartbeat');
  expect(hbs.filter((b) => b.sessionId === 'sid-1').length).toBe(1);
  expect(hbs.filter((b) => b.sessionId === 'sid-2').length).toBeGreaterThan(0);
});
