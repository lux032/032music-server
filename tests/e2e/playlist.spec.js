const { test, expect } = require('@playwright/test');
async function login(page) {
  await page.goto('/admin/login'); await page.getByLabel('用户名').fill('admin'); await page.getByLabel('密码').fill('e2e-admin-password'); await page.getByRole('button', { name: /登录/ }).click(); await page.waitForURL(/\/admin$/);
  await expect.poll(async () => Number(await page.locator('#tracks-count').textContent())).toBeGreaterThan(0);
}
async function api(page, path, method = 'GET', body) {
  return page.evaluate(async ({path,method,body}) => { const r = await fetch('/api/v1'+path, {method, headers: {'Content-Type':'application/json','X-CSRF-Token':document.querySelector('meta[name="csrf-token"]').content}, body:body ? JSON.stringify(body) : undefined}); return {status:r.status, body:await r.json().catch(()=>null)}; }, {path,method,body});
}
async function album(page) { await page.goto('/admin/albums'); await page.locator('a[href^="/admin/albums/"]').first().click(); await expect(page.locator('[data-playlist-album]')).toBeVisible(); }
async function createInPicker(page, name) { const d=page.locator('.playlist-picker'); await expect(d).toBeVisible(); await d.getByLabel('名称',{exact:true}).fill(name); await d.getByRole('button',{name:'创建并加入'}).click(); await expect(d).toHaveCount(0); }
async function target(page,name) { const d=page.locator('.playlist-picker'); await d.getByLabel('搜索歌单').fill(name); await d.locator('.playlist-picker-results button').filter({hasText:name}).click(); await expect(d).toHaveCount(0); }
async function playlist(page,name) { const result=await api(page,'/playlists?limit=500'); return result.body.items.find(p=>p.name===name); }
const image=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAHElEQVR4nGJhYDghx8CAiVhABDYwOCUAAQAA//+SmQKNqlOHfgAAAABJRU5ErkJggg==','base64');
test.beforeEach(async({page})=>login(page));
test('album whole/single/multi picker creates atomically and retains source, failures are retryable',async({page},info)=>{
 await album(page); const url=page.url(), name=`Album ${info.project.name} ${Date.now()}`, rows=page.locator('[data-track-list] [data-track-id]'), count=await rows.count();
 await page.locator('[data-playlist-album]').click(); await createInPicker(page,name); expect(page.url()).toBe(url); const p=await playlist(page,name); expect(p.itemCount).toBe(count);
 await rows.first().locator('[data-playlist-track]').dblclick(); await expect(page.locator('.playlist-picker')).toBeVisible(); expect(await page.locator('#global-audio-element').evaluate(a=>a.paused)).toBe(true); await target(page,name); expect((await playlist(page,name)).itemCount).toBe(count);
 await rows.nth(0).locator('.playlist-select').check(); await rows.nth(1).locator('.playlist-select').check(); await page.locator('[data-playlist-selected]').click(); const multi=`Multi ${info.project.name} ${Date.now()}`; await createInPicker(page,multi); expect((await playlist(page,multi)).itemCount).toBe(2);
 await page.locator('[data-playlist-album]').click(); await page.route('**/api/v1/playlists/*/items/append',r=>r.abort('failed')); const d=page.locator('.playlist-picker'); await d.getByLabel('搜索歌单').fill(name); await d.locator('.playlist-picker-results button').filter({hasText:name}).click(); await expect(d.locator('[data-picker-status]')).not.toHaveText('正在加入…'); await expect(d).toBeVisible(); await page.unroute('**/api/v1/playlists/*/items/append'); await target(page,name);
 await page.locator('[data-playlist-album]').click(); await page.keyboard.press('Escape'); await expect(page.locator('[data-playlist-album]')).toBeFocused();
});
test('playing queue snapshot/single saves preserve audio; artwork and safe editing',async({page},info)=>{
 await album(page); await page.locator('.album-hero .primary-round').click(); const audio=page.locator('#global-audio-element'); await expect.poll(()=>audio.evaluate(a=>a.paused)).toBe(false); await audio.evaluate(a=>{a.dataset.playlistMarker='persistent';});
 const state=()=>page.evaluate(()=>JSON.parse(sessionStorage.getItem('032_player_state'))); const before=await state();
 if(await page.locator('#player-btn-queue').getAttribute('aria-expanded') !== 'true') await page.locator('#player-btn-queue').click();
 await page.locator('.playlist-save-queue').click(); const name=`Queue ${info.project.name} ${Date.now()}`; await createInPicker(page,name); const p=await playlist(page,name); expect(p.itemCount).toBe(before.queue.length); expect((await state()).queue.map(t=>t.id)).toEqual(before.queue.map(t=>t.id)); await expect.poll(()=>audio.evaluate(a=>a.paused)).toBe(false);
 await page.locator('.q-playlist').first().click(); await target(page,name); expect((await playlist(page,name)).itemCount).toBe(p.itemCount);
 if(info.project.name!=='desktop-chromium') await page.locator('#player-btn-queue').click();
 await page.evaluate(id=>{const a=document.createElement('a');a.href='/admin/playlists/'+id;a.id='test-playlist-link';a.textContent='playlist';document.querySelector('main').append(a);},p.id); await page.locator('#test-playlist-link').click(); await expect(page.locator('[data-playlist-id]')).toBeVisible();
 expect(await audio.getAttribute('data-playlist-marker')).toBe('persistent'); const url=page.url(); await page.locator('[data-playlist-edit]').click(); const form=page.locator('[data-playlist-artwork]'); await form.locator('input[type=file]').setInputFiles({name:'cover.png',mimeType:'image/png',buffer:image}); await expect(page.locator('[data-artwork-preview]')).toBeVisible();
 let time=await audio.evaluate(a=>a.currentTime); await form.getByRole('button',{name:'上传封面',exact:true}).click(); await expect(form.locator('[data-artwork-status]')).toHaveText('封面已更新'); expect(page.url()).toBe(url); await expect.poll(()=>audio.evaluate(a=>a.paused)).toBe(false); await expect.poll(()=>audio.evaluate(a=>a.currentTime)).toBeGreaterThan(time); expect(await audio.getAttribute('data-playlist-marker')).toBe('persistent');
 time=await audio.evaluate(a=>a.currentTime); await form.getByRole('button',{name:'恢复自动封面'}).click(); await expect(form.locator('[data-artwork-status]')).toHaveText('已恢复自动封面'); await expect.poll(()=>audio.evaluate(a=>a.currentTime)).toBeGreaterThan(time); expect(page.url()).toBe(url);
 const rows=page.locator('.playlist-track-list [data-track-id]'); const first=await rows.first().getAttribute('data-track-id'); await rows.first().locator('.playlist-drag').focus(); await page.keyboard.press('Alt+ArrowDown'); await expect(rows.nth(1)).toHaveAttribute('data-track-id',first); await expect(rows.nth(1).locator('.playlist-drag')).toBeFocused();
 const current=await api(page,`/playlists/${p.id}`); await api(page,`/playlists/${p.id}/items/remove`,'POST',{trackIds:[Number(first)],expectedRevision:current.body.playlist.revision}); await rows.first().locator('[data-playlist-move][value=down]').click(); await expect(rows).toHaveCount(p.itemCount-1);
 await rows.nth(0).locator('.playlist-select').check(); await rows.nth(1).locator('.playlist-select').check(); await page.locator('[data-playlist-remove-selected]').click(); await expect(rows).toHaveCount(p.itemCount-3); await page.locator('.playlist-undo button').click(); await expect(rows).toHaveCount(p.itemCount-1);
 await page.locator('.playlist-search input').fill('Track'); await page.locator('.playlist-search button').click(); await expect(page.locator('.playlist-search input')).toHaveValue('Track'); await expect(page.locator('.candidate-track-list')).toContainText('已添加');
});
test('picker pagination, pending, PJAX rebind and touch handle order',async({page},info)=>{
 await album(page); const rows=page.locator('[data-track-list] [data-track-id]'), ids=await rows.evaluateAll(rows=>rows.slice(0,3).map(r=>Number(r.dataset.trackId)));
 const name=`Edit ${info.project.name} ${Date.now()}`, p=(await api(page,'/playlists','POST',{name,trackIds:ids})).body;
 // Simulate 501 targets to ensure the picker searches beyond the first page.
 await page.route('**/api/v1/playlists?limit=500&offset=*',async route=>{const offset=new URL(route.request().url()).searchParams.get('offset');const items=offset==='0'?Array.from({length:500},(_,i)=>({id:i+10000,name:'Other '+i,itemCount:0})):[p];await route.fulfill({json:{items,total:501}});});
 await rows.first().locator('[data-playlist-track]').click(); let requests=0;
 await page.route(`**/api/v1/playlists/${p.id}/items/append`,async route=>{requests++;await new Promise(r=>setTimeout(r,250));await route.continue();});
 const d=page.locator('.playlist-picker'); await d.getByLabel('搜索歌单').fill(name); const button=d.locator('.playlist-picker-results button'); await expect(button).toHaveCount(1); await button.evaluate(el=>{el.click();el.click();});await expect(d).toHaveCount(0);expect(requests).toBe(1);
 await page.unroute('**/api/v1/playlists?limit=500&offset=*'); await page.unroute(`**/api/v1/playlists/${p.id}/items/append`);
 await page.evaluate(id=>{const a=document.createElement('a');a.id='edit-target';a.href='/admin/playlists/'+id;a.textContent='edit';document.querySelector('main').append(a);},p.id);await page.locator('#edit-target').click();
 const list=page.locator('.playlist-track-list'), handle=list.locator('.playlist-drag').first();await list.evaluate(el=>window.scrollBy(0,el.getBoundingClientRect().top-120));const from=await handle.boundingBox(), to=await list.locator('article').nth(1).boundingBox();
 // Native touch events on mobile; mouse pointer on desktop.
 if(info.project.name==='desktop-chromium'){await page.mouse.move(from.x+from.width/2,from.y+from.height/2);await page.mouse.down();await page.mouse.move(from.x+from.width/2,to.y+to.height-2,{steps:5});await page.mouse.up();}
 else {const client=await page.context().newCDPSession(page);await client.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{x:from.x+from.width/2,y:from.y+from.height/2}]});await client.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{x:from.x+from.width/2,y:to.y+to.height-2}]});await client.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});await client.detach();}
 await expect(list.locator('article').nth(1)).toHaveAttribute('data-track-id',String(ids[0]));await expect.poll(async()=>(await api(page,`/playlists/${p.id}`)).body.tracks[1].id).toBe(ids[0]);
 await list.locator('[data-playlist-remove]').first().click();await expect(page.locator('.playlist-undo')).toBeVisible();await page.getByRole('link',{name:'专辑',exact:true}).first().click();await expect(page.locator('.playlist-undo')).toHaveCount(0);await page.goBack();await expect(page.locator('[data-playlist-id]')).toBeVisible();
 await page.locator('[data-playlist-track]').first().click();await expect(page.locator('.playlist-picker')).toHaveCount(1);await page.keyboard.press('Escape');
});
test('playlist candidate isolation, shuffle, queue append and stale navigation cleanup',async({page},info)=>{
 await album(page); const ids=await page.locator('[data-track-id]').evaluateAll(rows=>rows.slice(0,3).map(r=>Number(r.dataset.trackId))), name=`Playback ${info.project.name} ${Date.now()}`;
 const p=(await api(page,'/playlists','POST',{name,trackIds:ids})).body;
 await page.goto(`/admin/playlists/${p.id}?q=Track`); await expect(page.locator('.candidate-track-list')).toContainText('已添加'); await page.locator('[data-play-all]').click(); await expect.poll(()=>page.locator('#global-audio-element').evaluate(a=>a.paused)).toBe(false);
 const queue=()=>page.evaluate(()=>JSON.parse(sessionStorage.getItem('032_player_state')).queue.map(t=>Number(t.id)));
 expect(await queue()).toEqual(ids); await page.locator('[data-play-shuffle]').click();expect((await queue()).sort((a,b)=>a-b)).toEqual([...ids].sort((a,b)=>a-b));
 await page.locator('[data-queue-all]').click();expect((await queue()).length).toBe(6);
 await page.locator('[data-playlist-edit]').click(); await page.locator('[data-playlist-artwork] input[type=file]').setInputFiles({name:'cover.png',mimeType:'image/png',buffer:image});
 let uploads=0; await page.route(`**/api/v1/playlists/${p.id}/artwork`,async route=>{uploads++;await new Promise(r=>setTimeout(r,500));await route.continue().catch(()=>{});});
 await page.locator('[data-playlist-artwork] button').first().evaluate(el=>{el.click();el.click();});await page.getByRole('link',{name:'专辑',exact:true}).first().click();await expect(page).toHaveURL(/\/admin\/albums$/);await page.waitForTimeout(650);expect(uploads).toBe(1);await expect(page.locator('[data-playlist-cover]')).toHaveCount(0);
});
test('artwork refresh keeps revision/order snapshot and failed drag preserves disabled candidates',async({page},info)=>{
 await album(page); const ids=await page.locator('[data-track-id]').evaluateAll(rows=>rows.slice(0,3).map(r=>Number(r.dataset.trackId))); const p=(await api(page,'/playlists','POST',{name:`CAS ${info.project.name}`,trackIds:ids})).body;
 await page.goto(`/admin/playlists/${p.id}?q=Track`);
 for(const reset of [false,true]) {
  const current=(await api(page,`/playlists/${p.id}`)).body; const ordered=current.tracks.map(t=>t.id).reverse();
  await api(page,`/playlists/${p.id}/order`,'PUT',{trackIds:ordered,expectedRevision:current.playlist.revision});
  await page.locator('[data-playlist-edit]').click();const form=page.locator('[data-playlist-artwork]');
  if(reset) await form.locator('[data-playlist-artwork-reset]').click();else {await form.locator('input[type=file]').setInputFiles({name:'cover.png',mimeType:'image/png',buffer:image});await form.getByRole('button',{name:'上传封面',exact:true}).click();}
  await expect(form.locator('[data-artwork-status]')).toHaveText(reset?'已恢复自动封面':'封面已更新');
  const list=page.locator('.playlist-track-list');expect(await list.locator('article').evaluateAll(rows=>rows.map(r=>Number(r.dataset.trackId)))).toEqual(ordered);
  const snapshot=(await api(page,`/playlists/${p.id}`)).body;await expect(page.locator('[data-playlist-id]')).toHaveAttribute('data-playlist-revision',String(snapshot.playlist.revision));
  await list.locator('[data-playlist-move][value=down]').first().click();const next=[ordered[1],ordered[0],ordered[2]];await expect.poll(async()=>(await api(page,`/playlists/${p.id}`)).body.tracks.map(t=>t.id)).toEqual(next);
 }
 const list=page.locator('.playlist-track-list');const before=await list.locator('article').evaluateAll(rows=>rows.map(r=>Number(r.dataset.trackId)));
 await page.route(`**/api/v1/playlists/${p.id}/order`,route=>route.abort('failed'));
 await list.evaluate(el=>window.scrollBy(0,el.getBoundingClientRect().top-100));const from=await list.locator('.playlist-drag').first().boundingBox(),to=await list.locator('article').nth(1).boundingBox();
 await page.mouse.move(from.x+10,from.y+10);await page.mouse.down();await page.mouse.move(from.x+10,to.y+to.height-2,{steps:4});await page.mouse.up();
 await expect.poll(()=>list.locator('article').evaluateAll(rows=>rows.map(r=>Number(r.dataset.trackId)))).toEqual(before);
 await expect(page.locator('.candidate-track-list button:disabled').first()).toBeDisabled();
 await page.unroute(`**/api/v1/playlists/${p.id}/order`);await list.locator('[data-playlist-move][value=down]').first().click();await expect.poll(async()=>(await api(page,`/playlists/${p.id}`)).body.tracks.map(t=>t.id)).toEqual([before[1],before[0],before[2]]);
});
test('album and library playlist actions fit existing row geometry',async({page})=>{
 for(const source of ['album','library']) {
  if(source==='album')await album(page);else await page.goto('/admin/tracks');
  const geometry=await page.locator('[data-track-list] [data-track-id]').first().evaluate(row=>{const b=row.querySelector('[data-playlist-track]'),r=row.getBoundingClientRect(),a=b.getBoundingClientRect();return {children:row.children.length,inside:a.top>=r.top&&a.bottom<=r.bottom&&a.left>=r.left&&a.right<=r.right,width:a.right<=r.right&&row.scrollWidth<=Math.ceil(r.width),height:r.height,cell:b.parentElement.getBoundingClientRect().height};});
  expect(geometry.inside).toBe(true);expect(geometry.width,source+JSON.stringify(geometry)).toBe(true);expect(geometry.height).toBeLessThan(120);expect(geometry.cell).toBeLessThan(100);
 }
});

test('merged wide shell retains six playlist cells and reachable editor',async({page})=>{
 await album(page);const ids=await page.locator('[data-track-id]').evaluateAll(rows=>rows.slice(0,3).map(r=>Number(r.dataset.trackId)));const p=(await api(page,'/playlists','POST',{name:'Merged geometry',trackIds:ids})).body;
 for(const width of [1280,1920,3840]){await page.setViewportSize({width,height:900});await page.goto(`/admin/playlists/${p.id}`);await expect(page.locator('.playlist-select')).toHaveCount(3);await page.locator('[data-playlist-edit]').click();
 const values=await page.locator('.playlist-track-list article').first().evaluate(row=>{const rect=row.getBoundingClientRect();return {columns:getComputedStyle(row).gridTemplateColumns.split(' ').length,inside:[...row.children].every(el=>{const r=el.getBoundingClientRect();return r.top>=rect.top&&r.bottom<=rect.bottom+1&&r.right<=rect.right+1;})};});expect(values.columns).toBe(6);expect(values.inside).toBe(true);await expect(page.locator('[data-playlist-artwork] button').first()).toBeVisible();}
});
