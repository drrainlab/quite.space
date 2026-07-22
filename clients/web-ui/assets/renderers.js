// Renderer registry + capability negotiation + semantic fallback (SC-0, §34).
//
// A space hands us a declarative composition; we interpret it with our OWN
// trusted renderers, keyed by allowlisted id. An unknown or unsupported
// renderer never executes — it degrades down a semantic chain so meaning
// (title/author/kind) always survives (ADR-013 invariant 1). This file holds
// the client's capability profile and the object renderers used by the
// composition contract; the message feed reuses the same registry seam.

// The local renderer capability profile (client.renderer.capabilities.v1).
// Not published to anyone — used locally to pick renderers and a quality mode.
const CLIENT_CAPABILITIES = {
  renderer_api: 1,
  supported_renderers: {
    'generic.text.v1': [1], 'generic.image.v1': [1], 'generic.audio.v1': [1],
    'generic.video.v1': [1], 'generic.file.v1': [1], 'generic.link.v1': [1],
    'message.relic.v1': [1], 'music.card.v1': [1], 'image.card.v1': [1],
    'video.poster.v1': [1], 'file.card.v1': [1], 'link.card.v1': [1],
    'note.card.v1': [1],
    'shelf.compact.v1': [1], 'ordered-list.v1': [1],
    'wall.freeform-lite.v1': [1], 'card-grid.v1': [1], 'pinned-rail.v1': [1],
  },
  features: { backdrop_blur: true, animated_glyphs: true, freeform_wall: true,
    vector_graffiti: false, audio_playback: true, video_playback: false },
  max_scene_objects: 128,
};

function rendererSupported(id) {
  return Array.isArray(CLIENT_CAPABILITIES.supported_renderers[id]);
}

// renderMode picks Full / Reduced / Minimal from the environment. The client
// keeps the last word on accessibility (ADR-013 refinement 4): reduced data,
// reduced transparency, or a small viewport pull rendering down.
function renderMode() {
  const m = window.matchMedia;
  if (m && (m('(prefers-reduced-transparency: reduce)').matches ||
            m('(prefers-reduced-motion: reduce)').matches)) return 'reduced';
  if (window.innerWidth <= 600) return 'reduced';
  return 'full';
}

// genericModel projects any composition object to the always-renderable
// title/author/detail form — the tail of every fallback chain.
function genericObjectNode(obj) {
  const d = document.createElement('div');
  d.className = 'sc-obj sc-generic';
  const t = document.createElement('div');
  t.className = 'sc-title';
  t.textContent = obj.fallback_title || obj.id || 'item';
  d.appendChild(t);
  const meta = [obj.fallback_author, obj.fallback_detail, obj.semantic_kind]
    .filter(Boolean).join(' · ');
  if (meta) {
    const m = document.createElement('div');
    m.className = 'sc-meta'; m.textContent = meta;
    d.appendChild(m);
  }
  return d;
}

// A small set of first-class object renderers. Each returns a DOM node from
// the object model; none ever runs untrusted markup.
const OBJECT_RENDERERS = {
  'music.card.v1': (o) => {
    const n = genericObjectNode(o); n.classList.add('sc-music'); return n;
  },
  'message.relic.v1': (o) => {
    const n = genericObjectNode(o); n.classList.add('sc-relic'); return n;
  },
  'generic.text.v1': genericObjectNode,
  'generic.audio.v1': genericObjectNode,
  'generic.image.v1': genericObjectNode,
  'generic.video.v1': genericObjectNode,
  'generic.file.v1': genericObjectNode,
  'generic.link.v1': genericObjectNode,
};

// renderObject walks the semantic fallback chain:
//   renderer → fallback_renderer → generic.<kind> → title/author/meta.
// The first supported + known renderer wins; anything unknown degrades. In
// Minimal mode even a supported rich renderer falls back to generic.
function renderObject(obj, mode) {
  const chain = [obj.renderer, obj.fallback_renderer, 'generic.' + obj.semantic_kind + '.v1'];
  for (const id of chain) {
    if (!id) continue;
    if (mode === 'minimal' && !id.startsWith('generic.')) continue;
    if (rendererSupported(id) && OBJECT_RENDERERS[id]) {
      const node = OBJECT_RENDERERS[id](obj);
      node.dataset.renderer = id;
      return node;
    }
  }
  const node = genericObjectNode(obj);
  node.dataset.renderer = 'generic.fallback';
  // Surface the unsupported presentation only in Protocol view.
  if (typeof PROTOCOL !== 'undefined' && PROTOCOL) {
    const w = document.createElement('div');
    w.className = 'sc-meta'; w.textContent = 'Unsupported presentation: ' + (obj.renderer || '?');
    node.appendChild(w);
  }
  return node;
}

// applyScopedAppearance writes an appearance snapshot's palette onto a SCOPED
// container (never :root), so appearance is per-space and can degrade. Minimal
// mode drops any background image for an opaque tint.
function applyScopedAppearance(el, appearance, mode) {
  if (!el || !appearance) return;
  el.classList.add('sc-appearance');
  for (const p of appearance.palette || []) {
    if (/^#[0-9a-fA-F]{6}$/.test(p.hex)) el.style.setProperty('--sc-' + p.name, p.hex);
  }
  const bg = appearance.background;
  if (bg && bg.tint && /^#[0-9a-fA-F]{6}$/.test(bg.tint)) {
    el.style.setProperty('--sc-tint', bg.tint);
    // dim is milliunits [0,1000]; in reduced/minimal we lean opaque.
    const dim = Math.min(1, (bg.dim || 0) / 1000 + (mode !== 'full' ? 0.2 : 0));
    el.style.setProperty('--sc-dim', String(dim));
  }
}
