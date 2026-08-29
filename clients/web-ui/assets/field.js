// The Field view (SP-3, ADR-031): a LOCAL PROJECTION OF SIGNED CLAIMS —
// people by their last known position and the age of that knowing,
// markers as historical claims, places and routes as geo-bearing objects.
// No basemap tiles in this wave: the canvas is the operational layer
// itself, and it must never pretend to more than the space knows.
//
// Rendering discipline: redraw on data/interaction only — NO permanent
// rAF loop (the hardware floor is a phone that might be someone's only
// radio). Resize/DPR handling follows stage.js's hard-won rules; the
// backing-pixel cap is deliberately NOT copied — a map has edges and
// text, and blurry markers read as broken.

const FIELD = {
  data: null,          // last /field bundle
  canvas: null,
  ctx: null,
  tiles: new Map(),    // "z/x/y" → basemap tile entry (LRU, decoded)
  rafPending: false,   // one-shot redraw latch — never a running loop
  // The viewport is ABSOLUTE (SP-3.1): a place in the world and a zoom
  // level, not an offset over whatever the data happened to span. The
  // old fit-relative form re-derived itself from the bbox on every 10 s
  // poll, so an arriving marker moved the map under the person's hand.
  // null = "not placed yet" — the next draw fits to content once.
  view: null,          // {lat, lon, z} — z is continuous, tile-style
  proj: null,          // computed each draw from FIELD.view
  timer: null,         // poll while the view is open
  shareTimer: null,    // position emission loop
  placing: false,      // "tap the map to place it"
  placingWhat: 'marker', // what the next tap creates: 'marker' | 'place'
  resizeObs: null,
  // SP-3.2 — sweeps. `sweeps` is the space's completed list (event wins);
  // `actives` is THIS node's live sessions (GET /api/sweeps); `tracks`
  // caches decoded track assets by hex — 'pending'/'failed' are states,
  // so a broken asset is asked for once, not on every frame.
  sweeps: null,
  actives: null,
  tracks: new Map(),
};

const FIELD_MARKER_GLYPHS = {
  waypoint: '📍', searched: '🔎', hazard: '⚠️', casualty: '🆘',
  base: '🏕️', extraction: '🚑', relay: '📡', seen: '👤', other: '✦',
};
const FIELD_MARKER_KINDS = Object.keys(FIELD_MARKER_GLYPHS);

function fieldShareKey() { return 'qp.field.share.' + current; }
function fieldCadenceKey() { return 'qp.field.cadence.' + current; }

// ---- data + lifecycle ----

async function refreshField() {
  if (!current) return;
  const box = document.getElementById('field');
  if (!box.dataset.built) fieldBuild(box);
  try {
    FIELD.data = await api(`/api/spaces/${current}/field`);
  } catch (e) { console.error(e); return; }
  // SP-3.2: completed sweeps (the reducer's facts, event wins) and this
  // node's live sessions. Both are allowed to fail without taking the map
  // down — an old node simply has no sweeps.
  try { FIELD.sweeps = (await api(`/api/spaces/${current}/sweeps`)).sweeps || []; }
  catch { FIELD.sweeps = FIELD.sweeps || []; }
  try { FIELD.actives = (await api('/api/sweeps')).sweeps || []; }
  catch { FIELD.actives = FIELD.actives || []; }
  fieldSweepBannerSync();
  fieldDrawAll();
}

// fieldTeardown stops everything the view owns — called by switchView on
// the way out (the ATMO.unmount discipline: a torn-off canvas keeps its
// timers unless asked not to).
function fieldTeardown() {
  if (FIELD.timer) { clearInterval(FIELD.timer); FIELD.timer = null; }
  fieldStopSharing(false);
  FIELD.placing = false;
  // The viewport belongs to the space being looked at: leaving drops it
  // so the next space fits to its OWN claims rather than opening on
  // somebody else's valley.
  FIELD.view = null;
}

function fieldOnEnter() {
  if (!FIELD.timer) FIELD.timer = setInterval(() => {
    if (typeof pubView !== 'undefined' && pubView === 'field') refreshField();
  }, 10_000);
  if (localStorage.getItem(fieldShareKey()) === '1') fieldStartSharing();
}

// ---- the screen ----

