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
  // srcFor/posterInto are the URL seam (PM-3): the transient post reader
  // mounts atmosphere through SESSION routes, and pointing the hardcoded
  // current-space assetURL at a space the node does not hold is exactly
  // how a preview quietly stops being one. Set per-mount; reset on unmount.
  let srcFor = null;    // (assetId) => url — bed audio
  let posterInto = null; // (img, assetId) => void — the fall poster
  let ignoreRemembered = false;
  // soundGate(assetId, proceed): the reader's chance to FETCH the bed's
  // bytes after the sound consent and before the element needs them.
  let soundGate = null;

  function startBed(audio, postId) {
    stopBed();
    if (!audio || !audio.asset) return { ok: false, why: 'this post has no sound' };
    const owner = 'bed:' + postId;
    if (!AUDIO.request(owner, 'bed', () => stopBed())) {
      return { ok: false, why: AUDIO.busyReason() || 'something else is playing' };
    }
    try {
      const el = new Audio();
      el.src = srcFor ? srcFor(audio.asset) : assetURL(audio.asset);
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
  //
  // AM-6: the atmosphere is a LAYER, not a block. The article does not
  // contain an atmosphere region — the article's own container becomes the
  // shell, the scene lives on an absolutely-positioned stage BEHIND all of
  // its content, a scrim sits between them for readability, and a slim bar
  // in the content flow is the controller. The post exists inside the
  // atmosphere; the atmosphere is not an insert inside the post.

  let mounted = null; // { shell, stage, scrim, bar, scene, atmosphere, postId }
  /** Keeps the still frame the same size as the stage it sits in. */
  let stillRO = null;

  /** Take down whatever is playing and undo the shell. Safe to call twice. */
  function unmount() {
    stopBed();
    srcFor = null;
    posterInto = null;
    ignoreRemembered = false;
    soundGate = null;
    if (stillRO) { stillRO.disconnect(); stillRO = null; }
    if (typeof STAGE !== 'undefined') STAGE.clear();
    if (mounted) {
      const m = mounted;
      mounted = null;
      try { m.stage.remove(); } catch { /* already gone */ }
      try { m.scrim.remove(); } catch { /* already gone */ }
      m.shell.classList.remove('atmo-shell');
      if (m.bar) m.bar.innerHTML = '';
    }
  }

  /**
   * Is this exact atmosphere already mounted and alive for this post?
   * A re-render of the same article must not pass through unmount/mount —
   * that stops the scene and the bed, which is how a REACTION on a post
   * killed its whole atmosphere: the resonance row's onChange refetches the
   * post and rebuilds the article.
   * @param {string} postId @param {any} atmosphere
   */
  function holds(postId, atmosphere) {
    return !!(mounted && mounted.postId === postId &&
      mounted.recipeSig === JSON.stringify(atmosphere));
  }

  /**
   * Lift the live atmosphere out of the DOM before the article is wiped.
   * Nothing is stopped: the scene keeps drawing (a detached canvas draws
   * fine) and the bed keeps playing (its <audio> was never in the DOM).
   * Returns the pieces the caller must put back, or null if nothing is up.
   */
  function detach() {
    if (!mounted) return null;
    mounted.stage.remove();
    mounted.scrim.remove();
    if (mounted.bar) mounted.bar.remove();
    mounted.shell.classList.remove('atmo-shell');
    return { bar: mounted.bar };
  }

  /**
   * Put a detached atmosphere back into (a rebuilt) shell. The caller has
   * already placed the bar node into the content flow; the layers go back
   * underneath. All the closures inside mount() keep working because the
   * NODES are the same — only their position in the document changed.
   * @param {HTMLElement} shell
   */
  function reattach(shell) {
    if (!mounted) return;
    shell.prepend(mounted.scrim);
    shell.prepend(mounted.stage);
    shell.classList.add('atmo-shell');
    mounted.shell = shell;
  }

  /**
   * Put a post's atmosphere BEHIND its reading view.
   *
   * The shell is the article container itself. mount() prepends two layers —
   * the stage (canvas host) and the scrim (readability) — and renders the
   * controller into `opts.bar`, a normal in-flow element the article
   * provides. Nothing moves and nothing sounds until a door is used, unless
   * `opts.enter` carries a mode the person already chose in the feed: that
   * click was the consent, so arriving in the chosen state honours it
   * rather than asking twice.
   *
   * @param {HTMLElement} shell the article container (becomes .atmo-shell)
   * @param {any} atmosphere the recipe as projected by the node
   * @param {string} postId
   * @param {{bar?: HTMLElement, enter?: 'still'|'quiet'|'sound'}} [opts]
   */
  function mount(shell, atmosphere, postId, opts) {
    unmount();
    if (!shell || !atmosphere || !atmosphere.visual) return;
    const bar = (opts && opts.bar) || null;
    srcFor = (opts && opts.srcFor) || null;
    posterInto = (opts && opts.posterInto) || null;
    soundGate = (opts && opts.soundGate) || null;
    // A remembered "always with sound" was recorded for HELD spaces; in a
    // transient preview it would be an audio fetch with no per-post
    // consent. The reader passes ignoreRemembered and the standing
    // instruction simply does not apply there.
    ignoreRemembered = !!(opts && opts.ignoreRemembered);

    const fall = atmosphere.fallback || {};
    const scene = typeof SCENES !== 'undefined'
      ? SCENES.make(String(atmosphere.visual.scene || ''), {
          seed: Number(atmosphere.visual.seed) || 0,
          params: paramsOf(atmosphere),
        })
      : null;

    // The two layers. Stage first, scrim second: both are z-index 0, so DOM
    // order stacks the scrim above the picture — and everything the article
    // already contains is forced above both by the .atmo-shell child rule.
    const stage = document.createElement('div');
    stage.className = 'atmo-stage';
    stage.setAttribute('aria-hidden', 'true');
    const scrim = document.createElement('div');
    scrim.className = 'atmo-scrim';
    scrim.setAttribute('aria-hidden', 'true');
    shell.prepend(scrim);
    shell.prepend(stage);
    shell.classList.add('atmo-shell');
    styleScrim(scrim, atmosphere);

    mounted = { shell, stage, scrim, bar, scene, atmosphere, postId,
                // The recipe's identity, so a re-render can tell "the same
                // atmosphere, keep it running" from "an edit changed it,
                // remount". JSON is fine here: the projection is canonical
                // enough that an unchanged recipe stringifies identically.
                recipeSig: JSON.stringify(atmosphere) };

    // The poster (an author-supplied image) or the deterministic still — the
    // first frame re-derived from the seed, nothing cached anywhere. Either
    // way the post is atmospheric from its first paint, motionless and
    // silent: this is also exactly what reduced motion, the minimal mode, a
    // space that asked for stillness and an unknown scene id all resolve to.
    if (fall.poster) {
      const img = document.createElement('img');
      img.className = 'atmo-poster';
      img.alt = '';
      if (posterInto) posterInto(img, fall.poster);
      else autoMediaSrc(img, fall.poster);
      stage.appendChild(img);
    } else if (scene) {
      stillFrame(stage, scene, atmosphere);
    }

    const hasSound = !!(atmosphere.audio && atmosphere.audio.asset);
    const label = (typeof SCENES !== 'undefined' &&
      SCENES.get(String(atmosphere.visual.scene || ''))?.label) || 'Atmosphere';
    // Whether THIS mount's scene is actually on the stage. STAGE.running()
    // alone cannot answer that — it is false during a mechanical pause
    // (offscreen host, hidden tab) and during the person's own pause.
    let sceneLive = false;

    /** Rebuild the bar for one of its two states. */
    function renderBar(state, notes) {
      if (!bar) return;
      bar.innerHTML = '';
      bar.className = 'atmo-bar';

      const glyph = document.createElement('span');
      glyph.className = 'atmo-glyph' + (state === 'live' ? ' on' : '');
      glyph.textContent = state === 'live' ? '\u25CF' : '\u25CC';
      bar.appendChild(glyph);

      const name = document.createElement('span');
      name.className = 'atmo-bar-label';
      if (state === 'live') {
        // Say only what is true: a declined scene is not "moving", and a
        // person's pause is its own state, not motion.
        const bits = [label];
        if (sceneLive) bits.push(STAGE.paused() ? 'paused' : 'moving');
        if (bed) bits.push('sound');
        name.textContent = bits.join(' \u00B7 ');
      } else {
        name.textContent = label + ' atmosphere';
        // The author's words are the atmosphere's description AND its last
        // line of defence: title everywhere, visible text when the picture
        // cannot exist at all.
        name.title = String(fall.text || '');
      }
      bar.appendChild(name);

      const btn = (text, cls, fn) => {
        const b = document.createElement('button');
        b.className = cls; b.textContent = text; b.onclick = fn;
        bar.appendChild(b);
        return b;
      };

      if (state === 'live') {
        if (sceneLive) btn(STAGE.paused() ? 'Resume' : 'Pause', 'btn-plain', () => {
          // Pause freezes ON THE CURRENT FRAME — the canvas keeps what it
          // holds and nothing is redrawn. A pause that jumped back to the
          // opening frame would read as the composition resetting, and a
          // reset is what Leave is for.
          if (STAGE.paused()) STAGE.resume(); else STAGE.pause();
          renderBar('live', notes);
        });
        if (hasSound && soundMode() !== 'never') {
          btn(bed ? 'Mute' : 'Unmute', 'btn-plain', () => {
            if (bed) stopBed();
            else {
              const r = startBed(atmosphere.audio, postId);
              if (!r.ok) notes = [r.why];
            }
            renderBar('live', notes);
          });
        }
        btn('Leave', 'btn-plain', () => {
          // The exit IS the deterministic reset: stop everything and return
          // to the still first frame with the doors.
          sceneLive = false;
          STAGE.clear();
          stopBed();
          if (!fall.poster && scene) stillFrame(stage, scene, atmosphere);
          renderBar('still');
        });
      } else {
        if (hasSound && soundMode() !== 'never') {
          btn('Open with sound', 'btn-filled', () => enter(true));
        }
        // A quiet door needs a picture to open onto; without a known scene
        // there is no motion to enter and the door would be a false promise.
        if (scene) {
          btn(hasSound && soundMode() !== 'never' ? 'Open quiet' : 'Open',
              'btn-tinted', () => enter(false));
        }
        addTakeButton();
      }

      const say = document.createElement('span');
      say.className = 'atmo-note';
      // No scene at all: the author's words carry the meaning (ADR-013 §1).
      const parts = [];
      if (!scene && state !== 'live') {
        if (fall.text) parts.push(String(fall.text));
        parts.push('this version does not know that scene');
      }
      for (const n of notes || []) parts.push(n);
      say.textContent = parts.join(' \u2014 ');
      bar.appendChild(say);
    }

    /** @param {boolean} withSound */
    function enter(withSound) {
      const notes = [];
      // From here the stage owns the canvas. Let go of the still, or its
      // observer repaints a frozen frame over the running scene on resize.
      if (stillRO) { stillRO.disconnect(); stillRO = null; }
      stage.querySelectorAll(':scope > canvas.stage-canvas').forEach(c => c.remove());
      if (scene) {
        sceneLive = STAGE.play(stage, scene, {
          palette: paletteOf(atmosphere),
          seed: Number(atmosphere.visual.seed) || 0,
          level,
        });
        // Declining is a normal outcome, not a failure — but it has to be
        // SAID, or a person who set reduced motion months ago just sees a
        // still picture and assumes the post is broken.
        if (!sceneLive) {
          notes.push('showing the still \u2014 ' + stageDeclinedBecause());
          if (!fall.poster) stillFrame(stage, scene, atmosphere);
        }
      }
      if (withSound && soundGate && atmosphere.audio && atmosphere.audio.asset) {
        // The consent already happened (the person pressed the sound
        // door); the gate fetches the bytes and re-enters when they are
        // there — or reports why they are not.
        const gate = soundGate;
        gate(atmosphere.audio.asset, (ok, why) => {
          if (ok) {
            const r = startBed(atmosphere.audio, postId);
            if (!r.ok) notes.push('quiet — ' + r.why);
          } else {
            notes.push('quiet — ' + (why || 'the sound never arrived'));
          }
          renderBar((!sceneLive && !bed) ? 'still' : 'live', notes);
        });
        renderBar((!sceneLive && !bed) ? 'still' : 'live',
          notes.concat(['fetching the sound…']));
        return;
      }
      if (withSound) {
        const r = startBed(atmosphere.audio, postId);
        if (!r.ok) notes.push(r.why);
        else if (soundMode() === 'remember') localStorage.setItem(CONSENT_KEY, '1');
      }
      // Nothing came up at all — no motion, no sound. That is not a "live"
      // state to control, it is the still state with an explanation, and the
      // doors stay so the person can try again when the reason lifts.
      if (!sceneLive && !bed) { renderBar('still', notes); return; }
      renderBar('live', notes);
    }

    /** "Use this atmosphere" — the recipe, into a new draft of your own.
     *  Copies the FORM and names the source; never the sound (that asset
     *  belongs to the post that published it) and never the digest (the node
     *  computes lineage from the source it actually holds, so the claim is
     *  checkable rather than written by this client about itself). */
    function addTakeButton() {
      if (typeof openComposer !== 'function' || !scene) return;
      const take = document.createElement('button');
      take.className = 'btn-plain atmo-take';
      take.textContent = 'Use this atmosphere';
      take.title = 'Start a new post with this look';
      take.onclick = () => {
        const copy = JSON.parse(JSON.stringify(atmosphere));
        delete copy.audio;
        delete copy.reactive;
        copy.fallback = { text: String((atmosphere.fallback || {}).text || '') };
        copy.derived_from = { publication_id: postId };
        unmount();
        openComposer(null, '');
        composerDoc.atmosphere = copy;
        renderComposerAtmosphere();
      };
      bar.appendChild(take);
    }

    renderBar('still');

    // The mode carried in from the feed. A door click there was the consent;
    // arriving in the chosen state honours it rather than asking twice.
    const how = (opts && opts.enter) || 'still';
    if (how === 'sound') enter(true);
    else if (how === 'quiet') enter(false);
    // A plain open (still) with a remembered yes: the person's own standing
    // instruction, recorded at their request, outranks the quiet default.
    else if (!ignoreRemembered && hasSound && soundMode() === 'remember' &&
             localStorage.getItem(CONSENT_KEY)) {
      enter(true);
    }
  }

  /**
   * A deterministic still as a feed card's background — the first frame
   * re-derived from the seed (nothing is cached), or the author's poster
   * image when the recipe carries one, which is cheaper still.
   *
   * The ladder, and why:
   *   poster        an <img>, no canvas at all
   *   known scene   ONE canvas, drawn only while the card is in or near the
   *                 viewport and discarded when it scrolls far away — a long
   *                 feed must not accumulate a canvas per card
   *   unknown scene nothing; the marker already says "Atmosphere" honestly
   *
   * Registers its disposer as `card.__unmount` — the registry.js teardown
   * contract — and returns it. NO live loops here, ever: the feed rule.
   *
   * @param {HTMLElement} card
   * @param {any} atmosphere
   * @returns {(() => void) | null}
   */
  function cardStill(card, atmosphere) {
    if (!card || !atmosphere || !atmosphere.visual) return null;
    const fall = atmosphere.fallback || {};
    const layer = document.createElement('div');
    layer.className = 'pub-card-atmo';
    layer.setAttribute('aria-hidden', 'true');

    let dispose;
    if (fall.poster) {
      const img = document.createElement('img');
      img.className = 'atmo-poster';
      img.alt = '';
      if (posterInto) posterInto(img, fall.poster);
      else autoMediaSrc(img, fall.poster);
      layer.appendChild(img);
      card.prepend(layer);
      dispose = () => layer.remove();
    } else {
      const scene = typeof SCENES !== 'undefined'
        ? SCENES.make(String(atmosphere.visual.scene || ''), {
            seed: Number(atmosphere.visual.seed) || 0,
            params: paramsOf(atmosphere),
          })
        : null;
      if (!scene) return null;
      card.prepend(layer);
      let io = null;
      if (window.IntersectionObserver) {
        let drawn = false;
        io = new IntersectionObserver(es => {
          const near = es.some(e => e.isIntersecting);
          if (near && !drawn) {
            drawn = true;
            drawStill(layer, scene, atmosphere);
          } else if (!near && drawn) {
            drawn = false;
            layer.querySelectorAll('canvas').forEach(c => c.remove());
          }
        }, { rootMargin: '600px 0px' });
        io.observe(card);
      } else {
        drawStill(layer, scene, atmosphere);
      }
      dispose = () => { if (io) io.disconnect(); layer.remove(); };
    }
    // The registry teardown contract, so whoever wipes the feed can run it.
    /** @type {any} */ (card).__unmount = dispose;
    return dispose;
  }

  /**
   * The readability layer, tuned to the recipe rather than fixed: a bright
   * palette bleaches text faster, so its scrim is stronger. The gradient is
   * denser where the title and text begin and lighter toward the edges, so
   * the atmosphere stays most visible in the margins of the composition —
   * a state the post sits in, not wallpaper behind every letter.
   */
  function styleScrim(scrim, atmosphere) {
    const pal = paletteOf(atmosphere);
    const ground = pal[0];
    let maxLuma = 0;
    for (const h of pal.slice(1)) maxLuma = Math.max(maxLuma, BRUSH.luminance(h));
    const a = Math.min(0.8, 0.42 + 0.3 * maxLuma);
    const n = parseInt(ground.slice(1), 16);
    const rgb = `${(n >> 16) & 255},${(n >> 8) & 255},${n & 255}`;
    scrim.style.background =
      `radial-gradient(130% 110% at 50% 30%, rgba(${rgb},${a.toFixed(3)}) 0%, ` +
      `rgba(${rgb},${(a * 0.72).toFixed(3)}) 70%, rgba(${rgb},${(a * 0.45).toFixed(3)}) 100%)`;
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
        if (mounted && mounted.stage === host && !STAGE.running()) {
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

  // paletteOf/paramsOf/declineReason are exported for the authoring side.
  // The point of putting both directions in one file was that they share one
  // reading of the recipe; leaving these private would have meant the editor
  // reimplementing them, which is exactly how a preview drifts from a post.
  return { marker, mount, unmount, holds, detach, reattach, cardStill,
           level, soundMode, setSoundMode, stopBed,
           paletteOf, paramsOf, declineReason: stageDeclinedBecause };
})();

if (typeof window !== 'undefined') {
  window.ATMO = ATMO;
  window.setAtmosphereSound =
    (/** @type {'ask'|'never'|'remember'} */ m) => ATMO.setSoundMode(m);
}

// ---------------------------------------------------------------------------
// AUTHORING (AM-5)
//
// The composer side lives here rather than in publications.js so that both
// directions share one reading of the recipe — paletteOf, paramsOf and the
// permille convention are written once. An editor that converted parameters
// its own way would be the fastest route to a preview that does not match
// what gets published.
//
// The editor writes the CONTRACT's shape directly (permille integers, hex
// strings, a list of {name,value}) rather than a convenient intermediate that
// someone later has to map. What the sliders move is exactly what gets signed.
// ---------------------------------------------------------------------------

const ATMO_EDIT = (() => {
  /** Bounds restated from protocol/publication/atmosphere.go. */
  const MAX_PARAMS = 16, MAX_PALETTE = 4, MAX_FALLBACK = 200, MAX_FADE = 10000;

  /** A fresh recipe: the default scene, a seed, and the space's own colours. */
  function blank() {
    const id = (typeof SCENES !== 'undefined' && SCENES.list()[0]) || 'drift@1';
    return {
      visual: { scene: id, seed: rollSeed(), params: [], palette: defaultPalette() },
      fallback: { text: '' },
    };
  }

  /**
   * A seed the author can re-roll. It is a uint64 on the wire; JavaScript
   * numbers are exact to 2^53, so this stays inside that — a seed that lost
   * precision on the way through the editor would publish a different picture
   * from the one that was previewed.
   */
  function rollSeed() { return Math.floor(Math.random() * 9007199254740991); }

  /** Start from the space's own palette when it has one: an atmosphere should
   *  look like it belongs to the room it is published in. */
  function defaultPalette() {
    const out = [];
    try {
      const el = document.getElementById('content');
      const cs = el && getComputedStyle(el);
      for (const [name, prop] of [['ground', '--bg'], ['accent', '--acc'], ['warm', '--human']]) {
        const v = (cs && cs.getPropertyValue(prop).trim() || '').toLowerCase();
        if (!/^#[0-9a-fA-F]{6}$/.test(v)) continue;
        // Skip a colour the palette already has. Several of the theme's
        // variables coincide in most spaces — accent and human are the same
        // in every archetype that does not distinguish them — and a palette
        // offering the same swatch twice reads as a broken picker.
        if (out.some(t => t.hex === v)) continue;
        out.push({ name, hex: v });
      }
    } catch { /* fall through to the built-in */ }
    return out.length ? out : [
      { name: 'ground', hex: '#0b1020' },
      { name: 'accent', hex: '#7ab4e0' },
    ];
  }

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text != null) n.textContent = text;
    return n;
  }

  function row(label, control) {
    const r = el('div', 'atmo-edit-row');
    r.appendChild(el('span', 'atmo-edit-label', label));
    r.appendChild(control);
    return r;
  }

  /**
   * Render the atmosphere editor for a composer document.
   *
   * @param {HTMLElement} host
   * @param {any} doc the composer's document; doc.atmosphere is edited in place
   * @param {() => void} [onChange] called after every edit
   */
  function render(host, doc, onChange) {
    host.innerHTML = '';
    host.className = 'atmo-edit';
    const changed = () => { render(host, doc, onChange); if (onChange) onChange(); };

    if (!doc.atmosphere) {
      const add = el('button', 'btn-plain', '+ Atmosphere');
      add.type = 'button';
      add.title = 'Publish the way this post is meant to be read';
      add.onclick = () => { doc.atmosphere = blank(); changed(); };
      host.appendChild(add);
      return;
    }

    const a = doc.atmosphere;
    const head = el('div', 'atmo-edit-head');
    head.appendChild(el('strong', null, 'Atmosphere'));
    const drop = el('button', 'btn-plain', 'Remove');
    drop.type = 'button';
    drop.onclick = () => { delete doc.atmosphere; changed(); };
    head.appendChild(drop);
    host.appendChild(head);

    // --- the live preview, through the SAME renderer the reader gets -------
    // Not a mock and not a screenshot: if the preview used anything else, the
    // safety cap and the mode ladder would be absent from it and an author
    // would tune against a picture nobody else can see.
    const preview = el('div', 'atmo-edit-preview');
    host.appendChild(preview);
    playPreview(preview, a);

    // --- scene ------------------------------------------------------------
    const pick = el('select', 'atmo-edit-scene');
    for (const id of (typeof SCENES !== 'undefined' ? SCENES.list() : [])) {
      const o = el('option', null, SCENES.get(id).label + '  (' + id + ')');
      o.value = id;
      if (id === a.visual.scene) o.selected = true;
      pick.appendChild(o);
    }
    pick.onchange = () => {
      a.visual.scene = pick.value;
      // Parameters belong to a scene, so they do not survive a change of one.
      // Carrying them across would silently apply another scene's tuning under
      // the same names and produce a picture the author did not choose.
      a.visual.params = [];
      changed();
    };
    host.appendChild(row('Scene', pick));

    // --- seed -------------------------------------------------------------
    const seedWrap = el('div', 'atmo-edit-seed');
    const seedVal = el('code', null, String(a.visual.seed));
    const dice = el('button', 'btn-plain', '⚄ Reroll');
    dice.type = 'button';
    dice.onclick = () => { a.visual.seed = rollSeed(); changed(); };
    seedWrap.appendChild(seedVal);
    seedWrap.appendChild(dice);
    host.appendChild(row('Seed', seedWrap));

    // --- parameters, bounded by the scene's own declaration ---------------
    const def = typeof SCENES !== 'undefined' ? SCENES.get(a.visual.scene) : null;
    if (def) {
      const names = Object.keys(def.params).slice(0, MAX_PARAMS);
      const current = ATMO.paramsOf(a);
      for (const name of names) {
        const value = Object.prototype.hasOwnProperty.call(current, name)
          ? current[name] : def.params[name];
        const s = el('input', 'atmo-edit-slider');
        s.type = 'range'; s.min = '0'; s.max = '1000'; s.step = '10';
        s.value = String(value);
        const out = el('span', 'atmo-edit-num', String(value));
        s.oninput = () => { out.textContent = s.value; };
        s.onchange = () => {
          setParam(a, name, Number(s.value));
          changed();
        };
        const wrap = el('div', 'atmo-edit-slider-wrap');
        wrap.appendChild(s); wrap.appendChild(out);
        host.appendChild(row(name, wrap));
      }
    }

    // --- palette ----------------------------------------------------------
    const pal = el('div', 'atmo-edit-palette');
    (a.visual.palette || []).forEach((tok, i) => {
      const c = el('input', 'atmo-edit-colour');
      c.type = 'color'; c.value = tok.hex;
      c.title = i === 0 ? 'the ground everything settles toward' : tok.name;
      c.onchange = () => { tok.hex = c.value.toLowerCase(); changed(); };
      pal.appendChild(c);
    });
    if ((a.visual.palette || []).length < MAX_PALETTE) {
      const more = el('button', 'btn-plain', '+');
      more.type = 'button';
      more.title = 'add a colour';
      more.onclick = () => {
        a.visual.palette.push({ name: 'c' + a.visual.palette.length, hex: '#8fa6c4' });
        changed();
      };
      pal.appendChild(more);
    }
    if ((a.visual.palette || []).length > 1) {
      const less = el('button', 'btn-plain', '−');
      less.type = 'button';
      less.title = 'remove the last colour';
      less.onclick = () => { a.visual.palette.pop(); changed(); };
      pal.appendChild(less);
    }
    host.appendChild(row('Palette', pal));

    // --- the words, which are required -----------------------------------
    // ADR-013 invariant 1: the renderer may degrade, the meaning may not. A
    // reader who gets no picture — old client, reduced motion, an id we do
    // not know — still gets this, so the validator refuses a recipe without
    // it and the editor says so before the publish button does.
    const fall = el('textarea', 'atmo-edit-text');
    fall.maxLength = MAX_FALLBACK;
    fall.rows = 2;
    fall.placeholder = 'Describe it in words — this is what a reader sees when the picture cannot be shown';
    fall.value = a.fallback.text || '';
    host.appendChild(row('In words', fall));
    const warn = el('p', 'hint warn',
      'A description in words is required — it is what a reader gets when the picture cannot be shown.');
    host.appendChild(warn);
    // Typing must not re-render: the editor rebuilds itself on structural
    // changes, and doing that per keystroke would take the caret away
    // mid-sentence. So the warning is toggled in place instead.
    const syncWarn = () => { warn.hidden = !!(a.fallback.text || '').trim(); };
    fall.oninput = () => { a.fallback.text = fall.value; syncWarn(); };
    syncWarn();

    // --- sound ------------------------------------------------------------
    const sound = el('div', 'atmo-edit-sound');
    if (a.audio && a.audio.asset) {
      sound.appendChild(el('code', null, a.audio.asset.slice(0, 8) + '…'));
      const g = el('input', 'atmo-edit-slider');
      g.type = 'range'; g.min = '0'; g.max = '1000'; g.step = '10';
      g.value = String(a.audio.gain ?? 700);
      g.title = 'gain';
      g.onchange = () => { a.audio.gain = Number(g.value); if (onChange) onChange(); };
      sound.appendChild(g);
      const loop = el('button', 'btn-plain', a.audio.mode === 'once' ? 'once' : 'loop');
      loop.type = 'button';
      loop.onclick = () => { a.audio.mode = a.audio.mode === 'once' ? 'loop' : 'once'; changed(); };
      sound.appendChild(loop);
      const rm = el('button', 'btn-plain', 'Remove');
      rm.type = 'button';
      rm.onclick = () => { delete a.audio; changed(); };
      sound.appendChild(rm);
    } else {
      const pickAudio = el('button', 'btn-plain', 'Add a sound…');
      pickAudio.type = 'button';
      pickAudio.onclick = () => chooseAsset('audio/*', async f => {
        const r = await uploadAssetFile(f);
        a.audio = { asset: r.asset_id, mode: 'loop', gain: 700, fade_in_ms: 600, fade_out_ms: 900 };
        if (Number(a.audio.fade_in_ms) > MAX_FADE) a.audio.fade_in_ms = MAX_FADE;
        changed();
      });
      sound.appendChild(pickAudio);
    }
    host.appendChild(row('Sound', sound));

    // --- lineage, when this recipe came from somebody else's post ----------
    if (a.derived_from && (a.derived_from.publication_id || a.derived_from.recipe_hash)) {
      const note = el('p', 'hint',
        a.derived_from.publication_id
          ? 'Derived from an atmosphere in this space. The link is recorded when you publish.'
          : 'Derived from another atmosphere, recorded by digest only.');
      host.appendChild(note);
    }
  }

  /** Set one parameter without disturbing the order of the others. */
  function setParam(a, name, value) {
    a.visual.params = a.visual.params || [];
    const v = Math.max(0, Math.min(1000, Math.round(value)));
    const found = a.visual.params.find(p => p.name === name);
    if (found) { found.value = v; return; }
    a.visual.params.push({ name, value: v });
  }

  /** A one-shot file picker that does not disturb the composer's own input. */
  function chooseAsset(accept, fn) {
    const inp = document.createElement('input');
    inp.type = 'file';
    inp.accept = accept;
    inp.style.display = 'none';
    inp.onchange = async () => {
      const f = inp.files && inp.files[0];
      inp.remove();
      if (f) { try { await fn(f); } catch (e) { console.warn('atmosphere asset:', e); } }
    };
    document.body.appendChild(inp);
    inp.click();
  }

  /** Play the recipe under construction, through the reader's own stage. */
  function playPreview(host, a) {
    if (typeof STAGE === 'undefined' || typeof SCENES === 'undefined') return;
    STAGE.clear();
    const scene = SCENES.make(String(a.visual.scene || ''), {
      seed: Number(a.visual.seed) || 0, params: ATMO.paramsOf(a),
    });
    if (!scene) {
      host.appendChild(el('p', 'hint', 'This build cannot render that scene.'));
      return;
    }
    // The preview obeys the same ladder as the reader. If it says no, say so
    // rather than showing an author a moving picture their own settings have
    // already turned off for everyone.
    if (!STAGE.play(host, scene, { palette: ATMO.paletteOf(a), seed: Number(a.visual.seed) || 0 })) {
      host.appendChild(el('p', 'hint', 'Preview is still — ' + ATMO.declineReason()));
    }
  }

  return { render, blank, rollSeed };
})();

if (typeof window !== 'undefined') window.ATMO_EDIT = ATMO_EDIT;
