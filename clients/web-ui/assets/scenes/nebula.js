// nebula@1 — clouds of the recipe's own colours, and a few seeded stars.
//
// The first shader scene, and the one that proved the floor: an fbm field
// advected by a slow rotating flow, the cloud body a ramp between the
// palette's second and third colours over its ground, stars in the fourth.
// Everything a shader must obey lives in shader.js; this file is a picture.

SHADER.define('nebula@1', {
  label: 'Nebula',
  frag: `
vec3 scene(vec2 uv, vec2 q, float t) {
  t *= 0.1;
  float ang = t * 0.35; mat2 rot = mat2(cos(ang), -sin(ang), sin(ang), cos(ang));
  vec2 w = rot * q * (2.0 + 3.0 * u_density) + vec2(t, -t * 0.6);
  float n = fbm(w + fbm(w * 0.7 - t) * 0.8);
  float cloud = smoothstep(0.30, 0.80, n);
  vec3 body = mix(u_pal[1], u_pal[2], smoothstep(0.4, 0.9, n));
  vec3 col = mix(u_pal[0], body, cloud * (0.35 + 0.65 * u_glow));
  // stars: sparse, seeded, gently twinkling — a soft dot inside its cell,
  // not the cell itself, so the third-resolution plate blurs it round.
  vec2 cell = floor(gl_FragCoord.xy / 6.0); vec2 cf = fract(gl_FragCoord.xy / 6.0);
  float s = hash(cell); vec2 centre = vec2(0.25 + 0.5 * hash(cell + 7.1), 0.25 + 0.5 * hash(cell + 3.7));
  float star = step(0.985, s) * smoothstep(0.32, 0.0, length(cf - centre)) * (0.6 + 0.4 * sin(t * 20.0 + s * 40.0));
  return col + star * u_pal[3] * (0.4 + 0.5 * u_glow);
}`,
});