function fieldBuild(box) {
  box.dataset.built = '1';
  box.innerHTML = '';

  const bar = document.createElement('div');
  bar.className = 'field-bar';
  // The share toggle SAYS WHAT IT DOES, literally — the label is the
  // scope contract (ADR-031): sharing happens while the map is open,
  // and nothing else is promised.
  const share = document.createElement('label');
  share.className = 'field-share';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.checked = localStorage.getItem(fieldShareKey()) === '1';
  cb.onchange = () => {
    localStorage.setItem(fieldShareKey(), cb.checked ? '1' : '0');
    if (cb.checked) fieldStartSharing(); else fieldStopSharing(true);
  };
  share.appendChild(cb);
  const shareTxt = document.createElement('span');
  shareTxt.textContent = t('field.share_label');
  share.appendChild(shareTxt);
  const ind = document.createElement('span');
  ind.id = 'fieldShareInd';
  ind.className = 'field-share-ind';
  share.appendChild(ind);
  bar.appendChild(share);

  // The basemap toggle, and its label is the disclosure (ADR-032): a
  // tile request tells a server which square of the world is on this
  // screen, so the switch says exactly that and starts OFF.
  const base = document.createElement('label');
  base.className = 'field-share';
  const bcb = document.createElement('input');
  bcb.type = 'checkbox';
  bcb.checked = fieldBasemapOn();
  bcb.onchange = async () => {
    if (bcb.checked && !localStorage.getItem(FIELD_BASEMAP_ASKED)) {
      let server = '';
      try { server = (await api('/api/settings')).tiles.server; } catch { /* name it generically */ }
      const ok = confirm(t('field.basemap_consent', { server: server || 'the tile server' }));
      if (!ok) { bcb.checked = false; return; }
      localStorage.setItem(FIELD_BASEMAP_ASKED, '1');
    }
    localStorage.setItem(fieldBasemapKey(), bcb.checked ? '1' : '0');
    if (!bcb.checked) FIELD.tiles.clear();
    fieldScheduleDraw();
  };
  base.appendChild(bcb);
  const baseTxt = document.createElement('span');
  baseTxt.textContent = t('field.basemap_label');
  base.appendChild(baseTxt);
  bar.appendChild(base);

  // Fit is now an ACT, not a side effect of new data arriving: the map
  // holds still under the hand and re-frames when asked.
  const fitBtn = document.createElement('button');
  fitBtn.className = 'btn-plain';
  fitBtn.textContent = '⌖ ' + t('field.fit');
  fitBtn.onclick = () => { FIELD.view = null; fieldScheduleDraw(); };
  bar.appendChild(fitBtn);

  // SP-3.2 follow-up: the door into sweeping was invisible. A sweep
  // starts on a geo-bearing object, and until this button the only way
  // to CREATE one was the HTTP API — the editor deliberately has no
  // coordinate fields (a typed number is a different claim than a finger
  // on the map, the marker dialog's own law). So a place is born here,
  // like a marker: arm, tap, name it.
  const placeBtn = document.createElement('button');
  placeBtn.className = 'btn-plain';
  placeBtn.textContent = '⊕ ' + t('field.new_place');
  placeBtn.onclick = () => fieldArmPlace();
  bar.appendChild(placeBtn);

  const spread = document.createElement('span');
  spread.style.flex = '1';
  bar.appendChild(spread);

  // Placing a marker is this view's CREATION act, so it lives in the
  // header's primary slot beside "+ post" and "+ object" (spacenav.js),
  // not duplicated here. What stays in this toolbar is what belongs to
  // being in the field: sharing, the map's own background, and the two
  // things a person says about themselves.
  // While the map is armed, say so where the eyes already are.
  const hint = document.createElement('span');
  hint.id = 'fieldPlacingHint';
  hint.className = 'field-placing';
  hint.style.display = 'none';
  hint.textContent = t('field.placing_hint');
  bar.appendChild(hint);

  const ok = document.createElement('button');
  ok.className = 'btn-tinted';
  ok.textContent = '✓ ' + t('field.im_ok');
  ok.onclick = () => fieldCheckin(false);
  bar.appendChild(ok);

  const sos = document.createElement('button');
  sos.className = 'field-sos';
  sos.textContent = '🆘 SOS';
  sos.onclick = () => {
    if (confirm(t('field.sos_confirm'))) fieldCheckin(true);
  };
  bar.appendChild(sos);
  box.appendChild(bar);

  // SP-3.2 — the active-sweep banner: the in-app half of the session's
  // visibility (the notification is the lock-screen half). Hidden until
  // a live session exists on this node.
  const sweepBan = document.createElement('div');
  sweepBan.id = 'fieldSweepBanner';
  sweepBan.className = 'field-sweep-banner';
  sweepBan.style.display = 'none';
  box.appendChild(sweepBan);

  // The canvas.
  const wrap = document.createElement('div');
  wrap.className = 'field-map';
  const cv = document.createElement('canvas');
  wrap.appendChild(cv);
  box.appendChild(wrap);
  FIELD.canvas = cv;
  FIELD.ctx = cv.getContext('2d');
  fieldWireCanvas(cv, wrap);

  // People roster with the honesty ladder + overdue arithmetic.
  const roster = document.createElement('div');
  roster.id = 'fieldPeople';
  roster.className = 'field-people';
  box.appendChild(roster);

  // The check-in cadence is the VIEWER's convention (localStorage) —
  // overdue is arithmetic the viewer runs, never a network alarm.
  const cadRow = document.createElement('div');
  cadRow.className = 'field-cadence';
  const cadLbl = document.createElement('span');
  cadLbl.textContent = t('field.cadence_label');
  cadRow.appendChild(cadLbl);
  const cad = document.createElement('select');
  for (const [v, label] of [['0', t('field.cadence_off')], ['3600', '1h'], ['14400', '4h'], ['28800', '8h']]) {
    const o = document.createElement('option');
    o.value = v; o.textContent = label;
    cad.appendChild(o);
  }
  cad.value = localStorage.getItem(fieldCadenceKey()) || '0';
  cad.onchange = () => { localStorage.setItem(fieldCadenceKey(), cad.value); fieldDrawAll(); };
  cadRow.appendChild(cad);
  box.appendChild(cadRow);
}

// ---- projection ----

// SPHERICAL WEB MERCATOR, the projection raster tiles are cut in (SP-3.1).
// The world is the unit square: at zoom z it is S = 256·2^z CSS px across,
// tile (x,y) at that zoom covers 1/2^z of it. Drawing tiles under any
// other projection smears them, so the map adopts theirs rather than
// asking them to adopt ours.
//
//   mercX(lon) = (lon + 180) / 360
//   mercY(lat) = 0.5 − ln(tan(π/4 + lat/2)) / 2π      [lat clamped ±85.05]
//
// The returned closure keeps the same four functions every layer already
// calls — x, y, invert, metersToPx — so nothing downstream changes.
// metersToPx becomes latitude-honest here, which the accuracy rings and
// the metre grid quietly wanted all along.
const MERC_LAT_MAX = 85.05112878;
const EARTH_CIRC_M = 40075016.686;

function mercX(lon) { return (lon + 180) / 360; }
function mercY(lat) {
  const l = Math.max(-MERC_LAT_MAX, Math.min(MERC_LAT_MAX, lat)) * Math.PI / 180;
  return 0.5 - Math.log(Math.tan(Math.PI / 4 + l / 2)) / (2 * Math.PI);
}
function mercLon(mx) { return mx * 360 - 180; }
function mercLat(my) {
  return (2 * Math.atan(Math.exp((0.5 - my) * 2 * Math.PI)) - Math.PI / 2) * 180 / Math.PI;
}

// fieldContentPoints: every claim that has a place. Used only to FIT.
function fieldContentPoints() {
  const pts = [];
  const d = FIELD.data || {};
  for (const p of d.people || []) {
    if (p.position && p.position.state !== 'unknown') pts.push([p.position.lat, p.position.lon]);
  }
  for (const m of d.markers || []) pts.push([m.lat, m.lon]);
  for (const o of d.objects || []) {
    if (o.geo) pts.push([o.geo.lat, o.geo.lon]);
    for (const pp of o.path || []) pts.push(pp);
  }
  return pts;
}

