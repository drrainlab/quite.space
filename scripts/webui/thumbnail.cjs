'use strict';
// The inline preview must fit, or not exist.
//
//   node scripts/webui/thumbnail.cjs
//
// THE DEFECT THIS PINS, found by running the desktop shell rather than by
// reading: encodeThumbUnder asked canvas.toBlob for image/webp and assumed it
// got it. WebKit cannot encode webp, and the spec says an engine that cannot
// encode the requested type falls back to image/png — which also IGNORES the
// quality argument. So the whole quality ladder produced the same oversized
// PNG twenty times, and the old last line returned it ANYWAY. The node then
// refused the entire message with "preview too large": on macOS and Linux no
// attachment could be sent at all, while Chrome and Android were fine.
//
// Two properties, and the second is the one that keeps a message going out:
//
//   1. the format is DISCOVERED from what came back, not assumed
//   2. an oversized preview is never returned — no preview is a valid answer
//
// The canvas here is a stub whose toBlob honours quality for jpeg/webp and
// ignores it for png, which is the whole behaviour under test.

const fs = require('fs');
const path = require('path');
const vm = require('vm');

const ROOT = path.resolve(__dirname, '..', '..');
// QP_APP_JS points the check at another copy — used to RED-PROOF it against
// the version that had the defect, which is the only way to know a guard of
// this shape can fail at all.
const APP = process.env.QP_APP_JS ||
  path.join(ROOT, 'clients', 'web-ui', 'assets', 'app.js');

let failures = 0;
const verbose = process.argv.includes('--verbose');

function check(name, ok, detail) {
  if (!ok) {
    failures++;
    console.error(`FAIL  ${name}${detail ? '\n      ' + detail : ''}`);
  } else if (verbose) {
    console.log(`  ok  ${name}`);
  }
}

// ---- lift the function out of app.js ---------------------------------------
const src = fs.readFileSync(APP, 'utf8');
const start = src.indexOf('async function encodeThumbUnder');
if (start < 0) {
  console.error('FAIL  encodeThumbUnder is gone from app.js — this check is stale');
  process.exit(1);
}
// Balance braces from the function's opening one, so the extraction survives
// the body being edited.
let depth = 0, end = -1;
for (let i = src.indexOf('{', start); i < src.length; i++) {
  if (src[i] === '{') depth++;
  else if (src[i] === '}' && --depth === 0) { end = i + 1; break; }
}
const fnSrc = src.slice(start, end);

// ---- a canvas that behaves like a real encoder ------------------------------
//
// bytesFor models the one difference that matters: png ignores quality, and
// the lossy formats do not. Sizes are proportional to area so downscaling
// helps, as it does in a browser.
function makeCanvas(width, height, supported) {
  return {
    width, height,
    getContext: () => ({ drawImage() {} }),
    toBlob(cb, type, q) {
      const real = supported.includes(type) ? type : 'image/png';
      const area = this.width * this.height;
      const size = real === 'image/png'
        ? Math.round(area * 3.0)             // lossless: quality does nothing
        : Math.round(area * 3.0 * (q || 1) * 0.06);
      cb({ type: real, size });
    },
  };
}

const CAP = 36 << 10;

function run(supported) {
  const sandbox = {
    document: {
      createElement: (tag) => {
        if (tag !== 'canvas') throw new Error('unexpected element ' + tag);
        const c = makeCanvas(1, 1, supported);
        return c;
      },
    },
    // The real one reads the blob; the shape is all this needs.
    blobToDataURL: async (b) => `data:${b.type};base64,AA`,
    Promise, Math, console,
  };
  vm.createContext(sandbox);
  vm.runInContext(fnSrc + '\nglobalThis.__fn = encodeThumbUnder;', sandbox);
  return sandbox.__fn(makeCanvas(480, 480, supported), CAP);
}

(async () => {
  // A WebKit-shaped engine: jpeg and png, no webp.
  const webkit = await run(['image/jpeg', 'image/png']);
  check('webkit: a preview is produced at all', !!webkit.blob,
    'no blob came back, so a picture that could have had a thumbnail has none');
  if (webkit.blob) {
    check('webkit: the format is jpeg, not the webp we asked for',
      webkit.blob.type === 'image/jpeg', `got ${webkit.blob.type}`);
    check('webkit: it fits the inline cap',
      webkit.blob.size <= CAP, `${webkit.blob.size} > ${CAP} — the node refuses the whole message`);
  }

  // A Chromium-shaped engine: webp works, and must still be used.
  const chromium = await run(['image/webp', 'image/jpeg', 'image/png']);
  check('chromium: webp is still preferred',
    chromium.blob && chromium.blob.type === 'image/webp',
    `got ${chromium.blob && chromium.blob.type}`);
  check('chromium: it fits the inline cap',
    chromium.blob && chromium.blob.size <= CAP);

  // An engine that can only produce png: nothing can fit, and the honest
  // answer is no preview rather than one the node will refuse.
  const pngOnly = await run(['image/png']);
  check('png-only: no oversized preview is returned',
    !pngOnly.blob || pngOnly.blob.size <= CAP,
    pngOnly.blob && `returned ${pngOnly.blob.size} bytes, over the ${CAP} cap`);
  check('png-only: an absent preview reports an empty dataURL',
    pngOnly.blob || pngOnly.dataURL === '');

  if (failures) {
    console.error(`\n${failures} failure(s)`);
    process.exit(1);
  }
  console.log('thumbnail: ok');
})();
