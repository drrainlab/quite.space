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
    'conv.you': 'You',
    'conv.write': 'write into the space…',
    'conv.send': 'send',
    'conv.save_card': 'Save as card',
    'conv.reply': 'reply',
    'conv.reply_hint': 'answer this message and address its author',
    'conv.replying_to': ({ name }) => `replying to ${name}`,
    'conv.cancel_reply': 'cancel reply',
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

    // space pass (UI-2)
    'pass.title': 'Space Pass',
    'pass.invite_from': ({ who }) => `an invitation from ${who}`,
    'pass.card_hint': 'A pass lets someone ask to enter. Your device confirms it — nobody gets in until then.',
    'pass.verify_hint': 'Read this phrase to the person you\'re inviting, so they know the pass is really from you:',
    'pass.terms_one': ({ hours }) => `1 entry · valid ${hours}h`,
    'pass.terms_many': ({ uses, hours }) => `${uses} entries · valid ${hours}h`,
    'pass.relay': 'Rendezvous relay (where the request waits):',
    'pass.relay_ph': 'relay address, e.g. 192.168.1.10:7411',
    'pass.configure': 'Configure',
    'pass.uses': 'Who can enter',
    'pass.uses_1': 'one person',
    'pass.uses_10': 'up to 10',
    'pass.ttl': 'Valid for',
    'pass.ttl_1': '1 hour',
    'pass.ttl_24': '24 hours',
    'pass.ttl_168': '7 days',
    'pass.create': 'Create a pass',
    'pass.copy': 'Copy pass',
    'pass.copied': 'Copied',
    'pass.show_qr': 'Show QR',
    'pass.revoke': 'Revoke',
    'pass.revoked': 'This pass has been revoked. People already inside stay — revoking only stops new entries.',
    'pass.tech_invite': 'Invite a device directly (technical)',
    'pass.need_relay': 'Set a relay in Settings first — that\'s where the entry request waits.',

    // join by pass (async state machine)
    'join.title': 'Enter with a pass',
    'join.paste': 'Paste the pass link someone shared with you:',
    'join.enter': 'Request entry',
    'join.waiting': 'Request sent — waiting for the owner\'s device to confirm your entry. You can close this window.',
    'join.offline': 'The owner\'s device is offline. Your request waits and will be delivered when one of you reconnects.',
    'join.ready': 'Entry confirmed — the space is ready.',
    'join.open': 'Open the space',
    'join.expired': 'This pass has expired. Ask for a new one.',
    'join.expired_waiting': 'The pass expired before the owner\'s device could confirm. Ask for a new pass.',
    'join.revoked': 'This pass was revoked before your entry was confirmed. Ask for a new one.',
    'join.rejected': 'That entry could not be completed. Ask for a new pass.',

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
