// Dedicated MB and Bangumi fake, owned and stopped by the discovery fixture.
import http from 'node:http';
const names = ['Discovery Work OST', 'A / B', 'Discovery Auto', 'Fresh A / B'];
const group = (id, title) => ({ id, title, score: 100, 'primary-type': 'Album', 'secondary-types': [], 'first-release-date': '2024-01-01', 'artist-credit': [{ name: 'E2E Artist', artist: { id: '33333333-3333-4333-8333-333333333333', name: 'E2E Artist' } }] });
http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (url.pathname === '/health') { res.end(String(process.pid)); return; }
  const send = data => { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(data)); };
  if (url.pathname.endsWith('/subjects/900001/subjects')) { send(names.map((name, i) => ({ id: 900010 + i, type: 3, name, relation: ['原声集', '片头曲', '其他', '片头曲'][i] }))); return; }
  if (/\/v0\/subjects\/\d+$/.test(url.pathname)) { const id = Number(url.pathname.split('/').at(-1)); send({ id, type: 3, name: names[id - 900010] || '发现测试作品', name_cn: '测试音乐条目', date: '2024-01-01', infobox: [{ key: '艺术家', value: 'E2E Artist' }] }); return; }
  if (url.searchParams.has('query')) {
    const query = url.searchParams.get('query').replace(/\\(.)/g, '$1');
    const fresh = query.includes('Fresh A / B');
    const multi = query.includes('A / B');
    const title = fresh ? 'Fresh A / B' : multi ? 'A / B' : names[0];
    const suffix = fresh ? '000000000004' : multi ? '000000000002' : '000000000001';
    send({ 'release-group-count': 1, 'release-groups': [group('55555555-5555-4555-8555-' + suffix, title)] }); return;
  }
  setTimeout(() => send({ 'release-group-count': 1, 'release-groups': [group('44444444-4444-4444-8444-444444444444', 'Refreshed Discovery')] }), 1800);
}).listen(45446, '127.0.0.1');
