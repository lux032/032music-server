// Dedicated fake MB subprocess, owned and stopped by the discovery fixture.
import http from 'node:http';
http.createServer((req, res) => {
  if (req.url === '/health') { res.end(String(process.pid)); return; }
  setTimeout(() => {
    res.setHeader('Content-Type', 'application/json');
    res.end(JSON.stringify({ 'release-group-count': 1, 'release-groups': [{ id: '44444444-4444-4444-8444-444444444444', title: 'Refreshed Discovery', 'primary-type': 'Album', 'secondary-types': [], 'first-release-date': '2024-01-01', 'artist-credit': [{ name: 'E2E Artist', artist: { id: '33333333-3333-4333-8333-333333333333', name: 'E2E Artist' } }] }] }));
  }, 1800);
}).listen(45446, '127.0.0.1');
