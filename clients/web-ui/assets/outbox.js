// @ts-check
'use strict';
/**
 * THE OUTBOX — the send button stops being a promise and becomes a fact.
 *
 * The field report this file answers, verbatim: pressed send, "nothing
 * happens", the text is erased — pressed again — the message arrived
 * TWICE. Both sends had worked; the screen just never said so, and a
 * person whose screen does not answer presses again. That is not user
 * error, it is a law of interfaces.
 *
 * The shape of the fix is a division of labour the node already earned:
 * once a message reaches the node's log it is fsynced, store-and-forward,
 * and honest about delivery — the LOG is the delivery queue, and no
 * JavaScript should try to be one. What JavaScript owns is the one gap
 * the log cannot see: from the finger on the button to the node's 200.
 * This file makes that gap durable:
 *
 *   pressing send WRITES HERE FIRST — localStorage, before any network —
 *     and only then clears the composer. The text is cleared because it
 *     is already safe, not because it was handed to fate;
 *   a pending bubble appears in the same keystroke, so the screen answers
 *     the press instantly, every time, on any network;
 *   a flusher walks the queue in order, one at a time (messages have an
 *     order), with a timeout per attempt and backoff between attempts,
 *     and survives page reloads — the queue is re-read at startup;
 *   every request carries a client_ref the node deduplicates on, which is
 *     the license to retry at all: without idempotency, "retry safely"
 *     is not a thing that exists.
 *
 * A PERMANENT refusal (the node said no: read-only room, bad reply edge)
 * is not retried — the words come back to the person, in a banner with a
 * restore control, because their text vanishing is the one outcome this
 * file exists to make impossible.
 */
