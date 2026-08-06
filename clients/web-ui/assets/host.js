'use strict';

// AR-1b.8 — the interface's side of the one channel back to an Android host.
//
// THREE FACTS ONLY THIS SIDE KNOWS, and each one is a specific way the app
// behaves worse than a person expects when it cannot say them:
//
//   which conversation is on screen — or Quiet posts a notification about a
//       message somebody is reading at that moment
//   that a conversation was actually SHOWN — or the notification stays in the
//       shade after every word of it has been read
//   the privacy choice they made — or the strict default is the only setting
//       there is
//
// ABSENT EVERYWHERE ELSE, AND THAT IS NORMAL. On a desktop browser there is no
// `window.QuietHost` and every call here is a no-op. Nothing in the interface
// may depend on the bridge existing: it is a host that happens to be listening,
// not a service the UI is built on.
//
// The token goes with every call because the host demands it — see HostBridge
// for why the origin lock is not considered enough on its own. It is the same
// token this page was served with; it never leaves the page for anywhere else.
const HOST = (() => {
  const bridge = typeof window !== 'undefined' ? window.QuietHost : null;
  const pass = typeof token === 'string' ? token : '';

  // Every call is wrapped: a host that throws — an old build, a method that
  // does not exist yet — must never take the interface down with it. The
  // bridge is an extra, and an extra that can break the page is not one.
  const call = (name, ...args) => {
    if (!bridge || typeof bridge[name] !== 'function') return false;
    try { return bridge[name](pass, ...args); } catch (e) { return false; }
  };

  let lastVisible = undefined;

  return {
    /** True on a host that is listening — used to show host-only settings. */
    present: !!bridge,

    /**
     * The conversation on screen, or null.
     *
     * DEDUPED, because it is called from a poll: repeating the same answer
     * every two seconds would cross the language boundary for nothing.
     */
    visibleSpace(id) {
      const next = id || null;
      if (next === lastVisible) return;
      lastVisible = next;
      call('visibleSpace', next || '');
    },

    /**
     * The messages of this conversation are on the screen NOW.
     *
     * NOT ON A TAP and not on selection: a tap opens a screen that may never
     * finish loading, and clearing the notification there would lose it for a
     * message nobody saw. This is called by the code that has just drawn the
     * messages, and only while the page is actually visible — the WebView
     * keeps polling in the background, and a poll is not a person looking.
     */
    read(id) {
      if (!id) return;
      if (typeof document !== 'undefined' && document.visibilityState !== 'visible') return;
      call('read', id);
    },

    /** hidden | space | preview */
    setNotificationPolicy(name) {
      return call('setNotificationPolicy', name) === true;
    },

    /**
     * AR-1c — the "stay connected" mode.
     *
     * THE ONE CALL HERE WITH A COST. It reaches a foreground service, which
     * Android only lets an app start from a visible screen — so it must be
     * driven by a person pressing something, never by a poll or a page load.
     */
    setStayConnected(on) {
      return call('setStayConnected', !!on) === true;
    },
  };
})();

// The host hands a notification's target here: a space, and possibly the exact
// message. Defined unconditionally — the Activity calls it whether or not this
// page was ready, and a missing function would be a swallowed intent.
window.quietOpen = function quietOpen(target) {
  try {
    const spaceId = target && target.space_id;
    if (!spaceId) return;
    // Switching spaces is the interface's own operation; going to the message
    // is a scroll once the rows exist. Both are attempted, and the scroll is
    // allowed to fail: a message may have been revised away, or be older than
    // what this device holds, and landing in the right conversation is still
    // the right answer.
    current = spaceId;
    refresh().then(() => {
      const mid = target.message_id || target.event_id;
      if (!mid) return;
      const row = document.querySelector(`[data-eid="${CSS.escape(mid)}"]`);
      if (row) row.scrollIntoView({ block: 'center' });
    }).catch(() => {});
  } catch (e) {
    console.warn('quiet: could not open the notification target', e);
  }
};

// Leaving the app is not reading it. The host suppresses notifications for the
// space on screen, so the moment this page stops being on a screen it must say
// so — otherwise a phone in a pocket goes on silently swallowing the
// notifications for whichever conversation was open when it was put away.
if (typeof document !== 'undefined') {
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') {
      if (typeof current === 'string' && current) HOST.visibleSpace(current);
    } else {
      HOST.visibleSpace(null);
    }
  });
}


// ---- the notification-privacy setting (AR-1b.7b) ----
//
// STORED BY THE HOST, NOT BY THIS PAGE. It governs Android's own surfaces and
// belongs to the device that shows them: a person may want previews on the
// phone in their pocket and nothing at all on a tablet in the kitchen, and
// keeping the answer here — in localStorage, per browser profile — would be a
// second copy that can disagree with the one doing the work.
//
// The page therefore never reads it back. It shows what was just chosen and
// says so; on the next launch the host applies its own stored value, which is
// the only one that ever mattered.
let notifChoice = localStorage.getItem('notif.policy.echo') || 'hidden';

function notifPick(name) {
  const msg = document.getElementById('notifMsg');
  if (!HOST.setNotificationPolicy(name)) {
    if (msg) msg.textContent = t('notif.failed');
    return;
  }
  notifChoice = name;
  // An ECHO, not a source of truth: it exists so the tab shows what the person
  // last chose here rather than resetting to the strictest option every time
  // it opens. The host's copy is the one that decides.
  localStorage.setItem('notif.policy.echo', name);
  if (msg) msg.textContent = t('notif.saved');
  notifSyncUI();
}

function notifSyncUI() {
  pickSeg('notifPolicy', notifChoice);
  const what = document.getElementById('notifPolicyWhat');
  if (what) what.textContent = t('notif.' + notifChoice);
}

// The tab exists only where something is listening.
if (typeof document !== 'undefined') {
  document.addEventListener('DOMContentLoaded', () => {
    const tab = document.getElementById('setTabNotifications');
    if (tab && HOST.present) tab.style.display = '';
    notifSyncUI();
    staySyncUI();
  });
}


// ---- the "stay connected" mode (AR-1c) ----
//
// ASKED FOR, NEVER ASSUMED. Holding a connection costs battery, and Android
// makes an app say so with a permanent notification for exactly that reason.
// So the switch starts off, the sentence beside it says what it buys and what
// it costs, and nothing here turns it on by itself.
let stayChoice = localStorage.getItem('stay.connected.echo') === '1';

function stayPick(on) {
  const msg = document.getElementById('stayMsg');
  if (!HOST.setStayConnected(on)) {
    if (msg) msg.textContent = t('stay.failed');
    return;
  }
  stayChoice = !!on;
  localStorage.setItem('stay.connected.echo', stayChoice ? '1' : '0');
  if (msg) msg.textContent = stayChoice ? t('stay.on') : t('stay.off');
  staySyncUI();
}

function staySyncUI() {
  pickSeg('stayMode', stayChoice ? 'on' : 'off');
}
