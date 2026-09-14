// The markdown renderer's block grammar, run against a DOM stub: a table
// becomes a table, a lone pipe stays prose, and the reader's threshold is
// where it says it is. Loads the real files; no copies of the rules here.
const fs = require('fs');
const path = require('path');
const assets = path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets');
function el(tag, text) {
  return {
    tag, nodeName: tag, children: [], childNodes: [], style: {}, className: '', attrs: {},
    textContent: text == null ? '' : text,
    appendChild(c) { this.children.push(c); this.childNodes.push(c); return c; },
    append(...cs) { cs.forEach((c) => this.appendChild(c)); },
    setAttribute(k, v) { this.attrs[k] = v; if (k === 'class') this.className = v; },
    removeAttribute(k) { delete this.attrs[k]; },
  };
}
const document = {
  createElement: (t) => el(t),
  createElementNS: (_ns, t) => el(t),
  createTextNode: (s) => ({ tag: '#text', nodeName: '#text', textContent: s, children: [], childNodes: [] }),
  createDocumentFragment: () => el('#frag'),
};
const window = {};
new Function('document', 'window', fs.readFileSync(path.join(assets, 'formula.js'), 'utf8'))(document, window);
new Function('document', 'window', 'FORMULA', fs.readFileSync(path.join(assets, 'markdown.js'), 'utf8'))(document, window, window.FORMULA);
new Function('document', 'window', fs.readFileSync(path.join(assets, 'reader.js'), 'utf8'))(document, window);
const MD = window.MD, READER = window.READER, FORMULA = window.FORMULA;
const text = (n) => n.tag === '#text' ? n.textContent : (n.children.length ? n.children.map(text).join('') : n.textContent);

let failures = 0;
const ok = (w) => console.log('ok   ' + w);
const fail = (w, d) => { failures++; console.log('FAIL ' + w + '\n  ' + d); };