// fieldFitView derives {lat, lon, z} from the claims on hand. It runs
// when the view has not been placed yet and when the person presses
// "fit" — never on a poll, which is what used to move the map.
function fieldFitView(w, h) {
  const pts = fieldContentPoints();
  if (!pts.length) return null;
  let minLat = 90, maxLat = -90, minLon = 180, maxLon = -180;
  for (const [la, lo] of pts) {
    if (la < minLat) minLat = la;
    if (la > maxLat) maxLat = la;
    if (lo < minLon) minLon = lo;
    if (lo > maxLon) maxLon = lo;
  }
  const lat0 = (minLat + maxLat) / 2;
  // A lone point is a place, not a bbox: open a ~600 m window around it
  // rather than zooming to a degenerate span where any accuracy circle
  // swallows the viewport (found by the owner's first solo share).
  const minSpanMerc = 600 / (EARTH_CIRC_M * Math.cos(lat0 * Math.PI / 180));
  const spanX = Math.max(mercX(maxLon) - mercX(minLon), minSpanMerc);
  const spanY = Math.max(mercY(minLat) - mercY(maxLat), minSpanMerc);
  const S = 0.82 * Math.min(w / spanX, h / spanY); // fit with breathing room
  const z = Math.max(1, Math.min(19, Math.log2(S / 256)));
  return {
    lat: mercLat((mercY(minLat) + mercY(maxLat)) / 2),
    lon: (minLon + maxLon) / 2,
    z,
  };
}

function fieldProjection(w, h) {
  if (!FIELD.view) FIELD.view = fieldFitView(w, h);
  if (!FIELD.view) return null;
  const v = FIELD.view;
  const S = 256 * Math.pow(2, v.z);
  const cmx = mercX(v.lon), cmy = mercY(v.lat);
  return {
    scale: S,
    x: (lon) => w / 2 + (mercX(lon) - cmx) * S,
    y: (lat) => h / 2 + (mercY(lat) - cmy) * S,
    invert: (px, py) => [
      mercLat(cmy + (py - h / 2) / S),
      mercLon(cmx + (px - w / 2) / S),
    ],
    metersToPx: (m, lat) =>
      m * S / (EARTH_CIRC_M * Math.cos((lat === undefined ? v.lat : lat) * Math.PI / 180)),
  };
}

// ---- the basemap (SP-3.1, ADR-032) ----
//
// A COURTESY, never a dependency: the map of claims is complete without
// it, so every failure here degrades to paper rather than to a spinner.
// The node is the only source (the page's CSP forbids talking to a tile
// server directly), and it is also where consent and the connectivity
// gate live — this file just draws what arrives.

function fieldBasemapKey() { return 'qp.field.basemap.' + current; }
const FIELD_BASEMAP_ASKED = 'qp.field.basemap.asked';
const FIELD_TILE_CAP = 160;      // decoded tiles held in memory
const FIELD_TILE_RETRY_MS = 60_000;

function fieldBasemapOn() {
  return localStorage.getItem(fieldBasemapKey()) === '1';
}

// fieldTile returns a cache entry, starting a load at most once per tile
// and remembering failures so an offline pan does not retry every frame.
function fieldTile(z, x, y) {
  const key = z + '/' + x + '/' + y;
  const cache = FIELD.tiles;
  const hit = cache.get(key);
  if (hit) {
    if (hit.err && Date.now() < hit.retryAt) return hit;
    if (!hit.err) { cache.delete(key); cache.set(key, hit); return hit; } // LRU touch
  }
  const entry = { img: new Image(), ok: false, err: false, retryAt: 0 };
  entry.img.onload = () => { entry.ok = true; fieldScheduleDraw(); };
  entry.img.onerror = () => {
    // The node answers 404 both for "policy said no" and "upstream is
    // unreachable". Either way the honest local move is the same: fall
    // back to paper and try again later.
    entry.err = true;
    entry.retryAt = Date.now() + FIELD_TILE_RETRY_MS;
    fieldScheduleDraw();
  };
  entry.img.src = `/api/tiles/${z}/${x}/${y}.png?token=${token}`;
  cache.set(key, entry);
  while (cache.size > FIELD_TILE_CAP) cache.delete(cache.keys().next().value);
  return entry;
}

// fieldDrawTiles paints the basemap under everything else and reports
// how many tiles actually landed — the caller uses that to decide
// whether the paper grid is still the honest thing to show.
function fieldDrawTiles(ctx, proj, w, h) {
  if (!fieldBasemapOn() || !FIELD.view) return 0;
  const dpr = Math.min(2, window.devicePixelRatio || 1);
  const v = FIELD.view;
  const S = proj.scale;
  const tz = Math.max(0, Math.min(19, Math.floor(v.z) + (dpr >= 1.5 ? 1 : 0)));
  const n = Math.pow(2, tz);
  const tileSize = S / n;              // CSS px on screen per tile
  if (!isFinite(tileSize) || tileSize <= 0) return 0;
  const cmx = mercX(v.lon), cmy = mercY(v.lat);
  const x0 = Math.floor((cmx - (w / 2) / S) * n);
  const x1 = Math.floor((cmx + (w / 2) / S) * n);
  const y0 = Math.max(0, Math.floor((cmy - (h / 2) / S) * n));
  const y1 = Math.min(n - 1, Math.floor((cmy + (h / 2) / S) * n));
  // A sanity bound: a viewport should never ask for hundreds of tiles.
  if ((x1 - x0 + 1) * (y1 - y0 + 1) > 400) return 0;

  // THE DARK RESTYLE IS A VIEW DECISION (ADR-032): the cache holds the
  // originals, and the theme is applied here, at draw. Flipping the app
  // to light restyles instantly and refetches nothing.
  const dark = !document.documentElement.matches('[data-theme="light"]');
  const canFilter = 'filter' in ctx;
  if (canFilter) {
    ctx.filter = dark
      ? 'invert(1) hue-rotate(180deg) brightness(0.66) contrast(1.05) saturate(0.30)'
      : 'saturate(0.8) brightness(1.02)';
  }
  let drawn = 0;
  for (let tx = x0; tx <= x1; tx++) {
    for (let ty = y0; ty <= y1; ty++) {
      const wrapped = ((tx % n) + n) % n;
      const e = fieldTile(tz, wrapped, ty);
      if (!e.ok) continue;
      const px = w / 2 + (tx / n - cmx) * S;
      const py = h / 2 + (ty / n - cmy) * S;
      // Snap to whole pixels and overdraw half a pixel: fractional tile
      // edges leave hairline seams the eye reads as a broken map.
      ctx.drawImage(e.img, Math.round(px), Math.round(py),
        Math.ceil(tileSize) + 1, Math.ceil(tileSize) + 1);
      drawn++;
    }
  }
  if (canFilter) ctx.filter = 'none';
  if (drawn) {
    // Sink the result into the app's own near-black violet. Without
    // ctx.filter this wash is the whole treatment, so it goes heavier.
    ctx.fillStyle = dark
      ? (canFilter ? 'rgba(24,16,28,0.35)' : 'rgba(13,10,16,0.55)')
      : 'rgba(243,241,247,0.25)';
    ctx.fillRect(0, 0, w, h);
  }
  return drawn;
}

