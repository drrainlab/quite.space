// @ts-check
// Post atmosphere, on the reading side.
//
// THE PRODUCT RULE THIS FILE EXISTS TO ENFORCE:
//
//   A post in the feed stays an ordinary, readable post. The atmosphere
//   begins only after a deliberate entry.
//
// So the feed gets a MARKER — a few words saying there is another layer here
// — and never a running scene. Not as a performance concession, though it is
// also that: a feed of twenty live canvases is a feed nobody can read, and an
// immersive mode you fall into by scrolling is not a mode anyone chose. The
// marker announces; entering is an act.
//
// The reading view then offers two doors, because sound and motion are
// different decisions and bundling them takes one of them away:
//
//   Open with sound   the scene, and the post's own ambient bed
//   Open quiet        the scene, in silence
//
// Everything below sits on machinery that already exists and is already
// bounded: SCENES resolves an allowlisted id or nothing (ADR-013 invariant 2),
// STAGE owns the only animation loop and the mode ladder, BRUSH owns the
// photosensitivity cap, and AUDIO decides who is allowed to make noise. This
// file adds no new authority of its own — it wires those together and handles
// the one thing none of them can: what a person sees when the scene is
// missing, declined, or unknown (invariant 1 — the renderer may degrade, the
// meaning may not).

