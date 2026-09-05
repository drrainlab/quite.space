// UI-1 — the library views: Files and Links.
//
// Nothing here is a new kind of event. Both views are the SAME log the
// chat shows, read as a shelf: every file, photo, voice note and track
// anyone shared, every link anyone posted — the two lists a person opens
// in any messenger when they remember "somebody sent that here". They
// ride fetchEntries and its ETag, so a poll that changed nothing costs
// nothing.

const LIB_FILE_KINDS = new Set(['file', 'visual', 'video', 'voice', 'audio']);

function libIsFile(e) { return LIB_FILE_KINDS.has(e.kind) && !!e.asset; }
// A link is a link block — or the first URL inside an ordinary message,
// which is how most links actually arrive. The row keeps the message's
// own words as its title so the person recognises the moment.
const LIB_URL_RE = /https?:\/\/[^\s<>"')\]]+/;
function libLinkOf(e) {
  if (e.kind === 'link' && e.url) return { url: e.url, title: e.title || e.url };
  if (e.kind === 'text' && e.text) {
    const m = e.text.match(LIB_URL_RE);
    if (m) return { url: m[0], title: e.text.replace(m[0], '').trim() || m[0] };
  }
  return null;
}
function libIsLink(e) { return !!libLinkOf(e); }

function libWhen(at) {
  const d = new Date(at * 1000);
  if (isNaN(d.getTime())) return '';
  return d.toLocaleDateString([], { day: '2-digit', month: 'short' }) + ' ' +
    d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

function libBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + ' B';
  if (n < 1048576) return (n / 1024).toFixed(0) + ' KB';
  return (n / 1048576).toFixed(1) + ' MB';
}

function libFileTitle(e) {
  return e.filename || e.title || e.caption || e.alt ||
    (t('spaces.last.' + e.kind) || e.kind);
}

function libFileIcon(e) {
  if (e.thumb_b64 && e.thumb_mime) {
    return `<img alt="" src="data:${esc(e.thumb_mime)};base64,${e.thumb_b64}">`;
  }
  const name = { voice: 'signal', audio: 'studio', visual: 'star', video: 'comet', file: 'book' }[e.kind] || 'book';
  return typeof spaceIconSVG === 'function' ? spaceIconSVG(name, 18) : '';
}

function libHost(url) {
  try { return new URL(url).host.replace(/^www\./, ''); } catch (_) { return url; }
}

function libRow(e, view) {
  const who = e.mine ? t('conv.you') : (e.author_name || '');
  const meta = [who, libWhen(e.created_at)].filter(Boolean);
  if (view === 'files') {
    const a = document.createElement('div');
    a.className = 'lib-row';
    a.setAttribute('role', 'button');
    a.tabIndex = 0;
    if (e.asset && e.asset.size) meta.push(libBytes(e.asset.size));
    // "complete" is the asset store's word for here-and-whole; anything
    // else is a state worth showing (fetching, held, missing).
    const here = !e.asset || !e.asset.state || e.asset.state === 'complete' || e.asset.state === 'ready';
    if (!here) meta.push(e.asset.state);
    a.innerHTML = `<span class="lib-ic">${libFileIcon(e)}</span>` +
      `<span class="lib-main"><span class="lib-t">${esc(libFileTitle(e))}</span>` +
      `<span class="lib-m">${esc(meta.join(' · '))}</span></span>`;
    const open = async () => {
      if (!e.asset) return;
      if (!here) {
        // Not here yet: ask for it the way the chat's card does, then let
        // the next poll draw the truth.
        try { await api(`/api/spaces/${current}/assets/${e.asset.id}/fetch`, { method: 'POST' }); } catch (_) {}
        return;
      }
      window.open(assetURL(e.asset.id), '_blank');
    };
    a.onclick = open;
    a.onkeydown = (ev) => { if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); open(); } };
    return a;
  }
  const ln = libLinkOf(e) || { url: '', title: '' };
  const a = document.createElement('a');
  a.className = 'lib-row';
  a.href = ln.url;
  a.target = '_blank';
  a.rel = 'noopener noreferrer';
  meta.unshift(libHost(ln.url));
  a.innerHTML = `<span class="lib-ic">${e.thumb_b64 && e.thumb_mime
      ? `<img alt="" src="data:${esc(e.thumb_mime)};base64,${e.thumb_b64}">`
      : (typeof spaceIconSVG === 'function' ? spaceIconSVG('compass', 18) : '')}</span>` +
    `<span class="lib-main"><span class="lib-t">${esc(ln.title || ln.url)}</span>` +
    `<span class="lib-m">${esc(meta.join(' · '))}</span></span>`;
  return a;
}

let libSig = { files: '', links: '' };
async function refreshLibrary(view) {
  if (!current) return;
  const host = document.getElementById(view);
  if (!host) return;
  let entries;
  try { entries = await fetchEntries(current); } catch (_) { return; }
  const pick = view === 'files' ? libIsFile : libIsLink;
  const rows = entries.filter(pick).reverse(); // newest first, like an inbox
  const sig = current + '|' + rows.map(e => e.id + (e.asset ? e.asset.state : '')).join(',') + '|' + LOCALE;
  if (sig === libSig[view]) return;
  libSig[view] = sig;
  host.innerHTML = '';
  if (!rows.length) {
    const d = document.createElement('div');
    d.className = 'lib-empty';
    d.textContent = t('library.empty.' + view);
    host.appendChild(d);
    return;
  }
  const list = document.createElement('div');
  list.className = 'lib-list';
  for (const e of rows) list.appendChild(libRow(e, view));
  host.appendChild(list);
}

if (typeof window !== 'undefined') window.refreshLibrary = refreshLibrary;
