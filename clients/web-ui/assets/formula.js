// @ts-check
'use strict';
/**
 * FORMULA — TeX arithmetic into MathML, as nodes.
 *
 * People paste formulas the way they paste tables: an assistant wrote
 * `$$ C_{\text{заказа}} = C_{\text{материала}} + \ldots $$` and the chat
 * showed the backslashes. Browsers have rendered MathML natively for a
 * while now (Chrome 109, Safari, Firefox), so the honest fix is a small
 * translator from the TeX people actually type into a MathML TREE — no
 * library, no fonts to ship, no innerHTML (ADR-013 §2), and the source
 * always survives: whatever this cannot read is shown as the characters
 * the author typed, in a code span, never dropped and never guessed.
 *
 * The subset is the one that appears in messages: scripts, fractions,
 * roots, text runs, Greek and the usual operators, big operators with
 * limits, \left...\right, accents, matrices and cases. Bounded by
 * construction: one pass over the tokens, recursion only into braces.
 */
const FORMULA = (() => {
  const NS = 'http://www.w3.org/1998/Math/MathML';

  /** Symbols: a command and the character it stands for (as an mi or mo). */
  const SYM = {
    // Greek
    alpha: 'α', beta: 'β', gamma: 'γ', delta: 'δ', epsilon: 'ε', varepsilon: 'ε',
    zeta: 'ζ', eta: 'η', theta: 'θ', vartheta: 'ϑ', iota: 'ι', kappa: 'κ',
    lambda: 'λ', mu: 'μ', nu: 'ν', xi: 'ξ', pi: 'π', varpi: 'ϖ', rho: 'ρ',
    varrho: 'ϱ', sigma: 'σ', varsigma: 'ς', tau: 'τ', upsilon: 'υ', phi: 'ϕ',
    varphi: 'φ', chi: 'χ', psi: 'ψ', omega: 'ω',
    Gamma: 'Γ', Delta: 'Δ', Theta: 'Θ', Lambda: 'Λ', Xi: 'Ξ', Pi: 'Π',
    Sigma: 'Σ', Upsilon: 'Υ', Phi: 'Φ', Psi: 'Ψ', Omega: 'Ω',
    // letters and constants
    infty: '∞', partial: '∂', nabla: '∇', hbar: 'ℏ', ell: 'ℓ', Re: 'ℜ', Im: 'ℑ',
    aleph: 'ℵ', emptyset: '∅', varnothing: '∅', degree: '°',
  };
  const OPS = {
    cdot: '⋅', times: '×', div: '÷', pm: '±', mp: '∓', ast: '∗', star: '⋆',
    circ: '∘', bullet: '∙', oplus: '⊕', otimes: '⊗',
    le: '≤', leq: '≤', ge: '≥', geq: '≥', ne: '≠', neq: '≠', approx: '≈',
    equiv: '≡', sim: '∼', simeq: '≃', cong: '≅', propto: '∝', ll: '≪', gg: '≫',
    to: '→', rightarrow: '→', leftarrow: '←', leftrightarrow: '↔',
    Rightarrow: '⇒', Leftarrow: '⇐', Leftrightarrow: '⇔', mapsto: '↦',
    uparrow: '↑', downarrow: '↓', implies: '⇒', iff: '⇔',
    in: '∈', notin: '∉', ni: '∋', subset: '⊂', subseteq: '⊆', supset: '⊃',
    supseteq: '⊇', cup: '∪', cap: '∩', setminus: '∖', land: '∧', lor: '∨',
    lnot: '¬', neg: '¬', forall: '∀', exists: '∃', nexists: '∄', therefore: '∴',
    because: '∵', mid: '∣', parallel: '∥', perp: '⊥', angle: '∠', top: '⊤', bot: '⊥',
    cdots: '⋯', ldots: '…', dots: '…', vdots: '⋮', ddots: '⋱',
    langle: '⟨', rangle: '⟩', lfloor: '⌊', rfloor: '⌋', lceil: '⌈', rceil: '⌉',
    lvert: '|', rvert: '|', lVert: '‖', rVert: '‖', vert: '|', Vert: '‖',
    prime: '′', backslash: '\\',
  };
  /** Big operators: rendered as mo with limits stacked in display mode. */
  const BIG = {
    sum: '∑', prod: '∏', coprod: '∐', int: '∫', iint: '∬', iiint: '∭',
    oint: '∮', bigcup: '⋃', bigcap: '⋂', bigvee: '⋁', bigwedge: '⋀',
  };
  /** Named functions: upright text, limits below in display mode. */
  const FUNC = new Set(['sin', 'cos', 'tan', 'cot', 'sec', 'csc', 'arcsin', 'arccos',
    'arctan', 'sinh', 'cosh', 'tanh', 'ln', 'log', 'lg', 'exp', 'det', 'dim',
    'ker', 'deg', 'gcd', 'arg', 'hom', 'Pr']);
  const LIMFUNC = new Set(['lim', 'max', 'min', 'sup', 'inf', 'limsup', 'liminf']);
  /** Accents: command → the combining mark placed over (or under) the base. */
  const ACCENT = {
    hat: '^', widehat: '^', bar: '¯', overline: '¯', vec: '→', overrightarrow: '→',
    dot: '˙', ddot: '¨', tilde: '~', widetilde: '~', check: 'ˇ', breve: '˘', acute: '´', grave: '`',
  };
  const UNDER = { underline: '_', underbrace: '⏟', overbrace: '⏞' };
  const SPACE = { ',': '0.17em', ':': '0.22em', ';': '0.28em', '!': '-0.17em',
    ' ': '0.3em', quad: '1em', qquad: '2em', enspace: '0.5em', thinspace: '0.17em' };
  /** Font variants for one-argument styling commands. */
  const VARIANT = { mathbf: 'bold', boldsymbol: 'bold', textbf: 'bold', mathit: 'italic',
    mathrm: 'normal', textrm: 'normal', mathbb: 'double-struck', mathcal: 'script',
    mathscr: 'script', mathfrak: 'fraktur', mathsf: 'sans-serif', mathtt: 'monospace' };
  /** Environments, and what wraps their table. */
  const ENV = { matrix: ['', ''], pmatrix: ['(', ')'], bmatrix: ['[', ']'],
    Bmatrix: ['{', '}'], vmatrix: ['|', '|'], Vmatrix: ['‖', '‖'],
    cases: ['{', ''], aligned: ['', ''], align: ['', ''], 'align*': ['', ''],
    array: ['', ''], gathered: ['', ''], split: ['', ''] };

  function mk(tag, text) {
    const n = document.createElementNS(NS, tag);
    if (text != null) n.textContent = text;
    return n;
  }
  const mi = (t) => mk('mi', t);
  const mo = (t) => mk('mo', t);
  const mn = (t) => mk('mn', t);
  const row = (nodes) => {
    if (nodes.length === 1) return nodes[0];
    const r = mk('mrow');
    nodes.forEach((n) => r.appendChild(n));
    return r;
  };

  /**
   * Tokens: {t: 'cmd', v} {t: 'ch', v} {t: '{'} {t: '}'} {t: '^'} {t: '_'} {t: '&'}.
   * Whitespace is dropped here; \text reads the source directly by index.
   */
  function tokenize(src) {
    const toks = [];
    let i = 0;
    while (i < src.length) {
      const c = src[i];
      if (c === '\\') {
        const m = /^\\([A-Za-z]+\*?|.)/.exec(src.slice(i));
        if (!m) { i++; continue; }
        toks.push({ t: 'cmd', v: m[1], at: i + m[0].length });
        i += m[0].length;
        continue;
      }
      if (/\s/.test(c)) { i++; continue; }
      if ('{}^_&'.includes(c)) { toks.push({ t: c, at: i + 1 }); i++; continue; }
      toks.push({ t: 'ch', v: c, at: i + 1 });
      i++;
    }
    return toks;
  }

  /**
   * Parse one formula. Throws on structure it cannot read; the caller then
   * shows the source. Never throws on an unknown command — that renders as
   * its own name, marked, so the meaning is at least visible.
   */
  function parse(src, display) {
    const toks = tokenize(src);
    let p = 0;
    const peek = () => toks[p];
    const next = () => toks[p++];

    /** A brace group's raw source text, for \text and friends. */
    function rawGroup() {
      const open = next();
      if (!open || open.t !== '{') throw new Error('expected {');
      let depth = 1;
      const start = open.at;
      let i = start;
      while (i < src.length && depth > 0) {
        if (src[i] === '\\') { i += 2; continue; }
        if (src[i] === '{') depth++;
        else if (src[i] === '}') depth--;
        i++;
      }
      if (depth !== 0) throw new Error('unbalanced {');
      // Skip the tokens the raw scan consumed.
      while (p < toks.length && toks[p].at <= i) p++;
      return src.slice(start, i - 1).replace(/\\([{}$&#%_])/g, '$1');
    }

    /**
     * One argument: a brace group, or the next single base WITHOUT its
     * scripts — `x_i^2` is x with a subscript and a superscript, not x with
     * a subscript that has a superscript of its own.
     */
    function arg() {
      const t = peek();
      if (!t) throw new Error('missing argument');
      if (t.t === '{') { next(); const nodes = seq((x) => x.t === '}'); next(); return row(nodes); }
      const b = bare();
      if (!b) throw new Error('missing argument');
      return b;
    }

    /** Atoms until `stop(token)` is true (the stop token is not consumed). */
    function seq(stop) {
      const out = [];
      while (p < toks.length && !stop(peek())) {
        const n = atom();
        if (n) out.push(n);
      }
      return out;
    }

    /** One atom with its scripts. */
    function atom() {
      let base = bare();
      if (!base) return null;
      let sub = null, sup = null;
      // Scripts attach to the nearest base; a base that is a big operator
      // stacks them in display mode, the TeX way.
      while (peek() && (peek().t === '^' || peek().t === '_')) {
        const k = next().t;
        const a = arg();
        if (k === '^') sup = sup ? row([sup, a]) : a; else sub = sub ? row([sub, a]) : a;
      }
      // Primes are superscripts that people type as quotes.
      let primes = '';
      while (peek() && peek().t === 'ch' && peek().v === "'") { next(); primes += '′'; }
      if (primes) { const pm = mo(primes); sup = sup ? row([pm, sup]) : pm; }
      if (!sub && !sup) return base;
      const stacked = display && base.__limits;
      if (sub && sup) { const n = mk(stacked ? 'munderover' : 'msubsup'); n.append(base, sub, sup); return n; }
      if (sub) { const n = mk(stacked ? 'munder' : 'msub'); n.append(base, sub); return n; }
      const n = mk(stacked ? 'mover' : 'msup'); n.append(base, sup); return n;
    }

    /** A base with no scripts: a character, a command, a group. */
    function bare() {
      const t = next();
      if (!t) return null;
      if (t.t === '{') { const nodes = seq((x) => x.t === '}'); next(); return row(nodes); }
      if (t.t === '}') throw new Error('unexpected }');
      if (t.t === '^' || t.t === '_') { p--; return mi(''); }
      if (t.t === '&') return mo('&');
      if (t.t === 'ch') return charNode(t.v);
      return command(t.v);
    }

    function charNode(c) {
      if (/[0-9.]/.test(c)) {
        // A number runs across digits and one decimal point.
        let s = c;
        while (peek() && peek().t === 'ch' && /[0-9.]/.test(peek().v)) s += next().v;
        return mn(s);
      }
      if (/[A-Za-z]/.test(c)) return mi(c);
      if (/[Ѐ-ӿ]/.test(c)) { const n = mi(c); n.setAttribute('mathvariant', 'normal'); return n; }
      if ('+-*/=<>(),;:!|[]'.includes(c)) return mo(c === '-' ? '−' : c === '*' ? '∗' : c);
      return mo(c);
    }

    function command(name) {
      if (name === 'frac' || name === 'dfrac' || name === 'tfrac') {
        const n = mk('mfrac'); n.append(arg(), arg()); return n;
      }
      if (name === 'sqrt') {
        if (peek() && peek().t === 'ch' && peek().v === '[') {
          next();
          const idx = row(seq((x) => x.t === 'ch' && x.v === ']'));
          next();
          const n = mk('mroot'); n.append(arg(), idx); return n;
        }
        const n = mk('msqrt'); n.appendChild(arg()); return n;
      }
      if (name === 'text' || name === 'textrm' || name === 'textit' || name === 'textbf' || name === 'operatorname' || name === 'mbox') {
        const n = mk('mtext', rawGroup());
        if (name === 'textit') n.setAttribute('mathvariant', 'italic');
        if (name === 'textbf') n.setAttribute('mathvariant', 'bold');
        return n;
      }
      if (VARIANT[name]) {
        const inner = arg();
        applyVariant(inner, VARIANT[name]);
        return inner;
      }
      if (name === 'left') {
        const open = fence(next());
        const body = seq((x) => x.t === 'cmd' && x.v === 'right');
        next();
        const close = fence(next());
        const r = mk('mrow');
        const o = mo(open); o.setAttribute('stretchy', 'true'); o.setAttribute('fence', 'true');
        const c = mo(close); c.setAttribute('stretchy', 'true'); c.setAttribute('fence', 'true');
        r.appendChild(o); body.forEach((b) => r.appendChild(b)); r.appendChild(c);
        return r;
      }
      if (name === 'right') throw new Error('\\right without \\left');
      if (name === 'begin') {
        const env = rawGroup();
        if (!(env in ENV)) throw new Error('unknown environment ' + env);
        if (env === 'array') rawGroup(); // the column spec is ignored
        return table(env);
      }
      if (name === 'end') throw new Error('\\end without \\begin');
      if (ACCENT[name]) {
        const n = mk('mover'); const acc = mo(ACCENT[name]); acc.setAttribute('accent', 'true');
        n.append(arg(), acc); return n;
      }
      if (UNDER[name]) {
        const n = mk(name === 'overbrace' ? 'mover' : 'munder');
        n.append(arg(), mo(UNDER[name])); return n;
      }
      if (SPACE[name] !== undefined) { const s = mk('mspace'); s.setAttribute('width', SPACE[name]); return s; }
      if (name === '\\') { const s = mk('mspace'); s.setAttribute('linebreak', 'newline'); return s; }
      if (name === 'displaystyle' || name === 'textstyle' || name === 'limits' || name === 'nolimits' || name === 'scriptstyle') return mk('mrow');
      if (BIG[name]) { const n = mo(BIG[name]); n.__limits = true; n.setAttribute('largeop', 'true'); return n; }
      if (FUNC.has(name)) return mi(name);
      if (LIMFUNC.has(name)) { const n = mi(name); n.__limits = true; return n; }
      if (SYM[name]) return mi(SYM[name]);
      if (OPS[name]) return mo(OPS[name]);
      if (name.length === 1 && !/[A-Za-z]/.test(name)) return mo(name); // \{ \} \% \$ \_ \& \#
      // Unknown: shown as typed, marked, never dropped.
      const n = mi('\\' + name); n.setAttribute('class', 'md-math-unknown'); return n;
    }

    /** The character after \left or \right. */
    function fence(t) {
      if (!t) throw new Error('missing delimiter');
      if (t.t === 'ch') return t.v === '.' ? '' : t.v;
      if (t.t === 'cmd') {
        if (t.v === '{' || t.v === '}' || t.v === '|') return t.v;
        if (OPS[t.v]) return OPS[t.v];
      }
      throw new Error('bad delimiter');
    }

    /** Rows split by \\, cells by &, until \end. */
    function table(env) {
      const tbl = mk('mtable');
      let tr = mk('mtr'), cell = [];
      const flushCell = () => { const td = mk('mtd'); td.appendChild(row(cell)); tr.appendChild(td); cell = []; };
      const flushRow = () => { flushCell(); tbl.appendChild(tr); tr = mk('mtr'); };
      for (;;) {
        const t = peek();
        if (!t) throw new Error('\\end missing');
        if (t.t === 'cmd' && t.v === 'end') { next(); rawGroup(); break; }
        if (t.t === '&') { next(); flushCell(); continue; }
        if (t.t === 'cmd' && t.v === '\\') { next(); flushRow(); continue; }
        const n = atom();
        if (n) cell.push(n);
      }
      if (cell.length || tr.childNodes.length) flushRow();
      if (env === 'cases' || env === 'aligned' || env === 'align' || env === 'align*' || env === 'split') {
        tbl.setAttribute('columnalign', 'left');
      }
      const [open, close] = ENV[env];
      if (!open && !close) return tbl;
      const r = mk('mrow');
      if (open) { const o = mo(open); o.setAttribute('stretchy', 'true'); o.setAttribute('fence', 'true'); r.appendChild(o); }
      r.appendChild(tbl);
      if (close) { const c = mo(close); c.setAttribute('stretchy', 'true'); c.setAttribute('fence', 'true'); r.appendChild(c); }
      return r;
    }

    function applyVariant(node, variant) {
      if (node.nodeName === 'mi' || node.nodeName === 'mn' || node.nodeName === 'mtext') {
        node.setAttribute('mathvariant', variant);
        return;
      }
      Array.from(node.childNodes || []).forEach((c) => applyVariant(c, variant));
    }

    const nodes = seq(() => false);
    const math = mk('math');
    math.setAttribute('xmlns', NS);
    if (display) math.setAttribute('display', 'block');
    const r = mk('mrow');
    nodes.forEach((n) => r.appendChild(n));
    math.appendChild(r);
    return math;
  }

  /**
   * Render TeX into a MathML element, or null when the source has structure
   * this cannot read (the caller shows the source then).
   * @param {string} src @param {boolean} display
   * @returns {Element|null}
   */
  function render(src, display) {
    try {
      return parse(String(src == null ? '' : src).trim(), !!display);
    } catch (_) {
      return null;
    }
  }

  return { render };
})();

if (typeof window !== 'undefined') window.FORMULA = FORMULA;
