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

    /**
     * Whether this device opens without asking for the passphrase.
     *
     * The one call here that reads rather than tells, and it is a boolean
     * about the device in front of the person — see HostBridge for why that
     * is not the same as the getters the bridge refuses to have. On an older
     * host without the verb, `call` answers false, which is the safe reading:
     * the setting simply does not appear.
     */
    unlockRemembered() {
      return call('unlockRemembered') === true;
    },

    /** Ask again next launch. */
    forgetPassphrase() {
      return call('forgetPassphrase') === true;
    },

    /**
     * Whether the platform refused to run "stay connected".
     *
     * False on a host that does not know the verb, which is the right answer
     * for one that never refused anything.
     */
    stayRefused() {
      return call('stayRefused') === true;
    },

    /**
     * Ask the phone to SHOW the recovery passphrase — in its own native
     * dialog, on its own screen. Nothing comes back into this page but
     * "the dialog is up": the bridge's doctrine (no getters, ever) is the
     * reason this is a request and not a read. False when the host cannot
     * right now (older build, or the Activity no longer holds the opened
     * key) — the caller says "lock and unlock first" instead of pretending.
     */
    revealPassphrase() {
      return call('revealPassphrase') === true;
    },

    /** Ask the phone to run its native choose-a-code flow. Same shape. */
    changeCode() {
      return call('changeCode') === true;
    },

    // ---- SR-0: Radio Mode + push-to-talk ----
    // The store lives on the host (it must work with the screen off); the
    // page keeps a localStorage echo for instant rendering and the host
    // corrects it through window.quietRadio — the notif.policy.echo shape.
    setRadioMode(spaceId, enabled, autoplay) {
      return call('setRadioMode', spaceId, !!enabled, !!autoplay) === true;
    },
    setRadioBackground(on) {
      return call('setRadioBackground', !!on) === true;
    },
    // The finger. False = not now (no mic permission yet — the host raises
    // the native dialog — or the engine is warming). Everything the page
    // renders afterwards arrives via window.quietPtt pushes.
    pttPress(spaceId) {
      return call('pttPress', spaceId) === true;
    },
    pttRelease() {
      return call('pttRelease') === true;
    },
    pttCancel() {
      return call('pttCancel') === true;
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

// ---- a remembered passphrase ----
//
// The host keeps it so the app opens without asking. The whole reason that is
// defensible is that it can be undone from here — and the row appears only
// where there is actually something to undo.
function unlockSyncUI() {
  const sec = document.getElementById('unlockSec');
  if (!sec) return;
  sec.hidden = !HOST.unlockRemembered();
}

function forgetPassphrase() {
  const msg = document.getElementById('unlockMsg');
  const ok = HOST.forgetPassphrase();
  if (msg) {
    msg.textContent = ok
      ? 'Forgotten. Quiet will ask for it the next time it opens.'
      : 'This device could not forget it — nothing changed.';
  }
  if (ok) unlockSyncUI();
}

// The tab exists only where something is listening.
if (typeof document !== 'undefined') {
  document.addEventListener('DOMContentLoaded', () => {
    const tab = document.getElementById('setTabNotifications');
    if (tab && HOST.present) tab.style.display = '';
    notifSyncUI();
    staySyncUI();
    unlockSyncUI();
  });
}


// ---- the "stay connected" mode (AR-1c) ----
//
// ON BY DEFAULT, AND THE COST IS SAID OUT LOUD. It used to start off, on the
// argument that holding a connection spends somebody's battery on a decision
// they did not make. What that argument missed is that the mode is not only
// about receiving: media bytes never travel to the relay, so a phone with
// this off takes every photo it has ever sent offline with it, and the person
// on the other side is left looking at a picture that never arrives with no
// way to tell why. A default that quietly breaks other people's screens is
// not the conservative choice it looks like.
//
// So it starts on, Android shows the permanent notice it requires, the
// sentence beside the switch says what it buys and what it costs, and turning
// it off is the first thing in the tab.
//
// THIS IS AN ECHO, NOT THE SETTING. The host owns the mode; this mirrors it
// so the segmented control is painted correctly before any call is made, and
// its default has to match the host's (RuntimeController.availabilityRequested).
let stayChoice = (localStorage.getItem('stay.connected.echo') ?? '1') === '1';

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
  // THE HOST IS THE TRUTH; THIS IS AN ECHO, and a refusal is where the two
  // come apart. Pressing "Stay connected" succeeds here — the bridge answers
  // before the service has tried to start — and the platform may refuse it a
  // moment later, which turns the mode off over there while this side still
  // remembers a yes. The control would then show "Stay connected" over a mode
  // that is not running, which is the one thing a switch must never do.
  const refused = HOST.stayRefused();
  if (refused && stayChoice) {
    stayChoice = false;
    localStorage.setItem('stay.connected.echo', '0');
  }
  pickSeg('stayMode', stayChoice ? 'on' : 'off');
  // WHO PUT IT THERE. A refused start turns the mode off so nothing retries in
  // a loop, and that leaves a control sitting in a position the person did not
  // choose, reading as their own decision or as a setting that does nothing.
  // Said only while it is true — the next successful start clears it — so it
  // describes now, not a bad afternoon last week.
  const msg = document.getElementById('stayMsg');
  if (!msg) return;
  if (refused) msg.textContent = t('stay.refused');
  // Only a refusal is an alarm. The ordinary "On."/"Off." this element also
  // carries is a result, and colouring those would cry wolf on every press.
  msg.classList.toggle('alarm', refused);
}
