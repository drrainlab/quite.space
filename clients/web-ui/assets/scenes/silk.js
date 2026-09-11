// silk@1 — sheets of cloth folding in slow air.
//
// Four passes of a sine domain warp, each finer than the last; two folded
// coordinates pick where on the palette a thread sits and how much of the
// ground shows through, and a narrow sheen in the fourth colour rides the
// folds. Fully covered — there is no dark sky here — so the floor's
// luminance law is what paces its arrival: the sheet fades in over a few
// dozen frames rather than appearing.

SHADER.define('silk@1', {
  label: 'Silk',
  frag: `
vec3 scene(vec2 uv, vec2 q, float t) {
  t *= 0.15;
  vec2 p = q * (1.2 + 2.0 * u_density);
  for (int k = 1; k < 5; k++) {
    float fk = float(k);
    p.x += 0.35 / fk * sin(fk * 1.8 * p.y + t + 0.4 * fk) + 0.8;
    p.y += 0.35 / fk * cos(fk * 1.8 * p.x + t + 0.4 * fk) - 0.8;
  }
  float r = 0.5 + 0.5 * sin(2.5 * p.x);
  float g = 0.5 + 0.5 * sin(2.5 * p.y + 1.0);
  float sheen = pow(0.5 + 0.5 * sin(4.0 * (p.x + p.y)), 3.0);
  vec3 col = mix(u_pal[1], u_pal[2], r);
  col = mix(u_pal[0], col, 0.35 + 0.5 * g);
  return col + u_pal[3] * sheen * 0.25 * u_glow;
}`,
});
