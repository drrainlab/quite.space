// @ts-check
'use strict';
/**
 * LINK CARDS — the preview is made by the sender, and only by the sender.
 *
 * The whole feature turns on one question that every other messenger
 * answers badly: who fetches the picture? Telegram's servers do it, so
 * Telegram sees every link anybody sends. A client that fetched on RECEIPT
 * would be worse still — opening a room would report this device, its
 * address and the minute it read, to whoever wrote the link. That is a
 * read receipt nobody agreed to, and a way to find somebody by sending
 * them a link to a server you own.
 *
 * So: the sender's own node fetches once, at compose time, and what
 * travels is ordinary sealed content in the log (`block.link.v1`, whose
 * own comment has said "the preview is attached by the SENDER" since
 * before anything attached one). Reading a card is reading bytes that are
 * already yours. THIS FILE NEVER ISSUES A REQUEST TO A THIRD PARTY, and
 * the interface's Content-Security-Policy — img-src 'self' data: blob: —
 * is what makes that a fact rather than a promise.
 *
 * Two things follow from the same rule and are easy to mistake for
 * oversights:
 *
 *   · nothing is unfurled for messages that ARRIVE. A URL somebody sent
 *     you before this existed stays a plain link forever. Going back to
 *     make cards for it would be exactly the beacon this avoids.
 *   · what a card claims is the SITE's claim about itself, taken by one
 *     device at one moment. It has the standing of a quotation, and it is
 *     drawn like one.
 *
 * The play control is decided HERE, from the address, by the reader —
 * never from a flag in the event. A sender does not get to declare that
 * their link deserves a play button.
 *
 * PRESSING IT IS THE ONE OUTBOUND MOMENT, and it plays in place, inside
 * the conversation. What gets mounted is our own /player, which frames
 * the embed one level further down: this document never frames
 * youtube.com, so its policy permits nothing but 'self' and a third
 * party's script never stands beside the session token (node/player.go
 * carries the argument in full).
 */
