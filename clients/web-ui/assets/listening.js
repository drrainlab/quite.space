// Listening room client (LR-2). Protocol state is ONLY the host's durable
// commands; every position on every device is COMPUTED from the winning
// command and the shared clock. The audio element's state is never protocol
// state: a follower pausing their own audio just stops following — no
// command is sent, nobody else is interrupted.

// ---- SyncClock: calibrate against the node's shared time source ----
// The node reports its source honestly: "relay:<addr>" when calibrated
// against a common relay, "node:<fp>" otherwise. Only a shared relay source
// justifies the "calibrated" badge; anything else renders as approximate.

const SYNC_CLOCK = {
  offset: null,       // sharedMs - performance.now() at calibration
  rttUnc: 0,          // client↔node half-RTT + node-reported uncertainty
  sourceId: null,
  calibratedAtPerf: 0,

  async calibrate() {
    let best = null;
    for (let i = 0; i < 4; i++) {
      const t0 = performance.now();
      let r;
      try { r = await api('/api/time'); } catch (e) { return; }
      const t1 = performance.now();
      const rtt = t1 - t0;
      if (!best || rtt < best.rtt) {
        best = { rtt, offset: r.now_ms + rtt / 2 - t1, src: r.source_id, unc: r.uncertainty_ms || 0 };
      }
    }
    if (!best) return;
    this.offset = best.offset;
    this.rttUnc = best.rtt / 2 + best.unc;
    this.sourceId = best.src;
    this.calibratedAtPerf = performance.now();
  },

  nowMs() {
    if (this.offset == null) return Date.now(); // uncalibrated best-effort
    return this.offset + performance.now();
  },

  // uncertaintyMs grows while the calibration ages (sleep, background tabs).
  uncertaintyMs() {
    if (this.offset == null) return Infinity;
    const ageMin = (performance.now() - this.calibratedAtPerf) / 60000;
    return this.rttUnc + ageMin * 10;
  },

  quality() {
    if (this.offset == null) return 'uncalibrated';
    if (this.uncertaintyMs() > 1000) return 'stale';
    return this.sourceId && this.sourceId.startsWith('relay:') ? 'calibrated' : 'approximate';
  },
};

document.addEventListener('visibilitychange', () => {
  // Returning from background: recalibration is mandatory (plan LR-2).
  if (!document.hidden) SYNC_CLOCK.calibrate();
});

// ---- create dialog (composer chip, styled like Poll/Form) ----

let listenTrack = null; // { asset, duration_ms, name } picked in the dialog

function openListeningDialog() {
  listenTrack = null;
  document.getElementById('listenTitle').value = '';
  document.getElementById('listenMsg').textContent = '';
  renderListenDrop();
  dlgListenRoom.showModal();
  setTimeout(() => document.getElementById('listenTitle').focus(), 50);
}

function renderListenDrop() {
  const zone = document.getElementById('listenDrop');
  zone.innerHTML = '';
  zone.className = 'comp-drop' + (listenTrack ? ' has' : '');
  const hint = document.createElement('div');
  hint.className = 'comp-drop-hint';
  hint.textContent = listenTrack
    ? `♫ ${listenTrack.name} · ${fmtListenTime(listenTrack.duration_ms)}`
    : 'drop an audio file here, or click to browse';
  zone.appendChild(hint);
  zone.onclick = () => {
    const inp = document.createElement('input');
    inp.type = 'file'; inp.accept = 'audio/*';
    inp.onchange = () => inp.files[0] && pickListenTrack(inp.files[0]);
    inp.click();
  };
  zone.ondragover = (e) => { e.preventDefault(); zone.classList.add('over'); };
  zone.ondragleave = () => zone.classList.remove('over');
  zone.ondrop = (e) => {
    e.preventDefault(); zone.classList.remove('over');
    const f = e.dataTransfer.files[0];
    if (f) pickListenTrack(f);
  };
}

async function pickListenTrack(file) {
  const msg = document.getElementById('listenMsg');
  msg.textContent = 'uploading…';
  try {
    const up = await uploadAssetFile(file);
    const dur = await audioDurationMs(file);
    listenTrack = { asset: up.asset_id, duration_ms: dur, name: file.name };
    msg.textContent = '';
    renderListenDrop();
  } catch (e) { msg.textContent = e.message || 'upload failed'; }
}

function audioDurationMs(file) {
  return new Promise((resolve) => {
    const url = URL.createObjectURL(file);
    const a = new Audio();
    a.preload = 'metadata';
    a.onloadedmetadata = () => {
      URL.revokeObjectURL(url);
      resolve(isFinite(a.duration) ? Math.round(a.duration * 1000) : 0);
    };
    a.onerror = () => { URL.revokeObjectURL(url); resolve(0); };
    a.src = url;
  });
}