// fieldDrawAttribution: ODbL credit, drawn whenever OSM pixels are on
// screen. Part of rendering, not a footnote somebody can forget.
function fieldDrawAttribution(ctx, w, h, dim) {
  const label = '© OpenStreetMap contributors';
  ctx.font = '10px ui-monospace, monospace';
  ctx.textAlign = 'right';
  const wide = ctx.measureText(label).width;
  ctx.fillStyle = 'rgba(13,10,16,0.6)';
  ctx.fillRect(w - wide - 12, h - 20, wide + 8, 14);
  ctx.fillStyle = dim;
  ctx.fillText(label, w - 8, h - 10);
}

// ---- drawing (redraw-on-event, no permanent loop) ----

// The map's own paper, and the honest fallback when the basemap is off
// or has nothing to show: a metre grid anchored to WORLD coordinates, so
// it pans and zooms with the claims it carries, plus a scale bar.
// grid=false draws only the scale bar: with a basemap under it the metre
// grid would be a second, contradicting map — but the bar still has to
// say how far a screen inch is.
function fieldDrawPaper(ctx, proj, w, h, line, dim, grid) {
  const [latC] = proj.invert(w / 2, h / 2);
  const pxPerM = proj.metersToPx(1, latC);
  if (!isFinite(pxPerM) || pxPerM <= 0) return;
  // A round step — 1/2/5 × 10ⁿ metres — targeting ~90 px between lines.
  const target = 90 / pxPerM;
  const pow = Math.pow(10, Math.floor(Math.log10(target)));
  let step = pow * 10;
  for (const m of [1, 2, 5]) { if (pow * m >= target) { step = pow * m; break; } }
  const stepLatDeg = step / 111320;
  const cos = Math.max(0.05, Math.cos(latC * Math.PI / 180));
  const stepLonDeg = stepLatDeg / cos;
  // The visible window in world coordinates, from the projection itself.
  const [latTop, lonLeft] = proj.invert(0, 0);
  const [latBot, lonRight] = proj.invert(w, h);
  if (grid) {
    ctx.strokeStyle = line;
    ctx.globalAlpha = 0.45;
    ctx.lineWidth = 1;
    ctx.beginPath();
    for (let lo = Math.ceil(lonLeft / stepLonDeg) * stepLonDeg; lo < lonRight; lo += stepLonDeg) {
      const x = Math.round(proj.x(lo)) + 0.5;
      ctx.moveTo(x, 0); ctx.lineTo(x, h);
    }
    for (let la = Math.ceil(latBot / stepLatDeg) * stepLatDeg; la < latTop; la += stepLatDeg) {
      const y = Math.round(proj.y(la)) + 0.5;
      ctx.moveTo(0, y); ctx.lineTo(w, y);
    }
    ctx.stroke();
    ctx.globalAlpha = 1;
  }
  // Scale bar, bottom-left: one grid cell long, so the label explains
  // the grid at the same time.
  const barPx = step * pxPerM;
  const bx = 14, by = h - 14;
  ctx.strokeStyle = dim;
  ctx.lineWidth = 1.5;
  ctx.beginPath();
  ctx.moveTo(bx, by - 4); ctx.lineTo(bx, by);
  ctx.lineTo(bx + barPx, by); ctx.lineTo(bx + barPx, by - 4);
  ctx.stroke();
  ctx.fillStyle = dim;
  ctx.font = '10px ui-monospace, monospace';
  ctx.textAlign = 'left';
  ctx.fillText(step >= 1000 ? (step / 1000) + ' km' : step + ' m', bx + 4, by - 5);
}

function fieldDrawAll() {
  fieldDrawMap();
  fieldDrawRoster();
}

