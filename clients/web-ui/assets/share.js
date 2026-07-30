// @ts-check
// Choosing where a message goes (SHARE-0).
//
// The picker is the NAVIGATOR, in selection mode. Not a list that looks
// like it — the same rows, the same search, the same sections, one flag.
// That is the whole reason the Navigator was built before sharing was: a
// second destination list would have drifted from the first one within a
// week, and a person would have had to learn two arrangements of the same
// places.
//
// This gate CHOOSES and nothing more. It resolves with the targets and the
// comment; carrying a message to them is SHARE-1. Keeping the two apart
// means the honesty work in the sending path — what may be quoted, what
// travels with it — lands in one place instead of being smeared through a
// dialog.

const SHARE = (() => {
  /** @type {ReturnType<typeof NAV.mountPicker>|null} */
  let handle = null;
  /** @type {((v:any)=>void)|null} */
  let settle = null;

  function dlg() { return /** @type {HTMLDialogElement} */(document.getElementById('dlgShare')); }

  /** Does the chosen set include the assistant? */
  function aiPicked() {
    if (!handle) return false;
    const ids = handle.picked();
    return (spacesCache || []).some(s => s.ai && ids.includes(s.id));
  }

  function refreshFoot() {
    if (!handle) return;
    const n = handle.picked().length;
    const send = /** @type {HTMLButtonElement} */(document.getElementById('shareSend'));
    send.disabled = n === 0;
    send.textContent = n ? t('share.send_to', { count: n }) : t('share.send');
    const comment = /** @type {HTMLTextAreaElement} */(document.getElementById('shareComment'));
    // The same optional accompanying message either way — differently
    // invited, because "what should the AI do with this" is the question
    // somebody actually has when the assistant is one of the destinations.
    comment.placeholder = aiPicked() ? t('share.comment.ai') : t('share.comment');
    const who = document.getElementById('sharePicked');
    who.textContent = n
      ? t('share.picked', { names: handle.picked().map(labelOf).join(', ') })
      : '';
  }

  /** @param {string} tid */
  function labelOf(tid) {
    const sp = (spacesCache || []).find(s => s.id === tid);
    return sp ? spaceName(sp) : tid.slice(0, 8);
  }

  /**
   * Open the picker.
   * @param {{source?:string, event?:string}} [about] what is being sent
   * @returns {Promise<{targets:string[], comment:string}|null>}
   */
  function pick(about) {
    close(null);
    const d = dlg();
    const host = /** @type {HTMLElement} */(document.getElementById('sharePicker'));
    handle = NAV.mountPicker(host, refreshFoot);
    NAV.render(spacesCache, current);
    /** @type {HTMLTextAreaElement} */(document.getElementById('shareComment')).value = '';
    document.getElementById('shareAbout').textContent = about && about.source
      ? t('share.about', { space: labelOf(about.source) }) : '';
    refreshFoot();
    d.showModal();
    setTimeout(() => handle && handle.focus(), 0);
    return new Promise((resolve) => { settle = resolve; });
  }

  /** @param {{targets:string[],comment:string}|null} value */
  function close(value) {
    if (handle) { handle.destroy(); handle = null; }
    const d = dlg();
    if (d && d.open) d.close();
    if (settle) { settle(value); settle = null; }
  }

  function send() {
    if (!handle) return;
    const targets = handle.picked();
    if (!targets.length) return;
    const comment = /** @type {HTMLTextAreaElement} */(
      document.getElementById('shareComment')).value.trim();
    close({ targets, comment });
  }

  // ---- what happened afterwards (SHARE-2) ----
  //
  // One row per destination, always. "Sent" as a single word would be a
  // claim about the whole act, and the whole act is exactly what does not
  // have one outcome — partial success is the normal case here, not an
  // error path.
  //
  // The delivery line beneath each row is the ordinary ledger state, and it
  // never says "delivered": a transport can prove at most that a relay
  // ACCEPTED something (ADR-007), and an id the ledger is not tracking
  // renders no line at all rather than an invented "pending".

  /** @type {any[]} */
  let lastResults = [];
  let pollTimer = null;

  /** @param {any[]} results */
  function report(results) {
    lastResults = results || [];
    const d = /** @type {HTMLDialogElement} */(document.getElementById('dlgShareResult'));
    const list = document.getElementById('shareResultList');
    list.innerHTML = '';
    for (const r of lastResults) {
      const row = document.createElement('div');
      row.className = 'share-result' + (r.ok ? '' : ' warn');
      row.dataset.copy = r.copy || '';
      const head = document.createElement('div');
      head.textContent = r.ok
        ? t('share.result.ok', { name: labelOf(r.space) })
        : t('share.result.no', { name: labelOf(r.space), why: r.error || '' });
      row.appendChild(head);
      const line = document.createElement('div');
      line.className = 'share-delivery hint';
      row.appendChild(line);
      list.appendChild(row);
    }
    d.showModal();
    pollDelivery();
    clearInterval(pollTimer);
    // Slow on purpose: this is a state that changes when a transport
    // changes, not a live feed.
    pollTimer = setInterval(pollDelivery, 4000);
  }

  async function pollDelivery() {
    const ids = lastResults.filter(r => r.ok && r.copy).map(r => r.copy);
    if (!ids.length) return;
    let rows = [];
    try {
      rows = (await api('/api/deliveries?ids=' + ids.join(','))).deliveries || [];
    } catch { return; }
    const by = {};
    for (const x of rows) by[x.event] = x;
    document.querySelectorAll('#shareResultList .share-result').forEach(el => {
      const d = by[/** @type {HTMLElement} */(el).dataset.copy || ''];
      const line = el.querySelector('.share-delivery');
      line.textContent = d ? deliveryLine(d) : '';
    });
  }

  /** Turn a ledger state into a sentence that promises exactly what it can. */
  function deliveryLine(d) {
    // Not tracked is not pending. Say nothing rather than something false.
    if (!d.tracked) return '';
    if (d.waiting === 'connectivity') return t('share.wait.connectivity');
    if (d.waiting === 'faster_link') return t('share.wait.faster_link');
    if (d.state === 'in_custody') return t('share.wait.custody');
    if (d.proof === 'accepted_by_relay') return t('share.proof.relay');
    if (d.state === 'settled') return t('share.state.settled');
    if (d.route) return t('share.state.queued', { route: d.route });
    return '';
  }

  function closeReport() {
    clearInterval(pollTimer); pollTimer = null;
    const d = /** @type {HTMLDialogElement} */(document.getElementById('dlgShareResult'));
    if (d && d.open) d.close();
  }

  return { pick, send, cancel: () => close(null), report, closeReport };
})();

if (typeof window !== 'undefined') window.SHARE = SHARE;
