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
  // viewport: pan offset in CSS px, zoom as scale multiplier over fit
  panX: 0, panY: 0, zoom: 1,
  proj: null,          // computed each draw from data bbox
  timer: null,         // poll while the view is open
  shareTimer: null,    // position emission loop
  placing: false,      // "tap the map to place a marker"
  resizeObs: null,
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
  fieldDrawAll();
}

// fieldTeardown stops everything the view owns — called by switchView on
// the way out (the ATMO.unmount discipline: a torn-off canvas keeps its
// timers unless asked not to).
function fieldTeardown() {
  if (FIELD.timer) { clearInterval(FIELD.timer); FIELD.timer = null; }
  fieldStopSharing(false);
  FIELD.placing = false;
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

  const spread = document.createElement('span');
  spread.style.flex = '1';
  bar.appendChild(spread);

  const markerBtn = document.createElement('button');
  markerBtn.id = 'fieldMarkerBtn';
  markerBtn.className = 'btn-plain';
  markerBtn.textContent = '📍 ' + t('field.place_marker');
  markerBtn.onclick = () => {
    FIELD.placing = !FIELD.placing;
    markerBtn.classList.toggle('sel', FIELD.placing);
  };
  bar.appendChild(markerBtn);

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

// A local equirectangular projection about the content's own bbox:
// x scaled by cos(lat0) so metres look like metres at this latitude.
function fieldProjection(w, h) {
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
  if (!pts.length) return null;
  let minLat = 90, maxLat = -90, minLon = 180, maxLon = -180;
  for (const [la, lo] of pts) {
    if (la < minLat) minLat = la;
    if (la > maxLat) maxLat = la;
    if (lo < minLon) minLon = lo;
    if (lo > maxLon) maxLon = lo;
  }
  const lat0 = (minLat + maxLat) / 2;
  const cos = Math.max(0.05, Math.cos(lat0 * Math.PI / 180));
  // A lone point is a place, not a bbox: open a ~600 m window around it
  // rather than zooming to a degenerate span where any accuracy circle
  // swallows the viewport (found by the owner's first solo share).
  const minSpanDeg = 600 / 111320;
  const spanX = Math.max((maxLon - minLon) * cos, minSpanDeg);
  const spanY = Math.max(maxLat - minLat, minSpanDeg);
  const pad = 0.82; // fit-to-content with breathing room
  const scale = Math.min(w / spanX, h / spanY) * pad * FIELD.zoom;
  const cx = (minLon + maxLon) / 2, cy = lat0;
  return {
    x: (lon) => w / 2 + ((lon - cx) * cos) * scale + FIELD.panX,
    y: (lat) => h / 2 - (lat - cy) * scale + FIELD.panY,
    invert: (px, py) => [
      cy + (h / 2 + FIELD.panY - py) / scale,
      cx + (px - w / 2 - FIELD.panX) / (scale * cos),
    ],
    metersToPx: (m, lat) => (m / 111_320) * scale,
  };
}

// ---- drawing (redraw-on-event, no permanent loop) ----

// The map's own paper (no basemap tiles yet — SP-3.1): a metre grid
// anchored to WORLD coordinates, so it pans and zooms with the claims it
// carries, plus a scale bar. Together they are what makes a dot on a dark
// canvas read as a place on a map.
function fieldDrawPaper(ctx, proj, w, h, line, dim) {
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

  fieldDrawPaper(ctx, proj, w, h, line, dim);

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
    FIELD.panX += dx; FIELD.panY += dy;
    lastX = ev.clientX; lastY = ev.clientY;
    fieldDrawMap();
  });
  cv.addEventListener('pointerup', (ev) => {
    dragging = false;
    if (!moved && FIELD.placing && FIELD.proj) {
      const r = cv.getBoundingClientRect();
      const [lat, lon] = FIELD.proj.invert(ev.clientX - r.left, ev.clientY - r.top);
      fieldOpenMarkerDialog(lat, lon);
    }
  });
  cv.addEventListener('wheel', (ev) => {
    ev.preventDefault();
    const f = ev.deltaY < 0 ? 1.15 : 1 / 1.15;
    FIELD.zoom = Math.min(64, Math.max(0.25, FIELD.zoom * f));
    fieldDrawMap();
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
      FIELD.zoom = Math.min(64, Math.max(0.25, FIELD.zoom * (d / pinchDist)));
      fieldDrawMap();
    }
    pinchDist = d;
  }, { passive: false });
  if (typeof ResizeObserver !== 'undefined' && !FIELD.resizeObs) {
    FIELD.resizeObs = new ResizeObserver(() => fieldDrawMap());
    FIELD.resizeObs.observe(host);
  }
}

// ---- marker placement ----

function fieldOpenMarkerDialog(lat, lon) {
  FIELD.placing = false;
  document.getElementById('fieldMarkerBtn')?.classList.remove('sel');
  const kind = prompt(t('field.marker_kind_prompt', { kinds: FIELD_MARKER_KINDS.join(', ') }), 'waypoint');
  if (!kind || !FIELD_MARKER_KINDS.includes(kind.trim())) return;
  const text = prompt(t('field.marker_text_prompt'), '') || '';
  const body = { kind: kind.trim(), text: text.trim(), lat, lon };
  if (kind.trim() === 'hazard') {
    const hrs = prompt(t('field.marker_ttl_prompt'), '');
    const h = parseFloat(hrs);
    if (h > 0) body.expires_in_seconds = Math.round(h * 3600);
  }
  api(`/api/spaces/${current}/markers`, { method: 'POST', body: JSON.stringify(body) })
    .then(() => refreshField())
    .catch(e => console.error(e));
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
