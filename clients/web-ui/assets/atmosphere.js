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

  /**
   * The recipe's image sequence, or an empty list.
   *
   * A stage missing its anchor or its image is dropped rather than repaired:
   * the authoring validator refuses both, so anything that gets here that way
   * arrived from a client that does not agree with the contract, and guessing
   * on its behalf is how two renderers start disagreeing.
   */
  function stagesOf(a) {
    const list = (a && a.visual && a.visual.stages) || [];
    if (!Array.isArray(list)) return [];
    return list.filter(s => s && typeof s.anchor === 'string' && s.anchor &&
      typeof s.image === 'string' && s.image);
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
    // A sequence has no scene id, so SCENES has no name for it — saying
    // "Atmosphere" would describe nothing. Say how many pictures instead.
    const n = stagesOf(atmosphere).length;
    const what = known ? known.label
      : (n ? (n === 1 ? '1 picture' : n + ' pictures') : 'Atmosphere');
    el.textContent = what + (hasSound ? ' · sound' : '');
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
   * Hold the sound where it is.
   *
   * Distinct from stopBed, which throws the element away — and the element is
   * where the PLAYHEAD lives. Rebuilding it to make sound again starts the
   * piece from its first second, which is not what anyone means by unmuting a
   * background they were listening to.
   *
   * The audio floor IS released: while this is silent, a listening room or a
   * voice note should be able to take it. Resuming asks for it back, and may
   * be told no — which is the same answer starting would have got.
   */
  function pauseBed() {
    if (!bed || bed.held) return;
    bed.held = true;
    try { bed.el.pause(); } catch { /* already gone */ }
    AUDIO.release(bed.owner);
  }

  /** Continue from exactly where pauseBed held it. */
  function resumeBed() {
    if (!bed) return { ok: false, why: 'this post has no sound' };
    if (!bed.held) return { ok: true };
    if (!AUDIO.request(bed.owner, 'bed', () => stopBed())) {
      return { ok: false, why: AUDIO.busyReason() || 'something else is playing' };
    }
    bed.held = false;
    try { bed.el.play().catch(() => { /* the poster is still right */ }); } catch { /* gone */ }
    return { ok: true };
  }

  /** Is a bed loaded and actually sounding? */
  function bedSounding() { return !!(bed && !bed.held); }

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
  // stageInto is the same seam for a sequence's plates, and it is separate
  // from posterInto for one reason: the reader's posterInto reports failure
  // by inserting a note beside the image and replacing it. In a background
  // layer that would put visible text inside aria-hidden chrome and delete a
  // plate. A stage that never arrives leaves the previous picture up instead.
  let stageInto = null;  // (img, assetId) => void
  let ignoreRemembered = false;
  // soundGate(assetId, proceed): the reader's chance to FETCH the bed's
  // bytes after the sound consent and before the element needs them.
  let soundGate = null;

  /**
   * The bed's bytes, before the <audio> element needs them.
   *
   * A space serves an asset it does not hold as a 409, and an <audio> pointed
   * at one fails silently — el.play() rejects and the catch says nothing. So
   * a reader who was not the author heard nothing and was told nothing, while
   * the author heard it fine because the bytes were already on their disk.
   *
   * The pictures never had this bug: their plates go through observeMedia,
   * which ASKS the node to fetch. The sound has to ask too, and this is the
   * one place that does it — every path to startBed goes through here.
   *
   * @param {string} assetId
   * @param {(ok: boolean, why?: string) => void} done called when it is
   *   settled; may be preceded by `waiting` calls while the ladder retries
   * @param {(note: string) => void} [waiting] the fetch is still going, and
   *   this is what to say meanwhile
   * @returns {boolean} true when the answer is deferred, so the caller knows
   *   to show a waiting state rather than fall through to a final one
   */
  function withBedBytes(assetId, done, waiting) {
    // A transient post reader supplies its own gate: its bytes come through
    // a SESSION route, and the held-space fetch below would ask a space this
    // node does not have.
    if (soundGate) { soundGate(assetId, done); return true; }
    // No fetch machinery at all (the editor's preview, a stub): behave the
    // way this did before there was any asking — point at it and hope.
    if (typeof autoFetchAsset !== 'function') { done(true); return false; }

    // A bed is the biggest thing an atmosphere carries — many more chunks
    // than any picture in the post — and one pass of autoFetchAsset gives up
    // after about a hundred seconds. Over a relay's sync cadence that is well
    // short of the honest answer, so this keeps going FOR AS LONG AS CHUNKS
    // ARE STILL ARRIVING, and stops when a whole pass adds nothing. Patience
    // is spent on progress, never on silence.
    const ROUNDS_WITHOUT_PROGRESS = 2;
    let best = -1;    // the most chunks seen to arrive
    let idle = 0;     // consecutive passes that added none
    let say = 'fetching the sound…';

    const progress = (have, total) => {
      say = 'fetching the sound… ' + have + '/' + total;
      if (have > best) { best = have; idle = 0; }
      if (waiting) waiting(say);
    };
    const silence = (reason) => {
      // The node says nobody has answered YET. The ladder keeps asking, so
      // this changes what the reader is told, not what we do.
      if (reason === 'no_source' && best <= 0) {
        say = 'nobody is answering for the sound yet — still asking';
        if (waiting) waiting(say);
      }
    };
    const pass = () => {
      const seen = best;
      autoFetchAsset(assetId, null, silence, progress).then((ok) => {
        if (ok) { done(true); return; }
        idle = best > seen ? 0 : idle + 1;
        if (idle >= ROUNDS_WITHOUT_PROGRESS) {
          done(false, best > 0
            ? 'the sound stopped arriving at ' + best + ' of its pieces'
            : 'the sound never arrived');
          return;
        }
        if (waiting) waiting(say);
        pass();
      }, () => done(false, 'the sound never arrived'));
    };
    pass();
    return true;
  }

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
    if (!bedSounding() || !bed.analyser || !bed.data) return 0;
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
  /**
   * Which mount a callback belongs to. Fetching the bed's bytes takes as long
   * as it takes, and a reader can close the post while it is in the air — so
   * anything that starts sound checks that the post it was asked for is still
   * the one on screen.
   */
  let mountGen = 0;
  /** Keeps the still frame the same size as the stage it sits in. */
  let stillRO = null;
  /** The running image sequence, when the recipe is one. */
  let seq = null;

  /** Take down whatever is playing and undo the shell. Safe to call twice. */
  function unmount() {
    mountGen++;
    stopBed();
    srcFor = null;
    posterInto = null;
    stageInto = null;
    ignoreRemembered = false;
    soundGate = null;
    if (seq) { seq.dispose(); seq = null; }
    if (stillRO) { stillRO.disconnect(); stillRO = null; }
    if (typeof STAGE !== 'undefined') STAGE.clear();
    if (mounted) {
      const m = mounted;
      mounted = null;
      try { m.veil.remove(); } catch { /* already gone */ }
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
    mounted.veil.remove();
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
    shell.prepend(mounted.veil);
    shell.classList.add('atmo-shell');
    mounted.shell = shell;
    // The LAYERS survived, but every block node was rebuilt underneath them.
    // A sequence anchored to the old nodes would be watching orphans from the
    // first incoming message onward, and the background would freeze on
    // whatever picture happened to be up.
    if (seq) seq.arm(shell);
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
    stageInto = (opts && opts.stageInto) || null;
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

    // The layers, and the one structural thing that is easy to get wrong: the
    // atmosphere is the background of the READING AREA, not of the document.
    //
    // Stretching a picture over the whole article means a long post shows one
    // thin slice of it and a crossfade is invisible — a 120 000px article was
    // rendering a 64px photograph as a flat colour. So a veil spans the
    // article (that is what has to be clipped and rounded) and the stage
    // sticks to the top of the scrollport inside it, one screen tall. The
    // scrim rides the same way, pulled back over the stage.
    //
    // A running scene gains from this too: STAGE's backing-store cap was
    // being spread over the article's whole height.
    const veil = document.createElement('div');
    veil.className = 'atmo-veil';
    veil.setAttribute('aria-hidden', 'true');
    const stage = document.createElement('div');
    stage.className = 'atmo-stage';
    const scrim = document.createElement('div');
    scrim.className = 'atmo-scrim';
    veil.appendChild(stage);
    veil.appendChild(scrim);
    shell.prepend(veil);
    shell.classList.add('atmo-shell');
    styleScrim(shell, scrim, atmosphere);

    mounted = { shell, veil, stage, scrim, bar, scene, atmosphere, postId,
                // The recipe's identity, so a re-render can tell "the same
                // atmosphere, keep it running" from "an edit changed it,
                // remount". JSON is fine here: the projection is canonical
                // enough that an unchanged recipe stringifies identically.
                recipeSig: JSON.stringify(atmosphere) };

    // An image sequence takes the stage instead of a poster or a still: it IS
    // the background, and it is already showing its first picture before the
    // reader has moved. Switching atmosphere off skips it entirely and falls
    // through to the poster/fallback below — that is the same answer this
    // gave before sequences existed.
    const wantsAtmosphere = typeof STAGE === 'undefined' || STAGE.wanted();
    const isSequence = stagesOf(atmosphere).length > 0;
    if (isSequence && wantsAtmosphere) {
      seq = startSequence(shell, stage, scrim, atmosphere);
    } else if (fall.poster) {
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
    // A sequence has no scene id, so SCENES has no name for it and the bar
    // used to introduce itself as "Atmosphere atmosphere". Name it for what
    // it is instead — and say how many pictures, because that is the one
    // thing a reader cannot see from the first of them.
    const seqCount = stagesOf(atmosphere).length;
    const label = isSequence
      ? (seqCount === 1 ? 'One picture as you read' : seqCount + ' pictures as you read')
      : ((typeof SCENES !== 'undefined' &&
          SCENES.get(String(atmosphere.visual.scene || ''))?.label) || 'Atmosphere');
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
        // Say only what is true: a declined scene is not "moving", a
        // person's pause is its own state, and a bed that is loaded but
        // held is not sounding.
        const bits = [label];
        if (sceneLive) bits.push(STAGE.paused() ? 'paused' : 'moving');
        if (bedSounding()) bits.push('sound');
        else if (bed) bits.push('sound held');
        name.textContent = bits.join(' \u00B7 ');
      } else {
        // A sequence's label is already a sentence; a scene's is a name.
        name.textContent = isSequence ? label : label + ' atmosphere';
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
        // Pause takes the WHOLE atmosphere, because that is what the bar
        // calls it: one thing, named once, whose state reads "moving · sound".
        // A Pause that froze a slow gradient and left the music playing did
        // nothing a person could notice, which is how it was reported — as a
        // button that does not work. Mute keeps its own job: the sound alone.
        const anyLive = () => (sceneLive && !STAGE.paused()) || bedSounding();
        if (sceneLive || bed) btn(anyLive() ? 'Pause' : 'Resume', 'btn-plain', () => {
          // Freezing keeps the CURRENT frame and the CURRENT playhead: the
          // canvas is not redrawn and the element is not rebuilt. A pause
          // that jumped back to the opening frame — or the opening second —
          // would read as the composition resetting, and a reset is Leave.
          if (anyLive()) {
            if (sceneLive) STAGE.pause();
            pauseBed();
            renderBar('live', notes);
            return;
          }
          if (sceneLive) STAGE.resume();
          const r = resumeBed();
          renderBar('live', r.ok ? notes : ['quiet — ' + r.why]);
        });
        if (hasSound && soundMode() !== 'never') {
          btn(bedSounding() ? 'Mute' : 'Unmute', 'btn-plain', () => {
            if (bedSounding()) { pauseBed(); renderBar('live', notes); return; }
            // The bed is loaded but held: continue it, never rebuild it.
            if (bed) {
              const r = resumeBed();
              renderBar('live', r.ok ? notes : ['quiet — ' + r.why]);
              return;
            }
            // Unmute asks for the bytes too. It usually returns at once —
            // they arrived when the reader first opened the sound — but a
            // reader who muted before they ever landed would otherwise get
            // an <audio> pointed at a 409 and silence with no explanation.
            const gen = mountGen;
            const deferred = withBedBytes(atmosphere.audio.asset, (ok, why) => {
              if (gen !== mountGen) return;
              if (ok) {
                const r = startBed(atmosphere.audio, postId);
                renderBar('live', r.ok ? notes : [r.why]);
              } else {
                renderBar('live', ['quiet — ' + (why || 'the sound never arrived')]);
              }
            }, (note) => { if (gen === mountGen) renderBar('live', [note]); });
            if (deferred) renderBar('live', ['fetching the sound…']);
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
      // A sequence has no scene BY DESIGN, so saying "this version does not
      // know that scene" there would invent a fault out of a working post.
      const parts = [];
      if (!scene && !isSequence && state !== 'live') {
        if (fall.text) parts.push(String(fall.text));
        parts.push('this version does not know that scene');
      }
      if (isSequence && !seq && state !== 'live') {
        if (fall.text) parts.push(String(fall.text));
        parts.push('the pictures are not showing — ' + stageDeclinedBecause());
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
      if (withSound && atmosphere.audio && atmosphere.audio.asset) {
        // The consent already happened (the person pressed the sound door);
        // the bytes are fetched and the bed starts when they are there — or
        // the bar says why they are not.
        const gen = mountGen;
        const state = () => (!sceneLive && !bed) ? 'still' : 'live';
        const deferred = withBedBytes(atmosphere.audio.asset, (ok, why) => {
          // The reader may have left, or opened another post, while this was
          // in the air. Starting a bed for a post nobody is on would be sound
          // arriving from nowhere.
          if (gen !== mountGen) return;
          if (ok) {
            const r = startBed(atmosphere.audio, postId);
            if (!r.ok) notes.push('quiet — ' + r.why);
            else if (soundMode() === 'remember') localStorage.setItem(CONSENT_KEY, '1');
          } else {
            notes.push('quiet — ' + (why || 'the sound never arrived'));
          }
          renderBar(state(), notes);
        }, (note) => {
          if (gen !== mountGen) return;
          renderBar(state(), notes.concat([note]));
        });
        if (deferred) renderBar(state(), notes.concat(['fetching the sound…']));
        return;
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
   *   sequence      its FIRST picture as an <img> — the opening image is what
   *                 the post opens on, and one image is cheaper than a canvas
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

    // The card's own words need a backing too — a title over a bright
    // photograph is exactly as unreadable as a paragraph over one. Same
    // function, so the two surfaces cannot drift apart.
    const ground = paletteOf(atmosphere)[0];
    const inkFrom = (luma, mean) => {
      paintInk(card, ground, luma, mean);
      card.classList.add('pub-card-atmo-on');
    };

    const opening = stagesOf(atmosphere)[0];
    let dispose;
    if (fall.poster || opening) {
      const img = document.createElement('img');
      img.className = 'atmo-poster';
      img.alt = '';
      const id = fall.poster || opening.image;
      inkFrom(0.5, null); // until the bytes are here, assume the awkward middle
      img.onload = () => { const s = plateStats(img); if (s) inkFrom(s.luma, s.mean); };
      if (posterInto) posterInto(img, id);
      else autoMediaSrc(img, id);
      layer.appendChild(img);
      card.prepend(layer);
      dispose = () => { img.onload = null; layer.remove();
                        card.classList.remove('pub-card-atmo-on'); };
    } else {
      const scene = typeof SCENES !== 'undefined'
        ? SCENES.make(String(atmosphere.visual.scene || ''), {
            seed: Number(atmosphere.visual.seed) || 0,
            params: paramsOf(atmosphere),
          })
        : null;
      if (!scene) return null;
      // A generative still is drawn from the palette, so the palette's own
      // brightest token is the honest measure of it.
      let maxLuma = 0;
      for (const h of paletteOf(atmosphere).slice(1)) {
        maxLuma = Math.max(maxLuma, BRUSH.luminance(h));
      }
      inkFrom(maxLuma, null);
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
      dispose = () => { if (io) io.disconnect(); layer.remove();
                        card.classList.remove('pub-card-atmo-on'); };
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
  function styleScrim(shell, scrim, atmosphere) {
    const pal = paletteOf(atmosphere);
    let maxLuma = 0;
    for (const h of pal.slice(1)) maxLuma = Math.max(maxLuma, BRUSH.luminance(h));
    paintScrim(shell, scrim, pal[0], maxLuma);
  }

  /**
   * Write the scrim: the gradient carries the SHAPE, `opacity` carries the
   * strength.
   *
   * Splitting them is what lets the readability layer follow a stage change —
   * opacity transitions natively and a gradient does not, so baking the alpha
   * into the colour stops would mean the scrim jumping while the picture
   * crossfaded under it. The product of the two is identical to writing the
   * alpha into the stops, which is what this did before stages existed.
   */
  /**
   * The colour the wash is made of.
   *
   * A flat neutral over a photograph reads as a sheet laid on top of it —
   * you can see the picture and you can see the sheet. A wash mixed from the
   * picture's OWN average, darkened, reads as the same picture in lower
   * light, which is what an atmosphere is supposed to be. The room's ground
   * colour is still folded in, so a space keeps its identity across every
   * photograph an author might publish in it.
   *
   * @param {[number,number,number]|null} mean the picture's average colour
   * @param {string} ground the palette's first token, `#rrggbb`
   * @returns {[number,number,number]}
   */
  function washColour(mean, ground) {
    const n = parseInt(String(ground).slice(1), 16);
    const g = [(n >> 16) & 255, (n >> 8) & 255, n & 255];
    if (!mean) return /** @type {[number,number,number]} */ (g);
    // Darkened first: an average is by definition mid-toned, and a mid-toned
    // wash cannot darken anything however much of it is laid down.
    const DARK = 0.34, TOWARD_ROOM = 0.34;
    return /** @type {[number,number,number]} */ (mean.map((c, i) => {
      const d = c * DARK;
      return Math.round(d + (g[i] - d) * TOWARD_ROOM);
    }));
  }

  /**
   * Make a surface readable over a picture. ONE measurement, TWO variables.
   *
   * Everything about readability here comes from how BRIGHT the picture is,
   * and it comes out as exactly two things: the colour the text's halo is
   * made of, and how far that halo reaches. The colour is the wash — the
   * picture's own average, darkened — so the halo reads as the photograph in
   * shadow rather than as a grey outline drawn over it. The reach grows with
   * brightness, which sinks the surroundings of every stroke precisely where
   * a bright field would otherwise bleach it.
   *
   * The shape lives in the stylesheet; only the numbers live here. This is
   * used by both surfaces that need it — the article's shell and a feed
   * card — so the two cannot drift apart.
   *
   * @param {HTMLElement|null} el @param {string} ground the palette's first token
   * @param {number} luma @param {[number,number,number]|null} [mean]
   */
  function paintInk(el, ground, luma, mean) {
    if (!el) return;
    const l = Math.max(0, Math.min(1, luma || 0));
    el.style.setProperty('--atmo-ink', washColour(mean || null, ground).join(','));
    el.style.setProperty('--atmo-halo', (1 + 1.5 * l).toFixed(2));
  }

  /**
   * Write the scrim: the gradient carries the colour, `opacity` the strength.
   *
   * A dark photograph needs almost nothing and should keep its depth; a
   * bright one needs a great deal. The ceiling stays below 1 on purpose — at
   * some point the honest answer is that an atmosphere is a background, not
   * a page colour.
   *
   * @param {HTMLElement|null} shell @param {HTMLElement} scrim
   * @param {string} ground @param {number} luma
   * @param {[number,number,number]|null} [mean] the picture's average colour
   */
  function paintScrim(shell, scrim, ground, luma, mean) {
    const rgb = washColour(mean || null, ground).join(',');
    scrim.style.background = `linear-gradient(rgba(${rgb},0.9), rgba(${rgb},1))`;
    scrim.style.opacity = String(Math.min(0.86, 0.24 + 0.78 * Math.max(0, luma)));
    paintInk(shell, ground, luma, mean);
  }

  // ---- the image sequence --------------------------------------------------
  //
  // A story's background follows the reader: each stage names a block, and
  // when that block comes up the picture behind the article changes.
  //
  // Stages are CONTENT; the transition is DECORATION. So a person who turned
  // atmosphere off gets no sequence at all — that is the same answer as
  // before this existed — but reduced motion does NOT freeze the story on its
  // first picture: it fixes the crossfade at a safe length, ignores the
  // author's timing, and never moves anything. Opacity is not motion; an
  // instant full-screen brightness step is the thing actually worth avoiding,
  // which is why a `cut` becomes a fade there rather than the reverse.
  //
  // Three bounds do the safety work, and they are structural rather than
  // measured: only opacity ever animates, no two changes may land closer
  // together than MIN_STAGE_DWELL_MS, and a fast scroll coalesces to the
  // stage it ends on instead of playing every one it flew past.

  /** A fixed, calm crossfade for reduced motion and the poster rung. */
  const SAFE_FADE_MS = 900;
  /** What a stage that named no duration gets. */
  const DEFAULT_FADE_MS = 700;
  /** Mirrors publication.MaxFadeMs — the contract's own ceiling. */
  const MAX_FADE_MS = 10000;
  /** No two background changes may land closer together than this. */
  const MIN_STAGE_DWELL_MS = 700;
  /** A stage takes over when its block's top passes this much of the screen. */
  const ACTIVATION_LINE = 0.4;
  /**
   * ...and gives way only once its block has gone back past THIS much.
   *
   * A single line has no dead band, and a reader who stops with an anchor
   * resting on it oscillates: trackpad inertia moves the page by a pixel,
   * the answer flips, and the dwell timer does not prevent that — it paces
   * it to one crossfade every 700ms, which is precisely a flicker. The gap
   * between the two lines is what makes a stage that has taken over stay
   * taken over. Same shape as the relay selector's hysteresis.
   */
  const RELEASE_LINE = 0.48;

  /**
   * How tall the reading area is, and zero when there is not one yet.
   *
   * clientHeight rather than innerHeight alone: it excludes the scrollbar,
   * which is the right number for "where is the reader looking", and it is
   * the one an embedded webview reliably reports — innerHeight comes back 0
   * in at least one shell we render inside.
   */
  function viewportHeight() {
    const doc = document.documentElement;
    return (doc && doc.clientHeight) || window.innerHeight || 0;
  }

  /** sRGB channel to linear, for relative luminance. */
  function toLinear(c) {
    const s = c / 255;
    return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
  }

  /**
   * How bright a plate is WHERE THE TEXT WILL BE, 0..1, or -1 if unreadable.
   *
   * Two things this deliberately is not. It is not the MEAN — a photograph
   * with a bright sky and a dark foreground averages to something middling
   * while the words sit on the sky, and the average is what said a picture
   * was fine when it was not. It takes a high percentile, so one bright
   * region decides. And it is not the whole FRAME — the plate is object-fit:
   * cover over the reading pane, so the outer columns are cropped away and
   * only a middle band is ever behind the article.
   *
   * BRUSH.luminance answers this for a hex colour and there is no per-pixel
   * entry point, so the coefficients are repeated here rather than the image
   * being reduced to a palette it does not have. Assets come from this same
   * origin, so the canvas is not tainted; a browser that disagrees returns -1
   * and the recipe's own palette decides the scrim, exactly as before.
   */
  function plateStats(img) {
    try {
      const N = 24;
      const cv = document.createElement('canvas');
      cv.width = N; cv.height = N;
      const ctx = cv.getContext('2d', { willReadFrequently: true });
      if (!ctx) return null;
      // MEASURE WHAT IS ON SCREEN, not what is in the file.
      //
      // A plate is object-fit: cover, so a tall photograph in a wide window
      // has most of its height cropped away — and the part that survives is
      // exactly the part the text is read against. Drawing the whole file
      // measured a picture nobody sees, which is how a bright band ended up
      // under a paragraph with a scrim computed from the dark sky above it.
      const nw = img.naturalWidth || 0, nh = img.naturalHeight || 0;
      const box = img.getBoundingClientRect();
      let sx = 0, sy = 0, sw = nw, sh = nh;
      if (nw > 0 && nh > 0 && box.width > 0 && box.height > 0) {
        const want = box.width / box.height, have = nw / nh;
        if (have > want) { sw = nh * want; sx = (nw - sw) / 2; }
        else { sh = nw / want; sy = (nh - sh) / 2; }
      }
      ctx.drawImage(img, sx, sy, sw, sh, 0, 0, N, N);
      const d = ctx.getImageData(0, 0, N, N).data;
      // The text column, in image terms: the middle band. Reading the edges
      // would let a dark corner pay for a bright centre.
      const lo = Math.floor(N * 0.2), hi = Math.ceil(N * 0.8);
      const vals = [];
      const sum = [0, 0, 0];
      let count = 0;
      for (let y = 0; y < N; y++) {
        for (let x = lo; x < hi; x++) {
          const i = (y * N + x) * 4;
          vals.push(0.2126 * toLinear(d[i]) + 0.7152 * toLinear(d[i + 1]) +
                    0.0722 * toLinear(d[i + 2]));
          sum[0] += d[i]; sum[1] += d[i + 1]; sum[2] += d[i + 2];
          count++;
        }
      }
      if (!vals.length) return null;
      vals.sort((a, b) => a - b);
      return {
        // The 85th percentile: the brightest place a line of text is likely
        // to cross, without letting one specular highlight black out the page.
        luma: vals[Math.min(vals.length - 1, Math.floor(vals.length * 0.85))],
        // The plain mean, in sRGB. This one is not about contrast at all —
        // it is what the wash is COLOURED with, so it wants the picture's
        // ordinary colour rather than its extremes.
        mean: /** @type {[number,number,number]} */ ([
          Math.round(sum[0] / count), Math.round(sum[1] / count), Math.round(sum[2] / count),
        ]),
      };
    } catch { return null; }
  }

  /** Point a background plate at an asset, without touching anything else. */
  function plateSrc(img, assetId) {
    if (stageInto) { stageInto(img, assetId); return; }
    if (typeof assetURL !== 'function') return;
    const base = assetURL(assetId);
    img.src = base;
    // Deliberately no unavailable callback: markUnavailable would insert a
    // visible note as this plate's sibling, inside a layer the reader is
    // never meant to read.
    if (typeof observeMedia === 'function') {
      observeMedia(img, assetId, () => { img.src = base + '&v=' + Date.now(); }, null);
    }
  }

  /**
   * Drive a sequence over one mounted article.
   *
   * Two stacked plates and a crossfade between them: the front one is
   * showing, the back one is always already holding the NEXT stage's picture,
   * so an ordinary forward read never waits for bytes.
   *
   * @param {HTMLElement} shell @param {HTMLElement} stage
   * @param {HTMLElement} scrim @param {any} atmosphere
   */
  function startSequence(shell, stage, scrim, atmosphere) {
    const list = stagesOf(atmosphere);
    if (!list.length) return null;

    const plates = [0, 1].map(() => {
      const img = document.createElement('img');
      img.className = 'atmo-plate';
      img.alt = '';
      img.setAttribute('aria-hidden', 'true');
      stage.appendChild(img);
      return img;
    });
    let front = 0;      // which plate is on screen
    let idx = -1;       // which stage is on screen
    let lastSwapAt = 0;
    let dwellTimer = null;
    let pending = false;
    /** Catches layout that settles without a scroll — a late image, a font. */
    let ro = null;
    /** @type {{el: Element, i: number}[]} — resolvable anchors, in order. */
    let anchors = [];

    function fadeMs(s) {
      const safe = (typeof MODES !== 'undefined' && MODES.reducedMotion()) ||
        (typeof STAGE !== 'undefined' && STAGE.mode() === 'poster');
      if (safe) return SAFE_FADE_MS;
      if (s.transition === 'cut') return 0;
      return Math.max(0, Math.min(MAX_FADE_MS,
        Number(s.duration_ms) || DEFAULT_FADE_MS));
    }

    /** Put stage i's picture on the back plate, ready to be swapped in. */
    function preload(i) {
      if (i < 0 || i >= list.length) return;
      const back = plates[1 - front];
      // Nobody is waiting on this plate any more: a handler left over from
      // the show() that just finished would fire against a picture it was
      // never about.
      back.onload = null;
      if (back.dataset.asset === list[i].image) return;
      back.dataset.asset = list[i].image;
      back.classList.remove('on');
      plateSrc(back, list[i].image);
    }

    function show(i) {
      if (i === idx || i < 0 || i >= list.length) return;
      const s = list[i];
      const back = plates[1 - front];
      shell.style.setProperty('--atmo-fade', fadeMs(s) + 'ms');
      let swapped = false;
      const swap = () => {
        if (swapped || back.dataset.asset !== s.image) return;
        swapped = true;
        back.onload = null;
        back.classList.add('on');
        plates[front].classList.remove('on');
        front = 1 - front;
        idx = i;
        lastSwapAt = performance.now();
        // A photograph fills the whole layer at full strength, where a
        // generative scene is soft gradients — so a bright picture needs more
        // scrim than a bright palette token does, and the scrim has to be
        // recomputed per stage rather than once at mount.
        const seen = plateStats(back);
        if (seen) {
          // Both numbers come from the SAME crop of the SAME picture: how
          // strong the wash has to be, and what colour it is made of.
          paintScrim(shell, scrim, paletteOf(atmosphere)[0], seen.luma, seen.mean);
        }
        preload(i + 1);
      };
      // The handler is (re)assigned unconditionally, even when the plate
      // already holds this picture. Assigning it only on the load branch
      // left a plate carrying a handler from an EARLIER show(), and an
      // element has exactly one `onload` — so the newest intent has to be
      // the one written there, or a late frame swaps in a stage the reader
      // has already scrolled away from.
      back.onload = swap;
      if (back.dataset.asset !== s.image) {
        back.dataset.asset = s.image;
        plateSrc(back, s.image);
      }
      // Already loaded (the usual case — this is the plate that was
      // preloaded) — `load` will not fire again for bytes already there.
      if (back.complete && back.naturalWidth > 0) swap();
    }

    /**
     * Which stage the reader is in, read from the document rather than from
     * intersection events: the active stage is the last anchor whose top has
     * passed the line, which is the same answer going up as going down. The
     * anchors are in document order — the validator guarantees it — so the
     * scan stops at the first one still below.
     */
    function recompute() {
      if (!anchors.length) return;
      // A viewport of zero has no honest answer: every anchor's top is 0,
      // every one of them counts as reached, and the sequence would open on
      // its LAST picture. That is not hypothetical — an article measured
      // before its pane is laid out reports exactly this. Wait; the resize
      // observer calls back the moment there is something to measure.
      const h = viewportHeight();
      if (h <= 0) return;
      let want = 0;
      for (const a of anchors) {
        // The stage that is already showing keeps the more generous line, so
        // it holds until the reader has clearly gone back past it; anything
        // further down has to earn its turn at the stricter one.
        const limit = h * (a.i <= idx ? RELEASE_LINE : ACTIVATION_LINE);
        if (a.el.getBoundingClientRect().top <= limit) want = a.i;
        else break;
      }
      if (want === idx) return;
      const wait = MIN_STAGE_DWELL_MS - (performance.now() - lastSwapAt);
      if (wait > 0) {
        // One trailing timer, never a queue: when it fires it shows whatever
        // the reader has arrived at by then, so scrolling through six stages
        // in a second is one change, not six.
        if (!dwellTimer) {
          dwellTimer = setTimeout(() => { dwellTimer = null; recompute(); }, wait);
        }
        return;
      }
      show(want);
    }

    function schedule() {
      if (pending) return;
      pending = true;
      // A single coalescing frame, not a loop: it is armed only by a scroll
      // or a resize, and it reads at most a dozen rectangles.
      requestAnimationFrame(() => { pending = false; recompute(); });
    }

    /**
     * Bind to the blocks of (a rebuilt) article. renderArticle keeps the
     * atmosphere alive across a full rebuild of the content, so without
     * re-arming on reattach this observer would be watching orphans after the
     * first incoming message.
     */
    function arm(root) {
      if (ro) { ro.disconnect(); ro = null; }
      anchors = [];
      const byId = new Map();
      // One pass over the DOM rather than a selector per anchor: an anchor is
      // an author-chosen string, and building a selector out of one is a
      // quoting problem nobody needs to have.
      root.querySelectorAll('[data-block-id]').forEach(el => {
        const id = /** @type {HTMLElement} */ (el).dataset.blockId;
        if (id && !byId.has(id)) byId.set(id, el);
      });
      list.forEach((s, i) => {
        const el = byId.get(s.anchor);
        // A stage whose block is not here is skipped, not repaired: the
        // decoder accepts a dangling anchor deliberately, and the honest
        // rendering of one is that the previous picture simply holds.
        if (el) anchors.push({ el, i });
      });
      if (window.ResizeObserver) {
        ro = new ResizeObserver(schedule);
        ro.observe(root);
      }
      recompute();
    }

    function dispose() {
      if (ro) { ro.disconnect(); ro = null; }
      if (dwellTimer) { clearTimeout(dwellTimer); dwellTimer = null; }
      window.removeEventListener('scroll', schedule, true);
      window.removeEventListener('resize', schedule);
      for (const p of plates) { p.onload = null; p.remove(); }
      shell.style.removeProperty('--atmo-fade');
    }

    // Scroll does not bubble, so the capture phase is how one listener sees
    // every scroller the article might be inside.
    window.addEventListener('scroll', schedule, true);
    window.addEventListener('resize', schedule);
    // The opening picture is there from the first paint, with no transition
    // to arrive through.
    shell.style.setProperty('--atmo-fade', '0ms');
    show(0);
    arm(shell);
    return { arm, dispose, at: () => idx, count: list.length };
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
           // stages so the feed can tell a sequence from a scene without a
           // second reading of the recipe — the reason both directions live
           // in this file.
           paletteOf, paramsOf, stages: stagesOf,
           declineReason: stageDeclinedBecause };
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
  const MAX_STAGES = 12;

  /**
   * Every block of a document, in reading order, with something to call it.
   *
   * Stages anchor to block ids, and an author picking "b7f3a291" out of a
   * list is not choosing a place in their own post. Nesting is walked because
   * document order is what the contract requires of a sequence, and a section
   * with a paragraph inside it has that paragraph AFTER it.
   */
  function blockChoices(doc) {
    const out = [];
    const walk = (list) => {
      for (const b of list || []) {
        if (!b || !b.id) continue;
        const p = b.props || {};
        const words = String(p.text || p.caption || '').trim().replace(/\s+/g, ' ');
        out.push({
          id: String(b.id),
          label: b.type + (words ? ' — ' + words.slice(0, 44) : ''),
        });
        walk(b.children);
      }
    };
    walk(doc.blocks);
    return out;
  }

  /**
   * Keep a sequence in document order, which the contract requires.
   *
   * Sorting rather than refusing: the author's intent when they anchor a
   * picture to a block is "here", never "here, third". A stage whose block
   * has been deleted sorts to the end, where the editor shows it as needing
   * a new anchor instead of silently dropping the author's picture.
   */
  function sortStages(a, doc) {
    const order = new Map(blockChoices(doc).map((c, i) => [c.id, i]));
    (a.visual.stages || []).sort((x, y) =>
      (order.has(x.anchor) ? order.get(x.anchor) : 1e6) -
      (order.has(y.anchor) ? order.get(y.anchor) : 1e6));
  }

  /** A fresh recipe: the default scene, a seed, and the space's own colours. */
  function blank() {
    const id = (typeof SCENES !== 'undefined' && SCENES.list()[0]) || 'drift@1';
    return {
      visual: { scene: id, seed: rollSeed(), params: [], palette: defaultPalette() },
      fallback: { text: '' },
    };
  }

  // The words a reader gets when the picture cannot be shown are REQUIRED by
  // the contract (ADR-013 invariant 1: the renderer may degrade, the meaning
  // may not). That rule is about what a reader receives, not about what an
  // author must invent — so the editor writes a true sentence about the
  // recipe and the author edits it if they want better ones.
  //
  // A literal "default" would satisfy the validator and tell the reader
  // nothing, which is the outcome the invariant exists to prevent. These say
  // what is actually there.

  /** The last sentence this editor wrote, so the author's own is never lost. */
  let autoWords = '';

  function describe(a) {
    const v = (a && a.visual) || {};
    if (!v.scene) {
      const n = (v.stages || []).length;
      if (n > 1) return n + ' pictures behind the text, changing as you read.';
      return 'A picture behind the text.';
    }
    const known = typeof SCENES !== 'undefined' && SCENES.get(String(v.scene));
    return known ? known.label + ' — a slow moving background.'
                 : 'A slow moving background.';
  }

  /** Keep the description true to the recipe, unless the author wrote theirs. */
  function refreshWords(a) {
    if (!a) return;
    a.fallback = a.fallback || { text: '' };
    const now = a.fallback.text || '';
    if (now && now !== autoWords) return; // theirs; leave it alone
    a.fallback.text = describe(a);
    autoWords = a.fallback.text;
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
    // Every structural change comes back through here, so this is the one
    // place the description has to be kept true to what the recipe became.
    refreshWords(a);
    // A post has ONE background, so the two forms are alternatives and the
    // editor says so with a switch rather than letting an author fill in both
    // and meet the refusal at the publish button. An empty scene id is what
    // "this is a sequence" means on the wire, so it means that here too.
    const isSeq = !a.visual.scene;

    const head = el('div', 'atmo-edit-head');
    head.appendChild(el('strong', null, 'Atmosphere'));
    const mode = el('select', 'atmo-edit-mode');
    for (const [v, label] of [['scene', 'A moving scene'], ['stages', 'Pictures as you read']]) {
      const o = el('option', null, label);
      o.value = v;
      if ((v === 'stages') === isSeq) o.selected = true;
      mode.appendChild(o);
    }
    mode.onchange = () => {
      if (mode.value === 'stages') {
        // Nothing generative survives: there is no scene left for a seed or
        // a parameter to steer, and the contract refuses both.
        a.visual.scene = '';
        a.visual.seed = 0;
        a.visual.params = [];
        a.visual.stages = a.visual.stages || [];
      } else {
        delete a.visual.stages;
        a.visual.scene = (typeof SCENES !== 'undefined' && SCENES.list()[0]) || 'drift@1';
        a.visual.seed = rollSeed();
        a.visual.params = [];
      }
      changed();
    };
    head.appendChild(mode);
    const drop = el('button', 'btn-plain', 'Remove');
    drop.type = 'button';
    drop.onclick = () => { delete doc.atmosphere; changed(); };
    head.appendChild(drop);
    host.appendChild(head);

    // Sound first, and large. Every visual knob below has a working default —
    // a scene, a seed, a palette taken from the room — so the panel opens on
    // the one decision that has none and that nothing can guess: whether this
    // post is meant to be heard.
    renderSound(host, a, changed, onChange);

    // A sequence with no pictures is not an atmosphere, and this says so out
    // here where a collapsed panel cannot hide it. (The words have their own
    // warning, beside the field, because they can only go missing while
    // somebody is typing in it — see renderWords.)
    const problems = [];
    if (isSeq && !docStages({ atmosphere: a }).length) {
      problems.push('A sequence needs at least one picture, or this post has no atmosphere at all.');
    }
    for (const p of problems) host.appendChild(el('p', 'hint warn', p));

    // --- everything visual, folded away ------------------------------------
    // The defaults are already publishable, so this is tuning, not setup. It
    // opens by itself when something out here needs attention, because a
    // warning pointing at a field nobody can see is worse than no warning.
    const more = el('details', 'atmo-edit-more');
    more.open = editOpen || problems.length > 0;
    const sum = el('summary', null, 'Customize');
    sum.appendChild(el('span', 'atmo-edit-sum', summarise(a, doc)));
    more.appendChild(sum);
    // Re-render on toggle rather than just remembering: a closed panel must
    // not leave the live preview drawing into a box of zero height.
    more.ontoggle = () => {
      if (more.open === editOpen) return;
      editOpen = more.open;
      render(host, doc, onChange);
    };
    host.appendChild(more);

    // Folding the panel away takes the preview's canvas out of the document,
    // and STAGE would go on drawing into it — a rAF loop nobody can see and
    // nothing will ever stop. Hand the stage back instead.
    if ((!more.open || isSeq) && previewHasStage) {
      previewHasStage = false;
      if (typeof STAGE !== 'undefined') STAGE.clear();
    }

    if (more.open) {
      if (isSeq) renderStages(more, doc, a, changed);
      else renderScene(more, a, changed, onChange);
      // The palette's ground is what the readability scrim is made of, so a
      // sequence needs one just as much as a scene does.
      renderPalette(more, a, changed);
      renderWords(more, a);
      renderLineage(more, a);
    }
  }

  /** Whether the visual half is unfolded. Outlives a re-render; not a document
   *  property — where this author last left a panel is nobody else's business. */
  let editOpen = false;
  /** Whether the editor's preview is the thing currently holding STAGE. There
   *  is one stage for the whole client, so this is how the editor knows it has
   *  something of its own to give back. */
  let previewHasStage = false;

  /** What is under the fold, in a few words, so closing it hides no decision. */
  function summarise(a, doc) {
    if (!a.visual.scene) {
      const n = docStages({ atmosphere: a }).length;
      return n === 1 ? '1 picture' : n + ' pictures';
    }
    const known = typeof SCENES !== 'undefined' && SCENES.get(String(a.visual.scene));
    return known ? known.label : String(a.visual.scene);
  }

  /** The generative half: scene, seed, parameters, and a live preview. */
  function renderScene(host, a, changed, onChange) {
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
  }

  /**
   * The sequence half: a picture per place in the post.
   *
   * Per-stage SOUND is deliberately not offered. The field is in the contract
   * so a sequence authored today does not have to be re-signed when per-stage
   * sound ships, but nothing plays it yet, and an editor that let an author
   * attach audio no reader will hear would be selling something that does not
   * exist.
   */
  function renderStages(host, doc, a, changed) {
    a.visual.stages = a.visual.stages || [];
    sortStages(a, doc);
    const n = a.visual.stages.length;
    host.appendChild(el('p', 'hint',
      n === 0
        ? 'No pictures yet. Add one from the block list below — it goes into the ' +
          'post where the background should change.'
        : (n === 1 ? 'One picture' : n + ' pictures') +
          ', shown among the blocks below where each one takes over.'));
    // The "no pictures at all" warning lives at panel level, outside the fold:
    // a recipe that will not publish must not be able to hide.
  }

  // ---- stages IN THE BLOCK FLOW --------------------------------------------
  //
  // A change of background is a place in the post, so the composer puts it
  // where places live — between the blocks, moved and deleted like one.
  //
  // It is NOT a block on the wire, and that difference is the whole reason
  // for the split. A block type this build invented would reach an older
  // client as an unknown type and draw a fallback card in the middle of the
  // article — a visible fault where the author put something meant to be
  // invisible. Kept in the signed atmosphere, it degrades to nothing at all:
  // no pictures, just the fallback words. The recipe also stays ONE object,
  // so "use this atmosphere" and the lineage hash cover the whole form
  // rather than the half that did not move into the body.

  /** The stages of a composer document, or none when it carries a scene. */
  function docStages(doc) {
    const a = doc && doc.atmosphere;
    if (!a || !a.visual || a.visual.scene) return [];
    return a.visual.stages || [];
  }

  /** The stage anchored to this block, if any. */
  function stageFor(doc, blockId) {
    return docStages(doc).find(s => s && s.anchor === blockId) || null;
  }

  /**
   * Add a picture to the flow. It anchors to the LAST block without one,
   * which is where an author writing top-down means "from here on".
   *
   * Returns a sentence when it cannot, rather than doing something surprising:
   * a scene and a sequence are alternatives, and inventing an anchor for a
   * post with no blocks would be a stage that never fires.
   */
  function addStage(doc, changed) {
    const choices = blockChoices(doc);
    if (!choices.length) {
      return 'Write something first — a picture marks a place in the post, and there are none yet.';
    }
    if (doc.atmosphere && doc.atmosphere.visual && doc.atmosphere.visual.scene) {
      return 'This post has a moving scene. A scene and pictures are alternatives — ' +
        'switch it to “Pictures as you read” in the Atmosphere panel first.';
    }
    if (!doc.atmosphere) {
      doc.atmosphere = {
        visual: { scene: '', seed: 0, params: [], palette: defaultPalette(), stages: [] },
        fallback: { text: '' },
      };
    }
    const a = doc.atmosphere;
    a.visual.stages = a.visual.stages || [];
    if (a.visual.stages.length >= MAX_STAGES) {
      return 'A post carries at most ' + MAX_STAGES + ' pictures.';
    }
    const taken = new Set(a.visual.stages.map(s => s.anchor));
    // From the END: the last free place is the one "from here on" means.
    let free = null;
    for (let i = choices.length - 1; i >= 0; i--) {
      if (!taken.has(choices[i].id)) { free = choices[i]; break; }
    }
    if (!free) return 'Every block already changes the background.';
    chooseAsset('image/*', async f => {
      const r = await uploadAssetFile(f);
      a.visual.stages.push({
        anchor: free.id, image: r.asset_id, transition: 'fade', duration_ms: 700,
      });
      sortStages(a, doc);
      changed();
    });
    return '';
  }

  /**
   * One row for the block flow: the picture that takes over here.
   *
   * Rendered immediately BEFORE its anchor block, which is how it reads —
   * everything from this point down has this background.
   *
   * @param {any} doc @param {any} s the stage @param {() => void} changed
   */
  function stageRow(doc, s, changed) {
    const a = doc.atmosphere;
    const stages = a.visual.stages;
    const i = stages.indexOf(s);
    const r = el('div', 'comp-stage');

    const mark = el('span', 'comp-stage-mark', '\u{1F5BC}');
    mark.title = 'The background changes here';
    r.appendChild(mark);

    const thumb = el('img', 'comp-stage-thumb');
    thumb.alt = '';
    thumb.title = 'Click to replace';
    if (typeof assetURL === 'function' && s.image) thumb.src = assetURL(s.image);
    thumb.onclick = () => chooseAsset('image/*', async f => {
      const up = await uploadAssetFile(f);
      s.image = up.asset_id;
      changed();
    });
    r.appendChild(thumb);

    r.appendChild(el('span', 'comp-stage-label', 'background changes here'));

    const how = el('select', 'atmo-edit-transition');
    for (const [v, label] of [['fade', 'fades in'], ['cut', 'cuts in']]) {
      const o = el('option', null, label);
      o.value = v;
      if ((s.transition || 'fade') === v) o.selected = true;
      how.appendChild(o);
    }
    how.onchange = () => {
      s.transition = how.value;
      if (s.transition === 'cut') delete s.duration_ms;
      changed();
    };
    r.appendChild(how);

    if ((s.transition || 'fade') !== 'cut') {
      const ms = el('input', 'atmo-edit-ms');
      ms.type = 'number'; ms.min = '0'; ms.max = String(MAX_FADE); ms.step = '100';
      ms.value = String(s.duration_ms || 700);
      ms.title = 'milliseconds';
      ms.onchange = () => {
        s.duration_ms = Math.max(0, Math.min(MAX_FADE, Number(ms.value) || 0));
        changed();
      };
      r.appendChild(ms);
    }

    // Moving means RE-ANCHORING to the next free place — the row has no
    // position of its own to slide along.
    const choices = blockChoices(doc);
    const at = choices.findIndex(c => c.id === s.anchor);
    const taken = new Set(stages.filter(x => x !== s).map(x => x.anchor));
    const freeFrom = (from, step) => {
      for (let j = from; j >= 0 && j < choices.length; j += step) {
        if (!taken.has(choices[j].id)) return choices[j].id;
      }
      return null;
    };
    const up = freeFrom(at - 1, -1), down = freeFrom(at + 1, 1);
    const mk = (txt, title, to) => {
      const b = el('button', 'icon-btn comp-ctl', txt);
      b.type = 'button'; b.title = title; b.disabled = !to;
      b.onclick = () => { s.anchor = to; sortStages(a, doc); changed(); };
      r.appendChild(b);
    };
    mk('↑', 'Move earlier', up);
    mk('↓', 'Move later', down);

    const rm = el('button', 'icon-btn comp-ctl', '✕');
    rm.type = 'button'; rm.title = 'Remove this picture';
    rm.onclick = () => { stages.splice(i, 1); changed(); };
    r.appendChild(rm);
    return r;
  }

  function renderPalette(host, a, changed) {
    a.visual.palette = a.visual.palette || [];
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
  }

  /**
   * The words, which are required.
   *
   * ADR-013 invariant 1: the renderer may degrade, the meaning may not. A
   * reader who gets no picture — old client, reduced motion, an id we do not
   * know — still gets this, so the validator refuses a recipe without it.
   * The editor keeps a true sentence here, so the field can only be empty
   * WHILE somebody is clearing it — the next structural render writes one
   * back. That is why the warning lives here, in place, rather than with the
   * panel's own: it has to appear on the keystroke that empties the field,
   * for the author who is looking straight at it, and it would never fire at
   * all if it were computed at render time.
   */
  function renderWords(host, a) {
    const fall = el('textarea', 'atmo-edit-text');
    fall.maxLength = MAX_FALLBACK;
    fall.rows = 2;
    fall.placeholder = 'What a reader gets when the picture cannot be shown';
    fall.value = a.fallback.text || '';
    host.appendChild(row('In words', fall));
    const warn = el('p', 'hint warn',
      'Without these words a reader who cannot see the picture gets nothing.');
    host.appendChild(warn);
    // Typing must not re-render: the editor rebuilds itself on structural
    // changes, and doing that per keystroke would take the caret away
    // mid-sentence. So the warning is toggled in place instead.
    const syncWarn = () => { warn.hidden = !!(a.fallback.text || '').trim(); };
    fall.oninput = () => { a.fallback.text = fall.value; syncWarn(); };
    syncWarn();
  }

  /**
   * The sound, at the top of the panel and given room.
   *
   * It is the only part of an atmosphere with no usable default: a scene, a
   * seed and a palette can all be chosen for an author, and a bed cannot be
   * invented. It is also the part a reader consents to separately, so it is
   * worth deciding on deliberately rather than finding at the bottom of a
   * list of sliders.
   */
  function renderSound(host, a, changed, onChange) {
    const box = el('div', 'atmo-edit-sound');
    if (a.audio && a.audio.asset) {
      const line = el('div', 'atmo-edit-sound-line');
      line.appendChild(el('span', 'atmo-edit-sound-mark', '♫'));
      line.appendChild(el('code', null, a.audio.asset.slice(0, 8) + '…'));
      const loop = el('button', 'btn-plain', a.audio.mode === 'once' ? 'once' : 'loop');
      loop.type = 'button';
      loop.title = a.audio.mode === 'once'
        ? 'plays once when the reader opens the post'
        : 'plays for as long as they are reading';
      loop.onclick = () => { a.audio.mode = a.audio.mode === 'once' ? 'loop' : 'once'; changed(); };
      line.appendChild(loop);
      const rm = el('button', 'btn-plain', 'Remove');
      rm.type = 'button';
      rm.onclick = () => { delete a.audio; changed(); };
      line.appendChild(rm);
      box.appendChild(line);

      const g = el('input', 'atmo-edit-slider');
      g.type = 'range'; g.min = '0'; g.max = '1000'; g.step = '10';
      g.value = String(a.audio.gain ?? 700);
      g.title = 'gain';
      g.onchange = () => { a.audio.gain = Number(g.value); if (onChange) onChange(); };
      const gwrap = el('div', 'atmo-edit-slider-wrap');
      gwrap.appendChild(el('span', 'atmo-edit-sound-sub', 'volume'));
      gwrap.appendChild(g);
      box.appendChild(gwrap);
      box.appendChild(el('p', 'hint',
        'A reader chooses this: the post opens silent, with a door that says ' +
        'there is sound behind it.'));
    } else {
      const pickAudio = el('button', 'btn-filled atmo-edit-sound-add', '♫  Add a sound');
      pickAudio.type = 'button';
      pickAudio.onclick = () => chooseAsset('audio/*', async f => {
        const r = await uploadAssetFile(f);
        a.audio = { asset: r.asset_id, mode: 'loop', gain: 700, fade_in_ms: 600, fade_out_ms: 900 };
        if (Number(a.audio.fade_in_ms) > MAX_FADE) a.audio.fade_in_ms = MAX_FADE;
        changed();
      });
      box.appendChild(pickAudio);
      box.appendChild(el('p', 'hint',
        'Plays behind the post for a reader who opens it with sound. Optional.'));
    }
    host.appendChild(box);
  }

  /** Where this recipe came from, when it came from somebody else's post. */
  function renderLineage(host, a) {
    if (!a.derived_from) return;
    if (!a.derived_from.publication_id && !a.derived_from.recipe_hash) return;
    host.appendChild(el('p', 'hint',
      a.derived_from.publication_id
        ? 'Derived from an atmosphere in this space. The link is recorded when you publish.'
        : 'Derived from another atmosphere, recorded by digest only.'));
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
    previewHasStage = true;
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

  // stageFor/stageRow/addStage are what the composer's BLOCK LIST calls: a
  // change of background is a place in the post, so it is edited where places
  // are. The knowledge of what a stage is stays here, with the rest of the
  // recipe's authoring — publications.js only asks "is there one here".
  return { render, blank, rollSeed, stageFor, stageRow, addStage };
})();

if (typeof window !== 'undefined') window.ATMO_EDIT = ATMO_EDIT;
