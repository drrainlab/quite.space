'use strict';

// Renders the Quiet Chimes voice into WAV files for Android notification
// channels. THE SAME MATHEMATICS the browser plays — chime-dsp.js is
// required directly, so "similar on Android" is impossible: it is the one
// model with two mouths. Re-run after changing chime-dsp.js and commit the
// artifacts; the app never renders at runtime, a notification channel
// wants a file.
//
//   node scripts/sound/render-chimes.cjs
//
// Android res/raw names must be [a-z0-9_].

const fs = require('fs');
const path = require('path');
const DSP = require(path.join(__dirname, '..', '..', 'clients', 'web-ui', 'assets', 'chime-dsp.js'));

const SR = 44100;
const OUT = path.join(__dirname, '..', '..', 'android', 'host', 'app', 'src', 'main', 'res', 'raw');

// tier → file. The channel sounds carry NO modifier: a notification does
// not know who spoke (the modifier is the in-app layer's nuance), and a
// channel sound must be one recognizable identity, not a family of them.
const RENDERS = [
  ['tick', 'chime_message.wav'],
  ['signal', 'chime_signal.wav'],
  ['personal', 'chime_signal_personal.wav'],
];

function wav(pcm, sr) {
  // 16-bit mono PCM WAV. 44-byte header, little-endian.
  const n = pcm.length;
  const buf = Buffer.alloc(44 + n * 2);
  buf.write('RIFF', 0); buf.writeUInt32LE(36 + n * 2, 4); buf.write('WAVE', 8);
  buf.write('fmt ', 12); buf.writeUInt32LE(16, 16);
  buf.writeUInt16LE(1, 20);  // PCM
  buf.writeUInt16LE(1, 22);  // mono
  buf.writeUInt32LE(sr, 24);
  buf.writeUInt32LE(sr * 2, 28); // byte rate
  buf.writeUInt16LE(2, 32);  // block align
  buf.writeUInt16LE(16, 34); // bits
  buf.write('data', 36); buf.writeUInt32LE(n * 2, 40);
  for (let i = 0; i < n; i++) {
    const v = Math.max(-1, Math.min(1, pcm[i]));
    buf.writeInt16LE(Math.round(v * 32767), 44 + i * 2);
  }
  return buf;
}

fs.mkdirSync(OUT, { recursive: true });
for (const [tier, name] of RENDERS) {
  const pcm = DSP.renderChime(tier, null, SR);
  const file = path.join(OUT, name);
  fs.writeFileSync(file, wav(pcm, SR));
  console.log(`${name}: ${(pcm.length / SR * 1000).toFixed(0)} ms, ${(fs.statSync(file).size / 1024).toFixed(1)} KB`);
}
