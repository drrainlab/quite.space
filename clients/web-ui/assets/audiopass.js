// @ts-check
// Passing a Space Pass through the air, as sound (SP-0).
//
// Two people in a room, no network, no camera, no pairing. One device sings a
// pass; every device listening picks it up. That last part is the reason this
// exists rather than a QR code: sound is a BROADCAST, so one host can admit a
// whole room at once, and it works across a table, in the dark, over a phone
// call, or from a recording.
//
// # Why there is no error-correcting code here
//
// The obvious design reaches for Reed-Solomon. This one does not. The host
// LOOPS the payload forever in numbered, CRC-checked blocks, and the receiver
// keeps whichever blocks arrive intact until it holds the set. A cough, a
// chair, a passing bus costs one block on one pass and it comes round again.
// Robustness by repetition rather than by algebra: far less code, nothing to
// get subtly wrong, and it degrades gracefully instead of cliff-edging.
//
// It also means a listener may start at ANY moment — every block carries its
// own preamble, so there is no "start of transmission" to miss.
//
// # Modulation
//
// 16 tones, 150 Hz apart, 1200–3450 Hz. Four bits per symbol, 40 ms per
// symbol — about 12 bytes a second, so a 368-byte pass is roughly 40 seconds
// per loop. The band sits above room rumble and below the rolloff of cheap
// laptop speakers, and 150 Hz spacing is ~3 FFT bins at our window size, so a
// tone has to drift badly before it is mistaken for its neighbour.
//
// # What this carries is a BEARER CREDENTIAL
//
// Anyone within earshot can join. That is the feature, not a flaw — but sound
// also records, so a pass sung once can be replayed later by anyone who kept
// the audio. Its TTL and use count are what bound that, and the UI must say
// so plainly rather than leaving it to be discovered.

const AP = {
  BASE_HZ: 1200,
  STEP_HZ: 150,
  TONES: 16,          // 4 bits per symbol
  SYMBOL_MS: 40,
  PREAMBLE: [15, 0, 15],  // extremes, so a partial match is unlikely to be noise
  PAYLOAD: 24,        // bytes of pass per block
  SAMPLE_RATE: 48000,
};

/** @param {number} i */
function toneHz(i) { return AP.BASE_HZ + i * AP.STEP_HZ; }

// ---- framing ----

/** CRC-16/CCITT-FALSE. Small, adequate for a 28-byte block. @param {Uint8Array} b */
function crc16(b) {
  let c = 0xffff;
  for (const byte of b) {
    c ^= byte << 8;
    for (let i = 0; i < 8; i++) c = (c & 0x8000) ? ((c << 1) ^ 0x1021) & 0xffff : (c << 1) & 0xffff;
  }
  return c & 0xffff;
}

/**
 * Split a payload into numbered blocks. Each block stands alone: it carries
 * its index, the total, and its own checksum, so a receiver can use it
 * without having heard anything before it.
 * @param {Uint8Array} data
 * @returns {Uint8Array[]}
 */
function apBlocks(data) {
  const total = Math.ceil(data.length / AP.PAYLOAD);
  if (total > 255) throw new Error('payload too large for 8-bit block numbering');
  const out = [];
  for (let i = 0; i < total; i++) {
    const slice = data.subarray(i * AP.PAYLOAD, Math.min((i + 1) * AP.PAYLOAD, data.length));
    // EVERY block is the same length on the air, the last one padded. A
    // receiver has no way to know a block is short until it has decoded the
    // length field that is inside it, so a variable-length block would have
    // to be decoded before it could be measured. Uniform blocks cost a
    // couple of seconds on the final one and remove that circularity
    // entirely; `len` below still records how many bytes are real.
    const body = new Uint8Array(3 + AP.PAYLOAD);
    body[0] = i;
    body[1] = total;
    body[2] = slice.length;
    body.set(slice, 3);
    const c = crc16(body);
    const blk = new Uint8Array(body.length + 2);
    blk.set(body);
    blk[body.length] = c >> 8;
    blk[body.length + 1] = c & 0xff;
    out.push(blk);
  }
  return out;
}