const LINKS = (() => {
  /** Device-local, like every other switch about what this screen does. */
  const KEY = 'qp.linkpreview';
  /** Remembered once: watching means talking to YouTube, and it says so. */
  const PLAY_OK = 'qp.link.play';

  /** The card staged in the composer, and the address it was made from. */
  let staged = null;
  let stagedFor = '';
  /** What we last asked the node about, so one paste asks once. */
  let asked = '';
  let timer = null;
  /** The lookup in flight, so a send can wait for the one already running. */
  let inflight = null;

  /** How long typing must stop before the node goes out to the network. */
  const TYPING_PAUSE = 260;
  /** The longest a send will wait for a card before going without one. */
  const SETTLE_CAP = 2500;

  const enabled = () => localStorage.getItem(KEY) !== 'off';

  function setEnabled(on) {
    localStorage.setItem(KEY, on ? 'on' : 'off');
    if (!on) drop();
    syncUI();
  }

  function syncUI() {
    if (typeof pickSeg === 'function') pickSeg('setLinkPreview', enabled() ? 'on' : 'off');
  }

  // ---- recognising an address --------------------------------------------

  /** The same shapes the node knows, so both ends agree on what a video is. */
  function youtubeID(raw) {
    let u;
    try { u = new URL(raw); } catch { return ''; }
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
    const h = u.hostname.toLowerCase().replace(/^www\./, '');
    const path = u.pathname.replace(/^\/+|\/+$/g, '');
    const ok = (s) => (/^[A-Za-z0-9_-]{6,24}$/.test(s || '') ? s : '');
    if (h === 'youtu.be') return ok(path.split('/')[0]);
    if (['youtube.com', 'm.youtube.com', 'music.youtube.com', 'youtube-nocookie.com'].includes(h)) {
      if (path === 'watch') return ok(u.searchParams.get('v') || '');
      for (const p of ['shorts/', 'embed/', 'live/', 'v/']) {
        if (path.startsWith(p)) return ok(path.slice(p.length).split('/')[0]);
      }
    }
    return '';
  }

  /**
   * A SoundCloud track or set, as the path its own widget resolves:
   * "artist/track" or "artist/sets/name". Short on.soundcloud.com links
   * are NOT recognised — they redirect, and following a redirect to find
   * out what something is would be a fetch this file promised never to
   * make.
   */
  function soundcloudPath(raw) {
    let u;
    try { u = new URL(raw); } catch { return ''; }
    if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
    const h = u.hostname.toLowerCase().replace(/^(www\.|m\.)/, '');
    if (h !== 'soundcloud.com') return '';
    const segs = u.pathname.replace(/^\/+|\/+$/g, '').split('/');
    if (segs.length < 2 || segs.length > 3) return '';
    for (const seg of segs) {
      if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(seg)) return '';
    }
    const path = segs.join('/');
    return path.length <= 200 ? path : '';
  }

  /**
   * What an address can PLAY as, or null. One place, so the card, the
   * chip and the mount all agree on what deserves the control — and on
   * which site's name the first-time question wears.
   */
  function playTarget(raw) {
    const v = youtubeID(raw);
    if (v) return { q: 'v=' + encodeURIComponent(v), site: 'YouTube', timed: true };
    const sc = soundcloudPath(raw);
    // audio says the mounted frame is a STRIP, not a 16:9 stage: the
    // classic SoundCloud widget is 166px of waveform, and stretching it
    // into a video's shape leaves a field of its background around it.
    if (sc) return { q: 'sc=' + encodeURIComponent(sc), site: 'SoundCloud', timed: false, audio: true };
    return null;
  }

  /** Seconds into a video, when the address carries a timestamp. */
  function startAt(raw) {
    let u;
    try { u = new URL(raw); } catch { return ''; }
    const t = u.searchParams.get('t') || u.searchParams.get('start') || '';
    const m = /^(?:(\d+)h)?(?:(\d+)m)?(\d+)s?$/.exec(t);
    if (m) return String((+(m[1] || 0)) * 3600 + (+(m[2] || 0)) * 60 + (+m[3]));
    return /^\d{1,7}$/.test(t) ? t : '';
  }

  const URL_RE = /https?:\/\/[^\s<>"'`]+/;
  /** Trailing punctuation belongs to the sentence — markdown.js's rule. */
  const TAIL = /[.,;:!?»)\]}'"]+$/;

  function firstURL(text) {
    const m = URL_RE.exec(String(text || ''));
    return m ? m[0].replace(TAIL, '') : '';
  }

  // ---- the composer ------------------------------------------------------

  /**
   * Typing is not asking. The node goes out to the network here, so it
   * waits for the typing to stop, asks about ONE address, and asks about
   * it once — an edited line does not re-fetch the same page.
   */
  function onInput(el) {
    if (!enabled()) return;
    const url = firstURL(el && el.value);
    if (!url) { drop(); asked = ''; return; }
    if (url === stagedFor || url === asked) return;
    clearTimeout(timer);
    timer = setTimeout(() => look(url), TYPING_PAUSE);
  }

  /**
   * look never rejects and never leaves `inflight` set: settle() waits on
   * it at send time, and a send that hangs on a page that will not answer
   * would be a worse bug than a missing card.
   */
  function look(url, opts) {
    asked = url;
    const keep = !!(opts && opts.keep);
    const stage = document.getElementById('linkStage');
    if (stage) {
      stage.hidden = false;
      stage.className = 'link-stage waiting';
      stage.textContent = t('link.looking');
    }
    inflight = (async () => {
      try {
        const card = await api('/api/unfurl', { method: 'POST', body: JSON.stringify({ url }) });
        // The box may have moved on while the page was answering — unless
        // this is the send itself, which has the text it is sending and is
        // about to empty the box anyway.
        const box = document.getElementById('text');
        if (!keep && firstURL(box && box.value) !== url) { drop(); return; }
        staged = card; stagedFor = url;
        if (stage) draw(stage);
      } catch {
        // A site that will not answer is not an error worth a dialog: the
        // address itself was always readable, and that is what will be
        // sent.
        drop();
      } finally {
        inflight = null;
      }
    })();
    return inflight;
  }

  /**
   * SEND WAITS FOR THE CARD IT IS ABOUT TO NEED.
   *
   * The obvious flow — paste, press send — is faster than a round trip to
   * a stranger's server, so the first build of this shipped a feature that
   * only worked for people who hesitated. Nobody hesitates.
   *
   * So a send with an address in it settles first: it waits for a lookup
   * already running, or starts one, and gives up after SETTLE_CAP. The cap
   * is what keeps this honest — a slow page delays a message by at most
   * that, and then the message goes as the plain address it always was.
   */
  async function settle(text) {
    if (!enabled()) return null;
    const url = firstURL(text);
    if (!url) return null;
    if (stagedFor !== url) {
      clearTimeout(timer);
      const work = inflight && asked === url ? inflight : look(url, { keep: true });
      await Promise.race([work, new Promise((r) => setTimeout(r, SETTLE_CAP))]);
    }
    return pending(text);
  }

  function draw(stage) {
    stage.hidden = false;
    stage.className = 'link-stage';
    stage.textContent = '';
    const card = build(staged, { compact: true });
    stage.appendChild(card);
    const x = document.createElement('button');
    x.type = 'button';
    x.className = 'link-drop';
    x.setAttribute('aria-label', t('link.drop'));
    x.title = t('link.drop');
    x.textContent = '✕';
    x.onclick = () => { drop(); asked = stagedFor || asked; };
    stage.appendChild(x);
  }

  /** Forget the staged card. The message is unaffected — it is still text. */
  function drop() {
    staged = null; stagedFor = '';
    clearTimeout(timer);
    const stage = document.getElementById('linkStage');
    if (stage) { stage.hidden = true; stage.textContent = ''; }
  }

  /** Called after a send, whatever happened to it. */
  function reset() { drop(); asked = ''; }

  /**
   * What say() needs to know: is there a card, and is the message NOTHING
   * BUT its address?
   *
   * When it is (the common case — somebody pastes a link and presses send)
   * the card replaces the message rather than following it, so one paste
   * makes one entry. When there are words too, the words are the message
   * and the card comes after it.
   */
  function pending(text) {
    if (!staged || !stagedFor) return null;
    const bare = String(text || '').trim() === stagedFor;
    return { card: staged, bare };
  }

  /** base64 → Blob, so the thumbnail rides the existing multipart seam. */
  function thumbBlob(card) {
    if (!card.thumb_b64) return null;
    const bin = atob(card.thumb_b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return new Blob([bytes], { type: card.thumb_mime || 'image/jpeg' });
  }

  /** Emit the staged card as its own signed block. */
  async function send(card) {
    await postBlock({
      kind: 'link', url: card.url, title: card.title,
      description: card.description,
      preview_mime: card.thumb_mime || '',
    }, thumbBlob(card), null);
  }

  // ---- the card ----------------------------------------------------------

  /**
   * One card, drawn the same way in the composer and in the feed.
   * `e` is an entry from /api/entries or a fresh card from /api/unfurl —
   * the field names are the same on purpose.
   */
  function build(e, opts) {
    const compact = !!(opts && opts.compact);
    const url = e.url || '';
    const target = playTarget(url);
    const card = document.createElement('div');
    card.className = 'linkcard' + (compact ? ' compact' : '');

    if (e.thumb_b64) {
      const holder = document.createElement('div');
      holder.className = 'link-shot';
      const img = document.createElement('img');
      // A data: URI, from bytes that arrived inside the event. No request
      // leaves this page to draw it — that is the whole design.
      img.src = `data:${e.thumb_mime || 'image/jpeg'};base64,${e.thumb_b64}`;
      img.alt = '';
      img.loading = 'lazy';
      holder.appendChild(img);
      if (target) {
        const play = document.createElement('button');
        play.type = 'button';
        play.className = 'link-play';
        play.setAttribute('aria-label', t('link.play'));
        play.textContent = '▶';
        play.onclick = (ev) => { ev.preventDefault(); ev.stopPropagation(); watch(url, target, card, holder); };
        holder.appendChild(play);
      }
      card.appendChild(holder);
    }

    const body = document.createElement('div');
    body.className = 'link-body';
    if (e.site) {
      const s = document.createElement('div');
      s.className = 'link-site';
      s.textContent = e.site;
      body.appendChild(s);
    } else if (!compact) {
      const s = document.createElement('div');
      s.className = 'link-site';
      try { s.textContent = new URL(url).hostname.replace(/^www\./, ''); } catch { s.textContent = ''; }
      body.appendChild(s);
    }
    // The title is the ADDRESS's anchor, and it goes through the same
    // scheme check every other link in this client does: a refused address
    // stays readable and stops being clickable.
    const href = typeof MD !== 'undefined' ? MD.safeHref(url) : '';
    const a = document.createElement(href ? 'a' : 'span');
    a.className = 'link-title';
    if (href) {
      a.setAttribute('href', href);
      a.setAttribute('target', '_blank');
      a.setAttribute('rel', 'noopener noreferrer nofollow');
    }
    a.textContent = e.title || url;
    body.appendChild(a);
    if (e.description) {
      const d = document.createElement('div');
      d.className = 'link-descr';
      d.textContent = e.description;
      body.appendChild(d);
    }
    // A playable address whose card has NO picture still deserves the
    // verb — the control lived on the poster, so a posterless card was
    // silently inert. The chip mounts the same frame at the top of the
    // card.
    if (target && !e.thumb_b64 && !compact) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'link-watch';
      b.textContent = '▶ ' + t('link.play');
      b.onclick = (ev) => { ev.preventDefault(); ev.stopPropagation(); watch(url, target, card, null); };
      body.appendChild(b);
    }
    card.appendChild(body);
    return card;
  }

  // ---- watching ----------------------------------------------------------

  /**
   * Play, and say once what playing costs.
   *
   * Everything above this line is local: the card was drawn from bytes
   * that arrived with the message. Pressing play is the one moment
   * somebody deliberately reaches out to YouTube, so the first time it is
   * asked rather than assumed — once, and remembered, because a question
   * re-asked forever is a question nobody reads.
   *
   * @param {string} url  the address, read for its timestamp
   * @param {{q:string,site:string,timed:boolean}} target  what playTarget recognised
   * @param {Element} host  where the frame will be mounted
   * @param {Element} [replace]  the element the frame takes the place of
   */
  function watch(url, target, host, replace) {
    if (localStorage.getItem(PLAY_OK) === 'yes') { mount(target, url, host, replace); return; }
    if (host.querySelector('.link-ask')) return;
    const ask = document.createElement('div');
    ask.className = 'link-ask';
    const line = document.createElement('span');
    line.textContent = t('link.playAsk', { site: target.site });
    ask.appendChild(line);
    const no = document.createElement('button');
    no.type = 'button'; no.className = 'btn-plain';
    no.textContent = t('link.playNo');
    no.onclick = () => ask.remove();
    const yes = document.createElement('button');
    yes.type = 'button'; yes.className = 'btn-tinted';
    yes.textContent = t('link.playYes');
    yes.onclick = () => {
      localStorage.setItem(PLAY_OK, 'yes');
      ask.remove();
      mount(target, url, host, replace);
    };
    ask.append(no, yes);
    host.appendChild(ask);
  }

  /**
   * Mount the player where the poster was.
   *
   * The frame is OUR OWN /player, not youtube.com — that page frames the
   * embed under a policy of its own, in a document carrying no token and
   * no script of ours. One frame of distance, and it is the reason the
   * interface's own policy needs to permit nothing but 'self'
   * (node/player.go says the rest).
   *
   * The permissions are handed down a level at a time. Miss one here and
   * the embed loses it silently — autoplay first, which is the difference
   * between a video that starts and a black rectangle.
   */
  function mount(target, url, host, replace) {
    const t0 = target.timed ? startAt(url) : '';
    const frame = document.createElement('iframe');
    frame.className = 'link-player' + (target.audio ? ' audio' : '');
    // Relative here, absolute in a shell that has no http origin of its
    // own: the desktop serves this interface over wails://, and an embed
    // refuses a page whose identity cannot travel in a Referer header. The
    // node says where its player listens; empty means "right here".
    const origin = (typeof status === 'object' && status && status.player_origin) || '';
    frame.src = origin + '/player?' + target.q +
      (t0 ? '&t=' + encodeURIComponent(t0) : '');
    frame.setAttribute('allow', 'autoplay; encrypted-media; picture-in-picture; fullscreen');
    frame.setAttribute('allowfullscreen', '');
    frame.title = t('link.play');
    if (replace && replace.parentNode) replace.replaceWith(frame);
    else host.insertBefore(frame, host.firstChild);
  }

  /**
   * A message that is just a video address gets a play control, and
   * NOTHING else.
   *
   * No picture is fetched for it, then or ever — a card belongs to the
   * person who sent it, and this message was sent by somebody else,
   * possibly years before any of this existed. What the reader's client
   * may honestly add is not a claim about the page but the ability to ACT
   * on an address already in their hands. It costs no request until the
   * control is pressed, and pressing it asks first, exactly as a card
   * does.
   */
  function decorate(node) {
    if (!node || !node.querySelectorAll) return node;
    for (const a of node.querySelectorAll('a[href]')) {
      const href = a.getAttribute('href') || '';
      const target = playTarget(href);
      if (!target) continue;
      const next = a.nextElementSibling;
      if (next && next.classList && next.classList.contains('link-watch')) continue;
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'link-watch';
      b.textContent = '▶ ' + t('link.play');
      b.onclick = (ev) => { ev.preventDefault(); ev.stopPropagation(); watch(href, target, node, b); };
      a.after(b);
    }
    return node;
  }

  return { enabled, setEnabled, syncUI, onInput, pending, settle, send, reset, drop,
    build, decorate, youtubeID, soundcloudPath, playTarget, firstURL, _startAt: startAt };
})();

if (typeof window !== 'undefined') window.LINKS = LINKS;
if (typeof module !== 'undefined' && module.exports) module.exports = LINKS;
