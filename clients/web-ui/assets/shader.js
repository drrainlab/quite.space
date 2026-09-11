// @ts-check
// The shader engine: the one place WebGL is spoken.
//
// A fragment shader is code, but of one narrow kind — a pure function of
// (pixel, time, seed, palette, params) → colour, with no state, no DOM, no
// network, sandboxed by the browser's GPU process. That is the only kind
// of "program" that can honestly be called a recipe, and it is why the
// scenes in scenes/*.js are allowed to be one. Every scene is still an
// ALLOWLISTED id (ADR-013 invariant 2): the GLSL ships with the build, and
// a recipe carries a name, a seed, a palette and permille parameters —
// never shader source.
//
// The floor stays underneath. A scene renders into an OFFSCREEN canvas at
// a fraction of the stage's resolution and reaches the stage only through
// BRUSH's `plate`, which meters the frame's mean colour and admits it by
// the same luminance-step law as any brush mark. A shader cannot flash,
// whatever it computes.
//
// What a scene writes is `vec3 scene(vec2 uv, vec2 q, float t)`: uv in
// 0..1, q centred and aspect-corrected, t in seconds already scaled by the
// scene's `drift`. The prelude gives it hash/noise/fbm, the palette as
// u_pal[0..3] (index 0 is the ground, as everywhere) and one uniform per
// declared parameter, `u_<name>`, in 0..1.

