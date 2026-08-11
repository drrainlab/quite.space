// The catalogs must not drift.
//
// A missing translation is invisible in the running app: t() falls back to
// English and the screen keeps working, so nobody notices until somebody
// reading Russian meets an English sentence in the middle of a paragraph.
// This check makes that loud instead — run it the way the other stubs are
// run, with `node scripts/i18n/catalogs.cjs`.
//
// It asserts four things:
//   1. every EN key exists in RU, and vice versa
//   2. an entry has the same SHAPE in both (a function stays a function —
//      otherwise a call site that passes vars gets a bare string back)
//   3. every function entry actually runs and returns a string
//   4. a function entry consumes the same variables in both locales, so a
//      translation cannot silently drop an interpolated name or count

const vm = require('vm');
const fs = require('fs');
const path = require('path');

const FILE = path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets', 'i18n.js');

const sandbox = {
  navigator: { language: 'en' },
  localStorage: { getItem: () => null, setItem: () => {} },
  document: { documentElement: {}, addEventListener: () => {}, querySelectorAll: () => [] },
  Intl, console,
};
vm.createContext(sandbox);
vm.runInContext(fs.readFileSync(FILE, 'utf8') + ';globalThis.__C = I18N;', sandbox);
const I18N = sandbox.__C;

let failures = 0;
function check(what, ok, detail) {
  if (ok) return;
  failures++;
  console.error('FAIL: ' + what + (detail ? ' — ' + detail : ''));
}

const locales = Object.keys(I18N);
check('there is more than one locale', locales.length > 1, locales.join(', '));

const en = Object.keys(I18N.en);
for (const loc of locales) {
  if (loc === 'en') continue;
  const keys = Object.keys(I18N[loc]);
  const missing = en.filter(k => !(k in I18N[loc]));
  const extra = keys.filter(k => !(k in I18N.en));
  check(`${loc} translates every EN key`, missing.length === 0,
    missing.length ? `${missing.length} missing: ${missing.slice(0, 8).join(', ')}` : '');
  check(`${loc} invents no key of its own`, extra.length === 0, extra.join(', '));
}

// A generous bag of vars: every interpolated name used anywhere in the
// catalog. A function that reaches for something absent still returns a
// string (undefined interpolates), so shape is checked separately below.
const ARGS = {
  count: 2, n: 2, who: 'who', space: 'space', from: 'from', names: ['a', 'b'],
  more: 2, title: 'title', sender: 'sender', author: 'author', name: 'name',
  why: 'why', route: 'route', uses: 2, hours: 1, got: 1, total: 3, kind: 'kind',
  provider: 'provider', model: 'model', mode: 'mode', size: '1 KB', fits: '2 KB',
  state: 'state', node: 1, carrier: 'carrier', done: 1, all: 2, ago: 'now',
  owner: 'owner',
};

// Which vars a function entry actually reads, read off its source. Crude on
// purpose: it only has to catch a translation that forgot ${who}.
function varsUsed(fn) {
  const src = String(fn);
  const used = new Set();
  for (const k of Object.keys(ARGS)) {
    if (new RegExp('\\$\\{[^}]*\\b' + k + '\\b').test(src)) used.add(k);
  }
  return used;
}

for (const key of en) {
  const ref = I18N.en[key];
  for (const loc of locales) {
    const val = I18N[loc][key];
    if (val === undefined) continue; // already reported above
    check(`${loc}:${key} has the same shape as EN`, typeof val === typeof ref,
      `EN is ${typeof ref}, ${loc} is ${typeof val}`);
    if (typeof val !== 'function' || typeof ref !== 'function') continue;
    let out;
    try {
      out = val(ARGS);
    } catch (e) {
      check(`${loc}:${key} runs`, false, e.message);
      continue;
    }
    check(`${loc}:${key} returns a string`, typeof out === 'string', typeof out);
    const want = varsUsed(ref), got = varsUsed(val);
    const dropped = [...want].filter(v => !got.has(v));
    check(`${loc}:${key} keeps every interpolated variable`, dropped.length === 0,
      dropped.length ? 'dropped ' + dropped.join(', ') : '');
  }
}

if (failures) {
  console.error(`\ni18n catalogs: ${failures} problem(s)`);
  process.exit(1);
}
console.log(`i18n catalogs: all checks passed (${locales.join(' + ')}, ${en.length} keys each)`);
