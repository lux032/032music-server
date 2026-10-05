// 032 Music Server - playback-reporter.js
// Event-driven playback session reporter (apiRevision 3 session protocol).
//
// Pure logic module: every side effect (network, clock, timers, storage,
// uuid, position/duration probe) is injected, so it can be unit tested
// without a DOM or a real server.
//
// Contract implemented (README "播放上报（会话协议）" + capabilities):
//  - one durable session per explicit play; clientId persists in
//    localStorage, sessionId is fresh per play and per tab.
//  - seq is strictly monotonic inside a session and events are delivered
//    serially; a start is retried with the SAME sessionId+seq (idempotent
//    on the server); a heartbeat is never sent before its session's start.
//  - heartbeats are droppable/coalesced so a network outage can never
//    build an unbounded backlog of stale heartbeats.
//  - heartbeat cadence: 15s while playing/buffering, 60s while paused.
//  - pause/buffering/resume/seek/end are reported immediately; the state
//    carried by start/heartbeat is the real audio state.
//  - 409 session_expired: exactly one serial resume — start a new session
//    with resumedFromSessionId at the current position, then re-send the
//    current event (an end is re-sent with its final position).
//  - 404 session_not_found: rebuild with a fresh start (no resume); a lost
//    end is restored with start+end only when the state just before the
//    end was really 'playing' AND the position passed the count threshold,
//    otherwise it is discarded.
//  - hasPlayed: the reporter keeps evidence that the session actually
//    reached the real 'playing' state and persists it in the snapshot.
//    It is a historical record only — it is NEVER sufficient to label a
//    recovery start 'playing': recovery evidence follows the server's
//    priorState rule, i.e. the honest state just before the event. A
//    session paused at a high position (e.g. seek while paused) is never
//    disguised as playing, so a play that only reached the threshold
//    through a paused seek can never be falsely counted.
//  - 409 resume_invalid: never silently degrade into a high-position new
//    start (that would double-count). The invalid resume info is cleared,
//    reporting pauses, a block marker is PERSISTED to sessionStorage so a
//    refresh of the same track stays blocked, and only an explicit new
//    play starts reporting again.
//  - applied:false with a terminal server state (or any unrecoverable
//    protocol error on the live session) latches the reporter the same
//    way: no silent mid-track self-heal, only an explicit new play.
//  - a late 409 for a non-end event of an already replaced/ended session
//    never revives the old track; its queued end closes the chain itself.
//  - errors arriving late for an already replaced session never close or
//    "recover" the current session.

const CLIENT_ID_KEY = '032_playback_client_id';
const SNAPSHOT_KEY = '032_playback_session_v1';
const BLOCK_KEY = '032_playback_block_v1';
const CLIENT_ID_PATTERN = /^[A-Za-z0-9-]{1,64}$/;

function safeWindowStorage(kind) {
  try { return window[kind]; } catch (_) { return null; }
}

