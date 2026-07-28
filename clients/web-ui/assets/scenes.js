// @ts-check
// The scene registry.
//
// A scene id in a recipe is exactly like a renderer id in a composition: an
// allowlisted name, resolved locally, never an implementation that arrived
// from a space (ADR-013 invariant 2). An id this build does not know resolves
// to null and the reader shows the fallback text — a scene that is missing
// must degrade the picture, never the meaning (invariant 1).
//
// The registry also declares each scene's parameters, and that is not
// bookkeeping: it is what lets scripts/atmosphere/flashcheck.cjs enumerate
// every scene and sweep every corner of its parameter space automatically. A
// scene whose parameters are only known to its own draw loop could not be
// swept, and an unswept scene is one nobody has proved is safe to look at.

const SCENES = (() => {
  /** live_signal's preset grammar, reused verbatim. */
  const ID = /^[a-z][a-z0-9-]{0,47}@[1-9][0-9]{0,3}$/;
  const PARAM = /^[a-z][a-z0-9_-]{0,31}$/;
  const MAX_PARAMS = 16;

  /** @type {Map<string, any>} */
  const reg = new Map();

  /**
   * Register a built-in scene. Producer-side strictness (the doctrine in
   * blocks_types.go): a malformed definition throws here, at load, rather
   * than surfacing as a broken picture on somebody else's screen.
   *
   * @param {string} id "name@version"
   * @param {{label:string, params?:Record<string,number>,
   *          make:(env:any)=>{draw:(b:any,t:number,dt:number)=>void, stop?:()=>void}}} def
   */
  function define(id, def) {
    if (!ID.test(id)) throw new Error(`scene id ${id} is not name@version`);
    if (reg.has(id)) throw new Error(`scene ${id} is already defined`);
    if (!def || typeof def.make !== 'function') throw new Error(`scene ${id} has no make()`);
    const params = def.params || {};
    const names = Object.keys(params);
    if (names.length > MAX_PARAMS) throw new Error(`scene ${id} declares too many params`);
    for (const n of names) {
      if (!PARAM.test(n)) throw new Error(`scene ${id} param ${n} is not a name`);
      const v = params[n];
      if (!Number.isInteger(v) || v < 0 || v > 1000) {
        throw new Error(`scene ${id} param ${n} default is not 0..1000 permille`);
      }
    }
    reg.set(id, { id, label: String(def.label || id), params, make: def.make });
  }

  /** @param {string} id */
  function get(id) { return reg.get(id) || null; }

  /** Every known scene id, in a stable order. */
  function list() { return [...reg.keys()].sort(); }

  /**
   * Instantiate a scene from a recipe.
   *
   * Permille in, 0..1 out: the contract carries integers because deterministic
   * CBOR has no floats, and the scene wants a fraction. Converting in exactly
   * one place is what keeps "the parameter is bounded" true rather than
   * something each scene has to remember.
   *
   * @param {string} id
   * @param {{seed?:number, params?:Record<string,number>}} recipe
   * @returns {{draw:(b:any,t:number,dt:number)=>void, stop?:()=>void}|null}
   */
  function make(id, recipe) {
    const d = get(id);
    if (!d) return null;
    const given = (recipe && recipe.params) || {};
    /** @type {Record<string, number>} */
    const p = {};
    for (const n of Object.keys(d.params)) {
      // An unknown parameter name in the recipe is ignored, and a missing one
      // takes the scene's own default. Must-ignore, on this side of the wire
      // too: a recipe written by a newer client still renders here.
      const v = Object.prototype.hasOwnProperty.call(given, n) ? given[n] : d.params[n];
      const n0 = typeof v === 'number' && isFinite(v) ? v : d.params[n];
      p[n] = Math.min(1000, Math.max(0, Math.round(n0))) / 1000;
    }
    try {
      return d.make({ seed: (recipe && recipe.seed) || 0, params: p });
    } catch {
      return null; // a scene that cannot even be built is a missing scene
    }
  }

  return { define, get, list, make };
})();

if (typeof window !== 'undefined') window.SCENES = SCENES;
if (typeof module !== 'undefined' && module.exports) module.exports = SCENES;