function fieldDrawMap() {
  const cv = FIELD.canvas, ctx = FIELD.ctx;
  if (!cv || !ctx) return;
  const host = cv.parentElement;
  const w = host.clientWidth, h = host.clientHeight;
  if (w < 2 || h < 2) return;
  const dpr = Math.min(2, window.devicePixelRatio || 1);
  // Setting canvas.width CLEARS the canvas — skip same-size resizes
  // (stage.js's rule, learned the hard way there).
  if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) {
    cv.width = Math.round(w * dpr);
    cv.height = Math.round(h * dpr);
    cv.style.width = w + 'px';
    cv.style.height = h + 'px';
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);
  const css = getComputedStyle(document.documentElement);
  const line = css.getPropertyValue('--line').trim() || 'rgba(215,164,239,.14)';
  const acc = css.getPropertyValue('--acc').trim() || '#d39ae7';
  const dim = css.getPropertyValue('--dim').trim() || '#879993';
  const fg = css.getPropertyValue('--fg').trim() || '#ddd8df';

  const proj = fieldProjection(w, h);
  FIELD.proj = proj;
  if (!proj) {
    ctx.fillStyle = dim;
    ctx.font = '13px system-ui';
    ctx.textAlign = 'center';
    ctx.fillText(t('field.empty'), w / 2, h / 2);
    return;
  }
  const d = FIELD.data;

  // The basemap goes UNDER everything, and the paper grid is what
  // honestly stands in when it is off or has nothing yet — never a
  // spinner, never an empty pretence that something is loading.
  const tiles = fieldDrawTiles(ctx, proj, w, h);
  fieldDrawPaper(ctx, proj, w, h, line, dim, tiles === 0);
  if (tiles === 0 && fieldBasemapOn()) {
    ctx.fillStyle = dim;
    ctx.font = '11px system-ui';
    ctx.textAlign = 'center';
    ctx.fillText(t('field.basemap_none'), w / 2, 18);
  }

  // Zones and places.
  for (const o of d.objects || []) {
    if (!o.geo) continue;
    const x = proj.x(o.geo.lon), y = proj.y(o.geo.lat);
    if (o.geo.radius_m) {
      const r = Math.max(6, proj.metersToPx(o.geo.radius_m, o.geo.lat));
      ctx.beginPath();
      ctx.arc(x, y, r, 0, Math.PI * 2);
      ctx.strokeStyle = line;
      ctx.setLineDash([4, 4]);
      ctx.stroke();
      ctx.setLineDash([]);
    }
    ctx.fillStyle = dim;
    ctx.beginPath();
    ctx.arc(x, y, 3, 0, Math.PI * 2);
    ctx.fill();
    ctx.font = '11px system-ui';
    ctx.textAlign = 'left';
    ctx.fillText(o.name, x + 7, y + 4);
  }

  // Routes: intent as a thin line, waypoints as dots.
  for (const o of d.objects || []) {
    if (!o.path || o.path.length < 2) continue;
    ctx.beginPath();
    o.path.forEach(([la, lo], i) => {
      const x = proj.x(lo), y = proj.y(la);
      if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    });
    ctx.strokeStyle = acc;
    ctx.globalAlpha = 0.5;
    ctx.lineWidth = 1.5;
    ctx.stroke();
    ctx.globalAlpha = 1;
    for (const [la, lo] of o.path) {
      ctx.beginPath();
      ctx.arc(proj.x(lo), proj.y(la), 2.4, 0, Math.PI * 2);
      ctx.fillStyle = acc;
      ctx.fill();
    }
  }

  // SP-3.2: completed sweep tracks — under the markers, over the intent
  // lines. Where a route says "we meant to go here", a track says "we
  // actually went", and its gaps stay gaps.
  fieldDrawTracks(ctx, proj, acc, dim);

  // Markers: expired claims are HISTORY — dimmed, never present danger.
  for (const m of d.markers || []) {
    const x = proj.x(m.lon), y = proj.y(m.lat);
    ctx.globalAlpha = m.active ? 1 : 0.35;
    ctx.font = '15px system-ui';
    ctx.textAlign = 'center';
    ctx.fillText(FIELD_MARKER_GLYPHS[m.kind] || '✦', x, y + 5);
    ctx.font = '10px system-ui';
    ctx.fillStyle = dim;
    ctx.fillText(m.text, x, y + 18);
    ctx.globalAlpha = 1;
  }

  // People: last KNOWN position wearing its age. ● live / ◐ stale; the
  // unknowns live in the roster, honestly absent from the map.
  for (const p of d.people || []) {
    const pos = p.position;
    if (!pos || pos.state === 'unknown') continue;
    const x = proj.x(pos.lon), y = proj.y(pos.lat);
    const col = typeof glyphColor === 'function' ? glyphColor(p.principal) : acc;
    if (pos.accuracy_m) {
      // A RING, not a wash: desktop fixes claim kilometres of accuracy,
      // and a filled disc at that radius painted the whole map grey. A
      // ring larger than the viewport is skipped — it says nothing.
      const r = Math.max(4, proj.metersToPx(pos.accuracy_m, pos.lat));
      if (r < Math.max(w, h)) {
        ctx.beginPath();
        ctx.arc(x, y, r, 0, Math.PI * 2);
        ctx.strokeStyle = col;
        ctx.globalAlpha = 0.3;
        ctx.setLineDash([3, 5]);
        ctx.lineWidth = 1;
        ctx.stroke();
        ctx.setLineDash([]);
        ctx.globalAlpha = 1;
      }
    }
    ctx.beginPath();
    ctx.arc(x, y, 5, 0, Math.PI * 2);
    ctx.fillStyle = pos.state === 'live' ? col : 'transparent';
    if (pos.state === 'live') ctx.fill();
    ctx.lineWidth = 2;
    ctx.strokeStyle = col;
    ctx.stroke();
    ctx.font = '11px system-ui';
    ctx.textAlign = 'left';
    ctx.fillStyle = fg;
    ctx.fillText(p.name || p.principal.slice(0, 6), x + 9, y - 4);
    ctx.fillStyle = dim;
    ctx.fillText(fieldAge(pos.age_seconds), x + 9, y + 8);
  }

  // Last, above every layer: whoever's pixels are underneath gets said.
  if (tiles > 0) fieldDrawAttribution(ctx, w, h, dim);
}

function fieldAge(sec) {
  if (sec === undefined) return '';
  if (sec < 60) return sec + 's';
  if (sec < 3600) return Math.floor(sec / 60) + 'm';
  return Math.floor(sec / 3600) + 'h ' + Math.floor((sec % 3600) / 60) + 'm';
}

function fieldDrawRoster() {
  const box = document.getElementById('fieldPeople');
  if (!box || !FIELD.data) return;
  box.innerHTML = '';
  const cadence = parseInt(localStorage.getItem(fieldCadenceKey()) || '0', 10);
  for (const p of FIELD.data.people || []) {
    const row = document.createElement('div');
    row.className = 'field-person';
    const dot = document.createElement('span');
    const st = p.position ? p.position.state : 'unknown';
    dot.className = 'field-dot ' + st;
    dot.textContent = st === 'live' ? '●' : st === 'stale' ? '◐' : '○';
    row.appendChild(dot);
    const name = document.createElement('span');
    name.className = 'field-name';
    name.textContent = p.name || p.principal.slice(0, 8);
    row.appendChild(name);
    const meta = document.createElement('span');
    meta.className = 'field-meta';
    if (st === 'unknown') {
      meta.textContent = t('field.unknown');
    } else {
      meta.textContent = fieldAge(p.position.age_seconds);
    }
    row.appendChild(meta);
    if (p.checkin) {
      const ci = document.createElement('span');
      ci.className = 'field-ci' + (p.checkin.sos ? ' sos' : '');
      let text = (p.checkin.sos ? '🆘 ' : '✓ ') + fieldAge(p.checkin.age_seconds);
      // THE OVERDUE PHRASING LAW (ADR-031): the viewer's arithmetic over
      // signed facts — expected / last seen / overdue. Never a verdict.
      if (cadence > 0 && p.checkin.age_seconds > cadence) {
        text += ' · ' + t('field.overdue', {
          every: fieldAge(cadence), last: fieldAge(p.checkin.age_seconds),
        });
        ci.classList.add('overdue');
      }
      ci.textContent = text;
      row.appendChild(ci);
    } else if (cadence > 0) {
      const ci = document.createElement('span');
      ci.className = 'field-ci overdue';
      ci.textContent = t('field.never_checked');
      row.appendChild(ci);
    }
    box.appendChild(row);
  }
}

// ---- interactions: the codebase's first pan/zoom ----

