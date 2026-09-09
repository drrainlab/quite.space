#!/usr/bin/env node
// Every asset script is a CLASSIC script: they all share one global
// lexical scope, so two files declaring `const SKY` do not shadow each
// other — the second one to load throws at parse time and silently
// never runs. 1.0.2 shipped exactly that (sky.js vs starfield.js): the
// starfield vanished and Settings broke on every device. This harness
// reads index.html's script order and refuses a duplicate top-level
// name across files. Bindings that are meant to be shared go through
// window.* assignments, which this does not count.
const fs = require('fs');
const path = require('path');
const dir = path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets');
const html = fs.readFileSync(path.join(dir, 'index.html'), 'utf8');
const scripts = [...html.matchAll(/<script src="([^"]+)"><\/script>/g)].map(m => m[1]);
const decl = /^(?:const|let|var|class|function|async function)\s+([A-Za-z_$][\w$]*)/;
const owners = new Map();
let bad = 0;
for (const file of scripts) {
  const src = fs.readFileSync(path.join(dir, file), 'utf8');
  for (const line of src.split('\n')) {
    const m = line.match(decl);
    if (!m) continue;
    const name = m[1];
    if (owners.has(name) && owners.get(name) !== file) {
      console.error(`duplicate top-level binding \`${name}\`: ${owners.get(name)} and ${file} — the later script fails to load`);
      bad++;
    } else if (!owners.has(name)) {
      owners.set(name, file);
    }
  }
}
if (bad) process.exit(1);
console.log(`ok: ${owners.size} top-level bindings across ${scripts.length} scripts, no duplicates`);