const SHADER = (() => {
  const VERT = 'attribute vec2 p; void main(){ gl_Position = vec4(p, 0.0, 1.0); }';

  const PRELUDE = `
precision mediump float;
uniform vec2 u_res; uniform float u_time; uniform float u_seed; uniform vec3 u_pal[4];
float hash(vec2 p){ p = fract(p * vec2(123.34, 456.21) + u_seed); p += dot(p, p + 45.32); return fract(p.x * p.y); }
float noise(vec2 p){ vec2 i = floor(p), f = fract(p); f = f*f*(3.0-2.0*f);
  float a = hash(i), b = hash(i+vec2(1,0)), c = hash(i+vec2(0,1)), d = hash(i+vec2(1,1));
  return mix(mix(a,b,f.x), mix(c,d,f.x), f.y); }
float fbm(vec2 p){ float v = 0.0, a = 0.5; for (int k = 0; k < 4; k++) { v += a * noise(p); p = p * 2.03 + 17.0; a *= 0.5; } return v; }
`;

  const MAIN = `
void main(){
  vec2 uv = gl_FragCoord.xy / u_res;
  vec2 q = (uv - 0.5) * vec2(u_res.x / u_res.y, 1.0);
  gl_FragColor = vec4(clamp(scene(uv, q, u_time), 0.0, 1.0), 1.0);
}`;

  /** Every shader scene shares these three; a scene may add its own. */
  const COMMON = { drift: 400, density: 500, glow: 500 };

  /** How much of the stage a shader paints, in pixels per axis. */
  const SCALE = 1 / 3;
  /** The plate's alpha: the frame mostly replaces the last one, softly. */
  const PLATE_ALPHA = 0.85;

  /** @param {string} hex @returns {[number,number,number]} */
  function rgb(hex) {
    const n = parseInt(String(hex || '#000000').slice(1), 16) || 0;
    return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
  }

  /** @param {WebGLRenderingContext} gl @param {number} type @param {string} src */
  function compile(gl, type, src) {
    const sh = gl.createShader(type);
    if (!sh) throw new Error('shader');
    gl.shaderSource(sh, src);
    gl.compileShader(sh);
    if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(sh) || 'shader');
    return sh;
  }

  /**
   * Build one scene instance. No document (the flash harness), a document
   * without canvases (the DOM stub), or no WebGL: a scene that admits
   * nothing rather than a broken one — the still and the fallback text
   * stand in, exactly as for a scene this build does not have.
   *
   * @param {{seed?:number, params:Record<string,number>}} env
   * @param {string} frag
   * @param {string[]} names
   */
  function build(env, frag, names) {
    const silent = { draw() {} };
    if (typeof document === 'undefined') return silent;
    let cv, gl = null;
    try {
      cv = document.createElement('canvas');
      if (cv && typeof cv.getContext === 'function') {
        gl = cv.getContext('webgl', { alpha: false, antialias: false, preserveDrawingBuffer: false,
          powerPreference: 'low-power', depth: false, stencil: false });
      }
    } catch { gl = null; }
    if (!gl) return silent;
    /** @type {Record<string, WebGLUniformLocation|null>} */
    const uni = {};
    try {
      const prog = gl.createProgram();
      if (!prog) throw new Error('program');
      gl.attachShader(prog, compile(gl, gl.VERTEX_SHADER, VERT));
      gl.attachShader(prog, compile(gl, gl.FRAGMENT_SHADER, PRELUDE + declare(names) + frag + MAIN));
      gl.linkProgram(prog);
      if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) throw new Error(gl.getProgramInfoLog(prog) || 'link');
      gl.useProgram(prog);
      const buf = gl.createBuffer();
      gl.bindBuffer(gl.ARRAY_BUFFER, buf);
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
      const loc = gl.getAttribLocation(prog, 'p');
      gl.enableVertexAttribArray(loc);
      gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
      for (const n of ['u_res', 'u_time', 'u_seed', 'u_pal[0]'].concat(names.map(n => 'u_' + n))) {
        uni[n] = gl.getUniformLocation(prog, n);
      }
      // Parameters and seed never change for the life of an instance.
      gl.uniform1f(uni.u_seed, ((Number(env.seed) || 0) % 1000) / 1000);
      for (const n of names) gl.uniform1f(uni['u_' + n], Number(env.params[n]) || 0);
    } catch { return silent; }
    let lost = false, palKey = '';
    const pal = new Float32Array(12);
    cv.addEventListener('webglcontextlost', (e) => { e.preventDefault(); lost = true; });
    const drift = 0.2 + 0.8 * (Number(env.params.drift) || 0);
    return {
      draw(b, t) {
        if (lost) return;
        const w = Math.max(8, Math.round(b.width * SCALE)), h = Math.max(8, Math.round(b.height * SCALE));
        if (cv.width !== w || cv.height !== h) { cv.width = w; cv.height = h; gl.viewport(0, 0, w, h); }
        // The recipe's palette, so the poster and the scene agree on
        // colour. Four slots; a shorter palette repeats its last colour.
        const p = b.palette || [];
        const key = p.join(',');
        if (key !== palKey) {
          palKey = key;
          for (let i = 0; i < 4; i++) {
            const c = rgb(p[Math.min(i, p.length - 1)]);
            pal[i * 3] = c[0]; pal[i * 3 + 1] = c[1]; pal[i * 3 + 2] = c[2];
          }
          gl.uniform3fv(uni['u_pal[0]'], pal);
        }
        gl.uniform2f(uni.u_res, w, h);
        gl.uniform1f(uni.u_time, t * drift);
        gl.drawArrays(gl.TRIANGLES, 0, 3);
        b.plate(cv, PLATE_ALPHA);
      },
      stop() {
        try { const ext = gl.getExtension('WEBGL_lose_context'); if (ext) ext.loseContext(); } catch { /* gone already */ }
      },
    };
  }

  /** @param {string[]} names */
  function declare(names) {
    return names.map(n => `uniform float u_${n};`).join(' ') + '\n';
  }

  /**
   * Register a shader scene. `params` extends the common three; `frag` is
   * the body that defines `vec3 scene(vec2 uv, vec2 q, float t)`.
   *
   * @param {string} id
   * @param {{label:string, params?:Record<string,number>, frag:string}} def
   */
  function define(id, def) {
    const params = Object.assign({}, COMMON, def.params || {});
    const names = Object.keys(params);
    SCENES.define(id, {
      label: def.label,
      params,
      make: (env) => build(env, def.frag, names),
    });
  }

  return { define, SCALE, PLATE_ALPHA };
})();

if (typeof window !== 'undefined') window.SHADER = SHADER;
if (typeof module !== 'undefined' && module.exports) module.exports = SHADER;
