// i18n foundation: every user-facing string flows through t(key, vars).
// Plural + interpolation + Intl relative time; no string concatenation of
// independent keys (so the DOM never bakes in English sentence structure).
// EN is the source locale; a RU catalog drops in later without touching code.

const I18N = {
  en: {
    // shell / connection
    'app.name': 'quiet spaces',
    'conn.direct': 'Direct',
    'conn.lan': 'Local network',
    'conn.mesh': 'Mesh',
    'conn.relay': 'Through a relay',
    'conn.syncing': 'Catching up',
    'conn.offline': 'No connection right now',
    'conn.off': 'Not connected',
    'conn.here': ({ count }) => `${count} ${plural(count, 'person', 'people')} here`,
    'conn.summary': ({ state, count }) => `${state} · ${count} here`,
    'conn.details': 'Connection details',
    'conn.mesh_node': ({ node }) => `mesh · node ${node}`,

    // spaces list
    'spaces.new': '+ space',
    'spaces.join': 'join',
    'spaces.me': 'me',
    'spaces.owner': 'owner',
    'spaces.empty.title': "It's quiet here so far.",
    'spaces.empty.body': 'Create your own space, or enter with a pass.',
    'spaces.activity.events': ({ count }) => `${count} ${plural(count, 'moment', 'moments')}`,
    'spaces.unread': ({ count }) => `${count} new`,

    // conversation
    'conv.member': 'member',
    'conv.write': 'write into the space…',
    'conv.send': 'send',
    'conv.save_card': 'Save as card',
    'conv.edited': 'edited',
    'entry.unsupported': 'unsupported content',

    // system events (human)
    'sys.joined_with_pass': ({ who, owner }) => `${who} entered with ${owner}'s pass`,
    'sys.joined': ({ who }) => `${who} joined`,
    'sys.connection_changed': 'Connection changed',
    'sys.connection_changed_body': 'Messages will continue over the local network.',

    // honesty
    'honesty.undecryptable': ({ count }) =>
      `${count} ${plural(count, 'moment', 'moments')} you can't read here yet (no key — shown, not hidden)`,
    'honesty.simulated': 'SIMULATED',

    // presence
    'presence.here': 'here',
    'presence.away': 'away briefly',
    'presence.listening': 'listening',
    'presence.recording': 'recording',
    'presence.unknown': 'presence unknown',
    'presence.last_seen': ({ who, ago }) => `${who} was here ${ago}`,
    'presence.is_here': ({ who }) => `${who} is here`,
    'presence.pick': 'presence…',

    // device
    'device.title': 'This device',
    'device.trusted': 'Trusted device',
    'device.verification': 'Verification code',
    'device.add': 'Add another device',
    'device.security': 'Security',
    'device.technical': 'Technical details',

    // protocol view
    'protocol.toggle': 'Protocol view',
    'protocol.on': 'Protocol view is on',
    'protocol.off': 'Protocol view is off',

    // generic
    'action.close': 'close',
    'action.cancel': 'cancel',
    'action.share': 'Share',
    'action.copy': 'Copy link',
    'action.open': 'Open',
    'action.retry': 'Retry',
    'time.now': 'just now',
  },
};

let LOCALE = 'en';

function plural(n, one, many) { return n === 1 ? one : many; }

// t(key, vars) — resolves against the active locale, falls back to the key
// itself in dev so a missing string is loud, not silent.
function t(key, vars) {
  const cat = I18N[LOCALE] || I18N.en;
  let entry = cat[key];
  if (entry === undefined) entry = I18N.en[key];
  if (entry === undefined) { console.warn('i18n: missing key', key); return key; }
  return typeof entry === 'function' ? entry(vars || {}) : entry;
}

// Relative time via Intl — never hand-rolled "N minutes ago" strings.
const RTF = new Intl.RelativeTimeFormat(LOCALE === 'en' ? 'en' : LOCALE, { numeric: 'auto' });
function relTime(seconds) {
  if (seconds < 45) return t('time.now');
  if (seconds < 5400) return RTF.format(-Math.round(seconds / 60), 'minute');
  if (seconds < 86400) return RTF.format(-Math.round(seconds / 3600), 'hour');
  return RTF.format(-Math.round(seconds / 86400), 'day');
}
