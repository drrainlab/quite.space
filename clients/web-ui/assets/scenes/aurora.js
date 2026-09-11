// aurora@1 — curtains of light over a dark ground.
//
// A ribbon whose brightness follows noise along x and a soft vertical
// envelope, broken into fine vertical rays; the ribbon's colour crosses
// from the palette's second colour low to its third high, the fourth
// colour is the rays' edge and the stars above. Slow by construction: the
// drift parameter scales a clock that is already a tenth of real time.

SHADER.define('aurora@1', {
  label: 'Aurora',
  frag: `
vec3 scene(vec2 uv, vec2 q, float t) {
  t *= 0.08;
  float x = uv.x * (2.0 + 3.0 * u_density) + fbm(vec2(uv.x * 1.5 + t * 0.3, t)) * 1.5;
  float band = fbm(vec2(x, t * 0.7 + uv.y * 0.6));
  float env = smoothstep(0.05, 0.4, uv.y) * smoothstep(0.98, 0.55, uv.y);
  float ribbon = band * band * env;
  float rays = 0.5 + 0.5 * noise(vec2(x * 6.0 + fbm(vec2(uv.y * 2.0, t)), t * 0.5));
  float a = ribbon * (0.55 + 0.45 * rays) * (0.5 + 0.9 * u_glow);
  vec3 tint = mix(u_pal[1], u_pal[2], smoothstep(0.2, 0.75, uv.y + 0.2 * band));
  vec3 col = mix(u_pal[0], tint, clamp(a * 1.6, 0.0, 1.0));
  col += u_pal[3] * rays * ribbon * 0.25 * u_glow;
  vec2 cell = floor(gl_FragCoord.xy / 5.0); vec2 cf = fract(gl_FragCoord.xy / 5.0);
  float s = hash(cell + 11.0);
  float star = step(0.99, s) * smoothstep(0.3, 0.0, length(cf - 0.5)) * (1.0 - env);
  return col + star * u_pal[3] * 0.5;
}`,
});