/** Bytes to 4-bit symbols, high nibble first. @param {Uint8Array} b */
function apSymbols(b) {
  const s = new Uint8Array(b.length * 2);
  for (let i = 0; i < b.length; i++) { s[2 * i] = b[i] >> 4; s[2 * i + 1] = b[i] & 0x0f; }
  return s;
}

/** @param {Uint8Array} sym */
function apBytes(sym) {
  const b = new Uint8Array(sym.length >> 1);
  for (let i = 0; i < b.length; i++) b[i] = (sym[2 * i] << 4) | sym[2 * i + 1];
  return b;
}

/**
 * The full symbol stream for one loop: every block preceded by its own
 * preamble, so a listener can lock on at any point.
 * @param {Uint8Array} data
 */
function apEncode(data) {
  /** @type {number[]} */
  const sym = [];
  for (const blk of apBlocks(data)) {
    sym.push(...AP.PREAMBLE);
    sym.push(...apSymbols(blk));
  }
  return Uint8Array.from(sym);
}

// ---- synthesis ----

/**
 * Render symbols to PCM. Each symbol is a windowed tone: the short fade at
 * the edges stops the clicks that a hard switch produces, which otherwise
 * smear energy across every bin and confuse the detector.
 * @param {Uint8Array} sym
 * @param {number} rate
 */
function apRender(sym, rate) {
  const n = Math.round(rate * AP.SYMBOL_MS / 1000);
  const out = new Float32Array(sym.length * n);
  const fade = Math.round(n * 0.08);
  for (let s = 0; s < sym.length; s++) {
    const w = 2 * Math.PI * toneHz(sym[s]) / rate;
    for (let i = 0; i < n; i++) {
      let a = 0.6;
      if (i < fade) a *= i / fade;
      else if (i > n - fade) a *= (n - i) / fade;
      out[s * n + i] = a * Math.sin(w * i);
    }
  }
  return out;
}

// ---- detection ----

/**
 * Goertzel: the energy at one frequency in one window. Cheaper and sharper
 * than a full FFT when the frequencies of interest are known in advance,
 * which here they always are.
 * @param {Float32Array} buf @param {number} off @param {number} len
 * @param {number} hz @param {number} rate
 */
function goertzel(buf, off, len, hz, rate) {
  const k = 2 * Math.cos(2 * Math.PI * hz / rate);
  let s0 = 0, s1 = 0, s2 = 0;
  for (let i = 0; i < len; i++) { s0 = buf[off + i] + k * s1 - s2; s2 = s1; s1 = s0; }
  return s1 * s1 + s2 * s2 - k * s1 * s2;
}

/**
 * The loudest tone in a window, and how far ahead of the runner-up it is.
 * The margin is what separates "a tone" from "noise that happens to peak
 * somewhere" — a symbol with no clear winner is refused rather than guessed.
 * @param {Float32Array} buf @param {number} off @param {number} len @param {number} rate
 */
function apDetect(buf, off, len, rate) {
  let best = -1, bestE = 0, secondE = 0;
  for (let t = 0; t < AP.TONES; t++) {
    const e = goertzel(buf, off, len, toneHz(t), rate);
    if (e > bestE) { secondE = bestE; bestE = e; best = t; }
    else if (e > secondE) secondE = e;
  }
  return { tone: best, energy: bestE, margin: secondE > 0 ? bestE / secondE : Infinity };
}

/**
 * Decode a captured buffer into whatever blocks it contains.
 *
 * Sync is by search rather than by assumption: the preamble is hunted for at
 * every offset, because a microphone stream has no idea where a symbol
 * begins. Blocks that fail their checksum are dropped silently — that is the
 * point of looping, and a corrupted block accepted is far worse than a block
 * missed.
 * @param {Float32Array} buf @param {number} rate
 * @returns {Map<number, {total: number, data: Uint8Array}>}
 */
