// @ts-check
// SR-0 — Radio Mode: the space's composer becomes a push-to-talk surface,
// and the page renders what the HOST pushes (window.quietPtt) — it never
// records, never transcribes, never sees the microphone. The name is
// deliberately not radiomeet.js: that file is RF pairing; this one is a
// way of talking.
//
// Two independent primitives, per the owner's invariant: PTT input here;
// automatic speech of incoming messages lives entirely on the host
// (RadioPlaybackCoordinator) and needs nothing from this page.
//
// Availability: only where the host bridge exists (the Android app). On
// desktop/web the section and the composer simply do not appear.

const RADIO = (() => {
  const echoKey = (space) => `radio.mode.echo.${space}`;

  function hostReady() {
    return typeof QuietHost !== 'undefined' && typeof HOST !== 'undefined';
  }

  /** The local echo: instant render, corrected by quietRadio pushes. */
  function state(space) {
    try {
      const raw = localStorage.getItem(echoKey(space));
      if (raw) return JSON.parse(raw);
    } catch { /* a broken echo is an absent echo */ }
    return { enabled: false, autoplay: true };
  }

  function remember(space, st) {
    try { localStorage.setItem(echoKey(space), JSON.stringify(st)); } catch {}
  }

  function setMode(space, enabled, autoplay) {
    if (!HOST.setRadioMode(space, enabled, autoplay)) return false;
    remember(space, { enabled, autoplay });
    return true;
  }

  // ---- the PTT composer ----
  // States arrive from the host; the page's own pointer handling only
  // decides which verb to call. Slide left ≥ 72px = cancel; a visible
  // cancel button covers whoever cannot drag (accessibility, groom §41).

  let ui = null;          // {wrap, button, status, wave, cancelBtn}
  let uiSpace = null;
  let pointerDown = false;
  let slidPast = false;
  let lastState = 'idle';

  function label(key, fallback) {
    try {
      const v = typeof t === 'function' ? t(key) : null;
      return v && v !== key ? v : fallback;
    } catch { return fallback; }
  }

  function buildUI(space) {
    const wrap = document.createElement('div');
    wrap.id = 'pttWrap';
    wrap.innerHTML =
      `<button id="pttCancel" class="btn-plain" style="display:none">${esc(label('ptt.cancel', 'cancel'))}</button>` +
      `<div id="pttButton" role="button" tabindex="0" aria-label="${esc(label('ptt.hold', 'Hold to talk'))}">` +
      `<span id="pttGlyph">◉</span><span id="pttLabel">${esc(label('ptt.hold', 'Hold to talk'))}</span>` +
      `<canvas id="pttWave" width="200" height="30"></canvas></div>` +
      `<button id="pttKeyboard" class="btn-plain" title="${esc(label('ptt.type', 'Type instead'))}">⌨</button>`;
    const button = wrap.querySelector('#pttButton');
    const cancelBtn = wrap.querySelector('#pttCancel');
    const kb = wrap.querySelector('#pttKeyboard');

    let startX = 0;
    button.addEventListener('pointerdown', (e) => {
      e.preventDefault();
      button.setPointerCapture(e.pointerId);
      pointerDown = true; slidPast = false; startX = e.clientX;
      if (!HOST.pttPress(space)) {
        // Not now: mic dialog is up, or the engine is warming. Stay idle.
        pointerDown = false;
        setStatus(label('ptt.notready', 'Preparing offline voice…'));
      }
    });
    button.addEventListener('pointermove', (e) => {
      if (!pointerDown) return;
      const dx = e.clientX - startX;
      const cancelling = dx < -72;
      if (cancelling !== slidPast) {
        slidPast = cancelling;
        wrap.classList.toggle('ptt-cancelling', cancelling);
        setStatus(cancelling ? label('ptt.slidecancel', 'release to cancel')
                             : label('ptt.release', 'release to send'));
      }
    });
    const up = () => {
      if (!pointerDown) return;
      pointerDown = false;
      wrap.classList.remove('ptt-cancelling');
      if (slidPast) HOST.pttCancel(); else HOST.pttRelease();
    };
    button.addEventListener('pointerup', up);
    button.addEventListener('pointercancel', () => { pointerDown = false; HOST.pttCancel(); });
    // Keyboard accessibility: space/enter press-and-hold semantics are a
    // poor fit for a hold gesture, so keys TOGGLE: first press starts,
    // second sends. Cancel via the visible button or Escape.
    button.addEventListener('keydown', (e) => {
      if (e.key === ' ' || e.key === 'Enter') {
        e.preventDefault();
        if (lastState === 'idle') HOST.pttPress(space); else HOST.pttRelease();
      } else if (e.key === 'Escape') {
        HOST.pttCancel();
      }
    });
    cancelBtn.onclick = () => HOST.pttCancel();
    kb.onclick = () => showComposer(true);
    return { wrap, button, status: wrap.querySelector('#pttLabel'), wave: wrap.querySelector('#pttWave'), cancelBtn };
  }

  function setStatus(text) {
    if (ui) ui.status.textContent = text;
  }

  const waveBars = new Array(24).fill(0);
  // Auto-gain: a quiet voice on a far mic still draws a living wave. The
  // running peak decays, so the scale follows the speaker, not a constant.
  let wavePeak = 1500;
  function drawWave(amp) {
    if (!ui) return;
    wavePeak = Math.max(1500, wavePeak * 0.97, amp);
    waveBars.push(Math.min(1, amp / wavePeak)); waveBars.shift();
    const g = ui.wave.getContext('2d');
    if (!g) return;
    const { width, height } = ui.wave;
    g.clearRect(0, 0, width, height);
    const acc = getComputedStyle(document.documentElement).getPropertyValue('--acc').trim() || '#d39ae7';
    g.fillStyle = acc;
    const w = width / waveBars.length;
    waveBars.forEach((v, i) => {
      const h = Math.max(2, v * height);
      g.fillRect(i * w + 1, (height - h) / 2, w - 2, h);
    });
  }

  /** Swap the ordinary composer for the PTT surface (or back). */
  function showComposer(typing) {
    const composer = document.getElementById('composer');
    if (!composer || !ui) return;
    composer.style.display = typing ? '' : 'none';
    ui.wrap.style.display = typing ? 'none' : '';
  }

  function mountFor(space) {
    const composer = document.getElementById('composer');
    if (!composer) return;
    const enabled = hostReady() && space && state(space).enabled;
    if (!enabled) {
      // Restore ONLY what this module took. When no PTT surface was ever
      // mounted, the composer's visibility belongs to switchView — and
      // this line used to clobber it on every poll, resurrecting the chat
      // box under the Objects view. Even when tearing down a real swap,
      // hand the composer back in the state the CURRENT view wants, not
      // unconditionally visible.
      if (ui) {
        ui.wrap.remove(); ui = null; uiSpace = null;
        const chatting = typeof pubView === 'undefined' || pubView === 'chat';
        composer.style.display = chatting ? '' : 'none';
      }
      return;
    }
    if (ui && uiSpace === space) return;
    if (ui) ui.wrap.remove();
    ui = buildUI(space);
    uiSpace = space;
    composer.parentElement.insertBefore(ui.wrap, composer);
    showComposer(false);
  }

  // ---- pushes from the host ----
  window.quietPtt = function quietPtt(e) {
    if (!ui) return;
    lastState = e.state || 'idle';
    switch (lastState) {
      case 'arming':
      case 'recording':
        ui.wrap.classList.add('ptt-live');
        ui.cancelBtn.style.display = '';
        setStatus(e.limit ? label('ptt.limit', 'time limit — sending')
                          : label('ptt.release', 'release to send'));
        if (typeof e.amplitude === 'number') drawWave(e.amplitude);
        break;
      case 'finalizing':
        setStatus(label('ptt.recognizing', 'recognizing…'));
        break;
      case 'sending':
        setStatus(e.transcript ? `“${e.transcript}”` : label('ptt.sending', 'sending…'));
        break;
      default: { // idle
        ui.wrap.classList.remove('ptt-live', 'ptt-cancelling');
        wavePeak = 1500;
        ui.cancelBtn.style.display = 'none';
        waveBars.fill(0);
        if (e.error === 'empty') setStatus(label('ptt.empty', "Didn't catch that — hold and try again"));
        else if (e.error === 'mic_unavailable') setStatus(label('ptt.nomic', 'Microphone unavailable'));
        else if (e.error === 'engine_unavailable') setStatus(label('ptt.notready', 'Preparing offline voice…'));
        else if (e.error === 'asr_failed') setStatus(label('ptt.asrfail', "Couldn't recognize — try again"));
        else if (e.error === 'send_failed') setStatus(label('ptt.sendfail', 'Not sent — hold to retry'));
        else setStatus(label('ptt.hold', 'Hold to talk'));
      }
    }
  };

  // The host says a message from this space is sounding right now: a
  // quiet chip near the PTT surface, nothing louder (groom §32).
  window.quietSpeaking = function quietSpeaking(space, on) {
    if (!ui || uiSpace !== space) return;
    ui.wrap.classList.toggle('ptt-speaking', !!on);
    if (on && lastState === 'idle') setStatus('∿ ' + label('ptt.speaking', 'speaking'));
    else if (!on && lastState === 'idle') setStatus(label('ptt.hold', 'Hold to talk'));
  };

  window.quietRadio = function quietRadio(space, st) {
    remember(space, { enabled: !!st.enabled, autoplay: !!st.autoplay });
    if (typeof current !== 'undefined' && current === space) mountFor(space);
    // the settings row, if rendered, re-reads the echo on next paint
  };

  /** Settings rows for the space panel — called by app.js beside the
   *  other sections; renders nothing when there is no host. */
  function renderSection(mbox, space) {
    if (!hostReady() || !space) return;
    const st = state(space);
    const h = document.createElement('h3');
    h.textContent = label('radio.title', 'Radio');
    mbox.appendChild(h);
    const row = document.createElement('div');
    row.className = 'member radio-row';
    row.innerHTML =
      `<div class="mhead"><span class="mname">${esc(label('radio.mode', 'Radio mode'))}</span>` +
      `<button class="btn-plain radio-toggle">${st.enabled ? 'ON' : 'OFF'}</button></div>` +
      `<div class="caps">${esc(label('radio.hint', 'Hold to talk; new messages can be spoken automatically.'))}</div>`;
    row.querySelector('.radio-toggle').onclick = () => {
      const cur = state(space);
      if (setMode(space, !cur.enabled, cur.autoplay)) {
        mountFor(space);
        row.querySelector('.radio-toggle').textContent = !cur.enabled ? 'ON' : 'OFF';
      }
    };
    mbox.appendChild(row);
    if (st.enabled) {
      const auto = document.createElement('div');
      auto.className = 'member radio-row';
      auto.innerHTML =
        `<div class="mhead"><span class="mname">${esc(label('radio.autoplay', 'Speak new messages'))}</span>` +
        `<button class="btn-plain radio-toggle">${st.autoplay ? 'ON' : 'OFF'}</button></div>`;
      auto.querySelector('.radio-toggle').onclick = () => {
        const cur = state(space);
        if (setMode(space, cur.enabled, !cur.autoplay)) {
          auto.querySelector('.radio-toggle').textContent = !cur.autoplay ? 'ON' : 'OFF';
        }
      };
      mbox.appendChild(auto);
    }
  }

  return { renderSection, mountFor, enabled: (space) => state(space).enabled };
})();
