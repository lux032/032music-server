// Isolated S2 protocol fixture: never contacts a real tracker or downloader.
import http from 'node:http';
import { createHash } from 'node:crypto';
const info = Buffer.from('d6:lengthi4e4:name7:e2e.bin12:piece lengthi16384e6:pieces20:01234567890123456789e');
const torrent = Buffer.concat([Buffer.from('d4:info'), info, Buffer.from('e')]);
const hash = '30128d2e52b955e8cd9f89c7dd7cddff43d7d639';
if (createHash('sha1').update(info).digest('hex') !== hash) throw new Error('fixture info hash changed');
let scenario = 'normal', torrents = [], adds = 0;
const xml = text => text.replaceAll('&', '&amp;').replaceAll('<', '&lt;');
http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://127.0.0.1:45447');
  const send = (body, type = 'text/plain', status = 200) => { res.writeHead(status, { 'Content-Type': type }); res.end(body); };
  if (url.pathname === '/health') return send(String(process.pid));
  if (url.pathname === '/control') {
    let body = ''; for await (const chunk of req) body += chunk;
    const control = body ? JSON.parse(body) : {};
    scenario = control.scenario || url.searchParams.get('scenario') || 'normal';
    if (control.reset) { torrents = []; adds = 0; }
    if (scenario === 'complete') torrents.forEach(t => Object.assign(t, { progress: 1, amount_left: 0, state: 'uploading', completion_on: Math.floor(Date.now() / 1000) }));
    return send(JSON.stringify({ scenario, hash, adds }), 'application/json');
  }
  if (url.pathname.includes('/torznab/api')) {
    if (url.searchParams.get('apikey') !== 'e2e-key') return send('<error code="100" description="invalid key"/>', 'application/xml');
    if (scenario === 'error') return send('<error code="100" description="secret must not reach UI"/>', 'application/xml');
    if (scenario === 'cooldown') { res.setHeader('Retry-After', '3'); return send('<error code="500" description="limited"/>', 'application/xml'); }
    const item = (title, seeders, magnet = false) => `<item><title>${xml(title)}</title><guid>${seeders}</guid><link>${xml(magnet ? 'magnet:?xt=urn:btih:' + hash : 'http://127.0.0.1:45447/dl/jpopsuki/?jackett_apikey=e2e-key')}</link><size>104857600</size><category>3000</category><pubDate>Mon, 01 Jun 2026 00:00:00 GMT</pubDate><torznab:attr name="seeders" value="${seeders}"/><torznab:attr name="peers" value="2"/><torznab:attr name="grabs" value="9"/><torznab:attr name="downloadvolumefactor" value="0"/></item>`;
    return send(`<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>${scenario === 'empty' ? '' : scenario === 'magnet-only' ? item('Magnet only FLAC', 30, true) : item('E2E Album MP3 320', 2) + item('E2E Album FLAC 24bit Log Cue', 20)}</channel></rss>`, 'application/xml');
  }
  if (url.pathname.startsWith('/dl/')) return url.searchParams.get('jackett_apikey') === 'e2e-key' ? send(torrent, 'application/x-bittorrent') : send('Forbidden', 'text/plain', 403);
  if (url.pathname === '/api/v2/auth/login') {
    let body = ''; for await (const chunk of req) body += chunk;
    const form = new URLSearchParams(body);
    if (form.get('username') !== 'e2e' || form.get('password') !== 'e2e-password') return send('Fails.');
    res.setHeader('Set-Cookie', 'SID=e2e-session; Path=/'); return send('Ok.');
  }
  if (url.pathname.startsWith('/api/v2/') && !/(?:^|;\s*)SID=e2e-session(?:;|$)/.test(req.headers.cookie || '')) return send('Forbidden', 'text/plain', 403);
  if (url.pathname === '/api/v2/app/version') return send('v4.6.0');
  if (url.pathname === '/api/v2/torrents/categories') return send('{"music":{"name":"music","savePath":"/music"}}', 'application/json');
  if (url.pathname === '/api/v2/torrents/add') {
    let body = ''; for await (const chunk of req) body += chunk.toString('latin1');
    if (!body.includes('name="torrents"')) return send('Fails.', 'text/plain', 400);
    adds++;
    const tags = /name="tags"\r\n\r\n([^\r]*)/.exec(body)?.[1] || '';
    if (!torrents.length) torrents.push({ hash, infohash_v1: hash, tags, progress: 0.3, amount_left: 70, size: 100, state: 'downloading', completion_on: 0 });
    if (scenario === 'add-unknown') { req.socket.destroy(); return; }
    if (scenario === 'add-timeout') { setTimeout(() => { if (!res.destroyed) send('Ok.'); }, 35000); return; }
    return send(scenario === 'hash-conflict' ? 'Fails.' : 'Ok.');
  }
  if (url.pathname === '/api/v2/torrents/info') return send(JSON.stringify(torrents.filter(t => !url.searchParams.get('hashes') || url.searchParams.get('hashes').split('|').includes(t.hash))), 'application/json');
  if (url.pathname === '/api/v2/torrents/addTags') {
    let body = ''; for await (const chunk of req) body += chunk;
    const form = new URLSearchParams(body); torrents.forEach(t => { t.tags = form.get('tags') || ''; }); return send('');
  }
  send('not found', 'text/plain', 404);
}).listen(45447, '127.0.0.1');