function fieldWireCanvas(cv, host) {
  let dragging = false, lastX = 0, lastY = 0, moved = false;
  cv.addEventListener('pointerdown', (ev) => {
    dragging = true; moved = false;
    lastX = ev.clientX; lastY = ev.clientY;
    cv.setPointerCapture(ev.pointerId);
  });
  cv.addEventListener('pointermove', (ev) => {
    if (!dragging) return;
    const dx = ev.clientX - lastX, dy = ev.clientY - lastY;
    if (Math.abs(dx) + Math.abs(dy) > 2) moved = true;
    fieldPanBy(dx, dy);
    lastX = ev.clientX; lastY = ev.clientY;
    fieldScheduleDraw();
  });
  cv.addEventListener('pointerup', (ev) => {
    dragging = false;
    if (!moved && FIELD.placing && FIELD.proj) {
      const r = cv.getBoundingClientRect();
      const [lat, lon] = FIELD.proj.invert(ev.clientX - r.left, ev.clientY - r.top);
      if (FIELD.placingWhat === 'place') fieldOpenPlaceDialog(lat, lon);
      else fieldOpenMarkerDialog(lat, lon);
    }
  });
  cv.addEventListener('wheel', (ev) => {
    ev.preventDefault();
    const r = cv.getBoundingClientRect();
    fieldZoomAt(ev.clientX - r.left, ev.clientY - r.top, ev.deltaY < 0 ? 0.28 : -0.28);
    fieldScheduleDraw();
  }, { passive: false });
  // Pinch: two pointers tracked crudely and honestly.
  let pinchDist = 0;
  cv.addEventListener('touchmove', (ev) => {
    if (ev.touches.length !== 2) { pinchDist = 0; return; }
    ev.preventDefault();
    const dx = ev.touches[0].clientX - ev.touches[1].clientX;
    const dy = ev.touches[0].clientY - ev.touches[1].clientY;
    const d = Math.hypot(dx, dy);
    if (pinchDist > 0) {
      const r = cv.getBoundingClientRect();
      const mx = (ev.touches[0].clientX + ev.touches[1].clientX) / 2 - r.left;
      const my = (ev.touches[0].clientY + ev.touches[1].clientY) / 2 - r.top;
      fieldZoomAt(mx, my, Math.log2(d / pinchDist));
      fieldScheduleDraw();
    }
    pinchDist = d;
  }, { passive: false });
  if (typeof ResizeObserver !== 'undefined' && !FIELD.resizeObs) {
    FIELD.resizeObs = new ResizeObserver(() => fieldScheduleDraw());
    FIELD.resizeObs.observe(host);
  }
}

// fieldPanBy moves the CENTRE, not an offset: the viewport is a place in
// the world, so dragging changes where we are, not how far we have
// strayed from a fit that keeps moving.
function fieldPanBy(dx, dy) {
  const v = FIELD.view;
  if (!v) return;
  const S = 256 * Math.pow(2, v.z);
  const cmx = mercX(v.lon) - dx / S;
  const cmy = Math.max(0, Math.min(1, mercY(v.lat) - dy / S));
  v.lon = mercLon(cmx);
  v.lat = mercLat(cmy);
}

// fieldZoomAt zooms about a point on the canvas: whatever is under the
// cursor (or between the fingers) STAYS there. Zooming about the centre
// makes a basemap feel like it is fighting the hand.
function fieldZoomAt(px, py, dz) {
  const v = FIELD.view;
  if (!v || !FIELD.proj) return;
  const [alat, alon] = FIELD.proj.invert(px, py);
  const cv = FIELD.canvas, host = cv && cv.parentElement;
  const w = host ? host.clientWidth : 0, h = host ? host.clientHeight : 0;
  v.z = Math.max(1, Math.min(19, v.z + dz));
  const S = 256 * Math.pow(2, v.z);
  const cmx = mercX(alon) - (px - w / 2) / S;
  const cmy = Math.max(0, Math.min(1, mercY(alat) - (py - h / 2) / S));
  v.lon = mercLon(cmx);
  v.lat = mercLat(cmy);
}

// fieldScheduleDraw coalesces redraws into one animation frame. There is
// still NO permanent loop (the hardware floor law): the latch arms only
// when something actually happened — a gesture, a tile arriving, a poll.
function fieldScheduleDraw() {
  if (FIELD.rafPending) return;
  FIELD.rafPending = true;
  requestAnimationFrame(() => {
    FIELD.rafPending = false;
    fieldDrawMap();
  });
}

// ---- marker placement ----

// The marker composer. What the person is CLAIMING is what the dialog
// shows: a kind (glyph cards, the object editor's picker idiom), a note,
// and — only for kinds that age — how long the claim speaks for. The
// coordinates are displayed and never editable: they came from a finger
// on the map, and a typed number would be a different claim.
let markerDraft = null;

// fieldArmMarker: the header's "+ marker" arms the map rather than
// opening a form. A marker is a claim ABOUT A PLACE, so the place is
// chosen first — the dialog opens where the finger landed.
function fieldArmMarker() {
  if (typeof pubView !== 'undefined' && pubView !== 'field') switchView('field');
  FIELD.placing = true;
  FIELD.placingWhat = 'marker';
  const hint = document.getElementById('fieldPlacingHint');
  if (hint) hint.style.display = '';
  fieldScheduleDraw();
}

// Kinds that are TEMPORARY BY NATURE offer an expiry. "Searched" is a
// fact about a moment that stays true forever; "hazard" is a condition
// that may lift. Offering a TTL on the first would invite a lie.
const FIELD_MARKER_AGEING = ['hazard', 'casualty', 'extraction'];

function fieldOpenMarkerDialog(lat, lon) {
  FIELD.placing = false;
  const armed = document.getElementById('fieldPlacingHint');
  if (armed) armed.style.display = 'none';
  markerDraft = { lat, lon, kind: 'waypoint' };
  const dlg = document.getElementById('dlgMarker');
  document.getElementById('markerAt').textContent =
    lat.toFixed(5) + ', ' + lon.toFixed(5);
  const txt = /** @type {HTMLInputElement} */(document.getElementById('markerText'));
  txt.value = '';
  document.getElementById('markerMsg').textContent = '';

  const ttl = /** @type {HTMLSelectElement} */(document.getElementById('markerTtl'));
  ttl.innerHTML = '';
  for (const [secs, key] of [[0, 'field.ttl.forever'], [3600, 'field.ttl.1h'],
    [6 * 3600, 'field.ttl.6h'], [24 * 3600, 'field.ttl.24h'], [72 * 3600, 'field.ttl.72h']]) {
    const o = document.createElement('option');
    o.value = String(secs);
    o.textContent = t(key);
    ttl.appendChild(o);
  }

  const pick = document.getElementById('markerKindPick');
  pick.innerHTML = '';
  const mark = () => pick.querySelectorAll('.kind-card').forEach(c =>
    c.classList.toggle('sel', c.dataset.k === markerDraft.kind));
  const ageing = () => {
    const on = FIELD_MARKER_AGEING.includes(markerDraft.kind);
    document.getElementById('markerTtlRow').style.display = on ? '' : 'none';
    document.getElementById('markerTtlHint').style.display = on ? '' : 'none';
  };
  for (const k of FIELD_MARKER_KINDS) {
    const card = document.createElement('button');
    card.type = 'button';
    card.className = 'kind-card';
    card.dataset.k = k;
    const g = document.createElement('span');
    g.className = 'kind-glyph';
    g.textContent = FIELD_MARKER_GLYPHS[k] || '✦';
    card.appendChild(g);
    const l = document.createElement('span');
    l.className = 'kind-label';
    l.textContent = t('field.kind.' + k);
    card.appendChild(l);
    card.onclick = () => {
      markerDraft.kind = k;
      mark();
      ageing();
      // The note is optional, and its placeholder is the kind's own
      // question — an empty note falls back to the kind's name, which
      // is why nothing here is required.
      txt.placeholder = t('field.kind_ph.' + k);
      txt.focus();
    };
    pick.appendChild(card);
  }
  mark();
  ageing();
  txt.placeholder = t('field.kind_ph.waypoint');
  dlg.showModal();
  setTimeout(() => txt.focus(), 30);
}

