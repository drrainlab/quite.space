// @ts-check
'use strict';
/**
 * READER — a long message opens as a page, not as a wall in the chat.
 *
 * People paste whole documents into a conversation: an assistant's answer,
 * a README, a comparison with a table in it. Rendered in full, one such
 * message is the screen for as long as it is on it, and the conversation
 * around it disappears. So a message past a modest size keeps a PREVIEW in
 * its bubble — the top of the rendered text, fading out — and one control
 * that opens the whole of it in a reading container with the article's
 * typography. Nothing about the wire changes; the bubble holds the same
 * text it always did, and a client without this file shows all of it.
 *
 * The threshold is deliberately generous. A long-ish message that fits on
 * a phone screen is still a message; this exists for the ones that do not.
 */
const READER = (() => {
  const LONG_CHARS = 700;
  const LONG_LINES = 14;

  /** @param {string} text */
  function isLong(text) {
    const s = String(text == null ? '' : text);
    if (s.length > LONG_CHARS) return true;
    let lines = 1;
    for (let i = 0; i < s.length; i++) if (s.charCodeAt(i) === 10) lines++;
    return lines > LONG_LINES;
  }

  let wired = false;
  function dialog() {
    const dlg = /** @type {HTMLDialogElement|null} */ (document.getElementById('dlgReader'));
    if (dlg && !wired) {
      wired = true;
      const close = document.getElementById('readerClose');
      if (close) close.addEventListener('click', () => dlg.close());
      // A click on the backdrop is a click on the dialog itself; a click
      // inside lands on a child. Only the former closes.
      dlg.addEventListener('click', (ev) => { if (ev.target === dlg) dlg.close(); });
    }
    return dlg;
  }

  /**
   * Open one entry in full.
   * @param {{text?: string, author_name?: string, author?: string, created_at?: number}} e
   */
  function open(e) {
    const dlg = dialog();
    if (!dlg) return;
    const who = document.getElementById('readerWho');
    const body = document.getElementById('readerBody');
    if (who) {
      const name = e.author_name || e.author || '';
      const when = e.created_at ? new Date(e.created_at * 1000).toLocaleString() : '';
      who.textContent = name && when ? `${name} · ${when}` : name || when;
    }
    if (body) {
      if (typeof MD !== 'undefined' && MD.into) MD.into(body, e.text || '');
      else body.textContent = e.text || '';
      const lang = typeof MD !== 'undefined' && MD.scriptOf ? MD.scriptOf(e.text || '') : '';
      if (lang) body.setAttribute('lang', lang); else body.removeAttribute('lang');
      body.scrollTop = 0;
    }
    dlg.showModal();
  }

  return { isLong, open };
})();

if (typeof window !== 'undefined') window.READER = READER;
