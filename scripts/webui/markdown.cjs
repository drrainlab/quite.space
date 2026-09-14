// The markdown renderer's block grammar, run against a DOM stub: a table
// becomes a table, a lone pipe stays prose, and the reader's threshold is
// where it says it is. Loads the real files; no copies of the rules here.
const fs = require('fs');
const path = require('path');
const assets = path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets');
function el(tag, text) {
  return {
    tag, children: [], style: {}, className: '', attrs: {},
    textContent: text == null ? '' : text,
    appendChild(c) { this.children.push(c); return c; },
    append(...cs) { this.children.push(...cs); },
    setAttribute(k, v) { this.attrs[k] = v; },
  };
}
const document = {
  createElement: (t) => el(t),
  createTextNode: (s) => ({ tag: '#text', textContent: s, children: [] }),
  createDocumentFragment: () => el('#frag'),
};
const window = {};
new Function('document', 'window', fs.readFileSync(path.join(assets, 'markdown.js'), 'utf8'))(document, window);
new Function('document', 'window', fs.readFileSync(path.join(assets, 'reader.js'), 'utf8'))(document, window);
const MD = window.MD, READER = window.READER;
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
if (failures) { console.log(`${failures} failure(s)`); process.exit(1); }