function fieldPlaceMarker() {
  if (!markerDraft) return;
  const txt = /** @type {HTMLInputElement} */(document.getElementById('markerText'));
  const body = {
    kind: markerDraft.kind,
    text: txt.value.trim(),
    lat: markerDraft.lat, lon: markerDraft.lon,
  };
  const ttl = /** @type {HTMLSelectElement} */(document.getElementById('markerTtl'));
  if (FIELD_MARKER_AGEING.includes(markerDraft.kind) && Number(ttl.value) > 0) {
    body.expires_in_seconds = Number(ttl.value);
  }
  api(`/api/spaces/${current}/markers`, { method: 'POST', body: JSON.stringify(body) })
    .then(() => {
      document.getElementById('dlgMarker').close();
      markerDraft = null;
      refreshField();
    })
    .catch(e => { document.getElementById('markerMsg').textContent = e.message; });
}

// ---- place creation (SP-3.2 follow-up) ----

// A place is an OBJECT with geo — the thing «Начать свип» lives on. It is
// born from a finger on the map (the marker discipline: displayed
// coordinates, never editable ones), and the reward for creating one is
// immediate: the flow lands on the new object's card, where the sweep
// button is.
let placeDraft = null;

function fieldArmPlace() {
  if (typeof pubView !== 'undefined' && pubView !== 'field') switchView('field');
  FIELD.placing = true;
  FIELD.placingWhat = 'place';
  const hint = document.getElementById('fieldPlacingHint');
  if (hint) hint.style.display = '';
  fieldScheduleDraw();
}

function fieldOpenPlaceDialog(lat, lon) {
  FIELD.placing = false;
  const armed = document.getElementById('fieldPlacingHint');
  if (armed) armed.style.display = 'none';
  placeDraft = { lat, lon };
  const dlg = document.getElementById('dlgPlace');
  document.getElementById('placeAt').textContent =
    lat.toFixed(5) + ', ' + lon.toFixed(5);
  const name = /** @type {HTMLInputElement} */(document.getElementById('placeName'));
  name.value = '';
  document.getElementById('placeMsg').textContent = '';
  const rad = /** @type {HTMLSelectElement} */(document.getElementById('placeRadius'));
  rad.innerHTML = '';
  for (const m of [100, 300, 1000, 0]) {
    const o = document.createElement('option');
    o.value = String(m);
    o.textContent = m ? m + ' m' : t('field.radius_point');
    rad.appendChild(o);
  }
  rad.value = '300';
  document.getElementById('placeCancel').onclick = () => dlg.close();
  document.getElementById('placeCreate').onclick = () => fieldCreatePlace();
  dlg.showModal();
  setTimeout(() => name.focus(), 30);
}

function fieldCreatePlace() {
  if (!placeDraft) return;
  const name = /** @type {HTMLInputElement} */(document.getElementById('placeName'));
  if (!name.value.trim()) {
    document.getElementById('placeMsg').textContent = t('objects.name_required');
    return;
  }
  const rad = Number((/** @type {HTMLSelectElement} */(document.getElementById('placeRadius'))).value);
  const geo = { lat: placeDraft.lat, lon: placeDraft.lon };
  if (rad > 0) geo.radius_m = rad;
  api(`/api/spaces/${current}/objects`, {
    method: 'POST',
    body: JSON.stringify({ object: { kind: 'place', name: name.value.trim(), geo } }),
  })
    .then(o => {
      document.getElementById('dlgPlace').close();
      placeDraft = null;
      refreshField();
      // The payoff lands where the person is going anyway: the new
      // place's card, sweep button and all.
      if (o && o.object_id && typeof openObject === 'function') {
        switchView('objects');
        openObject(o.object_id);
      }
    })
    .catch(e => { document.getElementById('placeMsg').textContent = e.message; });
}

// ---- position sharing: while the map is open, and nothing more ----

function fieldStartSharing() {
  if (FIELD.shareTimer || !navigator.geolocation) return;
  const emit = () => {
    if (typeof pubView === 'undefined' || pubView !== 'field') return;
    navigator.geolocation.getCurrentPosition((fix) => {
      api(`/api/spaces/${current}/position`, {
        method: 'POST',
        body: JSON.stringify({
          lat: fix.coords.latitude, lon: fix.coords.longitude,
          accuracy_m: Math.round(fix.coords.accuracy || 0),
          ttl_seconds: 600,
        }),
      }).then(() => {
        const ind = document.getElementById('fieldShareInd');
        if (ind) { ind.classList.add('on'); }
        refreshField();
      }).catch(() => {});
    }, () => {}, { enableHighAccuracy: true, timeout: 15_000, maximumAge: 30_000 });
  };
  emit();
  FIELD.shareTimer = setInterval(emit, 60_000);
}

function fieldStopSharing(userAct) {
  if (FIELD.shareTimer) { clearInterval(FIELD.shareTimer); FIELD.shareTimer = null; }
  const ind = document.getElementById('fieldShareInd');
  if (ind) ind.classList.remove('on');
  // No "I stopped sharing" event exists or is needed: the last claim
  // simply ages out of its signed TTL (ADR-031).
  if (userAct) refreshField();
}

// ---- check-in ----

