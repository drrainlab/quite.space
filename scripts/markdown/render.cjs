'use strict';
// The post text block's language, and the four things it must never do.
//
//   node scripts/markdown/render.cjs
//
// Half of this file is ordinary rendering — headings, emphasis, lists — and
// half is the part that matters: markdown arrived as a way to make text
// READ better, and the moment a renderer for it starts interpreting HTML,
// following addresses, or fetching from hosts an author names, it has become
// a way to make text DO things. ADR-013 §2 says no executable payload, ever.
// These assertions are that sentence, spelled out for one file.

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { install } = require('../atmosphere/domstub.cjs');

const ROOT = path.resolve(__dirname, '..', '..');
const ASSETS = path.join(ROOT, 'clients', 'web-ui', 'assets');

let failures = 0;
const verbose = process.argv.includes('--verbose');

function assert(name, cond, why) {
  if (!cond) {
    failures++;
    console.error(`FAIL  ${name}\n      ${why}`);
  } else if (verbose) {
    console.log(`  ok  ${name}`);
  }
  return cond;
}

const ctx = vm.createContext(Object.create(null));
const harness = install(ctx);
vm.runInContext(fs.readFileSync(path.join(ASSETS, 'markdown.js'), 'utf8'), ctx,
  { filename: 'markdown.js' });
const MD = vm.runInContext('MD', ctx);

/** The rendered tree as `tag[attr=v]{text}`, so a shape can be asserted. */
function shape(node) {
  if (node.tagName === undefined) return String(node.textContent || '');
  const kids = (node.children || []).map(shape).join('');
  const own = String(node.textContent || '');
  const attrs = Object.keys(node.attrs || {}).sort()
    .map(k => `[${k}=${node.attrs[k]}]`).join('');
  const tag = String(node.tagName).toLowerCase();
  if (tag === '#fragment' || tag === 'div') return kids || own;
  return `${tag}${attrs}{${own}${kids}}`;
}

function draw(src) {
  const host = harness.document.createElement('div');
  MD.into(host, src);
  return host;
}

/** Every element in the tree, flattened. */
function all(node, out = []) {
  for (const c of node.children || []) { out.push(c); all(c, out); }
  return out;
}

const tags = (host) => all(host).map(n => String(n.tagName).toLowerCase());
const text = (host) => {
  let s = String(host.textContent || '');
  for (const c of host.children || []) s += text(c);
  return s;
};

// ---- the ordinary half ----------------------------------------------------

{
  const h = draw('# Title\n\nSome **bold** and *slanted* words.');
  assert('a heading is a heading', tags(h).includes('h2'),
    `got ${JSON.stringify(tags(h))}`);
  assert('and never an h1', !tags(h).includes('h1'),
    'a body heading that outranks the article title can impersonate it');
  assert('bold renders', tags(h).includes('strong'), `got ${JSON.stringify(tags(h))}`);
  assert('emphasis renders', tags(h).includes('em'), `got ${JSON.stringify(tags(h))}`);
}

{
  const h = draw('- one\n- two\n\n1. first\n2. second');
  const t = tags(h);
  assert('bullets make a ul', t.filter(x => x === 'ul').length === 1, JSON.stringify(t));
  assert('numbers make an ol', t.filter(x => x === 'ol').length === 1, JSON.stringify(t));
  assert('four items in all', t.filter(x => x === 'li').length === 4, JSON.stringify(t));
}

{
  const h = draw('> quoted line\n> and another\n\nplain');
  assert('a blockquote', tags(h).includes('blockquote'), JSON.stringify(tags(h)));
}

{
  const h = draw('---\n\nafter');
  assert('a rule', tags(h).includes('hr'), JSON.stringify(tags(h)));
}

{
  const h = draw('```\n**not bold**\n```');
  assert('a fenced block is pre+code', tags(h).includes('pre') && tags(h).includes('code'),
    JSON.stringify(tags(h)));
  assert('and its contents are literal', text(h).includes('**not bold**'),
    `got ${JSON.stringify(text(h))} — a fence that applies markup is not a fence`);
}

{
  const h = draw('`**stars**` stay stars');
  assert('inline code wins over emphasis', !tags(h).includes('strong'),
    'code is where somebody SHOWS you the syntax; applying it there is the one place it must not run');
}

{
  const h = draw('line one\nline two\n\nnew paragraph');
  const t = tags(h);
  assert('two paragraphs', t.filter(x => x === 'p').length === 2, JSON.stringify(t));
  assert('a single newline is a break', t.includes('br'), JSON.stringify(t));
}

// The exact shape from the report: a wall of markdown that had been rendering
// as its own source, mid-sentence.
{
  const h = draw('нас это не только термины. **Проверьте.** --\n\n## Довольно тихое место\n\nСначала проект назывался Quiet Spaces.');
  const t = tags(h);
  // `##` lands on h3, not h2 — every body heading is shifted one below the
  // article's own title, so the second level of a document is the third of
  // the page. That is the rule, not an off-by-one.
  assert('the reported wall breaks into parts',
    t.includes('h3') && t.includes('strong') && t.filter(x => x === 'p').length >= 2,
    JSON.stringify(t));
}

// ---- the half that matters ------------------------------------------------

{
  const h = draw('<script>alert(1)</script> and <b>bold?</b>');
  assert('no html is ever parsed', !tags(h).includes('script') && !tags(h).includes('b'),
    `built ${JSON.stringify(tags(h))} — this renderer builds a tree and must never take markup from a document`);
  assert('and the brackets survive as characters', text(h).includes('<script>'),
    `got ${JSON.stringify(text(h))}`);
}

{
  const h = draw('![a picture](https://example.com/track.gif?who=me)');
  assert('an inline image is never fetched', !tags(h).includes('img'),
    'an <img> in text is a beacon: it reports who opened the post, and when, to whoever wrote it');
  assert('but the author\'s words and the address survive',
    text(h).includes('a picture') && text(h).includes('example.com'),
    `got ${JSON.stringify(text(h))}`);
}

{
  const h = draw('[click](javascript:alert(1)) and [data](data:text/html,<b>) and [ok](https://quite.space)');
  const anchors = all(h).filter(n => String(n.tagName).toLowerCase() === 'a');
  assert('exactly one link survives', anchors.length === 1,
    `built ${anchors.length} anchors — a scheme this app would not otherwise follow must stay text`);
  assert('and it is the safe one', anchors[0] && anchors[0].getAttribute('href') === 'https://quite.space',
    `href was ${anchors[0] && anchors[0].getAttribute('href')}`);
  assert('carrying no referrer and no opener',
    /noopener/.test(anchors[0].getAttribute('rel') || '') &&
    /noreferrer/.test(anchors[0].getAttribute('rel') || ''),
    `rel was ${anchors[0] && anchors[0].getAttribute('rel')}`);
  assert('the refused ones remain readable', text(h).includes('javascript:'),
    'a refused link must still show what it claimed to be');
}

{
  // Nothing here may be interpreted twice: a link's TEXT is inline markup,
  // and its target is not.
  const h = draw('[**bold label**](https://quite.space/a)');
  const a = all(h).find(n => String(n.tagName).toLowerCase() === 'a');
  assert('a link keeps its own address', a && a.getAttribute('href') === 'https://quite.space/a',
    `href was ${a && a.getAttribute('href')}`);
}

{
  const h = draw('');
  assert('empty source renders nothing and does not throw', tags(h).length === 0,
    JSON.stringify(tags(h)));
}

if (failures) {
  console.error(`\n${failures} failing`);
  process.exit(1);
}
console.log('markdown: ok');