const OUTBOX = (() => {
  const KEY = 'qp.outbox';
  /** One attempt's budget. Loopback answers in milliseconds; anything
   * slower than this is a node that is not there, and the retry loop —
   * not a longer wait — is the honest response. */
  const ATTEMPT_MS = 15000;
  /** Backoff between failed rounds: quick first retry, calm afterwards. */
  const BACKOFF_MS = [2000, 4000, 8000, 15000, 30000];

  /** @type {{ref:string,space:string,text:string,reply_to?:string,mentions?:string[],card?:any,bare?:boolean,at:number}[]} */
  let q = [];
  try { q = JSON.parse(localStorage.getItem(KEY) || '[]') || []; } catch { q = []; }

  let flushing = false;
  let failures = 0;
  let timer = null;

  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(q)); } catch { /* full: the queue still lives in memory */ } };

  function newRef() {
    const b = new Uint8Array(12);
    crypto.getRandomValues(b);
    return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
  }

  /**
   * enqueue is the WHOLE of what the send button does now: durable first,
   * visible second, network never. Returns the queued item.
   */
  function enqueue(space, text, opts) {
    const item = {
      ref: newRef(), space, text,
      reply_to: (opts && opts.reply_to) || undefined,
      mentions: (opts && opts.mentions && opts.mentions.length) ? opts.mentions : undefined,
      card: (opts && opts.card) || undefined,
      bare: !!(opts && opts.bare),
      at: Math.floor(Date.now() / 1000),
    };
    q.push(item);
    save();
    paint();
    flush();
    return item;
  }

  /** fetch with a deadline — the outbox's own, never the global api()'s:
   * other callers legitimately wait longer than a loopback POST may. */
  async function post(path, body) {
    const ctl = new AbortController();
    const bomb = setTimeout(() => ctl.abort(), ATTEMPT_MS);
    try {
      return await fetch(path, {
        method: 'POST',
        headers: { 'X-QP-Token': token, 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
        signal: ctl.signal,
      });
    } finally { clearTimeout(bomb); }
  }

  /**
   * One item, delivered or judged. Returns:
   *   'sent'      — gone, remove it;
   *   'retry'     — transient (network, timeout, 5xx, auth-in-flux);
   *   a string    — permanent refusal, in the node's words.
   */
  async function sendOne(item) {
    // A bare link message becomes its card alone — but only when the card
    // exists. The lookup may still be running (or was never started: the
    // page reloaded); give it the settle window once, then send honestly
    // as text. The card is a garnish; the words are the message.
    let card = item.card;
    if (!card && !item.reply_to && typeof LINKS !== 'undefined' && LINKS.enabled()) {
      const url = LINKS.firstURL(item.text);
      if (url && url === item.text.trim()) {
        try {
          const settled = await LINKS.settle(item.text);
          if (settled) { card = settled.card; item.card = card; item.bare = settled.bare; save(); }
        } catch { /* the address is still a message */ }
      }
    }
    const cardOnly = !!card && item.bare && !item.reply_to;
    if (!cardOnly) {
      let r;
      try {
        r = await post(`/api/spaces/${item.space}/messages`, {
          text: item.text, mentions: item.mentions || [],
          ...(item.reply_to ? { reply_to: item.reply_to } : {}),
          client_ref: item.ref,
        });
      } catch { return 'retry'; }
      if (!r.ok) {
        if (r.status >= 500 || r.status === 401) return 'retry';
        let msg = String(r.status);
        try { msg = (await r.json()).error || msg; } catch { /* keep the code */ }
        return msg;
      }
    }
    if (card) {
      // Best-effort, exactly as before: the words are already durable in
      // the log, and a block the space will not take must not read as a
      // failure to send.
      try { await LINKS.send(card); } catch (e) { console.warn('link card', e); }
    }
    return 'sent';
  }

  /** The flusher: strictly in order, one in flight, self-rearming. */
  async function flush() {
    if (flushing) return;
    flushing = true;
    try {
      while (q.length) {
        const item = q[0];
        const verdict = await sendOne(item);
        if (verdict === 'sent') {
          q.shift(); save(); failures = 0; paint();
          if (typeof refreshSpace === 'function' && item.space === current) {
            stickNext();
            await refreshSpace().catch(() => {});
          }
          continue;
        }
        if (verdict === 'retry') {
          const wait = BACKOFF_MS[Math.min(failures, BACKOFF_MS.length - 1)];
          failures++;
          paint();
          clearTimeout(timer);
          timer = setTimeout(flush, wait);
          return; // rearmed; keep order, try the same item again
        }
        // Permanent: the node refused these words. They go back to the
        // person — losing them is the one outcome this file forbids.
        q.shift(); save(); paint();
        refuse(item, verdict);
      }
    } finally { flushing = false; }
  }

  // ---- what the person sees ----------------------------------------------

  /**
   * Pending bubbles live in their OWN strip below the log, not among the
   * entries: the feed reconciler owns #log wholesale (a rebuild does
   * innerHTML=''), and synthetic rows inside it would be torn out by the
   * next poll. The strip renders purely from the queue, so reload paints
   * the same truth the queue holds.
   */
  function paint() {
    const strip = document.getElementById('outboxStrip');
    if (!strip) return;
    const mine = q.filter((i) => i.space === current);
    strip.hidden = mine.length === 0;
    strip.textContent = '';
    for (const item of mine) {
      const row = document.createElement('div');
      row.className = 'entry msg own pending';
      const bubble = document.createElement('div');
      bubble.className = 'bubble';
      bubble.appendChild(document.createTextNode(item.text));
      const mark = document.createElement('span');
      mark.className = 'pending-mark';
      mark.textContent = failures > 0 ? t('outbox.retrying') : t('outbox.sending');
      bubble.appendChild(mark);
      row.appendChild(bubble);
      strip.appendChild(row);
    }
  }

  /** A refusal banner with the person's words and the way back. */
  function refuse(item, why) {
    const note = document.getElementById('sendNote');
    if (!note) { alert(t('outbox.refused', { why })); return; }
    note.hidden = false;
    note.textContent = '';
    const line = document.createElement('span');
    line.textContent = t('outbox.refused', { why }) + ' — «' +
      (item.text.length > 60 ? item.text.slice(0, 60) + '…' : item.text) + '»';
    note.appendChild(line);
    const back = document.createElement('button');
    back.type = 'button';
    back.className = 'btn-plain';
    back.textContent = t('outbox.restore');
    back.onclick = () => {
      const box = document.getElementById('text');
      if (box) {
        box.value = box.value ? box.value + ' ' + item.text : item.text;
        if (typeof growComposer === 'function') growComposer(box);
        box.focus();
      }
      note.hidden = true;
    };
    note.appendChild(back);
  }

  /** One-shot "scroll to the bottom on the next paint, stick or not":
   * your own send always answers with itself on screen. */
  let stickOnce = false;
  function stickNext() { stickOnce = true; }
  function takeStick() { const s = stickOnce; stickOnce = false; return s; }

  // Re-read at startup: whatever a dying page left here still goes.
  if (typeof window !== 'undefined') {
    window.addEventListener('load', () => { paint(); if (q.length) flush(); });
    // The strip follows the person between rooms.
    window.addEventListener('focus', () => { if (q.length) flush(); });
  }

  return { enqueue, flush, paint, pending: () => q.length, _sendOne: sendOne, takeStick };
})();

if (typeof window !== 'undefined') window.OUTBOX = OUTBOX;
if (typeof module !== 'undefined' && module.exports) module.exports = OUTBOX;