const ATMO = (() => {
  /** Whether sound is ever offered, and whether a yes is remembered. */
  const SOUND_KEY = 'qp.atmosphere.sound';   // ask | never | remember
  /** Set when the person has said yes once and asked us to remember. */
  const CONSENT_KEY = 'qp.atmosphere.consent';

  /** @returns {'ask'|'never'|'remember'} */
  function soundMode() {
    const v = localStorage.getItem(SOUND_KEY);
    return v === 'never' || v === 'remember' ? v : 'ask';
  }

  /** @param {'ask'|'never'|'remember'} m */
  function setSoundMode(m) {
    localStorage.setItem(SOUND_KEY, m);
    if (m !== 'remember') localStorage.removeItem(CONSENT_KEY);
    if (typeof syncSettingsUI === 'function') syncSettingsUI();
  }

  /** The palette a recipe supplies, as plain hex, ground first. */
  function paletteOf(a) {
    const pal = ((a.visual && a.visual.palette) || [])
      .map(t => String(t.hex || ''))
      .filter(h => /^#[0-9a-fA-F]{6}$/.test(h));
    // Index 0 is the ground the wash pulls toward. A recipe that gave no
    // palette gets the reading surface's own background rather than black,
    // so a scene never paints a dark rectangle over a light theme.
    if (!pal.length) return [surfaceInk(), '#7ab4e0'];
    return pal;
  }

  /** The reading surface's background, sampled rather than assumed. */
  function surfaceInk() {
    try {
      const el = document.getElementById('pubArticle') || document.body;
      const c = getComputedStyle(el).backgroundColor;
      const m = /rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(c);
      if (!m) return '#0b1020';
      const h = n => Number(n).toString(16).padStart(2, '0');
      return '#' + h(m[1]) + h(m[2]) + h(m[3]);
    } catch { return '#0b1020'; }
  }

  /** Recipe params arrive as a list of {name, value}; scenes want a map. */
  function paramsOf(a) {
    /** @type {Record<string, number>} */
    const out = {};
    for (const p of (a.visual && a.visual.params) || []) {
      if (p && typeof p.name === 'string') out[p.name] = Number(p.value) || 0;
    }
    return out;
  }

  // ---- the feed marker -----------------------------------------------------

  /**
   * A badge saying a post carries an atmosphere. Deliberately inert: no
   * canvas, no audio, no fetch. It is a signpost, not a preview.
   *
   * It names the scene when this build knows it and says nothing specific
   * when it does not — claiming "Drift" for an id we cannot render would be
   * describing something the reader will never see.
   * @param {any} atmosphere
   */
  function marker(atmosphere) {
    if (!atmosphere || !atmosphere.visual) return null;
    const el = document.createElement('span');
    el.className = 'atmo-marker';
    const known = typeof SCENES !== 'undefined' &&
      SCENES.get(String(atmosphere.visual.scene || ''));
    const hasSound = !!(atmosphere.audio && atmosphere.audio.asset);
    el.textContent = (known ? known.label : 'Atmosphere') + (hasSound ? ' · sound' : '');
    el.title = hasSound
      ? 'This post has an atmosphere and an ambient sound. Open it to enter.'
      : 'This post has an atmosphere. Open it to enter.';
    return el;
  }

  // ---- the bed -------------------------------------------------------------

  let bed = null; // { el, ctx, analyser, data, owner, gain }

  /** Stop and forget the bed, whatever state it is in. */
  function stopBed() {
    if (!bed) return;
    const b = bed;
    bed = null;
    try { b.el.pause(); b.el.src = ''; } catch { /* already gone */ }
    try { b.ctx && b.ctx.close(); } catch { /* already closed */ }
    AUDIO.release(b.owner);
  }

  /**
   * Start the post's own ambient bed, if anything else will let us.
   *
   * Returns a reason string when it declines, so the reader is told rather
   * than left wondering why the scene is silent. The bed never preempts:
   * a listening session is other people hearing the same thing at the same
   * time, and a background is not worth interrupting that.
   *
   * @param {any} audio the recipe's audio block
   * @param {string} postId
   * @returns {{ok: true} | {ok: false, why: string}}
   */
  function startBed(audio, postId) {
    stopBed();
    if (!audio || !audio.asset) return { ok: false, why: 'this post has no sound' };
    const owner = 'bed:' + postId;
    if (!AUDIO.request(owner, 'bed', () => stopBed())) {
      return { ok: false, why: AUDIO.busyReason() || 'something else is playing' };
    }
    try {
      const el = new Audio();
      el.src = assetURL(audio.asset);
      el.loop = audio.mode !== 'once';
      el.crossOrigin = 'anonymous';
      const target = Math.min(1, Math.max(0, (Number(audio.gain) || 700) / 1000));
      const fadeIn = Math.min(10000, Number(audio.fade_in_ms) || 0);
      el.volume = fadeIn > 0 ? 0 : target;

      // An analyser, and ONLY over this element. A scene that asked to react
      // to sound reacts to the bed it was published with — never the
      // microphone, never another post, never the system output.
      let ctx = null, analyser = null, data = null;
      try {
        const AC = window.AudioContext || /** @type {any} */ (window).webkitAudioContext;
        if (AC) {
          ctx = new AC();
          const src = ctx.createMediaElementSource(el);
          analyser = ctx.createAnalyser();
          analyser.fftSize = 256;
          data = new Uint8Array(analyser.frequencyBinCount);
          src.connect(analyser);
          analyser.connect(ctx.destination);
          if (ctx.state === 'suspended') ctx.resume();
        }
      } catch {
        // No analyser is a scene that stays calm, not a scene that breaks.
        ctx = null; analyser = null; data = null;
      }

      bed = { el, ctx, analyser, data, owner, gain: target };
      el.play().catch(() => { /* autoplay refused: the poster is still right */ });
      if (fadeIn > 0) rampVolume(el, 0, target, fadeIn);
      return { ok: true };
    } catch (e) {
      AUDIO.release(owner);
      return { ok: false, why: 'the sound could not be opened' };
    }
  }

  /** A linear volume ramp on a timer — no Web Audio needed, so it always works. */
  function rampVolume(el, from, to, ms) {
    const steps = Math.max(1, Math.round(ms / 50));
    let i = 0;
    const id = setInterval(() => {
      i++;
      const v = from + (to - from) * (i / steps);
      try { el.volume = Math.min(1, Math.max(0, v)); } catch { /* gone */ }
      if (i >= steps || !bed || bed.el !== el) clearInterval(id);
    }, 50);
  }

  /**
   * How loud the bed is right now, 0..1 — the one live value a scene may see.
   * Zero whenever there is no bed, which is the common case and must look
   * deliberate rather than broken.
   */
  function level() {
    if (!bed || !bed.analyser || !bed.data) return 0;
    try {
      bed.analyser.getByteFrequencyData(bed.data);
      let sum = 0;
      for (let i = 0; i < bed.data.length; i++) sum += bed.data[i];
      // Mean bin energy, normalised and gently curved so a quiet bed still
      // moves the scene a little instead of sitting at nothing.
      return Math.min(1, Math.pow(sum / (bed.data.length * 255), 0.6));
    } catch { return 0; }
  }

  // ---- the reading view ----------------------------------------------------

  let mounted = null; // { host }
  /** Keeps the still frame the same size as the region it sits in. */
  let stillRO = null;

  /** Take down whatever is playing. Safe at any time, including twice. */
  function unmount() {
    stopBed();
    if (stillRO) { stillRO.disconnect(); stillRO = null; }
    if (typeof STAGE !== 'undefined') STAGE.clear();
    mounted = null;
  }

  /**
   * Put a post's atmosphere into its reading view.
   *
   * Renders the poster and the two doors; entering starts the scene and, if
   * that door had sound on it, the bed. Nothing moves and nothing sounds
   * until then.
   *
   * @param {HTMLElement} host the article's atmosphere region
   * @param {any} atmosphere the recipe as projected by the node
   * @param {string} postId
   */
  function mount(host, atmosphere, postId) {
    unmount();
    if (!host || !atmosphere || !atmosphere.visual) return;
    mounted = { host };
    host.classList.add('atmo-host');
    host.innerHTML = '';

    const fall = atmosphere.fallback || {};
    const scene = typeof SCENES !== 'undefined'
      ? SCENES.make(String(atmosphere.visual.scene || ''), {
          seed: Number(atmosphere.visual.seed) || 0,
          params: paramsOf(atmosphere),
        })
      : null;

    // The poster sits underneath for the whole life of the view. It is what
    // reduced motion, the minimal render mode, a space that asked for
    // stillness, a scene id this build does not know, and a scene that threw
    // all resolve to — one answer for every way the picture can be absent.
    if (fall.poster) {
      const img = document.createElement('img');
      img.className = 'atmo-poster';
      img.alt = '';
      autoMediaSrc(img, fall.poster);
      host.appendChild(img);
    }

    // No poster asset? Draw the scene's own first frame and stop.
    //
    // Every scene opens settled rather than from an empty field (see WARMUP
    // in scenes/field.js), which is what makes one frame worth looking at —
    // the plan asks each scene for a poster-quality first frame and this is
    // where that gets spent. It is a single draw with no loop, so it is
    // still true that nothing moves before the reader asks: a still picture
    // is exactly what reduced motion wants, not something to withhold from
    // it. The cost is one frame, once, and it replaces an empty box.
    if (!fall.poster && scene) stillFrame(host, scene, atmosphere);

    // The words are required by the contract and are the last line of the
    // fallback: a reader who gets no picture at all still gets the author's
    // description of what the picture was.
    const words = document.createElement('p');
    words.className = 'atmo-fallback';
    words.textContent = String(fall.text || '');
    host.appendChild(words);

    const doors = document.createElement('div');
    doors.className = 'atmo-doors';
    host.appendChild(doors);

    const say = document.createElement('span');
    say.className = 'atmo-note';
    doors.appendChild(say);

    const hasSound = !!(atmosphere.audio && atmosphere.audio.asset);

    /** @param {boolean} withSound */
    function enter(withSound) {
      doors.querySelectorAll('button').forEach(b => b.remove());
      const notes = [];
      // From here the stage owns the canvas. Let go of the still, or the
      // observer repaints a frozen frame over a running scene on every
      // resize — which looks exactly like the scene stopping.
      if (stillRO) { stillRO.disconnect(); stillRO = null; }
      host.querySelectorAll(':scope > canvas.stage-canvas').forEach(c => c.remove());
      if (scene) {
        const ok = STAGE.play(host, scene, {
          palette: paletteOf(atmosphere),
          seed: Number(atmosphere.visual.seed) || 0,
          level,
        });
        // Declining is a normal outcome, not a failure — but it has to be
        // SAID, or a person who set reduced motion months ago just sees a
        // still picture and assumes the post is broken.
        if (!ok) notes.push(stageDeclinedBecause());
      } else {
        notes.push('this version does not know that scene');
      }
      if (withSound) {
        const r = startBed(atmosphere.audio, postId);
        if (!r.ok) notes.push(r.why);
        else if (soundMode() === 'remember') localStorage.setItem(CONSENT_KEY, '1');
      }
      say.textContent = notes.length ? 'Showing the still image — ' + notes.join('; ') : '';
      const leave = document.createElement('button');
      leave.className = 'btn-plain'; leave.textContent = 'Leave';
      leave.onclick = () => { unmount(); mount(host, atmosphere, postId); };
      doors.appendChild(leave);
    }

    if (hasSound && soundMode() !== 'never') {
      const b = document.createElement('button');
      b.className = 'btn-filled'; b.textContent = 'Open with sound';
      b.onclick = () => enter(true);
      doors.appendChild(b);
    }
    const q = document.createElement('button');
    q.className = 'btn-tinted';
    q.textContent = hasSound && soundMode() !== 'never' ? 'Open quiet' : 'Open';
    q.onclick = () => enter(false);
    doors.appendChild(q);

    // Remembering a yes is a convenience, never a default: it only applies
    // when the person asked for it AND has already said yes once here.
    if (hasSound && soundMode() === 'remember' && localStorage.getItem(CONSENT_KEY)) {
      enter(true);
    }
  }

  /**
   * One frame of the scene, painted into a static canvas and left there.
   *
   * Deliberately not STAGE.play: the stage owns the animation loop and the
   * mode ladder, and neither applies to a picture that will never change.
   * Going through BRUSH directly keeps the palette clamp and every drawing
   * bound that a running scene has — the only thing missing is the second
   * frame.
   */
  function stillFrame(host, scene, atmosphere) {
    // TRACK the host rather than guess when it is ready. The first attempt
    // here waited for the region to look "big enough" and drew once — and a
    // region that measured 30x1335 while the surrounding column was still
    // collapsing passed that test comfortably, so the still was painted into
    // a shape the reader never sees and never corrected. There is no
    // threshold that tells a settled layout from an unsettled one; observing
    // the host and redrawing on every change needs no such judgement, and it
    // is what the stage already does for a running scene.
    if (stillRO) { stillRO.disconnect(); stillRO = null; }
    if (window.ResizeObserver) {
      stillRO = new ResizeObserver(() => {
        // A running scene owns the canvas; the still must not fight it.
        if (mounted && mounted.host === host && !STAGE.running()) {
          drawStill(host, scene, atmosphere);
        }
      });
      stillRO.observe(host);
    }
    drawStill(host, scene, atmosphere);
  }

  /** Paint one frame into a fresh canvas, replacing any earlier still. */
  function drawStill(host, scene, atmosphere) {
    try {
      const r = host.getBoundingClientRect();
      if (r.width < 1 || r.height < 1) return;
      host.querySelectorAll(':scope > canvas.stage-canvas').forEach(c => c.remove());
      const w = Math.max(1, Math.round(r.width)), h = Math.max(1, Math.round(r.height));
      const cv = document.createElement('canvas');
      cv.className = 'stage-canvas';
      cv.setAttribute('aria-hidden', 'true');
      // Same cap as the stage, for the same reason.
      let scale = Math.min(2, window.devicePixelRatio || 1);
      if (w * h * scale * scale > 256000) scale = Math.sqrt(256000 / (w * h));
      cv.width = Math.max(1, Math.round(w * scale));
      cv.height = Math.max(1, Math.round(h * scale));
      const ctx = cv.getContext('2d');
      if (!ctx) return;
      ctx.setTransform(scale, 0, 0, scale, 0, 0);
      host.prepend(cv);
      const b = BRUSH.make({
        width: w, height: h, palette: paletteOf(atmosphere),
        seed: Number(atmosphere.visual.seed) || 0, ctx, level: () => 0,
      });
      b.begin();
      scene.draw(b.surface, 0, 1 / 30);
      b.end();
    } catch { /* no still is a fallback text, which is already there */ }
  }

  /** Why the stage said no, in words a reader can act on. */
  function stageDeclinedBecause() {
    if (!STAGE.wanted()) return 'atmosphere is switched off in settings';
    if (MODES.reducedMotion()) return 'your system asks for reduced motion';
    if (typeof renderMode === 'function' && renderMode() === 'minimal') {
      return 'the minimal render mode is on';
    }
    if (document.hidden) return 'this tab is in the background';
    return 'this space asks for stillness';
  }

  return { marker, mount, unmount, level, soundMode, setSoundMode, stopBed };
})();

if (typeof window !== 'undefined') {
  window.ATMO = ATMO;
  window.setAtmosphereSound =
    (/** @type {'ask'|'never'|'remember'} */ m) => ATMO.setSoundMode(m);
}