{
  const f = MD.render('intro\n\n| Образец | Что проверяет |\n|---|:--:|\n| Кронштейн | Подготовку |\n| Корпус | Размеры, **поверхность** |\n\nafter');
  const kinds = f.children.map((c) => c.tag + (c.className ? '.' + c.className : ''));
  kinds.join(' ') === 'p div.md-table p'
    ? ok('a pipe table between paragraphs becomes a table block')
    : fail('a pipe table becomes a table block', kinds.join(' '));
  const table = f.children[1].children[0];
  const head = table.children[0].children[0].children.map(text);
  head.join('|') === 'Образец|Что проверяет'
    ? ok('the header row keeps its cells')
    : fail('the header row keeps its cells', head.join('|'));
  const rows = table.children[1].children;
  rows.length === 2 && text(rows[1].children[0]) === 'Корпус'
    ? ok('body rows follow, one per line')
    : fail('body rows follow', String(rows.length));
  const bold = rows[1].children[1].children.find((c) => c.tag === 'strong');
  bold && text(bold) === 'поверхность'
    ? ok('a cell is inline markup')
    : fail('a cell is inline markup', JSON.stringify(rows[1].children[1].children.map(text)));
  table.children[0].children[0].children[1].style.textAlign === 'center'
    ? ok('the separator’s colons align the column')
    : fail('alignment from the separator', JSON.stringify(table.children[0].children[0].children[1].style));
}
{
  const f = MD.render('| just one | pipe line |\nno separator under it');
  f.children.length === 1 && f.children[0].tag === 'p'
    ? ok('a pipe line without a separator stays a paragraph')
    : fail('a pipe line without a separator stays prose', f.children.map((c) => c.tag).join(' '));
}
{
  const f = MD.render('| a | b |\n|---|---|\n| only one cell |\n| one | two | three |');
  const rows = f.children[0].children[0].children[1].children;
  rows[0].children.length === 2 && rows[1].children.length === 2
    ? ok('short rows are padded and long rows are cut to the header')
    : fail('rows follow the header width', rows.map((r) => r.children.length).join(','));
}
{
  const f = MD.render('| a \\| b | c |\n|---|---|\n| x | y |');
  text(f.children[0].children[0].children[0].children[0].children[0]) === 'a | b'
    ? ok('an escaped pipe is a literal pipe inside a cell')
    : fail('escaped pipe', text(f.children[0].children[0].children[0].children[0].children[0]));
}
{
  !READER.isLong('a short message with a\nline break')
    ? ok('a short message is not long')
    : fail('a short message is not long', '');
  READER.isLong('x'.repeat(701))
    ? ok('past seven hundred characters it is long')
    : fail('seven hundred characters', '');
  READER.isLong(Array(16).fill('line').join('\n'))
    ? ok('past fourteen lines it is long')
    : fail('fourteen lines', '');
  !READER.isLong(Array(14).fill('line').join('\n'))
    ? ok('fourteen lines is still a message')
    : fail('fourteen lines is still a message', '');
}
// Formulas (formula.js through markdown.js).
{
  const m = FORMULA.render('C_{\\text{заказа}} = C_{\\text{материала}}', true);
  const row = m.children[0];
  m.attrs.display === 'block' && row.children[0].tag === 'msub' && row.children[0].children[1].tag === 'mtext'
    && text(row.children[0].children[1]) === 'заказа'
    ? ok('a subscript in \\text is an mtext under an msub, displayed')
    : fail('the owner’s formula', JSON.stringify(row.children.map((c) => c.tag)));
}
{
  const m = FORMULA.render('x_i^2', false);
  const n = m.children[0].children[0];
  n.tag === 'msubsup' && text(n.children[1]) === 'i' && text(n.children[2]) === '2'
    ? ok('x_i^2 is one base with a subscript and a superscript')
    : fail('x_i^2', n.tag + ' ' + JSON.stringify(n.children.map(text)));
}
{
  const m = FORMULA.render('\\sum_{i=1}^{n} \\frac{a}{b} \\sqrt[3]{x} \\left( y \\right)', true);
  const kinds = m.children[0].children.map((c) => c.tag);
  kinds.join(' ') === 'munderover mfrac mroot mrow'
    ? ok('big operators stack their limits in display mode; fractions, roots and fences are their own nodes')
    : fail('block constructs', kinds.join(' '));
  const inl = FORMULA.render('\\sum_{i=1}^{n}', false);
  inl.children[0].children[0].tag === 'msubsup'
    ? ok('inline, the same sum keeps its limits at the side')
    : fail('inline limits', inl.children[0].children[0].tag);
}
{
  const f = MD.render('so $E=mc^2$ and $5 and $10 for \\(x_1\\)');
  const kinds = f.children[0].children.map((c) => c.tag === 'math' ? 'MATH' : text(c));
  kinds.join('|') === 'so |MATH| and $5 and $10 for |MATH'
    ? ok('$…$ and \\(…\\) are formulas; prices are prices')
    : fail('inline math vs currency', kinds.join('|'));
}
{
  const g = MD.render('intro\n$$ C_{a} =\n b + c $$\nafter');
  g.children.map((c) => c.tag + (c.className ? '.' + c.className : '')).join(' ') === 'p div.md-math p'
    ? ok('a $$ block may span lines and sits between the paragraphs')
    : fail('displayed block', g.children.map((c) => c.tag).join(' '));
  const u = MD.render('$$ never closed\nline two');
  u.children.length === 1 && u.children[0].tag === 'p'
    ? ok('an unclosed $$ is an ordinary paragraph')
    : fail('unclosed $$', u.children.map((c) => c.tag).join(' '));
}
{
  const un = FORMULA.render('\\foo{x}', false);
  un && un.children[0].children[0].className === 'md-math-unknown' && text(un.children[0].children[0]) === '\\foo'
    ? ok('an unknown command is shown as typed, marked')
    : fail('unknown command', un ? JSON.stringify(un.children[0].children.map(text)) : 'null');
  const bad = MD.render('see $a}$ here');
  const code = bad.children[0].children.find((c) => c.tag === 'code');
  code && code.className === 'md-math-src' && text(code) === '$a}$'
    ? ok('a formula that cannot be read is its own source in a code span')
    : fail('unreadable formula keeps its source', JSON.stringify(bad.children[0].children.map(text)));
}
{
  const m = FORMULA.render('\\begin{pmatrix} a & b \\\\ c & d \\end{pmatrix}', true);
  const r = m.children[0].children[0];
  const tbl = r.children[1];
  r.children[0].tag === 'mo' && tbl.tag === 'mtable' && tbl.children.length === 2 && tbl.children[1].children.length === 2
    ? ok('a pmatrix is a fenced two-by-two table')
    : fail('pmatrix', JSON.stringify(r.children.map((c) => c.tag)));
}
if (failures) { console.log(`${failures} failure(s)`); process.exit(1); }
