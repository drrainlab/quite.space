// nebula@1 — THE SHADER SCENE (spike).
//
// A fragment shader is code, but of one narrow kind: a pure function of
// (pixel, time, seed, params) → colour, with no state, no DOM, no network,
// sandboxed by the browser's GPU process. That makes it the only kind of
// "code" that can honestly be called a recipe. This spike keeps the GLSL
// built in (the recipe still carries scene id + seed + params, as every
// scene does); whether GLSL may one day RIDE a recipe is a separate
// decision that needs this one to work first.
//
// The floor stays. The shader renders into an offscreen WebGL canvas at a
// third of the stage's resolution, and the frame reaches the stage only
// through BRUSH.plate(): its mean colour is metered and admitted like any
// mark — the frame's luminance may move by at most one step — so a
// shader cannot flash, whatever it computes.

(function () {
  const VERT = `attribute vec2 p; void main(){ gl_Position = vec4(p, 0.0, 1.0); }`;
  // Two octaves of value noise, advected by a slow rotating field; the
  // colour ramp is driven by hue and glow. Deliberately small and cheap.
  const FRAG = `
precision mediump float;
uniform vec2 u_res; uniform float u_time; uniform float u_seed;
uniform float u_hue; uniform float u_drift; uniform float u_density; uniform float u_glow;
float hash(vec2 p){ p = fract(p * vec2(123.34, 456.21) + u_seed); p += dot(p, p + 45.32); return fract(p.x * p.y); }
float noise(vec2 p){ vec2 i = floor(p), f = fract(p); f = f*f*(3.0-2.0*f);
  float a = hash(i), b = hash(i+vec2(1,0)), c = hash(i+vec2(0,1)), d = hash(i+vec2(1,1));
  return mix(mix(a,b,f.x), mix(c,d,f.x), f.y); }
float fbm(vec2 p){ float v = 0.0, a = 0.5; for (int k = 0; k < 4; k++) { v += a * noise(p); p = p * 2.03 + 17.0; a *= 0.5; } return v; }
vec3 hsl(float h, float s, float l){ vec3 rgb = clamp(abs(mod(h*6.0 + vec3(0,4,2), 6.0) - 3.0) - 1.0, 0.0, 1.0); return l + s * (rgb - 0.5) * (1.0 - abs(2.0*l - 1.0)); }
void main(){
  vec2 uv = gl_FragCoord.xy / u_res; vec2 q = (uv - 0.5) * vec2(u_res.x / u_res.y, 1.0);
  float t = u_time * (0.02 + 0.08 * u_drift);
  float ang = t * 0.35; mat2 rot = mat2(cos(ang), -sin(ang), sin(ang), cos(ang));
  vec2 w = rot * q * (2.0 + 3.0 * u_density) + vec2(t, -t * 0.6);
  float n = fbm(w + fbm(w * 0.7 - t) * 0.8);
  float cloud = smoothstep(0.30, 0.80, n);
  float h = fract(u_hue + 0.08 * n);
  vec3 col = hsl(h, 0.55, 0.10 + 0.42 * cloud * (0.5 + 0.5 * u_glow));
  vec3 deep = hsl(fract(u_hue + 0.55), 0.35, 0.06);
  col = mix(deep, col, 0.35 + 0.65 * cloud);
  // stars: sparse, seeded, gently twinkling — a soft dot inside its cell,
  // not the cell itself, so the third-resolution plate blurs it round.
  vec2 cell = floor(gl_FragCoord.xy / 6.0); vec2 cf = fract(gl_FragCoord.xy / 6.0);
  float s = hash(cell); vec2 centre = vec2(0.25 + 0.5 * hash(cell + 7.1), 0.25 + 0.5 * hash(cell + 3.7));
  float star = step(0.985, s) * smoothstep(0.32, 0.0, length(cf - centre)) * (0.6 + 0.4 * sin(u_time * 2.0 + s * 40.0));
  col += star * (0.4 + 0.5 * u_glow);
  gl_FragColor = vec4(col, 1.0);
}`;

  function compile(gl, type, src) {
    const sh = gl.createShader(type);
    gl.shaderSource(sh, src); gl.compileShader(sh);
    if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) throw new Error(gl.getShaderInfoLog(sh) || 'shader');
    return sh;
  }

  function make(env) {
    const p = env.params;
    // No document (the Node harness) or no WebGL: a scene that admits
    // nothing rather than a broken one — the still and the fallback text
    // stand in, exactly as for a scene this build does not have.
    if (typeof document === 'undefined') return { draw() {} };
    const cv = document.createElement('canvas');
    const gl = cv.getContext('webgl', { alpha: false, antialias: false, preserveDrawingBuffer: false,
      powerPreference: 'low-power', depth: false, stencil: false });
    if (!gl) return { draw() {} };
    let prog, uni = {};
    try {
      prog = gl.createProgram();
      gl.attachShader(prog, compile(gl, gl.VERTEX_SHADER, VERT));
      gl.attachShader(prog, compile(gl, gl.FRAGMENT_SHADER, FRAG));
      gl.linkProgram(prog);
      if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) throw new Error('link');
      gl.useProgram(prog);
      const buf = gl.createBuffer();
      gl.bindBuffer(gl.ARRAY_BUFFER, buf);
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 3, -1, -1, 3]), gl.STATIC_DRAW);
      const loc = gl.getAttribLocation(prog, 'p');
      gl.enableVertexAttribArray(loc);
      gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);
      for (const n of ['u_res', 'u_time', 'u_seed', 'u_hue', 'u_drift', 'u_density', 'u_glow']) uni[n] = gl.getUniformLocation(prog, n);
    } catch { return { draw() {} }; }
    const seed = ((env.seed || 0) % 1000) / 1000;
    let lost = false;
    cv.addEventListener('webglcontextlost', (e) => { e.preventDefault(); lost = true; });
    return {
      draw(b, t, dt) {
        if (lost) return;
        // A third of the stage's pixels: a shader is dearer than a flow field,
        // and the plate is blurred by the floor's own alpha anyway.
        const w = Math.max(8, Math.round(b.width / 3)), h = Math.max(8, Math.round(b.height / 3));
        if (cv.width !== w || cv.height !== h) { cv.width = w; cv.height = h; gl.viewport(0, 0, w, h); }
        gl.uniform2f(uni.u_res, w, h);
        gl.uniform1f(uni.u_time, t);
        gl.uniform1f(uni.u_seed, seed);
        gl.uniform1f(uni.u_hue, p.hue); gl.uniform1f(uni.u_drift, p.drift);
        gl.uniform1f(uni.u_density, p.density); gl.uniform1f(uni.u_glow, p.glow);
        gl.drawArrays(gl.TRIANGLES, 0, 3);
        b.plate(cv, 0.85);
      },
      stop() {
        try { const ext = gl.getExtension('WEBGL_lose_context'); if (ext) ext.loseContext(); } catch {}
      },
    };
  }

  SCENES.define('nebula@1', {
    label: 'Nebula (shader · lab)',
    lab: true,
    params: { hue: 700, drift: 400, density: 450, glow: 500 },
    make,
  });
})();