export function createPlaybackReporter(options = {}) {
  const transport = options.transport;
  if (typeof transport !== 'function') throw new Error('playback reporter requires a transport(body, {keepalive})');
  const getPositionMillis = options.getPositionMillis || (() => 0);
  const getDurationMillis = options.getDurationMillis || (() => 0);
  const now = options.now || (() => Date.now());
  const setTimeoutFn = options.setTimeout || ((fn, ms) => setTimeout(fn, ms));
  const clearTimeoutFn = options.clearTimeout || ((id) => clearTimeout(id));
  const uuid = options.uuid || (() => crypto.randomUUID());
  const local = options.localStorage !== undefined ? options.localStorage : safeWindowStorage('localStorage');
  const sessionStore = options.sessionStorage !== undefined ? options.sessionStorage : safeWindowStorage('sessionStorage');
  const heartbeatActiveMillis = options.heartbeatActiveMillis || 15000;
  const heartbeatPausedMillis = options.heartbeatPausedMillis || 60000;
  // Bounded network retries: 1 initial attempt + these backoff delays.
  const retryDelaysMillis = options.retryDelaysMillis || [1000, 3000];
  const clientKind = options.clientKind || 'web';
  const onProtocolError = options.onProtocolError || (() => {});

  let clientId = null;
  let session = null;        // current session object (may be unconfirmed)
  let blocked = false;       // resume_invalid latch; only play()/restore() clears it
  let queue = [];
  let pumping = false;
  let heartbeatTimer = null;

  // ------------------------------------------------------------ utilities
  function loadClientId() {
    if (clientId) return clientId;
    let id = null;
    try { id = local && local.getItem(CLIENT_ID_KEY); } catch (_) {}
    if (typeof id !== 'string' || !CLIENT_ID_PATTERN.test(id)) {
      id = uuid();
      try { if (local) local.setItem(CLIENT_ID_KEY, id); } catch (_) {}
    }
    clientId = id;
    return id;
  }

  function persistSnapshot(sess) {
    try {
      if (sessionStore) sessionStore.setItem(SNAPSHOT_KEY, JSON.stringify({ sessionId: sess.id, trackId: sess.trackId, hasPlayed: sess.hasPlayed === true }));
    } catch (_) {}
  }

  function readSnapshot() {
    try {
      const raw = sessionStore && sessionStore.getItem(SNAPSHOT_KEY);
      if (!raw) return null;
      const parsed = JSON.parse(raw);
      if (!parsed || typeof parsed.sessionId !== 'string' || !Number.isInteger(parsed.trackId)) return null;
      // Legacy snapshots without hasPlayed are treated as "not played":
      // never invent play evidence.
      parsed.hasPlayed = parsed.hasPlayed === true;
      return parsed;
    } catch (_) { return null; }
  }

  function clearSnapshot() {
    try { if (sessionStore) sessionStore.removeItem(SNAPSHOT_KEY); } catch (_) {}
  }

  // H1: the resume_invalid block is persisted so a page refresh of the same
  // track cannot silently open a new high-position chain.
  function writeBlockMarker(trackId) {
    try { if (sessionStore) sessionStore.setItem(BLOCK_KEY, JSON.stringify({ blocked: true, trackId })); } catch (_) {}
  }

  function readBlockMarker() {
    try {
      const raw = sessionStore && sessionStore.getItem(BLOCK_KEY);
      if (!raw) return null;
      const parsed = JSON.parse(raw);
      if (!parsed || parsed.blocked !== true || !Number.isInteger(parsed.trackId)) return null;
      return parsed;
    } catch (_) { return null; }
  }

  function clearBlockMarker() {
    try { if (sessionStore) sessionStore.removeItem(BLOCK_KEY); } catch (_) {}
  }

  function probePosition() { return Math.max(0, Math.floor(Number(getPositionMillis()) || 0)); }
  function probeDuration() { return Math.max(0, Math.floor(Number(getDurationMillis()) || 0)); }
  function sleep(millis) { return new Promise((resolve) => setTimeoutFn(resolve, millis)); }

  function newSession(trackId, resumeFrom, state) {
    const initialState = state || 'buffering';
    return {
      id: uuid(), trackId, seq: 0, state: initialState,
      // hasPlayed is a historical record that the track once reached the
      // real 'playing' state; it is persisted in the snapshot but is NOT
      // sufficient evidence to label a recovery start 'playing'.
      hasPlayed: initialState === 'playing',
      started: false, ended: false, superseded: false,
      endDelivered: false, endGivenUp: false, missingRecoveryDone: false,
    };
  }

  // Recovery-start state follows the server's priorState rule: a non-end
  // recovery start carries the session's honest CURRENT state; an end
  // recovery start may say 'playing' only when the state frozen at the end
  // was really 'playing'. hasPlayed history alone never justifies it — a
  // paused seek to a high position must not be disguised as playing.
  function recoveryStartState(sess, job) {
    if (job.type === 'end') return job.priorState === 'playing' ? 'playing' : job.priorState;
    return sess.state;
  }

  // ------------------------------------------------------------- queueing
  // A job captures position/duration at enqueue time (the moment the event
  // happened); heartbeats refresh the position at delivery time. sessionId
  // and seq are stamped at delivery so a retried start keeps the same seq
  // and a recovered session continues its own monotonic sequence.
  function makeJob(sess, type, extra = {}) {
    return {
      session: sess,
      type,
      state: extra.state || null,
      endReason: extra.endReason || null,
      resumeFrom: extra.resumeFrom || null,
      // Frozen at enqueue: the session state at the moment an end happened.
      // End recovery uses it as server-style priorState evidence; it must
      // never be re-read from the session later (a stale or mutated state
      // could fabricate playing evidence).
      priorState: type === 'end' ? sess.state : null,
      positionMillis: extra.positionMillis !== undefined ? Math.max(0, Math.floor(extra.positionMillis)) : probePosition(),
      durationMillis: extra.durationMillis !== undefined ? Math.max(0, Math.floor(extra.durationMillis)) : probeDuration(),
      refreshPosition: !!extra.refreshPosition,
      recoveryDepth: extra.recoveryDepth || 0,
    };
  }

  function enqueue(sess, type, extra = {}) {
    const job = makeJob(sess, type, extra);
    // Coalesce pending seeks of the same session: only the latest matters.
    if (type === 'seek') queue = queue.filter((other) => !(other.session === sess && other.type === 'seek'));
    queue.push(job);
    pump();
    return job;
  }

  function isStale(job) {
    const sess = job.session;
    if (job.type === 'end') return sess.endDelivered || sess.endGivenUp;
    if (sess.ended || sess.superseded) return true;
    if (job.type === 'heartbeat') {
      // Droppable: any newer event queued for the same session supersedes it.
      return queue.some((other) => other !== job && other.session === sess);
    }
    return false;
  }

  async function pump() {
    if (pumping) return;
    pumping = true;
    try {
      while (queue.length > 0) {
        const job = queue.shift();
        if (isStale(job)) continue;
        await deliver(job);
      }
    } finally {
      pumping = false;
    }
  }

  function buildBody(job) {
    const sess = job.session;
    sess.seq += 1;
    const body = {
      clientId: loadClientId(),
      clientKind,
      sessionId: sess.id,
      seq: sess.seq,
      type: job.type,
      trackId: sess.trackId,
      positionMillis: job.refreshPosition ? probePosition() : job.positionMillis,
      durationMillis: job.durationMillis,
    };
    if (job.type === 'start') {
      body.state = job.state;
      if (job.resumeFrom) body.resumedFromSessionId = job.resumeFrom;
    } else if (job.type === 'heartbeat') {
      body.state = sess.state;
    } else if (job.type === 'end') {
      body.endReason = job.endReason;
    }
    return body;
  }

  async function deliver(job) {
    const body = buildBody(job);
    let response = null;
    for (let attempt = 0; ; attempt++) {
      try {
        response = await transport(body, { keepalive: false });
        if (!response || response.status < 500) break;
        // 5xx is retryable like a network error.
      } catch (_) { /* network error: retry bounded */ }
      if (attempt >= retryDelaysMillis.length) {
        if (job.type === 'end') job.session.endGivenUp = true;
        return; // gave up; the session lease will expire server-side
      }
      await sleep(retryDelaysMillis[attempt]);
      if (isStale(job)) return;
    }
    handleResponse(job, response);
  }

  function errorCode(response) {
    return response && response.json && response.json.error ? response.json.error.code : null;
  }

  function handleResponse(job, response) {
    const sess = job.session;
    const status = response ? response.status : 0;
    const json = response ? response.json : null;

    if (status >= 200 && status < 300) {
      if (job.type === 'start') { sess.started = true; sess.missingRecoveryDone = false; }
      if (job.type === 'end') {
        sess.endDelivered = true;
        sess.ended = true;
        if (sess === session) cancelHeartbeat();
      }
      if (json && json.applied) {
        if (sess === session && !sess.ended) persistSnapshot(sess);
        return;
      }
      // applied:false — stale seq / idempotent replay: harmless. But a
      // terminal server state must stop the local session from faking life.
      if (json && json.state === 'ended') {
        finalizeLocally(sess, 'server_ended');
      }
      return;
    }

    const code = errorCode(response);
    if (status === 404 && code === 'session_not_found') { recoverMissing(job); return; }
    if (status === 409 && code === 'session_expired') { recoverExpired(job); return; }
    if (status === 409 && code === 'resume_invalid') { blockResumeInvalid(sess); return; }
    if (status === 404 && code === 'not_found') { dropSession(sess, 'track_not_found'); return; }
    if (status === 400 || status === 409) { dropSession(sess, code || 'invalid'); return; }
    // Unexpected status: leave the session alone; heartbeats keep trying.
    if (job.type === 'end') sess.endGivenUp = true;
  }

  // The session is terminal server-side (or unusable): stop reporting on
  // it. When it was the live session, latch against a silent high-position
  // self-heal — only an explicit new play starts reporting again.
  function finalizeLocally(sess, reason) {
    sess.ended = true;
    sess.endDelivered = true;
    if (sess === session) {
      session = null;
      cancelHeartbeat();
      clearSnapshot();
      blocked = true;
    }
    if (reason) onProtocolError({ reason, sessionId: sess.id, trackId: sess.trackId });
  }

  function dropSession(sess, reason) {
    finalizeLocally(sess, reason);
  }

  // resume_invalid must never degrade into a silent high-position new start:
  // clear the persisted resume info, block auto-reporting (persisted, so a
  // refresh stays blocked) and wait for an explicit new play.
  function blockResumeInvalid(sess) {
    blocked = true;
    writeBlockMarker(sess.trackId);
    finalizeLocally(sess, 'resume_invalid');
  }

  // 404 session_not_found: rebuild. A lost end is restored with start+end
  // (original reason, final position, state playing) only when the state
  // frozen at the end was really 'playing' AND the position passed the
  // count threshold (README: a paused high-position seek never counts);
  // anything else is simply discarded.
  function recoverMissing(job) {
    const sess = job.session;
    if (job.type === 'end') {
      sess.endDelivered = true;
      if (job.recoveryDepth < 1 && job.priorState === 'playing' && pastCountThreshold(job.positionMillis, job.durationMillis)) {
        const recovery = newSession(sess.trackId, null, 'playing');
        const startJob = makeJob(recovery, 'start', { state: 'playing', positionMillis: job.positionMillis, durationMillis: job.durationMillis, recoveryDepth: job.recoveryDepth + 1 });
        const endJob = makeJob(recovery, 'end', { endReason: job.endReason, positionMillis: job.positionMillis, durationMillis: job.durationMillis, recoveryDepth: job.recoveryDepth + 1 });
        queue.unshift(endJob);
        queue.unshift(startJob);
      }
      return;
    }
    if (sess.missingRecoveryDone) return;
    if (job.recoveryDepth >= 2) { dropSession(sess, 'missing_recovery_failed'); return; }
    sess.missingRecoveryDone = true;
    // Same sessionId (unknown server-side), no resume, honest current state.
    queue.unshift(makeJob(sess, 'start', { state: sess.state }));
  }

  // 409 session_expired: exactly one serial resume — a new session with
  // resumedFromSessionId, then the current event is re-sent against it (an
  // end keeps its final position and original reason).
  function recoverExpired(job) {
    const sess = job.session;
    if (job.recoveryDepth >= 1) { dropSession(sess, 'expired_recovery_failed'); return; }
    const isEnd = job.type === 'end';
    // A late 409 for a non-end event of an already replaced/ended session
    // must not revive the old track; its queued end closes the chain itself.
    if (!isEnd && (sess.superseded || sess.ended)) return;
    if (isEnd) sess.endDelivered = true;
    // The replacement session keeps the honest current state (so later
    // transitions and heartbeats stay truthful); the recovery START follows
    // the priorState rule for its evidence (see recoveryStartState).
    const recovery = newSession(sess.trackId, sess.id, sess.state);
    recovery.hasPlayed = sess.hasPlayed;
    const depth = job.recoveryDepth + 1;
    const startJob = makeJob(recovery, 'start', {
      state: recoveryStartState(sess, job),
      positionMillis: isEnd ? job.positionMillis : undefined,
      durationMillis: job.durationMillis > 0 ? job.durationMillis : undefined,
      resumeFrom: sess.id,
      recoveryDepth: depth,
    });
    // Retarget the current event onto the recovery session.
    job.session = recovery;
    job.recoveryDepth = depth;
    // Exactly one recovery per expired session: mark the old session and
    // move every still-queued job for it onto the single recovery session,
    // preserving each job's frozen priorState/position identity (ends are
    // never dropped). A late old-session response then hits the superseded
    // guard above instead of forking a second recovery chain.
    sess.superseded = true;
    for (const pending of queue) {
      if (pending.session === sess) {
        pending.session = recovery;
        pending.recoveryDepth = depth;
      }
    }
    queue.unshift(job);
    queue.unshift(startJob);
    // An end recovery exists only to close the chain out: it must NOT
    // become the active session or be persisted as resumable (a reload
    // resuming an about-to-end-completed session earns a resume_invalid).
    if (!isEnd && sess === session) {
      session = recovery;
      persistSnapshot(recovery);
      scheduleHeartbeat();
    }
  }

  function pastCountThreshold(positionMillis, durationMillis) {
    return durationMillis > 0 && positionMillis >= durationMillis / 2;
  }

  // ------------------------------------------------------------ heartbeat
  function scheduleHeartbeat() {
    cancelHeartbeat();
    const sess = session;
    if (!sess || sess.ended || blocked) return;
    const interval = sess.state === 'paused' ? heartbeatPausedMillis : heartbeatActiveMillis;
    heartbeatTimer = setTimeoutFn(() => {
      heartbeatTimer = null;
      const cur = session;
      if (!cur || cur.ended || blocked) return;
      // Coalesce: never queue a second heartbeat while one is still waiting.
      if (!queue.some((job) => job.session === cur && job.type === 'heartbeat')) {
        enqueue(cur, 'heartbeat', { refreshPosition: true });
      }
      scheduleHeartbeat();
    }, interval);
  }

  function cancelHeartbeat() {
    if (heartbeatTimer !== null) {
      clearTimeoutFn(heartbeatTimer);
      heartbeatTimer = null;
    }
  }

  // -------------------------------------------------------- session cycles
  function endSession(sess, reason) {
    if (!sess || sess.ended) return;
    sess.ended = true;
    if (sess === session) cancelHeartbeat();
    enqueue(sess, 'end', { endReason: reason });
    // client_closed/error/stopped stay persisted (resumable whitelist — a
    // stopped session is the undo-clear / breakpoint resume path);
    // completed/skipped/replaced clear the snapshot so a reload never
    // resumes them.
    if (sess === session && (reason === 'completed' || reason === 'skipped' || reason === 'replaced')) {
      clearSnapshot();
    }
  }

  // Explicit new play: end the previous session (real position, given
  // reason) before starting a fresh one with a brand-new sessionId.
  function play(trackId, opts = {}) {
    blocked = false;
    clearBlockMarker(); // an explicit new play is the only thing that lifts a block
    const previous = session;
    if (previous && !previous.ended) {
      previous.superseded = true;
      endSession(previous, opts.reason || 'replaced');
    }
    session = newSession(trackId, null, opts.initialState || 'buffering');
    enqueue(session, 'start', {
      state: session.state,
      positionMillis: opts.positionMillis,
      durationMillis: opts.durationMillis,
    });
    scheduleHeartbeat();
  }

  // Refresh / restoreState / BFCache recovery of the same track: resume the
  // persisted chain when the track matches, otherwise start a new play.
  function restore(trackId, opts = {}) {
    if (session && !session.ended) return; // double-restore guard
    // A persisted resume_invalid block survives the refresh: restoring the
    // same track stays blocked (no silent new high-position chain). A stale
    // marker for a different track is dropped.
    const marker = readBlockMarker();
    if (marker) {
      if (marker.trackId === trackId) { blocked = true; return; }
      // Stale marker for a different track: a resume_invalid block does not
      // follow the user to another track.
      clearBlockMarker();
      blocked = false;
    }
    // An in-memory latch (server-finalized session, this page lifetime) is
    // also only lifted by an explicit new play — never by a restore.
    if (blocked) return;
    const snapshot = readSnapshot();
    const resumeFrom = snapshot && snapshot.trackId === trackId ? snapshot.sessionId : null;
    session = newSession(trackId, resumeFrom, opts.initialState || 'paused');
    // Resuming a chain carries the persisted play evidence forward.
    if (resumeFrom && snapshot.hasPlayed) session.hasPlayed = true;
    enqueue(session, 'start', {
      state: session.state,
      resumeFrom,
      positionMillis: opts.positionMillis,
      durationMillis: opts.durationMillis,
    });
    scheduleHeartbeat();
  }

  function transition(type, newState) {
    const sess = session;
    if (!sess || sess.ended || blocked) return;
    if (sess.state === newState) return;
    sess.state = newState;
    if (newState === 'playing') sess.hasPlayed = true;
    enqueue(sess, type, {});
    scheduleHeartbeat();
  }

  return {
    play,
    restore,
    notifyPlaying() { transition('resume', 'playing'); },
    notifyPaused() { transition('pause', 'paused'); },
    notifyBuffering() {
      const sess = session;
      if (!sess || sess.ended || blocked || sess.state !== 'playing') return;
      transition('buffering', 'buffering');
    },
    notifySeek() {
      const sess = session;
      if (!sess || sess.ended || blocked) return;
      enqueue(sess, 'seek', {});
    },
    // Natural end of the media: end(completed) with the real final
    // position (never 0 — the server zeroes the breakpoint itself).
    complete() { if (session) endSession(session, 'completed'); },
    fail() { if (session) endSession(session, 'error'); },
    stopSession(reason) { if (session) endSession(session, reason || 'stopped'); },
    // pagehide: best-effort end(client_closed) with keepalive, bypassing the
    // serial queue so it never waits behind backlog. The snapshot is kept so
    // a reload/BFCache return resumes the chain without double counting.
    suspend() {
      const sess = session;
      if (!sess || sess.ended) return;
      sess.ended = true;
      cancelHeartbeat();
      const body = {
        clientId: loadClientId(),
        clientKind,
        sessionId: sess.id,
        seq: sess.seq + 1,
        type: 'end',
        trackId: sess.trackId,
        positionMillis: probePosition(),
        durationMillis: probeDuration(),
        endReason: 'client_closed',
      };
      // The start is confirmed: persist so a reload resumes THIS session.
      // Unconfirmed: keep the predecessor snapshot (a resumable end) so a
      // quick second refresh still has a chain to resume — the server's
      // chain-fork handling guarantees it counts at most once.
      if (sess.started) persistSnapshot(sess);
      try { transport(body, { keepalive: true }).catch(() => {}); } catch (_) {}
    },
    hasActiveSession() { return !!(session && !session.ended); },
    isBlocked() { return blocked; },
    getDebug() {
      return {
        clientId,
        sessionId: session ? session.id : null,
        seq: session ? session.seq : 0,
        state: session ? session.state : null,
        hasPlayed: session ? session.hasPlayed : null,
        ended: session ? session.ended : null,
        blocked,
        queueLength: queue.length,
      };
    },
    dispose() {
      cancelHeartbeat();
      queue = [];
      session = null;
    },
  };
}
