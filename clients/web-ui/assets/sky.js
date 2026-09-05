// SKY (SK-1) — a shared drawing that is a message.
//
// The card lives in the chat like any other moment; opening it shows a
// dark square where everybody's strokes glow in their own hue — the
// colour IS the person (glyphColor of the principal), so who drew what
// is visible without a legend. Every gesture becomes one small event on
// pen-up; nothing is sent while the finger moves, and nobody is told
// "5 drawing now" — the picture updating is the only presence there is.
// "Watch how it was drawn" replays the log in order: a canvas with
// memory is the whole reimagining.

const SKY = (() => {
  let open = null; // { spaceId, skyId, strokes, timer, playing }
  const GRID = 128;

  function card(e) {
    const wrap = document.createElement('div');
    wrap.className = 'skycard';
    const s = e.sky || { strokes: 0, hands: 0 };
    const title = (e.sky && e.sky.title) || t('sky.title');
    wrap.innerHTML =
      `<div class="sky-preview"><canvas width="160" height="160"></canvas></div>` +
      `<div class="sky-main"><div class="sky-t">✦ ${esc(title)}</div>` +
      `<div class="sky-m">${esc(t('sky.card.meta', { strokes: s.strokes, hands: s.hands }))}</div></div>` +
      `<button type="button" class="btn-tinted sky-open">${esc(t('sky.open'))}</button>`;
    wrap.querySelector('.sky-open').onclick = (ev) => { ev.stopPropagation(); openSky(e.id, title); };
    // The preview draws itself from the live projection, quietly.
    const cv = wrap.querySelector('canvas');
    fetchSky(e.id).then(d => paint(cv, d.strokes || [], d.me, 1)).catch(() => {});
    return wrap;
  }

  async function fetchSky(skyId) {
    return api(`/api/spaces/${current}/sky/${skyId}`);
  }

  function hueOf(author) {
    return (typeof glyphColor === 'function') ? glyphColor(author) : '#c8b4ff';
  }

  function paint(cv, strokes, me, upto) {
    const ctx = cv.getContext('2d');
    const W = cv.width, H = cv.height;
    ctx.clearRect(0, 0, W, H);
    ctx.fillStyle = '#05060c';
    ctx.fillRect(0, 0, W, H);
    const n = upto === 1 ? strokes.length : Math.min(strokes.length, upto | 0);
    const sx = W / GRID, sy = H / GRID;
    ctx.lineCap = 'round'; ctx.lineJoin = 'round';
    for (let i = 0; i < n; i++) {
      const s = strokes[i];
      const col = hueOf(s.author);
      const glow = [0.35, 0.65, 1][(s.bright || 2) - 1];
      const pts = s.points || [];
      ctx.strokeStyle = col; ctx.fillStyle = col;
      ctx.globalAlpha = 0.25 + 0.5 * glow;
      ctx.shadowColor = col; ctx.shadowBlur = 6 * glow * (W / 160);
      ctx.lineWidth = Math.max(1, W / 160 * (1 + glow));
      if (pts.length >= 4) {
        ctx.beginPath();
        ctx.moveTo(pts[0] * sx + sx / 2, pts[1] * sy + sy / 2);
        for (let k = 2; k < pts.length; k += 2) ctx.lineTo(pts[k] * sx + sx / 2, pts[k + 1] * sy + sy / 2);
        ctx.stroke();
      }
      // Pixels of light at the points: the sketch reads as a constellation.
      for (let k = 0; k < pts.length; k += 2) {
        ctx.beginPath();
        ctx.arc(pts[k] * sx + sx / 2, pts[k + 1] * sy + sy / 2, Math.max(0.8, W / 160 * 0.9), 0, Math.PI * 2);
        ctx.fill();
      }
    }
    ctx.globalAlpha = 1; ctx.shadowBlur = 0;
  }

  function dlg() { return document.getElementById('dlgSky'); }

  async function openSky(skyId, title) {
    const d = dlg();
    if (!d) return;
    open = { spaceId: current, skyId, strokes: [], me: '', timer: null, playing: false, bright: 2 };
    d.querySelector('.sky-dlg-title').textContent = '✦ ' + title;
    d.querySelectorAll('.sky-bright').forEach(b => b.classList.toggle('sel', Number(b.dataset.b) === 2));
    d.showModal();
    wireCanvas();
    await refresh();
    open.timer = setInterval(refresh, 2000);
  }

  function closeSky() {
    if (open && open.timer) clearInterval(open.timer);
    open = null;
    const d = dlg();
    if (d && d.open) d.close();
  }

  async function refresh() {
    if (!open || open.playing) return;
    try {
      const data = await fetchSky(open.skyId);
      open.strokes = data.strokes || [];
      open.me = data.me || '';
      const d = dlg();
      d.querySelector('.sky-dlg-meta').textContent =
        t('sky.card.meta', { strokes: data.count || 0, hands: data.hands || 0 }) +
        (data.evicted ? ' · ' + t('sky.cooled') : '');
      paint(d.querySelector('canvas.sky-canvas'), open.strokes, open.me, 1);
    } catch (_) { /* the next tick draws the truth */ }
  }

  let wired = false;
  function wireCanvas() {
    if (wired) return;
    wired = true;
    const cv = dlg().querySelector('canvas.sky-canvas');
    let drawing = null;
    const toGrid = (ev) => {
      const r = cv.getBoundingClientRect();
      const x = Math.max(0, Math.min(GRID - 1, Math.floor((ev.clientX - r.left) / r.width * GRID)));
      const y = Math.max(0, Math.min(GRID - 1, Math.floor((ev.clientY - r.top) / r.height * GRID)));
      return [x, y];
    };
    cv.onpointerdown = (ev) => {
      if (!open || open.playing) return;
      cv.setPointerCapture(ev.pointerId);
      drawing = toGrid(ev);
    };
    cv.onpointermove = (ev) => {
      if (!drawing) return;
      const [x, y] = toGrid(ev);
      const n = drawing.length;
      if (drawing[n - 2] === x && drawing[n - 1] === y) return;
      if (n / 2 >= 256) return; // one gesture, one event: the client splits by lifting
      drawing.push(x, y);
      // Local echo: the stroke is drawn before it is sent, in my own hue.
      paint(cv, open.strokes.concat([{ author: open.me, points: drawing, bright: open.bright }]), open.me, 1);
    };
    const finish = async () => {
      if (!drawing) return;
      const pts = drawing; drawing = null;
      if (pts.length < 2) return;
      try {
        await api(`/api/spaces/${open.spaceId}/sky/${open.skyId}/strokes`,
          { method: 'POST', body: JSON.stringify({ points: pts, bright: open.bright }) });
      } catch (_) { /* refused: the next refresh shows the truth */ }
      refresh();
    };
    cv.onpointerup = finish;
    cv.onpointercancel = finish;
  }

  function setBright(b) {
    if (!open) return;
    open.bright = b;
    dlg().querySelectorAll('.sky-bright').forEach(x => x.classList.toggle('sel', Number(x.dataset.b) === b));
  }

  async function undo() {
    if (!open) return;
    const mine = open.strokes.filter(s => s.mine);
    if (!mine.length) return;
    const last = mine[mine.length - 1];
    try {
      await api(`/api/spaces/${open.spaceId}/sky/${open.skyId}/erase`,
        { method: 'POST', body: JSON.stringify({ ids: [last.id] }) });
    } catch (_) {}
    refresh();
  }

  // Watch how it was drawn: the strokes, in log order, one every beat.
  async function play() {
    if (!open || open.playing) return;
    open.playing = true;
    const cv = dlg().querySelector('canvas.sky-canvas');
    const all = open.strokes.slice();
    for (let i = 0; i <= all.length && open && open.playing; i++) {
      paint(cv, all, open.me, i === all.length ? 1 : i);
      await new Promise(r => setTimeout(r, Math.max(40, Math.min(140, 6000 / Math.max(1, all.length)))));
    }
    if (open) { open.playing = false; refresh(); }
  }

  async function start() {
    if (!current) return;
    try {
      await api(`/api/spaces/${current}/sky`, { method: 'POST', body: JSON.stringify({ title: '' }) });
      document.getElementById('attachMenu').style.display = 'none';
      if (typeof refreshSpace === 'function') refreshSpace();
    } catch (err) { alert(err.message); }
  }

  return { card, openSky, closeSky, setBright, undo, play, start };
})();

function renderSky(e) { return SKY.card(e); }
if (typeof window !== 'undefined') { window.SKY = SKY; window.renderSky = renderSky; }
