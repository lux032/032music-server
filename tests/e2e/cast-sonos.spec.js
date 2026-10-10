// Web panel → Sonos queue casting against an in-test fake Sonos zone player
// (the e2e server is started with MUSIC_SERVER_CAST_DEVICES pointing here
// and multicast discovery off, so no real speaker is ever touched).
const { test, expect } = require('@playwright/test');
const http = require('node:http');

const PORT = 45450;
const UUID = 'RINCON_E2E000001400';

function unescapeXML(v) {
  return v.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&#34;/g, '"').replace(/&#39;/g, "'").replace(/&#xA;/g, '\n').replace(/&amp;/g, '&');
}
function escapeXML(v) {
  return String(v).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function createFakeSonos() {
  const sonos = { queue: [], metas: [], current: '', track: 1, state: 'STOPPED', relTime: '0:00:00', playMode: 'NORMAL', volume: 20, actions: [] };
  const server = http.createServer((req, res) => {
    let raw = '';
    req.on('data', (chunk) => { raw += chunk; });
    req.on('end', () => {
      if (req.method === 'GET') {
        res.setHeader('Content-Type', 'text/xml');
        res.end(`<?xml version="1.0"?><root xmlns="urn:schemas-upnp-org:device-1-0"><device><deviceType>urn:schemas-upnp-org:device:ZonePlayer:1</deviceType><friendlyName>127.0.0.1 - Sonos Era 100</friendlyName><manufacturer>Sonos, Inc.</manufacturer><modelName>Sonos Era 100</modelName><UDN>uuid:${UUID}</UDN><roomName>E2E Room</roomName></device></root>`);
        return;
      }
      const action = (req.headers.soapaction || '').replace(/"/g, '').split('#')[1];
      const arg = (name) => { const m = raw.match(new RegExp(`<${name}>([\\s\\S]*?)</${name}>`)); return m ? unescapeXML(m[1]) : ''; };
      sonos.actions.push(action);
      const out = {};
      switch (action) {
        case 'GetZoneGroupState':
          out.ZoneGroupState = `<ZoneGroupState><ZoneGroups><ZoneGroup Coordinator="${UUID}" ID="g"><ZoneGroupMember UUID="${UUID}" Location="http://127.0.0.1:${PORT}/xml/device_description.xml" ZoneName="E2E Room"/></ZoneGroup></ZoneGroups></ZoneGroupState>`;
          break;
        case 'RemoveAllTracksFromQueue': sonos.queue = []; sonos.metas = []; sonos.state = 'STOPPED'; break;
        case 'AddMultipleURIsToQueue': {
          const uris = arg('EnqueuedURIs').split(' ').filter(Boolean);
          const metas = arg('EnqueuedURIsMetaData').split(/ (?=<DIDL-Lite)/);
          sonos.queue.push(...uris); sonos.metas.push(...metas);
          out.NumTracksAdded = uris.length; out.NewQueueLength = sonos.queue.length;
          break;
        }
        case 'AddURIToQueue': {
          const at = Number(arg('DesiredFirstTrackNumberEnqueued'));
          if (at > 0 && at <= sonos.queue.length) { sonos.queue.splice(at - 1, 0, arg('EnqueuedURI')); sonos.metas.splice(at - 1, 0, arg('EnqueuedURIMetaData')); }
          else { sonos.queue.push(arg('EnqueuedURI')); sonos.metas.push(arg('EnqueuedURIMetaData')); }
          break;
        }
        case 'RemoveTrackFromQueue': { const n = Number(arg('ObjectID').replace('Q:0/', '')); sonos.queue.splice(n - 1, 1); sonos.metas.splice(n - 1, 1); break; }
        case 'ReorderTracksInQueue': {
          const start = Number(arg('StartingIndex')); const before = Number(arg('InsertBefore'));
          const [uri] = sonos.queue.splice(start - 1, 1); const [meta] = sonos.metas.splice(start - 1, 1);
          const pos = before > start ? before - 2 : before - 1;
          sonos.queue.splice(pos, 0, uri); sonos.metas.splice(pos, 0, meta);
          break;
        }
        case 'SetAVTransportURI': sonos.current = arg('CurrentURI'); sonos.track = 1; break;
        case 'Seek':
          if (arg('Unit') === 'TRACK_NR') { sonos.track = Number(arg('Target')); sonos.relTime = '0:00:00'; } else sonos.relTime = arg('Target');
          break;
        case 'Play': sonos.state = 'PLAYING'; break;
        case 'Pause': sonos.state = 'PAUSED_PLAYBACK'; break;
        case 'Stop': sonos.state = 'STOPPED'; break;
        case 'Next': sonos.track = Math.min(sonos.track + 1, sonos.queue.length); break;
        case 'Previous': sonos.track = Math.max(sonos.track - 1, 1); break;
        case 'SetPlayMode': sonos.playMode = arg('NewPlayMode'); break;
        case 'GetTransportInfo': out.CurrentTransportState = sonos.state; break;
        case 'GetMediaInfo': out.CurrentURI = sonos.current; out.NrTracks = sonos.queue.length; break;
        case 'GetPositionInfo':
          out.Track = sonos.track; out.RelTime = sonos.relTime; out.TrackDuration = '0:00:30';
          out.TrackURI = sonos.queue[sonos.track - 1] || ''; out.TrackMetaData = sonos.metas[sonos.track - 1] || '';
          break;
        case 'Browse': {
          const start = Number(arg('StartingIndex')); const count = Number(arg('RequestedCount'));
          const slice = sonos.queue.slice(start, start + count);
          out.Result = `<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/">${slice.map((u, i) => `<item id="Q:0/${start + i + 1}"><dc:title>T</dc:title><res>${escapeXML(u)}</res></item>`).join('')}</DIDL-Lite>`;
          out.NumberReturned = slice.length; out.TotalMatches = sonos.queue.length;
          break;
        }
        case 'GetGroupVolume': out.CurrentVolume = sonos.volume; break;
        case 'SetGroupVolume': sonos.volume = Number(arg('DesiredVolume')); break;
        default:
          res.statusCode = 500;
          res.end(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><detail><UPnPError><errorCode>401</errorCode></UPnPError></detail></s:Fault></s:Body></s:Envelope>`);
          return;
      }
      res.setHeader('Content-Type', 'text/xml; charset="utf-8"');
      res.end(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><u:${action}Response xmlns:u="urn:x">${Object.entries(out).map(([k, v]) => `<${k}>${escapeXML(v)}</${k}>`).join('')}</u:${action}Response></s:Body></s:Envelope>`);
    });
  });
  return { sonos, server };
}

let fake;
test.beforeAll(async () => {
  fake = createFakeSonos();
  await new Promise((resolve) => fake.server.listen(PORT, '127.0.0.1', resolve));
});
test.afterAll(async () => {
  await new Promise((resolve) => fake.server.close(resolve));
});

test.beforeEach(({}, testInfo) => { test.skip(testInfo.project.name !== 'desktop-chromium', 'cast flow is viewport independent'); });

async function login(page) {
  await page.setViewportSize({ width: 1672, height: 900 });
  await page.goto('/admin/login');
  await page.getByLabel('用户名').fill('admin');
  await page.getByLabel('密码').fill('e2e-admin-password');
  await page.getByRole('button', { name: /登录/ }).click();
  await page.waitForURL(/\/admin$/);
  await expect.poll(async () => Number(await page.locator('#tracks-count').textContent())).toBeGreaterThan(0);
}

const trackIdOf = (uri) => Number((/\/api\/v1\/tracks\/(\d+)\//.exec(uri) || [])[1] || 0);

test('pushes the whole queue to the Sonos queue and mirrors device-side playback', async ({ page, request }) => {
  await login(page);
  await page.goto('/admin/tracks');
  await page.locator('.row-play-btn').first().click();
  await expect(page.locator('#player-title')).not.toHaveText('未在播放');
  const audio = page.locator('#global-audio-element');
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(false);
  const queue = await page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).queue);
  expect(queue.length).toBeGreaterThan(3);

  // Pick the fake Sonos from the cast menu.
  await page.locator('#player-btn-cast').click();
  const option = page.locator('#cast-menu').getByRole('menuitemradio', { name: /E2E Room/ });
  await expect(option).toBeVisible();
  await option.click();

  // Batch push: the whole page queue lands in the Sonos queue, transport on
  // the queue, playing track 1 — and the browser stops its own audio.
  await expect.poll(() => fake.sonos.queue.length).toBe(queue.length);
  await expect.poll(() => fake.sonos.state).toBe('PLAYING');
  expect(fake.sonos.current).toBe(`x-rincon-queue:${UUID}#0`);
  expect(fake.sonos.queue.map(trackIdOf)).toEqual(queue.map((t) => Number(t.id)));
  expect(fake.sonos.actions).toContain('AddMultipleURIsToQueue');
  expect(fake.sonos.actions).not.toContain('AddURIToQueue');
  await expect(page.locator('body')).toHaveClass(/is-casting/);
  await expect(page.locator('#player-cast-label')).toHaveText('E2E Room');
  await expect.poll(() => audio.evaluate((el) => el.paused)).toBe(true);

  // DIDL-Lite metadata carries title / artist / album / cover.
  expect(fake.sonos.metas[0]).toContain(`<dc:title>${escapeXML(queue[0].title)}</dc:title>`);
  expect(fake.sonos.metas[0]).toContain('<upnp:album>');
  expect(fake.sonos.metas[0]).toContain('object.item.audioItem.musicTrack');

  // The speaker can fetch the audio stream over the LAN with the media token.
  const media = await request.get(fake.sonos.queue[0]);
  expect(media.status()).toBe(200);
  expect(media.headers()['content-type']).toMatch(/^audio\//);

  // Sonos advances on its own: the panel follows without any push.
  fake.sonos.track = 3;
  fake.sonos.relTime = '0:00:07';
  await expect(page.locator('#player-title')).toHaveText(queue[2].title);
  await expect(page.locator('#player-time-cur')).toHaveText(/0:0[7-9]/);

  // Controls go to the renderer.
  await page.locator('#player-btn-play').click();
  await expect.poll(() => fake.sonos.state).toBe('PAUSED_PLAYBACK');
  await page.locator('#player-btn-next').click();
  await expect.poll(() => fake.sonos.track).toBe(4);
  await expect(page.locator('#player-title')).toHaveText(queue[3].title);
  await page.locator('#player-btn-loop').click();
  await expect.poll(() => fake.sonos.playMode).toBe('REPEAT_ALL');

  // Appending from the page extends the speaker queue in place.
  const before = fake.sonos.queue.length;
  await page.locator('.track-table-row').nth(1).locator('summary').click();
  await page.locator('.track-table-row').nth(1).locator('.queue-append-btn').click();
  await expect.poll(() => fake.sonos.queue.length).toBe(before + 1);
  expect(fake.sonos.track).toBe(4); // current track untouched

  // Back to the browser: the speaker pauses, local audio resumes the track.
  fake.sonos.state = 'PLAYING';
  await page.locator('#player-btn-cast').click();
  await page.locator('#cast-menu').getByRole('menuitemradio', { name: /本机/ }).click();
  await expect.poll(() => fake.sonos.state).toBe('PAUSED_PLAYBACK');
  await expect(page.locator('body')).not.toHaveClass(/is-casting/);
  await expect.poll(() => audio.evaluate((el) => el.getAttribute('src'))).toBe(queue[3].streamUrl);
});

test('a reloaded tab adopts the queue already playing on the speaker', async ({ page }) => {
  await login(page);
  // Seed the speaker queue through the API, as another client would.
  const ids = await page.evaluate(async () => {
    const res = await fetch('/api/v1/tracks?limit=3', { credentials: 'same-origin' });
    const body = await res.json();
    return (body.items || body.tracks || body).slice(0, 3).map((t) => t.id);
  });
  const res = await page.evaluate(async ([deviceIds]) => {
    const csrf = document.querySelector('meta[name="csrf-token"]').content;
    await fetch('/api/v1/cast/devices', { credentials: 'same-origin' });
    const r = await fetch(`/api/v1/cast/devices/${'RINCON_E2E000001400'}/queue`, {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
      body: JSON.stringify({ mode: 'replace', trackIds: deviceIds, startIndex: 1 })
    });
    return r.status;
  }, [ids]);
  expect(res).toBe(200);
  await expect.poll(() => fake.sonos.track).toBe(2);
  await page.evaluate((id) => sessionStorage.setItem('032_cast_device', JSON.stringify({ id, name: 'E2E Room', kind: 'sonos' })), UUID);
  await page.goto('/admin/albums');
  await expect(page.locator('body')).toHaveClass(/is-casting/);
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state') || '{"queue":[]}').queue.map((t) => Number(t.id)))).toEqual(ids.map(Number));
  await expect.poll(() => page.evaluate(() => JSON.parse(sessionStorage.getItem('032_player_state')).currentIndex)).toBe(1);
  expect(await page.locator('#global-audio-element').evaluate((el) => el.paused)).toBe(true);
});
