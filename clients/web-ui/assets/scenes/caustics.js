// caustics@1 — light through moving water.
//
// The net of bright lines on a pool floor is where light focuses along
// the boundaries between ripples, and the cheapest honest model of that
// is the edge of a Voronoi cell: F2 − F1 over a jittered grid whose seeds
// circle in time. Two octaves, the finer one turned and faster, so the
// net breathes rather than slides. The water between the lines is the
// palette's ground and second colour; the light itself crosses from the
// third to the fourth. The clock is a fifth of real time — a caustic
// that races is a swimming pool, a caustic that breathes is a window.

SHADER.define('caustics@1', {
  label: 'Caustics',
  frag: `
float edge(vec2 p, float t) {
  vec2 i = floor(p), f = fract(p);
  float d1 = 8.0, d2 = 8.0;
  for (int y = -1; y <= 1; y++) for (int x = -1; x <= 1; x++) {
    vec2 g = vec2(float(x), float(y));
    vec2 o = vec2(hash(i + g), hash(i + g + 7.3));
    o = 0.5 + 0.42 * sin(t + 6.2832 * o);
    float d = length(g + o - f);
    if (d < d1) { d2 = d1; d1 = d; } else if (d < d2) { d2 = d; }
  }
  return d2 - d1;
}
vec3 scene(vec2 uv, vec2 q, float t) {
  t *= 0.2;
  float k = 3.0 + 4.0 * u_density;
  float e1 = edge(q * k, t);
  float ang = 0.6; mat2 rot = mat2(cos(ang), -sin(ang), sin(ang), cos(ang));
  float e2 = edge(rot * q * k * 1.7 + 3.1, t * 1.3 + 2.0);
  float line = exp(-e1 * e1 * 60.0) * 0.8 + exp(-e2 * e2 * 90.0) * 0.5;
  float v = clamp(line, 0.0, 1.0);
  vec3 water = mix(u_pal[0], u_pal[1], 0.25 + 0.3 * fbm(q * 2.0 + t * 0.3));
  vec3 light = mix(u_pal[2], u_pal[3], v);
  return mix(water, light, v * (0.35 + 0.55 * u_glow));
}`,
});