function fieldCheckin(sos) {
  const send = (pos) => {
    const body = { sos: !!sos };
    if (pos) { body.lat = pos.coords.latitude; body.lon = pos.coords.longitude; }
    const finish = () =>
      api(`/api/spaces/${current}/checkin`, { method: 'POST', body: JSON.stringify(body) })
        .then(() => refreshField())
        .catch(e => console.error(e));
    if (navigator.getBattery) {
      navigator.getBattery().then(b => {
        body.battery_pct = Math.round(b.level * 100);
        finish();
      }).catch(finish);
    } else finish();
  };
  if (navigator.geolocation) {
    navigator.geolocation.getCurrentPosition(send, () => send(null), { timeout: 5000, maximumAge: 60_000 });
  } else send(null);
}


// ---- SP-3.2: the Field Session on the map ----

function fieldSweepMinutes(ms) {
  const m = Math.max(0, Math.floor(ms / 60000));
  return m >= 60 ? `${Math.floor(m / 60)} h ${m % 60} min` : `${m} min`;
}
function fieldSweepKm(m) {
  return m >= 1000 ? `${(m / 1000).toFixed(1)} km` : `${m} m`;
}

// The banner renders THIS NODE's live sessions. The poll (refreshField,
// every 10 s) is the truth; window.quietSweep pushes are the responsiveness
// between polls — an Android host pushes only while the page is visible,
// and a banner that only updated while looked at would go stale in a
// pocket, which is why the poll stays.
function fieldSweepBannerSync() {
  const ban = document.getElementById('fieldSweepBanner');
  if (!ban) return;
  const live = (FIELD.actives || []).filter(a => a.state === 2 || a.state === 3);
  if (!live.length) { ban.style.display = 'none'; ban.innerHTML = ''; return; }
  ban.style.display = '';
  ban.innerHTML = '';
  for (const a of live) {
    const row = document.createElement('div');
    row.className = 'field-sweep-row';
    const dot = document.createElement('span');
    dot.className = 'field-sweep-dot' + (a.state === 2 ? ' on' : '');
    row.appendChild(dot);
    const name = document.createElement('span');
    name.className = 'field-sweep-name';
    name.textContent = a.label || t('sweep.sweep');
    row.appendChild(name);
    const meta = document.createElement('span');
    meta.className = 'field-sweep-meta';
    const elapsed = a.started_at ? Date.now() - a.started_at * 1000 : 0;
    meta.textContent = t(a.state === 2 ? 'sweep.recording' : 'sweep.suspended') +
      ' · ' + fieldSweepMinutes(elapsed) + ' · ' + fieldSweepKm(a.distance_m || 0);
    row.appendChild(meta);
    const stop = document.createElement('button');
    stop.className = 'btn-tinted field-sweep-stop';
    stop.textContent = t('sweep.stop');
    stop.onclick = () => sweepStopDialog(a.sweep_id, () => refreshField());
    row.appendChild(stop);
    ban.appendChild(row);
  }
}

// The host's push (Android). The payload is a hint to redraw NOW; the poll
// remains the source, so a malformed push costs one refresh, never a lie.
window.quietSweep = function quietSweep(state) {
  try {
    if (state && (state.state === 'stopped' || state.state === 'recording')) {
      if (typeof pubView !== 'undefined' && pubView === 'field') refreshField();
    }
  } catch (e) { console.warn('quiet: sweep push', e); }
};

// Completed tracks: decoded once per asset, drawn BY SEGMENT — the decoder
// hands back segments between gaps, so joining across one is impossible by
// construction (the brief's law, in the shape of the data).
function fieldTrackFor(sweep) {
  const hex = sweep.track_asset;
  if (!hex || /^0+$/.test(hex)) return null;
  const got = FIELD.tracks.get(hex);
  if (got === 'pending' || got === 'failed') return null;
  if (got) return got;
  FIELD.tracks.set(hex, 'pending');
  fetch(`/api/spaces/${current}/assets/${hex}`, { headers: { 'X-QP-Token': token } })
    .then(r => { if (!r.ok) throw new Error(r.status); return r.arrayBuffer(); })
    .then(buf => {
      FIELD.tracks.set(hex, trackDecode(new Uint8Array(buf)));
      fieldScheduleDraw();
    })
    .catch(() => {
      // Asked once; the asset may simply not have synced yet. The next
      // space refresh clears the way for another try.
      FIELD.tracks.set(hex, 'failed');
    });
  return null;
}

function fieldDrawTracks(ctx, proj, acc, dim) {
  for (const sw of FIELD.sweeps || []) {
    const tr = fieldTrackFor(sw);
    if (!tr) continue;
    // Segments: the walked line.
    ctx.lineWidth = 2;
    ctx.strokeStyle = acc;
    ctx.globalAlpha = 0.75;
    for (const seg of tr.segments) {
      if (seg.length < 2) {
        if (seg.length === 1) {
          ctx.beginPath();
          ctx.arc(proj.x(seg[0].lon), proj.y(seg[0].lat), 2.2, 0, Math.PI * 2);
          ctx.fillStyle = acc;
          ctx.fill();
        }
        continue;
      }
      ctx.beginPath();
      seg.forEach((pt, i) => {
        const x = proj.x(pt.lon), y = proj.y(pt.lat);
        if (i === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
      });
      ctx.stroke();
    }
    ctx.globalAlpha = 1;
    // Gaps: DRAWN AS GAPS — a dotted bridge with the claim written on it,
    // never a solid line through a forest.
    for (const g of tr.gaps) {
      const a = tr.segments[g.afterSegment];
      const b = tr.segments[g.afterSegment + 1];
      if (!a || !b || !a.length || !b.length) continue;
      const p1 = a[a.length - 1], p2 = b[0];
      const x1 = proj.x(p1.lon), y1 = proj.y(p1.lat);
      const x2 = proj.x(p2.lon), y2 = proj.y(p2.lat);
      ctx.beginPath();
      ctx.setLineDash([2, 5]);
      ctx.strokeStyle = dim;
      ctx.lineWidth = 1.2;
      ctx.moveTo(x1, y1); ctx.lineTo(x2, y2);
      ctx.stroke();
      ctx.setLineDash([]);
      ctx.fillStyle = dim;
      ctx.font = '10px system-ui';
      ctx.textAlign = 'center';
      ctx.fillText(
        t('sweep.gap_' + g.reason) + ' · ' + Math.round(g.durationMs / 1000) + ' s',
        (x1 + x2) / 2, (y1 + y2) / 2 - 4);
    }
  }
}