async function confirmListenRoom() {
  const msg = document.getElementById('listenMsg');
  const title = document.getElementById('listenTitle').value.trim();
  if (!title) { msg.textContent = 'A title is required.'; return; }
  if (!listenTrack) { msg.textContent = 'Pick a track first.'; return; }
  try {
    const props = { title, asset: listenTrack.asset };
    if (listenTrack.duration_ms) props.duration_ms = String(listenTrack.duration_ms);
    const r = await api(`/api/spaces/${current}/apps`, { method: 'POST',
      body: JSON.stringify({ app_id: 'listening-room', document_id: composerDoc.document_id, props }) });
    composerDoc.blocks.push({ id: 'b' + randHex16().slice(0, 8), type: 'app',
      props: { asset: r.instance_id } });
    renderComposerBlocks();
    dlgListenRoom.close();
  } catch (e) { msg.textContent = e.message; }
}

// ---- the room renderer ----

function fmtListenTime(ms) {
  const s = Math.max(0, Math.floor((ms || 0) / 1000));
  return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
}

// computeSessionPosition: the ONLY place a position comes from.
function computeSessionPosition(cmd) {
  if (!cmd) return 0;
  if (cmd.action === 'play') {
    const elapsed = SYNC_CLOCK.nowMs() - cmd.effective_at_ms;
    return Math.max(0, cmd.position_ms + elapsed);
  }
  return cmd.position_ms; // pause | seek hold their position
}