function apDecode(buf, rate) {
  const n = Math.round(rate * AP.SYMBOL_MS / 1000);
  const found = new Map();
  const hop = Math.max(1, Math.round(n / 4)); // quarter-symbol search grid
  const blockSyms = (3 + AP.PAYLOAD + 2) * 2;

  // <= not <: the final block ends exactly at the buffer edge, and a strict
  // comparison silently drops it. With one block that is the whole message.
  const window = n * (AP.PREAMBLE.length + blockSyms);
  for (let off = 0; off + window <= buf.length; off += hop) {
    // Cheap gate: does the preamble sit here at all?
    let ok = true;
    for (let p = 0; p < AP.PREAMBLE.length && ok; p++) {
      const d = apDetect(buf, off + p * n, n, rate);
      if (d.tone !== AP.PREAMBLE[p] || d.margin < 2.0) ok = false;
    }
    if (!ok) continue;

    const start = off + AP.PREAMBLE.length * n;
    /** @type {number[]} */
    const sym = [];
    let clean = true;
    for (let s = 0; s < blockSyms; s++) {
      const d = apDetect(buf, start + s * n, n, rate);
      if (d.tone < 0 || d.margin < 1.5) { clean = false; break; }
      sym.push(d.tone);
    }
    if (!clean) continue;

    const raw = apBytes(Uint8Array.from(sym));
    const bodyLen = raw.length - 2;
    const want = (raw[bodyLen] << 8) | raw[bodyLen + 1];
    if (crc16(raw.subarray(0, bodyLen)) !== want) continue;

    const idx = raw[0], total = raw[1], len = raw[2];
    if (len > AP.PAYLOAD) continue;
    found.set(idx, { total, data: raw.subarray(3, 3 + len) });
    off = start + blockSyms * n - hop; // skip past what we just consumed
  }
  return found;
}

/**
 * Assemble collected blocks, or report what is still missing. Returns null
 * until every block is present — a partial pass is not a pass.
 * @param {Map<number, {total: number, data: Uint8Array}>} blocks
 */
function apAssemble(blocks) {
  if (blocks.size === 0) return { done: false, have: 0, total: 0, missing: [], bytes: null };
  const total = blocks.values().next().value.total;
  const missing = [];
  for (let i = 0; i < total; i++) if (!blocks.has(i)) missing.push(i);
  if (missing.length) return { done: false, have: blocks.size, total, missing, bytes: null };
  let size = 0;
  for (let i = 0; i < total; i++) size += blocks.get(i).data.length;
  const out = new Uint8Array(size);
  let at = 0;
  for (let i = 0; i < total; i++) { const d = blocks.get(i).data; out.set(d, at); at += d.length; }
  return { done: true, have: total, total, missing: [], bytes: out };
}

/** Seconds one full loop takes, so the UI can be honest about the wait. */
function apLoopSeconds(byteLen) {
  const blocks = Math.ceil(byteLen / AP.PAYLOAD);
  const symsPerBlock = AP.PREAMBLE.length + (3 + AP.PAYLOAD + 2) * 2;
  return blocks * symsPerBlock * AP.SYMBOL_MS / 1000;
}

// Exposed for the browser console and for the loopback self-test.
if (typeof window !== 'undefined') {
  window.AP = AP;
  window.apEncode = apEncode; window.apDecode = apDecode;
  window.apRender = apRender; window.apAssemble = apAssemble;
  window.apBlocks = apBlocks; window.apLoopSeconds = apLoopSeconds;
}

// ---- speaker and microphone ----
//
// The pass travels as RAW BYTES, not as its base64 text: we own both ends of
// this modem, so paying base64's 33% tax would add ten seconds of sound for
// nothing.

