// UI-1 — the built-in space icons.
//
// A curated set, not a generator: a person picks a mark for a room the
// way messengers let them, and the ROOM's manifest carries only the
// NAME of the mark (terminals.SpaceIcons is the allowlist). Nothing a
// client did not ship can ever be drawn — the same rule as presence
// glyphs and resonance palettes: no user-supplied rendering crosses the
// wire. An unknown or absent name falls back to the generated glyph, so
// old manifests and old clients agree on what they see.
//
// Marks, not stickers: 24-box, currentColor, 1.5 stroke, round joins —
// the header's own language, so they take the row's hue and dim with it.

const SPACE_ICON_PATHS = {
  campfire: '<path d="M12 3c1.5 2.5 4.5 4 4.5 8a4.5 4.5 0 0 1-9 0c0-1.6.6-2.8 1.5-3.8.3 1.3 1 2 1.9 2.3C10.6 7.3 10.8 5 12 3Z"/><path d="M5 20l14-3M5 17l14 3" opacity=".55"/>',
  forest: '<path d="M12 3 7.5 10h2.2L6 15.5h4.5V21h3v-5.5H18L14.3 10h2.2L12 3Z"/>',
  studio: '<path d="M4 13c1.5 0 1.5-4 3-4s1.5 6 3 6 1.5-8 3-8 1.5 5 3 5 1.5-3 3-3" opacity=".9"/><path d="M4 18h16" opacity=".4"/>',
  workshop: '<path d="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3Z"/><path d="M12 12l8-4.5M12 12v9M12 12 4 7.5" opacity=".55"/>',
  orbit: '<circle cx="12" cy="12" r="3.2"/><path d="M4.5 9.5c5-4.5 12-4 15 0 3 4.5-2 9.5-7.5 10.5-5.5 1-10.5-2.5-7.5-10.5Z" opacity=".55"/>',
  home: '<path d="M4 11.5 12 4l8 7.5"/><path d="M6.5 10v10h11V10"/><path d="M10 20v-5h4v5" opacity=".55"/>',
  radio: '<circle cx="12" cy="13" r="2"/><path d="M8.5 16.5a5 5 0 0 1 0-7M15.5 9.5a5 5 0 0 1 0 7" opacity=".7"/><path d="M5.5 19.5a9.5 9.5 0 0 1 0-13M18.5 6.5a9.5 9.5 0 0 1 0 13" opacity=".4"/>',
  planet: '<circle cx="12" cy="12" r="5"/><path d="M3.5 15c3-4 12-8 17-6 1 .4 1 1.5-1 3-3 2.5-11 5.5-15 4.5-1-.3-1.3-1-1-1.5Z" opacity=".55"/>',
  comet: '<circle cx="15.5" cy="8.5" r="3.5"/><path d="M12.5 11.5 4 20M11 8 5 11M16 14l-3 6" opacity=".55"/>',
  moon: '<path d="M14 3.5a8.5 8.5 0 1 0 6.5 13.5A7 7 0 0 1 14 3.5Z"/>',
  star: '<path d="M12 3.5l2.3 5.4 5.7.5-4.3 3.8 1.3 5.7-5-3-5 3 1.3-5.7L4 9.4l5.7-.5L12 3.5Z"/>',
  bulb: '<path d="M9 18h6M10 21h4"/><path d="M8 13.5a5 5 0 1 1 8 0c-1 1-1.5 2-1.5 3h-5c0-1-.5-2-1.5-3Z"/>',
  layers: '<path d="M12 4l8 4.5-8 4.5-8-4.5L12 4Z"/><path d="M4 12.5 12 17l8-4.5M4 16.5 12 21l8-4.5" opacity=".55"/>',
  box: '<path d="M4 8.5 12 4l8 4.5v8L12 21l-8-4.5v-8Z"/><path d="M4 8.5 12 13l8-4.5M12 13v8" opacity=".55"/>',
  book: '<path d="M5 4.5h6a2 2 0 0 1 2 2v13a1.5 1.5 0 0 0-1.5-1.5H5v-13.5Z"/><path d="M19 4.5h-6a2 2 0 0 0-2 2v13a1.5 1.5 0 0 1 1.5-1.5H19v-13.5Z" opacity=".55"/>',
  flask: '<path d="M10 3.5h4M10.5 3.5v6L5.5 18a1.5 1.5 0 0 0 1.3 2.5h10.4a1.5 1.5 0 0 0 1.3-2.5l-5-8.5v-6"/><path d="M7.5 15h9" opacity=".55"/>',
  wave: '<path d="M3 12c2-3 4-3 6 0s4 3 6 0 4-3 6 0" /><path d="M3 17c2-3 4-3 6 0s4 3 6 0 4-3 6 0" opacity=".45"/>',
  compass: '<circle cx="12" cy="12" r="8.5"/><path d="m15.5 8.5-2 5-5 2 2-5 5-2Z"/>',
  leaf: '<path d="M19 4.5c-8 0-14 4-14 11 0 2 .8 3.5 2 4.5 1-6 5-10 10-12-4 3-7 7-8 12 7 1 11-5 10-15.5Z"/>',
  signal: '<path d="M4 18V11M8.5 18V8M13 18V5M17.5 18v-8M22 18v-4" />',
};

// Per-archetype defaults: a fresh room gets its family's mark before
// anybody chooses one.
const ARCHETYPE_ICON = {
  campfire: 'campfire', forest: 'forest', studio: 'studio', workshop: 'workshop',
  orbit: 'orbit', home: 'home', radio_room: 'radio',
};

const _iconCache = new Map();
function spaceIconSVG(name, size = 24, color = 'currentColor') {
  const p = SPACE_ICON_PATHS[name];
  if (!p) return '';
  const key = name + '|' + size + '|' + color;
  const hit = _iconCache.get(key);
  if (hit) return hit;
  const svg = `<svg class="sp-icon" viewBox="0 0 24 24" width="${size}" height="${size}" fill="none" stroke="${color}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${p}</svg>`;
  _iconCache.set(key, svg);
  return svg;
}

// spaceAvatarSVG is the one call the list, the header and the cards
// share: the named icon in the room's hue when the manifest names one
// the client knows, the generated glyph otherwise.
function spaceAvatarSVG(sp, size = 24) {
  const name = sp && sp.character && sp.character.icon;
  if (name && SPACE_ICON_PATHS[name]) {
    const col = (typeof glyphColor === 'function') ? glyphColor(sp.id) : 'currentColor';
    return spaceIconSVG(name, size, col);
  }
  return glyphSVG(sp.id, sp && sp.ai ? 'bot' : 'space', size);
}

if (typeof window !== 'undefined') {
  window.spaceIconSVG = spaceIconSVG;
  window.spaceAvatarSVG = spaceAvatarSVG;
  window.SPACE_ICON_PATHS = SPACE_ICON_PATHS;
  window.ARCHETYPE_ICON = ARCHETYPE_ICON;
}