function renderListeningRoom(box, inst) {
  box.classList.add('listen-room');
  const iid = inst.instance_id;
  const st = {
    sess: null,        // last session response
    joined: false,     // autoplay gesture given
    following: true,   // false → Local pause · not following
    resyncUntil: 0,    // show Resyncing until this perf time
    programmatic: 0,   // >0 while we drive the audio element ourselves
    trackEnded: false, // session position ran past the track
    hostAway: false,
    lastMembersAt: 0,
  };

  const title = document.createElement('div');
  title.className = 'listen-title';
  const status = document.createElement('div');
  status.className = 'listen-status';
  const audio = document.createElement('audio');
  audio.controls = true;
  audio.style.width = '100%';
  const controls = document.createElement('div');
  controls.className = 'listen-controls';
  const meta = document.createElement('div');
  meta.className = 'pub-meta';
  box.append(title, status, audio, controls, meta);

  // A follower's own pause button = stop following, nothing more. The track
  // reaching its end is NOT a local pause — that's the session running out.
  audio.addEventListener('pause', () => {
    if (st.programmatic > 0 || audio.ended) return;
    if (st.sess?.command?.action === 'play') st.following = false;
    update();
  });
  audio.addEventListener('play', () => {
    if (st.programmatic > 0) return;
    // Pressing play yourself is a resync intent, host and follower alike.
    st.joined = true;
    st.following = true;
    update();
  });

  function progPause() { st.programmatic++; audio.pause(); setTimeout(() => st.programmatic--, 50); }
  function progPlay() {
    st.programmatic++;
    audio.play().catch(() => { st.joined = false; update(); });
    setTimeout(() => st.programmatic--, 50);
  }

  function statusLine() {
    const s = st.sess;
    if (!s) return '…';
    if (!s.command) return 'session idle · host starts it';
    const track = s.track || {};
    if (track.asset_state && track.asset_state !== 'complete') {
      return 'track unavailable — fetch?';
    }
    if (st.hostAway && !s.i_am_host) return 'host away · playing the last command';
    if (st.trackEnded && s.command.action === 'play') return 'track ended · host can restart';
    if (performance.now() < st.resyncUntil) return 'Resyncing…';
    if (s.command.action !== 'play') return 'paused by ' + (s.i_am_host ? 'you' : (s.host_name || 'host'));
    if (!st.joined) return 'Session active · tap to join audio';
    if (!st.following) return 'Local pause · not following';
    const q = SYNC_CLOCK.quality();
    return q === 'calibrated' ? 'Following' : 'Following · ' + q;
  }

  function update() {
    const s = st.sess;
    if (!s) return;
    const track = s.track || {};
    title.textContent = '🎧 ' + (track.title || 'Listening room') +
      (s.host_name ? ' · hosted by ' + (s.i_am_host ? 'you' : s.host_name) : '');
    status.textContent = statusLine();
    status.classList.toggle('warn', !st.following || st.hostAway);

    // Attach the source only once the asset is COMPLETE locally — a src set
    // during fetching would cache a failed load and never recover.
    const assetReady = track.asset &&
      (!track.asset_state || track.asset_state === 'complete');
    if (assetReady && audio.dataset.src !== track.asset) {
      audio.dataset.src = track.asset;
      audio.src = `/api/spaces/${current}/assets/${track.asset}?token=${token}`;
    }

    controls.innerHTML = '';
    if (s.i_am_host) {
      const mk = (label, fields) => {
        const b = document.createElement('button');
        b.className = 'btn-tinted';
        b.textContent = label;
        b.onclick = async () => {
          try {
            await api(`/api/spaces/${current}/apps/${iid}/actions/command`,
              { method: 'POST', body: JSON.stringify(fields) });
            await refreshSession();
          } catch (e) { status.textContent = e.message; }
        };
        return b;
      };
      if (!s.command) {
        controls.appendChild(mk('▶ Start session', { action: 'play', position_ms: 0, start_session: true }));
      } else {
        if (s.command.action === 'play') {
          let pos = Math.round(computeSessionPosition(s.command));
          const dur = Number((s.track || {}).duration_ms) || 0;
          if (dur) pos = Math.min(pos, dur);
          controls.appendChild(mk('⏸ Pause for everyone', { action: 'pause', position_ms: pos }));
        } else {
          controls.appendChild(mk('▶ Resume', { action: 'play', position_ms: s.command.position_ms }));
          controls.appendChild(mk('▶ Restart session', { action: 'play', position_ms: 0, start_session: true }));
        }
      }
    } else if (s.command && !st.joined && s.command.action === 'play') {
      const join = document.createElement('button');
      join.className = 'btn-tinted';
      join.textContent = '🎧 Tap to join audio';
      join.onclick = () => { st.joined = true; st.following = true; progPlay(); update(); };
      controls.appendChild(join);
    }
    // Resync is for ANY role that stopped following (host included).
    if (s.command && st.joined && !st.following) {
      const rs = document.createElement('button');
      rs.className = 'btn-tinted';
      rs.textContent = '↻ Resync';
      rs.onclick = () => { st.following = true; st.resyncUntil = performance.now() + 1200; progPlay(); update(); };
      controls.appendChild(rs);
    }
    if ((s.track || {}).asset_state && s.track.asset_state !== 'complete') {
      const f = document.createElement('button');
      f.className = 'btn-plain';
      f.textContent = '⬇ fetch track';
      f.onclick = async () => {
        f.textContent = 'requesting…';
        try { await api(`/api/spaces/${current}/assets/${s.track.asset}/fetch`, { method: 'POST' }); } catch (e) {}
      };
      controls.appendChild(f);
    }
    const unc = SYNC_CLOCK.uncertaintyMs();
    meta.textContent = s.command
      ? `session ${s.command.session_epoch}.${s.command.sequence} · clock ${SYNC_CLOCK.quality()}` +
        (isFinite(unc) ? ` ±${Math.round(unc)}ms` : '')
      : 'protocol: no commands yet';
  }

  async function refreshSession() {
    try {
      st.sess = await api(`/api/spaces/${current}/apps/${iid}/session`);
    } catch (e) { return; }
    // host presence → "host away" (best-effort, every 10s)
    if (performance.now() - st.lastMembersAt > 10_000 && st.sess && !st.sess.i_am_host) {
      st.lastMembersAt = performance.now();
      try {
        const m = await api(`/api/spaces/${current}/members`);
        const host = (Array.isArray(m) ? m : []).find(x => x.principal === st.sess.host);
        st.hostAway = !!host && host.presence && host.presence.known && !host.presence.current;
      } catch (e) {}
    }
    update();
  }

  // Drift correction: bands from the plan. <250ms nothing; 250–1500
  // playbackRate nudge; 1500–2000 careful seek; beyond → seek + visible
  // Resyncing status.
  function driftTick() {
    const s = st.sess;
    if (!s || !s.command) return;
    const cmd = s.command;
    const expected = computeSessionPosition(cmd);
    if (cmd.action !== 'play') {
      if (!audio.paused) progPause();
      if (Math.abs(audio.currentTime * 1000 - expected) > 500 && audio.readyState > 0) {
        audio.currentTime = expected / 1000;
      }
      return;
    }
    if (!st.joined || !st.following) return;
    if (audio.readyState === 0) return; // still buffering — status shows it
    const durMs = isFinite(audio.duration) ? audio.duration * 1000 : null;
    if (durMs && expected >= durMs - 100) {
      // The session position ran past the track: the moment is over.
      if (!audio.paused) progPause();
      if (!st.trackEnded) { st.trackEnded = true; update(); }
      return;
    }
    if (st.trackEnded) { st.trackEnded = false; update(); }
    if (audio.paused) progPlay();
    const drift = audio.currentTime * 1000 - expected;
    const abs = Math.abs(drift);
    if (abs < 250) {
      audio.playbackRate = 1;
    } else if (abs < 1500) {
      audio.playbackRate = drift > 0 ? 0.96 : 1.04;
    } else if (abs < 2000) {
      audio.playbackRate = 1;
      audio.currentTime = expected / 1000;
    } else {
      audio.playbackRate = 1;
      audio.currentTime = expected / 1000;
      st.resyncUntil = performance.now() + 1500;
      update();
    }
  }

  if (SYNC_CLOCK.offset == null) SYNC_CLOCK.calibrate();
  refreshSession();
  const pollT = setInterval(() => {
    if (!box.isConnected) { clearInterval(pollT); clearInterval(driftT); progPause(); return; }
    refreshSession();
  }, 2000);
  const driftT = setInterval(() => { if (box.isConnected) driftTick(); }, 500);

  return box;
}
