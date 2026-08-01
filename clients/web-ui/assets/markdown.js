// @ts-check
'use strict';
/**
 * MD — the post text block's language.
 *
 * A publication's text has always been plain text, and authors have always
 * written markdown into it anyway, because that is what people write. So the
 * renderer learns to read it. Nothing about the WIRE changes: the document
 * still carries the author's characters exactly as they typed them, and a
 * client that has never heard of this file shows those characters — which is
 * why markdown is the right language to adopt and, say, HTML is not. Its
 * degraded form is the source, and the source is readable (ADR-013 §1).
 *
 * WHAT THIS DELIBERATELY CANNOT DO, and why each one is a rule rather than a
 * missing feature (ADR-013 §2 — no executable payload, ever):
 *
 *   · no HTML passthrough. Angle brackets are characters. This builds a DOM
 *     TREE and never assigns innerHTML, so there is no parser here for a
 *     payload to reach even if one were written.
 *   · no images. `![](url)` would be a request to a host of the author's
 *     choosing the moment a reader opens the post — a beacon that reports
 *     who read what, and when, to whoever wrote it. Media belongs in media
 *     blocks, which are content-addressed and fetched on the reader's terms.
 *   · links only to schemes this app already trusts a person to follow, and
 *     never auto-followed. `javascript:` and `data:` are text.
 *
 * Bounded by construction: one pass, no backtracking, every loop over the
 * line array, nesting one level deep. A document is already size-capped by
 * the contract, so there is no input here that costs more than reading it.
 */
