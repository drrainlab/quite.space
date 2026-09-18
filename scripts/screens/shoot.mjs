// Headless Chrome over CDP, no dependencies (Node 22 has WebSocket + fetch).
import { spawn } from 'node:child_process';
import { writeFileSync } from 'node:fs';
const OUT = process.env.HOME + '/.quiet-stand/shots/';
const URL0 = 'http://127.0.0.1:8501/?token=fatok';
const chrome = spawn('/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', [
  '--headless=new', '--remote-debugging-port=9377', '--hide-scrollbars', '--mute-audio',
  '--user-data-dir=' + process.env.HOME + '/.quiet-stand/chrome-profile',
  '--enable-unsafe-swiftshader', '--use-angle=swiftshader', 'about:blank'], { stdio: 'ignore' });
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
let ws, id = 0; const pending = new Map();
async function connect() {
  for (let i = 0; i < 40; i++) {
    try {
      const list = await (await fetch('http://127.0.0.1:9377/json')).json();
      const page = list.find(t => t.type === 'page');
      if (page) { ws = new WebSocket(page.webSocketDebuggerUrl); break; }
    } catch (_) {}
    await sleep(250);
  }
  await new Promise(r => ws.addEventListener('open', r));
  ws.addEventListener('message', (m) => {
    const d = JSON.parse(m.data);
    if (d.id && pending.has(d.id)) { pending.get(d.id)(d.result || d.error); pending.delete(d.id); }
  });
}
const send = (method, params = {}) => new Promise(r => { const i = ++id; pending.set(i, r); ws.send(JSON.stringify({ id: i, method, params })); });
const js = async (expr) => (await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true })).result?.value;
async function open({ w, h, dpr = 2, mobile = false, theme = 'dark' }) {
  await send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: dpr, mobile });
  await send('Page.navigate', { url: URL0 });
  await sleep(1500);
  await js(`localStorage.setItem('qp.theme','${theme}'); localStorage.setItem('qp.lang','en'); localStorage.setItem('qp.preset','quiet-glass'); 1`);
  await send('Page.navigate', { url: URL0 });
  await sleep(4500);
  await js(`document.querySelectorAll('dialog[open]').forEach(d => d.close()); 1`);
}
const space = (name) => js(`(async()=>{const s=[...document.querySelectorAll('#nav .space')].find(x=>x.querySelector('.t')?.textContent.includes(${JSON.stringify(name)})); if(!s) return 'no '+${JSON.stringify(name)}; s.click(); await new Promise(r=>setTimeout(r,3000)); return 'ok'})()`);
async function shot(name) {
  const r = await send('Page.captureScreenshot', { format: 'png' });
  writeFileSync(OUT + name + '.png', Buffer.from(r.data, 'base64'));
  console.log('shot', name);
}
await connect();
await send('Page.enable'); await send('Runtime.enable');

// 1. conversation, dark
await open({ w: 1360, h: 850 });
console.log(await space('Maya'));
await js(`(async()=>{const l=document.getElementById('log'); l.scrollTop=0; await new Promise(r=>setTimeout(r,400)); const m=[...l.querySelectorAll('.msg')].find(x=>x.textContent.includes('Длинное')); if(m) m.classList.add('acting'); return 1})()`);
await sleep(600); await shot('conversation-dark');

// 2. posts feed (Field — demo), dark
console.log(await space('Field'));
await js(`switchView('posts'); 1`); await sleep(5000); await shot('posts-dark');

// 3. an article under an atmosphere
await js(`(async()=>{const c=[...document.querySelectorAll('#pubFeed .pub-card')].find(x=>/North route/.test(x.textContent))||document.querySelector('#pubFeed .pub-card'); if(c) c.click(); return !!c})()`);
await sleep(9000); await shot('article-atmosphere');

// 4. the field map
await js(`(async()=>{ if (typeof closeArticle==='function') closeArticle(); 1})()`);
await open({ w: 1360, h: 850 });
console.log(await space('Field'));
await js(`switchView('field'); 1`); await sleep(7000); await shot('field-dark');

// 5. conversation, light
await open({ w: 1360, h: 850, theme: 'light' });
console.log(await space('Maya')); await sleep(800); await shot('conversation-light');

// 6. the profile sheet
await js(`showIdentity(); 1`); await sleep(1200); await shot('sheet-light');

// 7. phone
await open({ w: 390, h: 844, dpr: 3, mobile: true });
await js(`(async()=>{ if (typeof togglePanel==='function' && document.body.classList.contains('fold-nav')) {} return 1})()`);
console.log(await js(`(async()=>{const s=[...document.querySelectorAll('#nav .space')].find(x=>x.querySelector('.t')?.textContent.includes('Maya')); if(!s) return 'no'; s.click(); await new Promise(r=>setTimeout(r,3000)); return 'ok'})()`));
await shot('phone-dark');
ws.close(); chrome.kill();