/** @param {string} b64u */
function apFromBase64Url(b64u) {
  let s = b64u.replace(/-/g, '+').replace(/_/g, '/');
  while (s.length % 4) s += '=';
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** @param {Uint8Array} b */
function apToBase64Url(b) {
  let s = '';
  for (const x of b) s += String.fromCharCode(x);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/**
 * Sing a pass, on a loop, until stopped.
 *
 * Looping is not a convenience — it IS the error correction. A listener that
 * misses blocks to a cough or a late start collects them next time round.
 * @param {string} passLink
 * @param {(loop: number) => void} [onLoop]
 */
function apPlay(passLink, onLoop) {
  const ctx = new (window.AudioContext || window.webkitAudioContext)();
  const bytes = apFromBase64Url(passLink);
  const pcm = apRender(apEncode(bytes), ctx.sampleRate);
  const buf = ctx.createBuffer(1, pcm.length, ctx.sampleRate);
  buf.copyToChannel(pcm, 0);

  const gain = ctx.createGain();
  gain.gain.value = 0.9;
  gain.connect(ctx.destination);
  const src = ctx.createBufferSource();
  src.buffer = buf;
  src.loop = true;
  src.connect(gain);
  src.start();

  let loops = 0;
  const period = pcm.length / ctx.sampleRate;
  const timer = setInterval(() => { loops++; if (onLoop) onLoop(loops); }, period * 1000);
  return {
    seconds: period,
    stop() { clearInterval(timer); try { src.stop(); } catch (_) {} ctx.close(); },
  };
}

/**
 * Listen for a pass.
 *
 * Decoding runs over a short SLIDING window rather than everything heard so
 * far: any block is self-contained and under 2.5 s long, so a window a few
 * blocks wide always contains whole ones, and the cost per tick stays flat
 * however long someone listens. Blocks accumulate across ticks in a map that
 * outlives the window.
 * @param {(state: {have: number, total: number, pass: string|null, level: number}) => void} onState
 */
async function apListen(onState) {
  const stream = await navigator.mediaDevices.getUserMedia({
    audio: {
      echoCancellation: false,   // all three would fight the tones: AGC pumps
      noiseSuppression: false,   // the level, suppression treats a steady tone
      autoGainControl: false,    // as noise, and AEC subtracts our own speaker
    },
  });
  const ctx = new (window.AudioContext || window.webkitAudioContext)();
  const src = ctx.createMediaStreamSource(stream);
  const node = ctx.createScriptProcessor(4096, 1, 1);

  const windowSec = 8;
  const ring = new Float32Array(Math.round(ctx.sampleRate * windowSec));
  let filled = 0;
  let level = 0;
  /** @type {Map<number, {total: number, data: Uint8Array}>} */
  const blocks = new Map();
  let done = false;

  node.onaudioprocess = (e) => {
    const inBuf = e.inputBuffer.getChannelData(0);
    let peak = 0;
    for (let i = 0; i < inBuf.length; i++) { const a = Math.abs(inBuf[i]); if (a > peak) peak = a; }
    level = peak;
    if (inBuf.length >= ring.length) {
      ring.set(inBuf.subarray(inBuf.length - ring.length));
      filled = ring.length;
    } else {
      ring.copyWithin(0, inBuf.length);
      ring.set(inBuf, ring.length - inBuf.length);
      filled = Math.min(ring.length, filled + inBuf.length);
    }
  };
  src.connect(node);
  // A zero-gain sink: some engines never pull from a ScriptProcessor that is
  // not connected to the destination, and then onaudioprocess never fires.
  const mute = ctx.createGain();
  mute.gain.value = 0;
  node.connect(mute);
  mute.connect(ctx.destination);

  const tick = setInterval(() => {
    if (done || filled < ring.length / 2) { onState({ have: blocks.size, total: 0, pass: null, level }); return; }
    const found = apDecode(ring.subarray(0, filled), ctx.sampleRate);
    for (const [k, v] of found) blocks.set(k, v);
    const res = apAssemble(blocks);
    if (res.done) {
      done = true;
      onState({ have: res.have, total: res.total, pass: apToBase64Url(res.bytes), level });
    } else {
      onState({ have: res.have, total: res.total, pass: null, level });
    }
  }, 1500);

  return {
    stop() {
      clearInterval(tick);
      try { node.disconnect(); src.disconnect(); } catch (_) {}
      stream.getTracks().forEach(t => t.stop());
      ctx.close();
    },
  };
}

if (typeof window !== 'undefined') {
  window.apPlay = apPlay; window.apListen = apListen;
  window.apFromBase64Url = apFromBase64Url; window.apToBase64Url = apToBase64Url;
}

// ---- dialog wiring ----

let AP_TX = null;   // live transmitter
let AP_RX = null;   // live receiver

function apTogglePlay() {
  const btn = document.getElementById('apPlayBtn');
  const msg = document.getElementById('apSendMsg');
  if (AP_TX) { AP_TX.stop(); AP_TX = null; btn.textContent = 'play this pass as sound'; return; }
  const link = (typeof mintedPass !== 'undefined' && mintedPass && mintedPass.link) || '';
  if (!link) { alert('Create the pass first.'); return; }
  try {
    AP_TX = apPlay(link, (n) => {
      msg.textContent = `Playing · pass ${n + 1} times through · `
        + `${Math.round(AP_TX.seconds)}s per pass. Keep playing until they say they have it.`;
    });
    btn.textContent = 'stop';
    msg.textContent = `Playing · ${Math.round(AP_TX.seconds)}s per pass, and it repeats. `
      + 'The other device can start listening at any point.';
  } catch (err) { alert('could not start audio: ' + err.message); }
}

async function apToggleListen() {
  const btn = document.getElementById('apListenBtn');
  const panel = document.getElementById('apRecv');
  const msg = document.getElementById('apRecvMsg');
  if (AP_RX) {
    AP_RX.stop(); AP_RX = null;
    btn.textContent = 'listen for a pass';
    panel.style.display = 'none';
    return;
  }
  try {
    panel.style.display = '';
    btn.textContent = 'stop listening';
    msg.textContent = 'Listening… ask them to play the pass.';
    AP_RX = await apListen((st) => {
      const lvl = document.getElementById('apLevel');
      if (lvl) lvl.style.width = Math.min(100, Math.round(st.level * 300)) + '%';
      const blocks = document.getElementById('apBlocks');
      if (blocks && st.total) {
        // Every block shown, so a person can SEE it filling in rather than
        // watch a bar that might mean nothing.
        let h = '';
        for (let i = 0; i < st.total; i++) h += `<span class="ap-b${i < st.have ? '' : ' off'}"></span>`;
        blocks.innerHTML = h;
      }
      if (st.pass) {
        msg.textContent = 'Got the whole pass. Requesting entry…';
        const ta = document.getElementById('joinPass');
        if (ta) ta.value = st.pass;
        AP_RX.stop(); AP_RX = null;
        btn.textContent = 'listen for a pass';
        if (typeof requestEntry === 'function') requestEntry();
      } else if (st.total) {
        msg.textContent = `Hearing it · ${st.have} of ${st.total} parts. `
          + 'Missing parts arrive as it repeats — keep listening.';
      } else if (st.level < 0.005) {
        msg.textContent = 'Listening… nothing audible yet. Check the volume on the other device.';
      } else {
        msg.textContent = 'Listening… hearing sound, waiting for a pass.';
      }
    });
  } catch (err) {
    panel.style.display = 'none';
    btn.textContent = 'listen for a pass';
    alert('Microphone unavailable: ' + err.message +
      '\n\nThe pass can still be pasted as a link.');
  }
}

if (typeof window !== 'undefined') {
  window.apTogglePlay = apTogglePlay; window.apToggleListen = apToggleListen;
}
