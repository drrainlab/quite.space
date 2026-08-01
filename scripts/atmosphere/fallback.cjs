'use strict';
// The words a reader gets when the picture cannot be shown.
//
//   node scripts/atmosphere/fallback.cjs
//
// The contract REQUIRES them (ADR-013 invariant 1: the renderer may degrade,
// the meaning may not), so the editor writes a true sentence rather than
// stopping an author at the publish button to invent one. Two properties make
// that safe, and neither is obvious from reading the code:
//
//   the sentence FOLLOWS the recipe — switch a scene to a sequence and the
//   words stop describing a scene, or the honest degradation is a lie;
//
//   the author's own words are NEVER overwritten — the moment somebody types
//   their own sentence it survives every later structural change.
//
// The second one is the one that breaks silently: it is invisible until an
// author loses a sentence they wrote, and by then it is in a signed document.

const fs = require('fs');
const path = require('path');
const vm = require('vm');
const { install } = require('./domstub.cjs');

const ROOT = path.resolve(__dirname, '..', '..');
const ASSETS = path.join(ROOT, 'clients', 'web-ui', 'assets');

let failures = 0;
const verbose = process.argv.includes('--verbose');

function check(name, got, want) {
  const ok = JSON.stringify(got) === JSON.stringify(want);
  if (!ok) {
    failures++;
    console.error(`FAIL  ${name}\n      expected ${JSON.stringify(want)}, got ${JSON.stringify(got)}`);
  } else if (verbose) {
    console.log(`  ok  ${name}`);
  }
  return ok;
}

function assert(name, cond, why) {
  if (!cond) {
    failures++;
    console.error(`FAIL  ${name}\n      ${why}`);
  } else if (verbose) {
    console.log(`  ok  ${name}`);
  }
  return cond;
}

function boot() {
  const ctx = vm.createContext(Object.create(null));
  install(ctx);
  for (const f of ['modes.js', 'brush.js', 'stage.js', 'scenes.js', 'atmosphere.js']) {
    const p = path.join(ASSETS, f);
    vm.runInContext(fs.readFileSync(p, 'utf8'), ctx, { filename: p });
  }
  return {
    edit: vm.runInContext('ATMO_EDIT', ctx),
    doc: vm.runInContext('document', ctx),
  };
}

const { edit, doc: DOC } = boot();

function docWithBlocks() {
  return {
    blocks: [
      { id: 'b1', type: 'text', props: { text: 'the first paragraph' } },
      { id: 'b2', type: 'text', props: { text: 'the second paragraph' } },
    ],
  };
}

function draw(doc) {
  const host = DOC.createElement('div');
  edit.render(host, doc);
  return doc.atmosphere;
}

// A fresh atmosphere is a scene, and the words say so — not "default", which
// would pass the validator and tell a reader nothing at all.
{
  const doc = docWithBlocks();
  doc.atmosphere = edit.blank();
  const a = draw(doc);
  const words = a.fallback.text;
  assert('a new atmosphere arrives with words', !!words, 'the fallback text was left empty');
  assert('the words are not the literal "default"', words !== 'default', `got ${JSON.stringify(words)}`);
  assert('a scene is described as a scene', /background/i.test(words), `got ${JSON.stringify(words)}`);
}

// Switching to a sequence must switch the sentence with it: the whole point
// of the fallback is that a client which cannot draw the thing can still say
// what was there, so words about a moving scene on a post that has three
// photographs are worse than none.
{
  const doc = docWithBlocks();
  doc.atmosphere = edit.blank();
  draw(doc);
  const sceneWords = doc.atmosphere.fallback.text;

  doc.atmosphere.visual.scene = '';
  doc.atmosphere.visual.stages = [
    { anchor: 'b1', image: 'aa' },
    { anchor: 'b2', image: 'bb' },
  ];
  const a = draw(doc);
  assert('the sentence follows the recipe', a.fallback.text !== sceneWords,
    'the words still describe a scene after switching to a sequence');
  check('a sequence counts its pictures', /^2 pictures/.test(a.fallback.text), true);
}

// The author's own sentence outranks ours, forever. This is the one that
// matters: an author writes their words, then adds a picture, and the editor
// must not quietly replace what they said with a generated description.
{
  const doc = docWithBlocks();
  doc.atmosphere = edit.blank();
  draw(doc);
  doc.atmosphere.fallback.text = 'Rain on a window, seen from a warm room.';

  doc.atmosphere.visual.scene = '';
  doc.atmosphere.visual.stages = [{ anchor: 'b1', image: 'aa' }];
  const a = draw(doc);
  check('the author keeps their words through a structural change',
    a.fallback.text, 'Rain on a window, seen from a warm room.');
}

// And clearing the field hands the description back, rather than leaving an
// author with an empty required field and a refusal at the publish button.
{
  const doc = docWithBlocks();
  doc.atmosphere = edit.blank();
  draw(doc);
  doc.atmosphere.fallback.text = 'Mine.';
  draw(doc);
  doc.atmosphere.fallback.text = '';
  const a = draw(doc);
  assert('clearing the field restores a true sentence', !!a.fallback.text,
    'the fallback stayed empty after the author cleared it');
}

if (failures) {
  console.error(`\n${failures} failing`);
  process.exit(1);
}
console.log('atmosphere fallback: ok');
