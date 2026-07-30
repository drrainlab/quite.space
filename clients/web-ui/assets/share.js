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

  return { pick, send, cancel: () => close(null) };
})();

if (typeof window !== 'undefined') window.SHARE = SHARE;
