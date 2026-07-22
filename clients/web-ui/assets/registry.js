// Runtime object registry (LR-0a) — the client half of protocol/contract.
//
// New wave objects (Shelf entries, listening-room) render through THIS
// registry with a fallback chain; existing feed/publication/app renderers do
// not migrate here until LR-0b (Registry Consolidation). Mirrors the server
// registry's strictness: duplicate registration throws, freeze() makes it
// read-only.
//
// A registered renderer receives (model, ctx) and returns a DOM node. The
// fallback chain never executes untrusted markup:
//   renderer(model) → text fallback card → opaque placeholder.

const RUNTIME_REGISTRY = (() => {
  const renderers = new Map();
  let frozen = false;

  function register(schemaId, renderFn) {
    if (frozen) throw new Error('runtime registry frozen: ' + schemaId);
    if (renderers.has(schemaId)) throw new Error('duplicate renderer: ' + schemaId);
    if (typeof renderFn !== 'function') throw new Error('renderer must be a function');
    renderers.set(schemaId, renderFn);
  }

  function freeze() { frozen = true; }
  function known(schemaId) { return renderers.has(schemaId); }

  // fallbackCard renders the mandatory text fallback — meaning survives even
  // when no renderer exists or a renderer throws.
  function fallbackCard(schemaId, model) {
    const d = document.createElement('div');
    d.className = 'rt-obj rt-fallback';
    const t = document.createElement('div');
    t.className = 'rt-fallback-text';
    t.textContent = (model && model.fallback) || 'object · ' + (schemaId || 'unknown');
    d.appendChild(t);
    d.dataset.schema = schemaId || '';
    return d;
  }

  // render walks the chain. ctx is renderer-defined (space id, api helpers…).
  function render(schemaId, model, ctx) {
    const fn = renderers.get(schemaId);
    if (fn) {
      try {
        const node = fn(model, ctx || {});
        if (node instanceof Node) {
          if (node instanceof Element) node.dataset.schema = schemaId;
          return node;
        }
      } catch (e) {
        console.warn('renderer failed, falling back', schemaId, e);
      }
    }
    return fallbackCard(schemaId, model);
  }

  return { register, freeze, known, render, fallbackCard };
})();