const MD = (() => {
  /** Schemes a link may carry. Everything else stays literal text. */
  const SAFE_SCHEME = /^(https?:|qs:|quiet:|mailto:)/i;

  /** Inline spans, in the order they are recognised. */
  const CODE = /`([^`\n]+)`/;
  const BOLD = /\*\*([^\n]+?)\*\*|__([^\n]+?)__/;
  const ITAL = /(?<![*\w])\*([^*\n]+?)\*(?!\*)|(?<![_\w])_([^_\n]+?)_(?!_)/;
  const LINK = /\[([^\]\n]*)\]\(([^)\s]+)\)/;
  const IMG = /!\[([^\]\n]*)\]\(([^)\s]+)\)/;

  function el(tag, text) {
    const n = document.createElement(tag);
    if (text != null) n.textContent = text;
    return n;
  }

  /**
   * One line of inline markup into nodes.
   *
   * Order matters: code first, so `**` inside a code span stays literal —
   * which is the difference between showing somebody the syntax and applying
   * it to them.
   *
   * @param {string} src
   * @returns {Node[]}
   */
  function inline(src) {
    const out = [];
    let rest = String(src);
    while (rest) {
      const m = first(rest);
      if (!m) { out.push(document.createTextNode(rest)); break; }
      if (m.index > 0) out.push(document.createTextNode(rest.slice(0, m.index)));
      out.push(m.node);
      rest = rest.slice(m.index + m.len);
    }
    return out;
  }

  /** The earliest inline construct in the line, or null. */
  function first(s) {
    /** @type {{index:number,len:number,node:Node}|null} */
    let best = null;
    const take = (re, make) => {
      const m = re.exec(s);
      if (!m) return;
      if (best && m.index >= best.index) return;
      best = { index: m.index, len: m[0].length, node: make(m) };
    };
    take(CODE, (m) => el('code', m[1]));
    // An image is recognised only to be REFUSED as one: it renders as its
    // words plus the address, so the author's meaning survives and nothing
    // is fetched from a stranger's host when a reader opens the post.
    take(IMG, (m) => {
      const span = el('span', (m[1] || 'image') + ' (' + m[2] + ')');
      span.className = 'md-noimg';
      span.title = 'images in text are shown as their address — use an image block';
      return span;
    });
    take(LINK, (m) => {
      if (!SAFE_SCHEME.test(m[2])) return document.createTextNode(m[0]);
      const a = el('a', m[1] || m[2]);
      a.setAttribute('href', m[2]);
      a.setAttribute('target', '_blank');
      a.setAttribute('rel', 'noopener noreferrer nofollow');
      return a;
    });
    take(BOLD, (m) => { const b = el('strong'); b.append(...inline(m[1] ?? m[2])); return b; });
    take(ITAL, (m) => { const i = el('em'); i.append(...inline(m[1] ?? m[2])); return i; });
    return best;
  }

  /** Is this line a fence? */
  const isFence = (l) => /^\s*```/.test(l);

  /**
   * Render markdown source into a fragment.
   *
   * @param {string} src
   * @returns {DocumentFragment}
   */
  function render(src) {
    const frag = document.createDocumentFragment();
    const lines = String(src == null ? '' : src).split(/\r?\n/);
    let i = 0;
    /** Paragraph lines waiting to be flushed. */
    let para = [];
    const flush = () => {
      if (!para.length) return;
      const p = el('p');
      // A single newline inside a paragraph is a line break, not a new
      // paragraph — people write addresses and verse that way and expect
      // them to hold their shape.
      para.forEach((line, k) => {
        if (k) p.appendChild(el('br'));
        p.append(...inline(line));
      });
      frag.appendChild(p);
      para = [];
    };

    while (i < lines.length) {
      const line = lines[i];

      if (isFence(line)) {                       // ``` fenced code
        flush();
        const body = [];
        i++;
        while (i < lines.length && !isFence(lines[i])) body.push(lines[i++]);
        i++;                                     // the closing fence, if any
        const pre = el('pre');
        pre.appendChild(el('code', body.join('\n')));
        frag.appendChild(pre);
        continue;
      }

      const h = /^(#{1,6})\s+(.*)$/.exec(line);
      if (h) {
        flush();
        // The article's own title is the h1 on the page, so a heading inside
        // the body starts one level down and can never impersonate it.
        const node = el('h' + Math.min(6, h[1].length + 1));
        node.append(...inline(h[2]));
        frag.appendChild(node);
        i++; continue;
      }

      if (/^\s*([-*_])(\s*\1){2,}\s*$/.test(line)) {   // --- *** ___
        flush(); frag.appendChild(el('hr')); i++; continue;
      }

      if (/^\s*>\s?/.test(line)) {               // > blockquote
        flush();
        const body = [];
        while (i < lines.length && /^\s*>\s?/.test(lines[i])) {
          body.push(lines[i].replace(/^\s*>\s?/, '')); i++;
        }
        const q = el('blockquote');
        // Appending the fragment spreads it — a quote holds the blocks it
        // quotes, not a wrapper around them.
        q.appendChild(render(body.join('\n')));
        frag.appendChild(q);
        continue;
      }

      const bullet = /^\s*[-*+]\s+(.*)$/.exec(line);
      const number = /^\s*\d+[.)]\s+(.*)$/.exec(line);
      if (bullet || number) {
        flush();
        const ordered = !!number;
        const list = el(ordered ? 'ol' : 'ul');
        while (i < lines.length) {
          const m = ordered
            ? /^\s*\d+[.)]\s+(.*)$/.exec(lines[i])
            : /^\s*[-*+]\s+(.*)$/.exec(lines[i]);
          if (!m) break;
          const li = el('li');
          li.append(...inline(m[1]));
          list.appendChild(li);
          i++;
        }
        frag.appendChild(list);
        continue;
      }

      if (!line.trim()) { flush(); i++; continue; }
      para.push(line);
      i++;
    }
    flush();
    return frag;
  }

  /**
   * Which script this text is mostly in, as a BCP-47 tag or ''.
   *
   * Asked once for a whole POST, never per character and never per block.
   *
   * CSS can switch faces per unicode-range and it is the wrong tool here: a
   * Russian paragraph is full of Latin — quite.space, a URL, a borrowed word
   * — and per-range switching would set those few words in another face
   * mid-sentence. Per block is wrong too, one step up: "Тишина." is six
   * letters and would be too short to call, so it would keep the default
   * face while the paragraph under it changed. A post is written in a
   * language, and that is the unit a reader experiences.
   *
   * Returns '' when there is too little to be sure, which for a whole
   * document means it is not really prose in either.
   *
   * @param {string} s
   */
  function scriptOf(s) {
    let cyr = 0, lat = 0;
    for (const ch of String(s)) {
      if (ch >= 'Ѐ' && ch <= 'ӿ') cyr++;
      else if ((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')) lat++;
    }
    if (cyr + lat < 8) return '';        // too little to call; leave it alone
    // Asymmetric, on purpose. An English post essentially never contains
    // Cyrillic, while a Russian one routinely carries Latin — product names,
    // URLs, borrowed words, technical terms often LONGER than the Russian
    // around them. "Мы делаем quite.space и relay, а не messenger" has twice
    // as many Latin letters as Cyrillic ones and is plainly a Russian
    // sentence, so whichever-side-has-more is the wrong question. A fifth of
    // the letters being Cyrillic is enough to decide.
    return cyr * 4 >= lat ? 'ru' : 'en';
  }

  /**
   * Render into a host element, replacing whatever it held.
   * @param {HTMLElement} host @param {string} src
   */
  function into(host, src) {
    host.textContent = '';
    host.appendChild(render(src));
    return host;
  }

  return { render, into, scriptOf };
})();

if (typeof window !== 'undefined') window.MD = MD;
